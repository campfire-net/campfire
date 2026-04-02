package convention_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// dispatcherTestEnv provides a real FS-backed campfire environment for dispatcher tests.
type dispatcherTestEnv struct {
	serverClient *protocol.Client
	callerClient *protocol.Client
	campfireID   string
	serverID     *identity.Identity
}

// setupDispatcherTestEnv creates a two-member filesystem campfire.
func setupDispatcherTestEnv(t *testing.T) *dispatcherTestEnv {
	t.Helper()

	storeDir := t.TempDir()
	transportDir := t.TempDir()

	serverID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating server identity: %v", err)
	}
	callerID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating caller identity: %v", err)
	}
	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	campfireID := cfID.PublicKeyHex()

	cfDir := filepath.Join(transportDir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	state := &campfire.CampfireState{
		PublicKey:             cfID.PublicKey,
		PrivateKey:            cfID.PrivateKey,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		CreatedAt:             time.Now().UnixNano(),
	}
	stateData, err := cfencoding.Marshal(state)
	if err != nil {
		t.Fatalf("marshalling campfire state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfDir, "campfire.cbor"), stateData, 0644); err != nil {
		t.Fatalf("writing campfire state: %v", err)
	}

	tr := fs.New(transportDir)
	for _, id := range []*identity.Identity{serverID, callerID} {
		if err := tr.WriteMember(campfireID, campfire.MemberRecord{
			PublicKey: id.PublicKey,
			JoinedAt:  time.Now().UnixNano(),
			Role:      campfire.RoleFull,
		}); err != nil {
			t.Fatalf("writing member: %v", err)
		}
	}

	membership := store.Membership{
		CampfireID:    campfireID,
		TransportDir:  tr.CampfireDir(campfireID),
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	}

	serverStore, err := store.Open(filepath.Join(storeDir, "server.db"))
	if err != nil {
		t.Fatalf("opening server store: %v", err)
	}
	t.Cleanup(func() { serverStore.Close() })
	if err := serverStore.AddMembership(membership); err != nil {
		t.Fatalf("server AddMembership: %v", err)
	}

	callerStore, err := store.Open(filepath.Join(storeDir, "caller.db"))
	if err != nil {
		t.Fatalf("opening caller store: %v", err)
	}
	t.Cleanup(func() { callerStore.Close() })
	if err := callerStore.AddMembership(membership); err != nil {
		t.Fatalf("caller AddMembership: %v", err)
	}

	return &dispatcherTestEnv{
		serverClient: protocol.New(serverStore, serverID),
		callerClient: protocol.New(callerStore, callerID),
		campfireID:   campfireID,
		serverID:     serverID,
	}
}

// makeConventionMsg builds a store.MessageRecord that looks like a convention
// operation invocation sent by the caller.
func makeConventionMsg(t *testing.T, env *dispatcherTestEnv, conventionName, operation string, args map[string]any) *store.MessageRecord {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"convention": conventionName,
		"version":    "0.1",
		"operation":  operation,
		"args":       args,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	tag := conventionName + ":" + operation
	return &store.MessageRecord{
		ID:        "msg-" + conventionName + "-" + operation,
		CampfireID: env.campfireID,
		Sender:    "aabbcc",
		Payload:   payload,
		Tags:      []string{tag},
		Timestamp: time.Now().UnixNano(),
	}
}

// waitForDispatch polls the dispatch store until a message reaches a terminal status
// or the timeout elapses.
func waitForDispatch(t *testing.T, ds *convention.MemoryDispatchStore, campfireID, msgID string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := ds.GetDispatchStatus(context.Background(), campfireID, msgID)
		if err != nil {
			t.Fatalf("GetDispatchStatus: %v", err)
		}
		if status == "fulfilled" || status == "failed" {
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for dispatch status for msg %s", msgID)
	return ""
}

// ---- Tag matching tests ----

func TestDispatcher_TagMatching_NoConventionTag(t *testing.T) {
	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)

	// Register a handler so we can verify it's NOT called.
	called := false
	d.RegisterTier1Handler("cf1", "myconv", "myop", nil, func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		called = true
		return nil, nil
	}, "server1", "",
	)

	msg := &store.MessageRecord{
		ID:        "no-tag-msg",
		CampfireID: "cf1",
		Sender:    "aabb",
		Payload:   []byte(`{"convention":"myconv","operation":"myop"}`),
		// Tags contain no convention invocation tag — no "name:op" pattern present.
		Tags:      []string{"status", "tag", "just-a-tag"},
		Timestamp: time.Now().UnixNano(),
	}

	// No tag matching the "convention:operation" invocation pattern → Dispatch returns false.
	dispatched := d.Dispatch(context.Background(), "cf1", msg)
	if dispatched {
		t.Fatal("expected Dispatch to return false for message without convention invocation tag")
	}
	if called {
		t.Fatal("handler should not be called")
	}
}

