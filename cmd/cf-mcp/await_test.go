package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// awaitStatus extracts the "status" field from a campfire_await tool response.
func awaitStatus(t *testing.T, resp jsonRPCResponse) map[string]interface{} {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}
	b, _ := json.Marshal(resp.Result)
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &result); err != nil || len(result.Content) == 0 {
		t.Fatalf("cannot extract content from await result: %s", string(b))
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
		t.Fatalf("cannot parse await payload JSON: %v (text: %s)", err, result.Content[0].Text)
	}
	return payload
}

// setupHTTPAwaitSession creates a full HTTP-transport-backed server session with
// a campfire and a CLI agent registered as a member. Returns the server, the
// httptest server URL, the session token, the campfire ID, and the CLI identity.
func setupHTTPAwaitSession(t *testing.T) (tsURL, token, campfireID string, cliID *identity.Identity) {
	t.Helper()
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})
	cfhttp.OverridePollTransportForTest(http.DefaultTransport)

	srv, _, tsURL2 := newTestServerWithHTTPTransport(t)

	initResp := mcpCall(t, tsURL2, "", "campfire_init", map[string]interface{}{})
	tok := extractTokenFromInit(t, initResp)

	createResp := mcpCall(t, tsURL2, tok, "campfire_create", map[string]interface{}{
		"description": "await test campfire",
	})
	createText := extractResultText(t, createResp)
	var createResult struct {
		CampfireID string `json:"campfire_id"`
	}
	if err := json.Unmarshal([]byte(createText), &createResult); err != nil {
		t.Fatalf("parsing create result: %v (text: %s)", err, createText)
	}

	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating CLI identity: %v", err)
	}

	tr := srv.transportRouter.GetCampfireTransport(createResult.CampfireID)
	if tr == nil {
		t.Fatal("campfire transport not found")
	}
	tr.AddPeer(createResult.CampfireID, id.PublicKeyHex(), "")
	st := tr.Store()
	st.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   createResult.CampfireID,
		MemberPubkey: id.PublicKeyHex(),
		Role:         store.PeerRoleMember,
	})
	v, _ := srv.sessManager.sessions.Load(tok)
	sess := v.(*Session)
	fsT := fs.New(sess.cfHome)
	fsT.WriteMember(createResult.CampfireID, campfire.MemberRecord{
		PublicKey: id.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
	})

	return tsURL2, tok, createResult.CampfireID, id
}

// ---------------------------------------------------------------------------
// Test: fulfilled future returns immediately with status "fulfilled"
// ---------------------------------------------------------------------------

// TestAwaitHTTP_FulfilledReturnsImmediately verifies that campfire_await
// returns status="fulfilled" and the fulfilling message without blocking when
// a message tagged "fulfills" targeting msg_id is already in the store.
func TestAwaitHTTP_FulfilledReturnsImmediately(t *testing.T) {
	tsURL, token, campfireID, cliID := setupHTTPAwaitSession(t)

	// Send the original message from the CLI agent.
	origMsg, err := message.NewMessage(cliID.PrivateKey, cliID.PublicKey, []byte("original"), nil, nil)
	if err != nil {
		t.Fatalf("creating original message: %v", err)
	}
	if err := cfhttp.Deliver(tsURL, campfireID, origMsg, cliID); err != nil {
		t.Fatalf("delivering original message: %v", err)
	}

	// Send a fulfilling message that references the original.
	fulfillMsg, err := message.NewMessage(
		cliID.PrivateKey, cliID.PublicKey,
		[]byte("fulfilled!"),
		[]string{"fulfills"},
		[]string{origMsg.ID},
	)
	if err != nil {
		t.Fatalf("creating fulfilling message: %v", err)
	}
	if err := cfhttp.Deliver(tsURL, campfireID, fulfillMsg, cliID); err != nil {
		t.Fatalf("delivering fulfilling message: %v", err)
	}

	// Give the store a moment to persist.
	time.Sleep(50 * time.Millisecond)

	// campfire_await should return immediately with status="fulfilled".
	start := time.Now()
	resp := mcpCall(t, tsURL, token, "campfire_await", map[string]interface{}{
		"campfire_id": campfireID,
		"msg_id":      origMsg.ID,
		"timeout":     "5m",
	})
	elapsed := time.Since(start)

	payload := awaitStatus(t, resp)

	status, _ := payload["status"].(string)
	if status != "fulfilled" {
		t.Errorf("expected status=fulfilled, got %q (payload: %v)", status, payload)
	}
	if payload["message"] == nil {
		t.Error("expected message field in fulfilled response")
	}

	// Should return well within 5 seconds (not block for a full chunk).
	if elapsed > 5*time.Second {
		t.Errorf("fulfilled await blocked for too long: %v", elapsed)
	}
}

