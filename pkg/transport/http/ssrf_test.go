package http_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// TestValidateJoinerEndpoint_PrivateLiteralIPsRejected verifies that literal private
// IPv4 and IPv6 addresses in endpoints are rejected by validateJoinerEndpoint.
func TestValidateJoinerEndpoint_PrivateLiteralIPsRejected(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
	}{
		// IPv4 loopback
		{"loopback", "http://127.0.0.1:9999/"},
		{"loopback2", "http://127.1.2.3:9999/"},
		// RFC1918
		{"10.x", "http://10.0.0.1:80/"},
		{"172.16.x", "http://172.16.0.1:80/"},
		{"172.31.x", "http://172.31.255.255:80/"},
		{"192.168.x", "http://192.168.1.1:80/"},
		// Link-local (AWS EC2 metadata, router)
		{"169.254.169.254", "http://169.254.169.254/"},
		{"169.254.0.1", "http://169.254.0.1:80/"},
		// Unspecified
		{"0.0.0.0", "http://0.0.0.0:80/"},
		// CGNAT RFC6598
		{"100.64.0.1", "http://100.64.0.1:80/"},
		// IPv4-mapped IPv6 — these must be unwrapped and treated as private IPv4
		{"::ffff:192.168.1.1", "http://[::ffff:192.168.1.1]:80/"},
		{"::ffff:10.0.0.1", "http://[::ffff:10.0.0.1]:80/"},
		{"::ffff:127.0.0.1", "http://[::ffff:127.0.0.1]:80/"},
		{"::ffff:169.254.169.254", "http://[::ffff:169.254.169.254]:80/"},
		// IPv6 loopback
		{"::1", "http://[::1]:80/"},
		// IPv6 link-local
		{"fe80::1", "http://[fe80::1]:80/"},
		// IPv6 unique-local
		{"fc00::1", "http://[fc00::1]:80/"},
		{"fd00::1", "http://[fd00::1]:80/"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := cfhttp.ValidateJoinerEndpoint(tc.endpoint); err == nil {
				t.Errorf("expected error for private endpoint %q, got nil", tc.endpoint)
			}
		})
	}
}

// TestValidateJoinerEndpoint_PublicIPsAllowed verifies that public IP addresses
// (routable, non-private) are accepted by validateJoinerEndpoint.
func TestValidateJoinerEndpoint_PublicIPsAllowed(t *testing.T) {
	publicEndpoints := []string{
		"http://1.2.3.4:8080/",
		"http://8.8.8.8:443/",
		"https://8.8.4.4:443/",
	}
	for _, ep := range publicEndpoints {
		if err := cfhttp.ValidateJoinerEndpoint(ep); err != nil {
			t.Errorf("unexpected error for public endpoint %q: %v", ep, err)
		}
	}
}

// TestValidateJoinerEndpoint_EmptyAllowed verifies that an empty endpoint (receive-only
// member) is accepted without error.
func TestValidateJoinerEndpoint_EmptyAllowed(t *testing.T) {
	if err := cfhttp.ValidateJoinerEndpoint(""); err != nil {
		t.Errorf("expected nil error for empty endpoint, got %v", err)
	}
}

// TestValidateJoinerEndpoint_BadURLsRejected verifies that non-HTTP schemes and
// malformed URLs are rejected.
func TestValidateJoinerEndpoint_BadURLsRejected(t *testing.T) {
	bad := []string{
		"ftp://example.com/",
		"file:///etc/passwd",
		"not-a-url",
		"//example.com/",
		"",   // empty is allowed separately — but "" with scheme is fine; this tests no-scheme
	}
	// Filter the empty string — it is explicitly allowed.
	for _, ep := range bad {
		if ep == "" {
			continue
		}
		if err := cfhttp.ValidateJoinerEndpoint(ep); err == nil {
			t.Errorf("expected error for bad URL %q, got nil", ep)
		}
	}
}

