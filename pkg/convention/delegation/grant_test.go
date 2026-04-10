package delegation_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/convention/delegation"
	"github.com/campfire-net/campfire/pkg/message"
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

	// 8 days from now — over the 7-day ceiling.
	eightDays := now.Unix() + 8*86400

	payload := makeGrantPayload(
		"0000000000000000000000000000000000000000000000000000000000000001",
		campfireHex,
		eightDays,
	)
	grant := newSignedGrant(t, priv, pub, payload)

	// Massage Timestamp so the message was signed "now" (NewMessage uses
	// time.Now(); we need to check relative to the message's own timestamp).
	// The signed grant's Timestamp is time.Now().UnixNano() at creation, so
	// msg.Timestamp/1e9 ≈ now.Unix(). An 8-day expiry therefore exceeds the
	// 7-day ceiling.
	err := delegation.ValidateGrant(grant, campfireHex, now)
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
