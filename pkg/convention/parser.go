package convention

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/naming"
)

// SignerType indicates how a convention:operation message was signed.
type SignerType string

const (
	SignerConventionRegistry SignerType = "convention_registry"
	SignerCampfireKey        SignerType = "campfire_key"
	SignerMemberKey          SignerType = "member_key"
)

// maxRateLimitCeiling is the maximum allowed value for rate_limit.max in a
// convention declaration. Values above this are clamped with a warning.
const maxRateLimitCeiling = 100

// maxResponseTimeout is the maximum allowed value for response_timeout in a
// convention declaration. Values above this are clamped to this value.
const maxResponseTimeout = 5 * time.Minute

// Declaration is a parsed convention:operation message.
type Declaration struct {
	Convention      string          `json:"convention"`
	Version         string          `json:"version"`
	Operation       string          `json:"operation"`
	Description     string          `json:"description,omitempty"`
	Supersedes      string          `json:"supersedes,omitempty"`
	Args            []ArgDescriptor `json:"args,omitempty"`
	ProducesTags    []TagRule       `json:"produces_tags,omitempty"`
	Antecedents     string          `json:"antecedents,omitempty"`
	PayloadRequired bool            `json:"payload_required,omitempty"`
	PayloadSchema   string          `json:"payload_schema,omitempty"`
	Signing         string          `json:"signing"`
	RateLimit       *RateLimit      `json:"rate_limit,omitempty"`
	Steps           []Step          `json:"steps,omitempty"`
	// MinOperatorLevel is the minimum operator provenance level required to
	// execute this operation. 0 means no restriction (default). The executor
	// checks the sender's level against this value before dispatching.
	// See Operator Provenance Convention v0.1 §8.
	MinOperatorLevel int `json:"min_operator_level,omitempty"`
	// Views declares named views associated with this convention. When a
	// declaration is loaded, views are auto-published as campfire:view messages
	// and registered as callable MCP tools alongside the write operations.
	Views []ViewDeclaration `json:"views,omitempty"`
	// Response controls how the executor handles responses for this operation.
	// Valid values: "sync" (default), "async", "none".
	// "sync" — caller blocks waiting for a response message.
	// "async" — caller does not block; response arrives out-of-band.
	// "none" — no response is expected.
	Response string `json:"response,omitempty"`
	// ResponseExplicit is true when the "response" field was present in the JSON payload.
	// When false, Response was defaulted to "sync" for backward compat and the executor
	// should treat the operation as a normal (non-blocking) send.
	ResponseExplicit bool `json:"-"`
	// ResponseTimeoutRaw is the raw duration string from JSON (e.g. "30s").
	// Parse populates ResponseTimeout from this field.
	ResponseTimeoutRaw string `json:"response_timeout,omitempty"`
	// ResponseTimeout is the maximum time to wait for a sync response.
	// Populated by Parse from ResponseTimeoutRaw.
	// Defaults to 30s when Response is "sync" and no value is specified.
	ResponseTimeout time.Duration `json:"-"`
	// Source metadata (populated during Parse, not from JSON payload)
	MessageID  string     `json:"-"`
	SignerKey  string     `json:"-"`
	SignerType SignerType `json:"-"`
}

// ViewDeclaration defines a named view within a convention declaration.
type ViewDeclaration struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Predicate   string `json:"predicate"` // S-expression filter
}

// ArgDescriptor describes an argument to a convention operation.
type ArgDescriptor struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Required    bool     `json:"required,omitempty"`
	Default     any      `json:"default,omitempty"`
	Description string   `json:"description,omitempty"`
	MaxLength   int      `json:"max_length,omitempty"`
	Min         int      `json:"min,omitempty"`
	MinSet      bool     `json:"-"` // populated by UnmarshalJSON; true when "min" was explicitly present
	Max         int      `json:"max,omitempty"`
	MaxCount    int      `json:"max_count,omitempty"`
	Pattern     string   `json:"pattern,omitempty"`
	Values      []string `json:"values,omitempty"`
	Repeated    bool     `json:"repeated,omitempty"`
}