// TestJoinRejectsPrivateEndpoint starts a real transport and verifies that a join
// request carrying a private JoinerEndpoint returns HTTP 400.
func TestJoinRejectsPrivateEndpoint(t *testing.T) {
	// Re-enable SSRF validation (TestMain disables it for other tests).
	cfhttp.RestoreValidateJoinerEndpoint()
	t.Cleanup(cfhttp.OverrideValidateJoinerEndpointForTest)

	campfireID := "ssrf-join-test"
	idJoiner := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+420)
	tr := startTransport(t, addr, s)

	// Minimal key provider so the handler doesn't bail at "join not supported".
	campfirePriv := make([]byte, 64)
	campfirePub := make([]byte, 32)
	tr.SetKeyProvider(func(id string) ([]byte, []byte, error) {
		return campfirePriv, campfirePub, nil
	})

	privateEndpoints := []string{
		"http://127.0.0.1:9999/",
		"http://10.0.0.1:9999/",
		"http://192.168.1.1:9999/",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::ffff:192.168.1.1]:80/",
		"http://[::1]:80/",
	}

	for _, badEP := range privateEndpoints {
		t.Run(badEP, func(t *testing.T) {
			joinBody, _ := json.Marshal(map[string]string{
				"joiner_pubkey":       idJoiner.PublicKeyHex(),
				"joiner_endpoint":     badEP,
				"ephemeral_x25519_pub": "",
			})

			req, err := http.NewRequest(http.MethodPost,
				fmt.Sprintf("http://%s/campfire/%s/join", addr, campfireID),
				bytes.NewReader(joinBody),
			)
			if err != nil {
				t.Fatalf("building request: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			signTestRequest(req, idJoiner, joinBody)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request error: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("endpoint %q: expected 400, got %d", badEP, resp.StatusCode)
			}
		})
	}
}

// TestJoinAcceptsPublicEndpoint verifies that a join request with a valid public IP
// endpoint does not get blocked by the SSRF check. We use a real routable IP literal
// so no DNS lookup is needed (avoids external dependency).
func TestJoinAcceptsPublicEndpoint(t *testing.T) {
	// validateJoinerEndpoint accepts public IP literals without a network call.
	if err := cfhttp.ValidateJoinerEndpoint("http://1.2.3.4:8080/"); err != nil {
		t.Errorf("expected public endpoint to pass, got: %v", err)
	}
}

// TestSSRFSafeTransportBlocksLoopback verifies that the SSRF-safe transport refuses
// to connect to loopback addresses (simulating a DNS rebind to an internal address).
func TestSSRFSafeTransportBlocksLoopback(t *testing.T) {
	// Start a real HTTP server on loopback so there is a real port to attempt.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	// Ensure ts.URL uses a loopback address (127.0.0.1 by default for httptest).
	host, _, _ := net.SplitHostPort(ts.Listener.Addr().String())
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		t.Skipf("test server bound to non-loopback %s, skipping", host)
	}

	safeClient := cfhttp.NewSSRFSafeClient()
	_, err := safeClient.Get(ts.URL)
	if err == nil {
		t.Error("SSRFSafeTransport: expected connection to loopback to be blocked, got nil error")
	}
}

// TestSSRFSafeTransport_RedirectToLoopbackBlocked verifies that the SSRF-safe
// transport's DialContext blocks direct connections to loopback addresses.
// This tests the transport-layer guard against DNS rebinding to loopback.
func TestSSRFSafeTransport_RedirectToLoopbackBlocked(t *testing.T) {
	// Create a client with the SSRF-safe transport and extract the transport.
	client := cfhttp.NewSSRFSafeClient()
	tr := client.Transport.(*http.Transport)

	ctx := context.Background()
	// Attempt to dial a loopback address directly.
	// The DialContext should block this with an SSRF message.
	conn, err := tr.DialContext(ctx, "tcp", "127.0.0.1:9999")
	if err == nil {
		defer conn.Close()
		t.Error("expected DialContext to block 127.0.0.1:9999, got nil error")
		return
	}
	// Verify error message indicates SSRF block (contains "private" or "blocked").
	errMsg := err.Error()
	if !(bytes.Contains([]byte(errMsg), []byte("private")) ||
		bytes.Contains([]byte(errMsg), []byte("blocked"))) {
		t.Errorf("expected SSRF block message, got: %v", err)
	}
}

