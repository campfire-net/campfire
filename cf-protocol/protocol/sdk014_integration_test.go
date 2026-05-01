package protocol_test

// sdk014_integration_test.go — E2E integration test for SDK 0.14:
// Identity as Infrastructure.
//
// Covered bead: campfire-agent-z0g
//
// Originally tested 6 outcomes; outcomes 1-4 (center creation, walk-up
// discovery, context key delegation, recentering) were removed in
// campfireagent-db1 — center-finding logic moves to L4 (cf-discovery).
//
// Remaining outcomes:
//   5. Provenance bridge: Bridge() between two clients produces IsBridged() == true and
//      LevelContactable via LevelFromMessage()
//   6. Convention gate: an executor with min_operator_level=2 rejects Level 1, accepts Level 2
//
// ALL real: real filesystem campfires, real Ed25519, real SQLite stores.
// NO mocks for crypto, transport, or campfire operations.
// The sdk014NoopTransport (phase 6 only) follows the same pattern as noopTransport in
// min_operator_level_test.go — used because convention.Execute needs a transport backend.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	"github.com/campfire-net/campfire/cf-conventions/cf-convention"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/pkg/provenance"
)

func TestSDK014_IdentityAsInfrastructure(t *testing.T) {
	// ── Phase 5: Provenance bridge tiers ──
	// Bridge() between two clients produces IsBridged() == true on the forwarded
	// message, and LevelFromMessage() returns LevelContactable (2).
	t.Log("Phase 5: Provenance bridge tiers")

	// Use a dedicated campfire for the bridge test.
	bridgeTransportDir := t.TempDir()
	srcID, srcStore, _ := setupTestEnv(t)
	bridgeCampfireID := setupFilesystemCampfire(t, srcID, srcStore, bridgeTransportDir, campfire.RoleFull)
	source := protocol.New(srcStore, srcID)

	destID, destStore, _ := setupTestEnv(t)
	addMemberFS(t, destID, destStore, bridgeTransportDir, bridgeCampfireID)
	dest := protocol.New(destStore, destID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		protocol.Bridge(ctx, source, dest, bridgeCampfireID, protocol.BridgeOptions{}) //nolint:errcheck
	}()
	time.Sleep(300 * time.Millisecond)

	_, err := source.Send(protocol.SendRequest{
		CampfireID: bridgeCampfireID,
		Payload:    []byte("sdk014 bridge provenance test"),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("Phase 5: source.Send: %v", err)
	}

	var bridgedMsg *protocol.Message
	deadline := time.After(10 * time.Second)
waitLoop:
	for {
		result, err := dest.Read(protocol.ReadRequest{CampfireID: bridgeCampfireID})
		if err != nil {
			t.Fatalf("Phase 5: dest.Read: %v", err)
		}
		for i := range result.Messages {
			msg := result.Messages[i]
			if string(msg.Payload) == "sdk014 bridge provenance test" && msg.Sender == destID.PublicKeyHex() {
				bridgedMsg = &msg
				break waitLoop
			}
		}
		select {
		case <-deadline:
			t.Fatal("Phase 5: timeout waiting for bridged message")
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}
	cancel()

	if !bridgedMsg.IsBridged() {
		t.Error("Phase 5: IsBridged() = false, want true")
	}

	hops := make([]provenance.MessageHop, len(bridgedMsg.Provenance))
	for i, h := range bridgedMsg.Provenance {
		hops[i] = provenance.MessageHop{Role: h.Role}
	}
	level := provenance.LevelFromMessage(hops, bridgedMsg.Sender, nil)
	if level != provenance.LevelContactable {
		t.Errorf("Phase 5: LevelFromMessage = %v (%d), want LevelContactable (%d)",
			level, int(level), int(provenance.LevelContactable))
	}
	t.Logf("Phase 5: IsBridged=true, level=%v (LevelContactable)", level)

	// ── Phase 6: Convention gate ──
	// An executor with min_operator_level=2 rejects a Level 1 message and
	// accepts a Level 2 message. Uses sdk014NoopTransport (test double).
	t.Log("Phase 6: Convention gate")

	const (
		sdk014SenderKey   = "aabbcc" + "0000000000000000000000000000000000000000000000000000000000"
		sdk014CampfireKey = "deadbeef" + "00000000000000000000000000000000000000000000000000000000"
	)

	declPayload, err := json.Marshal(map[string]any{
		"convention":         "peering",
		"version":            "0.3",
		"operation":          "sdk014-gate-test",
		"description":        "SDK 0.14 convention gate (requires level 2)",
		"min_operator_level": 2,
		"produces_tags": []any{
			map[string]any{"tag": "peering:core", "cardinality": "exactly_one"},
		},
		"args": []any{
			map[string]any{"name": "peer_key", "type": "string", "required": true, "max_length": 64},
		},
		"antecedents": "none",
		"signing":     "member_key",
	})
	if err != nil {
		t.Fatalf("Phase 6: marshal decl: %v", err)
	}

	decl, _, err := convention.Parse(
		[]string{convention.ConventionOperationTag},
		declPayload,
		sdk014SenderKey,
		sdk014CampfireKey,
	)
	if err != nil {
		t.Fatalf("Phase 6: parse decl: %v", err)
	}

	// Level 1 — must be rejected.
	tr1 := &sdk014NoopTransport{}
	ex1 := convention.NewExecutorForTest(tr1, sdk014SenderKey).
		WithProvenance(&sdk014StaticProvenance{levels: map[string]int{sdk014SenderKey: 1}})

	_, gateErr := ex1.Execute(context.Background(), decl, "campfire-sdk014", map[string]any{
		"peer_key": strings.Repeat("a", 64),
	})
	if gateErr == nil {
		t.Fatal("Phase 6: expected rejection at Level 1 but got nil error")
	}
	if !strings.Contains(gateErr.Error(), "operator provenance level") {
		t.Errorf("Phase 6: expected structured provenance error, got: %v", gateErr)
	}
	if len(tr1.sent) != 0 {
		t.Errorf("Phase 6: expected no messages sent on rejection, got %d", len(tr1.sent))
	}
	t.Log("Phase 6: Level 1 correctly rejected")

	// Level 2 — must be accepted.
	tr2 := &sdk014NoopTransport{}
	ex2 := convention.NewExecutorForTest(tr2, sdk014SenderKey).
		WithProvenance(&sdk014StaticProvenance{levels: map[string]int{sdk014SenderKey: 2}})

	_, acceptErr := ex2.Execute(context.Background(), decl, "campfire-sdk014", map[string]any{
		"peer_key": strings.Repeat("b", 64),
	})
	if acceptErr != nil {
		t.Fatalf("Phase 6: expected acceptance at Level 2, got error: %v", acceptErr)
	}
	if len(tr2.sent) != 1 {
		t.Errorf("Phase 6: expected 1 message sent at Level 2, got %d", len(tr2.sent))
	}
	t.Log("Phase 6: Level 2 correctly accepted")

	t.Log("SDK 0.14 E2E: outcomes 5+6 verified")
}

// ---------------------------------------------------------------------------
// Test doubles for Phase 6 convention gate.
// Named with sdk014 prefix to avoid collision with noopTransport and
// staticProvenanceChecker in min_operator_level_test.go (same package).
// ---------------------------------------------------------------------------

type sdk014NoopTransport struct {
	sent []struct{ tags []string }
}

func (n *sdk014NoopTransport) SendMessage(_ context.Context, _ string, _ []byte, tags []string, _ []string) (string, error) {
	n.sent = append(n.sent, struct{ tags []string }{tags})
	return "msg-id", nil
}

func (n *sdk014NoopTransport) SendCampfireKeySigned(_ context.Context, _ string, _ []byte, tags []string, _ []string) (string, error) {
	n.sent = append(n.sent, struct{ tags []string }{tags})
	return "msg-id-ck", nil
}

func (n *sdk014NoopTransport) ReadMessages(_ context.Context, _ string, _ []string) ([]convention.MessageRecord, error) {
	return nil, nil
}

func (n *sdk014NoopTransport) SendFutureAndAwait(_ context.Context, _ string, _ []byte, _ []string, _ []string, _ time.Duration) (string, []byte, error) {
	return "", nil, nil
}

type sdk014StaticProvenance struct {
	levels map[string]int
}

func (s *sdk014StaticProvenance) Level(key string) int {
	if l, ok := s.levels[key]; ok {
		return l
	}
	return 0
}
