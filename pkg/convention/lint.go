package convention

import (
	"fmt"
	"strings"
)

// LintSeverity classifies a lint finding.
type LintSeverity string

const (
	LintError   LintSeverity = "error"
	LintWarning LintSeverity = "warning"
)

// LintFinding is a single lint result entry.
type LintFinding struct {
	Severity LintSeverity `json:"severity"`
	Code     string       `json:"code"`
	Message  string       `json:"message"`
	Field    string       `json:"field,omitempty"`
}

// LintResult is the full output of Lint.
type LintResult struct {
	Valid    bool          `json:"valid"`    // true if no errors (warnings are OK)
	Errors   []LintFinding `json:"errors"`
	Warnings []LintFinding `json:"warnings"`
}

// Lint validates a declaration payload (JSON bytes) against all conformance rules
// plus additional arg-to-tag mapping checks. It does NOT require a real sender key
// context — senderKey and campfireKey are both passed as empty strings so that
// campfire_key auth checks are skipped.
//
// Returns a LintResult with errors and warnings populated. If the payload is not
// even parseable, returns a single error finding.
func Lint(payload []byte) *LintResult {
	result := &LintResult{}

	// Run Parse with synthetic keys. senderKey == campfireKey so the
	// campfire_key check (Check 7) passes; rate limit ceiling warnings surface
	// as ConformanceResult.Warnings which we promote to lint warnings.
	decl, conf, err := Parse([]string{"convention:operation"}, payload, "synthetic", "synthetic")
	if err != nil {
		result.Errors = append(result.Errors, LintFinding{
			Severity: LintError,
			Code:     "parse_error",
			Message:  err.Error(),
		})
		return result
	}

	// Promote ConformanceResult warnings to lint warnings.
	for _, w := range conf.Warnings {
		result.Warnings = append(result.Warnings, LintFinding{
			Severity: LintWarning,
			Code:     "conformance_warning",
			Message:  w,
		})
	}

	// Additional lint checks beyond Parse.
	lintArgToTagMapping(decl, result)

	result.Valid = len(result.Errors) == 0
	return result
}

// lintArgToTagMapping checks that every glob tag rule in produces_tags can be
// satisfied by at least one arg in the declaration. It also catches the known
// enum-alignment bug: enum arg values that lack the tag prefix will never map
// to the tag glob via collectArgValuesForPrefix.
func lintArgToTagMapping(decl *Declaration, result *LintResult) {
	for i, rule := range decl.ProducesTags {
		tagBase, isGlob := parseTagGlob(rule.Tag)
		if !isGlob {
			continue
		}

		// Categorize args relative to this tag prefix:
		// - exactCandidates: enum args whose values already include the prefix (correct)
		// - mismatchCandidates: enum args whose values could satisfy the prefix if they
		//   had it (the "coordination" vs "social:" bug)
		// - nameCandidates: non-enum args whose name matches the prefix base
		var exactCandidates []ArgDescriptor
		var mismatchCandidates []ArgDescriptor
		var nameCandidates []ArgDescriptor

		prefixBase := strings.TrimSuffix(tagBase, ":")

		// Build a set of the rule's declared values (if any) for oracle lookup.
		ruleValueSet := make(map[string]bool, len(rule.Values))
		for _, rv := range rule.Values {
			ruleValueSet[rv] = true
		}

		for _, arg := range decl.Args {
			if arg.Type == "enum" && len(arg.Values) > 0 {
				hasExact := false
				hasMismatch := false
				for _, v := range arg.Values {
					if strings.HasPrefix(v, tagBase) {
						hasExact = true
					} else {
						// Check if prefix+value is a declared tag value — strong signal of mismatch.
						candidate := tagBase + v
						if len(ruleValueSet) > 0 && ruleValueSet[candidate] {
							hasMismatch = true
						}
					}
				}
				if hasExact {
					exactCandidates = append(exactCandidates, arg)
				} else if hasMismatch {
					mismatchCandidates = append(mismatchCandidates, arg)
				}
			} else if arg.Type != "enum" {
				argBase := strings.TrimSuffix(arg.Name, "s")
				if argBase == prefixBase || arg.Name == prefixBase {
					nameCandidates = append(nameCandidates, arg)
				}
			}
		}

		// Determine if the tag can be satisfied at all.
		canSatisfy := len(exactCandidates) > 0 || len(nameCandidates) > 0

		if !canSatisfy && len(mismatchCandidates) > 0 {
			// There ARE enum candidates, but their values lack the tag prefix.
			// This is the enum-alignment bug: warn, don't error.
			for _, arg := range mismatchCandidates {
				result.Warnings = append(result.Warnings, LintFinding{
					Severity: LintWarning,
					Code:     "enum_tag_mismatch",
					Field:    fmt.Sprintf("args[%s] → produces_tags[%d]", arg.Name, i),
					Message: fmt.Sprintf(
						"enum arg %q values %v do not include prefix %q — values like %q will not produce tag %q via collectArgValuesForPrefix (did you mean prefix the values, e.g. %q?)",
						arg.Name, arg.Values, tagBase,
						arg.Values[0], rule.Tag,
						tagBase+arg.Values[0],
					),
				})
			}
		} else if !canSatisfy {
			// No arg can satisfy this prefix at all.
			result.Errors = append(result.Errors, LintFinding{
				Severity: LintError,
				Code:     "unmappable_tag",
				Field:    fmt.Sprintf("produces_tags[%d]", i),
				Message:  fmt.Sprintf("tag glob %q has no arg that can produce values with prefix %q", rule.Tag, tagBase),
			})
		}
	}
}