// UnmarshalJSON implements json.Unmarshaler for ArgDescriptor so that MinSet is
// populated correctly: it is true only when the "min" key was explicitly present
// in the JSON object, allowing callers to distinguish "min=0 declared" from
// "min not declared" (both produce Min==0 at the Go level).
// Regression fix: campfire-agent-bnq — negative values were rejected when min
// was not declared because the zero value of Min (int) enforced a floor of 0.
func (a *ArgDescriptor) UnmarshalJSON(data []byte) error {
	// Inspect raw keys to detect whether "min" was explicitly present.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// Alias type to avoid infinite recursion during standard decode.
	type argAlias ArgDescriptor
	var alias argAlias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}
	*a = ArgDescriptor(alias)

	_, a.MinSet = raw["min"]
	return nil
}

// TagRule describes a tag that an operation produces.
type TagRule struct {
	Tag         string   `json:"tag"`
	Cardinality string   `json:"cardinality"`
	Values      []string `json:"values,omitempty"`
	Max         int      `json:"max,omitempty"`
	Pattern     string   `json:"pattern,omitempty"`
}

// RateLimit constrains how often an operation can be invoked.
type RateLimit struct {
	Max    int    `json:"max"`
	Per    string `json:"per"`
	Window string `json:"window"`
}

// Step describes one step in a multi-step workflow.
type Step struct {
	Action         string         `json:"action"`
	Description    string         `json:"description,omitempty"`
	FutureTags     []string       `json:"future_tags,omitempty"`
	FuturePayload  map[string]any `json:"future_payload,omitempty"`
	ResultBinding  string         `json:"result_binding,omitempty"`
	Tags           []string       `json:"tags,omitempty"`
	AntecedentRefs []string       `json:"antecedents,omitempty"`
	PayloadSchema  string         `json:"payload_schema,omitempty"`
}

// ConformanceResult is the output of Parse.
type ConformanceResult struct {
	Valid                 bool
	Trusted               bool
	CampfireKeyAuthorized bool
	VersionSuperseded     bool
	Warnings              []string
}

// ConventionOperationTag is the tag used to identify convention operation declaration messages.
const ConventionOperationTag = "convention:operation"

const conventionRevokeTag = "convention:revoke"

var knownArgTypes = map[string]bool{
	"string": true, "integer": true, "duration": true, "boolean": true,
	"key": true, "campfire": true, "message_id": true, "json": true,
	"tag_set": true, "enum": true,
}

var validCardinalities = map[string]bool{
	"exactly_one": true, "at_most_one": true, "zero_to_many": true,
}

var validAntecedents = map[string]bool{
	"none": true, "exactly_one(target)": true, "exactly_one(self_prior)": true,
	"zero_or_one(self_prior)": true,
}

var validPerValues = map[string]bool{
	"sender": true, "campfire_id": true, "sender_and_campfire_id": true,
}

var deniedTagPrefixes = []string{naming.TagPrefix, campfire.TagPrefix}
var deniedTagExact = map[string]bool{
	"future": true, "fulfills": true, ConventionOperationTag: true, "convention:schema": true,
	"convention:revoke": true,
}

// innerQuantRe checks if a group body contains a quantifier.
var innerQuantRe = regexp.MustCompile(`[*+?]|\{[0-9]`)

const maxPatternLen = 64
const maxAlternations = 10

