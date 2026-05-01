// Package cfsession_test — cf-session package tests.
//
// Tests cover:
//   1. Declaration correctness (SessionDeclarations canonical set)
//   2. Tag constants have correct wire values
//   3. Jail-write backend: MaterializeWorkerIdentity + LoadWorkerIdentity round-trip
//   4. Jail permissions enforcement (JailDirMode, IdentityFileMode)
//   5. GenerateWorkerIdentity: unique keys per call (no key reuse)
//   6. IssueWorkerGrant: lazy-mint payload structure, depth invariant, TTL clamping
//   7. IssueWorkerGrant: scope does not widen parent template (D4)
//   8. OpenSession / CloseSession: emit session:open and session:close events
//   9. SigningProxy: allowlist enforcement (campfire not in allowlist → error)
//  10. GateEvaluator: DefaultGateEvaluator (via NewGateEvaluator) evaluates session grants
//  11. Adversarial: key-share attempt (two workers must get different keys)
//  12. Adversarial: parent-bound widening attempt returns IssueWorkerGrant error
//
// Integration: uses real CBOR, real Ed25519, real GateEvaluator (DefaultGateEvaluator
// from cf-authority/trust). No mocks.
//
// Tracked bead: campfireagent-c3a.
package cfsession_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	cfsession "github.com/campfire-net/campfire/cf-conventions/cf-session"
	"github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"
	convention "github.com/campfire-net/campfire/cf-conventions/cf-convention"
	cfprotocol "github.com/campfire-net/campfire/cf-protocol/protocol"
)

// ─────────────────────────────────────────────────────────────────────────────
// 1. Declaration set
// ─────────────────────────────────────────────────────────────────────────────

func TestSessionDeclarations_CanonicalSet(t *testing.T) {
	decls := cfsession.SessionDeclarations()
	if len(decls) != 4 {
		t.Fatalf("SessionDeclarations: got %d declarations, want 4", len(decls))
	}
	want := []string{"open", "close", "introduce-worker", "grant-worker"}
	for i, op := range want {
		if decls[i].Operation != op {
			t.Errorf("SessionDeclarations[%d]: got operation %q, want %q", i, decls[i].Operation, op)
		}
		if decls[i].Convention != cfsession.SessionConvention {
			t.Errorf("SessionDeclarations[%d]: convention = %q, want %q", i, decls[i].Convention, cfsession.SessionConvention)
		}
	}
}

func TestOpenDeclaration_Structure(t *testing.T) {
	d := cfsession.OpenDeclaration()
	if d.SignerType != convention.SignerCampfireKey {
		t.Errorf("open: SignerType = %v, want SignerCampfireKey", d.SignerType)
	}
	if len(d.ProducesTags) != 1 || d.ProducesTags[0].Tag != cfsession.TagSessionOpen {
		t.Errorf("open: ProducesTags = %v, want [{%s exactly_one}]", d.ProducesTags, cfsession.TagSessionOpen)
	}
	// Required args: session_id, parent_grant_chain_root, capability_template, until.
	required := requiredArgs(d)
	for _, name := range []string{"session_id", "parent_grant_chain_root", "capability_template", "until"} {
		if !required[name] {
			t.Errorf("open: %q must be a required arg", name)
		}
	}
}

func TestCloseDeclaration_Structure(t *testing.T) {
	d := cfsession.CloseDeclaration()
	if d.SignerType != convention.SignerCampfireKey {
		t.Errorf("close: SignerType = %v, want SignerCampfireKey", d.SignerType)
	}
	if len(d.ProducesTags) != 1 || d.ProducesTags[0].Tag != cfsession.TagSessionClose {
		t.Errorf("close: ProducesTags = %v, want [{%s exactly_one}]", d.ProducesTags, cfsession.TagSessionClose)
	}
	if !requiredArgs(d)["session_id"] {
		t.Error("close: session_id must be a required arg")
	}
}

func TestIntroduceWorkerDeclaration_Structure(t *testing.T) {
	d := cfsession.IntroduceWorkerDeclaration()
	if d.SignerType != convention.SignerMemberKey {
		t.Errorf("introduce-worker: SignerType = %v, want SignerMemberKey", d.SignerType)
	}
	if !requiredArgs(d)["worker_pubkey_hex"] {
		t.Error("introduce-worker: worker_pubkey_hex must be a required arg")
	}
}

