package naming_test

// Tests for NewResolverFromClient and PublishAPI (campfire-agent-liu, items 2 & 3).
//
// These are integration tests using a real protocol.Client with filesystem transport.
// They verify:
//   2. NewResolverFromClient returns a *Resolver backed by a protocol.Client.
//      The resolver implements naming.Transport via client.Read/Send/Await.
//   3. PublishAPI sends a naming:api message to the campfire, readable by ListAPI.

import (
	"context"
	"encoding/json"
	"path/filepath"
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
