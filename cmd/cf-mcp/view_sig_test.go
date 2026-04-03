package main

// Regression tests for campfire-agent-ltj: handleViewTool must verify message
// signatures and provenance hops before returning messages via MCP views.
//
// Previously, handleViewTool called fsT.ListMessages directly, bypassing the
// verification that protocol.Client.syncIfFilesystem performs. The fix routes
// the FS sync through syncFSVerified which applies the same checks.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

const viewSigCampfireID = "bbbb1111cccc2222dddd3333eeee4444bbbb1111cccc2222dddd3333eeee4444"

// addTestHop adds a valid provenance hop to msg, signed by a freshly generated
// campfire identity. This satisfies the syncFSVerified provenance requirement:
// every message must have at least one hop with a valid signature.
// (campfire-agent-ltj: syncFSVerified now enforces provenance on FS-synced messages.)
//
// This helper is also used by await_fs_test.go (same package — package main).
func addTestHop(t *testing.T, msg *message.Message) {
	t.Helper()
	campfireID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity for hop: %v", err)
	}
	if err := msg.AddHop(
		campfireID.PrivateKey, campfireID.PublicKey,
		[]byte("test-membership-hash"),
		1,        // memberCount
		"direct", // joinProtocol
		nil,      // receptionReqs
		"full",   // role
	); err != nil {
		t.Fatalf("adding provenance hop: %v", err)
	}
}

// setupFSView creates a temp FS transport dir, sets CF_TRANSPORT_DIR, creates
// the campfire messages directory, and returns the FS transport plus cleanup.
func setupFSView(t *testing.T) (*fs.Transport, func()) {
	t.Helper()
	dir := t.TempDir()
	msgsDir := filepath.Join(dir, viewSigCampfireID, "messages")
	if err := os.MkdirAll(msgsDir, 0755); err != nil {
		t.Fatalf("creating messages dir: %v", err)
	}
	tr := fs.New(dir)
	old := os.Getenv("CF_TRANSPORT_DIR")
	os.Setenv("CF_TRANSPORT_DIR", dir) //nolint:errcheck
	cleanup := func() {
		if old == "" {
			os.Unsetenv("CF_TRANSPORT_DIR") //nolint:errcheck
		} else {
			os.Setenv("CF_TRANSPORT_DIR", old) //nolint:errcheck
		}
	}
	return tr, cleanup
}

// extractViewMessages decodes a handleViewTool response and returns the
// "messages" slice from the inner result. The response is wrapped in a trust
// envelope: content[0].text → Envelope JSON → tainted.content.messages.
func extractViewMessages(t *testing.T, resp jsonRPCResponse) []interface{} {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("handleViewTool returned error: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}
	b, _ := json.Marshal(resp.Result)
	// Decode outer MCP content array.
	var outer struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &outer); err != nil || len(outer.Content) == 0 {
		t.Fatalf("cannot decode view response content: %s", string(b))
	}
	// Decode trust envelope — envelopedResponse wraps the view result as
	// tainted.content (Trust Convention v0.2 §6 / TaintedFields.Content).
	var envelope struct {
		Tainted struct {
			Content struct {
				Messages []interface{} `json:"messages"`
			} `json:"content"`
		} `json:"tainted"`
	}
	if err := json.Unmarshal([]byte(outer.Content[0].Text), &envelope); err != nil {
		t.Fatalf("cannot decode trust envelope: %v (text: %s)", err, outer.Content[0].Text)
	}
	return envelope.Tainted.Content.Messages
}

// makeViewEntry returns a viewToolEntry that matches messages tagged "status".
// All test messages use the "status" tag for deterministic predicate matching.
func makeViewEntry(campfireID string) *viewToolEntry {
	return &viewToolEntry{
		name:       "test-view",
		campfireID: campfireID,
		predicate:  `(tag "status")`,
	}
}

// ---------------------------------------------------------------------------
// Test 5: valid message with provenance appears in view
// ---------------------------------------------------------------------------

// TestViewTool_ValidMessageAppears verifies that a properly signed message
// with a valid provenance hop is returned by handleViewTool.
func TestViewTool_ValidMessageAppears(t *testing.T) {
	tr, cleanup := setupFSView(t)
	defer cleanup()

	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	msg, err := message.NewMessage(id.PrivateKey, id.PublicKey, []byte("hello"), []string{"status"}, nil)
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}
	addTestHop(t, msg)
	if err := tr.WriteMessage(viewSigCampfireID, msg); err != nil {
		t.Fatalf("writing message: %v", err)
	}

	srv := newTestServer(t)
	resp := srv.handleViewTool(float64(1), makeViewEntry(viewSigCampfireID), map[string]interface{}{})

	msgs := extractViewMessages(t, resp)
	if len(msgs) != 1 {
		t.Errorf("expected 1 message in view, got %d", len(msgs))
	}
}

// ---------------------------------------------------------------------------
// Test 6: tampered message is rejected by handleViewTool (regression: campfire-agent-ltj)
// ---------------------------------------------------------------------------

// TestViewTool_TamperedMessageRejected is a regression test for
// campfire-agent-ltj: handleViewTool previously accepted messages without
// verifying their Ed25519 signatures. This test tampers with the payload of
// an otherwise well-formed message and confirms it does NOT appear in the view.
func TestViewTool_TamperedMessageRejected(t *testing.T) {
	tr, cleanup := setupFSView(t)
	defer cleanup()

	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	msg, err := message.NewMessage(id.PrivateKey, id.PublicKey, []byte("legit"), []string{"status"}, nil)
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}
	addTestHop(t, msg)
	// Tamper with payload AFTER signing — signature is now invalid.
	msg.Payload = []byte("TAMPERED")
	if err := tr.WriteMessage(viewSigCampfireID, msg); err != nil {
		t.Fatalf("writing tampered message: %v", err)
	}

	srv := newTestServer(t)
	resp := srv.handleViewTool(float64(1), makeViewEntry(viewSigCampfireID), map[string]interface{}{})

	if resp.Error != nil {
		t.Fatalf("handleViewTool returned error: %v", resp.Error.Message)
	}
	msgs := extractViewMessages(t, resp)
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages (tampered message must be rejected), got %d", len(msgs))
	}
}

// ---------------------------------------------------------------------------
// Test 7: message without provenance hop is rejected by handleViewTool
// ---------------------------------------------------------------------------

// TestViewTool_NoprovenanceMessageRejected verifies that handleViewTool
// does not return a message that has no provenance hops, even if the message
// signature itself is valid. Empty provenance is rejected by syncFSVerified
// to prevent unsigned relay-chain bypass.
func TestViewTool_NoprovenanceMessageRejected(t *testing.T) {
	tr, cleanup := setupFSView(t)
	defer cleanup()

	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	// NewMessage creates Provenance: []ProvenanceHop{} — intentionally empty.
	msg, err := message.NewMessage(id.PrivateKey, id.PublicKey, []byte("no-hop"), []string{"status"}, nil)
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}
	// No addTestHop — Provenance stays empty.
	if err := tr.WriteMessage(viewSigCampfireID, msg); err != nil {
		t.Fatalf("writing no-provenance message: %v", err)
	}

	srv := newTestServer(t)
	resp := srv.handleViewTool(float64(1), makeViewEntry(viewSigCampfireID), map[string]interface{}{})

	if resp.Error != nil {
		t.Fatalf("handleViewTool returned error: %v", resp.Error.Message)
	}
	msgs := extractViewMessages(t, resp)
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages (no-provenance message must be rejected), got %d", len(msgs))
	}
}
