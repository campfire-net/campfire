package protocol_test

// bridge_ingress_test.go — tests for bridge provenance tiers.
//
// Covered bead: campfire-agent-0ca
//
// Tests verify:
//   - TestIsBridgedTrue: a message forwarded by Bridge() has IsBridged() == true
//   - TestIsBridgedFalse: a message written by direct Send() has IsBridged() == false

import (
	"context"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/protocol"
)

// TestIsBridgedTrue verifies that a message forwarded by protocol.Bridge()
// reports IsBridged() == true.
// Bridge() sets RoleOverride: campfire.RoleBlindRelay on the forwarded message,
// which causes sendFilesystem to write a blind-relay provenance hop.
func TestIsBridgedTrue(t *testing.T) {
	srcID, srcStore, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, srcID, srcStore, transportDir, campfire.RoleFull)
	source := protocol.New(srcStore, srcID)

	destID, destStore, _ := setupTestEnv(t)
	addMemberFS(t, destID, destStore, transportDir, campfireID)
	dest := protocol.New(destStore, destID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bridgeErr := make(chan error, 1)
	go func() {
		bridgeErr <- protocol.Bridge(ctx, source, dest, campfireID, protocol.BridgeOptions{})
	}()

	// Give bridge time to subscribe.
	time.Sleep(300 * time.Millisecond)

	// Send a message on source.
	_, err := source.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("bridge ingress test payload"),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("source.Send: %v", err)
	}

	// Wait for the forwarded message to appear on dest (re-sent by dest identity).
	var bridgedMsg *protocol.Message
	deadline := time.After(10 * time.Second)
	for {
		result, err := dest.Read(protocol.ReadRequest{CampfireID: campfireID})
		if err != nil {
			t.Fatalf("dest.Read: %v", err)
		}
		for i := range result.Messages {
			msg := result.Messages[i]
			// The bridged copy is re-sent by dest's identity (bridge agent).
			if string(msg.Payload) == "bridge ingress test payload" && msg.Sender == destID.PublicKeyHex() {
				bridgedMsg = &msg
				break
			}
		}
		if bridgedMsg != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for bridged message on dest")
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}

	cancel()

	// Core assertion: the forwarded message must be flagged as bridged.
	if !bridgedMsg.IsBridged() {
		t.Errorf("IsBridged() = false, want true — Bridge() must set blind-relay role on forwarded hops")
	}
}

// TestIsBridgedFalse verifies that a message written by a direct Send() call
// (without going through Bridge) reports IsBridged() == false.
func TestIsBridgedFalse(t *testing.T) {
	agentID, agentStore, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, agentStore, transportDir, campfire.RoleFull)
	client := protocol.New(agentStore, agentID)

	_, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("direct send payload for isbridged test"),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("client.Send: %v", err)
	}

	result, err := client.Read(protocol.ReadRequest{CampfireID: campfireID})
	if err != nil {
		t.Fatalf("client.Read: %v", err)
	}

	var directMsg *protocol.Message
	for i := range result.Messages {
		msg := result.Messages[i]
		if string(msg.Payload) == "direct send payload for isbridged test" {
			directMsg = &msg
			break
		}
	}

	if directMsg == nil {
		t.Fatal("direct send message not found in read result")
	}

	// Core assertion: a direct Send() must NOT be flagged as bridged.
	if directMsg.IsBridged() {
		t.Errorf("IsBridged() = true, want false — direct Send() must not produce blind-relay hops")
	}
}
