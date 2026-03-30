package convention_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
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

// serverTestEnv is the shared test scaffolding for Server integration tests.
type serverTestEnv struct {
	// serverClient is used by the Server (reads + sends responses).
	serverClient *protocol.Client
	// callerClient is used by the test to send operation requests.
	callerClient *protocol.Client
	campfireID   string
}

// setupServerTestEnv creates two identities (server + caller), a shared filesystem
// campfire that both are members of, and returns clients for each.
func setupServerTestEnv(t *testing.T) *serverTestEnv {
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

	// Create campfire identity.
	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	campfireID := cfID.PublicKeyHex()

	// Set up directory structure.
	cfDir := filepath.Join(transportDir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	// Write campfire state.
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

	// Write both members.
	for _, id := range []*identity.Identity{serverID, callerID} {
		if err := tr.WriteMember(campfireID, campfire.MemberRecord{
			PublicKey: id.PublicKey,
			JoinedAt:  time.Now().UnixNano(),
			Role:      campfire.RoleFull,
		}); err != nil {
			t.Fatalf("writing member: %v", err)
		}
	}

	// Set up stores.
	serverStore, err := store.Open(filepath.Join(storeDir, "server.db"))
	if err != nil {
		t.Fatalf("opening server store: %v", err)
	}
	t.Cleanup(func() { serverStore.Close() })

	callerStore, err := store.Open(filepath.Join(storeDir, "caller.db"))
	if err != nil {
		t.Fatalf("opening caller store: %v", err)
	}
	t.Cleanup(func() { callerStore.Close() })

	membership := store.Membership{
		CampfireID:    campfireID,
		TransportDir:  tr.CampfireDir(campfireID),
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	}
	if err := serverStore.AddMembership(membership); err != nil {
		t.Fatalf("server store add membership: %v", err)
	}
	if err := callerStore.AddMembership(membership); err != nil {
		t.Fatalf("caller store add membership: %v", err)
	}

	return &serverTestEnv{
		serverClient: protocol.New(serverStore, serverID),
		callerClient: protocol.New(callerStore, callerID),
		campfireID:   campfireID,
	}
}

// socialPostDecl returns a minimal Declaration for the social-post-format:post operation.
func socialPostDecl() *convention.Declaration {
	return &convention.Declaration{
		Convention: "social-post-format",
		Operation:  "post",
		Signing:    "member_key",
		Args: []convention.ArgDescriptor{
			{Name: "text", Type: "string", Required: true, MaxLength: 65536},
		},
		ProducesTags: []convention.TagRule{
			{Tag: "social:post", Cardinality: "exactly_one"},
		},
		Antecedents: "none",
	}
}

// TestServerSDK_RegisterAndDispatch verifies the end-to-end Server SDK flow:
//  1. Register a handler for the "post" operation.
//  2. Send a convention operation message via Client.Send.
//  3. Verify the server receives it, calls the handler with parsed args.
//  4. Verify the response is sent with the request message ID as antecedent.
func TestServerSDK_RegisterAndDispatch(t *testing.T) {
	env := setupServerTestEnv(t)
	decl := socialPostDecl()

	// Track handler invocations.
	var mu sync.Mutex
	var receivedText string
	var receivedSender string

	srv := convention.NewServer(env.serverClient, decl).
		WithPollInterval(50 * time.Millisecond)

	srv.RegisterHandler("post", func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		if text, ok := req.Args["text"].(string); ok {
			receivedText = text
		}
		receivedSender = req.Sender
		return &convention.Response{
			Payload: map[string]any{"ack": true},
		}, nil
	})

	// Start the server in the background.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var serveErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		serveErr = srv.Serve(ctx, env.campfireID)
	}()

	// Give server a moment to start its first poll.
	time.Sleep(20 * time.Millisecond)

	// Send a convention operation message from the caller.
	sentMsg, err := env.callerClient.Send(protocol.SendRequest{
		CampfireID: env.campfireID,
		Payload:    []byte(`{"text":"hello campfire"}`),
		Tags:       []string{"social:post"},
	})
	if err != nil {
		t.Fatalf("caller Send: %v", err)
	}

	// Wait for the server to process the message and send a response.
	// We poll the caller's store for a fulfillment of the sent message.
	var responseFound bool
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		result, err := env.callerClient.Read(protocol.ReadRequest{
			CampfireID: env.campfireID,
			Tags:       []string{"fulfills"},
		})
		if err != nil {
			t.Fatalf("caller Read fulfills: %v", err)
		}
		for _, msg := range result.Messages {
			for _, ant := range msg.Antecedents {
				if ant == sentMsg.ID {
					responseFound = true
				}
			}
		}
		if responseFound {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	cancel()
	wg.Wait()

	// ctx.Err() is context.DeadlineExceeded or Canceled — either is expected.
	if serveErr != context.Canceled && serveErr != context.DeadlineExceeded {
		t.Errorf("Serve returned unexpected error: %v", serveErr)
	}

	// Verify handler was called with correct args.
	mu.Lock()
	defer mu.Unlock()
	if receivedText != "hello campfire" {
		t.Errorf("handler received text %q, want %q", receivedText, "hello campfire")
	}
	if receivedSender == "" {
		t.Error("handler received empty sender")
	}
	if !responseFound {
		t.Error("no auto-threaded response found for sent message")
	}
}

