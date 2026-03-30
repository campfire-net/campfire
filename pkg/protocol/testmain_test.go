package protocol_test

import (
	"net/http"
	"testing"
	"time"

	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

func TestMain(m *testing.M) {
	// Override the SSRF-safe HTTP client and endpoint validator so that
	// in-process HTTP servers on loopback addresses work in tests.
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})
	cfhttp.OverrideValidateJoinerEndpointForTest()
	m.Run()
}
