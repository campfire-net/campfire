// Package trust — authority resolver per Trust Convention v0.1 §5.
package trust

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/campfire-net/campfire/pkg/convention"
)

// AuthorityLevel classifies the trust authority of a declaration.
type AuthorityLevel string

const (
	AuthoritySemantic    AuthorityLevel = "semantic"    // from convention registry
	AuthorityOperational AuthorityLevel = "operational" // from local campfire key
	AuthorityUntrusted   AuthorityLevel = "untrusted"   // from member key or unrecognized
)

// ResolveAuthority classifies a declaration's authority.
// §5: SignerType drives the classification; the campfire-key gate is enforced here.
// In Trust v0.2 the chain parameter is unused (no chain verification). Pass nil.
func ResolveAuthority(decl *convention.Declaration, chain interface{}) AuthorityLevel {
	// Campfire-key gate: signing="campfire_key" requires at least campfire key authority.
	// If the declared signing is campfire_key but the actual signer type is a member key,
	// the declaration is gated — treat as untrusted.
	if decl.Signing == "campfire_key" && decl.SignerType != convention.SignerCampfireKey {
		return AuthorityUntrusted
	}

	switch string(decl.SignerType) {
	case string(convention.SignerConventionRegistry):
		return AuthoritySemantic
	case string(convention.SignerCampfireKey):
		return AuthorityOperational
	default:
		return AuthorityUntrusted
	}
}

// ValidateOperationalOverride checks that local customization is strictly subtractive.
// Returns nil if the override is valid, or an error describing what was loosened.
func ValidateOperationalOverride(registry, local *convention.Declaration) error {
	// Build a name → arg map for registry args.
	regArgs := make(map[string]convention.ArgDescriptor, len(registry.Args))
	for _, a := range registry.Args {
		regArgs[a.Name] = a
	}

	for _, la := range local.Args {
		ra, ok := regArgs[la.Name]
		if !ok {
			// Local defines an arg not in registry — no registry constraint to compare.
			continue
		}

		// max_length: local must be <= registry (0 means unset/no limit).
		// If registry sets a limit, local must also set a limit AND must not exceed it.
		// Local=0 (no limit) when registry has a limit is loosening.
		if ra.MaxLength > 0 && (la.MaxLength == 0 || la.MaxLength > ra.MaxLength) {
			return fmt.Errorf("arg %q: max_length loosened (local=%d, registry=%d; 0 means no limit)", la.Name, la.MaxLength, ra.MaxLength)
		}

		// max_count: local must be <= registry (0 means unset/no limit).
		// Same logic as max_length.
		if ra.MaxCount > 0 && (la.MaxCount == 0 || la.MaxCount > ra.MaxCount) {
			return fmt.Errorf("arg %q: max_count loosened (local=%d, registry=%d; 0 means no limit)", la.Name, la.MaxCount, ra.MaxCount)
		}

		// min: local must be >= registry (raise minimum, not lower).
		// Local=0 (no minimum) when registry has a minimum is loosening.
		if ra.Min > 0 && la.Min < ra.Min {
			return fmt.Errorf("arg %q: min loosened (%d < registry %d)", la.Name, la.Min, ra.Min)
		}

		// max: local must be <= registry (lower maximum, not raise).
		// Local=0 (no maximum) when registry has a maximum is loosening.
		if ra.Max > 0 && (la.Max == 0 || la.Max > ra.Max) {
			return fmt.Errorf("arg %q: max loosened (local=%d, registry=%d; 0 means no limit)", la.Name, la.Max, ra.Max)
		}

		// values: local must be a subset of registry values.
		if len(ra.Values) > 0 && len(la.Values) > 0 {
			regSet := make(map[string]bool, len(ra.Values))
			for _, v := range ra.Values {
				regSet[v] = true
			}
			for _, v := range la.Values {
				if !regSet[v] {
					return fmt.Errorf("arg %q: values loosened — %q is not in registry values", la.Name, v)
				}
			}
		} else if len(ra.Values) == 0 && len(la.Values) > 0 {
			// Registry has no value constraint; local adding one is fine (more restrictive).
		}
	}

	// rate_limit: local may only tighten (lower max, shorter window).
	// If registry sets a rate_limit, local must also set one — removing it entirely
	// would loosen the constraint.
	if registry.RateLimit != nil {
		if local.RateLimit == nil {
			return fmt.Errorf("rate_limit removed by local override (registry has rate_limit, local has none)")
		}
		if local.RateLimit.Max > registry.RateLimit.Max {
			return fmt.Errorf("rate_limit.max loosened (%d > registry %d)", local.RateLimit.Max, registry.RateLimit.Max)
		}
		if registry.RateLimit.Window != "" && local.RateLimit.Window != "" {
			regDur, err1 := parseDurationStr(registry.RateLimit.Window)
			locDur, err2 := parseDurationStr(local.RateLimit.Window)
			if err1 == nil && err2 == nil && locDur > regDur {
				return fmt.Errorf("rate_limit.window loosened (%s > registry %s)", local.RateLimit.Window, registry.RateLimit.Window)
			}
		}
	}

	return nil
}

