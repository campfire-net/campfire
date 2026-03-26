package cmd

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/trust"
)

// countingChainStore wraps a trust.ChainStore and counts calls to ListMessages.
// It is used exclusively to verify that a warm ChainWalker cache does not
// re-issue store reads on subsequent listConventionOperations calls.
type countingChainStore struct {
	trust.ChainStore
	listMessagesCalls atomic.Int64
}

func (c *countingChainStore) ListMessages(campfireID string, afterTimestamp int64, filter ...store.MessageFilter) ([]store.MessageRecord, error) {
	c.listMessagesCalls.Add(1)
	return c.ChainStore.ListMessages(campfireID, afterTimestamp, filter...)
}

// setupDispatchEnv creates a temporary CF_HOME with an identity, a store with a
// membership, and a campfire that has a convention declaration posted to it.
// Returns the campfireID and cleanup func.
func setupDispatchEnv(t *testing.T, declPayload []byte) (campfireID string, cleanup func()) {
	t.Helper()

	dir := t.TempDir()
	// Override CF_HOME to use temp dir
	t.Setenv("CF_HOME", dir)
	cfHome = "" // reset cached value

	// Generate and save identity
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	idPath := filepath.Join(dir, "identity.json")
	if err := id.Save(idPath); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	// Open store
	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	// Generate a campfire ID
	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	campfireID = cfID.PublicKeyHex()

	// Add membership so resolveCampfireID finds it
	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: dir,
		JoinProtocol: "open",
		Role:         "member",
		JoinedAt:     1,
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	// Post declaration message to campfire
	if declPayload != nil {
		msg, err := message.NewMessage(id.PrivateKey, id.PublicKey, declPayload, []string{convention.ConventionOperationTag}, nil)
		if err != nil {
			t.Fatalf("creating declaration message: %v", err)
		}
		rec := store.MessageRecord{
			ID:         msg.ID,
			CampfireID: campfireID,
			Sender:     msg.SenderHex(),
			Payload:    msg.Payload,
			Tags:       msg.Tags,
			Timestamp:  msg.Timestamp,
			Signature:  msg.Signature,
		}
		if _, err := s.AddMessage(rec); err != nil {
			t.Fatalf("adding declaration: %v", err)
		}
	}

	cleanup = func() {
		cfHome = ""
		os.Unsetenv("CF_HOME")
	}
	return campfireID, cleanup
}

var testDecl = func() []byte {
	d := map[string]any{
		"convention":  "test-conv",
		"version":     "0.1",
		"operation":   "post",
		"description": "Test post operation",
		"produces_tags": []map[string]any{
			{"tag": "test:post", "cardinality": "exactly_one"},
		},
		"args": []map[string]any{
			{"name": "text", "type": "string", "required": true, "max_length": 1000},
		},
		"signing": "member_key",
	}
	b, _ := json.Marshal(d)
	return b
}()

func TestCLIDispatchConventionOp(t *testing.T) {
	campfireID, cleanup := setupDispatchEnv(t, testDecl)
	defer cleanup()

	err := dispatchConventionOp(campfireID[:12], "post", []string{"--text", "hello world"})
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}

	// Verify message was written to store
	s2, err := openStore()
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s2.Close()

	msgs, err := s2.ListMessages(campfireID, 0, store.MessageFilter{Tags: []string{"test:post"}})
	if err != nil {
		t.Fatalf("listing messages: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected dispatched message in store, got none")
	}
}

func TestCLIDispatchUnknownOp(t *testing.T) {
	campfireID, cleanup := setupDispatchEnv(t, testDecl)
	defer cleanup()

	err := dispatchConventionOp(campfireID[:12], "bogus-operation", nil)
	if err == nil {
		t.Fatal("expected error for unknown operation, got nil")
	}
	errStr := err.Error()
	if len(errStr) == 0 {
		t.Fatal("expected non-empty error message")
	}
}

func TestCLIDispatchReservedWord(t *testing.T) {
	// "create" is a reserved word — cobra should route to createCmd, not dispatch.
	// We verify this by checking the rootCmd subcommands include "create".
	_, _, err := rootCmd.Find([]string{"create"})
	if err != nil {
		t.Fatalf("create command not found in rootCmd: %v", err)
	}
	found := false
	for _, sub := range rootCmd.Commands() {
		if sub.Use == "create" || sub.Name() == "create" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("create is not registered as a subcommand of rootCmd")
	}
}

