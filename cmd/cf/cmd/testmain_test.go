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

	// Override relay URL validation to allow loopback relay endpoints in tests.
	cfhttp.OverrideValidateRelayURLForTest()

	// Isolate beacon directory so tests never scan real beacons on the host.
	// Some machines have 100K+ beacon files; scanning them causes test timeouts.
	// Also isolate the working directory so projectBeaconDir() doesn't find
	// the real .campfire/beacons/ in the repo root.
	if os.Getenv("CF_BEACON_DIR") == "" {
		tmpBeacon, err := os.MkdirTemp("", "cf-test-beacons-*")
		if err == nil {
			os.Setenv("CF_BEACON_DIR", tmpBeacon)
			defer os.RemoveAll(tmpBeacon)
		}
	}
	origDir, _ := os.Getwd()
	tmpWork, err := os.MkdirTemp("", "cf-test-workdir-*")
	if err == nil {
		os.Chdir(tmpWork) //nolint:errcheck
		defer func() {
			os.Chdir(origDir) //nolint:errcheck
			os.RemoveAll(tmpWork)
		}()
	}

	os.Exit(m.Run())
}