func TestDispatcher_TagMatching_WrongConvention(t *testing.T) {
	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)

	d.RegisterTier1Handler("cf1", "myconv", "myop", nil, func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		return nil, nil
	}, "server1", "",
	)

	// Message is for a different convention.
	msg := &store.MessageRecord{
		ID:        "wrong-conv-msg",
		CampfireID: "cf1",
		Sender:    "aabb",
		Payload:   []byte(`{"convention":"otherconv","operation":"myop"}`),
		Tags:      []string{"otherconv:myop"},
		Timestamp: time.Now().UnixNano(),
	}

	// Should return false — no handler registered for (cf1, otherconv, myop).
	dispatched := d.Dispatch(context.Background(), "cf1", msg)
	if dispatched {
		t.Fatal("expected Dispatch to return false for unregistered convention")
	}
}

func TestDispatcher_TagMatching_ConventionOperationDeclarationTag_Ignored(t *testing.T) {
	// The reserved tag "convention:operation" (declaration tag) must NOT trigger dispatch.
	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)

	d.RegisterTier1Handler("cf1", "convention", "operation", nil, func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		return nil, nil
	}, "server1", "",
	)

	msg := &store.MessageRecord{
		ID:        "decl-tag-msg",
		CampfireID: "cf1",
		Sender:    "aabb",
		Payload:   []byte(`{"convention":"convention","operation":"operation","signing":"member_key"}`),
		Tags:      []string{"convention:operation"},
		Timestamp: time.Now().UnixNano(),
	}

	// "convention:operation" is the declaration tag, not a valid invocation.
	dispatched := d.Dispatch(context.Background(), "cf1", msg)
	if dispatched {
		t.Fatal("declaration tag 'convention:operation' must not trigger dispatch")
	}
}

func TestDispatcher_TagMatching_UnregisteredConvention_ReturnsFalse(t *testing.T) {
	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)
	// No handlers registered.

	msg := &store.MessageRecord{
		ID:        "unreg-msg",
		CampfireID: "cf1",
		Sender:    "aabb",
		Payload:   []byte(`{"convention":"myconv","operation":"myop"}`),
		Tags:      []string{"myconv:myop"},
		Timestamp: time.Now().UnixNano(),
	}

	dispatched := d.Dispatch(context.Background(), "cf1", msg)
	if dispatched {
		t.Fatal("expected false for unregistered convention")
	}
}

// ---- Tier 1 dispatch tests ----

func TestDispatcher_Tier1_HandlerCalled(t *testing.T) {
	env := setupDispatcherTestEnv(t)
	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)

	var handlerCalled atomic.Bool
	d.RegisterTier1Handler(env.campfireID, "myconv", "myop", env.serverClient, func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		handlerCalled.Store(true)
		return &convention.Response{Payload: map[string]any{"ok": true}}, nil
	}, env.serverID.PublicKeyHex(), "",
	)

	msg := makeConventionMsg(t, env, "myconv", "myop", map[string]any{"key": "value"})
	dispatched := d.Dispatch(context.Background(), env.campfireID, msg)
	if !dispatched {
		t.Fatal("expected Dispatch to return true")
	}

	waitForDispatch(t, ds, env.campfireID, msg.ID, 2*time.Second)

	if !handlerCalled.Load() {
		t.Fatal("handler was not called")
	}
}

func TestDispatcher_Tier1_FulfillmentPosted(t *testing.T) {
	env := setupDispatcherTestEnv(t)
	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)

	// The caller sends an operation message — we simulate by posting directly.
	payload, _ := json.Marshal(map[string]any{
		"convention": "myconv",
		"version":    "0.1",
		"operation":  "myop",
		"args":       map[string]any{},
	})
	sentMsg, err := env.callerClient.Send(protocol.SendRequest{
		CampfireID: env.campfireID,
		Payload:    payload,
		Tags:       []string{"myconv:myop"},
	})
	if err != nil {
		t.Fatalf("callerClient.Send: %v", err)
	}

	d.RegisterTier1Handler(env.campfireID, "myconv", "myop", env.serverClient, func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		return &convention.Response{Payload: map[string]any{"result": "done"}}, nil
	}, env.serverID.PublicKeyHex(), "",
	)

	msgRecord := &store.MessageRecord{
		ID:         sentMsg.ID,
		CampfireID: env.campfireID,
		Sender:     fmt.Sprintf("%x", sentMsg.Sender),
		Payload:    payload,
		Tags:       sentMsg.Tags,
		Timestamp:  sentMsg.Timestamp,
	}

	d.Dispatch(context.Background(), env.campfireID, msgRecord)
	status := waitForDispatch(t, ds, env.campfireID, sentMsg.ID, 2*time.Second)
	if status != "fulfilled" {
		t.Fatalf("expected 'fulfilled', got %q", status)
	}

	// Verify the fulfillment message was actually posted to the campfire.
	result, err := env.callerClient.Read(protocol.ReadRequest{
		CampfireID: env.campfireID,
		Tags:       []string{"fulfills"},
	})
	if err != nil {
		t.Fatalf("callerClient.Read: %v", err)
	}
	found := false
	for _, m := range result.Messages {
		for _, ant := range m.Antecedents {
			if ant == sentMsg.ID {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("no fulfillment message found with correct antecedent")
	}
}

func TestDispatcher_Tier1_HandlerError_SendsErrorFulfillment(t *testing.T) {
	env := setupDispatcherTestEnv(t)
	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)

	// Caller posts a message.
	payload, _ := json.Marshal(map[string]any{
		"convention": "myconv",
		"version":    "0.1",
		"operation":  "myop",
		"args":       map[string]any{},
	})
	sentMsg, err := env.callerClient.Send(protocol.SendRequest{
		CampfireID: env.campfireID,
		Payload:    payload,
		Tags:       []string{"myconv:myop"},
	})
	if err != nil {
		t.Fatalf("callerClient.Send: %v", err)
	}

	d.RegisterTier1Handler(env.campfireID, "myconv", "myop", env.serverClient, func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		return nil, context.DeadlineExceeded
	}, env.serverID.PublicKeyHex(), "",
	)

	msgRecord := &store.MessageRecord{
		ID:         sentMsg.ID,
		CampfireID: env.campfireID,
		Sender:     fmt.Sprintf("%x", sentMsg.Sender),
		Payload:    payload,
		Tags:       sentMsg.Tags,
		Timestamp:  sentMsg.Timestamp,
	}

	d.Dispatch(context.Background(), env.campfireID, msgRecord)
	status := waitForDispatch(t, ds, env.campfireID, sentMsg.ID, 2*time.Second)
	if status != "failed" {
		t.Fatalf("expected 'failed', got %q", status)
	}

	// Verify error fulfillment was posted.
	result, err := env.callerClient.Read(protocol.ReadRequest{
		CampfireID: env.campfireID,
		Tags:       []string{"convention:error"},
	})
	if err != nil {
		t.Fatalf("callerClient.Read: %v", err)
	}
	if len(result.Messages) == 0 {
		t.Fatal("expected at least one convention:error message")
	}
}

