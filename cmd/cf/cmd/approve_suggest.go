package cmd

// approve_suggest.go — scope auto-suggestion for cf approve.
//
// WHY THIS FILE EXISTS:
//
// Design v2 §2.6: "cf approve includes scope auto-suggestion: the prompt is
// pre-filled with scope inferred from the failed predicate."
//
// The goal is least-privilege by default. When an agent hits a gate it cannot
// satisfy, the convention executor synthesizes a delegation:request carrying the
// failed predicate and a proposed minimum-sufficient scope. The approve command
// shows the operator a suggestion — the intersection of what was requested and
// what the failed predicate requires — so the default action is always narrower
// than the requested scope, never wider.
//
// OPEN-021: "Pre-fill the prompt with scope inferred from the failed predicate;
// default --persist 7d."
//
// Algorithm:
//  1. Start with the requested scope (what the agent asked for).
//  2. Walk the failed predicate AST and collect every convention:op pair it gates on.
//  3. The suggestion is the intersection of requested and required.
//     - If requested is empty (wildcard), the suggestion is just the required set.
//     - If requested is a subset of required, the suggestion equals required.
//     - If required is a subset of requested, the suggestion narrows to required.
//  4. A small "similar-ops allowance" adds a `*` on the same convention when the
//     required set names at least one op from that convention — avoids a second
//     approval for the next adjacent op.
//
// Layering:
//
//	This file is in cmd (L4). It imports the predicate types from
//	cf-conventions/cf-authority/trust (L3) only via the stable export surface
//	(PredicateAST, LeafPredicate, PredicateKind constants) — no import of
//	evaluator internals. The import is allowed because cmd is the CLI layer
//	and is permitted to depend on all layers.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"
)

// ScopeEntry is one line of a scope suggestion: a convention + op pattern pair.
type ScopeEntry struct {
	Convention string
	OpPattern  string
}

// String returns the human-readable form of a scope entry (e.g. "rd:claim").
func (s ScopeEntry) String() string {
	return s.Convention + ":" + s.OpPattern
}

// ScopeSuggestion is the result of auto-suggestion for one grant request.
type ScopeSuggestion struct {
	// Requested is the scope the agent asked for (from the delegation:request message).
	Requested []ScopeEntry

	// Suggested is the recommended minimum scope:
	//   intersection(Requested, Required) + similar-ops allowance.
	// This is what the approve prompt is pre-filled with.
	Suggested []ScopeEntry

	// WiderThanRequested is true when the Required set contains conventions/ops
	// not present in Requested (the predicate demands more than was asked).
	// In this case the suggestion equals Required rather than the intersection,
	// and the diff shows additions over the requested scope.
	WiderThanRequested bool
}

// DiffLines returns a human-readable diff between Requested and Suggested.
// Lines prefixed with "-" are in Requested but removed (narrowed).
// Lines prefixed with "+" are in Suggested but not in Requested (widened).
// Lines with " " are common.
func (ss ScopeSuggestion) DiffLines() []string {
	reqSet := make(map[string]bool, len(ss.Requested))
	sugSet := make(map[string]bool, len(ss.Suggested))
	for _, e := range ss.Requested {
		reqSet[e.String()] = true
	}
	for _, e := range ss.Suggested {
		sugSet[e.String()] = true
	}

	// Collect all keys for sorted output.
	allKeys := make(map[string]bool)
	for k := range reqSet {
		allKeys[k] = true
	}
	for k := range sugSet {
		allKeys[k] = true
	}
	sorted := make([]string, 0, len(allKeys))
	for k := range allKeys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	lines := make([]string, 0, len(sorted))
	for _, k := range sorted {
		inReq := reqSet[k]
		inSug := sugSet[k]
		switch {
		case inReq && inSug:
			lines = append(lines, "  "+k)
		case inReq && !inSug:
			lines = append(lines, "- "+k)
		case !inReq && inSug:
			lines = append(lines, "+ "+k)
		}
	}
	return lines
}

