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

	mux := buildMux(logger)

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", "err", err)
	}
}

// buildMux constructs the HTTP router and returns it. Extracted for testability.
// logger may be nil, in which case a default text-handler logger is used.
func buildMux(logger *slog.Logger) http.Handler {
	return buildMuxWithAuth(logger, nil)
}

// buildMuxWithAuth constructs the HTTP router with an explicit AuthConfig.
// If authCfg is nil, a default in-memory config with noopAuthProvider is created.
// Extracted so tests can inject a custom AuthConfig (e.g. pointing at a fake GitHub server).
func buildMuxWithAuth(logger *slog.Logger, authCfg *AuthConfig) http.Handler {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	if authCfg == nil {
		authCfg = newAuthConfig(logger, os.Getenv, os.Getenv("BASE_URL"), NewMemSessionStore(), noopAuthProvider{})
	}

	mux := http.NewServeMux()

	// Health endpoint.
	mux.HandleFunc("GET /healthz", handleHealthz)

	// Static assets (CSS, JS, etc.) embedded from static/.
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticFS)))

	// Auth routes.
	registerAuthRoutes(mux, authCfg)

	// UI routes.
	mux.HandleFunc("GET /", handleIndex(logger))
	mux.HandleFunc("GET /c/{id}", handleCampfireDetail(logger))

	return mux
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
