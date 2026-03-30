package convention_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/convention"
)

// Test keys used throughout this file.
const (
	provSenderKey   = "aabbcc" + "0000000000000000000000000000000000000000000000000000000000"
	provCampfireKey = "deadbeef" + "00000000000000000000000000000000000000000000000000000000"
)

// staticProvenanceChecker is a simple ProvenanceChecker that returns a fixed level for a given key.
type staticProvenanceChecker struct {
	levels map[string]int
}

func (s *staticProvenanceChecker) Level(key string) int {
	if l, ok := s.levels[key]; ok {
		return l
	}
	return 0
}

// noopTransport implements convention.ExecutorBackend but records nothing; used for gate tests.
type noopTransport struct {
	sent []struct{ tags []string }
}

func (n *noopTransport) SendMessage(_ context.Context, _ string, _ []byte, tags []string, _ []string) (string, error) {
	n.sent = append(n.sent, struct{ tags []string }{tags})
	return "msg-id", nil
}

func (n *noopTransport) SendCampfireKeySigned(_ context.Context, _ string, _ []byte, tags []string, _ []string) (string, error) {
	n.sent = append(n.sent, struct{ tags []string }{tags})
	return "msg-id-ck", nil
}

func (n *noopTransport) ReadMessages(_ context.Context, _ string, _ []string) ([]convention.MessageRecord, error) {
	return nil, nil
}

func (n *noopTransport) SendFutureAndAwait(_ context.Context, _ string, _ []byte, _ []string, _ []string, _ time.Duration) (string, []byte, error) {
	return "", nil, nil
}

// parseGatedDecl builds a Declaration with min_operator_level set to minLevel.
func parseGatedDecl(t *testing.T, minLevel int) *convention.Declaration {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"convention":         "peering",
		"version":            "0.3",
		"operation":          "core-peer-establish",
		"description":        "Establish a core peering link (requires level 2 operator)",
		"min_operator_level": minLevel,
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
		t.Fatalf("marshal gated decl: %v", err)
	}

	tags := []string{convention.ConventionOperationTag}
	decl, _, err := convention.Parse(tags, payload, provSenderKey, provCampfireKey)
	if err != nil {
		t.Fatalf("parse gated decl: %v", err)
	}
	return decl
}

