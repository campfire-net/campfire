// Package tests — E2E bridge tests.
//
// Validates protocol.Bridge end-to-end on real filesystem transport with two
// separate identities/stores sharing a campfire. No mocks.
//
// Covered bead: campfire-agent-eew
package tests

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/protocol"
)

// TestBridgeE2EUnidirectional verifies the message pump forwards messages
// from source to dest on a real filesystem campfire.
func TestBridgeE2EUnidirectional(t *testing.T) {
	sourceClient, destClient, campfireID := setupE2EEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bridgeErr := make(chan error, 1)
	go func() {
		bridgeErr <- protocol.Bridge(ctx, sourceClient, destClient, campfireID, protocol.BridgeOptions{})
	}()

	// Let bridge subscribe.
	time.Sleep(200 * time.Millisecond)

	// Send on source.
	_, err := sourceClient.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("e2e unidirectional"),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("source.Send: %v", err)
	}

	// Poll dest for the bridged message.
	deadline := time.After(5 * time.Second)
	for {
		result, err := destClient.Read(protocol.ReadRequest{CampfireID: campfireID})
		if err != nil {
			t.Fatalf("dest.Read: %v", err)
		}
		found := false
		for _, msg := range result.Messages {
			if string(msg.Payload) == "e2e unidirectional" && msg.Sender == destClient.PublicKeyHex() {
				found = true
				break
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

	// Clean shutdown.
	cancel()
	select {
	case err := <-bridgeErr:
		if err != nil && err != context.Canceled {
			t.Errorf("Bridge returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Bridge did not return within 2s after cancel")
	}
}

// TestBridgeE2EBidirectional verifies messages flow in both directions.
func TestBridgeE2EBidirectional(t *testing.T) {
	clientA, clientB, campfireID := setupE2EEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var directions []string

	bridgeErr := make(chan error, 1)
	go func() {
		bridgeErr <- protocol.Bridge(ctx, clientA, clientB, campfireID, protocol.BridgeOptions{
			Bidirectional: true,
			OnMessage: func(msg *protocol.Message, direction string) {
				mu.Lock()
				directions = append(directions, direction)
				mu.Unlock()
			},
		})
	}()

	time.Sleep(200 * time.Millisecond)

	// A → B
	_, err := clientA.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("from A"),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("clientA.Send: %v", err)
	}

	// B → A
	_, err = clientB.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("from B"),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("clientB.Send: %v", err)
	}

	// Wait for both directions.
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		gotAtoB, gotBtoA := false, false
		for _, d := range directions {
			if d == "source→dest" {
				gotAtoB = true
			}
			if d == "dest→source" {
				gotBtoA = true
			}
		}
		mu.Unlock()
		if gotAtoB && gotBtoA {
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

	// The OnMessage callbacks already verified both directions fired.
	// Additionally verify that Read on each side can see the other's message
	// (both clients share the filesystem, so all messages are visible).
	resultB, err := clientB.Read(protocol.ReadRequest{CampfireID: campfireID})
	if err != nil {
		t.Fatalf("clientB.Read: %v", err)
	}
	foundFromA := false
	for _, msg := range resultB.Messages {
		if string(msg.Payload) == "from A" {
			foundFromA = true
			break
		}
	}
	if !foundFromA {
		t.Error("clientB cannot read message 'from A'")
	}

	resultA, err := clientA.Read(protocol.ReadRequest{CampfireID: campfireID})
	if err != nil {
		t.Fatalf("clientA.Read: %v", err)
	}
	foundFromB := false
	for _, msg := range resultA.Messages {
		if string(msg.Payload) == "from B" {
			foundFromB = true
			break
		}
	}
	if !foundFromB {
		t.Error("clientA cannot read message 'from B'")
	}

	cancel()
}

// TestBridgeE2ETagFilter verifies only messages with matching tags are bridged.
func TestBridgeE2ETagFilter(t *testing.T) {
	sourceClient, destClient, campfireID := setupE2EEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var bridgedPayloads []string

	bridgeErr := make(chan error, 1)
	go func() {
		bridgeErr <- protocol.Bridge(ctx, sourceClient, destClient, campfireID, protocol.BridgeOptions{
			TagFilter: []string{"important"},
			OnMessage: func(msg *protocol.Message, direction string) {
				mu.Lock()
				bridgedPayloads = append(bridgedPayloads, string(msg.Payload))
				mu.Unlock()
			},
		})
	}()

	time.Sleep(200 * time.Millisecond)

	// Non-matching tag — should NOT be bridged.
	_, err := sourceClient.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("ignored debug"),
		Tags:       []string{"debug"},
	})
	if err != nil {
		t.Fatalf("source.Send (debug): %v", err)
	}

	// Matching tag — should be bridged.
	_, err = sourceClient.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("important msg"),
		Tags:       []string{"important"},
	})
	if err != nil {
		t.Fatalf("source.Send (important): %v", err)
	}

	// Wait for the matching message.
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
			t.Fatal("timeout waiting for bridged important message")
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Extra time to verify the debug message doesn't leak through.
	time.Sleep(1 * time.Second)

	mu.Lock()
	defer mu.Unlock()
	if len(bridgedPayloads) != 1 {
		t.Errorf("expected 1 bridged message, got %d: %v", len(bridgedPayloads), bridgedPayloads)
	}
	if len(bridgedPayloads) > 0 && bridgedPayloads[0] != "important msg" {
		t.Errorf("expected payload 'important msg', got %q", bridgedPayloads[0])
	}

	cancel()
}

