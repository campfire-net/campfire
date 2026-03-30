package naming_test

// Tests for NewResolverFromClient, PublishAPI, direct-read resolution, and auto-join.
//
// These are integration tests using a real protocol.Client with filesystem transport.
// They verify:
//   - NewResolverFromClient returns a *Resolver backed by a protocol.Client.
//   - Resolve uses direct-read (reads tagged messages, not futures).
//   - ListChildren uses direct-read.
//   - Auto-join: hierarchical walk auto-joins open registries.
//   - Invite-only registries return ErrInviteOnly.
//   - PublishAPI sends a naming:api message to the campfire, readable by ListAPI.

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/campfire/pkg/protocol"
)

// setupNamingTestCampfire creates a campfire using protocol.Init and Client.Create.
// Returns the client and campfire ID.
func setupNamingTestCampfire(t *testing.T) (*protocol.Client, string) {
	t.Helper()

	configDir := t.TempDir()
	client, err := protocol.Init(configDir)
	if err != nil {
		t.Fatalf("protocol.Init: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	transportDir := t.TempDir()
	beaconDir := t.TempDir()

	result, err := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir: beaconDir,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	return client, result.CampfireID
}

// TestPublishAPI verifies that PublishAPI sends a naming:api message that is
// readable via client.Read with Tags=["naming:api"].
func TestPublishAPI(t *testing.T) {
	client, campfireID := setupNamingTestCampfire(t)

	decl := naming.APIDeclaration{
		Endpoint:    "/search",
		Description: "search the registry",
		Args: []naming.APIArg{
			{Name: "q", Type: "string", Required: true, Description: "query"},
		},
		ResultTags: []string{"naming:search-result"},
	}

	if err := naming.PublishAPI(client, campfireID, decl); err != nil {
		t.Fatalf("PublishAPI: %v", err)
	}

	// Read back via client.Read filtering by naming:api tag.
	result, err := client.Read(protocol.ReadRequest{
		CampfireID: campfireID,
		Tags:       []string{"naming:api"},
		SkipSync:   false,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(result.Messages) == 0 {
		t.Fatal("no naming:api messages found after PublishAPI")
	}

	var gotDecl naming.APIDeclaration
	if err := json.Unmarshal(result.Messages[0].Payload, &gotDecl); err != nil {
		t.Fatalf("unmarshalling APIDeclaration: %v", err)
	}

	if gotDecl.Endpoint != decl.Endpoint {
		t.Errorf("Endpoint = %q, want %q", gotDecl.Endpoint, decl.Endpoint)
	}
	if gotDecl.Description != decl.Description {
		t.Errorf("Description = %q, want %q", gotDecl.Description, decl.Description)
	}
	if len(gotDecl.Args) != 1 || gotDecl.Args[0].Name != "q" {
		t.Errorf("Args = %v, want [{Name:q ...}]", gotDecl.Args)
	}
}

// TestPublishAPI_MultipleDeclarations verifies that multiple PublishAPI calls
// result in multiple naming:api messages.
func TestPublishAPI_MultipleDeclarations(t *testing.T) {
	client, campfireID := setupNamingTestCampfire(t)

	decls := []naming.APIDeclaration{
		{Endpoint: "/register", Description: "register a name"},
		{Endpoint: "/lookup", Description: "look up a name"},
	}

	for _, d := range decls {
		if err := naming.PublishAPI(client, campfireID, d); err != nil {
			t.Fatalf("PublishAPI(%s): %v", d.Endpoint, err)
		}
	}

	result, err := client.Read(protocol.ReadRequest{
		CampfireID: campfireID,
		Tags:       []string{"naming:api"},
		SkipSync:   false,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(result.Messages) != 2 {
		t.Errorf("got %d naming:api messages, want 2", len(result.Messages))
	}
}

// TestNewResolverFromClient verifies that NewResolverFromClient returns a
// *Resolver and that ListAPI works via the client transport, reading messages
// published by PublishAPI.
func TestNewResolverFromClient(t *testing.T) {
	client, campfireID := setupNamingTestCampfire(t)

	// Publish an API declaration.
	decl := naming.APIDeclaration{
		Endpoint:    "/resolve",
		Description: "resolve a name",
	}
	if err := naming.PublishAPI(client, campfireID, decl); err != nil {
		t.Fatalf("PublishAPI: %v", err)
	}

	// Create resolver using NewResolverFromClient.
	// rootID = campfireID here since we only test ListAPI (no name resolution needed).
	resolver := naming.NewResolverFromClient(client, campfireID)
	if resolver == nil {
		t.Fatal("NewResolverFromClient returned nil")
	}

	// Use Resolver.ListAPI to retrieve the declarations.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	decls, err := resolver.ListAPI(ctx, campfireID)
	if err != nil {
		t.Fatalf("resolver.ListAPI: %v", err)
	}

	if len(decls) == 0 {
		t.Fatal("ListAPI returned no declarations after PublishAPI")
	}

	found := false
	for _, d := range decls {
		if d.Endpoint == "/resolve" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ListAPI did not return published endpoint /resolve; got %v", decls)
	}
}

// TestNewResolverFromClient_TwoClients verifies that NewResolverFromClient
// can be used by client B to read API declarations published by client A to
// the same campfire (cross-client scenario using filesystem transport sync).
func TestNewResolverFromClient_TwoClients(t *testing.T) {
	transportBaseDir := t.TempDir()

	// Client A: creator
	configDirA := t.TempDir()
	clientA, err := protocol.Init(configDirA)
	if err != nil {
		t.Fatalf("Init A: %v", err)
	}
	t.Cleanup(func() { clientA.Close() })

	beaconDir := t.TempDir()
	result, err := clientA.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: transportBaseDir},
		BeaconDir: beaconDir,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	campfireID := result.CampfireID
	campfireDir := filepath.Join(transportBaseDir, campfireID)

	// Client B: joiner
	configDirB := t.TempDir()
	clientB, err := protocol.Init(configDirB)
	if err != nil {
		t.Fatalf("Init B: %v", err)
	}
	t.Cleanup(func() { clientB.Close() })

	if _, err := clientB.Join(protocol.JoinRequest{
		CampfireID: campfireID,
		Transport:  protocol.FilesystemTransport{Dir: campfireDir},
	}); err != nil {
		t.Fatalf("Join B: %v", err)
	}

	// A publishes an API declaration.
	declA := naming.APIDeclaration{
		Endpoint:    "/search",
		Description: "search by A",
	}
	if err := naming.PublishAPI(clientA, campfireID, declA); err != nil {
		t.Fatalf("PublishAPI A: %v", err)
	}

	// B creates a resolver and reads A's API declaration.
	resolver := naming.NewResolverFromClient(clientB, campfireID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	decls, err := resolver.ListAPI(ctx, campfireID)
	if err != nil {
		t.Fatalf("resolver.ListAPI (B reads A's declarations): %v", err)
	}

	found := false
	for _, d := range decls {
		if d.Endpoint == "/search" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("B's resolver.ListAPI did not find A's /search declaration; got %v", decls)
	}
}

// TestClientTransportDirectReadResolve verifies that clientTransport.Resolve
// uses direct-read (tagged registration messages) instead of futures.
// Register a name, resolve via NewResolverFromClient — no server process needed.
func TestClientTransportDirectReadResolve(t *testing.T) {
	client, nsID := setupNamingTestCampfire(t)
	ctx := context.Background()

	// Create a target campfire.
	target, err := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: t.TempDir()},
		BeaconDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}

	// Register a name via direct-write (naming.Register).
	_, err = naming.Register(ctx, client, nsID, "myservice", target.CampfireID, nil)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Resolve via clientTransport (NewResolverFromClient). This uses direct-read
	// internally — no futures, no server process.
	resolver := naming.NewResolverFromClient(client, nsID)
	result, err := resolver.ResolveURI(ctx, "cf://myservice")
	if err != nil {
		t.Fatalf("ResolveURI: %v", err)
	}

	if result.CampfireID != target.CampfireID {
		t.Errorf("CampfireID = %s, want %s", result.CampfireID, target.CampfireID)
	}
}

// TestClientTransportDirectReadList verifies that clientTransport.ListChildren
// uses direct-read to list all registered names.
func TestClientTransportDirectReadList(t *testing.T) {
	client, nsID := setupNamingTestCampfire(t)
	ctx := context.Background()

	// Register several names.
	names := []string{"alpha", "beta", "gamma"}
	for _, name := range names {
		target, err := client.Create(protocol.CreateRequest{
			Transport: protocol.FilesystemTransport{Dir: t.TempDir()},
			BeaconDir: t.TempDir(),
		})
		if err != nil {
			t.Fatalf("Create target: %v", err)
		}
		_, err = naming.Register(ctx, client, nsID, name, target.CampfireID, nil)
		if err != nil {
			t.Fatalf("Register %s: %v", name, err)
		}
	}

	// List via resolver (direct-read, no futures).
	resolver := naming.NewResolverFromClient(client, nsID)
	entries, err := resolver.ListChildren(ctx, nsID, "")
	if err != nil {
		t.Fatalf("ListChildren: %v", err)
	}

	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}

	found := make(map[string]bool)
	for _, e := range entries {
		found[e.Name] = true
	}
	for _, name := range names {
		if !found[name] {
			t.Errorf("name %q not found in ListChildren results", name)
		}
	}

	// Test prefix filtering.
	filtered, err := resolver.ListChildren(ctx, nsID, "al")
	if err != nil {
		t.Fatalf("ListChildren with prefix: %v", err)
	}
	if len(filtered) != 1 || filtered[0].Name != "alpha" {
		t.Errorf("ListChildren(prefix=al) = %v, want [alpha]", filtered)
	}
}

// TestClientTransportAutoJoin verifies that the resolver auto-joins open
// registries during hierarchical name resolution. Two campfires: root and
// child. Client B creates both and registers child in root. Client C
// (only a member of root) resolves a multi-segment name — auto-join kicks
// in to join the child campfire.
func TestClientTransportAutoJoin(t *testing.T) {
	// Shared filesystem base dirs and a shared beacon dir.
	rootTransportDir := t.TempDir()
	childTransportDir := t.TempDir()
	sharedBeaconDir := t.TempDir()

	// Client A: creates root and child campfires.
	configDirA := t.TempDir()
	clientA, err := protocol.Init(configDirA)
	if err != nil {
		t.Fatalf("Init A: %v", err)
	}
	t.Cleanup(func() { clientA.Close() })

	rootResult, err := clientA.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: rootTransportDir},
		BeaconDir: sharedBeaconDir,
	})
	if err != nil {
		t.Fatalf("Create root: %v", err)
	}
	rootID := rootResult.CampfireID

	childResult, err := clientA.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: childTransportDir},
		BeaconDir: sharedBeaconDir,
	})
	if err != nil {
		t.Fatalf("Create child: %v", err)
	}
	childID := childResult.CampfireID

	// Create a leaf campfire that the child campfire resolves to.
	leafTransportDir := t.TempDir()
	leafResult, err := clientA.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: leafTransportDir},
		BeaconDir: sharedBeaconDir,
	})
	if err != nil {
		t.Fatalf("Create leaf: %v", err)
	}
	leafID := leafResult.CampfireID

	ctx := context.Background()

	// Register "child" in root → points to childID.
	_, err = naming.Register(ctx, clientA, rootID, "child", childID, nil)
	if err != nil {
		t.Fatalf("Register child: %v", err)
	}

	// Register "leaf" in child → points to leafID.
	_, err = naming.Register(ctx, clientA, childID, "leaf", leafID, nil)
	if err != nil {
		t.Fatalf("Register leaf: %v", err)
	}

	// Client B: only joins root. Should auto-join child during resolution.
	configDirB := t.TempDir()
	clientB, err := protocol.Init(configDirB)
	if err != nil {
		t.Fatalf("Init B: %v", err)
	}
	t.Cleanup(func() { clientB.Close() })

	// B joins root.
	rootCampfireDir := filepath.Join(rootTransportDir, rootID)
	_, err = clientB.Join(protocol.JoinRequest{
		CampfireID: rootID,
		Transport:  &protocol.FilesystemTransport{Dir: rootCampfireDir},
	})
	if err != nil {
		t.Fatalf("B.Join root: %v", err)
	}

	// Verify B is NOT a member of child.
	m, err := clientB.GetMembership(childID)
	if err != nil {
		t.Fatalf("GetMembership child: %v", err)
	}
	if m != nil {
		t.Fatal("B should not be a member of child yet")
	}

	// B resolves "child.leaf" — should auto-join child campfire.
	resolver := naming.NewResolverFromClient(clientB, rootID, naming.ResolverClientOptions{
		BeaconDir: sharedBeaconDir,
	})
	result, err := resolver.ResolveURI(ctx, "cf://child.leaf")
	if err != nil {
		t.Fatalf("ResolveURI: %v", err)
	}

	if result.CampfireID != leafID {
		t.Errorf("CampfireID = %s, want %s", result.CampfireID, leafID)
	}

	// Verify B is now a member of child (auto-joined).
	m, err = clientB.GetMembership(childID)
	if err != nil {
		t.Fatalf("GetMembership child after resolve: %v", err)
	}
	if m == nil {
		t.Error("B should be a member of child after auto-join")
	}
}