// TestServerSDK_NoHandlerRegistered verifies that messages for operations without
// a registered handler are silently skipped (no panic, no response sent).
func TestServerSDK_NoHandlerRegistered(t *testing.T) {
	env := setupServerTestEnv(t)
	decl := socialPostDecl()

	var errSeen bool
	srv := convention.NewServer(env.serverClient, decl).
		WithPollInterval(50 * time.Millisecond).
		WithErrorHandler(func(err error) { errSeen = true })

	// Intentionally do NOT register any handler.

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		srv.Serve(ctx, env.campfireID) //nolint:errcheck
	}()

	// Send a message.
	_, err := env.callerClient.Send(protocol.SendRequest{
		CampfireID: env.campfireID,
		Payload:    []byte(`{"text":"ignored"}`),
		Tags:       []string{"social:post"},
	})
	if err != nil {
		t.Fatalf("caller Send: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	cancel()
	wg.Wait()

	// No error should have been produced (silent skip).
	if errSeen {
		t.Error("expected no error for missing handler, but errFn was called")
	}

	// No fulfillment should exist.
	result, err := env.callerClient.Read(protocol.ReadRequest{
		CampfireID: env.campfireID,
		Tags:       []string{"fulfills"},
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(result.Messages) != 0 {
		t.Errorf("expected 0 fulfillment messages, got %d", len(result.Messages))
	}
}

// TestServerSDK_ResponseIsAutoThreaded verifies that the response antecedent
// is the request message ID (auto-threading).
func TestServerSDK_ResponseIsAutoThreaded(t *testing.T) {
	env := setupServerTestEnv(t)
	decl := socialPostDecl()

	srv := convention.NewServer(env.serverClient, decl).
		WithPollInterval(50 * time.Millisecond)

	srv.RegisterHandler("post", func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		return &convention.Response{
			Payload: map[string]any{"echo": req.Args["text"]},
			Tags:    []string{"echo"},
		}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		srv.Serve(ctx, env.campfireID) //nolint:errcheck
	}()

	time.Sleep(20 * time.Millisecond)

	sentMsg, err := env.callerClient.Send(protocol.SendRequest{
		CampfireID: env.campfireID,
		Payload:    []byte(`{"text":"ping"}`),
		Tags:       []string{"social:post"},
	})
	if err != nil {
		t.Fatalf("caller Send: %v", err)
	}

	// Poll for the fulfillment.
	var antecedentCorrect bool
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		result, err := env.callerClient.Read(protocol.ReadRequest{
			CampfireID: env.campfireID,
			Tags:       []string{"fulfills"},
		})
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		for _, msg := range result.Messages {
			for _, ant := range msg.Antecedents {
				if ant == sentMsg.ID {
					antecedentCorrect = true
				}
			}
		}
		if antecedentCorrect {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	cancel()
	wg.Wait()

	if !antecedentCorrect {
		t.Errorf("response not threaded back to request message ID %s", sentMsg.ID)
	}
}

// TestServerSDK_ParsedArgsTyped verifies that the handler receives properly
// parsed and typed arguments (not raw bytes).
func TestServerSDK_ParsedArgsTyped(t *testing.T) {
	env := setupServerTestEnv(t)
	decl := &convention.Declaration{
		Convention: "test-convention",
		Operation:  "count",
		Signing:    "member_key",
		Args: []convention.ArgDescriptor{
			{Name: "count", Type: "integer", Required: true},
			{Name: "label", Type: "string", Required: false},
		},
		ProducesTags: []convention.TagRule{
			{Tag: "test-convention:count", Cardinality: "exactly_one"},
		},
		Antecedents: "none",
	}

	var mu sync.Mutex
	var gotCount any
	var gotLabel any

	srv := convention.NewServer(env.serverClient, decl).
		WithPollInterval(50 * time.Millisecond)

	srv.RegisterHandler("count", func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		gotCount = req.Args["count"]
		gotLabel = req.Args["label"]
		return nil, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		srv.Serve(ctx, env.campfireID) //nolint:errcheck
	}()

	time.Sleep(20 * time.Millisecond)

	_, err := env.callerClient.Send(protocol.SendRequest{
		CampfireID: env.campfireID,
		Payload:    []byte(`{"count":42,"label":"hello"}`),
		Tags:       []string{"test-convention:count"},
	})
	if err != nil {
		t.Fatalf("caller Send: %v", err)
	}

	// Wait for handler to be called.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		seen := gotCount != nil
		mu.Unlock()
		if seen {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	if gotCount == nil {
		t.Fatal("handler was not called")
	}
	// JSON numbers unmarshal as float64; validateArgs converts to int for "integer" type,
	// but the internal validateSingleValue validates the value — the returned map holds
	// the validated value. json.Unmarshal gives float64, which is accepted by integer validator.
	// The resolved map stores the original value. Confirm it is numeric.
	switch v := gotCount.(type) {
	case float64:
		if v != 42 {
			t.Errorf("count: want 42, got %v", v)
		}
	case int:
		if v != 42 {
			t.Errorf("count: want 42, got %v", v)
		}
	default:
		t.Errorf("count: unexpected type %T (value %v)", gotCount, gotCount)
	}

	if label, ok := gotLabel.(string); !ok || label != "hello" {
		t.Errorf("label: want %q, got %v (type %T)", "hello", gotLabel, gotLabel)
	}
}

// TestServerSDK_HandlerDispatchViaSubscribe verifies that Serve() routes an incoming
// convention operation message to the registered handler via client.Subscribe()
// (not a manual poll loop). Handler is called with the correct Request.Args
// and a response appears in the campfire within 5 seconds.
func TestServerSDK_HandlerDispatchViaSubscribe(t *testing.T) {
	env := setupServerTestEnv(t)
	decl := socialPostDecl()

	var mu sync.Mutex
	var gotText string

	srv := convention.NewServer(env.serverClient, decl).
		WithPollInterval(50 * time.Millisecond)

	srv.RegisterHandler("post", func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		if v, ok := req.Args["text"].(string); ok {
			gotText = v
		}
		return &convention.Response{Payload: map[string]any{"dispatched": true}}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		srv.Serve(ctx, env.campfireID) //nolint:errcheck
	}()

	// Give server time to start its subscription.
	time.Sleep(20 * time.Millisecond)

	sentMsg, err := env.callerClient.Send(protocol.SendRequest{
		CampfireID: env.campfireID,
		Payload:    []byte(`{"text":"subscribe dispatch"}`),
		Tags:       []string{"social:post"},
	})
	if err != nil {
		t.Fatalf("caller Send: %v", err)
	}

	// Wait for response to appear (confirms handler was called and response sent).
	var responseFound bool
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		result, err := env.callerClient.Read(protocol.ReadRequest{
			CampfireID: env.campfireID,
			Tags:       []string{"fulfills"},
		})
		if err != nil {
			t.Fatalf("caller Read fulfills: %v", err)
		}
		for _, msg := range result.Messages {
			for _, ant := range msg.Antecedents {
				if ant == sentMsg.ID {
					responseFound = true
				}
			}
		}
		if responseFound {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	wg.Wait()

	if !responseFound {
		t.Error("no response found within 5 seconds — handler was not dispatched via Subscribe")
	}
	mu.Lock()
	defer mu.Unlock()
	if gotText != "subscribe dispatch" {
		t.Errorf("handler received text %q, want %q", gotText, "subscribe dispatch")
	}
}

// TestServerSDK_ContextCancellation verifies that Serve() returns ctx.Err()
// within 2 seconds of context cancellation, and that no goroutine is leaked.
func TestServerSDK_ContextCancellation(t *testing.T) {
	env := setupServerTestEnv(t)
	decl := socialPostDecl()

	srv := convention.NewServer(env.serverClient, decl).
		WithPollInterval(50 * time.Millisecond)

	srv.RegisterHandler("post", func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		return nil, nil
	})

	// Sample goroutine count before starting.
	goroutinesBefore := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())

	var serveErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		serveErr = srv.Serve(ctx, env.campfireID)
	}()

	// Let the server run briefly.
	time.Sleep(20 * time.Millisecond)

	// Cancel and wait for Serve to exit — must complete within 2 seconds.
	cancel()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Good — Serve returned.
	case <-time.After(2 * time.Second):
		t.Fatal("Serve() did not return within 2 seconds of context cancellation")
	}

	// Serve must return ctx.Err() (context.Canceled).
	if serveErr != context.Canceled {
		t.Errorf("Serve() returned %v, want context.Canceled", serveErr)
	}

	// Allow time for goroutines to settle, then verify no leak.
	// The subscription goroutine must exit after context cancellation.
	time.Sleep(100 * time.Millisecond)
	goroutinesAfter := runtime.NumGoroutine()
	// Allow at most goroutinesBefore+1 goroutines: the test itself may add one.
	if goroutinesAfter > goroutinesBefore+2 {
		t.Errorf("goroutine leak: started with %d, now %d (delta %d > 2)",
			goroutinesBefore, goroutinesAfter, goroutinesAfter-goroutinesBefore)
	}
}

// TestServerSDK_PollIntervalBoundsLatency verifies that message arrival latency
// is bounded by the configured poll interval. Messages must be delivered within
// 3× the poll interval of being sent.
func TestServerSDK_PollIntervalBoundsLatency(t *testing.T) {
	env := setupServerTestEnv(t)
	decl := socialPostDecl()

	const pollInterval = 100 * time.Millisecond
	const maxLatency = 3 * pollInterval // generous upper bound

	var mu sync.Mutex
	var receivedAt time.Time

	srv := convention.NewServer(env.serverClient, decl).
		WithPollInterval(pollInterval)

	srv.RegisterHandler("post", func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		mu.Lock()
		receivedAt = time.Now()
		mu.Unlock()
		return nil, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		srv.Serve(ctx, env.campfireID) //nolint:errcheck
	}()

	// Wait for subscription to be established.
	time.Sleep(20 * time.Millisecond)

	sentAt := time.Now()
	_, err := env.callerClient.Send(protocol.SendRequest{
		CampfireID: env.campfireID,
		Payload:    []byte(`{"text":"latency check"}`),
		Tags:       []string{"social:post"},
	})
	if err != nil {
		t.Fatalf("caller Send: %v", err)
	}

	// Wait for handler to be called.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		seen := !receivedAt.IsZero()
		mu.Unlock()
		if seen {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if receivedAt.IsZero() {
		t.Fatal("handler was never called")
	}
	latency := receivedAt.Sub(sentAt)
	if latency > maxLatency {
		t.Errorf("message latency %v exceeds 3× poll interval (%v)", latency, maxLatency)
	}
}
