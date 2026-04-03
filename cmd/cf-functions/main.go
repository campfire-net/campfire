// cmd/cf-functions/main.go — Azure Functions custom handler for cf-mcp.
//
// Wraps the cf-mcp HTTP server so it can run as an Azure Functions custom
// handler (Functions v4). The Functions host starts this binary and forwards
// HTTP requests to it on FUNCTIONS_CUSTOMHANDLER_PORT.
//
// Architecture: cf-functions starts cf-mcp as a child process on a local
// ephemeral port, validates connectivity, then proxies Azure Functions
// requests to it. This is a true thin adapter — all MCP logic stays in cf-mcp.
//
// Routes served (Azure Functions path prefix /api):
//
//	GET  /api/health       → own health handler (checks child process liveness)
//	POST /api/mcp          → proxied to cf-mcp /mcp
//	ANY  /api/mcp/*        → proxied to cf-mcp /mcp/*
//	GET  /api/sse          → proxied to cf-mcp /sse
//	ANY  /api/campfire/*   → proxied to cf-mcp /campfire/*
//
// Configuration (env vars only):
//
//	FUNCTIONS_CUSTOMHANDLER_PORT     listen port assigned by Functions host (required)
//	AZURE_STORAGE_CONNECTION_STRING  validate Azure Table Storage at startup; passed through to cf-mcp
//	CF_SESSION_TOKEN                 key wrapping token (passed to cf-mcp via CF_SESSION_TOKEN)
//	CF_DOMAIN                        public domain for external address (sets CF_EXTERNAL_URL on cf-mcp)
//	CF_SESSIONS_DIR                  override sessions directory (default: $TMPDIR/cf-sessions)
//	CF_MCP_BIN                       path to cf-mcp binary (default: same dir as this binary, then PATH)
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/campfire-net/campfire/pkg/ratelimit"
	"github.com/campfire-net/campfire/pkg/store/aztable"
	"github.com/campfire-net/campfire/pkg/x402"
)

