package delegation_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-conventions/cf-convention-extensions/delegation"
	"github.com/campfire-net/campfire/cf-protocol/message"
)

// campfireHex is a fixed campfire ID used across tests.
const campfireHex = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// generateKey produces a fresh ed25519 keypair and fails the test on error.
func generateKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return pub, priv
}

// makeGrantPayload serialises a GrantPayload to JSON.
func makeGrantPayload(childPubHex, campfireID string, expiresAt int64) []byte {
	p := map[string]interface{}{
		"child_pubkey": childPubHex,
		"campfire_id":  campfireID,
		"expires_at":   expiresAt,
	}
	b, _ := json.Marshal(p)
	return b
}

// newSignedGrant creates a properly signed grant message.
func newSignedGrant(t *testing.T, parentPriv ed25519.PrivateKey, parentPub ed25519.PublicKey, payload []byte) *message.Message {
	t.Helper()
	signer := message.MustNewEd25519Signer(parentPriv, parentPub)
	msg, err := message.NewMessage(signer, payload, []string{delegation.GrantedTag}, nil)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	return msg
}

// now is a fixed reference time used in all tests.
var now = time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)

// expiresInOneDay returns now + 1 day as unix seconds — well within the 7-day ceiling.
func expiresInOneDay() int64 {
	return now.Unix() + 86400
}

