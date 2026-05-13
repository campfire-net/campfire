package discovery

// verifier.go — concrete implementation of Tier2Verifier (§11 post-join verification).
//
// Implements probe-write-then-observe and unjoin-declaration per cf-discovery-spec.md §11.
// The mechanical send-ack distinction (§11.3.1) is wired here:
//   - Send returns error → send-not-acknowledged → ErrPostJoinVerificationFailed (unjoin).
//   - Send succeeds, Read times out → send-acknowledged-but-observe-timeout
//     → ErrPostJoinVerificationLatency (degrade, do NOT unjoin per §11.3.1).

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
)

// ErrPostJoinVerificationLatency is returned when the probe message was sent
// successfully (send-acknowledged) but was not observed within the probe timeout.
// Per §11.3.1, this indicates a possible high-latency path rather than suppression.
// The caller MUST degrade (not unjoin) when this error is returned.
var ErrPostJoinVerificationLatency = errors.New("post-join verification: probe sent but not observed within timeout (possible high-latency — not suppression)")

// clientTier2Verifier is the standard implementation of Tier2Verifier backed by
// a protocol.Client. It performs §11 probe-write-then-observe and unjoin-declaration.
type clientTier2Verifier struct {
	client *protocol.Client
}

// NewClientTier2Verifier constructs a clientTier2Verifier backed by client.
func NewClientTier2Verifier(client *protocol.Client) Tier2Verifier {
	return &clientTier2Verifier{client: client}
}

// ProbeAndObserve implements Tier2Verifier.ProbeAndObserve per §11.3.
//
// §11.3.1 send-ack distinction:
//   - If client.Send returns an error → send-not-acknowledged → ErrPostJoinVerificationFailed.
//   - If client.Send succeeds → send-acknowledged. Start observe poll.
//   - If probe observed within probeTimeout → nil (verification passes).
//   - If probeTimeout elapses with no observation → ErrPostJoinVerificationLatency.
//
// The returned error from ProbeAndObserve is used by the caller to decide whether
// to unjoin (ErrPostJoinVerificationFailed) or degrade (ErrPostJoinVerificationLatency).
func (v *clientTier2Verifier) ProbeAndObserve(ctx context.Context, campfireID string, probeTimeout time.Duration) error {
	// Step 1: Send probe message tagged discovery:probe.
	msg, err := v.client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    nil, // §11.3: no content other than tag and signature
		Tags:       []string{ProbeTag},
	})
	if err != nil {
		// Send-not-acknowledged: transport rejected the write (step 2 of §11.3.1).
		// Treat as suppression → unjoin.
		return fmt.Errorf("%w: send rejected: %v", ErrPostJoinVerificationFailed, err)
	}

	// Send acknowledged. Capture probe message ID for the observe step (§11.3 step 3).
	probeMsgID := msg.ID

	// Step 3–4: Poll client.Read for the probe message until probeTimeout elapses.
	deadline := time.Now().Add(probeTimeout)
	pollInterval := 200 * time.Millisecond

	for {
		result, readErr := v.client.Read(protocol.ReadRequest{
			CampfireID: campfireID,
			Tags:       []string{ProbeTag},
		})
		if readErr != nil {
			// Read error during observe phase: conservatively return latency error
			// (send was acknowledged; read failure is ambiguous).
			return fmt.Errorf("%w: read error during observe: %v", ErrPostJoinVerificationLatency, readErr)
		}

		for _, m := range result.Messages {
			if m.ID == probeMsgID {
				// Probe observed — verification passes (§11.3 step 4).
				return nil
			}
		}

		if time.Now().After(deadline) {
			// Probe timeout: send was acknowledged but not observed.
			// §11.3.1: this is the latency path, not suppression.
			return ErrPostJoinVerificationLatency
		}

		// Wait before next poll; respect context cancellation.
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: context cancelled during observe: %v", ErrPostJoinVerificationLatency, ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

// PostUnjoinDeclaration implements Tier2Verifier.PostUnjoinDeclaration per §11.4–§11.5.
//
// Builds the 5-field unjoin-declaration, serializes in canonical alphabetical field order
// per §11.5.1, signs with the joiner's Ed25519 key, and posts via client.Send tagged
// discovery:unjoin-declaration.
//
// The signing payload is: "discovery:unjoin-declaration\n<canonical-json>" per §11.5.1.
// The explicit Ed25519 signature is embedded in the declaration payload so third parties
// can verify the claim out-of-band (the message envelope signature also covers it, but
// the embedded signature provides the full signing contract described in §11.5).
func (v *clientTier2Verifier) PostUnjoinDeclaration(ctx context.Context, campfireID, probeMsgID string) error {
	joinerPubkey := v.client.PublicKeyHex()

	// §11.5.1 canonical JSON: alphabetical field order, no whitespace.
	// Fields in alphabetical sort order: campfire_id, joiner_pubkey,
	// observed_inconsistency, probe_msg_id, reason.
	canonicalJSON := buildCanonicalUnjoinJSON(campfireID, joinerPubkey, probeMsgID)

	// Build signing payload: prefix + LF + canonical JSON (§11.5.1).
	var sigPayload bytes.Buffer
	sigPayload.WriteString(UnjoinDeclarationTag) // "discovery:unjoin-declaration"
	sigPayload.WriteByte(0x0A)                   // LF separator
	sigPayload.Write(canonicalJSON)

	// Sign with the joiner's Ed25519 key.
	signer := v.client.NewSigner()
	if signer == nil {
		return fmt.Errorf("posting unjoin-declaration: client has no signing identity")
	}
	sig, err := signer.Sign(sigPayload.Bytes())
	if err != nil {
		return fmt.Errorf("signing unjoin-declaration: %w", err)
	}

	// Build the full declaration payload including the explicit signature.
	// The signature is hex-encoded so it round-trips through JSON cleanly.
	sigHex := hex.EncodeToString(sig)
	declPayload := buildUnjoinDeclarationPayload(canonicalJSON, sigHex)

	// Post tagged discovery:unjoin-declaration.
	_, err = v.client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    declPayload,
		Tags:       []string{UnjoinDeclarationTag},
	})
	if err != nil {
		return fmt.Errorf("sending unjoin-declaration: %w", err)
	}
	return nil
}

