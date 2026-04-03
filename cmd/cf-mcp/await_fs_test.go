package main

// Tests for the FS polling path in handleAwait (non-HTTP mode).
//
// handleAwait uses fs.DefaultBaseDir() which reads CF_TRANSPORT_DIR.
// We point that env var at a temp dir and write messages directly
// via the FS transport to drive the polling loop.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// fsCampfireID returns a deterministic fake campfire ID for FS tests.
// It is just a hex string — no real campfire needs to exist.
const fsCampfireID = "aaabbbcccddd00001111222233334444aaabbbcccddd00001111222233334444"

// setupFSAwait creates a temp transport dir, sets CF_TRANSPORT_DIR, creates
// the campfire message directory, and returns the FS transport and cleanup func.
func setupFSAwait(t *testing.T) (*fs.Transport, func()) {
	t.Helper()
	dir := t.TempDir()
	// Ensure the messages directory exists so FS transport can write.
	msgsDir := filepath.Join(dir, fsCampfireID, "messages")
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

// writeFulfillment creates a pair of messages (original + fulfilling) in the
// given FS transport. Both messages include a valid provenance hop so that
// syncFSVerified accepts them (campfire-agent-ltj: hop verification added).
// Returns the original message ID.
//
// addTestHop is defined in view_sig_test.go (same package — package main).
func writeFulfillment(t *testing.T, tr *fs.Transport, campfireID string) string {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	origMsg, err := message.NewMessage(id.PrivateKey, id.PublicKey, []byte("original"), nil, nil)
	if err != nil {
		t.Fatalf("creating original message: %v", err)
	}
	addTestHop(t, origMsg)
	if err := tr.WriteMessage(campfireID, origMsg); err != nil {
		t.Fatalf("writing original message: %v", err)
	}

	fulfillMsg, err := message.NewMessage(
		id.PrivateKey, id.PublicKey,
		[]byte("fulfilled!"),
		[]string{"fulfills"},
		[]string{origMsg.ID},
	)
	if err != nil {
		t.Fatalf("creating fulfilling message: %v", err)
	}
	addTestHop(t, fulfillMsg)
	if err := tr.WriteMessage(campfireID, fulfillMsg); err != nil {
		t.Fatalf("writing fulfilling message: %v", err)
	}

	return origMsg.ID
}

// extractFSAwaitResult decodes the JSON-RPC response from handleAwait and
// returns the inner payload map. Fails the test on any structural error.
func extractFSAwaitResult(t *testing.T, resp jsonRPCResponse) map[string]interface{} {
	t.Helper()
	if resp.Error != nil {
		// Timeout is returned as a JSON-RPC error in FS mode; let the caller inspect.
		return nil
	}
	b, _ := json.Marshal(resp.Result)
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &result); err != nil || len(result.Content) == 0 {
		t.Fatalf("cannot extract content from await result: %s", string(b))
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
		t.Fatalf("cannot parse await payload JSON: %v (text: %s)", err, result.Content[0].Text)
	}
	return payload
}

// ---------------------------------------------------------------------------
// Test 1: fulfillment found on initial sync (returns before entering poll loop)
// ---------------------------------------------------------------------------

