package http_test

// Tests that convention:operation declaration messages are included in the join
// response, allowing the joiner to register convention tools without a separate
// message sync round-trip.

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// TestJoinResponseIncludesDeclarations verifies that when a campfire has
// convention:operation messages, the join response carries them in the
// Declarations field so the joiner can discover convention tools immediately.
func TestJoinResponseIncludesDeclarations(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire key: %v", err)
	}
	campfireID := fmt.Sprintf("%x", cfPub)

	hostID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating host identity: %v", err)
	}

	sHost := tempStore(t)
	if err := sHost.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: t.TempDir(),
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	// Seed a convention:operation message in the store (simulates what
	// campfire_create does when declarations are provided).
	declPayload := []byte(`{
		"convention": "social-post-format",
		"version": "0.3",
		"operation": "post",
		"description": "Publish a social post",
		"signing": "member_key"
	}`)
	if _, err := sHost.AddMessage(store.MessageRecord{
		ID:         "decl-msg-001",
		CampfireID: campfireID,
		Sender:     hostID.PublicKeyHex(),
		Payload:    declPayload,
		Tags:       []string{"convention:operation"},
		Timestamp:  time.Now().UnixNano(),
		Signature:  []byte("test-sig"),
		ReceivedAt: time.Now().UnixNano(),
	}); err != nil {
		t.Fatalf("adding declaration message: %v", err)
	}

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+350)
	ep := fmt.Sprintf("http://%s", addr)

	tr := cfhttp.New(addr, sHost)
	tr.SetSelfInfo(hostID.PublicKeyHex(), ep)
	tr.SetKeyProvider(func(id string) ([]byte, []byte, error) {
		if id == campfireID {
			return cfPriv, cfPub, nil
		}
		return nil, nil, fmt.Errorf("not found")
	})
	if err := tr.Start(); err != nil {
		t.Fatalf("starting transport: %v", err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	// Build and send a join request.
	joiner, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating joiner identity: %v", err)
	}
	ephemPriv, _ := ecdh.X25519().GenerateKey(rand.Reader)
	ephemPubHex := fmt.Sprintf("%x", ephemPriv.PublicKey().Bytes())

	joinBody, _ := json.Marshal(cfhttp.JoinRequest{
		JoinerPubkey:       joiner.PublicKeyHex(),
		EphemeralX25519Pub: ephemPubHex,
	})
	url := fmt.Sprintf("%s/campfire/%s/join", ep, campfireID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(joinBody))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	signTestRequest(req, joiner, joinBody)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("join HTTP error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var joinResp cfhttp.JoinResponse
	if err := json.NewDecoder(resp.Body).Decode(&joinResp); err != nil {
		t.Fatalf("decoding join response: %v", err)
	}

	// The join response must include the declaration message.
	if len(joinResp.Declarations) != 1 {
		t.Fatalf("expected 1 declaration in join response, got %d", len(joinResp.Declarations))
	}
	decl := joinResp.Declarations[0]
	if decl.ID != "decl-msg-001" {
		t.Errorf("declaration ID: got %q, want %q", decl.ID, "decl-msg-001")
	}
	if decl.Sender != hostID.PublicKeyHex() {
		t.Errorf("declaration sender mismatch")
	}
	if len(decl.Tags) != 1 || decl.Tags[0] != "convention:operation" {
		t.Errorf("declaration tags: got %v, want [convention:operation]", decl.Tags)
	}

	// Verify the payload is the original declaration JSON.
	var payload map[string]interface{}
	if err := json.Unmarshal(decl.Payload, &payload); err != nil {
		t.Fatalf("unmarshaling declaration payload: %v", err)
	}
	if payload["operation"] != "post" {
		t.Errorf("declaration operation: got %v, want 'post'", payload["operation"])
	}
}

// TestJoinResponseEmptyDeclarationsWhenNone verifies that when a campfire has
// no convention:operation messages, the Declarations field is nil/empty.
func TestJoinResponseEmptyDeclarationsWhenNone(t *testing.T) {
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire key: %v", err)
	}
	campfireID := fmt.Sprintf("%x", cfPub)

	hostID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating host identity: %v", err)
	}

	sHost := tempStore(t)
	if err := sHost.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: t.TempDir(),
		JoinProtocol: "open",
		Role:         "creator",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+352)
	ep := fmt.Sprintf("http://%s", addr)

	tr := cfhttp.New(addr, sHost)
	tr.SetSelfInfo(hostID.PublicKeyHex(), ep)
	tr.SetKeyProvider(func(id string) ([]byte, []byte, error) {
		if id == campfireID {
			return cfPriv, cfPub, nil
		}
		return nil, nil, fmt.Errorf("not found")
	})
	if err := tr.Start(); err != nil {
		t.Fatalf("starting transport: %v", err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	joiner, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating joiner identity: %v", err)
	}
	ephemPriv, _ := ecdh.X25519().GenerateKey(rand.Reader)
	ephemPubHex := fmt.Sprintf("%x", ephemPriv.PublicKey().Bytes())

	joinBody, _ := json.Marshal(cfhttp.JoinRequest{
		JoinerPubkey:       joiner.PublicKeyHex(),
		EphemeralX25519Pub: ephemPubHex,
	})
	url := fmt.Sprintf("%s/campfire/%s/join", ep, campfireID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(joinBody))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	signTestRequest(req, joiner, joinBody)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("join HTTP error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var joinResp cfhttp.JoinResponse
	if err := json.NewDecoder(resp.Body).Decode(&joinResp); err != nil {
		t.Fatalf("decoding join response: %v", err)
	}

	if len(joinResp.Declarations) != 0 {
		t.Errorf("expected 0 declarations for campfire without conventions, got %d", len(joinResp.Declarations))
	}
}
