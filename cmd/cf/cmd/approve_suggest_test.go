package cmd

import (
	"testing"

	"github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"
)

// TestSuggestScope_ExactMatch verifies that when the requested scope exactly
// matches what the predicate requires, the suggestion equals the request.
func TestSuggestScope_ExactMatch(t *testing.T) {
	requested := []ScopeEntry{
		{Convention: "rd", OpPattern: "claim"},
	}
	pred := trust.PredicateAST{
		Kind: trust.KindGrant,
		Leaf: &trust.LeafPredicate{GrantConvention: "rd", GrantOp: "claim"},
	}
	sug := SuggestScope(requested, pred)

	if len(sug.Suggested) != 1 {
		t.Fatalf("expected 1 suggested entry, got %d: %v", len(sug.Suggested), sug.Suggested)
	}
	if sug.Suggested[0].Convention != "rd" || sug.Suggested[0].OpPattern != "claim" {
		t.Errorf("expected rd:claim, got %v", sug.Suggested[0])
	}
	if sug.WiderThanRequested {
		t.Error("WiderThanRequested should be false for exact match")
	}
}

// TestSuggestScope_NarrowRequested verifies that the suggestion narrows the
// requested scope to only what the predicate requires.
// Requested: rd:* (wildcard)
// Predicate requires: rd:claim
// Suggestion should include: rd:claim (from predicate) + rd:* (from request, same conv)
func TestSuggestScope_NarrowRequested(t *testing.T) {
	requested := []ScopeEntry{
		{Convention: "rd", OpPattern: "*"},
		{Convention: "dontguess", OpPattern: "buy"},
	}
	// Predicate only requires rd:claim
	pred := trust.PredicateAST{
		Kind: trust.KindGrant,
		Leaf: &trust.LeafPredicate{GrantConvention: "rd", GrantOp: "claim"},
	}
	sug := SuggestScope(requested, pred)

	// dontguess:buy should be dropped (not in required conventions)
	// rd:claim should be in suggestion (from predicate)
	// rd:* may also be in suggestion (from requested, same convention)
	hasRd := false
	hasDontguess := false
	for _, e := range sug.Suggested {
		if e.Convention == "rd" {
			hasRd = true
		}
		if e.Convention == "dontguess" {
			hasDontguess = true
		}
	}
	if !hasRd {
		t.Error("expected rd entries in suggestion")
	}
	if hasDontguess {
		t.Error("dontguess should be removed (not in required conventions from predicate)")
	}
}

// TestSuggestScope_WiderThanRequested verifies that when the predicate requires
// more than was requested, WiderThanRequested is set and the suggestion includes
// the required entries.
func TestSuggestScope_WiderThanRequested(t *testing.T) {
	requested := []ScopeEntry{
		{Convention: "rd", OpPattern: "claim"},
	}
	// Predicate requires rd:claim AND dontguess:buy
	pred := trust.PredicateAST{
		Kind: trust.KindAllOf,
		Children: []trust.PredicateAST{
			{
				Kind: trust.KindGrant,
				Leaf: &trust.LeafPredicate{GrantConvention: "rd", GrantOp: "claim"},
			},
			{
				Kind: trust.KindGrant,
				Leaf: &trust.LeafPredicate{GrantConvention: "dontguess", GrantOp: "buy"},
			},
		},
	}
	sug := SuggestScope(requested, pred)

	if !sug.WiderThanRequested {
		t.Error("WiderThanRequested should be true when predicate requires more than requested")
	}

	hasDontguess := false
	for _, e := range sug.Suggested {
		if e.Convention == "dontguess" && e.OpPattern == "buy" {
			hasDontguess = true
		}
	}
	if !hasDontguess {
		t.Error("expected dontguess:buy in suggestion (required by predicate)")
	}
}

// TestSuggestScope_EmptyRequestedScope verifies that when no scope was requested
// (agent didn't specify), the suggestion is exactly the required set.
func TestSuggestScope_EmptyRequestedScope(t *testing.T) {
	pred := trust.PredicateAST{
		Kind: trust.KindGrantIn,
		Leaf: &trust.LeafPredicate{
			GrantInConvention: "social",
			GrantInOpGlob:     "post|reply",
			GrantInWhere:      trust.WhereMatcher{Kind: trust.WhereCampfireByID, CampfireID: "abc123"},
		},
	}
	sug := SuggestScope(nil, pred)

	if len(sug.Suggested) != 1 {
		t.Fatalf("expected 1 suggested entry, got %d: %v", len(sug.Suggested), sug.Suggested)
	}
	if sug.Suggested[0].Convention != "social" || sug.Suggested[0].OpPattern != "post|reply" {
		t.Errorf("expected social:post|reply, got %v", sug.Suggested[0])
	}
}

