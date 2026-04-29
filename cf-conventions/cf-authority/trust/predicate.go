package trust

import "fmt"

// PredicateKind identifies the kind of predicate node.
// Numeric ordering is the canonical sort order for AST canonicalization (§4.3).
type PredicateKind int

const (
	// Leaf predicate kinds (§2.1)
	KindLevel         PredicateKind = 1 // level: N
	KindGrant         PredicateKind = 2 // grant: conv:op
	KindGrantIn       PredicateKind = 3 // grant_in: conv:op_glob at where
	KindGrantQuota    PredicateKind = 4 // grant_quota: axis >= bound
	KindChainTo       PredicateKind = 5 // chain_to: pubkey
	KindChainToQuorum PredicateKind = 6 // chain_to_quorum: M-of-N

	// Composite predicate kinds (§2.2)
	KindAllOf PredicateKind = 100 // all_of: AND composition
	KindAnyOf PredicateKind = 101 // any_of: OR composition

	// Reserved for parse-time rejection — NOT a valid gate language node.
	// Included here as a named sentinel so ParsePredicateAST can return a
	// typed error instead of a panic when a 'not:' node is encountered.
	KindNot PredicateKind = 999 // not: — REJECTED at parse time (§2.2)
)

// MaxPredicateDepth is the maximum nesting depth for composite predicates (§2.2).
// Predicates with depth > 3 MUST be rejected at parse time.
const MaxPredicateDepth = 3

// PredicateAST is a node in the gate predicate language (§2.3).
type PredicateAST struct {
	Kind PredicateKind

	// Populated for leaf kinds.
	Leaf *LeafPredicate

	// Populated for composite kinds (all_of, any_of).
	// Children are in canonical order per §4.3:
	//   primary sort: PredicateKind (numeric enum order)
	//   secondary sort: lexicographic on primary operand string
	Children []PredicateAST
}

// LeafPredicate holds the payload for one of the six leaf kinds (§2.1).
// Exactly one field is set, matching the Kind of the parent PredicateAST node.
type LeafPredicate struct {
	// KindLevel: n >= N (provenance level)
	LevelN uint

	// KindGrant: exact capability check
	GrantConvention string
	GrantOp         string

	// KindGrantIn: scoped capability check
	GrantInConvention string
	GrantInOpGlob     string
	GrantInWhere      WhereMatcher

	// KindGrantQuota: bound axis check
	QuotaAxis  string
	QuotaBound uint

	// KindChainTo: trust anchor by key (NOT by name)
	ChainToPubkey []byte // 32-byte Ed25519 public key

	// KindChainToQuorum: M-of-N trust anchor
	QuorumM      uint
	QuorumPubkeys [][]byte // N Ed25519 public keys (32 bytes each), sorted ascending
}

// WhereMatcher is a single matcher in the where list (§1.3).
// Three kinds compose as OR. Empty list = "wherever the agent is a member."
type WhereMatcher struct {
	Kind       WhereKind
	CampfireID string // CampfireByID: exact hex ID
	Prefix     string // NamePrefix: campfire name prefix
	Tag        string // TagMatcher: campfire carries this tag
}

// WhereKind identifies the kind of WhereMatcher.
type WhereKind int

const (
	WhereCampfireByID WhereKind = 1 // {kind: 1, id: bstr}
	WhereNamePrefix   WhereKind = 2 // {kind: 2, prefix: tstr}
	WhereTagMatcher   WhereKind = 3 // {kind: 3, tag: tstr}
)

// ErrNotPredicateRejected is returned when a predicate AST contains a 'not:'
// node. The 'not:' predicate is explicitly excluded from the cf-authority gate
// language as a deliberate safety property (§2.2: "There is no `not:` predicate
// — complement predicates are escalation primitives; banning them is a
// deliberate safety property of the language").
//
// This is a parse-time rejection, not a runtime denial.
var ErrNotPredicateRejected = fmt.Errorf(
	"cf-authority: 'not:' predicate is not permitted in the gate language " +
		"(§2.2: complement predicates are escalation primitives; " +
		"banned as a deliberate safety property)")

// ParsePredicateAST parses a predicate AST from its map representation and
// validates it per spec §2. Returns ErrNotPredicateRejected if the AST
// contains a 'not:' node at any position (including nested inside all_of/any_of).
// Returns an error if depth exceeds MaxPredicateDepth (§2.2).
//
// The map representation mirrors the JSON fixture format from §5.4:
//
//	map[string]any with "kind" key and kind-specific keys.
func ParsePredicateAST(raw map[string]any) (PredicateAST, error) {
	return parsePredicateASTDepth(raw, 0)
}

