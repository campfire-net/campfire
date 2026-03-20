package http_test

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// signTestRequest sets the four Campfire auth headers (Sender, Nonce, Timestamp,
// Signature) on req. body must be the request body bytes (empty slice for GET).
// The signature covers timestamp+nonce+body, matching the server-side scheme.
func signTestRequest(req *http.Request, id *identity.Identity, body []byte) {
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		panic("signTestRequest: rand.Read: " + err.Error())
	}
	nonce := hex.EncodeToString(nonceBytes)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)

	// Build signed payload: timestamp + "\n" + nonce + "\n" + body.
	var payload []byte
	payload = append(payload, []byte(timestamp)...)
	payload = append(payload, '\n')
	payload = append(payload, []byte(nonce)...)
	payload = append(payload, '\n')
	payload = append(payload, body...)

	sig := id.Sign(payload)
	req.Header.Set("X-Campfire-Sender", id.PublicKeyHex())
	req.Header.Set("X-Campfire-Nonce", nonce)
	req.Header.Set("X-Campfire-Timestamp", timestamp)
	req.Header.Set("X-Campfire-Signature", base64.StdEncoding.EncodeToString(sig))
}

// doPoll performs an authenticated GET /campfire/{id}/poll request.
// Returns the http.Response (caller must close Body).
func doPoll(ep, campfireID string, since int64, timeout int, id *identity.Identity) (*http.Response, error) {
	url := fmt.Sprintf("%s/campfire/%s/poll?since=%d&timeout=%d", ep, campfireID, since, timeout)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	signTestRequest(req, id, []byte{})
	return http.DefaultClient.Do(req)
}

// addPeerEndpoint adds a peer to the store so membership checks pass.
func addPeerEndpoint(t *testing.T, s *store.Store, campfireID, pubKeyHex string) {
	t.Helper()
	err := s.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: pubKeyHex,
		Endpoint:     "http://127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("adding peer endpoint: %v", err)
	}
}

// storeMessageRecord inserts a message record directly into the store, returning the record.
func storeMessageRecord(t *testing.T, s *store.Store, campfireID string, id *identity.Identity) store.MessageRecord {
	t.Helper()
	msg, err := message.NewMessage(id.PrivateKey, id.PublicKey, []byte("poll test payload"), []string{"test"}, nil)
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}
	rec := store.MessageRecord{
		ID:          msg.ID,
		CampfireID:  campfireID,
		Sender:      id.PublicKeyHex(),
		Payload:     msg.Payload,
		Tags:        `["test"]`,
		Antecedents: `[]`,
		Timestamp:   msg.Timestamp,
		Signature:   msg.Signature,
		Provenance:  `[]`,
		ReceivedAt:  time.Now().UnixNano(),
	}
	if _, err := s.AddMessage(rec); err != nil {
		t.Fatalf("storing message: %v", err)
	}
	return rec
}

