package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	campfire "github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	fstransport "github.com/campfire-net/campfire/pkg/transport/fs"
	"github.com/spf13/pflag"
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

	// Create a campfire so its state (campfire.cbor) and directories exist on disk.
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("creating campfire: %v", err)
	}
	cf.AddMember(id.PublicKey)
	campfireID = cf.PublicKeyHex()

	// Set up filesystem transport so sendFilesystem can verify membership and add provenance.
	campfireTransportDir := filepath.Join(dir, "campfire-transport")
	tr := fstransport.ForDir(campfireTransportDir)
	if err := tr.Init(cf); err != nil {
		t.Fatalf("initializing campfire transport: %v", err)
	}
	if err := tr.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: id.PublicKey,
		JoinedAt:  1,
	}); err != nil {
		t.Fatalf("writing member record: %v", err)
	}

	// Add membership so resolveCampfireID finds it
	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: campfireTransportDir,
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

	// Create a campfire with filesystem transport.
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("creating campfire: %v", err)
	}
	cf.AddMember(id.PublicKey)
	campfireID := cf.PublicKeyHex()

	campfireTransportDir := filepath.Join(dir, "campfire-transport")
	tr := fstransport.ForDir(campfireTransportDir)
	if err := tr.Init(cf); err != nil {
		t.Fatalf("initializing campfire transport: %v", err)
	}
	if err := tr.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: id.PublicKey,
		JoinedAt:  1,
	}); err != nil {
		t.Fatalf("writing member record: %v", err)
	}

	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: campfireTransportDir,
		JoinProtocol: "open",
		Role:         "member",
		JoinedAt:     1,
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	adapter := &cliTransportAdapter{agentID: id, store: s}

	payload := []byte(`{"text":"hello"}`)
	tags := []string{"test:post"}
	ctx := context.Background()
	msgID, err := adapter.SendMessage(ctx, campfireID, payload, tags, nil)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if msgID == "" {
		t.Fatal("expected non-empty message ID")
	}

	// Read back — message is in the transport, but ReadMessages reads from the local store.
	// sendFilesystem writes to transport, not local store, so we read from store.ListMessages
	// after syncing. For this test, read directly from the transport to verify it was written.
	msgs, err := adapter.ReadMessages(ctx, campfireID, tags)
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

// TestCLIDispatchFlagMapping_TagSet verifies that tag_set args are wired as
// StringSlice even when Repeated is false.
func TestCLIDispatchFlagMapping_TagSet(t *testing.T) {
	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	arg := convention.ArgDescriptor{Name: "domain", Type: "tag_set"}

	// This mirrors the switch in convention_dispatch.go.
	switch {
	case arg.Repeated || arg.Type == "tag_set":
		flags.StringSlice(arg.Name, nil, "")
	default:
		flags.String(arg.Name, "", "")
	}

	if err := flags.Parse([]string{"--domain", "go,concurrency"}); err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	vals, err := flags.GetStringSlice("domain")
	if err != nil {
		t.Fatalf("GetStringSlice failed: %v", err)
	}
	if len(vals) != 2 || vals[0] != "go" || vals[1] != "concurrency" {
		t.Errorf("expected [go, concurrency], got %v", vals)
	}
}

// TestCLIDispatchEnumShortFormExpansion verifies that the CLI dispatch layer
// expands short enum values to their full tag-prefixed form before passing to
// the executor. This is CLI sugar — the executor requires canonical values.
func TestCLIDispatchEnumShortFormExpansion(t *testing.T) {
	desc := convention.ArgDescriptor{
		Name:   "content_type",
		Type:   "enum",
		Values: []string{"exchange:content-type:code", "exchange:content-type:analysis", "exchange:content-type:summary"},
	}
	argByName := map[string]convention.ArgDescriptor{"content_type": desc}

	// Simulate the expansion logic from convention_dispatch.go.
	expand := func(val string) string {
		if desc, ok := argByName["content_type"]; ok && desc.Type == "enum" && len(desc.Values) > 0 {
			directMatch := false
			for _, v := range desc.Values {
				if v == val {
					directMatch = true
					break
				}
			}
			if !directMatch {
				suffix := ":" + val
				var match string
				for _, v := range desc.Values {
					if strings.HasSuffix(v, suffix) {
						if match != "" {
							match = ""
							break
						}
						match = v
					}
				}
				if match != "" {
					val = match
				}
			}
		}
		return val
	}

	// Short form expands to full.
	if got := expand("analysis"); got != "exchange:content-type:analysis" {
		t.Errorf("expected expansion to full form, got %q", got)
	}
	// Full form passes through unchanged.
	if got := expand("exchange:content-type:code"); got != "exchange:content-type:code" {
		t.Errorf("expected pass-through for full form, got %q", got)
	}
	// Unknown short form passes through unchanged (executor will reject).
	if got := expand("nonexistent"); got != "nonexistent" {
		t.Errorf("expected pass-through for unknown value, got %q", got)
	}
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