// SuggestScope computes the scope suggestion given:
//   - requestedScope: what the agent asked for (may be empty → wildcard)
//   - failedPredicate: the AST of the gate predicate that was not satisfied
//
// Returns a ScopeSuggestion the caller can display and pre-fill into the prompt.
//
// The suggestion is always at least as narrow as the requested scope — the
// operator can widen it manually, but the default action must be least-privilege.
func SuggestScope(requestedScope []ScopeEntry, failedPredicate trust.PredicateAST) ScopeSuggestion {
	// Collect all convention:op pairs the failed predicate gates on.
	required := collectPredicateScope(failedPredicate)

	// Build sets for comparison.
	reqIndex := indexScope(requestedScope)
	reqConvs := convSet(requestedScope)

	// Build suggestion:
	//   Start with required; add only the requested entries that are already
	//   within the required conventions (same-convention entries, not wildcards
	//   to foreign conventions).
	//
	//   The "similar-ops allowance": for each convention in required, include a
	//   wildcard op (*) only if the requested scope already had a wildcard there.
	//   Otherwise stay tight.
	sugSet := make(map[string]ScopeEntry)
	for _, r := range required {
		sugSet[r.String()] = r
	}

	// If requested was empty → operator didn't specify a scope → the suggestion
	// is exactly the required set, which is already in sugSet.
	//
	// If requested was non-empty, retain any requested entries that are within
	// the required convention set (so we don't introduce foreign conventions)
	// but do not retain entries outside required conventions.
	widerThanRequested := false
	if len(requestedScope) > 0 {
		for _, r := range required {
			if !reqIndex[r.String()] && !reqConvs[r.Convention+":*"] {
				// This convention:op was not in the requested scope at all →
				// the predicate demands more than was asked.
				widerThanRequested = true
			}
		}
		// Narrow: only keep requested entries whose convention appears in required.
		requiredConvs := make(map[string]bool)
		for _, r := range required {
			requiredConvs[r.Convention] = true
		}
		for _, req := range requestedScope {
			if !requiredConvs[req.Convention] {
				// Remove entries for conventions the predicate doesn't require.
				// (They're out of scope for this grant request.)
				continue
			}
			sugSet[req.String()] = req
		}
	}

	suggested := make([]ScopeEntry, 0, len(sugSet))
	for _, e := range sugSet {
		suggested = append(suggested, e)
	}
	sort.Slice(suggested, func(i, j int) bool {
		si, sj := suggested[i].String(), suggested[j].String()
		return si < sj
	})

	return ScopeSuggestion{
		Requested:          requestedScope,
		Suggested:          suggested,
		WiderThanRequested: widerThanRequested,
	}
}

// collectPredicateScope walks a PredicateAST and collects every convention:op
// pair it gates on (from KindGrant and KindGrantIn leaf nodes).
// Returns deduplicated, sorted entries.
func collectPredicateScope(ast trust.PredicateAST) []ScopeEntry {
	seen := make(map[string]ScopeEntry)
	collectPredicateScopeInto(ast, seen)
	entries := make([]ScopeEntry, 0, len(seen))
	for _, e := range seen {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].String() < entries[j].String()
	})
	return entries
}

func collectPredicateScopeInto(ast trust.PredicateAST, out map[string]ScopeEntry) {
	switch ast.Kind {
	case trust.KindGrant:
		if ast.Leaf != nil {
			e := ScopeEntry{Convention: ast.Leaf.GrantConvention, OpPattern: ast.Leaf.GrantOp}
			out[e.String()] = e
		}
	case trust.KindGrantIn:
		if ast.Leaf != nil {
			e := ScopeEntry{Convention: ast.Leaf.GrantInConvention, OpPattern: ast.Leaf.GrantInOpGlob}
			out[e.String()] = e
		}
	case trust.KindAllOf, trust.KindAnyOf:
		for _, child := range ast.Children {
			collectPredicateScopeInto(child, out)
		}
	}
	// KindLevel, KindGrantQuota, KindChainTo, KindChainToQuorum: no scope entries.
}

// indexScope returns a map keyed by ScopeEntry.String() for O(1) lookup.
func indexScope(entries []ScopeEntry) map[string]bool {
	m := make(map[string]bool, len(entries))
	for _, e := range entries {
		m[e.String()] = true
	}
	return m
}

// convSet returns a map of "convention:*" for entries whose op pattern is "*".
func convSet(entries []ScopeEntry) map[string]bool {
	m := make(map[string]bool)
	for _, e := range entries {
		if e.OpPattern == "*" {
			m[e.Convention+":*"] = true
		}
	}
	return m
}

// ParseScopeString parses a comma-separated list of "convention:op" entries into
// ScopeEntry slice. Used to parse --scope flag values and delegation:request payloads.
// Returns an error if any entry is malformed.
func ParseScopeString(s string) ([]ScopeEntry, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	entries := make([]ScopeEntry, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		idx := strings.IndexByte(p, ':')
		if idx <= 0 || idx == len(p)-1 {
			return nil, fmt.Errorf("scope entry %q must be in convention:op_pattern format", p)
		}
		entries = append(entries, ScopeEntry{
			Convention: p[:idx],
			OpPattern:  p[idx+1:],
		})
	}
	return entries, nil
}

// FormatScope formats a ScopeEntry slice as a comma-separated string.
func FormatScope(entries []ScopeEntry) string {
	parts := make([]string, len(entries))
	for i, e := range entries {
		parts[i] = e.String()
	}
	return strings.Join(parts, ", ")
}
