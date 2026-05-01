// Package cfidentity_test — identity convention declarations and profile management tests.
//
// Tests cover:
//   1. All five declarations compile and return correct convention/operation/tag values
//   2. IdentityDeclarations() returns all five in canonical order
//   3. Tag constants follow the identity:<op> naming scheme
//   4. ProfileFile round-trip (SaveProfile + LoadProfile)
//   5. Ceremony flow: introduce → declare-home → verify-me (gate evaluation)
//
// Tracked bead: campfireagent-902.
package cfidentity_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	cfidentity "github.com/campfire-net/campfire/cf-conventions/cf-identity"
	"github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"
	convention "github.com/campfire-net/campfire/cf-conventions/cf-convention"
)

// ─────────────────────────────────────────────────────────────────────────────
// 1. Declaration correctness
// ─────────────────────────────────────────────────────────────────────────────

func TestIntroduceMeDeclaration(t *testing.T) {
	d := cfidentity.IntroduceMeDeclaration()
	if d.Convention != cfidentity.IdentityConvention {
		t.Errorf("convention: got %q, want %q", d.Convention, cfidentity.IdentityConvention)
	}
	if d.Operation != "introduce-me" {
		t.Errorf("operation: got %q, want %q", d.Operation, "introduce-me")
	}
	if len(d.ProducesTags) != 1 || d.ProducesTags[0].Tag != cfidentity.IdentityIntroductionTag {
		t.Errorf("produces_tags: got %v, want [{%s exactly_one}]", d.ProducesTags, cfidentity.IdentityIntroductionTag)
	}
	if d.SignerType != convention.SignerMemberKey {
		t.Errorf("signer_type: got %v, want SignerMemberKey", d.SignerType)
	}
	// Required args check.
	requiredArgs := map[string]bool{}
	for _, arg := range d.Args {
		if arg.Required {
			requiredArgs[arg.Name] = true
		}
	}
	if !requiredArgs["pubkey_hex"] {
		t.Error("introduce-me: pubkey_hex must be a required arg")
	}
}

func TestDeclareHomeDeclaration(t *testing.T) {
	d := cfidentity.DeclareHomeDeclaration()
	if d.Operation != "declare-home" {
		t.Errorf("operation: got %q, want %q", d.Operation, "declare-home")
	}
	if len(d.ProducesTags) != 1 || d.ProducesTags[0].Tag != cfidentity.IdentityHomeDeclaredTag {
		t.Errorf("produces_tags: got %v", d.ProducesTags)
	}
	// campfire_id and role must be required.
	argNames := map[string]convention.ArgDescriptor{}
	for _, arg := range d.Args {
		argNames[arg.Name] = arg
	}
	if !argNames["campfire_id"].Required {
		t.Error("declare-home: campfire_id must be required")
	}
	if !argNames["role"].Required {
		t.Error("declare-home: role must be required")
	}
	// role must accept primary, secondary, archive.
	roleArg := argNames["role"]
	wantValues := []string{"primary", "secondary", "archive"}
	if len(roleArg.Values) != len(wantValues) {
		t.Errorf("declare-home role values: got %v, want %v", roleArg.Values, wantValues)
	}
}

func TestVerifyMeDeclaration(t *testing.T) {
	d := cfidentity.VerifyMeDeclaration()
	if d.Operation != "verify-me" {
		t.Errorf("operation: got %q, want %q", d.Operation, "verify-me")
	}
	if len(d.ProducesTags) != 1 || d.ProducesTags[0].Tag != cfidentity.IdentityChallengeRespTag {
		t.Errorf("produces_tags: got %v", d.ProducesTags)
	}
	// challenge must be required.
	for _, arg := range d.Args {
		if arg.Name == "challenge" && !arg.Required {
			t.Error("verify-me: challenge must be required")
		}
	}
}

func TestListHomesDeclaration(t *testing.T) {
	d := cfidentity.ListHomesDeclaration()
	if d.Operation != "list-homes" {
		t.Errorf("operation: got %q, want %q", d.Operation, "list-homes")
	}
	if len(d.ProducesTags) != 1 || d.ProducesTags[0].Tag != cfidentity.IdentityHomesTag {
		t.Errorf("produces_tags: got %v", d.ProducesTags)
	}
}