// TestAwaitFS_FulfilledOnInitialSync verifies that handleAwait returns the
// fulfilling message immediately when it is already in the FS transport before
// handleAwait is called. The response must arrive well within the 2-second
// ticker interval (i.e. the initial-sync path fired, not the poll loop).
func TestAwaitFS_FulfilledOnInitialSync(t *testing.T) {
	tr, cleanup := setupFSAwait(t)
	defer cleanup()

	origMsgID := writeFulfillment(t, tr, fsCampfireID)

	srv := newTestServer(t)
	// s.httpTransport is nil → FS polling path.

	start := time.Now()
	resp := srv.handleAwait(float64(1), map[string]interface{}{
		"campfire_id": fsCampfireID,
		"msg_id":      origMsgID,
		"timeout":     "30s",
	})
	elapsed := time.Since(start)

	if resp.Error != nil {
		t.Fatalf("unexpected error from handleAwait: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}

	payload := extractFSAwaitResult(t, resp)
	if payload == nil {
		t.Fatal("expected non-nil payload for fulfilled response")
	}
	id, _ := payload["id"].(string)
	if id == "" {
		t.Errorf("expected non-empty id in fulfilled message payload; got: %v", payload)
	}

	// Must return before the first ticker fires (2s).
	if elapsed >= 2*time.Second {
		t.Errorf("initial-sync path took too long: %v (expected <2s)", elapsed)
	}
}

// ---------------------------------------------------------------------------
// Test 2: fulfillment arrives during poll loop
// ---------------------------------------------------------------------------

// TestAwaitFS_FulfilledDuringPoll verifies that handleAwait returns the
// fulfilling message after the poll ticker fires and finds the message.
// The message is written to the FS transport from a goroutine after a short
// delay, ensuring it arrives after the initial sync but before the timeout.
//
// This test waits up to 6 seconds (two ticker cycles plus margin) for the
// poll loop to fire and discover the fulfillment.
func TestAwaitFS_FulfilledDuringPoll(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timing-sensitive poll test in short mode")
	}

	tr, cleanup := setupFSAwait(t)
	defer cleanup()

	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	origMsg, err := message.NewMessage(id.PrivateKey, id.PublicKey, []byte("original"), nil, nil)
	if err != nil {
		t.Fatalf("creating original message: %v", err)
	}
	// Add provenance hop so syncFSVerified accepts the message (campfire-agent-ltj).
	addTestHop(t, origMsg)
	// Write original before calling handleAwait so the initial sync sees it
	// but finds no fulfillment yet.
	if err := tr.WriteMessage(fsCampfireID, origMsg); err != nil {
		t.Fatalf("writing original message: %v", err)
	}

	// Pre-create the fulfilling message (with provenance hop) before the goroutine
	// so we don't need t.Fatal inside the goroutine.
	fulfillMsg, err := message.NewMessage(
		id.PrivateKey, id.PublicKey,
		[]byte("fulfilled!"),
		[]string{"fulfills"},
		[]string{origMsg.ID},
	)
	if err != nil {
		t.Fatalf("creating fulfilling message: %v", err)
	}
	addTestHop(t, fulfillMsg)

	// Write the fulfilling message from a goroutine after a short delay so
	// it arrives after the initial sync check but within the first ticker window.
	go func() {
		// 200ms gives handleAwait time to complete the initial sync and block
		// on the ticker before we write the fulfillment.
		time.Sleep(200 * time.Millisecond)
		if err := tr.WriteMessage(fsCampfireID, fulfillMsg); err != nil {
			fmt.Printf("WARN: writing fulfilling message: %v\n", err)
		}
	}()

	srv := newTestServer(t)

	start := time.Now()
	resp := srv.handleAwait(float64(1), map[string]interface{}{
		"campfire_id": fsCampfireID,
		"msg_id":      origMsg.ID,
		"timeout":     "30s",
	})
	elapsed := time.Since(start)

	if resp.Error != nil {
		t.Fatalf("unexpected error from handleAwait: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}

	payload := extractFSAwaitResult(t, resp)
	if payload == nil {
		t.Fatal("expected non-nil payload for fulfilled response")
	}
	msgID, _ := payload["id"].(string)
	if msgID == "" {
		t.Errorf("expected non-empty id in fulfilled message payload; got: %v", payload)
	}

	// Should resolve within 6s (one ticker cycle at 2s + generous margin).
	if elapsed >= 6*time.Second {
		t.Errorf("poll loop took too long: %v (expected <6s)", elapsed)
	}
	// Should NOT have returned on the initial sync (message wasn't there yet).
	// We can't assert a minimum elapsed time reliably, but we verify the correct
	// message was returned.
	if msgID == origMsg.ID {
		t.Errorf("payload id is the original message ID, expected the fulfilling message ID")
	}
}

// ---------------------------------------------------------------------------
// Test 3: timeout path
// ---------------------------------------------------------------------------

// TestAwaitFS_Timeout verifies that handleAwait returns a JSON-RPC error with
// message "timeout: no fulfillment received" when the timeout expires and no
// fulfilling message has arrived. Uses a 10ms timeout to keep the test fast.
func TestAwaitFS_Timeout(t *testing.T) {
	_, cleanup := setupFSAwait(t)
	defer cleanup()

	srv := newTestServer(t)

	resp := srv.handleAwait(float64(1), map[string]interface{}{
		"campfire_id": fsCampfireID,
		"msg_id":      "nonexistent-message-id-that-will-never-be-fulfilled",
		"timeout":     "10ms",
	})

	if resp.Error == nil {
		t.Fatal("expected error response for timeout, got nil")
	}
	if resp.Error.Code != -32000 {
		t.Errorf("expected error code -32000, got %d", resp.Error.Code)
	}
	if resp.Error.Message != "timeout: no fulfillment received" {
		t.Errorf("unexpected error message: %q", resp.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// Test 4: nil store path (s.st == nil) opens dedicated store connection
// ---------------------------------------------------------------------------

// TestAwaitFS_NilStoreOpensConnection verifies that handleAwait opens its
// own store connection when s.st is nil (non-session / standalone mode).
// This exercises the store-open branch at lines ~1126-1132.
// We assert on the timeout error to confirm handleAwait ran end-to-end.
func TestAwaitFS_NilStoreOpensConnection(t *testing.T) {
	_, cleanup := setupFSAwait(t)
	defer cleanup()

	srv := newTestServer(t)
	// newTestServer leaves srv.st nil — the nil-store branch fires.
	if srv.st != nil {
		t.Skip("newTestServer unexpectedly set srv.st; skipping nil-store test")
	}

	resp := srv.handleAwait(float64(1), map[string]interface{}{
		"campfire_id": fsCampfireID,
		"msg_id":      "nonexistent-id",
		"timeout":     "10ms",
	})

	// The store must have opened successfully — otherwise we'd get a store-open
	// error with "opening store:" prefix instead of the timeout message.
	if resp.Error == nil {
		t.Fatal("expected error response, got nil")
	}
	if resp.Error.Message != "timeout: no fulfillment received" {
		t.Errorf("expected timeout error (store opened OK), got: code=%d msg=%q",
			resp.Error.Code, resp.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// Regression tests: campfire-agent-ltj — signature verification in FS path
// ---------------------------------------------------------------------------

// TestAwaitFS_TamperedFulfillmentRejected is a regression test for
// campfire-agent-ltj: handleAwait previously called fs.Transport.ListMessages
// directly, bypassing signature and provenance-hop verification. This test
// writes a fulfilling message whose payload has been tampered (invalidating the
// Ed25519 signature) and confirms that handleAwait times out rather than
// returning the tampered message.
func TestAwaitFS_TamperedFulfillmentRejected(t *testing.T) {
	tr, cleanup := setupFSAwait(t)
	defer cleanup()

	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	// Create original message (will be what we await on).
	origMsg, err := message.NewMessage(id.PrivateKey, id.PublicKey, []byte("original"), nil, nil)
	if err != nil {
		t.Fatalf("creating original message: %v", err)
	}
	addTestHop(t, origMsg)
	if err := tr.WriteMessage(fsCampfireID, origMsg); err != nil {
		t.Fatalf("writing original message: %v", err)
	}

	// Create a fulfilling message with a valid signature...
	fulfillMsg, err := message.NewMessage(
		id.PrivateKey, id.PublicKey,
		[]byte("fulfilled!"),
		[]string{"fulfills"},
		[]string{origMsg.ID},
	)
	if err != nil {
		t.Fatalf("creating fulfilling message: %v", err)
	}
	addTestHop(t, fulfillMsg)
	// ...then tamper with the payload AFTER signing (breaks Ed25519 signature).
	fulfillMsg.Payload = []byte("TAMPERED-payload")
	if err := tr.WriteMessage(fsCampfireID, fulfillMsg); err != nil {
		t.Fatalf("writing tampered message: %v", err)
	}

	srv := newTestServer(t)

	// handleAwait must NOT return the tampered message. It should time out.
	resp := srv.handleAwait(float64(1), map[string]interface{}{
		"campfire_id": fsCampfireID,
		"msg_id":      origMsg.ID,
		"timeout":     "50ms", // short timeout — we expect a timeout, not a result
	})

	if resp.Error == nil {
		t.Fatal("expected timeout error (tampered message should be rejected), got success response")
	}
	if resp.Error.Message != "timeout: no fulfillment received" {
		t.Errorf("expected timeout error, got: code=%d msg=%q", resp.Error.Code, resp.Error.Message)
	}
}

// TestAwaitFS_NoprovenanceFulfillmentRejected is a regression test for
// campfire-agent-ltj: messages with empty provenance must be rejected.
// A message with no provenance hops passes the hop-verification loop vacuously
// (there are no hops to check), which would allow unsigned relay chains.
// This test confirms that handleAwait does not accept such messages.
func TestAwaitFS_NoprovenanceFulfillmentRejected(t *testing.T) {
	tr, cleanup := setupFSAwait(t)
	defer cleanup()

	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	// Create original message WITH a provenance hop (so await can find it if synced).
	origMsg, err := message.NewMessage(id.PrivateKey, id.PublicKey, []byte("original"), nil, nil)
	if err != nil {
		t.Fatalf("creating original message: %v", err)
	}
	addTestHop(t, origMsg)
	if err := tr.WriteMessage(fsCampfireID, origMsg); err != nil {
		t.Fatalf("writing original message: %v", err)
	}

	// Create a fulfilling message but DO NOT add a provenance hop.
	// message.NewMessage sets Provenance to []ProvenanceHop{} (empty).
	fulfillMsg, err := message.NewMessage(
		id.PrivateKey, id.PublicKey,
		[]byte("fulfilled!"),
		[]string{"fulfills"},
		[]string{origMsg.ID},
	)
	if err != nil {
		t.Fatalf("creating fulfilling message: %v", err)
	}
	// No addTestHop — Provenance stays empty. syncFSVerified must reject this.
	if err := tr.WriteMessage(fsCampfireID, fulfillMsg); err != nil {
		t.Fatalf("writing no-provenance message: %v", err)
	}

	srv := newTestServer(t)

	resp := srv.handleAwait(float64(1), map[string]interface{}{
		"campfire_id": fsCampfireID,
		"msg_id":      origMsg.ID,
		"timeout":     "50ms",
	})

	if resp.Error == nil {
		t.Fatal("expected timeout error (no-provenance message should be rejected), got success response")
	}
	if resp.Error.Message != "timeout: no fulfillment received" {
		t.Errorf("expected timeout error, got: code=%d msg=%q", resp.Error.Code, resp.Error.Message)
	}
}