// ---- Deduplication tests ----

func TestDispatcher_Deduplication_SameMessageDispatchedOnce(t *testing.T) {
	env := setupDispatcherTestEnv(t)
	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)

	var callCount atomic.Int64
	d.RegisterTier1Handler(env.campfireID, "myconv", "myop", env.serverClient, func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		callCount.Add(1)
		return nil, nil
	}, env.serverID.PublicKeyHex(), "",
	)

	msg := makeConventionMsg(t, env, "myconv", "myop", nil)

	// Dispatch the same message twice.
	d.Dispatch(context.Background(), env.campfireID, msg)
	d.Dispatch(context.Background(), env.campfireID, msg)

	// Wait for any async processing to finish.
	time.Sleep(200 * time.Millisecond)

	if n := callCount.Load(); n != 1 {
		t.Fatalf("expected handler called exactly once, got %d", n)
	}
}

func TestDispatcher_Deduplication_ConcurrentDispatch(t *testing.T) {
	env := setupDispatcherTestEnv(t)
	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)

	var callCount atomic.Int64
	d.RegisterTier1Handler(env.campfireID, "myconv", "myop", env.serverClient, func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		callCount.Add(1)
		return nil, nil
	}, env.serverID.PublicKeyHex(), "",
	)

	msg := makeConventionMsg(t, env, "myconv", "myop", nil)

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			d.Dispatch(context.Background(), env.campfireID, msg)
		}()
	}
	wg.Wait()
	time.Sleep(200 * time.Millisecond)

	if n := callCount.Load(); n != 1 {
		t.Fatalf("expected handler called exactly once under concurrent dispatch, got %d", n)
	}
}

// ---- Cursor advancement test ----

func TestDispatcher_CursorAdvanced_AfterDispatch(t *testing.T) {
	env := setupDispatcherTestEnv(t)
	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)

	serverIDHex := env.serverID.PublicKeyHex()
	ts := time.Now().UnixNano()

	d.RegisterTier1Handler(env.campfireID, "myconv", "myop", env.serverClient, func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		return nil, nil
	}, serverIDHex, "",
	)

	msg := makeConventionMsg(t, env, "myconv", "myop", nil)
	msg.Timestamp = ts

	d.Dispatch(context.Background(), env.campfireID, msg)
	waitForDispatch(t, ds, env.campfireID, msg.ID, 2*time.Second)

	cursor, err := ds.GetCursor(context.Background(), serverIDHex, env.campfireID)
	if err != nil {
		t.Fatalf("GetCursor: %v", err)
	}
	if cursor != ts {
		t.Fatalf("expected cursor %d, got %d", ts, cursor)
	}
}

// ---- Metering hook test ----

func TestDispatcher_MeteringHook_FiredAfterDispatch(t *testing.T) {
	env := setupDispatcherTestEnv(t)
	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)

	var mu sync.Mutex
	var events []convention.ConventionMeterEvent

	d.MeteringHook = func(ctx context.Context, ev convention.ConventionMeterEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}

	d.RegisterTier1Handler(env.campfireID, "myconv", "myop", env.serverClient, func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		return nil, nil
	}, env.serverID.PublicKeyHex(), "",
	)

	msg := makeConventionMsg(t, env, "myconv", "myop", nil)
	d.Dispatch(context.Background(), env.campfireID, msg)
	waitForDispatch(t, ds, env.campfireID, msg.ID, 2*time.Second)

	// Give the metering hook a moment (it fires after MarkFulfilled).
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	n := len(events)
	var ev convention.ConventionMeterEvent
	if n > 0 {
		ev = events[0]
	}
	mu.Unlock()

	if n != 1 {
		t.Fatalf("expected 1 metering event, got %d", n)
	}
	if ev.Convention != "myconv" {
		t.Errorf("expected convention 'myconv', got %q", ev.Convention)
	}
	if ev.Operation != "myop" {
		t.Errorf("expected operation 'myop', got %q", ev.Operation)
	}
	if ev.Tier != 1 {
		t.Errorf("expected tier 1, got %d", ev.Tier)
	}
	if ev.MessageID != msg.ID {
		t.Errorf("expected message ID %q, got %q", msg.ID, ev.MessageID)
	}
}

