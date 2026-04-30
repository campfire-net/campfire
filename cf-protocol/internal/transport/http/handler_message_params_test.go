package http_test

// Tests for invalid query parameter handling in handleSync and handlePoll.
// Coverage: workspace-29n
//   - handleSync: non-integer 'since' returns 400.
//   - handlePoll: non-integer 'timeout' returns 400.
//   - handlePoll: timeout > 50 is capped (no error, returns normally).

import (
	"fmt"
	"io"
	"net/http"
	"testing"
)

// TestHandleSyncInvalidSince verifies that handleSync returns 400 when 'since'
// is a non-integer value (strconv.ParseInt fails).
func TestHandleSyncInvalidSince(t *testing.T) {
	campfireID := "sync-invalid-since"
	id := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, id.PublicKeyHex())

	_tr1 := startTransportWithSelf(t, s, id)
	ep := epOf(_tr1)

	url := fmt.Sprintf("%s/campfire/%s/sync?since=notanumber", ep, campfireID)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	signTestRequest(req, id, []byte{})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400 for non-integer since, got %d: %s", resp.StatusCode, body)
	}
}

// TestHandlePollInvalidTimeout verifies that handlePoll returns 400 when
// 'timeout' is a non-integer value (strconv.Atoi fails).
func TestHandlePollInvalidTimeout(t *testing.T) {
	campfireID := "poll-invalid-timeout"
	id := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, id.PublicKeyHex())

	_tr2 := startTransportWithSelf(t, s, id)
	ep := epOf(_tr2)

	url := fmt.Sprintf("%s/campfire/%s/poll?since=0&timeout=notanumber", ep, campfireID)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	signTestRequest(req, id, []byte{})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400 for non-integer timeout, got %d: %s", resp.StatusCode, body)
	}
}

// TestHandlePollTimeoutCapReturnsNormally verifies that a timeout value above
// the cap (50s) is silently capped and the poll returns without error.
// We pre-store a message so the poll returns immediately rather than waiting 50s.
func TestHandlePollTimeoutCapReturnsNormally(t *testing.T) {
	campfireID := "poll-timeout-cap-normal"
	id := tempIdentity(t)
	s := tempStore(t)
	addMembership(t, s, campfireID)
	addPeerEndpoint(t, s, campfireID, id.PublicKeyHex())

	_tr3 := startTransportWithSelf(t, s, id)
	ep := epOf(_tr3)

	// Pre-store a message so the initial sync returns immediately.
	storeMessageRecord(t, s, campfireID, id)

	// timeout=300 is well above the 50s cap. The cap is enforced server-side;
	// the client should receive 200 with the stored message.
	resp, err := doPoll(ep, campfireID, 0, 300, id)
	if err != nil {
		t.Fatalf("poll request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 (timeout capped, message exists), got %d: %s", resp.StatusCode, body)
	}

	// Cursor header must be present.
	if resp.Header.Get("X-Campfire-Cursor") == "" {
		t.Error("X-Campfire-Cursor header missing")
	}
}
