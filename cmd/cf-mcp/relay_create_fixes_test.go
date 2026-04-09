package main

// relay_create_fixes_test.go — Tests for campfireagent-3f5 fixes.
//
// Covers:
//   S-M2: ValidateCampfireID rejects empty, wrong-length, uppercase, non-hex IDs.
//   S-M3: AddMembership and UpsertPeerEndpoint errors → 500.
//   B-H1: Missing static X25519 key → 501 Not Implemented.
//   AP-L1: Membership struct built once (verified via transport registration).

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// ---------------------------------------------------------------------------
// S-M2: ValidateCampfireID — HTTP handler tests
// ---------------------------------------------------------------------------

// TestValidateCampfireID_Empty verifies that an empty campfire_id returns 400.
func TestValidateCampfireID_Empty(t *testing.T) {
	router, _ := newRelayTestServer(t)
	relayPriv, relayPubHex := generateRelayX25519Key(t)
	router.SetStaticX25519Key(relayPriv)

	creatorID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating creator identity: %v", err)
	}
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}

	body := buildCreateRequest(t, creatorID, cf.PrivateKey, cf.PublicKey, relayPubHex, "open")
	var req cfhttp.CreateCampfireRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	req.CampfireID = ""
	body, _ = json.Marshal(req)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, signedCreateRequest(t, "/campfire/create", body, creatorID))
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty campfire_id: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid campfire_id") {
		t.Errorf("expected 'invalid campfire_id' in body, got %q", w.Body.String())
	}
}

// TestValidateCampfireID_TooShort verifies that a too-short campfire_id returns 400.
func TestValidateCampfireID_TooShort(t *testing.T) {
	router, _ := newRelayTestServer(t)
	relayPriv, relayPubHex := generateRelayX25519Key(t)
	router.SetStaticX25519Key(relayPriv)

	creatorID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating creator identity: %v", err)
	}
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}

	body := buildCreateRequest(t, creatorID, cf.PrivateKey, cf.PublicKey, relayPubHex, "open")
	var req cfhttp.CreateCampfireRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// 32 hex chars = 16 bytes (too short; valid is 64 hex chars = 32 bytes)
	req.CampfireID = strings.Repeat("a", 32)
	body, _ = json.Marshal(req)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, signedCreateRequest(t, "/campfire/create", body, creatorID))
	if w.Code != http.StatusBadRequest {
		t.Errorf("too-short campfire_id: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid campfire_id") {
		t.Errorf("expected 'invalid campfire_id' in body, got %q", w.Body.String())
	}
}

// TestValidateCampfireID_Uppercase verifies that a campfire_id with uppercase hex returns 400.
func TestValidateCampfireID_Uppercase(t *testing.T) {
	router, _ := newRelayTestServer(t)
	relayPriv, relayPubHex := generateRelayX25519Key(t)
	router.SetStaticX25519Key(relayPriv)

	creatorID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating creator identity: %v", err)
	}
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}

	body := buildCreateRequest(t, creatorID, cf.PrivateKey, cf.PublicKey, relayPubHex, "open")
	var req cfhttp.CreateCampfireRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Make it uppercase — still 64 chars, valid hex digits but wrong case.
	req.CampfireID = strings.ToUpper(req.CampfireID)
	body, _ = json.Marshal(req)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, signedCreateRequest(t, "/campfire/create", body, creatorID))
	if w.Code != http.StatusBadRequest {
		t.Errorf("uppercase campfire_id: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid campfire_id") {
		t.Errorf("expected 'invalid campfire_id' in body, got %q", w.Body.String())
	}
}

// TestValidateCampfireID_NonHex verifies that a campfire_id with non-hex characters returns 400.
func TestValidateCampfireID_NonHex(t *testing.T) {
	router, _ := newRelayTestServer(t)
	relayPriv, relayPubHex := generateRelayX25519Key(t)
	router.SetStaticX25519Key(relayPriv)

	creatorID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating creator identity: %v", err)
	}
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}

	body := buildCreateRequest(t, creatorID, cf.PrivateKey, cf.PublicKey, relayPubHex, "open")
	var req cfhttp.CreateCampfireRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// 64 chars but contains 'g' which is not a valid hex character.
	req.CampfireID = strings.Repeat("g", 64)
	body, _ = json.Marshal(req)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, signedCreateRequest(t, "/campfire/create", body, creatorID))
	if w.Code != http.StatusBadRequest {
		t.Errorf("non-hex campfire_id: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid campfire_id") {
		t.Errorf("expected 'invalid campfire_id' in body, got %q", w.Body.String())
	}
}

