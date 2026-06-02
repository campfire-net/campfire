package protocol_test

// Tests for protocol.Bridge — campfire-agent-utj.
//
// All tests use real filesystem transport clients. No mocks.
// Two separate identities share a single transport directory.

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	cfencoding "github.com/campfire-net/campfire/cf-protocol/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
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

// setupForwardTestEnv creates a pair of clients for pass-through bridge tests.
// Unlike the shared-transport setup used by existing bridge tests, forward
// pass-through requires separate transport directories for source and dest:
// the source transport is where the original message is written; the dest
// transport is where the bridge writes the forwarded copy. Without separation,
// the dest would sync the original (no blind-relay hop) before the bridge
// appends the hop, and the store's INSERT-OR-IGNORE would prevent the updated
// version from landing.
//
// Returns (source, dest, campfireID). Both clients are members of the same
// campfire. The campfire state (key material for hop verification) is written
// to both transport directories.
func setupForwardTestEnv(t *testing.T) (source, dest *protocol.Client, campfireID string) {
	t.Helper()

	// Source identity + store + transport dir.
	srcID, srcStore, srcTransportDir := setupTestEnv(t)
	campfireID = setupFilesystemCampfire(t, srcID, srcStore, srcTransportDir, campfire.RoleFull)

	// Dest identity + store + its own transport dir.
	_, destStore, destTransportDir := setupTestEnv(t)
	destID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating dest identity: %v", err)
	}

	// Read the campfire state from the source transport and copy it into the
	// dest transport so dest can verify provenance hops (hop signature is over
	// the campfire's public key, which lives in campfire.cbor).
	srcCampfireDir := filepath.Join(srcTransportDir, campfireID)
	destCampfireDir := filepath.Join(destTransportDir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(destCampfireDir, sub), 0755); err != nil {
			t.Fatalf("creating dest %s directory: %v", sub, err)
		}
	}
	stateData, err := os.ReadFile(filepath.Join(srcCampfireDir, "campfire.cbor"))
	if err != nil {
		t.Fatalf("reading campfire state from source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destCampfireDir, "campfire.cbor"), stateData, 0644); err != nil {
		t.Fatalf("writing campfire state to dest: %v", err)
	}

	// Read the state to get the original CampfireState for decoding.
	var cfState campfire.CampfireState
	if err := cfencoding.Unmarshal(stateData, &cfState); err != nil {
		t.Fatalf("unmarshalling campfire state: %v", err)
	}

	// Add dest identity as a member in the dest transport and record membership.
	destTr := fs.New(destTransportDir)
	if err := destTr.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: destID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("writing dest member record: %v", err)
	}
	if err := destStore.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  destTr.CampfireDir(campfireID),
		JoinProtocol:  cfState.JoinProtocol,
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	}); err != nil {
		t.Fatalf("adding dest membership: %v", err)
	}

	source = protocol.New(srcStore, srcID)
	dest = protocol.New(destStore, destID)
	return source, dest, campfireID
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
	deadline := time.After(10 * time.Second)
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

	bridgeErr := make(chan error, 1)
	go func() {
		bridgeErr <- protocol.Bridge(ctx, source, dest, campfireID, protocol.BridgeOptions{
			Bidirectional: true,
		})
	}()

	time.Sleep(1 * time.Second) // allow Subscribe goroutines to start — poll interval is 500ms

	// Send a uniquely-tagged message from source.
	_, err := source.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("from source"),
		Tags:       []string{"bidir-src"},
	})
	if err != nil {
		t.Fatalf("source.Send: %v", err)
	}

	// Send a uniquely-tagged message from dest.
	_, err = dest.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("from dest"),
		Tags:       []string{"bidir-dest"},
	})
	if err != nil {
		t.Fatalf("dest.Send: %v", err)
	}

	// Verify delivery via Read polling: source's message should appear readable
	// by dest (bridged), and dest's message should appear readable by source.
	deadline := time.After(15 * time.Second)
	var gotSrcOnDest, gotDestOnSrc bool
	for !gotSrcOnDest || !gotDestOnSrc {
		select {
		case <-deadline:
			t.Fatalf("timeout: gotSrcOnDest=%v gotDestOnSrc=%v", gotSrcOnDest, gotDestOnSrc)
		default:
		}

		if !gotSrcOnDest {
			result, _ := dest.Read(protocol.ReadRequest{CampfireID: campfireID, Tags: []string{"bidir-src"}})
			if result != nil && len(result.Messages) > 0 {
				// The bridged copy is re-sent by the bridge agent (dest identity), not the original sender.
				// Just check it arrived.
				gotSrcOnDest = true
			}
		}
		if !gotDestOnSrc {
			result, _ := source.Read(protocol.ReadRequest{CampfireID: campfireID, Tags: []string{"bidir-dest"}})
			if result != nil && len(result.Messages) > 0 {
				gotDestOnSrc = true
			}
		}
		if !gotSrcOnDest || !gotDestOnSrc {
			time.Sleep(500 * time.Millisecond)
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

	// Wait for the message to be bridged, then confirm it was bridged exactly
	// once. A fixed sleep before the assertion is flaky (campfire-6e6): under load
	// the bridge's poll + sync + forward can exceed the window, yielding a spurious
	// "got 0". Instead poll (with a deadline) until the first forward lands, then
	// settle briefly to confirm dedup prevented a loop-back spiral.
	deadline := time.After(10 * time.Second)
	for {
		mu.Lock()
		c := bridgedCount
		mu.Unlock()
		if c >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for bridged message")
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}

	// Settle: allow any erroneous loop-back forward to occur (dedup must prevent
	// the forwarded message from looping back dest→source), then assert the
	// message was bridged exactly once (source→dest).
	time.Sleep(1 * time.Second)

	mu.Lock()
	count := bridgedCount
	mu.Unlock()
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
	deadline := time.After(10 * time.Second)
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

// TestBridgeForwardPreservesIdentity verifies that pass-through mode (Forward:
// true) delivers the original message ID and Sender to the destination — i.e.
// the original-author cryptographic attribution is preserved end-to-end.
// The destination message must carry IsBridged() == true (blind-relay hop
// appended by the bridge).
func TestBridgeForwardPreservesIdentity(t *testing.T) {
	source, dest, campfireID := setupForwardTestEnv(t)
	srcSenderHex := source.PublicKeyHex()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bridgeErr := make(chan error, 1)
	go func() {
		bridgeErr <- protocol.Bridge(ctx, source, dest, campfireID, protocol.BridgeOptions{
			Forward: true,
		})
	}()

	// Give bridge time to subscribe.
	time.Sleep(300 * time.Millisecond)

	// Send from source.
	sent, err := source.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("forward preserves identity"),
		Tags:       []string{"forward-test"},
	})
	if err != nil {
		t.Fatalf("source.Send: %v", err)
	}
	origID := sent.ID

	// Wait for the forwarded message to appear on dest.
	var fwdMsg *protocol.Message
	deadline := time.After(10 * time.Second)
	for {
		result, err := dest.Read(protocol.ReadRequest{
			CampfireID: campfireID,
			Tags:       []string{"forward-test"},
		})
		if err != nil {
			t.Fatalf("dest.Read: %v", err)
		}
		for i := range result.Messages {
			m := result.Messages[i]
			if m.ID == origID {
				fwdMsg = &m
				break
			}
		}
		if fwdMsg != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout waiting for forwarded message on dest")
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}

	cancel()

	// Core assertions: identity preserved, bridge hop appended.
	if fwdMsg.ID != origID {
		t.Errorf("forward: message ID changed: got %q, want %q", fwdMsg.ID, origID)
	}
	if fwdMsg.Sender != srcSenderHex {
		t.Errorf("forward: Sender changed: got %q, want %q", fwdMsg.Sender, srcSenderHex)
	}
	if string(fwdMsg.Payload) != "forward preserves identity" {
		t.Errorf("forward: payload changed: got %q", string(fwdMsg.Payload))
	}
	if !fwdMsg.IsBridged() {
		t.Error("forward: IsBridged() = false, want true — pass-through must append a blind-relay hop")
	}
}

