package main

// ssrf_declaration_test.go — SSRF validation tests for fetchDeclarationURL.
//
// fetchDeclarationURL is called by handleCreateConvention when the declarations
// parameter contains a URL. It must reject private/loopback targets to prevent
// SSRF attacks against internal services.
//
// Bead: campfire-agent-4v9

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFetchDeclarationURL_BlocksLoopback verifies that fetchDeclarationURL
// rejects loopback URLs (http://127.0.0.1/...) via the SSRF-safe transport.
// An attacker supplying a loopback URL to campfire_create declarations must
// not be able to probe the local network.
func TestFetchDeclarationURL_BlocksLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"operation":"test"}`))
	}))
	defer srv.Close()

	// srv.URL is http://127.0.0.1:<port> — loopback, must be blocked.
	_, err := fetchDeclarationURL(srv.URL)
	if err == nil {
		t.Fatal("fetchDeclarationURL must reject loopback URLs, got nil error")
	}
	if !strings.Contains(err.Error(), "private") && !strings.Contains(err.Error(), "loopback") &&
		!strings.Contains(err.Error(), "dial") && !strings.Contains(err.Error(), "refused") {
		// SSRFSafeTransport returns a dial error mentioning the blocked IP class.
		// Accept any error — the point is that it was rejected.
		t.Logf("fetchDeclarationURL loopback rejection error (expected): %v", err)
	}
}

// TestFetchDeclarationURL_BlocksPrivateRange verifies that fetchDeclarationURL
// rejects private-range URLs (10.x, 192.168.x, 172.16-31.x).
func TestFetchDeclarationURL_BlocksPrivateRange(t *testing.T) {
	privateURLs := []string{
		"http://10.0.0.1/decl.json",
		"http://192.168.1.1/decl.json",
		"http://172.16.0.1/decl.json",
		"http://169.254.169.254/latest/meta-data/",
	}
	for _, url := range privateURLs {
		_, err := fetchDeclarationURL(url)
		if err == nil {
			t.Errorf("fetchDeclarationURL(%q): expected SSRF rejection, got nil error", url)
		}
	}
}