// TestMinOperatorLevel_RejectedWhenLevelInsufficient verifies that an operation
// with min_operator_level=2 is rejected when the sender's level is 1.
func TestMinOperatorLevel_RejectedWhenLevelInsufficient(t *testing.T) {
	decl := parseGatedDecl(t, 2)

	transport := &noopTransport{}
	exec := convention.NewExecutorForTest(transport, provSenderKey)
	exec = exec.WithProvenance(&staticProvenanceChecker{
		levels: map[string]int{provSenderKey: 1},
	})

	_, err := exec.Execute(context.Background(), decl, "campfire-abc", map[string]any{
		"peer_key": strings.Repeat("a", 64),
	})

	if err == nil {
		t.Fatal("expected rejection but got nil error")
	}
	if !strings.Contains(err.Error(), "operator provenance level") {
		t.Errorf("expected provenance error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "requires level 2") {
		t.Errorf("expected 'requires level 2' in error, got: %v", err)
	}
	// No message should have been sent.
	if len(transport.sent) != 0 {
		t.Errorf("expected no messages sent on rejection, got %d", len(transport.sent))
	}
}

// TestMinOperatorLevel_AcceptedWhenLevelSufficient verifies that an operation
// with min_operator_level=2 is accepted when the sender's level is 2.
func TestMinOperatorLevel_AcceptedWhenLevelSufficient(t *testing.T) {
	decl := parseGatedDecl(t, 2)

	transport := &noopTransport{}
	exec := convention.NewExecutorForTest(transport, provSenderKey)
	exec = exec.WithProvenance(&staticProvenanceChecker{
		levels: map[string]int{provSenderKey: 2},
	})

	_, err := exec.Execute(context.Background(), decl, "campfire-abc", map[string]any{
		"peer_key": strings.Repeat("a", 64),
	})

	if err != nil {
		t.Fatalf("expected success but got error: %v", err)
	}
	if len(transport.sent) != 1 {
		t.Errorf("expected 1 message sent, got %d", len(transport.sent))
	}
}

// TestMinOperatorLevel_AcceptedWhenLevelExceedsMinimum verifies that level 3
// satisfies a min_operator_level=2 gate.
func TestMinOperatorLevel_AcceptedWhenLevelExceedsMinimum(t *testing.T) {
	decl := parseGatedDecl(t, 2)

	transport := &noopTransport{}
	exec := convention.NewExecutorForTest(transport, provSenderKey)
	exec = exec.WithProvenance(&staticProvenanceChecker{
		levels: map[string]int{provSenderKey: 3},
	})

	_, err := exec.Execute(context.Background(), decl, "campfire-abc", map[string]any{
		"peer_key": strings.Repeat("a", 64),
	})

	if err != nil {
		t.Fatalf("expected success (level 3 >= 2) but got error: %v", err)
	}
}

// TestMinOperatorLevel_ZeroGateAllowsAny verifies that min_operator_level=0 (default)
// allows senders with any level, including level 0 (anonymous).
func TestMinOperatorLevel_ZeroGateAllowsAny(t *testing.T) {
	decl := parseGatedDecl(t, 0)

	transport := &noopTransport{}
	// No provenance checker needed — zero gate skips the check entirely.
	exec := convention.NewExecutorForTest(transport, provSenderKey)

	_, err := exec.Execute(context.Background(), decl, "campfire-abc", map[string]any{
		"peer_key": strings.Repeat("a", 64),
	})

	if err != nil {
		t.Fatalf("expected success with zero gate, got: %v", err)
	}
}

// TestMinOperatorLevel_NoCheckerDefaultsToZero verifies that an operation with
// min_operator_level>0 but no ProvenanceChecker attached defaults sender level to 0
// and is rejected.
func TestMinOperatorLevel_NoCheckerDefaultsToZero(t *testing.T) {
	decl := parseGatedDecl(t, 2)

	transport := &noopTransport{}
	exec := convention.NewExecutorForTest(transport, provSenderKey)
	// No WithProvenance call — sender defaults to level 0.

	_, err := exec.Execute(context.Background(), decl, "campfire-abc", map[string]any{
		"peer_key": strings.Repeat("a", 64),
	})

	if err == nil {
		t.Fatal("expected rejection when no checker and min_operator_level=2")
	}
	if !strings.Contains(err.Error(), "operator provenance level") {
		t.Errorf("expected provenance error, got: %v", err)
	}
}

// TestMinOperatorLevel_ParseRoundTrip verifies that min_operator_level is preserved
// through JSON encoding/decoding of the Declaration.
func TestMinOperatorLevel_ParseRoundTrip(t *testing.T) {
	decl := parseGatedDecl(t, 2)
	if decl.MinOperatorLevel != 2 {
		t.Errorf("expected MinOperatorLevel=2, got %d", decl.MinOperatorLevel)
	}
}

// TestMinOperatorLevel_ErrorContainsOperationName verifies the error message
// names the failing operation.
func TestMinOperatorLevel_ErrorContainsOperationName(t *testing.T) {
	decl := parseGatedDecl(t, 2)

	exec := convention.NewExecutorForTest(&noopTransport{}, provSenderKey).
		WithProvenance(&staticProvenanceChecker{levels: map[string]int{provSenderKey: 0}})

	_, err := exec.Execute(context.Background(), decl, "campfire-abc", map[string]any{
		"peer_key": strings.Repeat("a", 64),
	})

	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "peering:core-peer-establish") {
		t.Errorf("expected operation name in error, got: %v", err)
	}
}

// TestMinOperatorLevel_WorkflowRejected verifies that a workflow (multi-step)
// operation with min_operator_level is also rejected at the gate.
func TestMinOperatorLevel_WorkflowRejected(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"convention":         "peering",
		"version":            "0.3",
		"operation":          "core-peer-workflow",
		"description":        "Multi-step core peering requiring level 2",
		"min_operator_level": 2,
		"signing":            "member_key",
		"steps": []any{
			map[string]any{
				"action":         "query",
				"description":    "Look up peer state",
				"future_tags":    []any{"peering:state"},
				"result_binding": "state",
			},
			map[string]any{
				"action":      "send",
				"description": "Establish peer",
				"tags":        []any{"peering:core"},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal workflow decl: %v", err)
	}

	tags := []string{convention.ConventionOperationTag}
	decl, _, parseErr := convention.Parse(tags, payload, provSenderKey, provCampfireKey)
	if parseErr != nil {
		t.Fatalf("parse workflow decl: %v", parseErr)
	}

	transport := &noopTransport{}
	exec := convention.NewExecutorForTest(transport, provSenderKey).
		WithProvenance(&staticProvenanceChecker{levels: map[string]int{provSenderKey: 1}})

	_, execErr := exec.Execute(context.Background(), decl, "campfire-abc", nil)
	if execErr == nil {
		t.Fatal("expected workflow to be rejected at level 1 gate")
	}
	if !errors.Is(execErr, execErr) || !strings.Contains(execErr.Error(), "operator provenance level") {
		t.Errorf("expected provenance error, got: %v", execErr)
	}
	if len(transport.sent) != 0 {
		t.Errorf("expected no messages sent on workflow rejection, got %d", len(transport.sent))
	}
}