// Version is set at build time via ldflags.
var Version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "cf-functions: fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// -------------------------------------------------------------------------
	// Config from env vars (only config source per bead spec).
	// -------------------------------------------------------------------------
	port := os.Getenv("FUNCTIONS_CUSTOMHANDLER_PORT")
	if port == "" {
		port = "8080"
	}
	listenAddr := ":" + port

	azConnStr := os.Getenv("AZURE_STORAGE_CONNECTION_STRING")
	cfDomain := os.Getenv("CF_DOMAIN")
	sessionsDir := os.Getenv("CF_SESSIONS_DIR")
	if sessionsDir == "" {
		sessionsDir = filepath.Join(os.TempDir(), "cf-sessions")
	}
	cfMCPBin := os.Getenv("CF_MCP_BIN")
	if cfMCPBin == "" {
		// Look next to ourselves first.
		self, err := os.Executable()
		if err == nil {
			candidate := filepath.Join(filepath.Dir(self), "cf-mcp")
			if _, err := os.Stat(candidate); err == nil {
				cfMCPBin = candidate
			}
		}
	}
	if cfMCPBin == "" {
		// Fall back to PATH.
		p, err := exec.LookPath("cf-mcp")
		if err == nil {
			cfMCPBin = p
		}
	}
	if cfMCPBin == "" {
		return errors.New("cf-mcp binary not found: set CF_MCP_BIN or place cf-mcp next to cf-functions")
	}

	// External address for peer-to-peer HTTP transport.
	externalAddr := cfDomain
	if externalAddr != "" && !strings.HasPrefix(externalAddr, "http") {
		externalAddr = "https://" + externalAddr
	}

	// -------------------------------------------------------------------------
	// Validate Azure Table Storage connectivity at startup.
	// -------------------------------------------------------------------------
	if azConnStr != "" {
		ts, err := aztable.NewTableStore(azConnStr)
		if err != nil {
			return fmt.Errorf("Azure Table Storage connection failed: %w", err)
		}
		if err := ts.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "cf-functions: warning: closing aztable probe: %v\n", err)
		}
		fmt.Fprintln(os.Stderr, "cf-functions: Azure Table Storage connectivity validated")
	}

	// -------------------------------------------------------------------------
	// Pick a local port for the cf-mcp child process.
	// -------------------------------------------------------------------------
	childPort, err := freePort()
	if err != nil {
		return fmt.Errorf("finding free port for cf-mcp: %w", err)
	}
	childAddr := fmt.Sprintf("127.0.0.1:%d", childPort)

	// -------------------------------------------------------------------------
	// Build cf-mcp environment: inherit current env, override config vars.
	// -------------------------------------------------------------------------
	childEnv := inheritEnv(
		"CF_EXTERNAL_URL", externalAddr,
		"CF_SESSIONS_DIR", sessionsDir,
	)
	if azConnStr != "" {
		// cf-mcp reads AZURE_STORAGE_CONNECTION_STRING directly for aztable backend.
		childEnv = setEnv(childEnv, "AZURE_STORAGE_CONNECTION_STRING", azConnStr)
	}

	// -------------------------------------------------------------------------
	// Start cf-mcp child with --http and --sessions-dir flags.
	// -------------------------------------------------------------------------
	childArgs := []string{
		"--http=" + childAddr,
		"--sessions-dir=" + sessionsDir,
	}
	if externalAddr != "" {
		childArgs = append(childArgs, "--external-addr="+externalAddr)
	}

	child := exec.Command(cfMCPBin, childArgs...)
	child.Env = childEnv
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr

	if err := child.Start(); err != nil {
		return fmt.Errorf("starting cf-mcp: %w", err)
	}
	fmt.Fprintf(os.Stderr, "cf-functions: started cf-mcp pid=%d on %s\n", child.Process.Pid, childAddr)

	// Wait for cf-mcp to become ready (up to 10s).
	childBaseURL, err := waitReady(childAddr, 10*time.Second)
	if err != nil {
		_ = child.Process.Kill()
		return fmt.Errorf("cf-mcp did not become ready: %w", err)
	}

	// -------------------------------------------------------------------------
	// Set up reverse proxy.
	// -------------------------------------------------------------------------
	proxy := httputil.NewSingleHostReverseProxy(childBaseURL)
	// Strip /api prefix before forwarding.
	proxy.Director = func(r *http.Request) {
		r.URL.Scheme = childBaseURL.Scheme
		r.URL.Host = childBaseURL.Host
		// /api/mcp → /mcp, /api/campfire/... → /campfire/...
		path := r.URL.Path
		if strings.HasPrefix(path, "/api/") {
			path = "/" + strings.TrimPrefix(path, "/api/")
		}
		r.URL.Path = path
		if r.URL.RawPath != "" {
			rawPath := r.URL.RawPath
			if strings.HasPrefix(rawPath, "/api/") {
				rawPath = "/" + strings.TrimPrefix(rawPath, "/api/")
			}
			r.URL.RawPath = rawPath
		}
		r.Host = childBaseURL.Host
	}

	// -------------------------------------------------------------------------
	// Rate limiter and x402 payment handler.
	// The rate limiter wraps an in-memory no-op store here — the actual
	// store lives inside cf-mcp. The limiter here is used only to track
	// per-campfire caps for the payment endpoint and to serve 402 challenges
	// when the proxy returns a 402 from the child process.
	// -------------------------------------------------------------------------
	limiter := ratelimit.New(nil, ratelimit.Config{})

	// Determine the payment URL from CF_DOMAIN (same env used for external addr).
	paymentURL := ""
	if cfDomain != "" {
		base := cfDomain
		if !strings.HasPrefix(base, "http") {
			base = "https://" + base
		}
		paymentURL = base + "/api/payment"
	}

	// Wrap the proxy so that 402 responses from cf-mcp are converted into
	// structured x402 payment challenges.
	proxyWithChallenge := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Use a response recorder to inspect the upstream status.
		rec := &statusRecorder{ResponseWriter: w}
		proxy.ServeHTTP(rec, r)
		// If cf-mcp returned 402 and we haven't written the body yet, rewrite.
		if rec.status == http.StatusPaymentRequired && !rec.written {
			x402.ChallengeFromError(w, ratelimit.ErrMonthlyCapExceeded, paymentURL)
		}
	})
	// -------------------------------------------------------------------------
	// -------------------------------------------------------------------------
	// Timer-triggered fallback sweep handler.
	// Azure Functions timer triggers send a POST to /{functionName}. The handler
	// forwards the request to cf-mcp's /sweep endpoint. This catches messages
	// that the event-driven dispatch path missed.
	// -------------------------------------------------------------------------
	sweepHandler := buildSweepHandler(childBaseURL)

	// -------------------------------------------------------------------------
	// HTTP mux: /api/health and /api/payment are own; everything else proxied.
	// Timer trigger routes (no /api prefix) are handled directly.
	// -------------------------------------------------------------------------
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		handleHealth(w, r, child)
	})
	mux.Handle("/api/payment", x402.NewPaymentHandler(x402.StubVerifier{}, limiter))
	mux.Handle("/api/mcp", proxyWithChallenge)
	mux.Handle("/api/mcp/", proxyWithChallenge)
	mux.Handle("/api/sse", proxyWithChallenge)
	mux.Handle("/api/campfire/", proxyWithChallenge)
	mux.HandleFunc("/sweep", sweepHandler)

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      65 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	fmt.Fprintf(os.Stderr, "cf-functions listening on %s (cf-mcp on %s)\n", listenAddr, childAddr)

	// -------------------------------------------------------------------------
	// Graceful shutdown on signal.
	// -------------------------------------------------------------------------
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "cf-functions: shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	// Reap child when parent exits.
	defer func() {
		if child.Process != nil {
			_ = child.Process.Kill()
		}
	}()

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("server: %w", err)
	}
	return nil
}

