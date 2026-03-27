package trust

// Envelope wraps campfire content with trust and safety metadata
// per Trust Convention v0.2 §6.
//
// v0.2 replaces trust_chain (chain position) with trust_status (local policy
// compatibility) and adds fingerprint_match per §6.2.
type Envelope struct {
	Verified         VerifiedFields         `json:"verified"`
	RuntimeComputed  RuntimeComputedFields  `json:"runtime_computed"`
	CampfireAsserted CampfireAssertedFields `json:"campfire_asserted"`
	Tainted          TaintedFields          `json:"tainted"`
}

// VerifiedFields holds cryptographically verifiable identifiers.
type VerifiedFields struct {
	CampfireID string `json:"campfire_id"`
}

// RuntimeComputedFields holds fields computed at runtime, not signed by any party.
// trust_status and fingerprint_match replace trust_chain (v0.1) per Trust v0.2 §6.2.
type RuntimeComputedFields struct {
	CampfireName          string      `json:"campfire_name"`
	RegisteredInDirectory bool        `json:"registered_in_directory"`
	TrustStatus           TrustStatus `json:"trust_status"`
	FingerprintMatch      bool        `json:"fingerprint_match"`
	SanitizationApplied   []string    `json:"sanitization_applied"`
	// OperatorProvenance is the sender's operator provenance level (0–3).
	// See Operator Provenance Convention v0.1 §8.2 and Trust Convention v0.2 §6.3.
	// Null/absent means provenance has not been computed (e.g. during bootstrap).
	OperatorProvenance *int `json:"operator_provenance,omitempty"`
}

// CampfireAssertedFields holds campfire-reported data that is not independently verifiable.
type CampfireAssertedFields struct {
	MemberCount  int    `json:"member_count,omitempty"`
	CreatedAge   string `json:"created_age,omitempty"`
	JoinProtocol string `json:"join_protocol,omitempty"`
}

// TaintedFields holds member-generated content that must be treated as untrusted.
type TaintedFields struct {
	ContentClassification string `json:"content_classification"`
	Content               any    `json:"content"`
}

// envelopeConfig holds options for envelope building.
type envelopeConfig struct {
	campfireName          string
	registeredInDirectory bool
	memberCount           int
	createdAge            string
	joinProtocol          string
	maxStringLen          int
	fingerprintMatch      bool
	operatorProvenance    *int
}

// EnvelopeOption configures envelope building.
type EnvelopeOption func(*envelopeConfig)

// WithCampfireName sets the campfire name in the envelope.
func WithCampfireName(name string) EnvelopeOption {
	return func(c *envelopeConfig) {
		c.campfireName = name
	}
}

// WithDirectoryRegistration sets whether the campfire is registered in the directory.
func WithDirectoryRegistration(registered bool) EnvelopeOption {
	return func(c *envelopeConfig) {
		c.registeredInDirectory = registered
	}
}

// WithMemberCount sets the member count in campfire_asserted.
func WithMemberCount(count int) EnvelopeOption {
	return func(c *envelopeConfig) {
		c.memberCount = count
	}
}

// WithCreatedAge sets the created_age in campfire_asserted.
func WithCreatedAge(age string) EnvelopeOption {
	return func(c *envelopeConfig) {
		c.createdAge = age
	}
}

// WithJoinProtocol sets the join_protocol in campfire_asserted.
// Valid values: "open", "invite-only". Reported as campfire-asserted (not cryptographically verified).
func WithJoinProtocol(protocol string) EnvelopeOption {
	return func(c *envelopeConfig) {
		c.joinProtocol = protocol
	}
}

// WithMaxStringLen overrides the default max string length for sanitization.
func WithMaxStringLen(maxLen int) EnvelopeOption {
	return func(c *envelopeConfig) {
		c.maxStringLen = maxLen
	}
}

// WithFingerprintMatch sets the fingerprint_match field in runtime_computed.
// True means the campfire's conventions have matching semantic fingerprints
// with the agent's local policy.
func WithFingerprintMatch(match bool) EnvelopeOption {
	return func(c *envelopeConfig) {
		c.fingerprintMatch = match
	}
}

// WithOperatorProvenance sets the operator_provenance level in runtime_computed.
// The level is 0–3 per Operator Provenance Convention v0.1 §4.
// Call with nil to explicitly omit the field (e.g. during bootstrap init).
func WithOperatorProvenance(level int) EnvelopeOption {
	return func(c *envelopeConfig) {
		c.operatorProvenance = &level
	}
}

