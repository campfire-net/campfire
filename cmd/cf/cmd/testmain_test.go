package cmd

import (
	"net/http"
	"os"
	"testing"

	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

func TestMain(m *testing.M) {
	// Override poll transport to allow loopback connections in tests.
	cfhttp.OverridePollTransportForTest(http.DefaultTransport)
	os.Exit(m.Run())
}
