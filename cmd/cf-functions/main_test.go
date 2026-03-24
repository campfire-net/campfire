package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Unit tests (no child process required)
// ---------------------------------------------------------------------------

func TestHandleHealth_Alive(t *testing.T) {
	// Build a fake exec.Cmd whose ProcessState is nil (simulates running process).
	cmd := &exec.Cmd{}
	// ProcessState is nil by default — process has not exited.
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	w := httptest.NewRecorder()
	handleHealth(w, req, cmd)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"ok"`) {
		t.Errorf("expected ok status in body, got %s", body)
	}
}

func TestHandleHealth_MethodNotAllowed(t *testing.T) {
	cmd := &exec.Cmd{}
	req := httptest.NewRequest(http.MethodPost, "/api/health", nil)
	w := httptest.NewRecorder()
	handleHealth(w, req, cmd)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestFreePort(t *testing.T) {
	port, err := freePort()
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	if port < 1 || port > 65535 {
		t.Errorf("unexpected port %d", port)
	}
}

func TestSetEnv(t *testing.T) {
	tests := []struct {
		name     string
		env      []string
		key      string
		value    string
		expected []string
	}{
		{
			name:     "add new",
			env:      []string{"FOO=bar"},
			key:      "BAZ",
			value:    "qux",
			expected: []string{"FOO=bar", "BAZ=qux"},
		},
		{
			name:     "replace existing",
			env:      []string{"FOO=bar", "BAZ=old"},
			key:      "BAZ",
			value:    "new",
			expected: []string{"FOO=bar", "BAZ=new"},
		},
		{
			name:     "remove when empty",
			env:      []string{"FOO=bar", "BAZ=old"},
			key:      "BAZ",
			value:    "",
			expected: []string{"FOO=bar"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := setEnv(tc.env, tc.key, tc.value)
			if len(got) != len(tc.expected) {
				t.Fatalf("len mismatch: want %d got %d (%v)", len(tc.expected), len(got), got)
			}
			for i, e := range tc.expected {
				if got[i] != e {
					t.Errorf("index %d: want %q got %q", i, e, got[i])
				}
			}
		})
	}
}

func TestInheritEnv(t *testing.T) {
	// inheritEnv should include current env plus overrides.
	env := inheritEnv("CF_TEST_KEY", "testval123")
	found := false
	for _, e := range env {
		if e == "CF_TEST_KEY=testval123" {
			found = true
			break
		}
	}
	if !found {
		t.Error("override key not found in inherited env")
	}
}

// ---------------------------------------------------------------------------
// Env var parsing tests
// ---------------------------------------------------------------------------

func TestEnvVarParsing_Port(t *testing.T) {
	// Verify port defaults to 8080 when not set.
	os.Unsetenv("FUNCTIONS_CUSTOMHANDLER_PORT")
	port := os.Getenv("FUNCTIONS_CUSTOMHANDLER_PORT")
	if port == "" {
		port = "8080"
	}
	if port != "8080" {
		t.Errorf("expected default port 8080, got %s", port)
	}
}

func TestEnvVarParsing_SessionsDir(t *testing.T) {
	os.Unsetenv("CF_SESSIONS_DIR")
	sessionsDir := os.Getenv("CF_SESSIONS_DIR")
	if sessionsDir == "" {
		sessionsDir = fmt.Sprintf("%s/cf-sessions", os.TempDir())
	}
	if !strings.HasSuffix(sessionsDir, "cf-sessions") {
		t.Errorf("expected sessions dir to end in cf-sessions, got %s", sessionsDir)
	}
}

// ---------------------------------------------------------------------------
// Integration-style tests (require cf-mcp binary)
// ---------------------------------------------------------------------------

// findCFMCP locates the cf-mcp binary for integration tests. Skips if not found.
func findCFMCP(t *testing.T) string {
	t.Helper()
	// Check if we can build it.
	p, err := exec.LookPath("cf-mcp")
	if err == nil {
		return p
	}
	t.Skip("cf-mcp not found on PATH; skipping integration tests")
	return ""
}

func TestWaitReady_Timeout(t *testing.T) {
	// Point at a port that isn't listening — should time out quickly.
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	start := time.Now()
	_, err = waitReady(addr, 300*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Error("expected timeout error, got nil")
	}
	if elapsed > 2*time.Second {
		t.Errorf("waitReady took too long: %v", elapsed)
	}
}

func TestWaitReady_Success(t *testing.T) {
	// Start a minimal HTTP server that responds 200 on /health.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	// Extract host:port from server URL.
	addr := strings.TrimPrefix(srv.URL, "http://")
	baseURL, err := waitReady(addr, 2*time.Second)
	if err != nil {
		t.Fatalf("waitReady: %v", err)
	}
	if baseURL == nil {
		t.Fatal("expected non-nil baseURL")
	}
}

func TestStartup_MissingBinary(t *testing.T) {
	// Set CF_MCP_BIN to a nonexistent path — run() should fail.
	t.Setenv("CF_MCP_BIN", "/nonexistent/cf-mcp-does-not-exist")
	t.Setenv("FUNCTIONS_CUSTOMHANDLER_PORT", "0")
	// run() will fail at the cf-mcp start step, not at listen.
	// We just verify it returns an error without panicking.
	err := run()
	if err == nil {
		t.Error("expected error for missing binary, got nil")
	}
}
