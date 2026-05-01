package convention_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	cfencoding "github.com/campfire-net/campfire/cf-protocol/encoding"
	"github.com/campfire-net/campfire/cf-conventions/cf-convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
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

// TestServerSDK_HandlerCalledOnMessage verifies that when a message arrives on
// the campfire, the registered handler is invoked. This is a correctness test —
// it does not assert latency bounds, which are load-dependent SLO claims that
// belong in integration/performance tests, not correctness tests.
func TestServerSDK_HandlerCalledOnMessage(t *testing.T) {
	env := setupServerTestEnv(t)
	decl := socialPostDecl()

	const pollInterval = 100 * time.Millisecond

	var mu sync.Mutex
	var handlerCalled bool

	srv := convention.NewServer(env.serverClient, decl).
		WithPollInterval(pollInterval)

	srv.RegisterHandler("post", func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		mu.Lock()
		handlerCalled = true
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

	_, err := env.callerClient.Send(protocol.SendRequest{
		CampfireID: env.campfireID,
		Payload:    []byte(`{"text":"handler invocation check"}`),
		Tags:       []string{"social:post"},
	})
	if err != nil {
		t.Fatalf("caller Send: %v", err)
	}

	// Wait for handler to be called (up to 5s — correctness only, no SLO).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		seen := handlerCalled
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
	if !handlerCalled {
		t.Fatal("handler was never called")
	}
}

// TestServerSDK_HandlerErrorSendsErrorFulfillment verifies that when a handler
// returns an error, the server posts a fulfillment message tagged "convention:error"
// with a JSON payload containing the error text.
func TestServerSDK_HandlerErrorSendsErrorFulfillment(t *testing.T) {
	env := setupServerTestEnv(t)
	decl := socialPostDecl()

	srv := convention.NewServer(env.serverClient, decl).
		WithPollInterval(50 * time.Millisecond)

	srv.RegisterHandler("post", func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		return nil, errors.New("processing failed: invalid input")
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
		Payload:    []byte(`{"text":"trigger error"}`),
		Tags:       []string{"social:post"},
	})
	if err != nil {
		t.Fatalf("caller Send: %v", err)
	}

	// Poll for error fulfillment.
	var errorMsg *protocol.Message
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		result, err := env.callerClient.Read(protocol.ReadRequest{
			CampfireID: env.campfireID,
			Tags:       []string{"convention:error"},
		})
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		for i, msg := range result.Messages {
			for _, ant := range msg.Antecedents {
				if ant == sentMsg.ID {
					errorMsg = &result.Messages[i]
					break
				}
			}
			if errorMsg != nil {
				break
			}
		}
		if errorMsg != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	wg.Wait()

	if errorMsg == nil {
		t.Fatal("no convention:error fulfillment found for failed handler")
	}

	// Verify both required tags are present.
	hasFulfills := false
	hasConventionError := false
	for _, tag := range errorMsg.Tags {
		if tag == "fulfills" {
			hasFulfills = true
		}
		if tag == "convention:error" {
			hasConventionError = true
		}
	}
	if !hasFulfills {
		t.Errorf("error fulfillment missing 'fulfills' tag; tags: %v", errorMsg.Tags)
	}
	if !hasConventionError {
		t.Errorf("error fulfillment missing 'convention:error' tag; tags: %v", errorMsg.Tags)
	}
}

// TestIsErrorResponse verifies that IsErrorResponse correctly identifies
// messages tagged "convention:error" and ignores untagged messages.
func TestIsErrorResponse(t *testing.T) {
	tests := []struct {
		name string
		tags []string
		want bool
	}{
		{
			name: "error response has convention:error tag",
			tags: []string{"fulfills", "convention:error"},
			want: true,
		},
		{
			name: "normal fulfillment is not error response",
			tags: []string{"fulfills"},
			want: false,
		},
		{
			name: "empty tags is not error response",
			tags: nil,
			want: false,
		},
		{
			name: "unrelated tags is not error response",
			tags: []string{"social:post", "echo"},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg := &protocol.Message{Tags: tc.tags}
			got := convention.IsErrorResponse(msg)
			if got != tc.want {
				t.Errorf("IsErrorResponse(%v) = %v, want %v", tc.tags, got, tc.want)
			}
		})
	}
}

