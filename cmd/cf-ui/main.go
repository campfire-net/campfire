// cmd/cf-ui/main.go — Campfire web UI server
//
// Serves server-rendered HTML enhanced with htmx for the hosted campfire
// service. Runs on Azure Container Apps.
//
// Usage:
//
//	cf-ui [--addr <addr>]
//
// Environment:
//
//	PORT — listening port (default 8080)
package main

import (
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// noopAuthProvider is the default AuthProvider used when no real campfire store
// is wired. It generates opaque random tokens and is a no-op for invalidation.
// A real implementation will be wired by the session middleware bead (campfire-agent-pd3).
type noopAuthProvider struct{}

func (noopAuthProvider) CreateSession(_ context.Context, _ Identity) (string, error) {
	return randomToken(32)
}

func (noopAuthProvider) InvalidateSession(_ context.Context, _ string) error { return nil }

// Version is set at build time via ldflags.
var Version = "dev"

//go:embed templates/* static/*
var assets embed.FS

// templateFS is the sub-filesystem rooted at "templates".
var templateFS fs.FS

// staticFS is the sub-filesystem rooted at "static".
var staticFS fs.FS

// templates holds all parsed page templates.
var templates *template.Template

func init() {
	var err error
	templateFS, err = fs.Sub(assets, "templates")
	if err != nil {
		panic("cf-ui: failed to sub templates FS: " + err.Error())
	}
	staticFS, err = fs.Sub(assets, "static")
	if err != nil {
		panic("cf-ui: failed to sub static FS: " + err.Error())
	}
	templates, err = template.New("").ParseFS(templateFS, "*.html")
	if err != nil {
		panic("cf-ui: failed to parse templates: " + err.Error())
	}
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := net.JoinHostPort("", port)

	bundle := buildMuxWithAuth(logger, nil)

	srv := &http.Server{
		Addr:         addr,
		Handler:      bundle.handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown on SIGINT / SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("cf-ui starting", "addr", addr, "version", Version)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("cf-ui shutting down")

	// Signal all SSE connections to close before HTTP shutdown drains them.
	bundle.hub.Shutdown()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", "err", err)
	}
}

// muxBundle bundles the HTTP handler with the AuthConfig, CSRF store, SSE hub,
// and metrics registry so tests can access them to seed sessions, generate CSRF
// tokens, publish events, and inspect metric values.
type muxBundle struct {
	handler   http.Handler
	authCfg   *AuthConfig
	csrfStore *csrfStore
	hub       *SSEHub
	metrics   *MetricsRegistry
}

// buildMux constructs the HTTP router and returns it. Extracted for testability.
// logger may be nil, in which case a default text-handler logger is used.
func buildMux(logger *slog.Logger) http.Handler {
	return buildMuxWithAuth(logger, nil).handler
}

// muxBundle bundles the HTTP handler with the AuthConfig, CSRF store, and SSE hub
// so tests can access them to seed sessions, generate CSRF tokens, and publish events.

// buildMuxWithAuth constructs the HTTP router with an explicit AuthConfig.
// If authCfg is nil, a default in-memory config with noopAuthProvider is created.
// Extracted so tests can inject a custom AuthConfig (e.g. pointing at a fake GitHub server).
// Returns a muxBundle containing the handler plus references to the auth/CSRF state.
func buildMuxWithAuth(logger *slog.Logger, authCfg *AuthConfig) muxBundle {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	if authCfg == nil {
		authCfg = newAuthConfig(logger, os.Getenv, os.Getenv("BASE_URL"), NewMemSessionStore(), noopAuthProvider{})
	}

	csrf, err := newCSRFStore()
	if err != nil {
		// This can only fail if the OS random source is broken — treat as fatal.
		panic("cf-ui: failed to initialize CSRF store: " + err.Error())
	}

	reg := NewMetricsRegistry()

	hub := NewSSEHub(authCfg.Sessions, logger).WithMetrics(reg)

	sessionMW := SessionMiddleware(authCfg.Sessions)
	csrfMW := CSRFMiddleware(csrf)

	mux := http.NewServeMux()

	// Public routes — no session or CSRF middleware.
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticFS)))

	// Metrics endpoint — public (infrastructure monitoring, no session required).
	mux.HandleFunc("GET /metrics", handleMetrics(reg))

	// Auth routes — public (session middleware must NOT wrap /auth/* because
	// unauthenticated users need to reach the login endpoints).
	// The magic-link POST route gets CSRF protection only.
	registerAuthRoutes(mux, authCfg, csrfMW)

	// Protected UI routes — require a valid session.
	mux.Handle("GET /", sessionMW(handleIndex(logger)))
	mux.Handle("GET /c/{id}", sessionMW(handleCampfireDetail(logger)))

	// SSE events stream — requires a valid session. Session middleware injects
	// the Identity; the hub handler enforces the connection budget.
	mux.Handle("GET /events", sessionMW(handleEventsHandler(hub)))

	// Wrap the entire mux with the latency middleware (outermost layer).
	handler := LatencyMiddleware(reg)(mux)

	return muxBundle{handler: handler, authCfg: authCfg, csrfStore: csrf, hub: hub, metrics: reg}
}

// handleHealthz returns a JSON health response.
func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"}) //nolint:errcheck
}

// handleIndex renders the campfire list page.
func handleIndex(logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		data := struct {
			Title   string
			Version string
		}{
			Title:   "Campfire",
			Version: Version,
		}
		if err := templates.ExecuteTemplate(w, "index.html", data); err != nil {
			logger.Error("template error", "template", "index.html", "err", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}
	}
}

// handleCampfireDetail renders the campfire detail page for a given ID.
func handleCampfireDetail(logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		data := struct {
			Title       string
			CampfireID  string
			Version     string
		}{
			Title:      "Campfire — " + id,
			CampfireID: id,
			Version:    Version,
		}
		if err := templates.ExecuteTemplate(w, "campfire.html", data); err != nil {
			logger.Error("template error", "template", "campfire.html", "err", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}
	}
}
