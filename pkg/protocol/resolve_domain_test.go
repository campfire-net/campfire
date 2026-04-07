package protocol

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestResolveNamingValue_HexPassthrough verifies that a 64-hex campfire ID
// passes through unchanged with no warnings.
func TestResolveNamingValue_HexPassthrough(t *testing.T) {
	hex := "0b8c2cbd58dcbca4c53caf1970f14e80548996e1d9024632461f776b00394d88"
	id, warn, err := resolveNamingValue(hex)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != hex {
		t.Errorf("got %q, want %q", id, hex)
	}
	if warn != "" {
		t.Errorf("expected no warning for hex passthrough, got %q", warn)
	}
}

// TestResolveNamingValue_Empty verifies that empty strings are rejected.
func TestResolveNamingValue_Empty(t *testing.T) {
	_, _, err := resolveNamingValue("")
	if err == nil {
		t.Fatal("expected error for empty value")
	}
}

// TestResolveNamingValue_Whitespace verifies that whitespace-only strings are rejected.
func TestResolveNamingValue_Whitespace(t *testing.T) {
	_, _, err := resolveNamingValue("   ")
	if err == nil {
		t.Fatal("expected error for whitespace value")
	}
}

// TestResolveNamingValue_HexWithWhitespace verifies trimming works.
func TestResolveNamingValue_HexWithWhitespace(t *testing.T) {
	hex := "0b8c2cbd58dcbca4c53caf1970f14e80548996e1d9024632461f776b00394d88"
	id, _, err := resolveNamingValue("  " + hex + "  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != hex {
		t.Errorf("got %q, want %q", id, hex)
	}
}

// TestResolveNamingValue_DomainResolution verifies that a domain triggers
// beacon fetch and returns the campfire ID with a warning.
func TestResolveNamingValue_DomainResolution(t *testing.T) {
	expectedID := "0b8c2cbd58dcbca4c53caf1970f14e80548996e1d9024632461f776b00394d88"

	origFetch := domainBeaconFetchFn
	domainBeaconFetchFn = func(domain string) (string, error) {
		if domain != "example.com" {
			t.Errorf("unexpected domain %q", domain)
		}
		return expectedID, nil
	}
	defer func() { domainBeaconFetchFn = origFetch }()

	id, warn, err := resolveNamingValue("example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != expectedID {
		t.Errorf("got %q, want %q", id, expectedID)
	}
	if !strings.Contains(warn, "resolved domain") {
		t.Errorf("expected domain resolution warning, got %q", warn)
	}
}

// TestResolveNamingValue_InvalidDomain verifies that localhost is rejected.
func TestResolveNamingValue_InvalidDomain(t *testing.T) {
	_, _, err := resolveNamingValue("localhost")
	if err == nil {
		t.Fatal("expected error for localhost")
	}
}

// --- Domain validation tests ---

func TestValidateDomain_Localhost(t *testing.T) {
	for _, d := range []string{"localhost", "sub.localhost", "LOCALHOST"} {
		if err := validateDomain(d); err == nil {
			t.Errorf("expected error for domain %q", d)
		}
	}
}

func TestValidateDomain_IPAddresses(t *testing.T) {
	for _, d := range []string{"127.0.0.1", "192.168.1.1", "10.0.0.1", "::1", "[::1]", "0.0.0.0"} {
		if err := validateDomain(d); err == nil {
			t.Errorf("expected error for IP %q", d)
		}
	}
}

func TestValidateDomain_InternalDomains(t *testing.T) {
	for _, d := range []string{"myhost.local", "service.internal"} {
		if err := validateDomain(d); err == nil {
			t.Errorf("expected error for internal domain %q", d)
		}
	}
}

func TestValidateDomain_BareHostname(t *testing.T) {
	if err := validateDomain("myhost"); err == nil {
		t.Error("expected error for bare hostname")
	}
}

func TestValidateDomain_ValidDomains(t *testing.T) {
	for _, d := range []string{"getcampfire.dev", "example.com", "sub.domain.org"} {
		if err := validateDomain(d); err != nil {
			t.Errorf("unexpected error for %q: %v", d, err)
		}
	}
}

func TestValidateDomain_SchemeRejected(t *testing.T) {
	if err := validateDomain("https://example.com"); err == nil {
		t.Error("expected error for domain with scheme")
	}
}

func TestValidateDomain_PortRejected(t *testing.T) {
	if err := validateDomain("example.com:8080"); err == nil {
		t.Error("expected error for domain with port")
	}
}

func TestValidateDomain_PathRejected(t *testing.T) {
	if err := validateDomain("example.com/path"); err == nil {
		t.Error("expected error for domain with path")
	}
}

