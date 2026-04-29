package trust_test

import (
	"errors"
	"testing"

	"github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"
)

// ---------------------------------------------------------------------------
// Condition (a): 'not:' AST node MUST be rejected at parse time
// ---------------------------------------------------------------------------

// TestParsePredicateAST_NotRejectedAtRoot verifies that a 'not:' predicate at
// the root level is rejected with ErrNotPredicateRejected (spec §2.2).
func TestParsePredicateAST_NotRejectedAtRoot(t *testing.T) {
	raw := map[string]any{
		"kind": "not",
		"children": []any{
			map[string]any{"kind": "level", "n": 2},
		},
	}
	_, err := trust.ParsePredicateAST(raw)
	if err == nil {
		t.Fatal("expected error for 'not:' predicate at root; got nil")
	}
	if !errors.Is(err, trust.ErrNotPredicateRejected) {
		t.Fatalf("expected ErrNotPredicateRejected; got %v", err)
	}
}

// TestParsePredicateAST_NotRejectedNestedInAllOf verifies that 'not:' nested
// inside an 'all_of' is also rejected (adversarial: depth-nested case).
func TestParsePredicateAST_NotRejectedNestedInAllOf(t *testing.T) {
	raw := map[string]any{
		"kind": "all_of",
		"children": []any{
			map[string]any{"kind": "level", "n": 1},
			map[string]any{
				"kind": "not",
				"children": []any{
					map[string]any{"kind": "level", "n": 2},
				},
			},
		},
	}
	_, err := trust.ParsePredicateAST(raw)
	if err == nil {
		t.Fatal("expected error for 'not:' nested inside all_of; got nil")
	}
	if !errors.Is(err, trust.ErrNotPredicateRejected) {
		t.Fatalf("expected ErrNotPredicateRejected; got %v", err)
	}
}

// TestParsePredicateAST_NotRejectedNestedInAnyOf verifies that 'not:' nested
// inside an 'any_of' is also rejected.
func TestParsePredicateAST_NotRejectedNestedInAnyOf(t *testing.T) {
	raw := map[string]any{
		"kind": "any_of",
		"children": []any{
			map[string]any{
				"kind": "not",
				"children": []any{
					map[string]any{"kind": "grant", "convention": "ready", "op": "claim"},
				},
			},
		},
	}
	_, err := trust.ParsePredicateAST(raw)
	if err == nil {
		t.Fatal("expected error for 'not:' nested inside any_of; got nil")
	}
	if !errors.Is(err, trust.ErrNotPredicateRejected) {
		t.Fatalf("expected ErrNotPredicateRejected; got %v", err)
	}
}

// TestParsePredicateAST_NotRejectedDeeplyNested verifies that 'not:' at depth 2
// (inside all_of inside any_of) is rejected.
func TestParsePredicateAST_NotRejectedDeeplyNested(t *testing.T) {
	raw := map[string]any{
		"kind": "any_of",
		"children": []any{
			map[string]any{
				"kind": "all_of",
				"children": []any{
					map[string]any{
						"kind": "not",
						"children": []any{
							map[string]any{"kind": "level", "n": 0},
						},
					},
				},
			},
		},
	}
	_, err := trust.ParsePredicateAST(raw)
	if err == nil {
		t.Fatal("expected error for 'not:' deeply nested; got nil")
	}
	if !errors.Is(err, trust.ErrNotPredicateRejected) {
		t.Fatalf("expected ErrNotPredicateRejected; got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Valid predicate parsing (positive cases)
// ---------------------------------------------------------------------------

func TestParsePredicateAST_LevelPredicate(t *testing.T) {
	raw := map[string]any{"kind": "level", "n": float64(2)}
	ast, err := trust.ParsePredicateAST(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ast.Kind != trust.KindLevel {
		t.Fatalf("expected KindLevel; got %v", ast.Kind)
	}
	if ast.Leaf == nil || ast.Leaf.LevelN != 2 {
		t.Fatalf("expected LevelN=2; got %v", ast.Leaf)
	}
}

func TestParsePredicateAST_GrantPredicate(t *testing.T) {
	raw := map[string]any{"kind": "grant", "convention": "ready", "op": "claim"}
	ast, err := trust.ParsePredicateAST(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ast.Kind != trust.KindGrant {
		t.Fatalf("expected KindGrant; got %v", ast.Kind)
	}
	if ast.Leaf.GrantConvention != "ready" || ast.Leaf.GrantOp != "claim" {
		t.Fatalf("unexpected leaf: %+v", ast.Leaf)
	}
}

func TestParsePredicateAST_ChainToPredicate(t *testing.T) {
	pubkey := make([]byte, 32)
	for i := range pubkey {
		pubkey[i] = byte(i)
	}
	raw := map[string]any{"kind": "chain_to", "pubkey": pubkey}
	ast, err := trust.ParsePredicateAST(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ast.Kind != trust.KindChainTo {
		t.Fatalf("expected KindChainTo; got %v", ast.Kind)
	}
	if len(ast.Leaf.ChainToPubkey) != 32 {
		t.Fatalf("expected 32-byte pubkey; got %d bytes", len(ast.Leaf.ChainToPubkey))
	}
}

func TestParsePredicateAST_AllOfComposite(t *testing.T) {
	raw := map[string]any{
		"kind": "all_of",
		"children": []any{
			map[string]any{"kind": "level", "n": float64(1)},
			map[string]any{"kind": "grant", "convention": "ready", "op": "claim"},
		},
	}
	ast, err := trust.ParsePredicateAST(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ast.Kind != trust.KindAllOf {
		t.Fatalf("expected KindAllOf; got %v", ast.Kind)
	}
	if len(ast.Children) != 2 {
		t.Fatalf("expected 2 children; got %d", len(ast.Children))
	}
}

// TestParsePredicateAST_DepthExceeded verifies that depth > MaxPredicateDepth
// is rejected (spec §2.2: "Maximum nesting depth: 3").
func TestParsePredicateAST_DepthExceeded(t *testing.T) {
	// Build a depth-4 tree: any_of > all_of > any_of > all_of > level
	raw := map[string]any{
		"kind": "any_of",
		"children": []any{
			map[string]any{
				"kind": "all_of",
				"children": []any{
					map[string]any{
						"kind": "any_of",
						"children": []any{
							map[string]any{
								"kind": "all_of",
								"children": []any{
									map[string]any{"kind": "level", "n": float64(0)},
								},
							},
						},
					},
				},
			},
		},
	}
	_, err := trust.ParsePredicateAST(raw)
	if err == nil {
		t.Fatal("expected error for depth-4 predicate tree; got nil")
	}
	// Should NOT be ErrNotPredicateRejected — it's a depth error.
	if errors.Is(err, trust.ErrNotPredicateRejected) {
		t.Fatalf("depth error should not be ErrNotPredicateRejected")
	}
}

// TestParsePredicateAST_UnknownKind verifies that unknown kinds are rejected.
func TestParsePredicateAST_UnknownKind(t *testing.T) {
	raw := map[string]any{"kind": "hash_equals", "hash": "abc123"}
	_, err := trust.ParsePredicateAST(raw)
	if err == nil {
		t.Fatal("expected error for unknown predicate kind 'hash_equals'; got nil")
	}
}
