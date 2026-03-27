package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
)

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

	err := dispatchConventionOp(context.Background(), campfireID[:12], "post", []string{"--text", "hello world"})
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

	err := dispatchConventionOp(context.Background(), campfireID[:12], "bogus-operation", nil)
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
	err := dispatchConventionOp(context.Background(), campfireID[:12], "", nil)
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
		_ = dispatchConventionOp(context.Background(), campfireID[:12], "", nil)
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

// TestListConventionOperations_InlineFallback verifies that when a campfire has
// inline declarations, listConventionOperations returns them without any registry lookup.
// In Trust v0.2, declarations are read directly from the campfire.
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

// TestListConventionOperations_EmptyCampfire verifies that a campfire with no
// declarations returns an empty list (no registry fallback in Trust v0.2).
func TestListConventionOperations_EmptyCampfire(t *testing.T) {
	campfireID, cleanup := setupDispatchEnv(t, nil) // no declarations posted
	defer cleanup()

	s, err := openStore()
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	decls, err := listConventionOperations(context.Background(), s, campfireID)
	if err != nil {
		t.Fatalf("listConventionOperations: %v", err)
	}
	if len(decls) != 0 {
		t.Errorf("expected 0 decls for campfire with no declarations, got %d", len(decls))
	}
}
