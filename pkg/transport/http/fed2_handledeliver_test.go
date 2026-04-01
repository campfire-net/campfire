package http_test

// FED-2 HTTP-layer tests for the beacon rejection gate in handleDeliver.
//
// handleDeliver (pkg/transport/http/handler_message.go lines 124-141) validates
// routing:beacon payloads before storage:
//   1. Payload must be valid JSON matching BeaconDeclaration.
//   2. Beacon must pass beacon.VerifyDeclaration (valid inner_signature).
//
// These tests prove the gate fires at the HTTP layer and returns HTTP 400 before
// any storage occurs. They complement the unit-level routing table tests in
// beacon_roundtrip_test.go by exercising the full handleDeliver path.
//
// Scenarios:
//   1. TestFED2_BeaconInvalidJSON — routing:beacon with non-JSON payload → HTTP 400.
//   2. TestFED2_BeaconTamperedSignature — valid JSON but tampered endpoint (bad sig) → HTTP 400.
//   3. TestFED2_BeaconValidAccepted — valid signed beacon → HTTP 200, message stored.
//
// Port block: 640 - 659 (fed2_handledeliver_test.go)

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/beacon"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
)

// newBeaconMessage creates a signed message.Message with tag "routing:beacon" whose
// Payload is the JSON-encoded beacon declaration. The message is signed by sender.
func newBeaconMessage(t *testing.T, sender *identity.Identity, payload []byte) *message.Message {
	t.Helper()
	msg, err := message.NewMessage(
		sender.PrivateKey,
		sender.PublicKey,
		payload,
		[]string{"routing:beacon"},
		nil,
	)
	if err != nil {
		t.Fatalf("newBeaconMessage: %v", err)
	}
	return msg
}

// postDeliver encodes msg as CBOR and POSTs to /campfire/<campfireID>/deliver.
// The HTTP request is signed by deliverer. Returns the HTTP response (caller closes body).
func postDeliver(t *testing.T, ep, campfireID string, msg *message.Message, deliverer *identity.Identity) *http.Response {
	t.Helper()
	body, err := cfencoding.Marshal(msg)
	if err != nil {
		t.Fatalf("postDeliver: encoding message: %v", err)
	}
	url := fmt.Sprintf("%s/campfire/%s/deliver", ep, campfireID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("postDeliver: building request: %v", err)
	}
	signTestRequest(req, deliverer, body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("postDeliver: request failed: %v", err)
	}
	return resp
}

// TestFED2_BeaconInvalidJSON verifies that a routing:beacon message whose payload
// is not valid JSON is rejected by handleDeliver with HTTP 400 before storage.
func TestFED2_BeaconInvalidJSON(t *testing.T) {
	campfireID := "fed2-beacon-invalid-json"
	id := tempIdentity(t)

	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, id.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+640)
	startTransportWithSelf(t, addr, s, id)
	ep := fmt.Sprintf("http://%s", addr)

	// Build a routing:beacon message with non-JSON payload.
	invalidPayload := []byte("this is not json {{{{")
	msg := newBeaconMessage(t, id, invalidPayload)

	resp := postDeliver(t, ep, campfireID, msg, id)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("FED-2 invalid-JSON beacon: expected HTTP 400, got %d: %s", resp.StatusCode, body)
	}

	// Confirm nothing was stored.
	msgs, err := s.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("FED-2 invalid-JSON beacon: expected 0 stored messages, got %d", len(msgs))
	}
}

// TestFED2_BeaconTamperedSignature verifies that a routing:beacon message whose
// payload is valid JSON but carries a bad inner_signature (tampered endpoint) is
// rejected by handleDeliver with HTTP 400 before storage.
func TestFED2_BeaconTamperedSignature(t *testing.T) {
	campfireID := "fed2-beacon-tampered-sig"
	id := tempIdentity(t)

	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, id.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+641)
	startTransportWithSelf(t, addr, s, id)
	ep := fmt.Sprintf("http://%s", addr)

	// Generate a campfire keypair and sign a valid beacon.
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire key: %v", err)
	}
	campfireIDHex := hex.EncodeToString(cfPub)

	decl, err := beacon.SignDeclaration(
		cfPub, cfPriv,
		"https://relay.example.com",
		"p2p-http",
		campfireIDHex,
		"open",
	)
	if err != nil {
		t.Fatalf("SignDeclaration: %v", err)
	}

	// Tamper: change the endpoint after signing so the signature no longer matches.
	decl.Endpoint = "https://attacker.example.com"

	tamperedPayload, err := json.Marshal(decl)
	if err != nil {
		t.Fatalf("marshaling tampered declaration: %v", err)
	}

	msg := newBeaconMessage(t, id, tamperedPayload)
	resp := postDeliver(t, ep, campfireID, msg, id)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("FED-2 tampered-sig beacon: expected HTTP 400, got %d: %s", resp.StatusCode, body)
	}

	// Confirm nothing was stored.
	msgs, err := s.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("FED-2 tampered-sig beacon: expected 0 stored messages, got %d", len(msgs))
	}
}

// TestFED2_BeaconValidAccepted verifies that a correctly signed routing:beacon
// message passes the FED-2 gate and is stored (HTTP 200).
func TestFED2_BeaconValidAccepted(t *testing.T) {
	campfireID := "fed2-beacon-valid"
	id := tempIdentity(t)

	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, id.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+642)
	startTransportWithSelf(t, addr, s, id)
	ep := fmt.Sprintf("http://%s", addr)

	// Generate a campfire keypair and sign a valid beacon.
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire key: %v", err)
	}
	campfireIDHex := hex.EncodeToString(cfPub)

	decl, err := beacon.SignDeclaration(
		cfPub, cfPriv,
		"https://relay.example.com",
		"p2p-http",
		campfireIDHex,
		"open",
	)
	if err != nil {
		t.Fatalf("SignDeclaration: %v", err)
	}

	validPayload, err := json.Marshal(decl)
	if err != nil {
		t.Fatalf("marshaling declaration: %v", err)
	}

	msg := newBeaconMessage(t, id, validPayload)
	resp := postDeliver(t, ep, campfireID, msg, id)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("FED-2 valid beacon: expected HTTP 200, got %d: %s", resp.StatusCode, body)
	}

	// Give the store a moment to commit, then confirm the message was stored.
	time.Sleep(10 * time.Millisecond)
	msgs, err := s.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Errorf("FED-2 valid beacon: expected 1 stored message, got %d", len(msgs))
	}
	if len(msgs) == 1 {
		// Confirm it's our message by payload.
		if string(msgs[0].Payload) != string(validPayload) {
			t.Errorf("stored payload mismatch: got %q, want %q", msgs[0].Payload, validPayload)
		}
	}
}