// handleHealth serves GET /api/health. Returns 200 if the cf-mcp child is
// still running, 503 otherwise.
func handleHealth(w http.ResponseWriter, r *http.Request, child *exec.Cmd) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	alive := child.ProcessState == nil // nil means not yet exited
	w.Header().Set("Content-Type", "application/json")
	if alive {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok","version":%q}`, Version)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"status":"degraded","version":%q}`, Version)
	}
}

// freePort finds an available TCP port on loopback.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port, nil
}

// waitReady polls the child's /health endpoint until it responds 200 or the
// timeout elapses. Returns the base URL on success.
func waitReady(addr string, timeout time.Duration) (*url.URL, error) {
	baseURL := &url.URL{Scheme: "http", Host: addr}
	healthURL := baseURL.String() + "/health"

	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	for time.Now().Before(deadline) {
		resp, err := client.Get(healthURL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return baseURL, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("timeout waiting for cf-mcp at %s", addr)
}

// inheritEnv returns os.Environ() with the given key=value overrides applied.
// Pairs must be (key, value, key, value, ...).
func inheritEnv(pairs ...string) []string {
	env := os.Environ()
	for i := 0; i+1 < len(pairs); i += 2 {
		env = setEnv(env, pairs[i], pairs[i+1])
	}
	return env
}

// statusRecorder wraps http.ResponseWriter to capture the status code written
// by a downstream handler without forwarding the response body prematurely.
// It is used to inspect 402 responses from the cf-mcp proxy before deciding
// whether to rewrite them as x402 payment challenges.
//
// NOTE: statusRecorder only intercepts WriteHeader — the body bytes written
// by the proxy are still forwarded to the underlying ResponseWriter. The
// ChallengeFromError call in the proxy wrapper is therefore only reached when
// the upstream handler did NOT write a body (i.e. returned an empty 402).
// In the common case where cf-mcp writes its own 402 body, that body is
// forwarded and written is set to true so ChallengeFromError is skipped.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.written = r.written || len(b) > 0
	return r.ResponseWriter.Write(b)
}

// buildSweepHandler returns an HTTP handler that forwards timer trigger requests
// to cf-mcp's /sweep endpoint. Azure Functions timer triggers POST to /{functionName}
// with a JSON body containing timer metadata. We forward as a POST to cf-mcp /sweep,
// which runs the fallback dispatch sweep and returns results.
//
// The handler returns 200 to the Functions host regardless of sweep outcome (the
// Functions runtime treats non-2xx as a trigger failure and retries). Actual errors
// are logged to stderr where Azure Application Insights picks them up.
func buildSweepHandler(childBaseURL *url.URL) http.HandlerFunc {
	client := &http.Client{Timeout: 60 * time.Second}
	sweepURL := childBaseURL.String() + "/sweep"

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, sweepURL, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cf-functions: sweep: build request: %v\n", err)
			w.WriteHeader(http.StatusOK) // return 200 to avoid Functions retry
			return
		}

		resp, err := client.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cf-functions: sweep: POST to cf-mcp: %v\n", err)
			w.WriteHeader(http.StatusOK) // return 200 to avoid Functions retry
			return
		}
		defer resp.Body.Close()

		// Forward the response from cf-mcp to the Functions host.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		buf := make([]byte, 4096)
		for {
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				w.Write(buf[:n])
			}
			if readErr != nil {
				break
			}
		}
	}
}

// setEnv sets key=value in env, replacing any existing entry for key.
func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			if value == "" {
				// Remove the entry.
				return append(env[:i], env[i+1:]...)
			}
			env[i] = key + "=" + value
			return env
		}
	}
	if value != "" {
		env = append(env, key+"="+value)
	}
	return env
}