// TestBridgeForwardVsRepublish verifies the behavioral difference between
// Forward=true (pass-through) and Forward=false (re-publish default):
// - Forward=false: delivered message has a new ID and Sender = bridge agent.
// - Forward=true: delivered message keeps the original ID and Sender.
//
// The forward sub-test uses separate transport directories (setupForwardTestEnv)
// so the dest store sees only the forwarded copy (with blind-relay hop) and not
// the original written to the source transport.
//
// The republish sub-test uses the shared-transport setup because re-publish
// produces a fresh message ID; the dest store uniquely identifies the bridged
// copy and dedup does not interfere.
func TestBridgeForwardVsRepublish(t *testing.T) {
	t.Run("forward", func(t *testing.T) {
		source, dest, campfireID := setupForwardTestEnv(t)
		srcSenderHex := source.PublicKeyHex()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go func() {
			_ = protocol.Bridge(ctx, source, dest, campfireID, protocol.BridgeOptions{
				Forward: true,
			})
		}()

		time.Sleep(300 * time.Millisecond)

		tag := "fvr-forward"
		sent, err := source.Send(protocol.SendRequest{
			CampfireID: campfireID,
			Payload:    []byte("mode test: forward"),
			Tags:       []string{tag},
		})
		if err != nil {
			t.Fatalf("source.Send: %v", err)
		}

		var delivered *protocol.Message
		deadline := time.After(10 * time.Second)
		for {
			result, err := dest.Read(protocol.ReadRequest{
				CampfireID: campfireID,
				Tags:       []string{tag},
			})
			if err != nil {
				t.Fatalf("dest.Read: %v", err)
			}
			for i := range result.Messages {
				m := result.Messages[i]
				if string(m.Payload) == "mode test: forward" {
					delivered = &m
					break
				}
			}
			if delivered != nil {
				break
			}
			select {
			case <-deadline:
				t.Fatal("timeout waiting for message on dest")
			default:
				time.Sleep(100 * time.Millisecond)
			}
		}
		cancel()

		if delivered.ID != sent.ID {
			t.Errorf("forward mode: ID changed: got %q, want %q", delivered.ID, sent.ID)
		}
		if delivered.Sender != srcSenderHex {
			t.Errorf("forward mode: Sender changed: got %q, want original sender %q", delivered.Sender, srcSenderHex)
		}
		if !delivered.IsBridged() {
			t.Error("forward mode: IsBridged() = false, want true")
		}
	})

	t.Run("republish", func(t *testing.T) {
		// Re-publish uses shared transport: the bridged copy has a new ID so dedup
		// does not prevent it from reaching dest.
		srcID, srcStore, transportDir := setupTestEnv(t)
		campfireID := setupFilesystemCampfire(t, srcID, srcStore, transportDir, campfire.RoleFull)
		source := protocol.New(srcStore, srcID)

		destID, destStore, _ := setupTestEnv(t)
		addMemberFS(t, destID, destStore, transportDir, campfireID)
		dest := protocol.New(destStore, destID)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go func() {
			_ = protocol.Bridge(ctx, source, dest, campfireID, protocol.BridgeOptions{
				Forward: false,
			})
		}()

		time.Sleep(300 * time.Millisecond)

		tag := "fvr-republish"
		sent, err := source.Send(protocol.SendRequest{
			CampfireID: campfireID,
			Payload:    []byte("mode test: republish"),
			Tags:       []string{tag},
		})
		if err != nil {
			t.Fatalf("source.Send: %v", err)
		}

		// Wait for the bridge-agent's copy (different sender = destID).
		var delivered *protocol.Message
		deadline := time.After(10 * time.Second)
		for {
			result, err := dest.Read(protocol.ReadRequest{
				CampfireID: campfireID,
				Tags:       []string{tag},
			})
			if err != nil {
				t.Fatalf("dest.Read: %v", err)
			}
			for i := range result.Messages {
				m := result.Messages[i]
				// In re-publish mode the bridge agent (destID) is the new sender.
				if string(m.Payload) == "mode test: republish" && m.Sender == destID.PublicKeyHex() {
					delivered = &m
					break
				}
			}
			if delivered != nil {
				break
			}
			select {
			case <-deadline:
				t.Fatal("timeout waiting for re-published message on dest")
			default:
				time.Sleep(100 * time.Millisecond)
			}
		}
		cancel()

		if delivered.ID == sent.ID {
			t.Error("republish mode: ID must differ from original (bridge creates a new message)")
		}
		if delivered.Sender == srcID.PublicKeyHex() {
			t.Errorf("republish mode: Sender must be bridge agent, got original sender %q", delivered.Sender)
		}
		if !delivered.IsBridged() {
			t.Error("republish mode: IsBridged() = false, want true")
		}
	})
}