// ---- Registration tests ----

func TestDispatcher_Registration_MultipleCampfires(t *testing.T) {
	// Register the same convention/operation for two different campfires.
	// Each should only dispatch when the campfireID matches.
	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)

	var calledForCF1, calledForCF2 atomic.Bool

	d.RegisterTier1Handler("cf-alpha", "testconv", "testop", nil, func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		calledForCF1.Store(true)
		return nil, nil
	}, "server-alpha", "",
	)

	d.RegisterTier1Handler("cf-beta", "testconv", "testop", nil, func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		calledForCF2.Store(true)
		return nil, nil
	}, "server-beta", "",
	)

	msg := &store.MessageRecord{
		ID:        "msg-alpha",
		CampfireID: "cf-alpha",
		Sender:    "aabb",
		Payload:   []byte(`{"convention":"testconv","operation":"testop"}`),
		Tags:      []string{"testconv:testop"},
		Timestamp: time.Now().UnixNano(),
	}

	// Dispatch to cf-alpha — only alpha handler should fire.
	dispatched := d.Dispatch(context.Background(), "cf-alpha", msg)
	if !dispatched {
		t.Fatal("expected Dispatch to return true for cf-alpha")
	}

	// Dispatch an unregistered campfire — returns false.
	msg2 := &store.MessageRecord{
		ID:        "msg-gamma",
		CampfireID: "cf-gamma",
		Sender:    "aabb",
		Payload:   []byte(`{"convention":"testconv","operation":"testop"}`),
		Tags:      []string{"testconv:testop"},
		Timestamp: time.Now().UnixNano(),
	}
	if d.Dispatch(context.Background(), "cf-gamma", msg2) {
		t.Fatal("expected Dispatch to return false for unregistered campfire")
	}

	time.Sleep(100 * time.Millisecond)
	if calledForCF2.Load() {
		t.Fatal("cf-beta handler should not have been called")
	}
}

func TestDispatcher_Registration_Replace(t *testing.T) {
	// Re-registering overwrites the previous handler.
	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)

	var firstCalled, secondCalled atomic.Bool

	d.RegisterTier1Handler("cf1", "myconv", "myop", nil, func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		firstCalled.Store(true)
		return nil, nil
	}, "server1", "",
	)

	d.RegisterTier1Handler("cf1", "myconv", "myop", nil, func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		secondCalled.Store(true)
		return nil, nil
	}, "server1", "",
	)

	msg := &store.MessageRecord{
		ID:        "msg-replace",
		CampfireID: "cf1",
		Sender:    "aabb",
		Payload:   []byte(`{"convention":"myconv","operation":"myop"}`),
		Tags:      []string{"myconv:myop"},
		Timestamp: time.Now().UnixNano(),
	}

	dispatched := d.Dispatch(context.Background(), "cf1", msg)
	if !dispatched {
		t.Fatal("expected Dispatch to return true")
	}
	time.Sleep(100 * time.Millisecond)

	if firstCalled.Load() {
		t.Fatal("first (overwritten) handler should not be called")
	}
	if !secondCalled.Load() {
		t.Fatal("second (replacement) handler should be called")
	}
}

// ---- Tier 2 dispatch tests ----

func TestDispatcher_Tier2_HTTPPostMadeWithCorrectBody(t *testing.T) {
	var received tier2Body
	var receivedOnce sync.Once
	ch := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedOnce.Do(func() {
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				t.Errorf("decode request body: %v", err)
			}
			close(ch)
		})
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)

	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)

	d.RegisterTier2Handler("cf1", "myconv", "myop", server.URL, nil, "server1", "")

	msg := &store.MessageRecord{
		ID:        "msg-tier2",
		CampfireID: "cf1",
		Sender:    "aabbcc",
		Payload:   []byte(`{"convention":"myconv","version":"0.1","operation":"myop","args":{"key":"val"}}`),
		Tags:      []string{"myconv:myop"},
		Timestamp: time.Now().UnixNano(),
	}

	dispatched := d.Dispatch(context.Background(), "cf1", msg)
	if !dispatched {
		t.Fatal("expected Dispatch to return true")
	}

	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for HTTP POST")
	}

	if received.MessageID != "msg-tier2" {
		t.Errorf("message_id: want %q, got %q", "msg-tier2", received.MessageID)
	}
	if received.CampfireID != "cf1" {
		t.Errorf("campfire_id: want %q, got %q", "cf1", received.CampfireID)
	}
	if received.Sender != "aabbcc" {
		t.Errorf("sender: want %q, got %q", "aabbcc", received.Sender)
	}
	if received.Convention != "myconv" {
		t.Errorf("convention: want %q, got %q", "myconv", received.Convention)
	}
	if received.Operation != "myop" {
		t.Errorf("operation: want %q, got %q", "myop", received.Operation)
	}
}

