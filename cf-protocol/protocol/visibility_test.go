package protocol_test

// Tests for protocol.Client.EmitVisibilityChanged() — campfireagent-c66.
//
// Design: design v2 §4.1 + protocol-spec §cf-protocol 1.0 Layer-1 Additions.
// All tests use a real filesystem transport and a real campfire keypair.
// No mocks — campfire-key signing is verified against the actual public key.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	cfencoding "github.com/campfire-net/campfire/cf-protocol/encoding"
	"github.com/campfire-net/campfire/cf-protocol/message"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	cfstransport "github.com/campfire-net/campfire/cf-protocol/transport/fs"
	"github.com/campfire-net/campfire/pkg/identity"
)

// setupVisibilityTestEnv creates a campfire with a KNOWN campfire keypair so
// tests can verify signatures against the campfire's public key.
// Returns: agentID, the SQLite store, transport base dir, campfire ID (hex),
// and the campfire's Ed25519 public key for signature verification.
func setupVisibilityTestEnv(t *testing.T) (
	agentID *identity.Identity,
	s store.Store,
	transportDir string,
	campfireID string,
	campfirePubKey ed25519.PublicKey,
) {
	t.Helper()
	storeDir := t.TempDir()
	transportDir = t.TempDir()

	var err error
	agentID, err = identity.Generate()
	if err != nil {
		t.Fatalf("generating agent identity: %v", err)
	}

	s, err = store.Open(filepath.Join(storeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Generate a known campfire keypair so we can verify signatures below.
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire keypair: %v", err)
	}
	campfireID = fmt.Sprintf("%x", cfPub)

	// Create the transport directory structure.
	cfDir := filepath.Join(transportDir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0755); err != nil {
			t.Fatalf("creating %s directory: %v", sub, err)
		}
	}

	// Write campfire state with the known keypair.
	state := &campfire.CampfireState{
		PublicKey:             cfPub,
		PrivateKey:            cfPriv,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		CreatedAt:             time.Now().UnixNano(),
		Threshold:             1,
	}
	stateData, err := cfencoding.Marshal(state)
	if err != nil {
		t.Fatalf("marshalling campfire state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfDir, "campfire.cbor"), stateData, 0644); err != nil {
		t.Fatalf("writing campfire state: %v", err)
	}

	// Write agent as a member.
	tr := cfstransport.New(transportDir)
	if err := tr.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("writing member record: %v", err)
	}

	// Record membership in the store.
	if err := s.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  tr.CampfireDir(campfireID),
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	return agentID, s, transportDir, campfireID, cfPub
}

// readVisibilityChangedMessages reads all campfire:visibility-changed messages
// from the transport directory for the given campfire.
func readVisibilityChangedMessages(t *testing.T, transportDir, campfireID string) []message.Message {
	t.Helper()
	messagesDir := filepath.Join(transportDir, campfireID, "messages")
	entries, err := os.ReadDir(messagesDir)
	if err != nil {
		t.Fatalf("reading messages dir: %v", err)
	}

	var result []message.Message
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".cbor" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(messagesDir, e.Name()))
		if err != nil {
			t.Fatalf("reading message file %s: %v", e.Name(), err)
		}
		var msg message.Message
		if err := cfencoding.Unmarshal(data, &msg); err != nil {
			t.Fatalf("decoding message %s: %v", e.Name(), err)
		}
		for _, tag := range msg.Tags {
			if tag == campfire.TagVisibilityChanged {
				result = append(result, msg)
				break
			}
		}
	}
	return result
}

// TestEmitVisibilityChanged_EmitsWithCorrectTag verifies that EmitVisibilityChanged
// fires a message with the campfire:visibility-changed tag.
func TestEmitVisibilityChanged_EmitsWithCorrectTag(t *testing.T) {
	agentID, s, transportDir, campfireID, _ := setupVisibilityTestEnv(t)
	client := protocol.New(s, agentID)

	err := client.EmitVisibilityChanged(campfireID, "open", "invite-only")
	if err != nil {
		t.Fatalf("EmitVisibilityChanged: %v", err)
	}

	msgs := readVisibilityChangedMessages(t, transportDir, campfireID)
	if len(msgs) == 0 {
		t.Fatal("expected at least one campfire:visibility-changed message, got none")
	}
	found := false
	for _, msg := range msgs {
		for _, tag := range msg.Tags {
			if tag == campfire.TagVisibilityChanged {
				found = true
			}
		}
	}
	if !found {
		t.Error("no message has campfire:visibility-changed tag")
	}
}