// TestBridgeForwardUnreachableTarget verifies that when Forward is true and
// the destination membership is missing (unreachable/unconfigured), the bridge
// returns a graceful error and does not panic.
func TestBridgeForwardUnreachableTarget(t *testing.T) {
	srcID, srcStore, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, srcID, srcStore, transportDir, campfire.RoleFull)
	source := protocol.New(srcStore, srcID)

	// dest has no membership for campfireID — simulates unreachable/unconfigured target.
	_, destStore, _ := setupTestEnv(t)
	destID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating dest identity: %v", err)
	}
	dest := protocol.New(destStore, destID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bridgeErr := make(chan error, 1)
	go func() {
		bridgeErr <- protocol.Bridge(ctx, source, dest, campfireID, protocol.BridgeOptions{
			Forward: true,
		})
	}()

	// Give bridge time to subscribe.
	time.Sleep(300 * time.Millisecond)

	// Send a message — the bridge will attempt to forward to dest but dest has no
	// membership, so forwardMessage must return an error.
	_, sendErr := source.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("unreachable target test"),
		Tags:       []string{"status"},
	})
	if sendErr != nil {
		t.Fatalf("source.Send: %v", sendErr)
	}

	// The bridge must return a descriptive error, not hang or panic.
	select {
	case err := <-bridgeErr:
		if err == nil {
			t.Error("expected error from Bridge with unreachable forward target, got nil")
		}
		// Accept context.Canceled only if ctx was cancelled — here we haven't
		// cancelled it, so a non-nil non-Canceled error is the expected outcome.
		if err == context.Canceled {
			t.Error("expected a delivery error, not context.Canceled — bridge should fail on forward error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Bridge did not return within 10 seconds after forward failure")
	}
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