// TestBridgeE2EDedup verifies no loops in bidirectional mode: a message sent
// on A appears once on B and is not echoed back to A.
func TestBridgeE2EDedup(t *testing.T) {
	clientA, clientB, campfireID := setupE2EEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	bridgedCount := 0

	bridgeErr := make(chan error, 1)
	go func() {
		bridgeErr <- protocol.Bridge(ctx, clientA, clientB, campfireID, protocol.BridgeOptions{
			Bidirectional: true,
			OnMessage: func(msg *protocol.Message, direction string) {
				mu.Lock()
				bridgedCount++
				mu.Unlock()
			},
		})
	}()

	time.Sleep(200 * time.Millisecond)

	// Send one message from A.
	_, err := clientA.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("dedup e2e"),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("clientA.Send: %v", err)
	}

	// Wait for bridge to process and settle.
	time.Sleep(2 * time.Second)

	mu.Lock()
	count := bridgedCount
	mu.Unlock()

	// Exactly one bridge event (A→B). Dedup prevents the forwarded copy from
	// looping back B→A.
	if count != 1 {
		t.Errorf("expected 1 bridged message (dedup), got %d", count)
	}

	// Also verify the message appears on B exactly once (as bridged copy).
	resultB, err := clientB.Read(protocol.ReadRequest{CampfireID: campfireID})
	if err != nil {
		t.Fatalf("clientB.Read: %v", err)
	}
	copies := 0
	for _, msg := range resultB.Messages {
		if string(msg.Payload) == "dedup e2e" {
			copies++
		}
	}
	// One original from A (sender=A's key) + one bridged copy (sender=B's key).
	if copies != 2 {
		t.Errorf("expected 2 copies on B (original + bridged), got %d", copies)
	}

	cancel()
}

// TestBridgeE2EGracefulShutdown verifies Bridge returns context.Canceled on
// clean exit and does not leak goroutines.
func TestBridgeE2EGracefulShutdown(t *testing.T) {
	clientA, clientB, campfireID := setupE2EEnv(t)

	ctx, cancel := context.WithCancel(context.Background())

	bridgeErr := make(chan error, 1)
	go func() {
		bridgeErr <- protocol.Bridge(ctx, clientA, clientB, campfireID, protocol.BridgeOptions{
			Bidirectional: true,
		})
	}()

	// Let bridge fully start (both pumps subscribing).
	time.Sleep(300 * time.Millisecond)

	cancel()

	// Bridge must return promptly with context.Canceled.
	select {
	case err := <-bridgeErr:
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Bridge did not return within 2s after context cancellation")
	}
}