// Parse validates a convention:operation message against the conformance checker.
func Parse(msgTags []string, payload []byte, senderKey, campfireKey string) (*Declaration, *ConformanceResult, error) {
	result := &ConformanceResult{Valid: true, CampfireKeyAuthorized: true}

	// Check 1: Tag presence — exactly one convention:operation tag.
	count := 0
	for _, t := range msgTags {
		if t == ConventionOperationTag {
			count++
		}
	}
	if count == 0 {
		return nil, nil, fmt.Errorf("missing required tag %q", ConventionOperationTag)
	}
	if count > 1 {
		return nil, nil, fmt.Errorf("duplicate tag %q (found %d, expected 1)", ConventionOperationTag, count)
	}

	// Check 2: Payload validity.
	var decl Declaration
	if err := json.Unmarshal(payload, &decl); err != nil {
		return nil, nil, fmt.Errorf("invalid JSON payload: %w", err)
	}
	if decl.Convention == "" {
		return nil, nil, fmt.Errorf("missing required field: convention")
	}
	if decl.Version == "" {
		return nil, nil, fmt.Errorf("missing required field: version")
	}
	if decl.Operation == "" {
		return nil, nil, fmt.Errorf("missing required field: operation")
	}
	if decl.Signing == "" {
		return nil, nil, fmt.Errorf("missing required field: signing")
	}

	// Check 2b: Response field validation and defaults.
	validResponseValues := map[string]bool{"sync": true, "async": true, "none": true}
	if decl.Response == "" {
		decl.Response = "sync"
		decl.ResponseExplicit = false
	} else if !validResponseValues[decl.Response] {
		return nil, nil, fmt.Errorf("invalid response value %q: must be one of \"sync\", \"async\", \"none\"", decl.Response)
	} else {
		decl.ResponseExplicit = true
	}
	if decl.ResponseTimeoutRaw != "" {
		d, err := time.ParseDuration(decl.ResponseTimeoutRaw)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid response_timeout %q: %w", decl.ResponseTimeoutRaw, err)
		}
		if d < 0 {
			return nil, nil, fmt.Errorf("invalid response_timeout %q: must be a non-negative duration", decl.ResponseTimeoutRaw)
		}
		if d > maxResponseTimeout {
			result.Warnings = append(result.Warnings, fmt.Sprintf("response_timeout clamped from %q to %q", decl.ResponseTimeoutRaw, maxResponseTimeout))
			d = maxResponseTimeout
		}
		decl.ResponseTimeout = d
	} else {
		decl.ResponseTimeout = 30 * time.Second
	}

	// Check 3: Arg type validation.
	for i, arg := range decl.Args {
		if !knownArgTypes[arg.Type] {
			return nil, nil, fmt.Errorf("args[%d] (%q): unknown type %q", i, arg.Name, arg.Type)
		}
	}

	// Check 4: Cardinality validation.
	for i, tr := range decl.ProducesTags {
		if !validCardinalities[tr.Cardinality] {
			return nil, nil, fmt.Errorf("produces_tags[%d] (%q): invalid cardinality %q", i, tr.Tag, tr.Cardinality)
		}
	}

	// Check 5: Antecedent rule validation.
	if decl.Antecedents == "" && len(decl.Steps) == 0 {
		decl.Antecedents = "none"
	}
	if decl.Antecedents != "" && !validAntecedents[decl.Antecedents] {
		return nil, nil, fmt.Errorf("invalid antecedent rule: %q", decl.Antecedents)
	}

	// Check 6: Pattern safety (args).
	for i, arg := range decl.Args {
		if arg.Pattern != "" {
			if err := validatePatternSafety(arg.Pattern); err != nil {
				return nil, nil, fmt.Errorf("args[%d] (%q) pattern: %w", i, arg.Name, err)
			}
		}
	}
	// Check 6: Pattern safety (produces_tags).
	for i, tr := range decl.ProducesTags {
		if tr.Pattern != "" {
			if err := validatePatternSafety(tr.Pattern); err != nil {
				return nil, nil, fmt.Errorf("produces_tags[%d] (%q) pattern: %w", i, tr.Tag, err)
			}
		}
	}

	// Check 7: Campfire-key signing check.
	if decl.Signing == "campfire_key" && senderKey != campfireKey {
		result.CampfireKeyAuthorized = false
		result.Valid = false
		result.Warnings = append(result.Warnings, "campfire_key operation not signed by campfire key")
	}

	// Check 8: Campfire-key workflow prohibition.
	if len(decl.Steps) > 0 && decl.Signing == "campfire_key" {
		return nil, nil, fmt.Errorf("campfire_key operations must be single-step declarations")
	}

	// Check 9: Steps validation.
	if len(decl.Steps) > 0 {
		if err := validateSteps(decl.Steps); err != nil {
			return nil, nil, fmt.Errorf("steps validation: %w", err)
		}
	}

	// Check 10: Tag denylist.
	// Exception: the naming-uri convention may produce naming: tags (it IS the naming protocol).
	// Exception: the convention-extension convention may produce convention:operation and
	// convention:revoke tags (it IS the convention management protocol).
	skipNamingDeny := decl.Convention == "naming-uri"
	skipConventionDeny := decl.Convention == InfrastructureConvention
	for i, tr := range decl.ProducesTags {
		tag := tr.Tag
		if skipNamingDeny && strings.HasPrefix(tag, naming.TagPrefix) {
			continue
		}
		if skipConventionDeny && (tag == ConventionOperationTag || tag == conventionRevokeTag) {
			continue
		}
		if err := checkDeniedTag(tag); err != nil {
			return nil, nil, fmt.Errorf("produces_tags[%d]: %w", i, err)
		}
	}

	// Check 11: Rate limit ceiling.
	if decl.RateLimit != nil {
		clampRateLimit(decl.RateLimit, result)
	}

	// Populate source metadata.
	decl.SignerKey = senderKey
	if decl.Signing == "campfire_key" {
		// Only grant campfire_key signer type when the sender is actually the campfire key.
		// If Check 7 rejected this (result.CampfireKeyAuthorized == false), the effective
		// signer type is member_key — preventing ResolveAuthority from granting operational
		// authority to a member-key-signed declaration claiming campfire_key signing.
		if result.CampfireKeyAuthorized {
			decl.SignerType = SignerCampfireKey
		} else {
			decl.SignerType = SignerMemberKey
		}
	} else if decl.Signing == "convention_registry" {
		decl.SignerType = SignerConventionRegistry
	} else {
		decl.SignerType = SignerMemberKey
	}

	return &decl, result, nil
}

