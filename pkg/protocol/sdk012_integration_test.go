package protocol_test

// Integration tests for SDK 0.12 API — campfire-agent-5sr.
//
// All tests use real filesystem transport and real SQLite store. No mocks.
// Each test exercises a distinct slice of the SDK 0.12 API surface.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/protocol"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// ---------------------------------------------------------------------------
// Test 1: Create with FilesystemTransport → Send → Get → verify Message fields
// ---------------------------------------------------------------------------

// TestSDK012_CreateSendGet creates a campfire via protocol.Init + client.Create,
// sends a message, retrieves it with client.Get, and verifies all protocol.Message
// fields are populated correctly.
func TestSDK012_CreateSendGet(t *testing.T) {
	configDir := t.TempDir()
	transportDir := t.TempDir()
	beaconDir := t.TempDir()

	client, err := protocol.Init(configDir)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	result, err := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir: beaconDir,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if result.CampfireID == "" {
		t.Fatal("Create returned empty CampfireID")
	}

	sent, err := client.Send(protocol.SendRequest{
		CampfireID: result.CampfireID,
		Payload:    []byte("sdk012 get test"),
		Tags:       []string{"status"},
		Instance:   "integrator",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if sent == nil {
		t.Fatal("Send returned nil")
	}

	got, err := client.Get(sent.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("Get returned nil for existing message")
	}

	// Verify protocol.Message fields.
	if got.ID != sent.ID {
		t.Errorf("ID mismatch: got %q, want %q", got.ID, sent.ID)
	}
	if string(got.Payload) != "sdk012 get test" {
		t.Errorf("Payload mismatch: got %q, want %q", got.Payload, "sdk012 get test")
	}
	if len(got.Tags) != 1 || got.Tags[0] != "status" {
		t.Errorf("Tags mismatch: got %v, want [status]", got.Tags)
	}
	if got.Instance != "integrator" {
		t.Errorf("Instance mismatch: got %q, want %q", got.Instance, "integrator")
	}
	if got.Sender == "" {
		t.Error("Sender is empty")
	}
	if got.Timestamp == 0 {
		t.Error("Timestamp is zero")
	}
	// Sender hex must match client identity (protocol.Message.Sender is already hex string).
	wantSender := client.PublicKeyHex()
	if got.Sender != wantSender {
		t.Errorf("Sender hex mismatch: got %q, want %q", got.Sender, wantSender)
	}
}

// ---------------------------------------------------------------------------
// Test 2: Send → Read → verify ReadResult.Messages is []protocol.Message
// ---------------------------------------------------------------------------

// TestSDK012_SendReadMessages sends two messages then calls Read and verifies
// ReadResult.Messages contains []protocol.Message with correct fields.
func TestSDK012_SendReadMessages(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	client := protocol.New(s, agentID)

	payloads := []string{"read-msg-alpha", "read-msg-beta"}
	var sentIDs []string
	for _, p := range payloads {
		msg, err := client.Send(protocol.SendRequest{
			CampfireID: campfireID,
			Payload:    []byte(p),
			Tags:       []string{"finding"},
		})
		if err != nil {
			t.Fatalf("Send %q: %v", p, err)
		}
		sentIDs = append(sentIDs, msg.ID)
	}

	result, err := client.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		IncludeCompacted: true,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	// ReadResult.Messages must be []protocol.Message (not MessageRecord).
	if len(result.Messages) < 2 {
		t.Fatalf("expected >= 2 messages, got %d", len(result.Messages))
	}

	// Build index by ID.
	byID := map[string]*protocol.Message{}
	for i := range result.Messages {
		m := &result.Messages[i]
		byID[m.ID] = m
	}

	for i, id := range sentIDs {
		m, ok := byID[id]
		if !ok {
			t.Errorf("message %q not found in Read result", id)
			continue
		}
		if string(m.Payload) != payloads[i] {
			t.Errorf("message %d payload mismatch: got %q, want %q", i, m.Payload, payloads[i])
		}
		if len(m.Tags) != 1 || m.Tags[0] != "finding" {
			t.Errorf("message %d tags mismatch: got %v, want [finding]", i, m.Tags)
		}
		if m.Sender == "" {
			t.Errorf("message %d Sender is empty", i)
		}
		if m.Timestamp == 0 {
			t.Errorf("message %d Timestamp is zero", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Test 3: Send with "future" tag → Subscribe → verify delivery as protocol.Message
// ---------------------------------------------------------------------------

// TestSDK012_FutureTagSubscribeDelivery sends a message with the "future" tag and
// verifies it is delivered on a Subscribe channel as a protocol.Message with
// correct fields including the future tag.
func TestSDK012_FutureTagSubscribeDelivery(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	client := protocol.New(s, agentID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := client.Subscribe(ctx, protocol.SubscribeRequest{
		CampfireID:   campfireID,
		PollInterval: 50 * time.Millisecond,
	})

	// Let subscription start.
	time.Sleep(100 * time.Millisecond)

	sent, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("future: need a ruling"),
		Tags:       []string{"future"},
		Instance:   "orchestrator",
	})
	if err != nil {
		t.Fatalf("Send with future tag: %v", err)
	}

	select {
	case msg, ok := <-sub.Messages():
		if !ok {
			t.Fatal("channel closed before delivering future message")
		}
		if msg.ID != sent.ID {
			t.Errorf("ID mismatch: got %q, want %q", msg.ID, sent.ID)
		}
		// Verify it's delivered as protocol.Message with correct fields.
		if string(msg.Payload) != "future: need a ruling" {
			t.Errorf("payload mismatch: got %q", msg.Payload)
		}
		foundFuture := false
		for _, tg := range msg.Tags {
			if tg == "future" {
				foundFuture = true
			}
		}
		if !foundFuture {
			t.Errorf("expected 'future' tag, got %v", msg.Tags)
		}
		if msg.Instance != "orchestrator" {
			t.Errorf("Instance mismatch: got %q, want orchestrator", msg.Instance)
		}
		if msg.Sender == "" {
			t.Error("delivered message Sender is empty")
		}
		if msg.Timestamp == 0 {
			t.Error("delivered message Timestamp is zero")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: future-tagged message not delivered via Subscribe")
	}
}

// ---------------------------------------------------------------------------
// Test 4: Await with fulfillment → verify returns *protocol.Message
// ---------------------------------------------------------------------------

// TestSDK012_AwaitFulfillment sends a "future" message then a "fulfills" reply
// and verifies that Await returns a non-nil *protocol.Message pointing to the
// fulfillment with correct fields.
func TestSDK012_AwaitFulfillment(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)

	client := protocol.New(s, agentID)

	// Send the future message.
	futureMsg, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("escalation: need ruling"),
		Tags:       []string{"future"},
	})
	if err != nil {
		t.Fatalf("Send future: %v", err)
	}

	// Launch Await in a goroutine.
	type awaitResult struct {
		msg *protocol.Message
		err error
	}
	ch := make(chan awaitResult, 1)
	go func() {
		msg, err := client.Await(protocol.AwaitRequest{
			CampfireID:   campfireID,
			TargetMsgID:  futureMsg.ID,
			Timeout:      10 * time.Second,
			PollInterval: 100 * time.Millisecond,
		})
		ch <- awaitResult{msg, err}
	}()

	// After a short delay, send the fulfillment.
	time.Sleep(200 * time.Millisecond)
	fulfillMsg, err := client.Send(protocol.SendRequest{
		CampfireID:  campfireID,
		Payload:     []byte("ruling: proceed"),
		Tags:        []string{"fulfills"},
		Antecedents: []string{futureMsg.ID},
	})
	if err != nil {
		t.Fatalf("Send fulfillment: %v", err)
	}

	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("Await: unexpected error: %v", r.err)
		}
		if r.msg == nil {
			t.Fatal("Await: expected *protocol.Message, got nil")
		}
		// Verify it's the fulfillment message.
		if r.msg.ID != fulfillMsg.ID {
			t.Errorf("Await returned wrong message: got %q, want %q", r.msg.ID, fulfillMsg.ID)
		}
		// Verify protocol.Message fields on the returned value.
		if string(r.msg.Payload) != "ruling: proceed" {
			t.Errorf("Await payload mismatch: got %q", r.msg.Payload)
		}
		foundFulfills := false
		for _, tg := range r.msg.Tags {
			if tg == "fulfills" {
				foundFulfills = true
			}
		}
		if !foundFulfills {
			t.Errorf("expected 'fulfills' tag in Await result, got %v", r.msg.Tags)
		}
		if len(r.msg.Antecedents) != 1 || r.msg.Antecedents[0] != futureMsg.ID {
			t.Errorf("Await result antecedents mismatch: got %v, want [%s]", r.msg.Antecedents, futureMsg.ID)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("Await: timed out waiting for result")
	}
}

// ---------------------------------------------------------------------------
// Test 5: Create with P2PHTTPTransport → verify transport wiring
// ---------------------------------------------------------------------------

// TestSDK012_CreateP2PHTTPTransport creates a campfire with P2PHTTPTransport and
// an in-process HTTP server. Verifies that Send dispatches through the P2P
// transport and delivers the CBOR message to the peer endpoint.
func TestSDK012_CreateP2PHTTPTransport(t *testing.T) {
	// In-process peer server: accepts delivery, records the call.
	type delivery struct {
		body []byte
	}
	deliveryCh := make(chan delivery, 1)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		deliveryCh <- delivery{body}
		w.WriteHeader(http.StatusOK)
	}))
	defer peer.Close()

	agentID, s, tmpDir := setupTestEnv(t)

	// Use the existing setupP2PHTTPCampfire helper from send_p2p_test.go.
	campfireID := setupP2PHTTPCampfire(t, agentID, s, tmpDir, peer.URL)

	client := protocol.New(s, agentID)

	msg, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("p2p transport wiring test"),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("Send via P2P transport: %v", err)
	}
	if msg == nil {
		t.Fatal("Send returned nil")
	}
	if msg.ID == "" {
		t.Fatal("message ID is empty")
	}

	// Verify the transport delivered to the peer.
	select {
	case d := <-deliveryCh:
		if len(d.body) == 0 {
			t.Error("peer received empty delivery body")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout: peer did not receive delivery from P2P transport")
	}

	// Verify message is in local store.
	msgs, err := s.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	found := false
	for _, m := range msgs {
		if m.ID == msg.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("message %s not in local store after P2P send", msg.ID)
	}

	// Verify provenance hop is valid.
	if len(msg.Provenance) != 1 {
		t.Fatalf("expected 1 provenance hop, got %d", len(msg.Provenance))
	}
}

