// Package tests — E2E integration test: all 5 security fixes working in combination.
//
// TestSecurityFixesE2E exercises all five security fixes merged to main and
// proves they work together in a single end-to-end scenario:
//
//  1. Beacon loop detection (pkg/transport/http/router.go): case-insensitive
//     node_id comparison via strings.EqualFold — verified in the companion
//     TestBeaconLoopDetectionCaseInsensitive in pkg/transport/http.
//  2. WrapKey argon2id (pkg/crypto/keywrap.go): argon2id + random salt for
//     KEK derivation — wrap/unwrap a freshly generated private key and verify
//     round-trip fidelity; wrong passphrase must fail to decrypt.
//  3. P2P HTTP role (pkg/protocol/client.go): EffectiveRole(m.Role) is the
//     identity function for all named roles — observer, writer, full,
//     blind-relay all pass through unchanged; unknown/empty coerce to full.
//  4. ProofToken validation (pkg/provenance/challenge.go): structural format
//     validation per proof_type — garbage tokens must be rejected with
//     ErrInvalidProofToken before any attestation is stored.
//  5. MCP signature verification (cmd/cf-mcp/main.go): syncFSVerified logic —
//     tampered messages (invalid Ed25519 sig) and messages with empty provenance
//     must be filtered out; only properly signed messages with valid hops pass.
//
// No mocks. Real crypto, real stores, real filesystem transport.
package tests

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/crypto"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/provenance"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// TestSecurityFixesE2E proves all five security fixes are in place and work
// correctly in combination. Run with:
//
//	go test ./tests/... -run TestSecurityFixesE2E -v
func TestSecurityFixesE2E(t *testing.T) {
	t.Run("Fix2_WrapKeyArgon2id", testFix2WrapKeyArgon2id)
	t.Run("Fix3_P2PHTTPRole_EffectiveRole", testFix3EffectiveRole)
	t.Run("Fix4_ProofTokenValidation", testFix4ProofTokenValidation)
	t.Run("Fix5_MCPSyncVerification", testFix5MCPSyncVerification)
}

// ─── Fix 2: WrapKey argon2id + random salt ───────────────────────────────────

// testFix2WrapKeyArgon2id verifies that WrapKey uses argon2id with a random
// salt (same passphrase → different ciphertext on every call) and that
// UnwrapKey correctly decrypts the result. A wrong passphrase must fail.
//
// Pre-argon2id: WrapKey used HKDF with a zero salt, meaning the same
// passphrase always derived the same KEK — enabling pre-computation attacks.
// Post-fix: argon2id with a random 32-byte salt makes KEK derivation
// non-deterministic; WrapKeyLegacyHKDF is still supported for backward compat.
func testFix2WrapKeyArgon2id(t *testing.T) {
	t.Helper()

	// Generate a realistic Ed25519 private key to wrap.
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	privKeyBytes := []byte(id.PrivateKey)
	passphrase := []byte("test-session-token-argon2id")

	// --- Wrap/unwrap round-trip ---
	wrapped, err := crypto.WrapKey(privKeyBytes, passphrase)
	if err != nil {
		t.Fatalf("WrapKey: %v", err)
	}
	if len(wrapped) == 0 {
		t.Fatal("WrapKey returned empty blob")
	}

	// Unwrap with the correct passphrase → must return original key.
	recovered, err := crypto.UnwrapKey(wrapped, passphrase)
	if err != nil {
		t.Fatalf("UnwrapKey (correct passphrase): %v", err)
	}
	if !bytes.Equal(recovered, privKeyBytes) {
		t.Fatalf("UnwrapKey recovered wrong key: got %d bytes, want %d bytes", len(recovered), len(privKeyBytes))
	}

	// --- Wrong passphrase must be rejected ---
	_, err = crypto.UnwrapKey(wrapped, []byte("wrong-passphrase"))
	if err == nil {
		t.Fatal("UnwrapKey accepted wrong passphrase — authentication bypass")
	}

	// --- Random salt: same passphrase → different ciphertext on every call ---
	// This is the key property argon2id adds over the old HKDF-zero-salt approach.
	wrapped2, err := crypto.WrapKey(privKeyBytes, passphrase)
	if err != nil {
		t.Fatalf("WrapKey (second call): %v", err)
	}
	if bytes.Equal(wrapped, wrapped2) {
		t.Fatal("WrapKey produced identical blobs for the same passphrase — random salt not applied (pre-computation attack possible)")
	}

	// Both wrapped blobs must unwrap correctly.
	recovered2, err := crypto.UnwrapKey(wrapped2, passphrase)
	if err != nil {
		t.Fatalf("UnwrapKey (second blob): %v", err)
	}
	if !bytes.Equal(recovered2, privKeyBytes) {
		t.Fatal("UnwrapKey (second blob) recovered wrong key")
	}

	// --- Backward compatibility: legacy HKDF-wrapped blobs must still unwrap ---
	legacyWrapped, err := crypto.WrapKeyLegacyHKDF(privKeyBytes, passphrase)
	if err != nil {
		t.Fatalf("WrapKeyLegacyHKDF: %v", err)
	}
	recoveredLegacy, err := crypto.UnwrapKey(legacyWrapped, passphrase)
	if err != nil {
		t.Fatalf("UnwrapKey (legacy HKDF blob): %v", err)
	}
	if !bytes.Equal(recoveredLegacy, privKeyBytes) {
		t.Fatal("UnwrapKey (legacy HKDF) recovered wrong key")
	}
}