// validatePatternSafety checks a regex pattern against the safe subset.
func validatePatternSafety(pattern string) error {
	if len(pattern) > maxPatternLen {
		return fmt.Errorf("pattern too long (%d chars, max %d)", len(pattern), maxPatternLen)
	}
	if _, err := regexp.Compile(pattern); err != nil {
		return fmt.Errorf("invalid regex: %w", err)
	}
	if hasNestedQuantifiers(pattern) {
		return fmt.Errorf("nested quantifiers detected")
	}
	if branches := maxAlternationBranches(pattern); branches > maxAlternations {
		return fmt.Errorf("too many alternation branches (%d, max %d)", branches, maxAlternations)
	}
	return nil
}

// hasNestedQuantifiers detects patterns like (a+)+ where a group containing
// a quantifier is itself quantified.
func hasNestedQuantifiers(pattern string) bool {
	depth := 0
	groupStart := make([]int, 0, 8)
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '\\' {
			i++ // skip escaped char
			continue
		}
		switch pattern[i] {
		case '(':
			groupStart = append(groupStart, i)
			depth++
		case ')':
			if depth > 0 {
				start := groupStart[len(groupStart)-1]
				groupStart = groupStart[:len(groupStart)-1]
				depth--
				body := pattern[start+1 : i]
				// Check if group body has a quantifier.
				if innerQuantRe.MatchString(body) {
					// Check if character after ')' is a quantifier.
					if i+1 < len(pattern) {
						next := pattern[i+1]
						if next == '*' || next == '+' || next == '?' || next == '{' {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

// maxAlternationBranches returns the max number of alternation branches
// within any single group (or the top level) of the pattern.
func maxAlternationBranches(pattern string) int {
	max := countBranches(pattern)
	depth := 0
	groupStart := make([]int, 0, 8)
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '\\' {
			i++
			continue
		}
		switch pattern[i] {
		case '(':
			groupStart = append(groupStart, i)
			depth++
		case ')':
			if depth > 0 {
				start := groupStart[len(groupStart)-1]
				groupStart = groupStart[:len(groupStart)-1]
				depth--
				body := pattern[start+1 : i]
				if b := countBranches(body); b > max {
					max = b
				}
			}
		}
	}
	return max
}

// countBranches counts alternation branches at the top level of s.
func countBranches(s string) int {
	branches := 1
	depth := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		case '|':
			if depth == 0 {
				branches++
			}
		}
	}
	return branches
}

// validateSteps checks step bindings and forward references.
func validateSteps(steps []Step) error {
	bindings := make(map[string]bool)
	for i, step := range steps {
		// Check variable references in antecedent refs.
		for _, ref := range step.AntecedentRefs {
			if err := checkVarRef(ref, bindings); err != nil {
				return fmt.Errorf("step[%d]: antecedent %q: %w", i, ref, err)
			}
		}
		// Check variable references in tags.
		for _, tag := range step.Tags {
			if err := checkVarRef(tag, bindings); err != nil {
				return fmt.Errorf("step[%d]: tag %q: %w", i, tag, err)
			}
		}
		// Check variable references in future_tags.
		for _, tag := range step.FutureTags {
			if err := checkVarRef(tag, bindings); err != nil {
				return fmt.Errorf("step[%d]: future_tag %q: %w", i, tag, err)
			}
		}
		// Check variable references in future_payload values.
		for k, v := range step.FuturePayload {
			if s, ok := v.(string); ok {
				if err := checkVarRef(s, bindings); err != nil {
					return fmt.Errorf("step[%d]: future_payload[%q]: %w", i, k, err)
				}
			}
		}
		// Register this step's result binding for subsequent steps.
		if step.ResultBinding != "" {
			bindings[step.ResultBinding] = true
		}
	}
	return nil
}

// checkVarRef validates variable references in a string value.
// $self_key is always valid. $<binding>.field must reference a known binding.
func checkVarRef(s string, bindings map[string]bool) error {
	idx := 0
	for idx < len(s) {
		pos := strings.Index(s[idx:], "$")
		if pos < 0 {
			break
		}
		pos += idx
		// Extract variable name (up to next non-alphanumeric/underscore/dot).
		end := pos + 1
		for end < len(s) && (s[end] == '_' || s[end] == '.' || isAlnum(s[end])) {
			end++
		}
		varName := s[pos+1 : end]
		if varName == "" {
			idx = end
			continue
		}
		if varName == "self_key" {
			idx = end
			continue
		}
		// Check for $binding.field pattern.
		parts := strings.SplitN(varName, ".", 2)
		binding := parts[0]
		if !bindings[binding] {
			return fmt.Errorf("unbound variable reference $%s", varName)
		}
		idx = end
	}
	return nil
}

func isAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// checkDeniedTag verifies a tag is not in the denylist.
func checkDeniedTag(tag string) error {
	// Check exact matches.
	if deniedTagExact[tag] {
		return fmt.Errorf("tag %q is reserved", tag)
	}
	// Check prefix matches. The tag itself or any value it could match
	// must not start with denied prefixes.
	for _, prefix := range deniedTagPrefixes {
		if strings.HasPrefix(tag, prefix) {
			return fmt.Errorf("tag %q overlaps with reserved prefix %q", tag, prefix)
		}
	}
	return nil
}

// clampRateLimit enforces ceiling values and validates the per field.
func clampRateLimit(rl *RateLimit, result *ConformanceResult) {
	if rl.Max > maxRateLimitCeiling {
		result.Warnings = append(result.Warnings, fmt.Sprintf("rate_limit.max clamped from %d to %d", rl.Max, maxRateLimitCeiling))
		rl.Max = maxRateLimitCeiling
	}
	if rl.Window != "" {
		d, err := parseDuration(rl.Window)
		if err == nil && d < time.Minute {
			result.Warnings = append(result.Warnings, fmt.Sprintf("rate_limit.window clamped from %q to \"1m\"", rl.Window))
			rl.Window = "1m"
		}
	}
	if !validPerValues[rl.Per] {
		result.Valid = false
		result.Warnings = append(result.Warnings, fmt.Sprintf("rate_limit.per %q is not valid; must be one of: sender, campfire_id, sender_and_campfire_id", rl.Per))
	}
}

// parseDuration parses duration strings like "1m", "24h", "30s", "7d".
func parseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	// Find where the numeric part ends.
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9') {
		i++
	}
	if i == 0 || i >= len(s) {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	n, err := strconv.Atoi(s[:i])
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	unit := s[i:]
	switch unit {
	case "s":
		return time.Duration(n) * time.Second, nil
	case "m":
		return time.Duration(n) * time.Minute, nil
	case "h":
		return time.Duration(n) * time.Hour, nil
	case "d":
		return time.Duration(n) * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unknown duration unit %q in %q", unit, s)
	}
}