// startTransportWithSelf starts a transport and sets the self identity.
func startTransportWithSelf(t *testing.T, addr string, s *store.Store, id *identity.Identity) *cfhttp.Transport {
	t.Helper()
	tr := cfhttp.New(addr, s)
	ep := fmt.Sprintf("http://%s", addr)
	tr.SetSelfInfo(id.PublicKeyHex(), ep)
	if err := tr.Start(); err != nil {
		t.Fatalf("starting transport on %s: %v", addr, err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)
	return tr
}

// TestHandlePollImmediateMessages: store 2 messages before poll.
// Expect 200 with both messages; cursor = ReceivedAt of newest.
func TestHandlePollImmediateMessages(t *testing.T) {
	campfireID := "poll-immediate"
	id := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, id.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+40)
	startTransportWithSelf(t, addr, s, id)
	ep := fmt.Sprintf("http://%s", addr)

	// Store two messages before polling.
	rec1 := storeMessageRecord(t, s, campfireID, id)
	time.Sleep(time.Millisecond) // ensure different ReceivedAt
	rec2 := storeMessageRecord(t, s, campfireID, id)

	resp, err := doPoll(ep, campfireID, 0, 1, id)
	if err != nil {
		t.Fatalf("poll request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	cursor := resp.Header.Get("X-Campfire-Cursor")
	if cursor == "" {
		t.Fatal("X-Campfire-Cursor header missing")
	}
	wantCursor := strconv.FormatInt(rec2.ReceivedAt, 10)
	if cursor != wantCursor {
		t.Errorf("cursor = %s, want %s", cursor, wantCursor)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	var msgs []message.Message
	if err := cfencoding.Unmarshal(body, &msgs); err != nil {
		t.Fatalf("decoding CBOR body: %v", err)
	}
	if len(msgs) != 2 {
		t.Errorf("expected 2 messages, got %d", len(msgs))
	}

	// Suppress unused
	_ = rec1
}

// TestHandlePollTimeout: poll with timeout=1, no messages, expect 204 within 2s.
func TestHandlePollTimeout(t *testing.T) {
	campfireID := "poll-timeout"
	id := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, id.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+41)
	startTransportWithSelf(t, addr, s, id)
	ep := fmt.Sprintf("http://%s", addr)

	since := time.Now().UnixNano()

	start := time.Now()
	resp, err := doPoll(ep, campfireID, since, 1, id) // timeout=1s
	if err != nil {
		t.Fatalf("poll request: %v", err)
	}
	defer resp.Body.Close()
	elapsed := time.Since(start)

	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 204, got %d: %s", resp.StatusCode, body)
	}
	if elapsed > 2*time.Second {
		t.Errorf("poll took too long: %v (expected ≤ 2s)", elapsed)
	}

	cursor := resp.Header.Get("X-Campfire-Cursor")
	wantCursor := strconv.FormatInt(since, 10)
	if cursor != wantCursor {
		t.Errorf("204 cursor = %s, want %s", cursor, wantCursor)
	}
}

// TestHandlePollWakeOnDeliver: goroutine blocks on poll (timeout=30),
// concurrent goroutine POSTs /deliver, expect poll 200 within 200ms.
func TestHandlePollWakeOnDeliver(t *testing.T) {
	campfireID := "poll-wake"
	idSender := tempIdentity(t)
	idPoller := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, idPoller.PublicKeyHex())
	addPeerEndpoint(t, s, campfireID, idSender.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+42)
	startTransportWithSelf(t, addr, s, idPoller)
	ep := fmt.Sprintf("http://%s", addr)

	since := time.Now().UnixNano()

	pollDone := make(chan struct{})
	var pollResp *http.Response
	var pollErr error

	go func() {
		pollResp, pollErr = doPoll(ep, campfireID, since, 30, idPoller)
		close(pollDone)
	}()

	// Small delay to let the poll goroutine block.
	time.Sleep(50 * time.Millisecond)

	// POST /deliver to wake the poll.
	msg := newTestMessage(t, idSender)
	if err := cfhttp.Deliver(ep, campfireID, msg, idSender); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	// Poll should return within 200ms.
	select {
	case <-pollDone:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("poll did not return within 200ms after deliver")
	}

	if pollErr != nil {
		t.Fatalf("poll error: %v", pollErr)
	}
	defer pollResp.Body.Close()
	if pollResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(pollResp.Body)
		t.Fatalf("expected 200, got %d: %s", pollResp.StatusCode, body)
	}

	// Verify we got the message.
	body, err := io.ReadAll(pollResp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	var msgs []message.Message
	if err := cfencoding.Unmarshal(body, &msgs); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if len(msgs) != 1 {
		t.Errorf("expected 1 message, got %d", len(msgs))
	}
	if len(msgs) > 0 && msgs[0].ID != msg.ID {
		t.Errorf("message ID mismatch: got %s, want %s", msgs[0].ID, msg.ID)
	}
}