func TestDispatcher_Tier2_202_MarkedFulfilled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)

	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)
	d.RegisterTier2Handler("cf1", "myconv", "myop", server.URL, nil, "server1", "")

	msg := &store.MessageRecord{
		ID:        "msg-t2-ok",
		CampfireID: "cf1",
		Sender:    "aabb",
		Payload:   []byte(`{"convention":"myconv","operation":"myop"}`),
		Tags:      []string{"myconv:myop"},
		Timestamp: time.Now().UnixNano(),
	}
	d.Dispatch(context.Background(), "cf1", msg)
	status := waitForDispatch(t, ds, "cf1", "msg-t2-ok", 3*time.Second)
	if status != "fulfilled" {
		t.Fatalf("expected 'fulfilled', got %q", status)
	}
}

func TestDispatcher_Tier2_Non202_MarkedFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)
	d.RegisterTier2Handler("cf1", "myconv", "myop", server.URL, nil, "server1", "")

	msg := &store.MessageRecord{
		ID:        "msg-t2-err",
		CampfireID: "cf1",
		Sender:    "aabb",
		Payload:   []byte(`{"convention":"myconv","operation":"myop"}`),
		Tags:      []string{"myconv:myop"},
		Timestamp: time.Now().UnixNano(),
	}
	d.Dispatch(context.Background(), "cf1", msg)
	status := waitForDispatch(t, ds, "cf1", "msg-t2-err", 3*time.Second)
	if status != "failed" {
		t.Fatalf("expected 'failed', got %q", status)
	}
}

func TestDispatcher_Tier2_MeteringHook(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)

	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)

	var mu sync.Mutex
	var events []convention.ConventionMeterEvent
	d.MeteringHook = func(ctx context.Context, ev convention.ConventionMeterEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}

	d.RegisterTier2Handler("cf1", "myconv", "myop", server.URL, nil, "server1", "")

	msg := &store.MessageRecord{
		ID:        "msg-t2-meter",
		CampfireID: "cf1",
		Sender:    "aabb",
		Payload:   []byte(`{"convention":"myconv","operation":"myop"}`),
		Tags:      []string{"myconv:myop"},
		Timestamp: time.Now().UnixNano(),
	}
	d.Dispatch(context.Background(), "cf1", msg)
	waitForDispatch(t, ds, "cf1", "msg-t2-meter", 3*time.Second)
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	n := len(events)
	var ev convention.ConventionMeterEvent
	if n > 0 {
		ev = events[0]
	}
	mu.Unlock()

	if n != 1 {
		t.Fatalf("expected 1 metering event, got %d", n)
	}
	if ev.Tier != 2 {
		t.Errorf("expected tier 2, got %d", ev.Tier)
	}
	if ev.ServerID != "server1" {
		t.Errorf("expected serverID 'server1', got %q", ev.ServerID)
	}
}

// tier2Body mirrors the JSON body sent by the dispatcher to Tier 2 handlers.
type tier2Body struct {
	MessageID  string         `json:"message_id"`
	CampfireID string         `json:"campfire_id"`
	Sender     string         `json:"sender"`
	Convention string         `json:"convention"`
	Operation  string         `json:"operation"`
	Args       map[string]any `json:"args"`
	Tags       []string       `json:"tags"`
}

// ---- Hardening: sendFulfillment failure ----

// TestDispatcher_Tier1_SendFulfillmentFailure_MeteringStillFires verifies that
// when the handler succeeds but sendFulfillment fails (e.g. the campfire transport
// is broken), the metering hook still fires with status "failed" and the dispatch
// record is reverted to "failed".
func TestDispatcher_Tier1_SendFulfillmentFailure_MeteringStillFires(t *testing.T) {
	env := setupDispatcherTestEnv(t)
	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)

	var mu sync.Mutex
	var events []convention.ConventionMeterEvent
	d.MeteringHook = func(ctx context.Context, ev convention.ConventionMeterEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}

	// Use a client with a broken store so Send() will fail when posting the
	// fulfillment message. We create a store, add membership, then close it
	// to force I/O errors on Send.
	brokenStoreDir := t.TempDir()
	brokenStore, err := store.Open(filepath.Join(brokenStoreDir, "broken.db"))
	if err != nil {
		t.Fatalf("opening broken store: %v", err)
	}
	// Add the membership so the client can attempt Send (it needs the campfire lookup).
	brokenStore.AddMembership(store.Membership{
		CampfireID:    env.campfireID,
		TransportDir:  "/nonexistent/path/that/will/fail",
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	})
	brokenClient := protocol.New(brokenStore, env.serverID)

	d.RegisterTier1Handler(env.campfireID, "myconv", "myop", brokenClient, func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		// Handler succeeds with a response that requires fulfillment posting.
		return &convention.Response{Payload: map[string]any{"result": "ok"}}, nil
	}, env.serverID.PublicKeyHex(), "forge-acct-1")

	msg := makeConventionMsg(t, env, "myconv", "myop", nil)
	d.Dispatch(context.Background(), env.campfireID, msg)
	status := waitForDispatch(t, ds, env.campfireID, msg.ID, 3*time.Second)

	if status != "failed" {
		t.Fatalf("expected dispatch status 'failed' after sendFulfillment error, got %q", status)
	}

	// Metering hook must still have fired.
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	n := len(events)
	var ev convention.ConventionMeterEvent
	if n > 0 {
		ev = events[0]
	}
	mu.Unlock()

	if n != 1 {
		t.Fatalf("expected 1 metering event even after sendFulfillment failure, got %d", n)
	}
	if ev.Status != "failed" {
		t.Errorf("expected metering status 'failed', got %q", ev.Status)
	}
	if ev.ForgeAccountID != "forge-acct-1" {
		t.Errorf("expected ForgeAccountID 'forge-acct-1', got %q", ev.ForgeAccountID)
	}
}

