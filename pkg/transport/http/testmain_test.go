package http_test

// TestMain is the entry point for the transport/http test suite.
// It overrides the package-level HTTP client with an unprotected client so that
// tests can make outbound calls to loopback servers (the SSRF-safe transport blocks
// loopback by design, which is correct for production but breaks tests).
//
// The SSRF-safe transport is tested separately in ssrf_test.go via NewSSRFSafeClient().

import (
	"net/http"
	"os"
	"testing"
	"time"

	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

func TestMain(m *testing.M) {
	// Replace the production SSRF-safe httpClient with the standard client for tests.
	// This allows test servers on loopback (127.0.0.1) to be reached normally.
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 30 * time.Second})
	cfhttp.OverrideValidateJoinerEndpointForTest()
	os.Exit(m.Run())
}
