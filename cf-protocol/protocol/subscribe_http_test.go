package protocol_test

// Regression test for campfire-d80: the convention server framework
// (convention.Server.Serve → protocol.Client.Subscribe) silently fails to
// dispatch messages for p2p-http relay-backed campfires when the client has no
// injected Syncer (the SDK default — protocol.Init does not set one). Messages
// reach the relay but Subscribe never pulls them into the local store, so the
// poll loop's store read returns nothing and the registered handler never fires.
//
// Filesystem-transport subscriptions worked because the built-in sync handled
// filesystem; the p2p-http branch was missing. This test drives Subscribe over a
// fake relay with NO injected syncer and asserts the message is delivered.

import (
	"context"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/message"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/pkg/identity"
)

func TestSubscribe_HTTPPeer_NoInjectedSyncer_DeliversMessages(t *testing.T) {
	agentID, s, _ := setupTestEnv(t)
	defer s.Close()

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}

	// A validly-signed message the relay will serve.
	valid := newValidSyncMessage(t, cfID, "deliberation:open from operator")

	relay := setupHTTPSyncPeer(t, []*message.Message{valid})
	defer relay.Close()

	campfireID := setupHTTPPeerCampfire(t, agentID, s, cfID, relay.URL)

	// SDK-style client: NO injected syncer (mirrors protocol.Init / convention.Server).
	client := protocol.New(s, agentID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := client.Subscribe(ctx, protocol.SubscribeRequest{
		CampfireID:   campfireID,
		Tags:         []string{"status"},
		PollInterval: 50 * time.Millisecond,
	})

	select {
	case msg, ok := <-sub.Messages():
		if !ok {
			t.Fatalf("subscription closed without delivering message; Err=%v", sub.Err())
		}
		if msg.ID != valid.ID {
			t.Fatalf("delivered message ID = %q, want %q", msg.ID, valid.ID)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("Subscribe over p2p-http relay never delivered the message (campfire-d80): " +
			"the poll loop did not pull relay messages into the store")
	}
}