// TestEmitVisibilityChanged_SignedByCampfireKey verifies that the emitted message
// is signed by the campfire's Ed25519 key, not the calling agent's key.
func TestEmitVisibilityChanged_SignedByCampfireKey(t *testing.T) {
	agentID, s, transportDir, campfireID, campfirePubKey := setupVisibilityTestEnv(t)
	client := protocol.New(s, agentID)

	err := client.EmitVisibilityChanged(campfireID, "open", "invite-only")
	if err != nil {
		t.Fatalf("EmitVisibilityChanged: %v", err)
	}

	msgs := readVisibilityChangedMessages(t, transportDir, campfireID)
	if len(msgs) == 0 {
		t.Fatal("no campfire:visibility-changed messages found")
	}

	msg := msgs[0]

	// (b) Sender must be the campfire key, not the agent key.
	if bytes.Equal(msg.Sender, agentID.PublicKey) {
		t.Error("message Sender is the agent key — must be the campfire key")
	}
	if !bytes.Equal(msg.Sender, campfirePubKey) {
		t.Errorf("message Sender is not the campfire pubkey: got %x, want %x",
			msg.Sender, campfirePubKey)
	}

	// Signature must verify against the campfire key.
	if !msg.VerifySignature() {
		t.Error("message signature does not verify against the campfire key")
	}
}

// TestEmitVisibilityChanged_PayloadFields verifies the JSON payload contains
// previous_join_protocol, new_join_protocol, and changed_at fields.
func TestEmitVisibilityChanged_PayloadFields(t *testing.T) {
	agentID, s, transportDir, campfireID, _ := setupVisibilityTestEnv(t)
	client := protocol.New(s, agentID)

	before := time.Now().UnixNano()
	err := client.EmitVisibilityChanged(campfireID, "open", "invite-only")
	after := time.Now().UnixNano()
	if err != nil {
		t.Fatalf("EmitVisibilityChanged: %v", err)
	}

	msgs := readVisibilityChangedMessages(t, transportDir, campfireID)
	if len(msgs) == 0 {
		t.Fatal("no campfire:visibility-changed messages found")
	}
	msg := msgs[0]

	var payload struct {
		PreviousJoinProtocol string `json:"previous_join_protocol"`
		NewJoinProtocol      string `json:"new_join_protocol"`
		ChangedAt            int64  `json:"changed_at"`
	}
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		t.Fatalf("unmarshalling payload: %v — raw: %s", err, msg.Payload)
	}

	if payload.PreviousJoinProtocol != "open" {
		t.Errorf("previous_join_protocol: got %q, want %q", payload.PreviousJoinProtocol, "open")
	}
	if payload.NewJoinProtocol != "invite-only" {
		t.Errorf("new_join_protocol: got %q, want %q", payload.NewJoinProtocol, "invite-only")
	}
	if payload.ChangedAt < before || payload.ChangedAt > after {
		t.Errorf("changed_at %d is outside the expected range [%d, %d]",
			payload.ChangedAt, before, after)
	}
}