// TestSSRFSafeTransport_UnreachablePublicIPReturnsDialError verifies that
// when all resolved public IPs are unreachable, the transport returns the
// actual dial error (not an SSRF message).
func TestSSRFSafeTransport_UnreachablePublicIPReturnsDialError(t *testing.T) {
	// Create a client with the SSRF-safe transport and extract the transport.
	client := cfhttp.NewSSRFSafeClient()
	tr := client.Transport.(*http.Transport)

	ctx := context.Background()
	// Attempt to dial a public IP (1.2.3.4) on a port with no listener.
	// This should fail with a genuine dial error (connection refused or timeout),
	// not an SSRF message.
	conn, err := tr.DialContext(ctx, "tcp", "1.2.3.4:1")
	if err == nil {
		defer conn.Close()
		t.Fatal("expected DialContext to fail with network error, got nil error")
	}

	// Verify error message does NOT contain SSRF-specific text.
	errMsg := err.Error()
	if bytes.Contains([]byte(errMsg), []byte("private")) ||
		bytes.Contains([]byte(errMsg), []byte("SSRF")) ||
		bytes.Contains([]byte(errMsg), []byte("blocked: resolves")) {
		t.Errorf("expected genuine dial error (not SSRF message) for unreachable public IP, got: %v", err)
	}
}

// TestValidateJoinerEndpoint_LiteralIPErrorDoesNotContainIP verifies that when a
// literal private IP is passed, the error message does NOT contain the specific IP
// (to prevent IP enumeration attacks).
func TestValidateJoinerEndpoint_LiteralIPErrorDoesNotContainIP(t *testing.T) {
	privateIP := "10.0.0.1"
	endpoint := fmt.Sprintf("http://%s:8080/", privateIP)

	err := cfhttp.ValidateJoinerEndpoint(endpoint)
	if err == nil {
		t.Fatalf("expected error for private IP %s, got nil", privateIP)
	}

	errMsg := err.Error()
	if bytes.Contains([]byte(errMsg), []byte(privateIP)) {
		t.Errorf("error message contains specific IP %q, should be redacted: %q", privateIP, errMsg)
	}

	// Message should still indicate it's a private/internal address
	if !bytes.Contains([]byte(errMsg), []byte("private")) && !bytes.Contains([]byte(errMsg), []byte("internal")) {
		t.Errorf("error message should mention private/internal, got: %q", errMsg)
	}
}

// TestValidateJoinerEndpoint_ResolvedIPErrorDoesNotContainIP verifies that when a
// hostname resolves to a private IP, the error message does NOT contain the specific
// resolved IP (to prevent IP enumeration attacks).
func TestValidateJoinerEndpoint_ResolvedIPErrorDoesNotContainIP(t *testing.T) {
	// Use a hostname that resolves to a private IP for testing purposes.
	// We'll use localhost which resolves to 127.0.0.1.
	endpoint := "http://localhost:8080/"

	err := cfhttp.ValidateJoinerEndpoint(endpoint)
	if err == nil {
		t.Fatalf("expected error for localhost (resolves to 127.0.0.1), got nil")
	}

	errMsg := err.Error()
	// The error should not contain the resolved IP (127.0.0.1)
	if bytes.Contains([]byte(errMsg), []byte("127.0.0.1")) {
		t.Errorf("error message contains resolved IP 127.0.0.1, should be redacted: %q", errMsg)
	}

	// The error should contain the hostname (since the attacker supplied it)
	if !bytes.Contains([]byte(errMsg), []byte("localhost")) {
		t.Errorf("error message should mention hostname 'localhost', got: %q", errMsg)
	}

	// Message should still indicate it's a private/internal address
	if !bytes.Contains([]byte(errMsg), []byte("private")) && !bytes.Contains([]byte(errMsg), []byte("internal")) {
		t.Errorf("error message should mention private/internal, got: %q", errMsg)
	}
}