// TestHandlePollUnauthorized: missing headers → 401.
func TestHandlePollUnauthorized(t *testing.T) {
	campfireID := "poll-unauth"
	s := tempStore(t)
	addMembership(t, s, campfireID)

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+43)
	startTransport(t, addr, s)
	ep := fmt.Sprintf("http://%s", addr)

	url := fmt.Sprintf("%s/campfire/%s/poll?since=0&timeout=1", ep, campfireID)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	// No auth headers.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

// TestHandlePollNonMember: valid sig but sender not in peer list → 403.
func TestHandlePollNonMember(t *testing.T) {
	campfireID := "poll-nonmember"
	idMember := tempIdentity(t)
	idStranger := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, idMember.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+44)
	// Use startTransportWithSelf so self key is idMember (not idStranger).
	startTransportWithSelf(t, addr, s, idMember)
	ep := fmt.Sprintf("http://%s", addr)

	// idStranger is not in peer_endpoints and not the local transport self key.
	resp, err := doPoll(ep, campfireID, 0, 1, idStranger)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 403, got %d: %s", resp.StatusCode, body)
	}
}

// TestHandlePollInvalidParams: since="abc" → 400.
func TestHandlePollInvalidParams(t *testing.T) {
	campfireID := "poll-badparams"
	id := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, id.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+45)
	startTransportWithSelf(t, addr, s, id)
	ep := fmt.Sprintf("http://%s", addr)

	url := fmt.Sprintf("%s/campfire/%s/poll?since=abc&timeout=1", ep, campfireID)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	signTestRequest(req, id, []byte{})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400, got %d: %s", resp.StatusCode, body)
	}
}

// TestHandlePollLimitExceeded: exhaust broker limit, next poll returns 503.
func TestHandlePollLimitExceeded(t *testing.T) {
	campfireID := "poll-limit"
	id := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, id.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+46)
	tr := cfhttp.New(addr, s)
	tr.SetSelfInfo(id.PublicKeyHex(), fmt.Sprintf("http://%s", addr))
	tr.SetMaxPollersPerCampfire(2)
	if err := tr.Start(); err != nil {
		t.Fatalf("starting transport: %v", err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)
	ep := fmt.Sprintf("http://%s", addr)

	// Block 2 poll slots (timeout=10s runs in background).
	var wg sync.WaitGroup
	var responses []*http.Response
	var mu sync.Mutex

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := doPoll(ep, campfireID, 0, 10, id)
			if err == nil {
				mu.Lock()
				responses = append(responses, resp)
				mu.Unlock()
			}
		}()
	}

	// Give pollers time to subscribe.
	time.Sleep(100 * time.Millisecond)

	// Third poll should get 503.
	resp, err := doPoll(ep, campfireID, 0, 1, id)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 503, got %d: %s", resp.StatusCode, body)
	}

	// Deliver a message to unblock the waiting goroutines so they finish.
	msg := newTestMessage(t, id)
	cfhttp.Deliver(ep, campfireID, msg, id) //nolint:errcheck

	wg.Wait()
	for _, r := range responses {
		r.Body.Close()
	}
}

// TestHandlePollInvalidTimeoutCapped: timeout > 50 is capped to 50.
// We verify by sending timeout=200 and checking the poll returns (eventually) without error.
// (Mainly ensures the cap doesn't break anything.)
func TestHandlePollInvalidTimeoutCapped(t *testing.T) {
	campfireID := "poll-timeout-cap"
	id := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, id.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+47)
	startTransportWithSelf(t, addr, s, id)
	ep := fmt.Sprintf("http://%s", addr)

	// Store a message so the poll returns immediately (we don't want to wait 50s).
	storeMessageRecord(t, s, campfireID, id)

	resp, err := doPoll(ep, campfireID, 0, 200, id) // 200 > cap of 50, but messages exist
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
}