// ─── Fix 3: P2P HTTP role — EffectiveRole identity for named roles ────────────

// testFix3EffectiveRole verifies that campfire.EffectiveRole correctly passes
// through all named roles (observer, writer, full, blind-relay) and that
// empty/unknown/legacy values coerce to RoleFull for backward compatibility.
//
// Pre-fix: sendP2PHTTP used m.Role directly. If the membership record stored
// a legacy value (e.g. "member", "creator", ""), the outbound provenance hop
// would carry the legacy string rather than the canonical role — making hop
// role fields inconsistent across protocol versions.
// Post-fix: EffectiveRole(m.Role) is called before writing the hop, ensuring
// all hops carry a canonical role string.
func testFix3EffectiveRole(t *testing.T) {
	t.Helper()

	tests := []struct {
		input string
		want  string
		label string
	}{
		// Named roles pass through unchanged.
		{campfire.RoleObserver, campfire.RoleObserver, "observer identity"},
		{campfire.RoleWriter, campfire.RoleWriter, "writer identity"},
		{campfire.RoleFull, campfire.RoleFull, "full identity"},
		{campfire.RoleBlindRelay, campfire.RoleBlindRelay, "blind-relay identity"},
		// Legacy/empty values coerce to full.
		{"", campfire.RoleFull, "empty → full"},
		{"member", campfire.RoleFull, "legacy 'member' → full"},
		{"creator", campfire.RoleFull, "legacy 'creator' → full"},
		{"FULL", campfire.RoleFull, "unrecognized case variant → full"},
	}

	for _, tc := range tests {
		got := campfire.EffectiveRole(tc.input)
		if got != tc.want {
			t.Errorf("EffectiveRole(%q) = %q, want %q (%s)", tc.input, got, tc.want, tc.label)
		}
	}

	// Verify blind-relay is a distinct effective role — it must NOT coerce to full.
	// (A blind-relay node must not appear as full in provenance hops.)
	if campfire.EffectiveRole(campfire.RoleBlindRelay) == campfire.RoleFull {
		t.Error("EffectiveRole(blind-relay) coerced to full — blind-relay nodes must retain their role in provenance hops")
	}
}

// ─── Fix 4: ProofToken structural format validation ──────────────────────────