// TestSuggestScope_NoGrantPredicates verifies that a predicate with no grant
// nodes (e.g. level-only) produces no scope entries.
func TestSuggestScope_NoGrantPredicates(t *testing.T) {
	pred := trust.PredicateAST{
		Kind: trust.KindLevel,
		Leaf: &trust.LeafPredicate{LevelN: 2},
	}
	sug := SuggestScope(nil, pred)

	if len(sug.Suggested) != 0 {
		t.Errorf("expected empty suggestion for level-only predicate, got %v", sug.Suggested)
	}
}

// TestSuggestScope_AnyOfPredicate verifies that any_of predicates collect
// all grant nodes from all branches.
func TestSuggestScope_AnyOfPredicate(t *testing.T) {
	pred := trust.PredicateAST{
		Kind: trust.KindAnyOf,
		Children: []trust.PredicateAST{
			{
				Kind: trust.KindGrant,
				Leaf: &trust.LeafPredicate{GrantConvention: "rd", GrantOp: "claim"},
			},
			{
				Kind: trust.KindGrant,
				Leaf: &trust.LeafPredicate{GrantConvention: "rd", GrantOp: "progress"},
			},
		},
	}
	sug := SuggestScope(nil, pred)

	if len(sug.Suggested) != 2 {
		t.Fatalf("expected 2 suggested entries for any_of, got %d: %v", len(sug.Suggested), sug.Suggested)
	}
}

// TestDiffLines_ShowsAddRemove verifies that DiffLines correctly marks added,
// removed, and common entries.
func TestDiffLines_ShowsAddRemove(t *testing.T) {
	ss := ScopeSuggestion{
		Requested: []ScopeEntry{
			{Convention: "rd", OpPattern: "claim"},
			{Convention: "rd", OpPattern: "done"},
		},
		Suggested: []ScopeEntry{
			{Convention: "rd", OpPattern: "claim"},
			{Convention: "rd", OpPattern: "progress"},
		},
	}
	lines := ss.DiffLines()

	hasAdded := false
	hasRemoved := false
	hasCommon := false
	for _, l := range lines {
		if l == "+ rd:progress" {
			hasAdded = true
		}
		if l == "- rd:done" {
			hasRemoved = true
		}
		if l == "  rd:claim" {
			hasCommon = true
		}
	}
	if !hasAdded {
		t.Errorf("expected '+ rd:progress' in diff, got: %v", lines)
	}
	if !hasRemoved {
		t.Errorf("expected '- rd:done' in diff, got: %v", lines)
	}
	if !hasCommon {
		t.Errorf("expected '  rd:claim' in diff, got: %v", lines)
	}
}

// TestParseScopeString verifies the convention:op parser.
func TestParseScopeString(t *testing.T) {
	cases := []struct {
		input    string
		wantLen  int
		wantErr  bool
	}{
		{"rd:claim", 1, false},
		{"rd:claim, dontguess:buy", 2, false},
		{"rd:claim|progress|done", 1, false},
		{"rd:*", 1, false},
		{"", 0, false},
		{"malformed", 0, true},
		{":op", 0, true},
		{"conv:", 0, true},
	}
	for _, tc := range cases {
		entries, err := ParseScopeString(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseScopeString(%q): expected error, got nil (entries=%v)", tc.input, entries)
			}
		} else {
			if err != nil {
				t.Errorf("ParseScopeString(%q): unexpected error: %v", tc.input, err)
			}
			if len(entries) != tc.wantLen {
				t.Errorf("ParseScopeString(%q): got %d entries, want %d", tc.input, len(entries), tc.wantLen)
			}
		}
	}
}

// TestParsePersistTTL verifies the --persist flag parser.
func TestParsePersistTTL(t *testing.T) {
	cases := []struct {
		input   string
		wantErr bool
		wantDur string // non-empty → check formatted output
	}{
		{"", false, "7d"},
		{"7d", false, "7d"},
		{"1d", false, "1d"},
		{"24h", false, "1d"},
		{"168h", false, "7d"},
		{"30d", false, "30d"},
		{"31d", true, ""},  // exceeds 30d maximum
		{"-1h", true, ""},  // negative
		{"0h", true, ""},   // zero
		{"bad", true, ""},  // unparseable
	}
	for _, tc := range cases {
		d, err := parsePersistTTL(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parsePersistTTL(%q): expected error, got duration %v", tc.input, d)
			}
		} else {
			if err != nil {
				t.Errorf("parsePersistTTL(%q): unexpected error: %v", tc.input, err)
			}
			if tc.wantDur != "" {
				got := formatTTL(d)
				if got != tc.wantDur {
					t.Errorf("parsePersistTTL(%q): formatTTL = %q, want %q", tc.input, got, tc.wantDur)
				}
			}
		}
	}
}