func TestValidateDomain_Empty(t *testing.T) {
	if err := validateDomain(""); err == nil {
		t.Error("expected error for empty domain")
	}
}

// --- fetchDomainBeacon tests (using httptest TLS servers) ---

func newTestBeaconServer(t *testing.T, campfireID string) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/campfire" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"campfire_id": campfireID,
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func withTestHTTPClient(t *testing.T, srv *httptest.Server) func() {
	t.Helper()
	origClient := domainResolverHTTPClient
	domainResolverHTTPClient = srv.Client()
	return func() { domainResolverHTTPClient = origClient }
}

func domainFromServer(srv *httptest.Server) string {
	return strings.TrimPrefix(srv.URL, "https://")
}

func TestFetchDomainBeacon_WithEd25519Prefix(t *testing.T) {
	expectedID := "0b8c2cbd58dcbca4c53caf1970f14e80548996e1d9024632461f776b00394d88"
	srv := newTestBeaconServer(t, "ed25519:"+expectedID)
	defer withTestHTTPClient(t, srv)()

	id, err := fetchDomainBeacon(domainFromServer(srv))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != expectedID {
		t.Errorf("got %q, want %q", id, expectedID)
	}
}

func TestFetchDomainBeacon_RawHex(t *testing.T) {
	expectedID := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	srv := newTestBeaconServer(t, expectedID)
	defer withTestHTTPClient(t, srv)()

	id, err := fetchDomainBeacon(domainFromServer(srv))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != expectedID {
		t.Errorf("got %q, want %q", id, expectedID)
	}
}

func TestFetchDomainBeacon_Timeout(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	origClient := domainResolverHTTPClient
	shortClient := srv.Client()
	shortClient.Timeout = 100 * time.Millisecond
	domainResolverHTTPClient = shortClient
	defer func() { domainResolverHTTPClient = origClient }()

	_, err := fetchDomainBeacon(domainFromServer(srv))
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestFetchDomainBeacon_InvalidJSON(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	t.Cleanup(srv.Close)
	defer withTestHTTPClient(t, srv)()

	_, err := fetchDomainBeacon(domainFromServer(srv))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestFetchDomainBeacon_MissingCampfireID(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"description": "no id"})
	}))
	t.Cleanup(srv.Close)
	defer withTestHTTPClient(t, srv)()

	_, err := fetchDomainBeacon(domainFromServer(srv))
	if err == nil {
		t.Fatal("expected error for missing campfire_id")
	}
}

func TestFetchDomainBeacon_HTTPError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	defer withTestHTTPClient(t, srv)()

	_, err := fetchDomainBeacon(domainFromServer(srv))
	if err == nil {
		t.Fatal("expected error for HTTP 404")
	}
}

func TestFetchDomainBeacon_InvalidHexInResponse(t *testing.T) {
	srv := newTestBeaconServer(t, "not-a-valid-hex")
	defer withTestHTTPClient(t, srv)()

	_, err := fetchDomainBeacon(domainFromServer(srv))
	if err == nil {
		t.Fatal("expected error for invalid campfire_id hex")
	}
}

// --- resolveNamingConfig tests ---

// withMockFetch overrides domainBeaconFetchFn to return a fixed ID for any domain.
func withMockFetch(t *testing.T, returnID string) func() {
	t.Helper()
	origFetch := domainBeaconFetchFn
	domainBeaconFetchFn = func(domain string) (string, error) {
		return returnID, nil
	}
	return func() { domainBeaconFetchFn = origFetch }
}

func TestResolveNamingConfig_MixedValues(t *testing.T) {
	expectedID := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	hexID := "0b8c2cbd58dcbca4c53caf1970f14e80548996e1d9024632461f776b00394d88"

	defer withMockFetch(t, expectedID)()

	cfg := &NamingConfig{
		Root:  hexID,
		Seeds: []string{"partner.dev"},
	}

	warnings, err := resolveNamingConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Root != hexID {
		t.Errorf("root should be unchanged hex, got %q", cfg.Root)
	}
	if cfg.Seeds[0] != expectedID {
		t.Errorf("seed[0]: got %q, want %q", cfg.Seeds[0], expectedID)
	}
	if len(warnings) != 1 {
		t.Errorf("expected 1 warning for domain resolution, got %d: %v", len(warnings), warnings)
	}
}

func TestResolveNamingConfig_EmptyConfig(t *testing.T) {
	cfg := &NamingConfig{}
	warnings, err := resolveNamingConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got %v", warnings)
	}
}

func TestResolveNamingConfig_DomainRoot(t *testing.T) {
	expectedID := "0b8c2cbd58dcbca4c53caf1970f14e80548996e1d9024632461f776b00394d88"
	defer withMockFetch(t, expectedID)()

	cfg := &NamingConfig{
		Root: "getcampfire.dev",
	}

	warnings, err := resolveNamingConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Root != expectedID {
		t.Errorf("root: got %q, want %q", cfg.Root, expectedID)
	}
	if len(warnings) != 1 {
		t.Errorf("expected 1 warning, got %d: %v", len(warnings), warnings)
	}
}