// ---- Hardening: context cancellation during dispatch ----

// TestDispatcher_ContextCancellation_CleanShutdown verifies that cancelling the
// context mid-dispatch does not hang or panic. The handler blocks until the context
// is cancelled, then returns an error. The dispatch should complete with "failed".
func TestDispatcher_ContextCancellation_CleanShutdown(t *testing.T) {
	env := setupDispatcherTestEnv(t)
	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)

	handlerStarted := make(chan struct{})
	d.RegisterTier1Handler(env.campfireID, "myconv", "myop", env.serverClient, func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		close(handlerStarted)
		// Block until context is cancelled.
		<-ctx.Done()
		return nil, ctx.Err()
	}, env.serverID.PublicKeyHex(), "")

	ctx, cancel := context.WithCancel(context.Background())
	msg := makeConventionMsg(t, env, "myconv", "myop", nil)
	dispatched := d.Dispatch(ctx, env.campfireID, msg)
	if !dispatched {
		t.Fatal("expected Dispatch to return true")
	}

	// Wait for the handler goroutine to start.
	select {
	case <-handlerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for handler to start")
	}

	// Cancel the context while the handler is blocked.
	cancel()

	// The dispatch should complete with "failed" status.
	status := waitForDispatch(t, ds, env.campfireID, msg.ID, 3*time.Second)
	if status != "failed" {
		t.Fatalf("expected 'failed' after context cancellation, got %q", status)
	}
}

// TestDispatcher_DispatchWithCancel_CancelFuncCalled verifies that
// DispatchWithCancel calls the provided cancel func after the goroutine completes,
// preventing context/timer leaks.
func TestDispatcher_DispatchWithCancel_CancelFuncCalled(t *testing.T) {
	env := setupDispatcherTestEnv(t)
	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)

	d.RegisterTier1Handler(env.campfireID, "myconv", "myop", env.serverClient, func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		return nil, nil
	}, env.serverID.PublicKeyHex(), "")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

	var cancelCalled atomic.Bool
	wrappedCancel := func() {
		cancelCalled.Store(true)
		cancel()
	}

	msg := makeConventionMsg(t, env, "myconv", "myop", nil)
	msg.ID = "msg-cancel-func-test" // unique ID to avoid dedup collision
	dispatched := d.DispatchWithCancel(ctx, wrappedCancel, env.campfireID, msg)
	if !dispatched {
		t.Fatal("expected DispatchWithCancel to return true")
	}

	waitForDispatch(t, ds, env.campfireID, msg.ID, 3*time.Second)
	// Give a moment for the deferred cancel to run.
	time.Sleep(50 * time.Millisecond)

	if !cancelCalled.Load() {
		t.Fatal("expected cancel func to be called after dispatch goroutine completed")
	}
}

// ---- Hardening: concurrent dispatch deduplication via MarkDispatched ----

// TestDispatcher_ConcurrentDispatch_MarkDispatched_Atomicity stress-tests that
// concurrent Dispatch calls for the same message ID result in exactly one handler
// invocation. This goes beyond TestDispatcher_Deduplication_ConcurrentDispatch by
// verifying metering fires exactly once and the final status is correct.
func TestDispatcher_ConcurrentDispatch_MarkDispatched_Atomicity(t *testing.T) {
	env := setupDispatcherTestEnv(t)
	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)

	var callCount atomic.Int64
	var mu sync.Mutex
	var meterEvents []convention.ConventionMeterEvent

	d.MeteringHook = func(ctx context.Context, ev convention.ConventionMeterEvent) {
		mu.Lock()
		meterEvents = append(meterEvents, ev)
		mu.Unlock()
	}

	d.RegisterTier1Handler(env.campfireID, "myconv", "myop", env.serverClient, func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		callCount.Add(1)
		// Simulate some work to widen the race window.
		time.Sleep(10 * time.Millisecond)
		return nil, nil
	}, env.serverID.PublicKeyHex(), "")

	msg := makeConventionMsg(t, env, "myconv", "myop", nil)
	msg.ID = "msg-concurrent-atomic" // unique ID

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			d.Dispatch(context.Background(), env.campfireID, msg)
		}()
	}
	wg.Wait()

	// Wait for async dispatch to finish.
	waitForDispatch(t, ds, env.campfireID, msg.ID, 3*time.Second)
	time.Sleep(100 * time.Millisecond)

	if n := callCount.Load(); n != 1 {
		t.Fatalf("expected handler called exactly once under 50 concurrent dispatches, got %d", n)
	}

	mu.Lock()
	nEvents := len(meterEvents)
	mu.Unlock()
	if nEvents != 1 {
		t.Fatalf("expected exactly 1 metering event, got %d", nEvents)
	}

	finalStatus, err := ds.GetDispatchStatus(context.Background(), env.campfireID, msg.ID)
	if err != nil {
		t.Fatalf("GetDispatchStatus: %v", err)
	}
	if finalStatus != "fulfilled" {
		t.Fatalf("expected final status 'fulfilled', got %q", finalStatus)
	}
}