// ---------------------------------------------------------------------------
// Test 6: Get/GetByPrefix error paths
// ---------------------------------------------------------------------------

// TestSDK012_GetErrorPaths verifies:
//   - Get with nonexistent ID returns nil, nil
//   - GetByPrefix with ambiguous prefix returns an error containing "ambiguous"
//   - GetByPrefix with nonexistent prefix returns nil, nil
func TestSDK012_GetErrorPaths(t *testing.T) {
	agentID, s, transportDir := setupTestEnv(t)
	campfireID := setupFilesystemCampfire(t, agentID, s, transportDir, campfire.RoleFull)
	client := protocol.New(s, agentID)

	t.Run("GetNonexistentID", func(t *testing.T) {
		got, err := client.Get("deadbeefdeadbeefdeadbeef00000000nonexistent")
		if err != nil {
			t.Fatalf("expected nil error for nonexistent ID, got %v", err)
		}
		if got != nil {
			t.Fatalf("expected nil message for nonexistent ID, got %+v", got)
		}
	})

	t.Run("GetByPrefixNonexistent", func(t *testing.T) {
		got, err := client.GetByPrefix("zzzzzzzznonexistentprefix")
		if err != nil {
			t.Fatalf("expected nil error for nonexistent prefix, got %v", err)
		}
		if got != nil {
			t.Fatalf("expected nil for nonexistent prefix, got %+v", got)
		}
	})

	t.Run("GetByPrefixAmbiguous", func(t *testing.T) {
		// Insert two messages with the same prefix via the store directly.
		const sharedPrefix = "ff11cc22"
		idA := sharedPrefix + "0000-0000-0000-000000000001"
		idB := sharedPrefix + "0000-0000-0000-000000000002"
		for _, id := range []string{idA, idB} {
			_, err := s.AddMessage(storeMessageRecord(id, campfireID))
			if err != nil {
				t.Fatalf("AddMessage(%s): %v", id, err)
			}
		}
		_, err := client.GetByPrefix(sharedPrefix)
		if err == nil {
			t.Fatal("expected ambiguity error, got nil")
		}
		if !strings.Contains(err.Error(), "ambiguous") {
			t.Errorf("expected 'ambiguous' in error, got %q", err.Error())
		}
	})
}