// CanonicalUnjoinSigningPayload returns the full signing payload for an unjoin-declaration
// per §11.5.1: "discovery:unjoin-declaration\n<canonical-json>".
// The canonical JSON has 5 fields in alphabetical order with no whitespace.
// This is exported for test verification and out-of-band signature checking.
func CanonicalUnjoinSigningPayload(campfireID, joinerPubkey, probeMsgID string) []byte {
	canonicalJSON := buildCanonicalUnjoinJSON(campfireID, joinerPubkey, probeMsgID)
	var b bytes.Buffer
	b.WriteString(UnjoinDeclarationTag) // "discovery:unjoin-declaration"
	b.WriteByte(0x0A)                   // LF separator
	b.Write(canonicalJSON)
	return b.Bytes()
}

// buildCanonicalUnjoinJSON constructs the canonical JSON object with the 5 unjoin fields
// in alphabetical order per §11.5.1. No whitespace, no trailing newline.
//
// Field order (alphabetical): campfire_id, joiner_pubkey, observed_inconsistency,
// probe_msg_id, reason.
func buildCanonicalUnjoinJSON(campfireID, joinerPubkey, probeMsgID string) []byte {
	// Hand-construct to guarantee field order and absence of extra whitespace.
	// Using encoding/json would produce the correct values but does not guarantee
	// map-key order; struct serialization would guarantee order but requires a
	// struct definition. Explicit construction is the simplest way to guarantee
	// the exact byte sequence defined in §11.5.1.
	const observedInconsistency = "probe message not visible on read after join"
	const reason = "probe-verification-failed"

	var b bytes.Buffer
	b.WriteByte('{')
	writeJSONStringField(&b, "campfire_id", campfireID, false)
	writeJSONStringField(&b, "joiner_pubkey", joinerPubkey, true)
	writeJSONStringField(&b, "observed_inconsistency", observedInconsistency, true)
	writeJSONStringField(&b, "probe_msg_id", probeMsgID, true)
	writeJSONStringField(&b, "reason", reason, true)
	b.WriteByte('}')
	return b.Bytes()
}

// writeJSONStringField writes `,"key":"value"` (or `"key":"value"` if first) to b.
// value must not contain characters that require JSON escaping beyond the basic set
// (the campfire_id, pubkey hex, and fixed strings used here do not).
func writeJSONStringField(b *bytes.Buffer, key, value string, comma bool) {
	if comma {
		b.WriteByte(',')
	}
	b.WriteByte('"')
	b.WriteString(key)
	b.WriteString(`":"`)
	// JSON-escape the value: escape backslash and double-quote per RFC 8259.
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			if c < 0x20 {
				// Control characters (U+0000–U+001F) must be escaped per RFC 8259.
				fmt.Fprintf(b, `\u%04x`, c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
}

// buildUnjoinDeclarationPayload combines the canonical JSON with the signature hex
// into the full message payload. The payload contains the canonical declaration fields
// plus the joiner_signature for out-of-band verification.
func buildUnjoinDeclarationPayload(canonicalJSON []byte, sigHex string) []byte {
	// Wrap canonical JSON object with the joiner_signature field appended.
	// Strip trailing '}', append signature field, close.
	if len(canonicalJSON) < 1 || canonicalJSON[len(canonicalJSON)-1] != '}' {
		return canonicalJSON // should not happen
	}
	base := canonicalJSON[:len(canonicalJSON)-1]
	var b bytes.Buffer
	b.Write(base)
	b.WriteString(`,"joiner_signature":"`)
	b.WriteString(sigHex)
	b.WriteString(`"}`)
	return b.Bytes()
}