// ---- ErrDispatchNotFound → not_found status (campfire-agent-43r) ----

// TestDispatcher_Tier1_DispatchNotFound_SkipsMeteringAndCursor verifies that when
// MarkFulfilledCAS returns ErrDispatchNotFound (record deleted between MarkDispatched
// and CAS), the dispatcher returns "not_found" and skips metering + cursor advancement.
func TestDispatcher_Tier1_DispatchNotFound_SkipsMeteringAndCursor(t *testing.T) {
	env := setupDispatcherTestEnv(t)
	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)

	serverIDHex := env.serverID.PublicKeyHex()

	var mu sync.Mutex
	var meterEvents []convention.ConventionMeterEvent
	d.MeteringHook = func(ctx context.Context, ev convention.ConventionMeterEvent) {
		mu.Lock()
		meterEvents = append(meterEvents, ev)
		mu.Unlock()
	}

	handlerDone := make(chan struct{})
	d.RegisterTier1Handler(env.campfireID, "myconv", "myop", env.serverClient, func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		// Delete the dispatch record mid-handler so that MarkFulfilledCAS
		// will return ErrDispatchNotFound.
		ds.DeleteDispatch(env.campfireID, req.MessageID)
		close(handlerDone)
		return &convention.Response{Payload: map[string]any{"ok": true}}, nil
	}, serverIDHex, "forge-acct-1")

	msg := makeConventionMsg(t, env, "myconv", "myop", nil)
	msg.ID = "msg-notfound-tier1"
	msg.Timestamp = time.Now().UnixNano()

	dispatched := d.Dispatch(context.Background(), env.campfireID, msg)
	if !dispatched {
		t.Fatal("expected Dispatch to return true")
	}

	// Wait for handler to complete.
	select {
	case <-handlerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for handler")
	}
	// Give invokeHandler time to run metering/cursor logic.
	time.Sleep(100 * time.Millisecond)

	// Metering hook must NOT have fired.
	mu.Lock()
	nEvents := len(meterEvents)
	mu.Unlock()
	if nEvents != 0 {
		t.Fatalf("expected 0 metering events for not_found dispatch, got %d", nEvents)
	}

	// Cursor must NOT have advanced.
	cursor, err := ds.GetCursor(context.Background(), serverIDHex, env.campfireID)
	if err != nil {
		t.Fatalf("GetCursor: %v", err)
	}
	if cursor != 0 {
		t.Fatalf("expected cursor 0 (not advanced) for not_found dispatch, got %d", cursor)
	}
}

// TestDispatcher_Tier1_HandlerError_DispatchNotFound_SkipsMeteringAndCursor verifies
// that when the handler errors and MarkFailedCAS returns ErrDispatchNotFound, the
// dispatcher returns "not_found" and skips metering + cursor advancement.
func TestDispatcher_Tier1_HandlerError_DispatchNotFound_SkipsMeteringAndCursor(t *testing.T) {
	env := setupDispatcherTestEnv(t)
	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)

	serverIDHex := env.serverID.PublicKeyHex()

	var mu sync.Mutex
	var meterEvents []convention.ConventionMeterEvent
	d.MeteringHook = func(ctx context.Context, ev convention.ConventionMeterEvent) {
		mu.Lock()
		meterEvents = append(meterEvents, ev)
		mu.Unlock()
	}

	handlerDone := make(chan struct{})
	d.RegisterTier1Handler(env.campfireID, "myconv", "myop", env.serverClient, func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		// Delete the dispatch record mid-handler so MarkFailedCAS returns ErrDispatchNotFound.
		ds.DeleteDispatch(env.campfireID, req.MessageID)
		close(handlerDone)
		return nil, fmt.Errorf("intentional handler error")
	}, serverIDHex, "forge-acct-1")

	msg := makeConventionMsg(t, env, "myconv", "myop", nil)
	msg.ID = "msg-notfound-tier1-err"
	msg.Timestamp = time.Now().UnixNano()

	dispatched := d.Dispatch(context.Background(), env.campfireID, msg)
	if !dispatched {
		t.Fatal("expected Dispatch to return true")
	}

	select {
	case <-handlerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for handler")
	}
	time.Sleep(100 * time.Millisecond)

	// Metering hook must NOT have fired.
	mu.Lock()
	nEvents := len(meterEvents)
	mu.Unlock()
	if nEvents != 0 {
		t.Fatalf("expected 0 metering events for not_found dispatch, got %d", nEvents)
	}

	// Cursor must NOT have advanced.
	cursor, err := ds.GetCursor(context.Background(), serverIDHex, env.campfireID)
	if err != nil {
		t.Fatalf("GetCursor: %v", err)
	}
	if cursor != 0 {
		t.Fatalf("expected cursor 0 (not advanced) for not_found dispatch, got %d", cursor)
	}
}

