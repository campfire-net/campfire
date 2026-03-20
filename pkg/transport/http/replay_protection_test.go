package http_test

// Tests for HTTP request replay protection (workspace-yov).
//
// The Ed25519 request signature scheme now signs (timestamp + nonce + body).
// The server enforces:
//   (a) Timestamp freshness: requests with timestamp > 60s old are rejected (401).
//   (b) Future timestamps: requests with timestamp > 60s in the future are rejected (401).
//   (c) Nonce uniqueness: replaying a valid request (same nonce) is rejected (401).
//   (d) Missing headers: requests missing X-Campfire-Nonce or X-Campfire-Timestamp are rejected (401).

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// buildDeliverRequest constructs a signed deliver request using the nonce and
// timestamp provided. The signed payload is timestamp+"\n"+nonce+"\n"+body.
func buildDeliverRequest(t *testing.T, ep, campfireID string, id *identity.Identity, body []byte, nonce, timestamp string) *http.Request {
	t.Helper()
	payload := buildTestPayload(timestamp, nonce, body)
	sig := id.Sign(payload)
	url := fmt.Sprintf("%s/campfire/%s/deliver", ep, campfireID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("X-Campfire-Sender", id.PublicKeyHex())
	req.Header.Set("X-Campfire-Nonce", nonce)
	req.Header.Set("X-Campfire-Timestamp", timestamp)
	req.Header.Set("X-Campfire-Signature", base64.StdEncoding.EncodeToString(sig))
	return req
}

// buildTestPayload constructs the canonical signed payload: timestamp+"\n"+nonce+"\n"+body.
// Mirrors buildSignedPayload in handler_message.go.
func buildTestPayload(timestamp, nonce string, body []byte) []byte {
	var p []byte
	p = append(p, []byte(timestamp)...)
	p = append(p, '\n')
	p = append(p, []byte(nonce)...)
	p = append(p, '\n')
	p = append(p, body...)
	return p
}

// freshNonce returns a random 16-byte hex nonce.
func freshNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("rand.Read: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// setupDeliverServer starts a transport for deliver tests and returns endpoint and identity.
func setupDeliverServer(t *testing.T, port int) (ep, campfireID string, id *identity.Identity) {
	t.Helper()
	id = tempIdentity(t)
	s := tempStore(t)
	campfireID = "replay-test-cf"
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, id.PublicKeyHex())

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ep = fmt.Sprintf("http://%s", addr)
	startTransportWithSelf(t, addr, s, id)
	return ep, campfireID, id
}

// newDeliverBody creates a valid CBOR-encoded message body for deliver tests.
func newDeliverBody(t *testing.T, id *identity.Identity) []byte {
	t.Helper()
	msg := newTestMessage(t, id)
	body, err := cfencoding.Marshal(msg)
	if err != nil {
		t.Fatalf("encoding message: %v", err)
	}
	return body
}

// ---------------------------------------------------------------------------
// workspace-yov — replay protection: stale timestamp
// ---------------------------------------------------------------------------

// TestReplayProtectionStaleTimestampRejected verifies that a request with a
// timestamp more than 60 seconds in the past is rejected with 401.
func TestReplayProtectionStaleTimestampRejected(t *testing.T) {
	base := portBase()
	ep, campfireID, id := setupDeliverServer(t, base+260)

	body := newDeliverBody(t, id)
	staleTimestamp := strconv.FormatInt(time.Now().Add(-90*time.Second).Unix(), 10)
	nonce := freshNonce()

	req := buildDeliverRequest(t, ep, campfireID, id, body, nonce, staleTimestamp)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for stale timestamp, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// workspace-yov — replay protection: future timestamp
// ---------------------------------------------------------------------------

// TestReplayProtectionFutureTimestampRejected verifies that a request with a
// timestamp more than 60 seconds in the future is rejected with 401.
func TestReplayProtectionFutureTimestampRejected(t *testing.T) {
	base := portBase()
	ep, campfireID, id := setupDeliverServer(t, base+261)

	body := newDeliverBody(t, id)
	futureTimestamp := strconv.FormatInt(time.Now().Add(90*time.Second).Unix(), 10)
	nonce := freshNonce()

	req := buildDeliverRequest(t, ep, campfireID, id, body, nonce, futureTimestamp)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for future timestamp, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// workspace-yov — replay protection: nonce replay rejected
// ---------------------------------------------------------------------------

// TestReplayProtectionNonceReplayRejected verifies that replaying an identical
// signed request (same nonce) is rejected with 401, even if the request itself
// was originally valid.
func TestReplayProtectionNonceReplayRejected(t *testing.T) {
	base := portBase()
	ep, campfireID, id := setupDeliverServer(t, base+262)

	// We need two different messages (different IDs) to avoid dedup at the store level,
	// but the same nonce to test replay detection.
	msg1 := newTestMessage(t, id)
	body1, err := cfencoding.Marshal(msg1)
	if err != nil {
		t.Fatalf("encoding message 1: %v", err)
	}

	msg2 := newTestMessage(t, id)
	body2, err := cfencoding.Marshal(msg2)
	if err != nil {
		t.Fatalf("encoding message 2: %v", err)
	}

	nonce := freshNonce()
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)

	// First request: should succeed (200).
	req1 := buildDeliverRequest(t, ep, campfireID, id, body1, nonce, timestamp)
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("first request failed: %v", err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for first (valid) request, got %d", resp1.StatusCode)
	}

	// Second request with same nonce but different body: must be rejected (401).
	req2 := buildDeliverRequest(t, ep, campfireID, id, body2, nonce, timestamp)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("replay request failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for replayed nonce, got %d", resp2.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// workspace-yov — replay protection: missing nonce/timestamp headers
// ---------------------------------------------------------------------------

// TestReplayProtectionMissingNonceRejected verifies that a request missing
// X-Campfire-Nonce is rejected with 401.
func TestReplayProtectionMissingNonceRejected(t *testing.T) {
	base := portBase()
	ep, campfireID, id := setupDeliverServer(t, base+263)

	body := newDeliverBody(t, id)
	url := fmt.Sprintf("%s/campfire/%s/deliver", ep, campfireID)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	signTestRequest(req, id, body)
	req.Header.Del("X-Campfire-Nonce") // remove nonce — should be rejected

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing nonce, got %d", resp.StatusCode)
	}
}

// TestReplayProtectionMissingTimestampRejected verifies that a request missing
// X-Campfire-Timestamp is rejected with 401.
func TestReplayProtectionMissingTimestampRejected(t *testing.T) {
	base := portBase()
	ep, campfireID, id := setupDeliverServer(t, base+264)

	body := newDeliverBody(t, id)
	url := fmt.Sprintf("%s/campfire/%s/deliver", ep, campfireID)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	signTestRequest(req, id, body)
	req.Header.Del("X-Campfire-Timestamp") // remove timestamp — should be rejected

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing timestamp, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// workspace-yov — replay protection: fresh request with unique nonce is accepted
// ---------------------------------------------------------------------------

// TestReplayProtectionFreshRequestAccepted verifies that a correctly constructed
// request with a valid timestamp and unique nonce is accepted (200).
func TestReplayProtectionFreshRequestAccepted(t *testing.T) {
	base := portBase()
	ep, campfireID, id := setupDeliverServer(t, base+265)

	if err := cfhttp.Deliver(ep, campfireID, newTestMessage(t, id), id); err != nil {
		t.Fatalf("fresh request should be accepted: %v", err)
	}
}

// TestReplayProtectionInvalidTimestampFormatRejected verifies that a non-numeric
// timestamp header value is rejected with 401.
func TestReplayProtectionInvalidTimestampFormatRejected(t *testing.T) {
	base := portBase()
	ep, campfireID, id := setupDeliverServer(t, base+266)

	body := newDeliverBody(t, id)
	url := fmt.Sprintf("%s/campfire/%s/deliver", ep, campfireID)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	signTestRequest(req, id, body)
	req.Header.Set("X-Campfire-Timestamp", "not-a-number") // override with bad value

	// The signature no longer matches since we changed the timestamp, but the
	// timestamp parse failure should trigger 401 before signature check.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid timestamp format, got %d", resp.StatusCode)
	}
}