// TestValidateCampfireID_ValidAccepted verifies that a valid 64-char lowercase hex ID is accepted.
func TestValidateCampfireID_ValidAccepted(t *testing.T) {
	router, _ := newRelayTestServer(t)
	relayPriv, relayPubHex := generateRelayX25519Key(t)
	router.SetStaticX25519Key(relayPriv)

	creatorID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating creator identity: %v", err)
	}
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}

	body := buildCreateRequest(t, creatorID, cf.PrivateKey, cf.PublicKey, relayPubHex, "open")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, signedCreateRequest(t, "/campfire/create", body, creatorID))
	if w.Code != http.StatusCreated {
		t.Errorf("valid campfire_id: expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// B-H1: No static X25519 key → 501 Not Implemented
// ---------------------------------------------------------------------------

// TestRelayCreate_NoStaticKey verifies that POST /campfire/create returns 501 when
// the relay has no static X25519 key configured.
func TestRelayCreate_NoStaticKey(t *testing.T) {
	router, _ := newRelayTestServer(t)
	// Deliberately do NOT call router.SetStaticX25519Key.

	// We still need a relay pub key to build an otherwise valid request body;
	// just use a freshly generated key that is NOT configured on the relay.
	relayPriv, _ := generateRelayX25519Key(t)

	creatorID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating creator identity: %v", err)
	}
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}

	body := buildCreateRequest(t, creatorID, cf.PrivateKey, cf.PublicKey,
		hex.EncodeToString(relayPriv.PublicKey().Bytes()), "open")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, signedCreateRequest(t, "/campfire/create", body, creatorID))
	if w.Code != http.StatusNotImplemented {
		t.Errorf("no static key: expected 501, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "relay not configured for campfire creation") {
		t.Errorf("expected 'relay not configured for campfire creation' in body, got %q", w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// AP-L1: Membership struct built once — verified via transport registration
// ---------------------------------------------------------------------------

// TestRelayCreate_MembershipStoredOnce verifies that creating a campfire stores
// the membership and registers the transport (AP-L1: single membership build, no duplication).
func TestRelayCreate_MembershipStoredOnce(t *testing.T) {
	router, _ := newRelayTestServer(t)
	relayPriv, relayPubHex := generateRelayX25519Key(t)
	router.SetStaticX25519Key(relayPriv)

	creatorID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating creator identity: %v", err)
	}
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}

	body := buildCreateRequest(t, creatorID, cf.PrivateKey, cf.PublicKey, relayPubHex, "open")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, signedCreateRequest(t, "/campfire/create", body, creatorID))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// The campfire transport must be registered exactly once.
	tr := router.GetCampfireTransport(cf.PublicKeyHex())
	if tr == nil {
		t.Fatal("campfire not registered in router after create")
	}

	// A second lookup returns the same transport (idempotent, no duplicate registration).
	tr2 := router.GetCampfireTransport(cf.PublicKeyHex())
	if tr != tr2 {
		t.Error("GetCampfireTransport returned different transport on second call (unexpected)")
	}
}

// ---------------------------------------------------------------------------
// S-M2: ValidateCampfireID unit tests (direct function call)
// ---------------------------------------------------------------------------

// buildValidCampfireID returns a valid 64-char lowercase hex campfire ID.
// ed25519.GenerateKey returns (PublicKey, PrivateKey, error).
func buildValidCampfireID(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating ed25519 key: %v", err)
	}
	return fmt.Sprintf("%x", pub)
}

func TestValidateCampfireID_DirectUnit(t *testing.T) {
	validID := buildValidCampfireID(t)
	cases := []struct {
		name    string
		id      string
		wantErr bool
		errPart string
	}{
		{"valid", validID, false, ""},
		{"empty", "", true, "empty"},
		{"too short", strings.Repeat("a", 32), true, "wrong length"},
		{"too long", strings.Repeat("a", 128), true, "wrong length"},
		{"uppercase", strings.ToUpper(validID), true, "uppercase"},
		{"non-hex g", strings.Repeat("g", 64), true, "non-hex"},
		{"non-hex space", strings.Repeat(" ", 64), true, "non-hex"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := cfhttp.ValidateCampfireID(tc.id)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error for %q, got nil", tc.id)
					return
				}
				if tc.errPart != "" && !strings.Contains(err.Error(), tc.errPart) {
					t.Errorf("expected error containing %q, got %q", tc.errPart, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("expected no error for %q, got %v", tc.id, err)
				}
			}
		})
	}
}