// TestDispatcher_Tier2_DispatchNotFound_SkipsMeteringAndCursor verifies the
// not_found path for Tier 2 dispatchers. When MarkFulfilledCAS returns
// ErrDispatchNotFound after a 202, metering and cursor must be skipped.
func TestDispatcher_Tier2_DispatchNotFound_SkipsMeteringAndCursor(t *testing.T) {
	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)

	serverIDHex := "server-notfound-tier2"

	var mu sync.Mutex
	var meterEvents []convention.ConventionMeterEvent
	d.MeteringHook = func(ctx context.Context, ev convention.ConventionMeterEvent) {
		mu.Lock()
		meterEvents = append(meterEvents, ev)
		mu.Unlock()
	}

	requestReceived := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Delete the dispatch record before responding 202, so MarkFulfilledCAS
		// will find no record.
		ds.DeleteDispatch("cf-nf-t2", "msg-notfound-tier2")
		close(requestReceived)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)

	d.RegisterTier2Handler("cf-nf-t2", "myconv", "myop", server.URL, nil, serverIDHex, "forge-acct-2")

	msg := &store.MessageRecord{
		ID:         "msg-notfound-tier2",
		CampfireID: "cf-nf-t2",
		Sender:     "aabb",
		Payload:    []byte(`{"convention":"myconv","operation":"myop"}`),
		Tags:       []string{"myconv:myop"},
		Timestamp:  time.Now().UnixNano(),
	}

	dispatched := d.Dispatch(context.Background(), "cf-nf-t2", msg)
	if !dispatched {
		t.Fatal("expected Dispatch to return true")
	}

	select {
	case <-requestReceived:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for HTTP handler")
	}
	time.Sleep(100 * time.Millisecond)

	// Metering hook must NOT have fired.
	mu.Lock()
	nEvents := len(meterEvents)
	mu.Unlock()
	if nEvents != 0 {
		t.Fatalf("expected 0 metering events for not_found tier2 dispatch, got %d", nEvents)
	}

	// Cursor must NOT have advanced.
	cursor, err := ds.GetCursor(context.Background(), serverIDHex, "cf-nf-t2")
	if err != nil {
		t.Fatalf("GetCursor: %v", err)
	}
	if cursor != 0 {
		t.Fatalf("expected cursor 0 (not advanced) for not_found tier2 dispatch, got %d", cursor)
	}
}

// TestDispatcher_Tier2_Non202_DispatchNotFound_SkipsMeteringAndCursor verifies
// the not_found path for Tier 2 when the HTTP handler returns non-202 and the
// subsequent MarkFailedCAS returns ErrDispatchNotFound.
func TestDispatcher_Tier2_Non202_DispatchNotFound_SkipsMeteringAndCursor(t *testing.T) {
	ds := convention.NewMemoryDispatchStore()
	d := convention.NewConventionDispatcher(ds, nil)

	serverIDHex := "server-notfound-tier2-fail"

	var mu sync.Mutex
	var meterEvents []convention.ConventionMeterEvent
	d.MeteringHook = func(ctx context.Context, ev convention.ConventionMeterEvent) {
		mu.Lock()
		meterEvents = append(meterEvents, ev)
		mu.Unlock()
	}

	requestReceived := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Delete dispatch record before returning 500, so MarkFailedCAS returns ErrDispatchNotFound.
		ds.DeleteDispatch("cf-nf-t2-fail", "msg-notfound-tier2-fail")
		close(requestReceived)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	d.RegisterTier2Handler("cf-nf-t2-fail", "myconv", "myop", server.URL, nil, serverIDHex, "forge-acct-3")

	msg := &store.MessageRecord{
		ID:         "msg-notfound-tier2-fail",
		CampfireID: "cf-nf-t2-fail",
		Sender:     "aabb",
		Payload:    []byte(`{"convention":"myconv","operation":"myop"}`),
		Tags:       []string{"myconv:myop"},
		Timestamp:  time.Now().UnixNano(),
	}

	dispatched := d.Dispatch(context.Background(), "cf-nf-t2-fail", msg)
	if !dispatched {
		t.Fatal("expected Dispatch to return true")
	}

	select {
	case <-requestReceived:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for HTTP handler")
	}
	time.Sleep(100 * time.Millisecond)

	// Metering hook must NOT have fired.
	mu.Lock()
	nEvents := len(meterEvents)
	mu.Unlock()
	if nEvents != 0 {
		t.Fatalf("expected 0 metering events for not_found tier2 dispatch, got %d", nEvents)
	}

	// Cursor must NOT have advanced.
	cursor, err := ds.GetCursor(context.Background(), serverIDHex, "cf-nf-t2-fail")
	if err != nil {
		t.Fatalf("GetCursor: %v", err)
	}
	if cursor != 0 {
		t.Fatalf("expected cursor 0 (not advanced) for not_found tier2 dispatch, got %d", cursor)
	}
}