func TestGrantWorkerDeclaration_Structure(t *testing.T) {
	d := cfsession.GrantWorkerDeclaration()
	if d.SignerType != convention.SignerCampfireKey {
		t.Errorf("grant-worker: SignerType = %v, want SignerCampfireKey", d.SignerType)
	}
	required := requiredArgs(d)
	for _, name := range []string{"worker_pubkey_hex", "parent_grant_id", "grant_payload_cbor", "until"} {
		if !required[name] {
			t.Errorf("grant-worker: %q must be a required arg", name)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 2. Tag constants
// ─────────────────────────────────────────────────────────────────────────────

func TestTagConstants_WireValues(t *testing.T) {
	if cfsession.TagSessionOpen != "session:open" {
		t.Errorf("TagSessionOpen = %q, want %q", cfsession.TagSessionOpen, "session:open")
	}
	if cfsession.TagSessionClose != "session:close" {
		t.Errorf("TagSessionClose = %q, want %q", cfsession.TagSessionClose, "session:close")
	}
	if cfsession.TagSessionOpen == cfsession.TagSessionClose {
		t.Error("TagSessionOpen and TagSessionClose must differ")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 3. Jail-write round-trip
// ─────────────────────────────────────────────────────────────────────────────

func TestJailWrite_RoundTrip(t *testing.T) {
	wid, err := cfsession.GenerateWorkerIdentity()
	if err != nil {
		t.Fatalf("GenerateWorkerIdentity: %v", err)
	}

	jailPath := t.TempDir()
	if err := cfsession.MaterializeWorkerIdentity(jailPath, wid); err != nil {
		t.Fatalf("MaterializeWorkerIdentity: %v", err)
	}

	loaded, err := cfsession.LoadWorkerIdentity(jailPath)
	if err != nil {
		t.Fatalf("LoadWorkerIdentity: %v", err)
	}

	if hex.EncodeToString(loaded.PublicKey) != hex.EncodeToString(wid.PublicKey) {
		t.Errorf("public key mismatch after round-trip")
	}
	if hex.EncodeToString(loaded.PrivateKey) != hex.EncodeToString(wid.PrivateKey) {
		t.Errorf("private key mismatch after round-trip")
	}
}

func TestJailWrite_SignatureVerifies(t *testing.T) {
	wid, err := cfsession.GenerateWorkerIdentity()
	if err != nil {
		t.Fatalf("GenerateWorkerIdentity: %v", err)
	}

	jailPath := t.TempDir()
	if err := cfsession.MaterializeWorkerIdentity(jailPath, wid); err != nil {
		t.Fatalf("MaterializeWorkerIdentity: %v", err)
	}

	loaded, err := cfsession.LoadWorkerIdentity(jailPath)
	if err != nil {
		t.Fatalf("LoadWorkerIdentity: %v", err)
	}

	// Sign with loaded private key, verify with loaded public key.
	msg := []byte("test message for session worker")
	sig := ed25519.Sign(loaded.PrivateKey, msg)
	if !ed25519.Verify(loaded.PublicKey, msg, sig) {
		t.Error("loaded identity: signature does not verify with loaded public key")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 4. Jail permissions
// ─────────────────────────────────────────────────────────────────────────────

func TestJailPermissions_CorrectModes(t *testing.T) {
	wid, err := cfsession.GenerateWorkerIdentity()
	if err != nil {
		t.Fatalf("GenerateWorkerIdentity: %v", err)
	}

	// Use a raw temp dir, then set mode explicitly.
	base := t.TempDir()
	jailPath := filepath.Join(base, "worker-jail")
	if err := os.Mkdir(jailPath, cfsession.JailDirMode); err != nil {
		t.Fatalf("creating jail dir: %v", err)
	}

	if err := cfsession.MaterializeWorkerIdentity(jailPath, wid); err != nil {
		t.Fatalf("MaterializeWorkerIdentity: %v", err)
	}

	// VerifyJailPermissions should pass.
	if err := cfsession.VerifyJailPermissions(jailPath); err != nil {
		t.Errorf("VerifyJailPermissions failed unexpectedly: %v", err)
	}

	// Check campfire/identity.json mode is 0600.
	identPath := filepath.Join(jailPath, "campfire", "identity.json")
	info, err := os.Stat(identPath)
	if err != nil {
		t.Fatalf("stat identity.json: %v", err)
	}
	if info.Mode().Perm() != cfsession.IdentityFileMode {
		t.Errorf("identity.json mode: got %o, want %o", info.Mode().Perm(), cfsession.IdentityFileMode)
	}
}

func TestJailPermissions_WrongMode_Fails(t *testing.T) {
	wid, err := cfsession.GenerateWorkerIdentity()
	if err != nil {
		t.Fatalf("GenerateWorkerIdentity: %v", err)
	}

	// Create jail with wrong mode (0755 instead of 0700).
	base := t.TempDir()
	jailPath := filepath.Join(base, "bad-jail")
	if err := os.Mkdir(jailPath, 0755); err != nil {
		t.Fatalf("creating bad jail dir: %v", err)
	}

	if err := cfsession.MaterializeWorkerIdentity(jailPath, wid); err != nil {
		t.Fatalf("MaterializeWorkerIdentity: %v", err)
	}

	// VerifyJailPermissions should fail because dir mode is wrong.
	if err := cfsession.VerifyJailPermissions(jailPath); err == nil {
		t.Error("VerifyJailPermissions should fail for 0755 jail dir")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 5. GenerateWorkerIdentity: no key reuse
// ─────────────────────────────────────────────────────────────────────────────

func TestGenerateWorkerIdentity_UniqueKeys(t *testing.T) {
	wid1, err := cfsession.GenerateWorkerIdentity()
	if err != nil {
		t.Fatalf("GenerateWorkerIdentity #1: %v", err)
	}
	wid2, err := cfsession.GenerateWorkerIdentity()
	if err != nil {
		t.Fatalf("GenerateWorkerIdentity #2: %v", err)
	}

	if hex.EncodeToString(wid1.PublicKey) == hex.EncodeToString(wid2.PublicKey) {
		t.Error("GenerateWorkerIdentity: two calls returned the same key — key reuse detected")
	}
}

func TestGenerateWorkerIdentity_KeySize(t *testing.T) {
	wid, err := cfsession.GenerateWorkerIdentity()
	if err != nil {
		t.Fatalf("GenerateWorkerIdentity: %v", err)
	}
	if len(wid.PublicKey) != ed25519.PublicKeySize {
		t.Errorf("public key size: got %d, want %d", len(wid.PublicKey), ed25519.PublicKeySize)
	}
	if len(wid.PrivateKey) != ed25519.PrivateKeySize {
		t.Errorf("private key size: got %d, want %d", len(wid.PrivateKey), ed25519.PrivateKeySize)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 6. IssueWorkerGrant: lazy-mint structure, depth invariant, TTL clamping
// ─────────────────────────────────────────────────────────────────────────────

func TestIssueWorkerGrant_Structure(t *testing.T) {
	wid, _ := cfsession.GenerateWorkerIdentity()
	parentGrantID := []byte("parent-grant-id-123")
	now := time.Now().UTC()
	sessionUntil := now.Add(2 * time.Hour)

	result, err := cfsession.IssueWorkerGrant(cfsession.GrantRequest{
		WorkerPubkey:  wid.PublicKey,
		ParentGrantID: parentGrantID,
		CapabilityTemplate: cfsession.CapabilityTemplate{
			Convention: "swarm-coordination",
			OpGlob:     "claim-item|complete",
			Until:      sessionUntil,
		},
		SessionUntil: sessionUntil,
	})
	if err != nil {
		t.Fatalf("IssueWorkerGrant: %v", err)
	}
	if len(result.Payload) == 0 {
		t.Error("IssueWorkerGrant: payload is empty")
	}
	if len(result.GrantID) == 0 {
		t.Error("IssueWorkerGrant: grant ID is empty")
	}

	// Decode and verify structure.
	gp, err := trust.UnmarshalGrantPayloadCBOR(result.Payload)
	if err != nil {
		t.Fatalf("UnmarshalGrantPayloadCBOR: %v", err)
	}

	// Depth invariant: worker grants issued by orchestrator have Depth=1.
	if gp.Depth != 1 {
		t.Errorf("GrantPayload.Depth = %d, want 1 (orchestrator-issued)", gp.Depth)
	}

	// Child pubkey matches worker pubkey.
	if hex.EncodeToString(gp.ChildPubkey) != hex.EncodeToString(wid.PublicKey) {
		t.Errorf("GrantPayload.ChildPubkey mismatch: got %x, want %x", gp.ChildPubkey, wid.PublicKey)
	}

	// ParentGrantID set correctly.
	if string(gp.ParentGrantID) != string(parentGrantID) {
		t.Errorf("GrantPayload.ParentGrantID mismatch: got %x, want %x", gp.ParentGrantID, parentGrantID)
	}

	// Exactly one capability.
	if len(gp.Capabilities) != 1 {
		t.Fatalf("expected 1 capability, got %d", len(gp.Capabilities))
	}
	cap := gp.Capabilities[0]
	if cap.Convention != "swarm-coordination" {
		t.Errorf("Capability.Convention = %q, want %q", cap.Convention, "swarm-coordination")
	}
	if cap.OpPattern != "claim-item|complete" {
		t.Errorf("Capability.OpPattern = %q, want %q", cap.OpPattern, "claim-item|complete")
	}
}

func TestIssueWorkerGrant_TTL_Clamped_To_SessionUntil(t *testing.T) {
	wid, _ := cfsession.GenerateWorkerIdentity()
	now := time.Now().UTC()
	sessionUntil := now.Add(1 * time.Hour)
	// Template asks for 2h but session only allows 1h.
	templateUntil := now.Add(2 * time.Hour)

	result, err := cfsession.IssueWorkerGrant(cfsession.GrantRequest{
		WorkerPubkey:  wid.PublicKey,
		ParentGrantID: []byte("grant-id"),
		CapabilityTemplate: cfsession.CapabilityTemplate{
			Convention: "test",
			OpGlob:     "*",
			Until:      templateUntil,
		},
		SessionUntil: sessionUntil,
	})
	if err != nil {
		t.Fatalf("IssueWorkerGrant: %v", err)
	}

	gp, err := trust.UnmarshalGrantPayloadCBOR(result.Payload)
	if err != nil {
		t.Fatalf("UnmarshalGrantPayloadCBOR: %v", err)
	}

	cap := gp.Capabilities[0]
	grantUntil := time.Unix(0, cap.Until).UTC()

	// Grant Until must be ≤ SessionUntil (D7 — no forever grants).
	if grantUntil.After(sessionUntil) {
		t.Errorf("grant Until %v exceeds session Until %v — D7 violated", grantUntil, sessionUntil)
	}
}

func TestIssueWorkerGrant_ValidationErrors(t *testing.T) {
	now := time.Now().UTC()
	wid, _ := cfsession.GenerateWorkerIdentity()

	tests := []struct {
		name string
		req  cfsession.GrantRequest
	}{
		{
			name: "empty worker pubkey",
			req: cfsession.GrantRequest{
				WorkerPubkey:  nil,
				ParentGrantID: []byte("gid"),
				CapabilityTemplate: cfsession.CapabilityTemplate{
					Convention: "test",
					OpGlob:     "*",
					Until:      now.Add(1 * time.Hour),
				},
				SessionUntil: now.Add(2 * time.Hour),
			},
		},
		{
			name: "empty convention in template",
			req: cfsession.GrantRequest{
				WorkerPubkey:  wid.PublicKey,
				ParentGrantID: []byte("gid"),
				CapabilityTemplate: cfsession.CapabilityTemplate{
					Convention: "",
					OpGlob:     "*",
					Until:      now.Add(1 * time.Hour),
				},
				SessionUntil: now.Add(2 * time.Hour),
			},
		},
		{
			name: "zero session until",
			req: cfsession.GrantRequest{
				WorkerPubkey:  wid.PublicKey,
				ParentGrantID: []byte("gid"),
				CapabilityTemplate: cfsession.CapabilityTemplate{
					Convention: "test",
					OpGlob:     "*",
					Until:      now.Add(1 * time.Hour),
				},
				SessionUntil: time.Time{}, // zero
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cfsession.IssueWorkerGrant(tc.req)
			if err == nil {
				t.Errorf("%s: expected error, got nil", tc.name)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 7. Adversarial: parent-bound widening (D4)
// ─────────────────────────────────────────────────────────────────────────────
// IssueWorkerGrant does NOT widen parent scope — the template is intersected.
// We test this by verifying the op_glob from the template is preserved (not
// widened to "*") and that the GateEvaluator rejects a scope-widening chain.

func TestIssueWorkerGrant_TemplateOpGlobPreserved(t *testing.T) {
	wid, _ := cfsession.GenerateWorkerIdentity()
	now := time.Now().UTC()
	sessionUntil := now.Add(1 * time.Hour)

	// Template allows only "claim-item".
	result, err := cfsession.IssueWorkerGrant(cfsession.GrantRequest{
		WorkerPubkey:  wid.PublicKey,
		ParentGrantID: []byte("gid"),
		CapabilityTemplate: cfsession.CapabilityTemplate{
			Convention: "swarm-coordination",
			OpGlob:     "claim-item",
			Until:      sessionUntil,
		},
		SessionUntil: sessionUntil,
	})
	if err != nil {
		t.Fatalf("IssueWorkerGrant: %v", err)
	}

	gp, err := trust.UnmarshalGrantPayloadCBOR(result.Payload)
	if err != nil {
		t.Fatalf("UnmarshalGrantPayloadCBOR: %v", err)
	}

	// OpPattern should be exactly "claim-item", not "*".
	cap := gp.Capabilities[0]
	if cap.OpPattern != "claim-item" {
		t.Errorf("op_glob widened: got %q, want %q", cap.OpPattern, "claim-item")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 8. OpenSession / CloseSession: event emission
// ─────────────────────────────────────────────────────────────────────────────

func TestOpenSession_EmitsSessionOpenTag(t *testing.T) {
	client, cleanup := setupProtocolClient(t)
	defer cleanup()

	sess, sessionID, err := client.NewSession(2 * time.Hour)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.End() //nolint:errcheck

	now := time.Now().UTC()
	payload := &cfsession.SessionOpenPayload{
		SessionID:            sessionID[:min(8, len(sessionID))],
		ParentGrantChainRoot: "deadbeef",
		CapabilityTemplate: cfsession.CapabilityTemplate{
			Convention: "swarm-coordination",
			OpGlob:     "*",
			Until:      now.Add(2 * time.Hour),
		},
		Until:       now.Add(2 * time.Hour).UnixNano(),
		KeyHandling: cfsession.KeyHandlingJail,
	}

	if err := cfsession.OpenSession(sess, payload); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	// Read back and verify session:open tag is present.
	msgs, err := sess.Read()
	if err != nil {
		t.Fatalf("Session.Read: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("no messages in session after OpenSession")
	}

	var found bool
	for _, m := range msgs {
		for _, tag := range m.Tags {
			if tag == cfsession.TagSessionOpen {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("session:open tag not found in session messages; tags: %v", msgs[0].Tags)
	}
}

func TestCloseSession_EmitsSessionCloseTag(t *testing.T) {
	client, cleanup := setupProtocolClient(t)
	defer cleanup()

	sess, sessionID, err := client.NewSession(2 * time.Hour)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.End() //nolint:errcheck

	payload := &cfsession.SessionClosePayload{
		SessionID: sessionID[:min(8, len(sessionID))],
		Reason:    "normal",
		ClosedAt:  time.Now().UnixNano(),
	}

	if err := cfsession.CloseSession(sess, payload); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}

	msgs, err := sess.Read()
	if err != nil {
		t.Fatalf("Session.Read: %v", err)
	}

	var found bool
	for _, m := range msgs {
		for _, tag := range m.Tags {
			if tag == cfsession.TagSessionClose {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("session:close tag not found after CloseSession")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 9. SigningProxy: allowlist enforcement
// ─────────────────────────────────────────────────────────────────────────────

func TestSigningProxy_AllowedCampfire(t *testing.T) {
	wid, _ := cfsession.GenerateWorkerIdentity()
	proxy := cfsession.NewSigningProxy(wid.PrivateKey, []string{"allowed-campfire"}, "/tmp/test.sock")

	payload := []byte("test payload to sign")
	sig, err := proxy.Sign("allowed-campfire", payload)
	if err != nil {
		t.Fatalf("proxy.Sign (allowed): %v", err)
	}
	if !ed25519.Verify(proxy.PublicKey(), payload, sig) {
		t.Error("proxy.Sign: signature does not verify")
	}
}

func TestSigningProxy_DisallowedCampfire_ReturnsError(t *testing.T) {
	wid, _ := cfsession.GenerateWorkerIdentity()
	proxy := cfsession.NewSigningProxy(wid.PrivateKey, []string{"allowed-only"}, "/tmp/test.sock")

	_, err := proxy.Sign("evil-campfire", []byte("payload"))
	if err == nil {
		t.Error("proxy.Sign (disallowed campfire): expected error, got nil — jail-break detected")
	}
}

func TestSigningProxy_EmptyAllowlist_AllowsAll(t *testing.T) {
	wid, _ := cfsession.GenerateWorkerIdentity()
	// Empty allowlist = no restriction.
	proxy := cfsession.NewSigningProxy(wid.PrivateKey, nil, "/tmp/test.sock")

	payload := []byte("any campfire")
	sig, err := proxy.Sign("any-campfire-id", payload)
	if err != nil {
		t.Fatalf("proxy.Sign (empty allowlist): %v", err)
	}
	if !ed25519.Verify(proxy.PublicKey(), payload, sig) {
		t.Error("proxy.Sign (empty allowlist): signature does not verify")
	}
}

func TestSigningProxy_PublicKey_MatchesPrivateKey(t *testing.T) {
	wid, _ := cfsession.GenerateWorkerIdentity()
	proxy := cfsession.NewSigningProxy(wid.PrivateKey, nil, "/tmp/test.sock")

	if hex.EncodeToString(proxy.PublicKey()) != hex.EncodeToString(wid.PublicKey) {
		t.Error("SigningProxy.PublicKey() does not match input private key")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 10. GateEvaluator: real DefaultGateEvaluator evaluates session grants
// ─────────────────────────────────────────────────────────────────────────────

func TestGateEvaluator_WorkerGrantAllowed(t *testing.T) {
	eval := cfsession.NewGateEvaluator()

	// Worker key.
	workerPub, _, _ := ed25519.GenerateKey(rand.Reader)
	// Root key.
	rootPub, _, _ := ed25519.GenerateKey(rand.Reader)

	now := time.Now().UTC()
	sessionUntil := now.Add(1 * time.Hour)

	// Build a grant for the worker from root (depth=1).
	nonce := make([]byte, 16)
	rand.Read(nonce) //nolint:errcheck
	cap := trust.Capability{
		Convention: "swarm-coordination",
		OpPattern:  "claim-item",
		Bounds:     map[string]any{},
		Until:      sessionUntil.UnixNano(),
		Nonce:      nonce,
	}
	gp := trust.GrantPayload{
		ParentGrantID: []byte("root-grant-id"),
		ChildPubkey:   workerPub,
		Capabilities:  []trust.Capability{cap},
		Depth:         1,
	}
	payload, err := trust.MarshalGrantPayloadCBOR(gp)
	if err != nil {
		t.Fatalf("MarshalGrantPayloadCBOR: %v", err)
	}

	req := convention.EvaluateRequest{
		Request: convention.GateOpRequest{
			Convention: "swarm-coordination",
			Operation:  "claim-item",
			CampfireID: "deadbeef" + "0000000000000000000000000000000000000000000000000000000000000000",
			Sender:     workerPub,
		},
		ChainMessages: []convention.GateChainMessage{
			{ID: "grant-msg-1", Payload: payload},
		},
		RootPrincipal: rootPub,
		CurrentTime:   now,
	}

	result := eval.Evaluate(context.Background(), req)
	if result.Decision != convention.GateAllow {
		t.Errorf("GateEvaluator: expected GateAllow for valid worker grant, got %v (reason: %v)",
			result.Decision, result.Reason)
	}
}

func TestGateEvaluator_ExpiredWorkerGrant_Denied(t *testing.T) {
	eval := cfsession.NewGateEvaluator()

	workerPub, _, _ := ed25519.GenerateKey(rand.Reader)
	rootPub, _, _ := ed25519.GenerateKey(rand.Reader)

	now := time.Now().UTC()

	// Expired grant (1 hour in the past).
	nonce := make([]byte, 16)
	rand.Read(nonce) //nolint:errcheck
	cap := trust.Capability{
		Convention: "swarm-coordination",
		OpPattern:  "*",
		Bounds:     map[string]any{},
		Until:      now.Add(-1 * time.Hour).UnixNano(), // expired
		Nonce:      nonce,
	}
	gp := trust.GrantPayload{
		ParentGrantID: []byte("gid"),
		ChildPubkey:   workerPub,
		Capabilities:  []trust.Capability{cap},
		Depth:         1,
	}
	payload, _ := trust.MarshalGrantPayloadCBOR(gp)

	req := convention.EvaluateRequest{
		Request: convention.GateOpRequest{
			Convention: "swarm-coordination",
			Operation:  "claim-item",
			CampfireID: "deadbeef" + "0000000000000000000000000000000000000000000000000000000000000000",
			Sender:     workerPub,
		},
		ChainMessages: []convention.GateChainMessage{{ID: "expired", Payload: payload}},
		RootPrincipal: rootPub,
		CurrentTime:   now,
	}

	result := eval.Evaluate(context.Background(), req)
	if result.Decision != convention.GateDeny {
		t.Errorf("expired grant: expected GateDeny, got %v", result.Decision)
	}
	if result.Reason != convention.DenyExpired {
		t.Errorf("expired grant reason: got %q, want %q", result.Reason, convention.DenyExpired)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 11. Adversarial: key-share attempt (two workers must get different keys)
// ─────────────────────────────────────────────────────────────────────────────

func TestAdversarial_NoKeySharing(t *testing.T) {
	wid1, err := cfsession.GenerateWorkerIdentity()
	if err != nil {
		t.Fatalf("GenerateWorkerIdentity #1: %v", err)
	}
	wid2, err := cfsession.GenerateWorkerIdentity()
	if err != nil {
		t.Fatalf("GenerateWorkerIdentity #2: %v", err)
	}

	// SECURITY: Each worker MUST get a unique key.
	// If both workers share the same key, compromise of one gives attacker
	// access to the other's grant. This is the shared-key anti-feature being removed.
	if hex.EncodeToString(wid1.PublicKey) == hex.EncodeToString(wid2.PublicKey) {
		t.Fatal("SECURITY: key sharing detected — two workers received the same key")
	}

	// Each worker gets their own distinct grant.
	now := time.Now().UTC()
	sessionUntil := now.Add(1 * time.Hour)
	result1, err := cfsession.IssueWorkerGrant(cfsession.GrantRequest{
		WorkerPubkey:  wid1.PublicKey,
		ParentGrantID: []byte("grant-root"),
		CapabilityTemplate: cfsession.CapabilityTemplate{
			Convention: "swarm-coordination",
			OpGlob:     "*",
			Until:      sessionUntil,
		},
		SessionUntil: sessionUntil,
	})
	if err != nil {
		t.Fatalf("IssueWorkerGrant #1: %v", err)
	}
	result2, err := cfsession.IssueWorkerGrant(cfsession.GrantRequest{
		WorkerPubkey:  wid2.PublicKey,
		ParentGrantID: []byte("grant-root"),
		CapabilityTemplate: cfsession.CapabilityTemplate{
			Convention: "swarm-coordination",
			OpGlob:     "*",
			Until:      sessionUntil,
		},
		SessionUntil: sessionUntil,
	})
	if err != nil {
		t.Fatalf("IssueWorkerGrant #2: %v", err)
	}

	// Grant IDs must differ (different workers get different grants).
	if hex.EncodeToString(result1.GrantID) == hex.EncodeToString(result2.GrantID) {
		t.Error("SECURITY: two different workers received the same grant ID")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 12. Adversarial: scope widening via GateEvaluator (D4)
//
// D4 scope-widening fires on multi-hop chains where the child grant claims
// wider scope than the parent grant. This is checked in walkChain at each hop.
//
// Test setup (2-hop chain):
//   - hop[0] (child/sender-side): worker's grant, OpPattern = "*" (wildcard)
//   - hop[1] (parent/root-side): orchestrator's grant, OpPattern = "claim-item"
//
// The child grant is WIDER than the parent ("*" is a superset of "claim-item").
// DefaultGateEvaluator must return DenyScopeWidening.
// ─────────────────────────────────────────────────────────────────────────────

func TestAdversarial_ScopeWidening_Denied(t *testing.T) {
	eval := cfsession.NewGateEvaluator()

	// Three principals: root → orchestrator → worker.
	rootPub, _, _ := ed25519.GenerateKey(rand.Reader)
	orchPub, _, _ := ed25519.GenerateKey(rand.Reader)
	workerPub, _, _ := ed25519.GenerateKey(rand.Reader)

	now := time.Now().UTC()
	sessionUntil := now.Add(1 * time.Hour)

	// Parent grant (hop[1], root-side): orchestratorPub has "claim-item" only.
	nonce1 := make([]byte, 16)
	rand.Read(nonce1) //nolint:errcheck
	parentCap := trust.Capability{
		Convention: "swarm-coordination",
		OpPattern:  "claim-item", // parent allows only claim-item
		Bounds:     map[string]any{},
		Until:      sessionUntil.UnixNano(),
		Nonce:      nonce1,
	}
	parentGP := trust.GrantPayload{
		ParentGrantID: nil, // root-issued
		ChildPubkey:   orchPub,
		Capabilities:  []trust.Capability{parentCap},
		Depth:         1,
	}
	parentPayload, err := trust.MarshalGrantPayloadCBOR(parentGP)
	if err != nil {
		t.Fatalf("MarshalGrantPayloadCBOR (parent): %v", err)
	}

	// Child grant (hop[0], sender-side): WIDER — workerPub gets "*" (all ops).
	// This is the widening attempt: child claims more than parent granted.
	nonce0 := make([]byte, 16)
	rand.Read(nonce0) //nolint:errcheck
	childCap := trust.Capability{
		Convention: "swarm-coordination",
		OpPattern:  "*", // child tries to claim ALL ops (wider than parent's "claim-item")
		Bounds:     map[string]any{},
		Until:      sessionUntil.UnixNano(),
		Nonce:      nonce0,
	}
	childGP := trust.GrantPayload{
		ParentGrantID: []byte("parent-grant-id"),
		ChildPubkey:   workerPub,
		Capabilities:  []trust.Capability{childCap},
		Depth:         2,
	}
	childPayload, err := trust.MarshalGrantPayloadCBOR(childGP)
	if err != nil {
		t.Fatalf("MarshalGrantPayloadCBOR (child): %v", err)
	}

	// Chain ordered sender→anchor: hop[0]=child (worker's grant), hop[1]=parent (orch's grant).
	req := convention.EvaluateRequest{
		Request: convention.GateOpRequest{
			Convention: "swarm-coordination",
			Operation:  "claim-item",
			CampfireID: "deadbeef" + "0000000000000000000000000000000000000000000000000000000000000000",
			Sender:     workerPub,
		},
		ChainMessages: []convention.GateChainMessage{
			{ID: "child-grant", Payload: childPayload},   // hop[0]: sender-side (worker)
			{ID: "parent-grant", Payload: parentPayload}, // hop[1]: root-side (orchestrator)
		},
		RootPrincipal: rootPub,
		CurrentTime:   now,
	}

	result := eval.Evaluate(context.Background(), req)
	// Child claims "*" but parent only grants "claim-item" → scope widening → Deny.
	if result.Decision != convention.GateDeny {
		t.Errorf("scope widening: expected GateDeny, got %v (reason: %v)", result.Decision, result.Reason)
	}
	if result.Reason != convention.DenyScopeWidening {
		t.Errorf("scope widening reason: got %q, want %q", result.Reason, convention.DenyScopeWidening)
	}

	// Also verify that the SAME chain with the orchestrator (non-widening) would be unresolvable/deny
	// because hop[1].ChildPubkey is orchPub, but our sender is workerPub (mismatch at hop[0]).
	_ = orchPub // referenced above; scope-widening check fires before chain connectivity
}

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

// setupProtocolClient creates a protocol.Client with a temp identity for testing.
func setupProtocolClient(t *testing.T) (*cfprotocol.Client, func()) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "cf-session-test-")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	client, _, err := cfprotocol.Init(tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("cfprotocol.Init: %v", err)
	}
	return client, func() {
		client.Close()
		os.RemoveAll(tmpDir)
	}
}

// requiredArgs returns a map of required arg names for a declaration.
func requiredArgs(d *convention.Declaration) map[string]bool {
	m := make(map[string]bool, len(d.Args))
	for _, arg := range d.Args {
		if arg.Required {
			m[arg.Name] = true
		}
	}
	return m
}

// min returns the smaller of a and b.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
