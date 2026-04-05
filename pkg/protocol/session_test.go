package protocol_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/session"
)

// setupSessionClient creates a protocol.Client with a temporary identity and
// store for session tests.
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

// TestNewSession_TokenPrefix verifies that the token has the cfs1_ prefix.
func TestNewSession_TokenPrefix(t *testing.T) {
	client, cleanup := setupSessionClient(t)
	defer cleanup()

	sess, tok, err := client.NewSession(2 * time.Hour)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.End()

	if !strings.HasPrefix(tok, session.TokenPrefix) {
		limit := len(tok)
		if limit > 10 {
			limit = 10
		}
		t.Errorf("token prefix: got %q, want prefix %q", tok[:limit], session.TokenPrefix)
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
	if !strings.Contains(err.Error(), "24h") && !strings.Contains(err.Error(), "maximum") {
		t.Errorf("error should mention 24h or maximum, got: %v", err)
	}
}

// TestSessionSendRead verifies that a joiner can read messages posted by the creator.
func TestSessionSendRead(t *testing.T) {
	creator, cleanupCreator := setupSessionClient(t)
	defer cleanupCreator()

	sess, tok, err := creator.NewSession(2 * time.Hour)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.End()

	// Creator sends a message.
	payload := "hello from creator"
	if _, err := sess.Send(payload); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Joiner joins via token (no CF_HOME required).
	joinerSess, err := protocol.JoinSession(tok, nil)
	if err != nil {
		t.Fatalf("JoinSession: %v", err)
	}
	defer joinerSess.Close()

	// Joiner reads messages.
	msgs, err := joinerSess.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	found := false
	for _, m := range msgs {
		if string(m.Payload) == payload {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("joiner did not find creator's message %q in %d messages", payload, len(msgs))
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