// ---------------------------------------------------------------------------
// Test 7: PublicKeyHex returns correct hex string matching identity
// ---------------------------------------------------------------------------

// TestSDK012_PublicKeyHex verifies that client.PublicKeyHex() returns a valid
// 64-character hex string that matches the identity used to create the client.
func TestSDK012_PublicKeyHex(t *testing.T) {
	configDir := t.TempDir()

	client, err := protocol.Init(configDir)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	hex := client.PublicKeyHex()
	if hex == "" {
		t.Fatal("PublicKeyHex returned empty string")
	}
	// Ed25519 public key is 32 bytes = 64 hex chars.
	if len(hex) != 64 {
		t.Errorf("PublicKeyHex length = %d, want 64", len(hex))
	}
	// Must be lowercase hex.
	for _, c := range hex {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("PublicKeyHex contains non-hex char %q in %q", c, hex)
			break
		}
	}
	// Must match the identity's own hex.
	identityHex := client.ClientIdentity().PublicKeyHex()
	if hex != identityHex {
		t.Errorf("PublicKeyHex() = %q, ClientIdentity().PublicKeyHex() = %q — mismatch", hex, identityHex)
	}
}

// ---------------------------------------------------------------------------
// Ensure cfhttp SSRF-safe client is overridden so P2P tests can reach loopback.
// The init() in send_p2p_test.go already does this, but we declare it here for
// clarity. Since both are in the same package test binary, only one init runs.
// We rely on send_p2p_test.go's init; no duplicate needed here.
// ---------------------------------------------------------------------------

// Verify the override is in place by referencing cfhttp to avoid unused import.
var _ = cfhttp.OverrideHTTPClientForTest
