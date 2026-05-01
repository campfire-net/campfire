// session_test.go — tests for session campfire creation and lifecycle.
//
// cf 0.30 migration (campfireagent-c3a):
// The shared-key session token mechanism is removed. Tests now verify the
// updated NewSession API: creator's own identity key, per-worker grants via
// cf-conventions/cf-session (tested separately in that package).
package protocol_test

import (
	"os"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
)

// setupSessionClient creates a protocol.Client with a temporary identity and
// store for session tests. Used by session_test.go and session_events_test.go.
func setupSessionClient(t *testing.T) (*protocol.Client, func()) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "cf-session-test-")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	client, _, err := protocol.Init(tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("protocol.Init: %v", err)
	}
	return client, func() {
		client.Close()
		os.RemoveAll(tmpDir)
	}
}

// TestNewSession_ReturnsSessionAndCampfireID verifies that NewSession returns a
// non-nil session and a non-empty campfire ID.
func TestNewSession_ReturnsSessionAndCampfireID(t *testing.T) {
	client, cleanup := setupSessionClient(t)
	defer cleanup()

	sess, campfireID, err := client.NewSession(2 * time.Hour)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.End() //nolint:errcheck

	if sess == nil {
		t.Fatal("NewSession returned nil session")
	}
	if campfireID == "" {
		t.Error("NewSession returned empty campfire ID")
	}
	// Campfire ID must be a 64-char hex string (32-byte Ed25519 public key).
	if len(campfireID) != 64 {
		t.Errorf("campfire ID length: got %d, want 64", len(campfireID))
	}
}

// TestNewSession_CampfireIDMatchesSess verifies that the returned campfire ID
// matches what sess.CampfireID() returns.
func TestNewSession_CampfireIDMatchesSess(t *testing.T) {
	client, cleanup := setupSessionClient(t)
	defer cleanup()

	sess, campfireID, err := client.NewSession(2 * time.Hour)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.End() //nolint:errcheck

	if sess.CampfireID() != campfireID {
		t.Errorf("sess.CampfireID() = %q, want %q", sess.CampfireID(), campfireID)
	}
}

// TestNewSession_TTLExceeded verifies that TTL > 24h is rejected.
func TestNewSession_TTLExceeded(t *testing.T) {
	client, cleanup := setupSessionClient(t)
	defer cleanup()

	_, _, err := client.NewSession(25 * time.Hour)
	if err == nil {
		t.Fatal("expected error for TTL > 24h, got nil")
	}
}

// TestNewSession_ZeroTTL verifies that TTL ≤ 0 is rejected.
func TestNewSession_ZeroTTL(t *testing.T) {
	client, cleanup := setupSessionClient(t)
	defer cleanup()

	_, _, err := client.NewSession(0)
	if err == nil {
		t.Fatal("expected error for zero TTL, got nil")
	}
}

// TestSessionSendRead verifies that messages sent on a session campfire
// can be read back.
func TestSessionSendRead(t *testing.T) {
	client, cleanup := setupSessionClient(t)
	defer cleanup()

	sess, _, err := client.NewSession(2 * time.Hour)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.End() //nolint:errcheck

	want := "hello from creator"
	if _, err := sess.Send(want); err != nil {
		t.Fatalf("Send: %v", err)
	}

	msgs, err := sess.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	var found bool
	for _, m := range msgs {
		if string(m.Payload) == want {
			found = true
		}
	}
	if !found {
		t.Errorf("sent message %q not found in %d messages", want, len(msgs))
	}
}

// TestSessionEnd_Cleanup verifies that End() works and subsequent sends fail.
func TestSessionEnd_Cleanup(t *testing.T) {
	client, cleanup := setupSessionClient(t)
	defer cleanup()

	sess, _, err := client.NewSession(time.Hour)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	if err := sess.End(); err != nil {
		t.Fatalf("End: %v", err)
	}

	// Subsequent operations on the ended session should fail gracefully.
	_, err = sess.Send("after-end")
	if err == nil {
		t.Error("expected error after session ended, got nil")
	}
}

// TestMaxSessionTTL verifies the re-exported constant has the expected value.
func TestMaxSessionTTL(t *testing.T) {
	if protocol.MaxSessionTTL != 24*time.Hour {
		t.Errorf("MaxSessionTTL = %v, want 24h", protocol.MaxSessionTTL)
	}
}
