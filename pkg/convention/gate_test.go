package convention_test

// gate_test.go — convention executor provenance gate tests.
//
// Covered bead: campfire-agent-0ca
//
// Tests verify:
//   - TestConventionGateBlocks: a convention with min_operator_level=2 rejects a
//     sender at level 1 with a structured error (not panic, not silent drop).
//   - TestConventionGatePasses: a convention with min_operator_level=2 accepts a
//     sender at level 2 and dispatches the operation.
//
// These tests use the real convention executor dispatch path (not a test-local check)
// and a test-double transport (noopTransport from min_operator_level_test.go).

import (
	"context"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/convention"
)

// TestConventionGateBlocks verifies that a convention operation with
// min_operator_level=2 is rejected when the sender's provenance level is 1.
// The error must be structured (contain "operator provenance level") and no
// message must be sent.
func TestConventionGateBlocks(t *testing.T) {
	decl := parseGatedDecl(t, 2)

	transport := &noopTransport{}
	exec := convention.NewExecutorForTest(transport, provSenderKey).
		WithProvenance(&staticProvenanceChecker{
			levels: map[string]int{provSenderKey: 1},
		})

	_, err := exec.Execute(context.Background(), decl, "campfire-abc", map[string]any{
		"peer_key": strings.Repeat("a", 64),
	})

	if err == nil {
		t.Fatal("expected rejection at level 1 but got nil error")
	}
	if !strings.Contains(err.Error(), "operator provenance level") {
		t.Errorf("expected structured provenance error, got: %v", err)
	}
	// No message should have been sent — gate fires before dispatch.
	if len(transport.sent) != 0 {
		t.Errorf("expected no messages sent on rejection, got %d", len(transport.sent))
	}
}

// TestConventionGatePasses verifies that a convention operation with
// min_operator_level=2 is accepted when the sender's provenance level is 2.
// Exactly one message should be sent.
func TestConventionGatePasses(t *testing.T) {
	decl := parseGatedDecl(t, 2)

	transport := &noopTransport{}
	exec := convention.NewExecutorForTest(transport, provSenderKey).
		WithProvenance(&staticProvenanceChecker{
			levels: map[string]int{provSenderKey: 2},
		})

	_, err := exec.Execute(context.Background(), decl, "campfire-abc", map[string]any{
		"peer_key": strings.Repeat("a", 64),
	})

	if err != nil {
		t.Fatalf("expected success at level 2 but got error: %v", err)
	}
	if len(transport.sent) != 1 {
		t.Errorf("expected 1 message sent, got %d", len(transport.sent))
	}
}