// TestClientTransportInviteOnlyError verifies that resolving through an
// invite-only campfire returns a clear error (ErrInviteOnly).
func TestClientTransportInviteOnlyError(t *testing.T) {
	// Shared dirs.
	rootTransportDir := t.TempDir()
	childTransportDir := t.TempDir()
	sharedBeaconDir := t.TempDir()

	// Client A: creates root (open) and child (invite-only).
	configDirA := t.TempDir()
	clientA, err := protocol.Init(configDirA)
	if err != nil {
		t.Fatalf("Init A: %v", err)
	}
	t.Cleanup(func() { clientA.Close() })

	rootResult, err := clientA.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: rootTransportDir},
		BeaconDir: sharedBeaconDir,
	})
	if err != nil {
		t.Fatalf("Create root: %v", err)
	}
	rootID := rootResult.CampfireID

	childResult, err := clientA.Create(protocol.CreateRequest{
		Transport:    protocol.FilesystemTransport{Dir: childTransportDir},
		BeaconDir:    sharedBeaconDir,
		JoinProtocol: "invite-only",
	})
	if err != nil {
		t.Fatalf("Create child: %v", err)
	}
	childID := childResult.CampfireID

	ctx := context.Background()

	// Register "private" in root → points to invite-only child.
	_, err = naming.Register(ctx, clientA, rootID, "private", childID, nil)
	if err != nil {
		t.Fatalf("Register private: %v", err)
	}

	// Register something in child so we have a target.
	leafResult, err := clientA.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: t.TempDir()},
		BeaconDir: sharedBeaconDir,
	})
	if err != nil {
		t.Fatalf("Create leaf: %v", err)
	}
	_, err = naming.Register(ctx, clientA, childID, "secret", leafResult.CampfireID, nil)
	if err != nil {
		t.Fatalf("Register secret: %v", err)
	}

	// Client B: joins root only.
	configDirB := t.TempDir()
	clientB, err := protocol.Init(configDirB)
	if err != nil {
		t.Fatalf("Init B: %v", err)
	}
	t.Cleanup(func() { clientB.Close() })

	rootCampfireDir := filepath.Join(rootTransportDir, rootID)
	_, err = clientB.Join(protocol.JoinRequest{
		CampfireID: rootID,
		Transport:  &protocol.FilesystemTransport{Dir: rootCampfireDir},
	})
	if err != nil {
		t.Fatalf("B.Join root: %v", err)
	}

	// B tries to resolve "private.secret" — should fail with invite-only error.
	resolver := naming.NewResolverFromClient(clientB, rootID, naming.ResolverClientOptions{
		BeaconDir: sharedBeaconDir,
	})
	_, err = resolver.ResolveURI(ctx, "cf://private.secret")
	if err == nil {
		t.Fatal("expected error resolving through invite-only campfire")
	}

	if !errors.Is(err, naming.ErrInviteOnly) && !strings.Contains(err.Error(), "invite-only") {
		t.Errorf("expected invite-only error, got: %v", err)
	}
}