// TestParseErrorResponse verifies that ParseErrorResponse extracts the error
// string from a valid error payload and returns an error for invalid payloads.
func TestParseErrorResponse(t *testing.T) {
	t.Run("valid error payload", func(t *testing.T) {
		msg := &protocol.Message{
			Tags:    []string{"fulfills", "convention:error"},
			Payload: []byte(`{"error":"processing failed: invalid input"}`),
		}
		got, err := convention.ParseErrorResponse(msg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "processing failed: invalid input" {
			t.Errorf("ParseErrorResponse = %q, want %q", got, "processing failed: invalid input")
		}
	})

	t.Run("invalid JSON payload returns error", func(t *testing.T) {
		msg := &protocol.Message{
			Tags:    []string{"convention:error"},
			Payload: []byte(`not json`),
		}
		_, err := convention.ParseErrorResponse(msg)
		if err == nil {
			t.Error("expected error for invalid JSON, got nil")
		}
	})

	t.Run("empty error field", func(t *testing.T) {
		msg := &protocol.Message{
			Tags:    []string{"convention:error"},
			Payload: []byte(`{"error":""}`),
		}
		got, err := convention.ParseErrorResponse(msg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("ParseErrorResponse = %q, want empty string", got)
		}
	})
}

// TestServerSDK_ErrorFulfillmentRoundTrip verifies the full round-trip:
// handler error → error fulfillment → IsErrorResponse detects it →
// ParseErrorResponse extracts the error message.
func TestServerSDK_ErrorFulfillmentRoundTrip(t *testing.T) {
	env := setupServerTestEnv(t)
	decl := socialPostDecl()

	const wantErrMsg = "round-trip failure: something went wrong"

	srv := convention.NewServer(env.serverClient, decl).
		WithPollInterval(50 * time.Millisecond)

	srv.RegisterHandler("post", func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		return nil, errors.New(wantErrMsg)
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
		Payload:    []byte(`{"text":"round trip"}`),
		Tags:       []string{"social:post"},
	})
	if err != nil {
		t.Fatalf("caller Send: %v", err)
	}

	// Poll for any fulfillment of this message.
	var errorMsg *protocol.Message
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		result, err := env.callerClient.Read(protocol.ReadRequest{
			CampfireID: env.campfireID,
			Tags:       []string{"fulfills"},
		})
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		for i, msg := range result.Messages {
			for _, ant := range msg.Antecedents {
				if ant == sentMsg.ID {
					errorMsg = &result.Messages[i]
					break
				}
			}
			if errorMsg != nil {
				break
			}
		}
		if errorMsg != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	wg.Wait()

	if errorMsg == nil {
		t.Fatal("no fulfillment found for sent message")
	}

	// Use helpers to detect and parse.
	if !convention.IsErrorResponse(errorMsg) {
		t.Errorf("IsErrorResponse = false, want true; tags: %v", errorMsg.Tags)
	}

	gotErr, err := convention.ParseErrorResponse(errorMsg)
	if err != nil {
		t.Fatalf("ParseErrorResponse: %v", err)
	}
	if gotErr != wantErrMsg {
		t.Errorf("error message = %q, want %q", gotErr, wantErrMsg)
	}
}

// gatedServerDecl returns a Declaration with min_operator_level=2 for use in Server provenance tests.
func gatedServerDecl() *convention.Declaration {
	return &convention.Declaration{
		Convention:       "peering",
		Operation:        "core-peer-establish",
		Signing:          "member_key",
		MinOperatorLevel: 2,
		Args: []convention.ArgDescriptor{
			{Name: "peer_key", Type: "string", Required: true, MaxLength: 64},
		},
		ProducesTags: []convention.TagRule{
			{Tag: "peering:core", Cardinality: "exactly_one"},
		},
		Antecedents: "none",
	}
}

// TestServerSDK_MinOperatorLevel_Blocked verifies that the Server rejects an incoming
// operation message when the sender's provenance level is below the declared minimum.
// An error fulfillment must be sent back; the handler must NOT be invoked.
func TestServerSDK_MinOperatorLevel_Blocked(t *testing.T) {
	env := setupServerTestEnv(t)
	decl := gatedServerDecl()

	callerKey := env.callerClient.PublicKeyHex()

	// Set caller's level to 1 — below the required 2.
	checker := &staticProvenanceChecker{levels: map[string]int{callerKey: 1}}

	var handlerCalled bool
	srv := convention.NewServer(env.serverClient, decl).
		WithPollInterval(50 * time.Millisecond).
		WithProvenance(checker)

	srv.RegisterHandler("core-peer-establish", func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		handlerCalled = true
		return &convention.Response{Payload: map[string]any{"ok": true}}, nil
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
		Payload:    []byte(`{"peer_key":"` + strings.Repeat("a", 64) + `"}`),
		Tags:       []string{"peering:core"},
	})
	if err != nil {
		t.Fatalf("caller Send: %v", err)
	}

	// Poll for an error fulfillment threaded back to the sent message.
	var errorMsg *protocol.Message
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		result, err := env.callerClient.Read(protocol.ReadRequest{
			CampfireID: env.campfireID,
			Tags:       []string{"convention:error"},
		})
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		for i, msg := range result.Messages {
			for _, ant := range msg.Antecedents {
				if ant == sentMsg.ID {
					errorMsg = &result.Messages[i]
					break
				}
			}
			if errorMsg != nil {
				break
			}
		}
		if errorMsg != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	wg.Wait()

	if errorMsg == nil {
		t.Fatal("expected a convention:error fulfillment but none arrived")
	}
	if handlerCalled {
		t.Error("handler must NOT be called when provenance gate rejects the request")
	}

	// Verify the error payload contains the provenance rejection message.
	errText, parseErr := convention.ParseErrorResponse(errorMsg)
	if parseErr != nil {
		t.Fatalf("ParseErrorResponse: %v", parseErr)
	}
	if !strings.Contains(errText, "operator provenance level") {
		t.Errorf("error payload = %q, want it to mention 'operator provenance level'", errText)
	}
	if !strings.Contains(errText, "requires level 2") {
		t.Errorf("error payload = %q, want 'requires level 2'", errText)
	}
}

