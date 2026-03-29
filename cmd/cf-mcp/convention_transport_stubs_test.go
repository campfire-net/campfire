package main

import (
	"context"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/ratelimit"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// TestSendCampfireKeySignedMessage verifies that sendCampfireKeySignedMessage
// signs a message with the campfire's Ed25519 key (not the agent's member key)
// and stores it. The message sender should be the campfire public key hex.
func TestSendCampfireKeySignedMessage(t *testing.T) {
	srv := newTestServer(t)

	// Create a campfire and write its state so ReadState can find it.
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("creating campfire: %v", err)
	}
	cfID := cf.PublicKeyHex()

	fsT := fs.New(srv.cfHome)
	if err := fsT.Init(cf); err != nil {
		t.Fatalf("initializing campfire fs state: %v", err)
	}

	// Open a store and wire it to the server.
	rawSt, err := store.Open(store.StorePath(srv.cfHome))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { rawSt.Close() })
	rl := ratelimit.New(rawSt, ratelimit.Config{})
	srv.st = rl

	// Add membership so ListMessages works.
	if err := rl.AddMembership(store.Membership{
		CampfireID:   cfID,
		TransportDir: fsT.CampfireDir(cfID),
		JoinProtocol: "open",
		Role:         "full",
		JoinedAt:     1,
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	ctx := context.Background()
	msgID, err := srv.sendCampfireKeySignedMessage(ctx, cfID, []byte(`{"test":"payload"}`), []string{"convention:operation"}, nil)
	if err != nil {
		t.Fatalf("sendCampfireKeySignedMessage: unexpected error: %v", err)
	}
	if msgID == "" {
		t.Fatal("sendCampfireKeySignedMessage: expected non-empty msgID")
	}

	// Verify message is in the store.
	msgs, err := rl.ListMessages(cfID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	// The sender should be the campfire public key, not the agent's key.
	if msgs[0].Sender != cfID {
		t.Errorf("expected sender=%s (campfire key), got %s", cfID, msgs[0].Sender)
	}
}

// TestSendCampfireKeySignedMessageNoState verifies that
// sendCampfireKeySignedMessage returns an error when no campfire state exists.
func TestSendCampfireKeySignedMessageNoState(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.sendCampfireKeySignedMessage(ctx, "nonexistent-campfire", []byte(`{}`), nil, nil)
	if err == nil {
		t.Fatal("expected error for missing campfire state, got nil")
	}
	if !strings.Contains(err.Error(), "loading campfire key") {
		t.Errorf("expected 'loading campfire key' in error, got: %v", err)
	}
}