func parsePredicateASTDepth(raw map[string]any, depth int) (PredicateAST, error) {
	if depth > MaxPredicateDepth {
		return PredicateAST{}, fmt.Errorf(
			"cf-authority: predicate nesting depth %d exceeds maximum %d (§2.2)",
			depth, MaxPredicateDepth)
	}

	kindStr, ok := raw["kind"].(string)
	if !ok {
		return PredicateAST{}, fmt.Errorf("cf-authority: predicate missing 'kind' field")
	}

	// §2.2: Explicit rejection of 'not:' at parse time.
	// This is NOT a runtime deny — it is a hard parse error.
	if kindStr == "not" {
		return PredicateAST{}, ErrNotPredicateRejected
	}

	switch kindStr {
	case "level":
		n, err := parseUint(raw, "n")
		if err != nil {
			return PredicateAST{}, fmt.Errorf("cf-authority: level predicate: %w", err)
		}
		return PredicateAST{
			Kind: KindLevel,
			Leaf: &LeafPredicate{LevelN: n},
		}, nil

	case "grant":
		conv, op, err := parseConvOp(raw, "convention", "op")
		if err != nil {
			return PredicateAST{}, fmt.Errorf("cf-authority: grant predicate: %w", err)
		}
		return PredicateAST{
			Kind: KindGrant,
			Leaf: &LeafPredicate{GrantConvention: conv, GrantOp: op},
		}, nil

	case "grant_in":
		conv, opGlob, err := parseConvOp(raw, "convention", "op_glob")
		if err != nil {
			return PredicateAST{}, fmt.Errorf("cf-authority: grant_in predicate: %w", err)
		}
		where, err := parseWhereMatcher(raw)
		if err != nil {
			return PredicateAST{}, fmt.Errorf("cf-authority: grant_in predicate: %w", err)
		}
		return PredicateAST{
			Kind: KindGrantIn,
			Leaf: &LeafPredicate{
				GrantInConvention: conv,
				GrantInOpGlob:     opGlob,
				GrantInWhere:      where,
			},
		}, nil

	case "grant_quota":
		axis, ok := raw["axis"].(string)
		if !ok {
			return PredicateAST{}, fmt.Errorf("cf-authority: grant_quota predicate: missing 'axis' field")
		}
		bound, err := parseUint(raw, "bound")
		if err != nil {
			return PredicateAST{}, fmt.Errorf("cf-authority: grant_quota predicate: %w", err)
		}
		return PredicateAST{
			Kind: KindGrantQuota,
			Leaf: &LeafPredicate{QuotaAxis: axis, QuotaBound: bound},
		}, nil

	case "chain_to":
		pubkey, err := parsePubkey(raw, "pubkey")
		if err != nil {
			return PredicateAST{}, fmt.Errorf("cf-authority: chain_to predicate: %w", err)
		}
		return PredicateAST{
			Kind: KindChainTo,
			Leaf: &LeafPredicate{ChainToPubkey: pubkey},
		}, nil

	case "chain_to_quorum":
		m, err := parseUint(raw, "m")
		if err != nil {
			return PredicateAST{}, fmt.Errorf("cf-authority: chain_to_quorum predicate: %w", err)
		}
		pubkeys, err := parsePubkeyList(raw, "pubkeys")
		if err != nil {
			return PredicateAST{}, fmt.Errorf("cf-authority: chain_to_quorum predicate: %w", err)
		}
		return PredicateAST{
			Kind: KindChainToQuorum,
			Leaf: &LeafPredicate{QuorumM: m, QuorumPubkeys: pubkeys},
		}, nil

	case "all_of":
		children, err := parseChildren(raw, depth)
		if err != nil {
			return PredicateAST{}, fmt.Errorf("cf-authority: all_of predicate: %w", err)
		}
		return PredicateAST{Kind: KindAllOf, Children: children}, nil

	case "any_of":
		children, err := parseChildren(raw, depth)
		if err != nil {
			return PredicateAST{}, fmt.Errorf("cf-authority: any_of predicate: %w", err)
		}
		return PredicateAST{Kind: KindAnyOf, Children: children}, nil

	default:
		return PredicateAST{}, fmt.Errorf("cf-authority: unknown predicate kind %q", kindStr)
	}
}

func parseChildren(raw map[string]any, parentDepth int) ([]PredicateAST, error) {
	childrenRaw, ok := raw["children"]
	if !ok {
		return nil, fmt.Errorf("missing 'children' field")
	}
	childList, ok := childrenRaw.([]any)
	if !ok {
		return nil, fmt.Errorf("'children' must be an array")
	}
	if len(childList) == 0 {
		return nil, fmt.Errorf("'children' must have at least one element")
	}
	children := make([]PredicateAST, 0, len(childList))
	for i, c := range childList {
		childMap, ok := c.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("child[%d] is not a map", i)
		}
		child, err := parsePredicateASTDepth(childMap, parentDepth+1)
		if err != nil {
			return nil, fmt.Errorf("child[%d]: %w", i, err)
		}
		children = append(children, child)
	}
	return children, nil
}