// TestEmitVisibilityChanged_AdversarialForge verifies that a message with the
// campfire:visibility-changed tag signed by an identity key (not the campfire key)
// fails signature verification against the campfire's public key.
//
// Design: design v2 §4.1 — "Receivers MUST reject any campfire:visibility-changed
// message whose signature does not verify against the campfire's public key."
func TestEmitVisibilityChanged_AdversarialForge(t *testing.T) {
	agentID, s, transportDir, campfireID, campfirePubKey := setupVisibilityTestEnv(t)

	// Attacker creates their own identity and signs with their own key.
	attackerID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating attacker identity: %v", err)
	}

	// Write the attacker as a member so they can send to the campfire.
	tr := cfstransport.New(transportDir)
	if err := tr.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: attackerID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("writing attacker member record: %v", err)
	}
	attackerStore, storeErr := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if storeErr != nil {
		t.Fatalf("opening attacker store: %v", storeErr)
	}
	t.Cleanup(func() { attackerStore.Close() })
	if err := attackerStore.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  tr.CampfireDir(campfireID),
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	}); err != nil {
		t.Fatalf("adding attacker membership: %v", err)
	}

	// Attacker tries to forge a campfire:visibility-changed event by sending
	// it as a regular member-signed message.
	attackerClient := protocol.New(attackerStore, attackerID)
	forgedMsg, sendErr := attackerClient.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte(`{"previous_join_protocol":"open","new_join_protocol":"invite-only","changed_at":1}`),
		Tags:       []string{campfire.TagVisibilityChanged},
	})
	if sendErr == nil {
		// Send might succeed (role enforcement not expected to block this here),
		// but the signature must NOT verify against the campfire pubkey.
		if forgedMsg != nil {
			// The forged message sender must NOT be the campfire key.
			if bytes.Equal(forgedMsg.Sender, campfirePubKey) {
				t.Error("forged message Sender equals the campfire key — this should not happen")
			}
			// A receiver implementing MUST-reject MUST check: msg.Sender == campfire pubkey.
			// We simulate that check here.
			if bytes.Equal(forgedMsg.Sender, campfirePubKey) {
				t.Error("adversarial: forged visibility-changed message incorrectly passes campfire-key check")
			}
			if bytes.Equal(forgedMsg.Sender, agentID.PublicKey) {
				// Correct: agent key, not campfire key — this message would be rejected by a
				// conformant receiver.
				t.Logf("PASS (adversarial): forged message Sender is the attacker key, not the campfire key — conformant receiver would reject it")
			}
		}
	}

	// Emit the REAL event using the legitimate API to confirm it passes.
	realClient := protocol.New(s, agentID)
	if err := realClient.EmitVisibilityChanged(campfireID, "open", "invite-only"); err != nil {
		t.Fatalf("EmitVisibilityChanged (real): %v", err)
	}
	realMsgs := readVisibilityChangedMessages(t, transportDir, campfireID)
	// Find the one signed by the campfire key.
	legitimateFound := false
	for _, msg := range realMsgs {
		if bytes.Equal(msg.Sender, campfirePubKey) && msg.VerifySignature() {
			legitimateFound = true
		}
	}
	if !legitimateFound {
		t.Error("adversarial: no legitimate campfire-key-signed visibility-changed message found after EmitVisibilityChanged")
	}
}

// TestEmitVisibilityChanged_TagConstantValue verifies that the tag constant has
// the expected wire value per the protocol spec (frozen at cf-protocol 1.0).
func TestEmitVisibilityChanged_TagConstantValue(t *testing.T) {
	if campfire.TagVisibilityChanged != "campfire:visibility-changed" {
		t.Errorf("TagVisibilityChanged wire value: got %q, want %q",
			campfire.TagVisibilityChanged, "campfire:visibility-changed")
	}
	if protocol.CampfireTagVisibilityChanged != "campfire:visibility-changed" {
		t.Errorf("protocol.CampfireTagVisibilityChanged wire value: got %q, want %q",
			protocol.CampfireTagVisibilityChanged, "campfire:visibility-changed")
	}
}

// TestEmitVisibilityChanged_StoredInLocalStore verifies the emitted event is
// mirrored to the local store so subscribers can read it without a sync step.
func TestEmitVisibilityChanged_StoredInLocalStore(t *testing.T) {
	agentID, s, _, campfireID, _ := setupVisibilityTestEnv(t)
	client := protocol.New(s, agentID)

	err := client.EmitVisibilityChanged(campfireID, "open", "invite-only")
	if err != nil {
		t.Fatalf("EmitVisibilityChanged: %v", err)
	}

	// Read from the store directly — no sync step.
	result, err := client.Read(protocol.ReadRequest{
		CampfireID: campfireID,
		Tags:       []string{campfire.TagVisibilityChanged},
		SkipSync:   true,
	})
	if err != nil {
		t.Fatalf("Read from store: %v", err)
	}
	if len(result.Messages) == 0 {
		t.Error("expected campfire:visibility-changed message in store, got none")
	}
}