func TestCLIDispatchNoOp_DefaultsToRead(t *testing.T) {
	campfireID, cleanup := setupDispatchEnv(t, nil)
	defer cleanup()

	// dispatchConventionOp with empty operationName should call read subcommand.
	// readCmd.RunE will fail because the test store has no messages, but the
	// important thing is it doesn't error on "no operation" — it reaches readCmd.
	// We can verify indirectly: the function should not return "unknown operation".
	err := dispatchConventionOp(campfireID[:12], "", nil)
	// May succeed (empty read) or fail on transport, but should NOT be an "unknown operation" error
	// Error is acceptable (empty store, transport errors) — just not an "unknown operation" error
	if err != nil && err.Error() == "unknown operation" {
		t.Fatalf("should have defaulted to read, not unknown operation error: %v", err)
	}
}

// TestCLIDispatchNoOp_NoDoubleClose is a regression test for the double-close
// panic: dispatchConventionOp used to call s.Close() explicitly before delegating
// to readCmd when operationName == "". The deferred close at the top of the
// function would then close the already-closed store, causing a panic on the
// underlying SQLite connection. This test verifies the function does not panic
// when the empty-operation path is taken.
func TestCLIDispatchNoOp_NoDoubleClose(t *testing.T) {
	campfireID, cleanup := setupDispatchEnv(t, nil)
	defer cleanup()

	// If double-close is present this will panic — caught by testing's recover.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("dispatchConventionOp panicked (double-close regression): %v", r)
		}
	}()

	// Call 5 times to exercise any lazy-init paths; a double-close panic on the
	// first call is sufficient, but repeated calls stress the deferred-only close.
	for range 5 {
		_ = dispatchConventionOp(campfireID[:12], "", nil)
	}
}

func TestCLITransportAdapter_SendAndRead(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CF_HOME", dir)
	defer func() { cfHome = ""; os.Unsetenv("CF_HOME") }()
	cfHome = ""

	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	adapter := &cliTransportAdapter{agentID: id, store: s}

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire id: %v", err)
	}

	payload := []byte(`{"text":"hello"}`)
	tags := []string{"test:post"}
	ctx := context.Background()
	msgID, err := adapter.SendMessage(ctx, cfID.PublicKeyHex(), payload, tags, nil)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if msgID == "" {
		t.Fatal("expected non-empty message ID")
	}

	// Read back
	msgs, err := adapter.ReadMessages(ctx, cfID.PublicKeyHex(), tags)
	if err != nil {
		t.Fatalf("ReadMessages: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected message in store after send")
	}
	if msgs[0].ID != msgID {
		t.Errorf("message ID mismatch: got %s, want %s", msgs[0].ID, msgID)
	}
}

// Verify convention.Declaration arg type → flag type mapping.
func TestCLIDispatchFlagMapping(t *testing.T) {
	decl := &convention.Declaration{
		Convention: "test",
		Operation:  "post",
		Args: []convention.ArgDescriptor{
			{Name: "text", Type: "string"},
			{Name: "count", Type: "integer"},
			{Name: "verbose", Type: "boolean"},
			{Name: "topics", Type: "string", Repeated: true},
		},
	}
	_ = decl
	// Just verify the types compile — actual flag parsing tested in TestCLIDispatchConventionOp.
}

