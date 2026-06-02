//go:build azurite

// Cloud-selection test. Gated behind the `azurite` build tag to mirror the
// aztable Azurite suite (pkg/store/aztable/azurite_test.go): it requires a live
// Azurite Table Storage emulator on 127.0.0.1:10002.
//
// Run with: go test -tags azurite ./pkg/storage/...
// Start Azurite first:
//   docker run -p 10000:10000 -p 10001:10001 -p 10002:10002 \
//     mcr.microsoft.com/azure-storage/azurite
package storage_test

import (
	"testing"

	"github.com/campfire-net/campfire/pkg/storage"
)

// azuriteConnStr is the well-known Azurite development Table Storage connection
// string (public, non-secret).
const azuriteConnStr = "DefaultEndpointsProtocol=http;" +
	"AccountName=devstoreaccount1;" +
	"AccountKey=Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==;" +
	"TableEndpoint=http://127.0.0.1:10002/devstoreaccount1;"

// TestFactorySelectsCloudWhenConnStringSet verifies that a non-empty
// connection string drives the factory down the CloudStorage branch over a
// real aztable TableStore. Ground-source: a real aztable store is opened
// against the Azurite emulator, not a mock.
func TestFactorySelectsCloudWhenConnStringSet(t *testing.T) {
	cfg := storage.Config{
		ConnectionString: azuriteConnStr,
		LocalPath:        "", // ignored on cloud branch
	}
	st, err := storage.Open(cfg)
	if err != nil {
		t.Fatalf("Open(cloud): %v", err)
	}
	defer st.Close()

	if st.Backend() != storage.BackendCloud {
		t.Fatalf("Backend() = %q, want %q", st.Backend(), storage.BackendCloud)
	}

	// MembershipExists forwards to the authoritative aztable store: not a member.
	exists, err := st.MembershipExists("nonexistent-campfire")
	if err != nil {
		t.Fatalf("MembershipExists: %v", err)
	}
	if exists {
		t.Fatalf("MembershipExists on empty cloud store = true, want false")
	}
}