// parseDurationStr parses duration strings like "1m", "24h", "30s", "7d".
// Duplicates convention.parseDuration but lives here to avoid cross-package access.
func parseDurationStr(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 || i >= len(s) {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	n, err := strconv.Atoi(s[:i])
	if err != nil {
		return 0, err
	}
	switch s[i:] {
	case "s":
		return time.Duration(n) * time.Second, nil
	case "m":
		return time.Duration(n) * time.Minute, nil
	case "h":
		return time.Duration(n) * time.Hour, nil
	case "d":
		return time.Duration(n) * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unknown duration unit in %q", s)
	}
}

// semanticArgEntry is the canonical form of an arg used for fingerprinting.
type semanticArgEntry struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

// semanticTagEntry is the canonical form of a produces_tag used for fingerprinting.
type semanticTagEntry struct {
	Tag         string `json:"tag"`
	Cardinality string `json:"cardinality"`
}

// semanticStepEntry is the canonical form of a step used for fingerprinting.
type semanticStepEntry struct {
	Action string `json:"action"`
}

// semanticFields is the structure hashed by SemanticFingerprint.
type semanticFields struct {
	Args        []semanticArgEntry  `json:"args"`
	ProducesTags []semanticTagEntry `json:"produces_tags"`
	Antecedents string              `json:"antecedents"`
	Signing     string              `json:"signing"`
	Steps       []semanticStepEntry `json:"steps"`
}

// SemanticFingerprint computes a SHA-256 hash of all semantic fields from a declaration.
// Two declarations with identical semantic fields produce identical fingerprints regardless
// of operational field differences (max_length, rate_limit, etc.).
func SemanticFingerprint(decl *convention.Declaration) string {
	// Args: sorted by name, capturing only semantic fields.
	args := make([]semanticArgEntry, len(decl.Args))
	for i, a := range decl.Args {
		args[i] = semanticArgEntry{Name: a.Name, Type: a.Type, Required: a.Required}
	}
	sort.Slice(args, func(i, j int) bool { return args[i].Name < args[j].Name })

	// ProducesTags: sorted by tag.
	tags := make([]semanticTagEntry, len(decl.ProducesTags))
	for i, t := range decl.ProducesTags {
		tags[i] = semanticTagEntry{Tag: t.Tag, Cardinality: t.Cardinality}
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i].Tag < tags[j].Tag })

	// Steps: in order (index matters for workflow).
	steps := make([]semanticStepEntry, len(decl.Steps))
	for i, s := range decl.Steps {
		steps[i] = semanticStepEntry{Action: s.Action}
	}

	sf := semanticFields{
		Args:         args,
		ProducesTags: tags,
		Antecedents:  decl.Antecedents,
		Signing:      decl.Signing,
		Steps:        steps,
	}

	data, _ := json.Marshal(sf)
	h := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(h[:])
}

// CompareVersions returns true if version a supersedes version b.
// Versions are compared segment-by-segment numerically after splitting on ".".
// Equal versions do not supersede each other.
func CompareVersions(a, b string) bool {
	return compareVersionSegments(a, b) > 0
}

// compareVersionSegments compares two version strings segment by segment.
// Returns positive if a > b, negative if a < b, 0 if equal.
func compareVersionSegments(a, b string) int {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")

	maxLen := len(aParts)
	if len(bParts) > maxLen {
		maxLen = len(bParts)
	}

	for i := 0; i < maxLen; i++ {
		var aVal, bVal int
		if i < len(aParts) {
			aVal, _ = strconv.Atoi(aParts[i])
		}
		if i < len(bParts) {
			bVal, _ = strconv.Atoi(bParts[i])
		}
		if aVal != bVal {
			if aVal > bVal {
				return 1
			}
			return -1
		}
	}
	return 0
}
