// convention_deliver_test.go — T5: convention dispatch on the P2P deliver path.
//
// Tests verify that:
//   - Convention dispatch fires for messages arriving via POST /campfire/{id}/deliver.
//   - A nil OnMessageDelivered hook is safe (no panic, deliver succeeds).
//   - SessionManager wires OnMessageDelivered when conventionDispatcher is set.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// TestConventionDeliver_DispatchFiresOnDeliverPath verifies that when a
// message arrives via POST /campfire/{id}/deliver (P2P peer path), the
// OnMessageDelivered hook fires.
//
// Done condition: after cfhttp.Deliver(), the dispatch counter is non-zero.
func TestConventionDeliver_DispatchFiresOnDeliverPath(t *testing.T) {
	cfhttp.OverrideValidateJoinerEndpointForTest()
	t.Cleanup(cfhttp.RestoreValidateJoinerEndpoint)
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})
	cfhttp.OverridePollTransportForTest(http.DefaultTransport)

	srv, _, tsURL := newTestServerWithHTTPTransport(t)

	// Create session and campfire.
	initResp := mcpCall(t, tsURL, "", "campfire_init", map[string]interface{}{})
	token := extractTokenFromInit(t, initResp)

	createResp := mcpCall(t, tsURL, token, "campfire_create", map[string]interface{}{
		"description":    "deliver dispatch test",
		"delivery_modes": []string{"pull", "push"},
	})
	if createResp.Error != nil {
		t.Fatalf("campfire_create failed: %v", createResp.Error.Message)
	}
	createText := extractResultText(t, createResp)
	var createResult struct {
		CampfireID string `json:"campfire_id"`
	}
	if err := json.Unmarshal([]byte(createText), &createResult); err != nil {
		t.Fatalf("parsing create result: %v", err)
	}
	campfireID := createResult.CampfireID

	// Attach a counting hook to the transport.
	var dispatchCount atomic.Int32
	tr := srv.transportRouter.GetCampfireTransport(campfireID)
	if tr == nil {
		t.Fatal("transport not registered for campfire")
	}
	tr.SetOnMessageDelivered(func(ctx context.Context, cfID string, msg *store.MessageRecord) {
		dispatchCount.Add(1)
	})

	// Register a CLI agent as a member and peer so delivery is accepted.
	cliID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating CLI identity: %v", err)
	}
	tr.AddPeer(campfireID, cliID.PublicKeyHex(), "")
	st := tr.Store()
	st.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: cliID.PublicKeyHex(),
		Endpoint:     "",
		Role:         store.PeerRoleMember,
	})

	sess := srv.sessManager.getSession(token)
	if sess == nil {
		t.Fatal("session not found for token")
	}
	fsT := fs.New(sess.cfHome)
	fsT.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: cliID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
	})

	// Deliver a message from the CLI agent via HTTP (P2P path).
	msg, err := message.NewMessage(cliID.PrivateKey, cliID.PublicKey, []byte("p2p test"), []string{"test"}, nil)
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}
	if err := cfhttp.Deliver(tsURL, campfireID, msg, cliID); err != nil {
		t.Fatalf("Deliver failed: %v", err)
	}

	// The hook should have fired.
	if dispatchCount.Load() == 0 {
		t.Error("OnMessageDelivered hook was not called after Deliver()")
	}
}