// BuildEnvelope creates a safety envelope for campfire content per Trust Convention v0.2 §6.
// trustStatus is the campfire's relationship to the agent's local policy (§6.2).
// fingerprintMatch indicates whether the campfire's semantic fingerprints match the local policy.
func BuildEnvelope(campfireID string, trustStatus TrustStatus, content any, opts ...EnvelopeOption) *Envelope {
	cfg := &envelopeConfig{
		maxStringLen: 1024,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	sanitized, steps := SanitizeContent(content, cfg.maxStringLen)
	if steps == nil {
		steps = []string{}
	}

	campfireName := cfg.campfireName
	if campfireName == "" {
		campfireName = "[unregistered]"
	}

	return &Envelope{
		Verified: VerifiedFields{
			CampfireID: campfireID,
		},
		RuntimeComputed: RuntimeComputedFields{
			CampfireName:          campfireName,
			RegisteredInDirectory: cfg.registeredInDirectory,
			TrustStatus:           trustStatus,
			FingerprintMatch:      cfg.fingerprintMatch,
			SanitizationApplied:   steps,
			OperatorProvenance:    cfg.operatorProvenance,
		},
		CampfireAsserted: CampfireAssertedFields{
			MemberCount:  cfg.memberCount,
			CreatedAge:   cfg.createdAge,
			JoinProtocol: cfg.joinProtocol,
		},
		Tainted: TaintedFields{
			ContentClassification: "tainted",
			Content:               sanitized,
		},
	}
}

// envelopeKeys are the top-level keys of the Envelope struct that content must not mimic.
var envelopeKeys = map[string]struct{}{
	"verified":         {},
	"runtime_computed": {},
	"campfire_asserted": {},
	"tainted":          {},
}

// Sanitize sanitizes a string: truncates to maxLen, strips control chars (except \n and \t),
// removes null bytes. Returns sanitized string and list of sanitization steps applied.
func Sanitize(s string, maxLen int) (string, []string) {
	var steps []string

	// Null byte removal (check before rune processing to track separately).
	hasNull := false
	for i := 0; i < len(s); i++ {
		if s[i] == 0x00 {
			hasNull = true
			break
		}
	}

	// Strip control chars (U+0000-U+001F except \n=0x0A and \t=0x09) and null bytes.
	stripped := make([]rune, 0, len(s))
	hadControl := false
	for _, r := range s {
		if r == '\x00' {
			// counted separately as null_bytes_removed
			continue
		}
		if r >= 0x01 && r <= 0x1F && r != '\n' && r != '\t' {
			hadControl = true
			continue
		}
		stripped = append(stripped, r)
	}

	if hasNull {
		steps = append(steps, "null_bytes_removed")
	}
	if hadControl {
		steps = append(steps, "control_chars_stripped")
	}

	result := string(stripped)

	// Truncation.
	if len([]rune(result)) > maxLen {
		result = string([]rune(result)[:maxLen])
		steps = append(steps, "truncated")
	}

	return result, steps
}

// SanitizeContent recursively sanitizes content, escaping anything that mimics envelope structure.
// Returns sanitized content and list of sanitization steps applied.
func SanitizeContent(content any, maxLen int) (any, []string) {
	var allSteps []string
	addStep := func(step string) {
		for _, s := range allSteps {
			if s == step {
				return
			}
		}
		allSteps = append(allSteps, step)
	}

	result := sanitizeValue(content, maxLen, addStep)
	return result, allSteps
}

// sanitizeValue recursively sanitizes a single value.
func sanitizeValue(v any, maxLen int, addStep func(string)) any {
	switch val := v.(type) {
	case string:
		sanitized, steps := Sanitize(val, maxLen)
		for _, s := range steps {
			addStep(s)
		}
		return sanitized

	case map[string]any:
		out := make(map[string]any, len(val))
		mimicry := false
		for k, mv := range val {
			newKey := k
			if _, reserved := envelopeKeys[k]; reserved {
				newKey = "content_" + k
				mimicry = true
			}
			out[newKey] = sanitizeValue(mv, maxLen, addStep)
		}
		if mimicry {
			addStep("envelope_mimicry_escaped")
		}
		return out

	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = sanitizeValue(item, maxLen, addStep)
		}
		return out

	default:
		// Numeric, bool, nil — pass through unchanged.
		return v
	}
}