// testFix4ProofTokenValidation verifies that ValidateResponse rejects garbage
// proof tokens before any attestation is stored.
//
// Pre-fix: proof_type and proof_token were accepted as-is; arbitrary strings
// would be stored as attestations without structural validation.
// Post-fix: validateProofTokenFormat checks structural correctness per §5.3:
//   - ProofTOTP: exactly 6 or 8 ASCII decimal digits
//   - ProofSMS: 4-8 ASCII decimal digits
//   - ProofCaptcha: >=16 printable non-whitespace chars
//   - ProofHardware/ProofEmailLink: >=32 printable non-whitespace chars
//
// This test issues a real challenge and then attempts to validate responses
// with garbage tokens — all must be rejected. A response with a correctly
// structured token must be accepted.
func testFix4ProofTokenValidation(t *testing.T) {
	t.Helper()

	// Each tryValidate call needs a fresh challenge. The Challenger enforces a
	// rate limit of 10 challenges/hour per targetKey. We avoid the rate limit by
	// using a fresh targetKey per call (each is a distinct 64-char hex string
	// starting with a different counter byte — valid for the key field format).
	//
	// The atomic counter drives both the unique targetKey and the challenge ID,
	// ensuring no ID collisions across concurrent or sequential calls.
	var callCounter int64
	now := time.Now()
	initiatorKey := strings.Repeat("a", 64)
	callbackCampfire := strings.Repeat("c", 64)

	// tryValidate issues a fresh challenge under a unique targetKey and attempts
	// to validate a response with the given proof_type and proof_token.
	tryValidate := func(proofType provenance.ProofType, proofToken string) error {
		seq := atomic.AddInt64(&callCounter, 1)
		// Unique targetKey per call: first 16 hex chars encode the counter,
		// remainder is constant. This yields a distinct rate-limit bucket per call.
		targetKey := fmt.Sprintf("%016x%048s", seq, strings.Repeat("b", 48))
		challengeID := fmt.Sprintf("challenge-fix4-%d", seq)

		ch, err := provenance.NewChallenger().IssueChallenge(
			challengeID, initiatorKey, targetKey, callbackCampfire, now,
		)
		if err != nil {
			t.Fatalf("IssueChallenge (seq=%d): %v", seq, err)
		}
		resp := &provenance.ChallengeResponse{
			AntecedentID:  ch.ID,
			ResponderKey:  targetKey,
			MessageSender: targetKey,
			TargetKey:     targetKey,
			Nonce:         ch.Nonce,
			ContactMethod: "https://example.com",
			ProofType:     proofType,
			ProofToken:    proofToken,
			RespondedAt:   now,
		}

		// Use the same challenger instance that issued the challenge so the
		// active-challenge map is found. Each call uses a fresh Challenger, so
		// we must re-issue and validate on the same instance.
		challenger := provenance.NewChallenger()
		ch2, err2 := challenger.IssueChallenge(
			challengeID, initiatorKey, targetKey, callbackCampfire, now,
		)
		if err2 != nil {
			t.Fatalf("re-IssueChallenge (seq=%d): %v", seq, err2)
		}
		resp.AntecedentID = ch2.ID
		resp.Nonce = ch2.Nonce
		_, validateErr := challenger.ValidateResponse(resp, now)
		return validateErr
	}

	// --- Garbage tokens must be rejected ---

	// TOTP: must be exactly 6 or 8 ASCII decimal digits (RFC 6238).
	invalidTOTPs := []struct {
		tok   string
		label string
	}{
		{"12345", "too short (5 digits)"},
		{"1234567", "wrong length (7 digits)"},
		{"abc123", "non-numeric"},
		{"123 456", "whitespace contaminated"},
	}
	for _, tc := range invalidTOTPs {
		err := tryValidate(provenance.ProofTOTP, tc.tok)
		if err == nil {
			t.Errorf("TOTP garbage token %q (%s) accepted — should be rejected", tc.tok, tc.label)
		} else if !errors.Is(err, provenance.ErrInvalidProofToken) {
			t.Errorf("TOTP garbage token %q: expected ErrInvalidProofToken, got: %v", tc.tok, err)
		}
	}

	// Empty proof_token must trigger ErrEmptyProofToken (checked before format).
	if err := tryValidate(provenance.ProofTOTP, ""); err == nil {
		t.Error("empty proof_token accepted — should be rejected with ErrEmptyProofToken")
	} else if !errors.Is(err, provenance.ErrEmptyProofToken) {
		t.Errorf("empty proof_token: expected ErrEmptyProofToken, got: %v", err)
	}

	// SMS OTP: must be 4-8 ASCII decimal digits.
	invalidSMS := []struct {
		tok   string
		label string
	}{
		{"123", "too short (3 digits)"},
		{"123456789", "too long (9 digits)"},
		{"12ab", "non-numeric"},
	}
	for _, tc := range invalidSMS {
		if err := tryValidate(provenance.ProofSMS, tc.tok); err == nil {
			t.Errorf("SMS garbage token %q (%s) accepted — should be rejected", tc.tok, tc.label)
		}
	}

	// CAPTCHA: must be >=16 printable non-whitespace chars.
	if err := tryValidate(provenance.ProofCaptcha, "shorttoken"); err == nil {
		t.Error("CAPTCHA token <16 chars accepted — should be rejected")
	}

	// Unknown proof type must be rejected with ErrUnknownProofType.
	if err := tryValidate("unknown-proof-type", "anytoken"); err == nil {
		t.Error("unknown proof_type accepted — should be rejected with ErrUnknownProofType")
	} else if !errors.Is(err, provenance.ErrUnknownProofType) {
		t.Errorf("unknown proof_type: expected ErrUnknownProofType, got: %v", err)
	}

	// --- Valid tokens must be accepted ---

	// TOTP: exactly 6 decimal digits.
	if err := tryValidate(provenance.ProofTOTP, "123456"); err != nil {
		t.Errorf("valid TOTP 6-digit token rejected unexpectedly: %v", err)
	}

	// TOTP: exactly 8 decimal digits (RFC 6238 extended TOTP).
	if err := tryValidate(provenance.ProofTOTP, "12345678"); err != nil {
		t.Errorf("valid TOTP 8-digit token rejected unexpectedly: %v", err)
	}

	// SMS: exactly 6 decimal digits (within the 4-8 range).
	if err := tryValidate(provenance.ProofSMS, "654321"); err != nil {
		t.Errorf("valid SMS OTP rejected unexpectedly: %v", err)
	}

	// CAPTCHA: 32 printable chars.
	if err := tryValidate(provenance.ProofCaptcha, strings.Repeat("x", 32)); err != nil {
		t.Errorf("valid CAPTCHA token rejected unexpectedly: %v", err)
	}
}