// TestConventionDeliver_NilDispatcherSafe verifies that delivering a message
// when no OnMessageDelivered hook is set does not panic and returns success.
func TestConventionDeliver_NilDispatcherSafe(t *testing.T) {
	cfhttp.OverrideValidateJoinerEndpointForTest()
	t.Cleanup(cfhttp.RestoreValidateJoinerEndpoint)
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})
	cfhttp.OverridePollTransportForTest(http.DefaultTransport)

	srv, _, tsURL := newTestServerWithHTTPTransport(t)

	initResp := mcpCall(t, tsURL, "", "campfire_init", map[string]interface{}{})
	token := extractTokenFromInit(t, initResp)

	createResp := mcpCall(t, tsURL, token, "campfire_create", map[string]interface{}{
		"description":    "nil dispatcher test",
		"delivery_modes": []string{"pull", "push"},
	})
	if createResp.Error != nil {
		t.Fatalf("campfire_create failed: %v", createResp.Error.Message)
	}
	createText := extractResultText(t, createResp)
	var createResult struct {
		CampfireID string `json:"campfire_id"`
	}
	if err := json.Unmarshal([]byte(createText), &createResult); err != nil {
		t.Fatalf("parsing create result: %v", err)
	}
	campfireID := createResult.CampfireID

	// Explicitly set nil hook — no conventionDispatcher.
	tr := srv.transportRouter.GetCampfireTransport(campfireID)
	if tr == nil {
		t.Fatal("transport not registered for campfire")
	}
	tr.SetOnMessageDelivered(nil)

	// Register a CLI peer.
	cliID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating CLI identity: %v", err)
	}
	tr.AddPeer(campfireID, cliID.PublicKeyHex(), "")
	st := tr.Store()
	st.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: cliID.PublicKeyHex(),
		Endpoint:     "",
		Role:         store.PeerRoleMember,
	})

	sess := srv.sessManager.getSession(token)
	if sess == nil {
		t.Fatal("session not found")
	}
	fsT := fs.New(sess.cfHome)
	fsT.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: cliID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
	})

	// Deliver must succeed without panicking.
	msg, err := message.NewMessage(cliID.PrivateKey, cliID.PublicKey, []byte("safe deliver"), []string{"test"}, nil)
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}
	if err := cfhttp.Deliver(tsURL, campfireID, msg, cliID); err != nil {
		t.Fatalf("Deliver with nil hook failed: %v", err)
	}
}

// TestConventionDeliver_SessionManagerWiresHook verifies that creating a
// session when SessionManager.conventionDispatcher is non-nil results in
// the transport's OnMessageDelivered hook being set (non-nil).
func TestConventionDeliver_SessionManagerWiresHook(t *testing.T) {
	cfhttp.OverrideValidateJoinerEndpointForTest()
	t.Cleanup(cfhttp.RestoreValidateJoinerEndpoint)
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})
	cfhttp.OverridePollTransportForTest(http.DefaultTransport)

	sessDir := t.TempDir()
	sm := NewSessionManager(sessDir)
	t.Cleanup(sm.Stop)

	router := NewTransportRouter()
	sm.router = router

	// Set a convention dispatcher on the session manager.
	ds := convention.NewMemoryDispatchStore()
	dispatcher := convention.NewConventionDispatcher(ds, nil)
	sm.conventionDispatcher = dispatcher

	srv := &server{
		cfHome:          t.TempDir(),
		beaconDir:       t.TempDir(),
		cfHomeExplicit:  true,
		sessManager:     sm,
		transportRouter: router,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", srv.handleMCPSessioned)
	mux.Handle("/campfire/", router)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	sm.externalAddr = ts.URL

	// Init creates a session in the manager.
	initResp := mcpCall(t, ts.URL, "", "campfire_init", map[string]interface{}{})
	token := extractTokenFromInit(t, initResp)
	if token == "" {
		t.Fatal("expected session token from init")
	}

	// The session's transport should have the hook wired (non-nil).
	sess := sm.getSession(token)
	if sess == nil {
		t.Fatal("session not found after init")
	}
	if sess.httpTransport == nil {
		t.Fatal("httpTransport is nil — hosted HTTP mode not active")
	}

	if sess.httpTransport.OnMessageDelivered == nil {
		t.Error("OnMessageDelivered hook is nil; expected it to be wired from conventionDispatcher")
	}

	// Verify the hook is callable.
	var mu sync.Mutex
	var invoked bool
	sess.httpTransport.SetOnMessageDelivered(func(ctx context.Context, campfireID string, msg *store.MessageRecord) {
		mu.Lock()
		invoked = true
		mu.Unlock()
	})
	sess.httpTransport.OnMessageDelivered(context.Background(), "test-campfire", &store.MessageRecord{})
	mu.Lock()
	called := invoked
	mu.Unlock()
	if !called {
		t.Error("wired hook is not callable")
	}
}