func parseUint(raw map[string]any, key string) (uint, error) {
	v, ok := raw[key]
	if !ok {
		return 0, fmt.Errorf("missing %q field", key)
	}
	switch n := v.(type) {
	case int:
		if n < 0 {
			return 0, fmt.Errorf("field %q must be non-negative", key)
		}
		return uint(n), nil
	case uint:
		return n, nil
	case float64:
		if n < 0 {
			return 0, fmt.Errorf("field %q must be non-negative", key)
		}
		return uint(n), nil
	case int64:
		if n < 0 {
			return 0, fmt.Errorf("field %q must be non-negative", key)
		}
		return uint(n), nil
	default:
		return 0, fmt.Errorf("field %q has unexpected type %T", key, v)
	}
}

func parseConvOp(raw map[string]any, convKey, opKey string) (string, string, error) {
	conv, ok := raw[convKey].(string)
	if !ok {
		return "", "", fmt.Errorf("missing or non-string %q field", convKey)
	}
	op, ok := raw[opKey].(string)
	if !ok {
		return "", "", fmt.Errorf("missing or non-string %q field", opKey)
	}
	return conv, op, nil
}

func parsePubkey(raw map[string]any, key string) ([]byte, error) {
	v, ok := raw[key]
	if !ok {
		return nil, fmt.Errorf("missing %q field", key)
	}
	switch b := v.(type) {
	case []byte:
		if len(b) != 32 {
			return nil, fmt.Errorf("field %q must be 32 bytes, got %d", key, len(b))
		}
		out := make([]byte, 32)
		copy(out, b)
		return out, nil
	case string:
		// Accept hex-encoded strings for test fixtures
		if len(b) != 64 {
			return nil, fmt.Errorf("field %q (hex string) must be 64 chars, got %d", key, len(b))
		}
		decoded := make([]byte, 32)
		for i := 0; i < 32; i++ {
			hi := hexNibble(b[i*2])
			lo := hexNibble(b[i*2+1])
			if hi < 0 || lo < 0 {
				return nil, fmt.Errorf("field %q: invalid hex character at position %d", key, i*2)
			}
			decoded[i] = byte(hi<<4 | lo)
		}
		return decoded, nil
	default:
		return nil, fmt.Errorf("field %q has unexpected type %T", key, v)
	}
}

func hexNibble(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	default:
		return -1
	}
}

func parsePubkeyList(raw map[string]any, key string) ([][]byte, error) {
	v, ok := raw[key]
	if !ok {
		return nil, fmt.Errorf("missing %q field", key)
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("field %q must be an array", key)
	}
	result := make([][]byte, 0, len(list))
	for i, item := range list {
		var entry map[string]any
		switch x := item.(type) {
		case []byte:
			if len(x) != 32 {
				return nil, fmt.Errorf("pubkeys[%d] must be 32 bytes, got %d", i, len(x))
			}
			b := make([]byte, 32)
			copy(b, x)
			result = append(result, b)
			continue
		case map[string]any:
			entry = x
		default:
			return nil, fmt.Errorf("pubkeys[%d] has unexpected type %T", i, item)
		}
		_ = entry
		return nil, fmt.Errorf("pubkeys[%d]: expected []byte element", i)
	}
	return result, nil
}

func parseWhereMatcher(raw map[string]any) (WhereMatcher, error) {
	whereRaw, ok := raw["where"]
	if !ok {
		return WhereMatcher{}, fmt.Errorf("missing 'where' field")
	}
	whereMap, ok := whereRaw.(map[string]any)
	if !ok {
		return WhereMatcher{}, fmt.Errorf("'where' must be a map")
	}
	kindNum, err := parseUint(whereMap, "kind")
	if err != nil {
		return WhereMatcher{}, fmt.Errorf("where.kind: %w", err)
	}
	switch WhereKind(kindNum) {
	case WhereCampfireByID:
		id, ok := whereMap["id"].(string)
		if !ok {
			return WhereMatcher{}, fmt.Errorf("where.id: missing or non-string")
		}
		return WhereMatcher{Kind: WhereCampfireByID, CampfireID: id}, nil
	case WhereNamePrefix:
		prefix, ok := whereMap["prefix"].(string)
		if !ok {
			return WhereMatcher{}, fmt.Errorf("where.prefix: missing or non-string")
		}
		return WhereMatcher{Kind: WhereNamePrefix, Prefix: prefix}, nil
	case WhereTagMatcher:
		tag, ok := whereMap["tag"].(string)
		if !ok {
			return WhereMatcher{}, fmt.Errorf("where.tag: missing or non-string")
		}
		return WhereMatcher{Kind: WhereTagMatcher, Tag: tag}, nil
	default:
		return WhereMatcher{}, fmt.Errorf("where.kind: unknown kind %d", kindNum)
	}
}