// TestServerSDK_MinOperatorLevel_Allowed verifies that the Server dispatches to the
// handler when the sender's provenance level meets the declared minimum.
func TestServerSDK_MinOperatorLevel_Allowed(t *testing.T) {
	env := setupServerTestEnv(t)
	decl := gatedServerDecl()

	callerKey := env.callerClient.PublicKeyHex()

	// Set caller's level to 2 — exactly the minimum required.
	checker := &staticProvenanceChecker{levels: map[string]int{callerKey: 2}}

	var mu sync.Mutex
	var handlerCalled bool

	srv := convention.NewServer(env.serverClient, decl).
		WithPollInterval(50 * time.Millisecond).
		WithProvenance(checker)

	srv.RegisterHandler("core-peer-establish", func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		mu.Lock()
		handlerCalled = true
		mu.Unlock()
		return &convention.Response{Payload: map[string]any{"ok": true}}, nil
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
		Payload:    []byte(`{"peer_key":"` + strings.Repeat("a", 64) + `"}`),
		Tags:       []string{"peering:core"},
	})
	if err != nil {
		t.Fatalf("caller Send: %v", err)
	}

	// Poll for a successful fulfillment (not error).
	var successMsg *protocol.Message
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		result, err := env.callerClient.Read(protocol.ReadRequest{
			CampfireID: env.campfireID,
			Tags:       []string{"fulfills"},
		})
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		for i, msg := range result.Messages {
			if convention.IsErrorResponse(&result.Messages[i]) {
				continue
			}
			for _, ant := range msg.Antecedents {
				if ant == sentMsg.ID {
					successMsg = &result.Messages[i]
					break
				}
			}
			if successMsg != nil {
				break
			}
		}
		if successMsg != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	wg.Wait()

	if successMsg == nil {
		t.Fatal("expected a successful fulfillment but none arrived")
	}
	mu.Lock()
	defer mu.Unlock()
	if !handlerCalled {
		t.Error("handler must be called when provenance level is sufficient")
	}
}

// TestServerSDK_MinOperatorLevel_NoCheckerDefaultsToZero verifies that when no
// ProvenanceChecker is attached and min_operator_level > 0, the Server rejects.
func TestServerSDK_MinOperatorLevel_NoCheckerDefaultsToZero(t *testing.T) {
	env := setupServerTestEnv(t)
	decl := gatedServerDecl()

	var handlerCalled bool
	// No WithProvenance — sender defaults to level 0.
	srv := convention.NewServer(env.serverClient, decl).
		WithPollInterval(50 * time.Millisecond)

	srv.RegisterHandler("core-peer-establish", func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		handlerCalled = true
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

	sentMsg, err := env.callerClient.Send(protocol.SendRequest{
		CampfireID: env.campfireID,
		Payload:    []byte(`{"peer_key":"` + strings.Repeat("a", 64) + `"}`),
		Tags:       []string{"peering:core"},
	})
	if err != nil {
		t.Fatalf("caller Send: %v", err)
	}

	// Expect an error fulfillment.
	var errorFound bool
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		result, err := env.callerClient.Read(protocol.ReadRequest{
			CampfireID: env.campfireID,
			Tags:       []string{"convention:error"},
		})
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		for _, msg := range result.Messages {
			for _, ant := range msg.Antecedents {
				if ant == sentMsg.ID {
					errorFound = true
				}
			}
		}
		if errorFound {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	wg.Wait()

	if !errorFound {
		t.Fatal("expected rejection (convention:error) when no checker and min_operator_level=2")
	}
	if handlerCalled {
		t.Error("handler must NOT be called when provenance gate rejects")
	}
}