// TestHandlePollFiltersByReceivedAt is the regression test for workspace-d68.
// A message with a past Timestamp (sender clock skew) but a recent ReceivedAt
// must appear in poll results when the cursor is set before its ReceivedAt.
// Before the fix, the poll used Timestamp for filtering; this caused messages
// from clock-skewed senders to be permanently missed.
func TestHandlePollFiltersByReceivedAt(t *testing.T) {
	campfireID := "poll-receivedat"
	id := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, id.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+48)
	startTransportWithSelf(t, addr, s, id)
	ep := fmt.Sprintf("http://%s", addr)

	now := time.Now().UnixNano()

	// Insert a message with a Timestamp 60 seconds in the past (sender clock is 60s behind)
	// but ReceivedAt is now (the server received it just now).
	msg, err := message.NewMessage(id.PrivateKey, id.PublicKey, []byte("skewed clock"), []string{"test"}, nil)
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}
	skewedRec := store.MessageRecord{
		ID:          msg.ID,
		CampfireID:  campfireID,
		Sender:      id.PublicKeyHex(),
		Payload:     msg.Payload,
		Tags:        `["test"]`,
		Antecedents: `[]`,
		Timestamp:   now - int64(60*time.Second), // 60s in the past (sender clock skew)
		Signature:   msg.Signature,
		Provenance:  `[]`,
		ReceivedAt:  now, // received by server just now
	}
	if _, err := s.AddMessage(skewedRec); err != nil {
		t.Fatalf("storing skewed message: %v", err)
	}

	// Poll with cursor = now-10min (well before ReceivedAt=now). The message
	// should appear because its ReceivedAt > cursor, even though
	// Timestamp = now-60s is also > cursor.
	cursor := now - int64(10*time.Minute)
	resp, err := doPoll(ep, campfireID, cursor, 1, id)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	var msgs []message.Message
	if err := cfencoding.Unmarshal(body, &msgs); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if len(msgs) != 1 {
		t.Errorf("expected 1 message (clock-skewed), got %d", len(msgs))
	}
	if len(msgs) > 0 && msgs[0].ID != skewedRec.ID {
		t.Errorf("wrong message: got %s, want %s", msgs[0].ID, skewedRec.ID)
	}
}

// TestHandlePollMembershipStoreError: ListPeerEndpoints returns an error
// (DB closed before request) — sender is not the self key, so membership
// check falls through to store lookup. Store error → skip loop → isMember
// stays false → 403 (fail-closed, not 500).
func TestHandlePollMembershipStoreError(t *testing.T) {
	campfireID := "poll-store-error"
	idSelf := tempIdentity(t)
	idCaller := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, idCaller.PublicKeyHex())

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+48)
	// Set self key to idSelf so idCaller is not the self key and must be
	// looked up via ListPeerEndpoints.
	startTransportWithSelf(t, addr, s, idSelf)
	ep := fmt.Sprintf("http://%s", addr)

	// Close the store so ListPeerEndpoints returns an error.
	if err := s.Close(); err != nil {
		t.Fatalf("closing store: %v", err)
	}

	resp, err := doPoll(ep, campfireID, 0, 1, idCaller)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	// Fail-closed: store error must yield 403, not 200 or 500.
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 403 (fail-closed on store error), got %d: %s", resp.StatusCode, body)
	}
}

// TestHandlePollNoPeerEndpoints: sender is not the self key and the campfire
// has no peer_endpoints rows. ListPeerEndpoints returns an empty list (no error).
// The loop finds no match → isMember stays false → 403.
func TestHandlePollNoPeerEndpoints(t *testing.T) {
	campfireID := "poll-no-peers"
	idSelf := tempIdentity(t)
	idCaller := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	// Intentionally do NOT call addPeerEndpoint — empty peer list.

	base := portBase()
	addr := fmt.Sprintf("127.0.0.1:%d", base+49)
	startTransportWithSelf(t, addr, s, idSelf)
	ep := fmt.Sprintf("http://%s", addr)

	resp, err := doPoll(ep, campfireID, 0, 1, idCaller)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 403 (no peer endpoints), got %d: %s", resp.StatusCode, body)
	}
}