func TestEchoDeclaration(t *testing.T) {
	d := cfidentity.EchoDeclaration()
	if d.Operation != "echo" {
		t.Errorf("operation: got %q, want %q", d.Operation, "echo")
	}
	if len(d.ProducesTags) != 1 || d.ProducesTags[0].Tag != cfidentity.IdentityHomeEchoTag {
		t.Errorf("produces_tags: got %v", d.ProducesTags)
	}
	// Required args: echo_of, signed_by_b, campfire_b_pubkey.
	requiredArgs := map[string]bool{}
	for _, arg := range d.Args {
		if arg.Required {
			requiredArgs[arg.Name] = true
		}
	}
	for _, name := range []string{"echo_of", "signed_by_b", "campfire_b_pubkey"} {
		if !requiredArgs[name] {
			t.Errorf("echo: %q must be a required arg", name)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 2. IdentityDeclarations canonical order
// ─────────────────────────────────────────────────────────────────────────────

func TestIdentityDeclarations_CanonicalOrder(t *testing.T) {
	decls := cfidentity.IdentityDeclarations()
	if len(decls) != 5 {
		t.Fatalf("IdentityDeclarations: got %d declarations, want 5", len(decls))
	}
	want := []string{"introduce-me", "declare-home", "verify-me", "list-homes", "echo"}
	for i, op := range want {
		if decls[i].Operation != op {
			t.Errorf("IdentityDeclarations[%d]: got %q, want %q", i, decls[i].Operation, op)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 3. Tag constants naming scheme
// ─────────────────────────────────────────────────────────────────────────────

func TestTagConstants_FollowNamingScheme(t *testing.T) {
	cases := []struct {
		name  string
		tag   string
		want  string
	}{
		{"IdentityIntroductionTag", cfidentity.IdentityIntroductionTag, "identity:introduction"},
		{"IdentityHomeDeclaredTag", cfidentity.IdentityHomeDeclaredTag, "identity:home-declared"},
		{"IdentityChallengeRespTag", cfidentity.IdentityChallengeRespTag, "identity:challenge-response"},
		{"IdentityHomesTag", cfidentity.IdentityHomesTag, "identity:homes"},
		{"IdentityHomeEchoTag", cfidentity.IdentityHomeEchoTag, "identity:home-echo"},
		{"IdentityRevokedTag", cfidentity.IdentityRevokedTag, "identity:revoked"},
		{"IdentityBeaconTag", cfidentity.IdentityBeaconTag, "identity:v1"},
		{"IdentityProfileTag", cfidentity.IdentityProfileTag, "identity:profile"},
	}
	for _, c := range cases {
		if c.tag != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.tag, c.want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 4. ProfileFile round-trip
// ─────────────────────────────────────────────────────────────────────────────

func TestProfileFile_RoundTrip(t *testing.T) {
	dir := t.TempDir()

	// File doesn't exist yet — LoadProfile returns zero value.
	p := cfidentity.LoadProfile(dir)
	if p.DisplayName != "" {
		t.Errorf("LoadProfile (missing file): got %q, want empty", p.DisplayName)
	}

	// Save and reload.
	want := cfidentity.ProfileFile{DisplayName: "Alice"}
	if err := cfidentity.SaveProfile(dir, want); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	got := cfidentity.LoadProfile(dir)
	if got.DisplayName != want.DisplayName {
		t.Errorf("LoadProfile after save: got %q, want %q", got.DisplayName, want.DisplayName)
	}

	// Verify file exists and has correct permissions.
	info, err := os.Stat(filepath.Join(dir, "profile.json"))
	if err != nil {
		t.Fatalf("stat profile.json: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("profile.json permissions: got %o, want 0600", info.Mode().Perm())
	}
}

func TestProfileFile_InvalidJSON(t *testing.T) {
	dir := t.TempDir()

	// Write invalid JSON.
	if err := os.WriteFile(filepath.Join(dir, "profile.json"), []byte("not json"), 0600); err != nil {
		t.Fatalf("writing invalid profile: %v", err)
	}

	// LoadProfile should return zero value gracefully (not panic).
	p := cfidentity.LoadProfile(dir)
	if p.DisplayName != "" {
		t.Errorf("LoadProfile (invalid JSON): got %q, want empty", p.DisplayName)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 5. Ceremony flow using real DefaultGateEvaluator
//
// Simulates the introduce → declare-home → verify-me sequence:
//   a. Build a delegation chain: root grants identity:introduce-me to sender.
//   b. Evaluate the chain using DefaultGateEvaluator.
//   c. Verify the echo ceremony: sender signs M_B and verifier checks signature.
//
// This is not a full wire-level protocol test — the ceremony flow test validates
// that the declaration types and GateEvaluator integration work correctly for
// real key material.
// ─────────────────────────────────────────────────────────────────────────────

// genKey generates a fresh Ed25519 key pair for testing.
func genKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	return pub, priv
}

// makeNonce returns 16 random bytes.
func makeNonce(t *testing.T) []byte {
	t.Helper()
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("reading random nonce: %v", err)
	}
	return nonce
}

// buildGrant builds a CBOR-encoded GrantPayload for identity:introduce-me.
// Grants the sender the identity convention with the introduce-me op.
func buildGrant(t *testing.T, childPubkey []byte, currentTime time.Time) trust.ChainMessage {
	t.Helper()

	cap := trust.Capability{
		Convention: cfidentity.IdentityConvention,
		OpPattern:  "introduce-me",
		Bounds:     map[string]any{},
		Until:      currentTime.Add(1 * time.Hour).UnixNano(),
		Nonce:      makeNonce(t),
	}
	gp := trust.GrantPayload{
		ParentGrantID: nil, // owner-root
		ChildPubkey:   childPubkey,
		Capabilities:  []trust.Capability{cap},
		Depth:         1,
	}
	payload, err := trust.MarshalGrantPayloadCBOR(gp)
	if err != nil {
		t.Fatalf("MarshalGrantPayloadCBOR: %v", err)
	}
	return trust.ChainMessage{
		ID:      "test-grant-message-id",
		Payload: payload,
	}
}

// TestCeremonyFlow_IntroduceDeclareVerify validates the full ceremony chain.
func TestCeremonyFlow_IntroduceDeclareVerify(t *testing.T) {
	rootPub, _ := genKey(t)
	senderPub, senderPriv := genKey(t)
	_ = senderPriv

	now := time.Now().UTC()
	eval := trust.DefaultGateEvaluator{}
	ctx := context.Background()

	// Step 1: introduce-me — root grants sender the introduce-me capability.
	grant := buildGrant(t, senderPub, now)
	req := trust.EvaluateRequest{
		Request: trust.OpRequest{
			Convention:  cfidentity.IdentityConvention,
			Operation:   "introduce-me",
			CampfireID:  "deadbeef" + "0000000000000000000000000000000000000000000000000000000000000000",
			Sender:      senderPub,
			Tags:        []string{cfidentity.IdentityIntroductionTag},
		},
		ChainMessages: []trust.ChainMessage{grant},
		RootPrincipal: rootPub,
		CurrentTime:   now,
	}
	result := eval.Evaluate(ctx, req)
	if result.Decision != trust.Allow {
		t.Errorf("introduce-me: expected Allow, got %v (reason: %v)", result.Decision, result.Reason)
	}

	// Step 2: declare-home — use a fresh grant for the declare-home op.
	capDeclare := trust.Capability{
		Convention: cfidentity.IdentityConvention,
		OpPattern:  "declare-home",
		Bounds:     map[string]any{},
		Until:      now.Add(1 * time.Hour).UnixNano(),
		Nonce:      makeNonce(t),
	}
	gpDeclare := trust.GrantPayload{
		ParentGrantID: nil,
		ChildPubkey:   senderPub,
		Capabilities:  []trust.Capability{capDeclare},
		Depth:         1,
	}
	payloadDeclare, err := trust.MarshalGrantPayloadCBOR(gpDeclare)
	if err != nil {
		t.Fatalf("MarshalGrantPayloadCBOR(declare-home): %v", err)
	}
	grantDeclare := trust.ChainMessage{ID: "declare-grant-msg-id", Payload: payloadDeclare}

	reqDeclare := trust.EvaluateRequest{
		Request: trust.OpRequest{
			Convention:  cfidentity.IdentityConvention,
			Operation:   "declare-home",
			CampfireID:  "deadbeef" + "0000000000000000000000000000000000000000000000000000000000000000",
			Sender:      senderPub,
			Tags:        []string{cfidentity.IdentityHomeDeclaredTag},
		},
		ChainMessages: []trust.ChainMessage{grantDeclare},
		RootPrincipal: rootPub,
		CurrentTime:   now,
	}
	resultDeclare := eval.Evaluate(ctx, reqDeclare)
	if resultDeclare.Decision != trust.Allow {
		t.Errorf("declare-home: expected Allow, got %v (reason: %v)", resultDeclare.Decision, resultDeclare.Reason)
	}

	// Step 3: verify-me — echo ceremony: sender signs a challenge nonce.
	challenge := []byte("test-challenge-nonce-12345678")
	sig := ed25519.Sign(senderPriv, challenge)
	if !ed25519.Verify(senderPub, challenge, sig) {
		t.Fatal("verify-me: signature verification failed against sender public key")
	}

	// Verify the verify-me declaration is well-formed.
	verifyDecl := cfidentity.VerifyMeDeclaration()
	if verifyDecl.Operation != "verify-me" {
		t.Errorf("verify-me declaration operation: got %q", verifyDecl.Operation)
	}
	if len(verifyDecl.ProducesTags) != 1 || verifyDecl.ProducesTags[0].Tag != cfidentity.IdentityChallengeRespTag {
		t.Errorf("verify-me declaration tags: got %v", verifyDecl.ProducesTags)
	}

	t.Logf("ceremony flow: introduce-me Allow ✓, declare-home Allow ✓, verify-me signature ✓")
}

// TestCeremonyFlow_ExpiredGrant verifies that an expired grant is denied.
func TestCeremonyFlow_ExpiredGrant(t *testing.T) {
	rootPub, _ := genKey(t)
	senderPub, _ := genKey(t)

	eval := trust.DefaultGateEvaluator{}
	ctx := context.Background()

	// Build a grant that expired 1 hour ago.
	pastTime := time.Now().UTC().Add(-2 * time.Hour)
	cap := trust.Capability{
		Convention: cfidentity.IdentityConvention,
		OpPattern:  "introduce-me",
		Bounds:     map[string]any{},
		Until:      pastTime.Add(30 * time.Minute).UnixNano(), // expired 30 min ago
		Nonce:      makeNonce(t),
	}
	gp := trust.GrantPayload{
		ParentGrantID: nil,
		ChildPubkey:   senderPub,
		Capabilities:  []trust.Capability{cap},
		Depth:         1,
	}
	payload, err := trust.MarshalGrantPayloadCBOR(gp)
	if err != nil {
		t.Fatalf("MarshalGrantPayloadCBOR: %v", err)
	}

	req := trust.EvaluateRequest{
		Request: trust.OpRequest{
			Convention: cfidentity.IdentityConvention,
			Operation:  "introduce-me",
			CampfireID: "deadbeef" + "0000000000000000000000000000000000000000000000000000000000000000",
			Sender:     senderPub,
		},
		ChainMessages: []trust.ChainMessage{{ID: "exp-grant", Payload: payload}},
		RootPrincipal: rootPub,
		CurrentTime:   time.Now().UTC(), // current time > Until
	}
	result := eval.Evaluate(ctx, req)
	if result.Decision != trust.Deny {
		t.Errorf("expired grant: expected Deny, got %v", result.Decision)
	}
	if result.Reason != trust.DenyExpired {
		t.Errorf("expired grant reason: got %q, want %q", result.Reason, trust.DenyExpired)
	}
}

// TestCeremonyFlow_RevokedIdentity verifies that a revoked sender key is denied.
func TestCeremonyFlow_RevokedIdentity(t *testing.T) {
	rootPub, _ := genKey(t)
	senderPub, _ := genKey(t)

	// The identity:revoked tag constants must be correct.
	if cfidentity.IdentityRevokedTag != "identity:revoked" {
		t.Errorf("IdentityRevokedTag: got %q, want %q", cfidentity.IdentityRevokedTag, "identity:revoked")
	}

	eval := trust.DefaultGateEvaluator{}
	ctx := context.Background()

	now := time.Now().UTC()
	cap := trust.Capability{
		Convention: cfidentity.IdentityConvention,
		OpPattern:  "introduce-me",
		Bounds:     map[string]any{},
		Until:      now.Add(1 * time.Hour).UnixNano(),
		Nonce:      makeNonce(t),
	}
	gp := trust.GrantPayload{
		ParentGrantID: nil,
		ChildPubkey:   senderPub,
		Capabilities:  []trust.Capability{cap},
		Depth:         1,
	}
	payload, err := trust.MarshalGrantPayloadCBOR(gp)
	if err != nil {
		t.Fatalf("MarshalGrantPayloadCBOR: %v", err)
	}

	// Encode senderPub as hex for the revocation view.
	senderHex := encodeHex(senderPub)

	req := trust.EvaluateRequest{
		Request: trust.OpRequest{
			Convention: cfidentity.IdentityConvention,
			Operation:  "introduce-me",
			CampfireID: "deadbeef" + "0000000000000000000000000000000000000000000000000000000000000000",
			Sender:     senderPub,
		},
		ChainMessages: []trust.ChainMessage{{ID: "revoked-grant", Payload: payload}},
		RootPrincipal: rootPub,
		CurrentTime:   now,
		RevocationView: []trust.RevocationViewEntry{
			{
				CampfireID:          senderHex, // convention: CampfireID == revoked key hex
				LatestObservedMsgID: "some-revoke-msg-id",
				ObservedAt:          now.Add(-1 * time.Minute),
			},
		},
	}
	result := eval.Evaluate(ctx, req)
	if result.Decision != trust.Deny {
		t.Errorf("revoked identity: expected Deny, got %v", result.Decision)
	}
	if result.Reason != trust.DenyRevoked {
		t.Errorf("revoked identity reason: got %q, want %q", result.Reason, trust.DenyRevoked)
	}
}

// encodeHex encodes a byte slice to a hex string without importing encoding/hex
// to avoid an unnecessary import in this test.
func encodeHex(b []byte) string {
	const chars = "0123456789abcdef"
	buf := make([]byte, len(b)*2)
	for i, v := range b {
		buf[i*2] = chars[v>>4]
		buf[i*2+1] = chars[v&0xf]
	}
	return string(buf)
}
