package protocol_test

// Tests for protocol.Bridge — campfire-agent-utj.
//
// All tests use real filesystem transport clients. No mocks.
// Two separate identities share a single transport directory.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// addMemberFS adds an identity as a full member to an existing campfire in the
// given transport base dir, and records the membership in the given store.
func addMemberFS(t *testing.T, id *identity.Identity, s store.Store, transportBaseDir, campfireID string) {
	t.Helper()
	tr := fs.New(transportBaseDir)
	if err := tr.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: id.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("writing member record: %v", err)
	}
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
}

func TestBridgeUnidirectional(t *testing.T) {
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
	time.Sleep(200 * time.Millisecond)

	// Send a message on source.
	_, err := source.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("hello from source"),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("source.Send: %v", err)
	}

	// Wait for the forwarded message to appear on dest (different ID, same payload).
	deadline := time.After(5 * time.Second)
	for {
		result, err := dest.Read(protocol.ReadRequest{CampfireID: campfireID})
		if err != nil {
			t.Fatalf("dest.Read: %v", err)
		}
		found := false
		for _, msg := range result.Messages {
			if string(msg.Payload) == "hello from source" {
				// The message was re-sent by dest's Send, so the sender should be dest's key.
				if msg.Sender == destID.PublicKeyHex() {
					found = true
					break
				}
			}
		}
		if found {
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
	err = <-bridgeErr
	if err != nil && err != context.Canceled {
		t.Errorf("Bridge returned unexpected error: %v", err)
	}
}

func TestBridgeBidirectional(t *testing.T) {
	srcID, srcStore, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, srcID, srcStore, transportDir, campfire.RoleFull)
	source := protocol.New(srcStore, srcID)

	destID, destStore, _ := setupTestEnv(t)
	addMemberFS(t, destID, destStore, transportDir, campfireID)
	dest := protocol.New(destStore, destID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var directions []string

	bridgeErr := make(chan error, 1)
	go func() {
		bridgeErr <- protocol.Bridge(ctx, source, dest, campfireID, protocol.BridgeOptions{
			Bidirectional: true,
			OnMessage: func(msg *protocol.Message, direction string) {
				mu.Lock()
				directions = append(directions, direction)
				mu.Unlock()
			},
		})
	}()

	time.Sleep(200 * time.Millisecond)

	// Send from source.
	_, err := source.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("from source"),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("source.Send: %v", err)
	}

	// Send from dest.
	_, err = dest.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("from dest"),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("dest.Send: %v", err)
	}

	// Wait for both directions to fire.
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		gotSourceToDest := false
		gotDestToSource := false
		for _, d := range directions {
			if d == "source\u2192dest" {
				gotSourceToDest = true
			}
			if d == "dest\u2192source" {
				gotDestToSource = true
			}
		}
		mu.Unlock()
		if gotSourceToDest && gotDestToSource {
			break
		}
		select {
		case <-deadline:
			mu.Lock()
			t.Fatalf("timeout: got directions %v, want both source→dest and dest→source", directions)
			mu.Unlock()
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}

	cancel()
}

func TestBridgeDedup(t *testing.T) {
	srcID, srcStore, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, srcID, srcStore, transportDir, campfire.RoleFull)
	source := protocol.New(srcStore, srcID)

	destID, destStore, _ := setupTestEnv(t)
	addMemberFS(t, destID, destStore, transportDir, campfireID)
	dest := protocol.New(destStore, destID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	bridgedCount := 0

	bridgeErr := make(chan error, 1)
	go func() {
		bridgeErr <- protocol.Bridge(ctx, source, dest, campfireID, protocol.BridgeOptions{
			Bidirectional: true,
			OnMessage: func(msg *protocol.Message, direction string) {
				mu.Lock()
				bridgedCount++
				mu.Unlock()
			},
		})
	}()

	time.Sleep(200 * time.Millisecond)

	// Send one message from source.
	_, err := source.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("dedup test"),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("source.Send: %v", err)
	}

	// Wait for bridge to process, then check count hasn't spiraled.
	time.Sleep(2 * time.Second)

	mu.Lock()
	count := bridgedCount
	mu.Unlock()

	// The message should be bridged exactly once (source→dest).
	// Dedup prevents the forwarded message from looping back dest→source.
	if count != 1 {
		t.Errorf("expected 1 bridged message (dedup), got %d", count)
	}

	cancel()
}

func TestBridgeTagFilter(t *testing.T) {
	srcID, srcStore, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, srcID, srcStore, transportDir, campfire.RoleFull)
	source := protocol.New(srcStore, srcID)

	destID, destStore, _ := setupTestEnv(t)
	addMemberFS(t, destID, destStore, transportDir, campfireID)
	dest := protocol.New(destStore, destID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var bridgedPayloads []string

	bridgeErr := make(chan error, 1)
	go func() {
		bridgeErr <- protocol.Bridge(ctx, source, dest, campfireID, protocol.BridgeOptions{
			TagFilter: []string{"important"},
			OnMessage: func(msg *protocol.Message, direction string) {
				mu.Lock()
				bridgedPayloads = append(bridgedPayloads, string(msg.Payload))
				mu.Unlock()
			},
		})
	}()

	time.Sleep(200 * time.Millisecond)

	// Send a message with non-matching tag — should NOT be bridged.
	_, err := source.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("ignored"),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("source.Send: %v", err)
	}

	// Send a message with matching tag — should be bridged.
	_, err = source.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("bridged"),
		Tags:       []string{"important"},
	})
	if err != nil {
		t.Fatalf("source.Send: %v", err)
	}

	// Wait for bridge to process the matching message.
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		count := len(bridgedPayloads)
		mu.Unlock()
		if count >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for bridged message")
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Give extra time to see if the "ignored" message leaks through.
	time.Sleep(1 * time.Second)

	mu.Lock()
	defer mu.Unlock()
	if len(bridgedPayloads) != 1 {
		t.Errorf("expected 1 bridged message, got %d: %v", len(bridgedPayloads), bridgedPayloads)
	}
	if len(bridgedPayloads) > 0 && bridgedPayloads[0] != "bridged" {
		t.Errorf("expected payload 'bridged', got %q", bridgedPayloads[0])
	}

	cancel()
}

func TestBridgeContextCancel(t *testing.T) {
	srcID, srcStore, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, srcID, srcStore, transportDir, campfire.RoleFull)
	source := protocol.New(srcStore, srcID)

	destID, destStore, _ := setupTestEnv(t)
	addMemberFS(t, destID, destStore, transportDir, campfireID)
	dest := protocol.New(destStore, destID)

	ctx, cancel := context.WithCancel(context.Background())

	bridgeErr := make(chan error, 1)
	go func() {
		bridgeErr <- protocol.Bridge(ctx, source, dest, campfireID, protocol.BridgeOptions{})
	}()

	// Let it start.
	time.Sleep(200 * time.Millisecond)

	cancel()

	// Bridge must return within 2 seconds.
	select {
	case err := <-bridgeErr:
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Bridge did not return within 2 seconds after context cancellation")
	}
}