// setupRegistryEnv creates a test environment with:
//   - An operator root campfire (root registry)
//   - A convention registry campfire (registered in root registry via naming:registration)
//   - A target campfire with no inline declarations
//
// Returns (targetCampfireID, cleanup). The convention registry has a declaration
// for the given payload.
func setupRegistryEnv(t *testing.T, declPayload []byte) (targetCampfireID string, cleanup func()) {
	t.Helper()

	dir := t.TempDir()
	t.Setenv("CF_HOME", dir)
	cfHome = ""

	// Generate identity.
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := id.Save(filepath.Join(dir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	// Open store.
	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	// Generate root registry campfire (operator root).
	rootPub, rootPriv, _ := ed25519.GenerateKey(nil)
	rootCampfireID := hex.EncodeToString(rootPub)

	// Generate convention registry campfire.
	convPub, convPriv, _ := ed25519.GenerateKey(nil)
	convRegistryID := hex.EncodeToString(convPub)

	// Generate target campfire (no inline declarations).
	targetID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating target campfire: %v", err)
	}
	targetCampfireID = targetID.PublicKeyHex()

	// Save operator root config so CFHome() / LoadOperatorRoot() finds it.
	operatorRoot := &naming.OperatorRoot{Name: "test", CampfireID: rootCampfireID}
	if err := naming.SaveOperatorRoot(dir, operatorRoot); err != nil {
		t.Fatalf("saving operator root: %v", err)
	}

	// Add memberships.
	for _, cfID := range []string{rootCampfireID, convRegistryID, targetCampfireID} {
		if err := s.AddMembership(store.Membership{
			CampfireID:   cfID,
			TransportDir: dir,
			JoinProtocol: "open",
			Role:         "member",
			JoinedAt:     1,
		}); err != nil {
			t.Fatalf("adding membership for %s: %v", cfID[:12], err)
		}
	}

	// Root registry: one message signed by root key (proves ownership).
	rootOwnerMsg, err := message.NewMessage(rootPriv, rootPub, []byte("root-init"), []string{"general"}, nil)
	if err != nil {
		t.Fatalf("creating root owner msg: %v", err)
	}
	if _, err := s.AddMessage(store.MessageRecord{
		ID: rootOwnerMsg.ID, CampfireID: rootCampfireID,
		Sender: hex.EncodeToString(rootPub), Payload: rootOwnerMsg.Payload,
		Tags: rootOwnerMsg.Tags, Timestamp: rootOwnerMsg.Timestamp,
		Signature: rootOwnerMsg.Signature,
	}); err != nil {
		t.Fatalf("adding root owner msg: %v", err)
	}

	// Root registry: naming:registration message pointing to convRegistryID.
	regMsg, err := message.NewMessage(rootPriv, rootPub, []byte(convRegistryID), []string{"naming:registration"}, nil)
	if err != nil {
		t.Fatalf("creating registration msg: %v", err)
	}
	if _, err := s.AddMessage(store.MessageRecord{
		ID: regMsg.ID, CampfireID: rootCampfireID,
		Sender: hex.EncodeToString(rootPub), Payload: regMsg.Payload,
		Tags: regMsg.Tags, Timestamp: regMsg.Timestamp,
		Signature: regMsg.Signature,
	}); err != nil {
		t.Fatalf("adding registration msg: %v", err)
	}

	// Convention registry: one general message (establishes the key).
	convInitMsg, err := message.NewMessage(convPriv, convPub, []byte("conv-init"), []string{"general"}, nil)
	if err != nil {
		t.Fatalf("creating conv init msg: %v", err)
	}
	if _, err := s.AddMessage(store.MessageRecord{
		ID: convInitMsg.ID, CampfireID: convRegistryID,
		Sender: hex.EncodeToString(convPub), Payload: convInitMsg.Payload,
		Tags: convInitMsg.Tags, Timestamp: convInitMsg.Timestamp,
		Signature: convInitMsg.Signature,
	}); err != nil {
		t.Fatalf("adding conv init msg: %v", err)
	}

	// Convention registry: convention:operation declaration.
	if declPayload != nil {
		convDeclMsg, err := message.NewMessage(convPriv, convPub, declPayload, []string{convention.ConventionOperationTag}, nil)
		if err != nil {
			t.Fatalf("creating conv decl msg: %v", err)
		}
		if _, err := s.AddMessage(store.MessageRecord{
			ID: convDeclMsg.ID, CampfireID: convRegistryID,
			Sender: hex.EncodeToString(convPub), Payload: convDeclMsg.Payload,
			Tags: convDeclMsg.Tags, Timestamp: convDeclMsg.Timestamp,
			Signature: convDeclMsg.Signature,
		}); err != nil {
			t.Fatalf("adding conv decl msg: %v", err)
		}
	}

	// Reset the singleton walker cache so this test environment's root key
	// does not bleed into subsequent tests.
	resetChainWalkers()
	cleanup = func() {
		resetChainWalkers()
		cfHome = ""
		os.Unsetenv("CF_HOME")
	}
	return targetCampfireID, cleanup
}

// TestListConventionOperations_InlineFallback verifies that when a campfire has
// inline declarations, listConventionOperations returns them without walking
// the trust chain.
func TestListConventionOperations_InlineFallback(t *testing.T) {
	campfireID, cleanupDispatch := setupDispatchEnv(t, testDecl)
	defer cleanupDispatch()

	s, err := openStore()
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	decls, err := listConventionOperations(context.Background(), s, campfireID)
	if err != nil {
		t.Fatalf("listConventionOperations: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("expected 1 inline decl, got %d", len(decls))
	}
	if decls[0].Operation != "post" {
		t.Errorf("operation = %q, want post", decls[0].Operation)
	}
}

// TestListConventionOperations_RegistryFallback verifies that when a campfire has
// no inline declarations, listConventionOperations walks the trust chain and reads
// declarations from the convention registry campfire.
func TestListConventionOperations_RegistryFallback(t *testing.T) {
	targetCampfireID, cleanup := setupRegistryEnv(t, testDecl)
	defer cleanup()

	s, err := openStore()
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	decls, err := listConventionOperations(context.Background(), s, targetCampfireID)
	if err != nil {
		t.Fatalf("listConventionOperations: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("expected 1 decl from registry, got %d", len(decls))
	}
	if decls[0].Operation != "post" {
		t.Errorf("operation = %q, want post", decls[0].Operation)
	}
}

// TestListConventionOperations_WalkerReuse verifies that two calls to
// listConventionOperations with the same operator root reuse the singleton
// ChainWalker, so the second call is served from the in-memory cache rather
// than issuing new store reads.
//
// The test injects a countingStore that increments a counter every time
// ListMessages is called. On the first call the chain is walked (counter > 0).
// On the second call the cached chain is returned; because the walker is reused
// the counter must not increase further.
func TestListConventionOperations_WalkerReuse(t *testing.T) {
	// Reset singleton so this test is independent of execution order.
	resetChainWalkers()
	defer resetChainWalkers()

	targetCampfireID, cleanup := setupRegistryEnv(t, testDecl)
	defer cleanup()

	s, err := openStore()
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	// Install a chain-store spy via the test injection hook.
	// The spy counts every ListMessages call made by the ChainWalker.
	// Convention reads (inline check, registry declaration reads) go through
	// cliStoreReader — not through the chain store — so they are not counted.
	var spy *countingChainStore
	chainStoreWrapper = func(cs trust.ChainStore) trust.ChainStore {
		spy = &countingChainStore{ChainStore: cs}
		return spy
	}
	defer func() { chainStoreWrapper = nil }()

	// First call — creates the singleton walker and walks the trust chain.
	// The chain walk issues ListMessages calls against the chain store;
	// counter must be > 0 after this call.
	decls, err := listConventionOperations(context.Background(), s, targetCampfireID)
	if err != nil {
		t.Fatalf("first listConventionOperations: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("expected 1 decl on first call, got %d", len(decls))
	}

	if spy == nil {
		t.Fatal("chainStoreWrapper was never invoked — spy not installed")
	}

	// Snapshot the walker map after the first call.
	chainWalkersMu.Lock()
	walkerCountAfterFirst := len(chainWalkers)
	chainWalkersMu.Unlock()

	if walkerCountAfterFirst == 0 {
		t.Fatal("expected at least one walker in singleton map after first call")
	}

	callsAfterFirst := spy.listMessagesCalls.Load()
	if callsAfterFirst == 0 {
		t.Fatal("expected chain store ListMessages to be called during first chain walk, got 0 calls")
	}

	// Remove the wrapper: the walker is now cached and re-creation won't fire.
	// The existing walker holds the spy as its chain store already.
	chainStoreWrapper = nil

	// Second call — must reuse the same walker whose cache is still warm.
	// WalkChain returns the cached Chain without re-fetching from the chain store.
	// spy.listMessagesCalls must not increase.
	decls2, err := listConventionOperations(context.Background(), s, targetCampfireID)
	if err != nil {
		t.Fatalf("second listConventionOperations: %v", err)
	}
	if len(decls2) != 1 {
		t.Fatalf("expected 1 decl on second call, got %d", len(decls2))
	}

	chainWalkersMu.Lock()
	walkerCountAfterSecond := len(chainWalkers)
	chainWalkersMu.Unlock()

	if walkerCountAfterSecond != walkerCountAfterFirst {
		t.Errorf("walker map grew from %d to %d — a new walker was created instead of reusing the singleton",
			walkerCountAfterFirst, walkerCountAfterSecond)
	}

	callsAfterSecond := spy.listMessagesCalls.Load()
	if callsAfterSecond != callsAfterFirst {
		t.Errorf("chain store ListMessages call count increased from %d to %d on second call — "+
			"walker cache was not hit, chain was re-fetched from the store",
			callsAfterFirst, callsAfterSecond)
	}
}

// TestListConventionOperations_NoOperatorRoot verifies that when no operator root
// is configured, listConventionOperations returns an empty list gracefully (offline).
func TestListConventionOperations_NoOperatorRoot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CF_HOME", dir)
	cfHome = ""
	defer func() { cfHome = ""; os.Unsetenv("CF_HOME") }()

	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := id.Save(filepath.Join(dir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	campfireID := id.PublicKeyHex()
	if err := s.AddMembership(store.Membership{
		CampfireID: campfireID, TransportDir: dir,
		JoinProtocol: "open", Role: "member", JoinedAt: 1,
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	// No operator root configured — should return empty list, not error.
	decls, err := listConventionOperations(context.Background(), s, campfireID)
	if err != nil {
		t.Fatalf("expected no error without operator root, got: %v", err)
	}
	if len(decls) != 0 {
		t.Errorf("expected 0 decls (no operator root), got %d", len(decls))
	}
}
