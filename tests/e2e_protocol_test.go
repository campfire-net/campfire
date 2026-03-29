// Package tests — End-to-end protocol round-trip test.
//
// TestE2E_ConventionRoundTrip proves the full API layering refactor works
// across all layers:
//
//  1. Two identities (caller + server) share a filesystem campfire.
//  2. An "echo" convention declaration defines a single "message" string arg.
//  3. A convention.Server registers a handler that echoes back the message.
//  4. The caller sends via protocol.Client.Send with the convention operation tag.
//  5. The Server receives, dispatches, and sends an auto-threaded response.
//  6. protocol.Client.Await verifies the sender can receive the response.
//
// No mocks — real filesystem transport throughout.
package tests

import (
	"context"
	"os"
	"path/filepath"
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

// setupE2EEnv creates a shared filesystem campfire with two members (server + caller).
// Returns protocol.Client instances for each and the campfire ID.
func setupE2EEnv(t *testing.T) (callerClient, serverClient *protocol.Client, campfireID string) {
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
	campfireID = cfID.PublicKeyHex()

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

	// Register both identities as full members.
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

	callerStore, err := store.Open(filepath.Join(storeDir, "caller.db"))
	if err != nil {
		t.Fatalf("opening caller store: %v", err)
	}
	t.Cleanup(func() { callerStore.Close() })

	if err := serverStore.AddMembership(membership); err != nil {
		t.Fatalf("server store add membership: %v", err)
	}
	if err := callerStore.AddMembership(membership); err != nil {
		t.Fatalf("caller store add membership: %v", err)
	}

	callerClient = protocol.New(callerStore, callerID)
	serverClient = protocol.New(serverStore, serverID)
	return callerClient, serverClient, campfireID
}

// echoDecl returns a Declaration for a simple "echo" operation that echoes
// the "message" arg back to the caller.
func echoDecl() *convention.Declaration {
	return &convention.Declaration{
		Convention: "test-echo",
		Operation:  "echo",
		Signing:    "member_key",
		Args: []convention.ArgDescriptor{
			{Name: "message", Type: "string", Required: true, MaxLength: 1024},
		},
		ProducesTags: []convention.TagRule{
			{Tag: "test-echo:echo", Cardinality: "exactly_one"},
		},
		Antecedents: "none",
	}
}

// TestE2E_ConventionRoundTrip exercises the full layering:
//   - protocol.Client.Send (caller → campfire)
//   - convention.Server polls via protocol.Client.Read
//   - handler dispatches and sends auto-threaded response
//   - protocol.Client.Await (caller waits for fulfillment)
func TestE2E_ConventionRoundTrip(t *testing.T) {
	callerClient, serverClient, campfireID := setupE2EEnv(t)
	decl := echoDecl()

	// Track what the handler received.
	var mu sync.Mutex
	var handlerGotMessage string

	srv := convention.NewServer(serverClient, decl).
		WithPollInterval(50 * time.Millisecond)

	srv.RegisterHandler("echo", func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		if msg, ok := req.Args["message"].(string); ok {
			handlerGotMessage = msg
		}
		return &convention.Response{
			Payload: map[string]any{"echo": req.Args["message"]},
		}, nil
	})

	// Start the server in the background.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		srv.Serve(ctx, campfireID) //nolint:errcheck
	}()

	// Give server a moment to start its first poll.
	time.Sleep(20 * time.Millisecond)

	// --- Step 1: Caller sends a convention operation message via Client.Send ---
	sentMsg, err := callerClient.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte(`{"message":"hello from caller"}`),
		Tags:       []string{"test-echo:echo"},
	})
	if err != nil {
		t.Fatalf("caller Send: %v", err)
	}
	if sentMsg == nil || sentMsg.ID == "" {
		t.Fatal("Send returned nil or empty-ID message")
	}
	t.Logf("sent message ID: %s", sentMsg.ID)

	// --- Step 2: Client.Await — blocks until the server's auto-threaded response arrives ---
	fulfillment, err := callerClient.Await(protocol.AwaitRequest{
		CampfireID:   campfireID,
		TargetMsgID:  sentMsg.ID,
		Timeout:      8 * time.Second,
		PollInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	if fulfillment == nil {
		t.Fatal("Await returned nil fulfillment")
	}

	// --- Step 3: Verify the fulfillment is correctly threaded ---
	hasFulfillsTag := false
	for _, tag := range fulfillment.Tags {
		if tag == "fulfills" {
			hasFulfillsTag = true
			break
		}
	}
	if !hasFulfillsTag {
		t.Errorf("fulfillment message missing 'fulfills' tag; tags: %v", fulfillment.Tags)
	}

	hasCorrectAntecedent := false
	for _, ant := range fulfillment.Antecedents {
		if ant == sentMsg.ID {
			hasCorrectAntecedent = true
			break
		}
	}
	if !hasCorrectAntecedent {
		t.Errorf("fulfillment antecedents %v do not include request ID %s", fulfillment.Antecedents, sentMsg.ID)
	}

	// --- Step 4: Verify the handler was called with the correct arg ---
	cancel()
	wg.Wait()

	mu.Lock()
	gotMsg := handlerGotMessage
	mu.Unlock()

	if gotMsg != "hello from caller" {
		t.Errorf("handler received message %q, want %q", gotMsg, "hello from caller")
	}
}

// TestE2E_Read verifies that protocol.Client.Read returns messages written
// by another identity on the same filesystem campfire (sync-before-query).
func TestE2E_Read(t *testing.T) {
	callerClient, serverClient, campfireID := setupE2EEnv(t)

	// Server sends a message; caller reads it back via protocol.Client.Read.
	_, err := serverClient.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("server broadcast"),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("server Send: %v", err)
	}

	result, err := callerClient.Read(protocol.ReadRequest{
		CampfireID: campfireID,
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("caller Read: %v", err)
	}

	found := false
	for _, msg := range result.Messages {
		if string(msg.Payload) == "server broadcast" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("caller Read did not find server's message (%d messages returned)", len(result.Messages))
	}
}