// Test 1: valid grant signed by real key → nil error.
func TestValidateGrant_Valid(t *testing.T) {
	pub, priv := generateKey(t)
	childPub, _ := generateKey(t)

	payload := makeGrantPayload(
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		campfireHex,
		expiresInOneDay(),
	)
	_ = childPub // child pubkey hex is in payload; parent signer is the parent key

	grant := newSignedGrant(t, priv, pub, payload)

	if err := delegation.ValidateGrant(grant, campfireHex, now); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

// Test 2: tampered payload (modified after signing) → ErrSignatureInvalid.
func TestValidateGrant_TamperedPayload(t *testing.T) {
	pub, priv := generateKey(t)

	payload := makeGrantPayload(
		"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		campfireHex,
		expiresInOneDay(),
	)
	grant := newSignedGrant(t, priv, pub, payload)

	// Tamper after signing.
	grant.Payload = []byte(`{"child_pubkey":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","campfire_id":"` + campfireHex + `","expires_at":9999999999}`)

	err := delegation.ValidateGrant(grant, campfireHex, now)
	if !errors.Is(err, delegation.ErrSignatureInvalid) {
		t.Fatalf("expected ErrSignatureInvalid, got %v", err)
	}
}

// Test 3: campfire_id mismatch → ErrCampfireMismatch.
func TestValidateGrant_CampfireMismatch(t *testing.T) {
	pub, priv := generateKey(t)

	otherCampfire := "1111111111111111111111111111111111111111111111111111111111111111"
	payload := makeGrantPayload(
		"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		otherCampfire, // payload says a different campfire
		expiresInOneDay(),
	)
	grant := newSignedGrant(t, priv, pub, payload)

	err := delegation.ValidateGrant(grant, campfireHex, now)
	if !errors.Is(err, delegation.ErrCampfireMismatch) {
		t.Fatalf("expected ErrCampfireMismatch, got %v", err)
	}
}

// Test 4: expires_at in the past → ErrGrantExpired.
func TestValidateGrant_Expired(t *testing.T) {
	pub, priv := generateKey(t)

	// Expired well before now (and beyond the 60-second slack).
	expiredAt := now.Unix() - 120

	payload := makeGrantPayload(
		"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		campfireHex,
		expiredAt,
	)
	grant := newSignedGrant(t, priv, pub, payload)

	err := delegation.ValidateGrant(grant, campfireHex, now)
	if !errors.Is(err, delegation.ErrGrantExpired) {
		t.Fatalf("expected ErrGrantExpired, got %v", err)
	}
}

// Test 5: expires_at > 7 days from msg.Timestamp → ErrGrantCeilingExceeded.
func TestValidateGrant_CeilingExceeded(t *testing.T) {
	pub, priv := generateKey(t)

	// Rule 4 compares payload.expires_at against msg.Timestamp/1e9 + 7*86400.
	// msg.Timestamp is set by message.NewMessage to real time.Now() — the caller
	// cannot inject it. So expiresAt must be computed relative to real wall-clock
	// now, not the fixture `now`, or the test rots as wall time advances past
	// the fixture date (drift makes the ceiling appear satisfied).
	eightDays := time.Now().Unix() + 8*86400

	payload := makeGrantPayload(
		"0000000000000000000000000000000000000000000000000000000000000001",
		campfireHex,
		eightDays,
	)
	grant := newSignedGrant(t, priv, pub, payload)

	// Pass real wall-clock time to the rule-3 slack check so it doesn't trip
	// before rule 4. (Rule 3 is caller-supplied now; rule 4 is msg.Timestamp.)
	err := delegation.ValidateGrant(grant, campfireHex, time.Now())
	if !errors.Is(err, delegation.ErrGrantCeilingExceeded) {
		t.Fatalf("expected ErrGrantCeilingExceeded, got %v", err)
	}
}

// Test 6: expires_at exactly at boundary (now - 60) → passes (clock-skew slack).
// Rule 3: expires_at > now - 60. The boundary value is now-60+1; exactly now-60
// fails. We test now-59 (one second above the floor) which must pass.
func TestValidateGrant_ClockSkewBoundary(t *testing.T) {
	pub, priv := generateKey(t)

	// Exactly at the slack boundary: expires_at = now - 59 > now - 60 → passes.
	atBoundary := now.Unix() - 59

	payload := makeGrantPayload(
		"0000000000000000000000000000000000000000000000000000000000000002",
		campfireHex,
		atBoundary,
	)
	grant := newSignedGrant(t, priv, pub, payload)

	// Rule 4 ceiling: msg was signed at real time.Now(), which is close to our
	// fixed `now`. atBoundary is well below the 7-day ceiling, so rule 4 passes.
	if err := delegation.ValidateGrant(grant, campfireHex, now); err != nil {
		t.Fatalf("expected nil at clock-skew boundary, got %v", err)
	}
}

// Test 7: ParseGrantPayload with malformed JSON → error.
func TestParseGrantPayload_MalformedJSON(t *testing.T) {
	_, err := delegation.ParseGrantPayload([]byte(`not json`))
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

// --- PostGrant tests (identity-delegation-v0.1.md §3 write path) ---

// TestPostGrant_RoundTrip posts a grant via PostGrant and verifies the posted
// message can be read back, parsed, signature-verified, and accepted by
// ValidateGrant. This is the happy-path smoke test that proves the write and
// read halves agree on the wire format.
func TestPostGrant_RoundTrip(t *testing.T) {
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()

	parentIdx := 0 // anchor identity
	childPub := env.pubkey(1)

	msg, err := delegation.PostGrant(ctx, env.clients[parentIdx], env.campfireIDRaw, childPub, 24*time.Hour)
	if err != nil {
		t.Fatalf("PostGrant: %v", err)
	}
	if msg == nil {
		t.Fatal("PostGrant returned nil message")
	}
	if msg.ID == "" {
		t.Error("posted message has empty ID")
	}
	if msg.Timestamp == 0 {
		t.Error("posted message has zero Timestamp")
	}
	if !msg.VerifySignature() {
		t.Error("posted message signature does not verify")
	}
	// Sender must be the parent identity's public key.
	parentPub := env.pubkey(parentIdx)
	if !ed25519.PublicKey(msg.Sender).Equal(parentPub) {
		t.Errorf("posted message Sender does not match parent pubkey")
	}

	gp, err := delegation.ParseGrantPayload(msg.Payload)
	if err != nil {
		t.Fatalf("ParseGrantPayload: %v", err)
	}
	if gp.CampfireID != env.campfireHex {
		t.Errorf("grant campfire_id: want %s, got %s", env.campfireHex, gp.CampfireID)
	}
	if gp.ChildPubkey != hex.EncodeToString(childPub) {
		t.Errorf("grant child_pubkey: want %s, got %s", hex.EncodeToString(childPub), gp.ChildPubkey)
	}
	if gp.ExpiresAt <= time.Now().Unix() {
		t.Errorf("grant expires_at not in future: %d", gp.ExpiresAt)
	}

	// Must pass ValidateGrant.
	if err := delegation.ValidateGrant(msg, env.campfireHex, time.Now()); err != nil {
		t.Errorf("ValidateGrant on PostGrant output: %v", err)
	}
}

// TestPostGrant_ZeroTTL asserts ErrGrantTTLInvalid for ttl=0 and negative.
func TestPostGrant_ZeroTTL(t *testing.T) {
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()
	childPub := env.pubkey(1)

	for _, ttl := range []time.Duration{0, -time.Second, -24 * time.Hour} {
		_, err := delegation.PostGrant(ctx, env.clients[0], env.campfireIDRaw, childPub, ttl)
		if !errors.Is(err, delegation.ErrGrantTTLInvalid) {
			t.Errorf("ttl=%v: expected ErrGrantTTLInvalid, got %v", ttl, err)
		}
	}
}

// TestPostGrant_ExceedsCeiling asserts ttl > 7 days returns ErrGrantCeilingExceeded
// and no message is posted to the campfire.
func TestPostGrant_ExceedsCeiling(t *testing.T) {
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()
	childPub := env.pubkey(1)

	// Count granted messages before.
	beforeMsgs, err := env.fsTransport.ListMessages(env.campfireHex)
	if err != nil {
		t.Fatalf("ListMessages pre: %v", err)
	}
	var beforeGrants int
	for i := range beforeMsgs {
		for _, tag := range beforeMsgs[i].Tags {
			if tag == delegation.GrantedTag {
				beforeGrants++
			}
		}
	}

	_, err = delegation.PostGrant(ctx, env.clients[0], env.campfireIDRaw, childPub, 8*24*time.Hour)
	if !errors.Is(err, delegation.ErrGrantCeilingExceeded) {
		t.Fatalf("expected ErrGrantCeilingExceeded, got %v", err)
	}

	// Count granted messages after — must be unchanged.
	afterMsgs, err := env.fsTransport.ListMessages(env.campfireHex)
	if err != nil {
		t.Fatalf("ListMessages post: %v", err)
	}
	var afterGrants int
	for i := range afterMsgs {
		for _, tag := range afterMsgs[i].Tags {
			if tag == delegation.GrantedTag {
				afterGrants++
			}
		}
	}
	if afterGrants != beforeGrants {
		t.Errorf("ceiling-violated PostGrant posted a message: before=%d after=%d", beforeGrants, afterGrants)
	}
}

// TestValidateGrant_ChildKeyMalformed_NonHex verifies that a grant whose payload
// child_pubkey is not valid hex is rejected with ErrGrantChildKeyMalformed.
func TestValidateGrant_ChildKeyMalformed_NonHex(t *testing.T) {
	pub, priv := generateKey(t)

	// "not-hex!!" is not valid hexadecimal.
	payload := makeGrantPayload("not-hex!!", campfireHex, expiresInOneDay())
	grant := newSignedGrant(t, priv, pub, payload)

	err := delegation.ValidateGrant(grant, campfireHex, now)
	if !errors.Is(err, delegation.ErrGrantChildKeyMalformed) {
		t.Fatalf("expected ErrGrantChildKeyMalformed for non-hex child_pubkey, got %v", err)
	}
}

// TestValidateGrant_ChildKeyMalformed_WrongLength verifies that a grant whose
// payload child_pubkey is valid hex but decodes to fewer than 32 bytes is
// rejected with ErrGrantChildKeyMalformed.
func TestValidateGrant_ChildKeyMalformed_WrongLength(t *testing.T) {
	pub, priv := generateKey(t)

	// 30 bytes = 60 hex chars — valid hex but wrong Ed25519 key size.
	shortKey := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	payload := makeGrantPayload(shortKey, campfireHex, expiresInOneDay())
	grant := newSignedGrant(t, priv, pub, payload)

	err := delegation.ValidateGrant(grant, campfireHex, now)
	if !errors.Is(err, delegation.ErrGrantChildKeyMalformed) {
		t.Fatalf("expected ErrGrantChildKeyMalformed for short child_pubkey, got %v", err)
	}
}

// TestPostGrant_EndToEnd issues a grant via PostGrant and then calls Resolve
// on the child — asserts Resolved{}. Proves the write path, the read path,
// and the convention wire format all agree.
func TestPostGrant_EndToEnd(t *testing.T) {
	env := newTrustTestEnv(t, 2)
	ctx := context.Background()

	parentPub := env.pubkey(0)
	childPub := env.pubkey(1)
	anchors := env.anchors(0)

	// Issue via PostGrant (not the test harness postGrant helper).
	if _, err := delegation.PostGrant(ctx, env.clients[0], env.campfireIDRaw, childPub, 24*time.Hour); err != nil {
		t.Fatalf("PostGrant: %v", err)
	}
	// Sync the posted message into every client's store so Resolve can see it.
	env.syncAll(t)

	out := delegation.Resolve(ctx, env.clients[1], env.campfireIDRaw, childPub, anchors)
	r, ok := out.(delegation.Resolved)
	if !ok {
		t.Fatalf("expected Resolved, got %T: %+v", out, out)
	}
	if len(r.Chain) != 1 {
		t.Errorf("expected chain length 1, got %d", len(r.Chain))
	}
	if !r.Anchor.Equal(parentPub) {
		t.Error("expected anchor == parent")
	}
	if !r.Chain[0].ChildPubkey.Equal(childPub) {
		t.Error("expected chain[0].ChildPubkey == child")
	}
	if !r.Chain[0].ParentPubkey.Equal(parentPub) {
		t.Error("expected chain[0].ParentPubkey == parent")
	}
}