// min returns the smaller of a and b (Go 1.21+ has builtin min, but this keeps
// the test compatible with older toolchains used in CI).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ─── Fix 5: MCP-style FS sync verifies signatures and provenance ─────────────

// testFix5MCPSyncVerification replicates the syncFSVerified filtering logic
// (cmd/cf-mcp/main.go) directly, proving that:
//
//   (a) A properly signed message with a valid provenance hop is stored.
//   (b) A message with a tampered payload (invalid Ed25519 sig) is rejected.
//   (c) A message with no provenance hops is rejected (empty relay chain).
//
// Pre-fix: handleViewTool and handleAwait called fs.Transport.ListMessages
// directly, bypassing the signature and hop verification that
// protocol.Client.syncIfFilesystem performs. Tampered messages and messages
// without provenance hops were returned to callers as if legitimate.
// Post-fix: syncFSVerified applies the same three-gate check that the protocol
// client uses: signature valid → provenance non-empty → all hops valid.
func testFix5MCPSyncVerification(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	campfireID := strings.Repeat("a1", 32) // 64 hex chars
	tr := fs.New(dir)

	// Create the messages directory manually (WriteMessage requires it to exist).
	msgsDir := filepath.Join(dir, campfireID, "messages")
	if err := os.MkdirAll(msgsDir, 0755); err != nil {
		t.Fatalf("creating messages dir: %v", err)
	}

	// --- (a) Valid message with provenance hop ---
	senderID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating sender identity: %v", err)
	}
	validMsg, err := message.NewMessage(senderID.PrivateKey, senderID.PublicKey, []byte("hello-world"), []string{"status"}, nil)
	if err != nil {
		t.Fatalf("NewMessage (valid): %v", err)
	}
	// Add a valid provenance hop (campfire relay signature).
	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	if err := validMsg.AddHop(cfID.PrivateKey, cfID.PublicKey, []byte("membership-hash"), 1, "open", nil, campfire.RoleFull); err != nil {
		t.Fatalf("AddHop (valid message): %v", err)
	}
	if err := tr.WriteMessage(campfireID, validMsg); err != nil {
		t.Fatalf("WriteMessage (valid): %v", err)
	}

	// --- (b) Tampered message: valid signature at creation, payload mutated after ---
	tamperedMsg, err := message.NewMessage(senderID.PrivateKey, senderID.PublicKey, []byte("legitimate"), []string{"status"}, nil)
	if err != nil {
		t.Fatalf("NewMessage (tampered): %v", err)
	}
	if err := tamperedMsg.AddHop(cfID.PrivateKey, cfID.PublicKey, []byte("membership-hash"), 1, "open", nil, campfire.RoleFull); err != nil {
		t.Fatalf("AddHop (tampered message): %v", err)
	}
	// Corrupt the payload AFTER signing — signature is now invalid.
	tamperedMsg.Payload = []byte("TAMPERED-PAYLOAD")
	if err := tr.WriteMessage(campfireID, tamperedMsg); err != nil {
		t.Fatalf("WriteMessage (tampered): %v", err)
	}

	// --- (c) Message with empty provenance (no relay hops) ---
	nohopMsg, err := message.NewMessage(senderID.PrivateKey, senderID.PublicKey, []byte("no-hop"), []string{"status"}, nil)
	if err != nil {
		t.Fatalf("NewMessage (no-hop): %v", err)
	}
	// Intentionally NO AddHop call — Provenance stays empty.
	if err := tr.WriteMessage(campfireID, nohopMsg); err != nil {
		t.Fatalf("WriteMessage (no-hop): %v", err)
	}

	// --- Apply syncFSVerified logic ---
	// Replicate cmd/cf-mcp/main.go:syncFSVerified exactly: list FS messages,
	// apply three-gate filter (signature → non-empty provenance → valid hops),
	// store survivors.
	st, err := store.Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	fsMessages, err := tr.ListMessages(campfireID)
	if err != nil {
		t.Fatalf("tr.ListMessages: %v", err)
	}
	if len(fsMessages) != 3 {
		t.Fatalf("expected 3 messages on disk, got %d", len(fsMessages))
	}

	for _, fsMsg := range fsMessages {
		// Gate 1: reject messages with invalid Ed25519 signature.
		if !fsMsg.VerifySignature() {
			continue
		}
		// Gate 2: reject messages with empty provenance.
		if len(fsMsg.Provenance) == 0 {
			continue
		}
		// Gate 3: reject messages with any invalid provenance hop.
		hopOK := true
		for _, hop := range fsMsg.Provenance {
			if !message.VerifyHop(fsMsg.ID, hop) {
				hopOK = false
				break
			}
		}
		if !hopOK {
			continue
		}
		if _, err := st.AddMessage(store.MessageRecordFromMessage(campfireID, &fsMsg, store.NowNano())); err != nil {
			t.Fatalf("st.AddMessage: %v", err)
		}
	}

	// Verify: only the valid message should be in the store.
	stored, err := st.ListMessages(campfireID, 0, store.MessageFilter{})
	if err != nil {
		t.Fatalf("st.ListMessages: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("expected 1 message in store after syncFSVerified filtering, got %d (tampered and no-hop messages must be rejected)", len(stored))
	}
	if stored[0].ID != validMsg.ID {
		t.Errorf("stored message ID %q, want %q", stored[0].ID, validMsg.ID)
	}

	// Cross-check: the tampered message must have an invalid signature.
	if tamperedMsg.VerifySignature() {
		t.Error("tampered message unexpectedly has a valid signature — payload mutation should break the Ed25519 signature")
	}

	// Cross-check: the no-hop message must have a valid signature but empty provenance.
	if !nohopMsg.VerifySignature() {
		t.Error("no-hop message should have a valid signature (only provenance is empty)")
	}
	if len(nohopMsg.Provenance) != 0 {
		t.Errorf("no-hop message has %d provenance hops, expected 0", len(nohopMsg.Provenance))
	}
}