func TestResolveNamingConfig_MultipleSeeds(t *testing.T) {
	expectedID := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	hexID := "0b8c2cbd58dcbca4c53caf1970f14e80548996e1d9024632461f776b00394d88"

	defer withMockFetch(t, expectedID)()

	cfg := &NamingConfig{
		Seeds: []string{hexID, "partner.dev", "other.org"},
	}

	warnings, err := resolveNamingConfig(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Seeds[0] != hexID {
		t.Errorf("seed[0]: should be unchanged hex, got %q", cfg.Seeds[0])
	}
	if cfg.Seeds[1] != expectedID {
		t.Errorf("seed[1]: got %q, want %q", cfg.Seeds[1], expectedID)
	}
	if cfg.Seeds[2] != expectedID {
		t.Errorf("seed[2]: got %q, want %q", cfg.Seeds[2], expectedID)
	}
	if len(warnings) != 2 {
		t.Errorf("expected 2 warnings, got %d: %v", len(warnings), warnings)
	}
}

// --- Integration test: InitWithConfig with domain naming.root ---

func TestInitWithConfig_DomainNamingRoot(t *testing.T) {
	expectedID := "0b8c2cbd58dcbca4c53caf1970f14e80548996e1d9024632461f776b00394d88"

	// Override the fetch function to avoid real network calls.
	origFetch := domainBeaconFetchFn
	domainBeaconFetchFn = func(domain string) (string, error) {
		if domain != "3dl.dev" {
			t.Errorf("unexpected domain %q", domain)
		}
		return expectedID, nil
	}
	defer func() { domainBeaconFetchFn = origFetch }()

	// Set up a temp config directory with a global config.
	tmp := t.TempDir()
	globalDir := tmp

	configContent := "[naming]\nroot = \"3dl.dev\"\nseeds = [\"3dl.dev\"]\n"
	writeConfig(t, filepath.Join(globalDir, configFilename), configContent)

	// Override ownerTrustedFn for test.
	origOwner := ownerTrustedFn
	ownerTrustedFn = func(string) bool { return true }
	defer func() { ownerTrustedFn = origOwner }()

	// Override home dir to use our temp dir.
	origHome := userHomeDirFn
	userHomeDirFn = func() (string, error) { return tmp, nil }
	defer func() { userHomeDirFn = origHome }()

	// Change to the temp dir so CWD-based config loading works.
	origWd, _ := os.Getwd()
	os.Chdir(tmp)
	defer os.Chdir(origWd)

	client, result, err := InitWithConfig(WithConfigDir(globalDir))
	if err != nil {
		t.Fatalf("InitWithConfig failed: %v", err)
	}
	defer client.Close()

	// Verify warnings contain domain resolution info.
	foundRootWarning := false
	foundSeedWarning := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "resolved domain") && strings.Contains(w, expectedID) {
			if strings.Contains(w, "3dl.dev") {
				foundRootWarning = true
				foundSeedWarning = true
			}
		}
	}
	if !foundRootWarning || !foundSeedWarning {
		t.Errorf("expected domain resolution warnings in result.Warnings, got %v", result.Warnings)
	}
}

// TestInitWithConfig_HexNamingRoot verifies that hex IDs still work without regression.
func TestInitWithConfig_HexNamingRoot(t *testing.T) {
	hexID := "0b8c2cbd58dcbca4c53caf1970f14e80548996e1d9024632461f776b00394d88"

	tmp := t.TempDir()
	globalDir := tmp

	configContent := "[naming]\nroot = \"" + hexID + "\"\n"
	writeConfig(t, filepath.Join(globalDir, configFilename), configContent)

	origOwner := ownerTrustedFn
	ownerTrustedFn = func(string) bool { return true }
	defer func() { ownerTrustedFn = origOwner }()

	origHome := userHomeDirFn
	userHomeDirFn = func() (string, error) { return tmp, nil }
	defer func() { userHomeDirFn = origHome }()

	origWd, _ := os.Getwd()
	os.Chdir(tmp)
	defer os.Chdir(origWd)

	client, result, err := InitWithConfig(WithConfigDir(globalDir))
	if err != nil {
		t.Fatalf("InitWithConfig failed: %v", err)
	}
	defer client.Close()

	// No domain resolution warnings should be present.
	for _, w := range result.Warnings {
		if strings.Contains(w, "resolved domain") {
			t.Errorf("unexpected domain resolution warning for hex ID: %q", w)
		}
	}
}