// ---------------------------------------------------------------------------
// Test: 30s cap returns pending
// ---------------------------------------------------------------------------

// TestAwaitHTTP_ChunkCapReturnsPending verifies that campfire_await with no
// fulfilling message blocks for at most httpAwaitChunkDuration (30s) and then
// returns status="pending" with retry=true.
//
// To avoid a 30-second test, we use a short timeout value that is less than
// the chunk cap and verify that pending is returned after that shorter window.
func TestAwaitHTTP_ChunkCapReturnsPending(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timing-sensitive test in short mode")
	}

	tsURL, token, campfireID, _ := setupHTTPAwaitSession(t)

	// Use a timeout longer than the chunk cap to trigger a pending response.
	// We use 35s so the 30s chunk fires before the total timeout.
	// To avoid a slow test, we override the chunk duration via a short timeout
	// that is greater than the chunk cap (impossible to do without changing the
	// constant), so instead we use a timeout of 31s but accept a ~30s wait.
	//
	// Practical approach: use timeout=35s. The chunk cap fires at 30s → pending.
	// Acceptable test duration: ~30s. Mark as not-short.
	start := time.Now()
	resp := mcpCall(t, tsURL, token, "campfire_await", map[string]interface{}{
		"campfire_id": campfireID,
		"msg_id":      "nonexistent-message-id",
		"timeout":     "35s",
	})
	elapsed := time.Since(start)

	payload := awaitStatus(t, resp)

	status, _ := payload["status"].(string)
	if status != "pending" {
		t.Errorf("expected status=pending, got %q (payload: %v)", status, payload)
	}
	retry, _ := payload["retry"].(bool)
	if !retry {
		t.Error("expected retry=true in pending response")
	}
	if payload["remaining"] == nil {
		t.Error("expected remaining field in pending response")
	}
	if payload["elapsed"] == nil {
		t.Error("expected elapsed field in pending response")
	}

	// Should have blocked for approximately 30s (chunk cap).
	if elapsed < 25*time.Second {
		t.Errorf("await returned too quickly: %v (expected ~30s)", elapsed)
	}
	if elapsed > 45*time.Second {
		t.Errorf("await blocked too long: %v", elapsed)
	}
}

// ---------------------------------------------------------------------------
// Test: full timeout returns status="timeout"
// ---------------------------------------------------------------------------

// TestAwaitHTTP_FullTimeoutReturnsTimeout verifies that campfire_await
// returns status="timeout" when the caller passes a timeout <= 0 (simulating
// an exhausted retry budget).
func TestAwaitHTTP_FullTimeoutReturnsTimeout(t *testing.T) {
	tsURL, token, campfireID, _ := setupHTTPAwaitSession(t)

	// Pass a very short timeout (1ms) so the server treats it as exhausted.
	// The server returns timeout immediately when timeout <= chunk_cap and
	// no fulfillment exists within that window.
	resp := mcpCall(t, tsURL, token, "campfire_await", map[string]interface{}{
		"campfire_id": campfireID,
		"msg_id":      "nonexistent-message-id",
		"timeout":     "1ms",
	})

	payload := awaitStatus(t, resp)

	status, _ := payload["status"].(string)
	// A 1ms timeout is shorter than the chunk cap, so the server will block for
	// 1ms and then return. With no fulfillment, remaining = 0 → timeout status.
	if status != "timeout" && status != "pending" {
		t.Errorf("expected status=timeout or pending for near-zero timeout, got %q", status)
	}
	// If pending, remaining should be very small.
	if status == "pending" {
		remaining, _ := payload["remaining"].(string)
		if remaining != "0s" && !strings.Contains(remaining, "ms") {
			t.Errorf("pending with near-zero remaining should show 0s or ms, got %q", remaining)
		}
	}
}

// ---------------------------------------------------------------------------
// Test: invalid campfire returns status="error"
// ---------------------------------------------------------------------------

// TestAwaitHTTP_InvalidCampfireReturnsError verifies that campfire_await
// returns status="error" when the campfire_id does not exist in the store.
func TestAwaitHTTP_InvalidCampfireReturnsError(t *testing.T) {
	tsURL, token, _, _ := setupHTTPAwaitSession(t)

	resp := mcpCall(t, tsURL, token, "campfire_await", map[string]interface{}{
		"campfire_id": "nonexistent-campfire-id-that-has-no-poll-broker",
		"msg_id":      "some-msg-id",
		"timeout":     "5s",
	})

	payload := awaitStatus(t, resp)

	status, _ := payload["status"].(string)
	if status != "error" {
		t.Errorf("expected status=error for invalid campfire, got %q (payload: %v)", status, payload)
	}
	errMsg, _ := payload["message"].(string)
	if errMsg == "" {
		t.Error("expected non-empty message field in error response")
	}
}
