package cmd

// Shared test helpers for cmd/cf/cmd tests.
// Extracted from membership_role_test.go when that file was removed (duplicate coverage
// moved to pkg/protocol/ tests). setupCampfireWithRole is still used by
// admit_leave_disband_dm_test.go.

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	cfencoding "github.com/campfire-net/campfire/cf-protocol/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/cf-protocol/message"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
)

// newTestMessage is a test helper that creates a signed message using the
// Ed25519 keypair directly. It wraps message.NewEd25519Signer + message.NewMessage
// so test code can remain concise after the Signer API migration.
func newTestMessage(t *testing.T, priv ed25519.PrivateKey, pub ed25519.PublicKey, payload []byte, tags []string, antecedents []string) *message.Message {
	t.Helper()
	signer, err := message.NewEd25519Signer(priv, pub)
	if err != nil {
		t.Fatalf("NewEd25519Signer: %v", err)
	}
	msg, err := message.NewMessage(signer, payload, tags, antecedents)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	return msg
}

// setupCampfireWithRole creates a campfire and adds the agent as a member with the
// given protocol role in both the transport directory and the local store.
func setupCampfireWithRole(t *testing.T, agentID *identity.Identity, s store.Store, transportBaseDir string, protocolRole string) string {
	t.Helper()

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}

	campfireID := cfID.PublicKeyHex()
	cfDir := filepath.Join(transportBaseDir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0755); err != nil {
			t.Fatalf("creating directory %s: %v", sub, err)
		}
	}

	state := &campfire.CampfireState{
		PublicKey:             cfID.PublicKey,
		PrivateKey:            cfID.PrivateKey,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		CreatedAt:             time.Now().UnixNano(),
	}
	stateData, err := cfencoding.Marshal(state)
	if err != nil {
		t.Fatalf("marshalling campfire state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfDir, "campfire.cbor"), stateData, 0644); err != nil {
		t.Fatalf("writing campfire state: %v", err)
	}

	transport := fs.New(transportBaseDir)
	if err := transport.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      protocolRole,
	}); err != nil {
		t.Fatalf("writing member record: %v", err)
	}

	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: transport.CampfireDir(campfireID),
		JoinProtocol: "open",
		Role:         protocolRole,
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	return campfireID
}
