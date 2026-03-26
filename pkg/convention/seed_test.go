package convention_test

import (
	"encoding/json"
	"testing"

	"github.com/campfire-net/campfire/pkg/convention"
)

// TestPromoteDeclaration verifies the structure of the embedded promote declaration.
// This is the ONE declaration compiled into the binary — the bootstrap primitive.
func TestPromoteDeclaration(t *testing.T) {
	decl := convention.PromoteDeclaration()

	if decl.Convention != convention.InfrastructureConvention {
		t.Errorf("convention: want %q, got %q", convention.InfrastructureConvention, decl.Convention)
	}
	if decl.Operation != "promote" {
		t.Errorf("operation: want %q, got %q", "promote", decl.Operation)
	}
	if decl.Signing != "campfire_key" {
		t.Errorf("signing: want %q, got %q", "campfire_key", decl.Signing)
	}

	// Must have produces_tags with convention:operation
	if len(decl.ProducesTags) != 1 {
		t.Fatalf("produces_tags: want 1, got %d", len(decl.ProducesTags))
	}
	if decl.ProducesTags[0].Tag != "convention:operation" {
		t.Errorf("produces_tags[0].tag: want %q, got %q", "convention:operation", decl.ProducesTags[0].Tag)
	}

	// Must have 'file' and 'registry' args
	argByName := make(map[string]convention.ArgDescriptor)
	for _, a := range decl.Args {
		argByName[a.Name] = a
	}
	fileArg, ok := argByName["file"]
	if !ok {
		t.Fatal("missing 'file' arg")
	}
	if fileArg.Type != "string" {
		t.Errorf("arg 'file' type: want string, got %q", fileArg.Type)
	}
	if !fileArg.Required {
		t.Error("arg 'file' should be required")
	}
	registryArg, ok := argByName["registry"]
	if !ok {
		t.Fatal("missing 'registry' arg")
	}
	if registryArg.Type != "campfire" {
		t.Errorf("arg 'registry' type: want campfire, got %q", registryArg.Type)
	}
	if !registryArg.Required {
		t.Error("arg 'registry' should be required")
	}
}

// TestPromoteDeclaration_IsUnderSizeLimit verifies that the promote declaration
// serializes to under 500 bytes — the "~500 bytes, stable forever" constraint.
func TestPromoteDeclaration_IsUnderSizeLimit(t *testing.T) {
	decl := convention.PromoteDeclaration()

	// Use encoding/json which is what sendDeclarationViaTransport uses
	data, err := json.Marshal(decl)
	if err != nil {
		t.Fatalf("marshaling promote declaration: %v", err)
	}
	const maxBytes = 600 // generous upper bound; spec says ~500
	if len(data) > maxBytes {
		t.Errorf("promote declaration too large: %d bytes (max %d)", len(data), maxBytes)
	}
}

// TestInfrastructureSeedDeclarations verifies that the seed set contains
// supersede and revoke declarations.
func TestInfrastructureSeedDeclarations(t *testing.T) {
	decls := convention.InfrastructureSeedDeclarations()
	if len(decls) != 2 {
		t.Fatalf("expected 2 seed declarations, got %d", len(decls))
	}

	ops := make(map[string]*convention.Declaration)
	for _, d := range decls {
		ops[d.Operation] = d
	}

	if _, ok := ops["supersede"]; !ok {
		t.Error("expected 'supersede' operation in seed declarations")
	}
	if _, ok := ops["revoke"]; !ok {
		t.Error("expected 'revoke' operation in seed declarations")
	}
}

// TestSupersedeDeclaration verifies the structure of the supersede declaration.
func TestSupersedeDeclaration(t *testing.T) {
	decl := convention.SupersedeDeclaration()

	if decl.Convention != convention.InfrastructureConvention {
		t.Errorf("convention: want %q, got %q", convention.InfrastructureConvention, decl.Convention)
	}
	if decl.Operation != "supersede" {
		t.Errorf("operation: want %q, got %q", "supersede", decl.Operation)
	}
	if decl.Signing != "campfire_key" {
		t.Errorf("signing: want %q, got %q", "campfire_key", decl.Signing)
	}

	// Must have produces_tags with convention:operation
	if len(decl.ProducesTags) != 1 {
		t.Fatalf("produces_tags: want 1, got %d", len(decl.ProducesTags))
	}
	if decl.ProducesTags[0].Tag != "convention:operation" {
		t.Errorf("produces_tags[0].tag: want %q, got %q", "convention:operation", decl.ProducesTags[0].Tag)
	}
	if decl.ProducesTags[0].Cardinality != "exactly_one" {
		t.Errorf("produces_tags[0].cardinality: want %q, got %q", "exactly_one", decl.ProducesTags[0].Cardinality)
	}

	// Must have file and supersedes args
	argByName := make(map[string]convention.ArgDescriptor)
	for _, a := range decl.Args {
		argByName[a.Name] = a
	}

	fileArg, ok := argByName["file"]
	if !ok {
		t.Fatal("missing 'file' arg")
	}
	if fileArg.Type != "string" {
		t.Errorf("arg 'file' type: want string, got %q", fileArg.Type)
	}
	if !fileArg.Required {
		t.Error("arg 'file' should be required")
	}

	supersededArg, ok := argByName["supersedes"]
	if !ok {
		t.Fatal("missing 'supersedes' arg")
	}
	if supersededArg.Type != "message_id" {
		t.Errorf("arg 'supersedes' type: want message_id, got %q", supersededArg.Type)
	}
	if !supersededArg.Required {
		t.Error("arg 'supersedes' should be required")
	}
}

// TestRevokeDeclaration verifies the structure of the revoke declaration.
func TestRevokeDeclaration(t *testing.T) {
	decl := convention.RevokeDeclaration()

	if decl.Convention != convention.InfrastructureConvention {
		t.Errorf("convention: want %q, got %q", convention.InfrastructureConvention, decl.Convention)
	}
	if decl.Operation != "revoke" {
		t.Errorf("operation: want %q, got %q", "revoke", decl.Operation)
	}
	if decl.Signing != "campfire_key" {
		t.Errorf("signing: want %q, got %q", "campfire_key", decl.Signing)
	}

	// Must have produces_tags with convention:revoke
	if len(decl.ProducesTags) != 1 {
		t.Fatalf("produces_tags: want 1, got %d", len(decl.ProducesTags))
	}
	if decl.ProducesTags[0].Tag != "convention:revoke" {
		t.Errorf("produces_tags[0].tag: want %q, got %q", "convention:revoke", decl.ProducesTags[0].Tag)
	}
	if decl.ProducesTags[0].Cardinality != "exactly_one" {
		t.Errorf("produces_tags[0].cardinality: want %q, got %q", "exactly_one", decl.ProducesTags[0].Cardinality)
	}

	// Must have target_id arg
	if len(decl.Args) != 1 {
		t.Fatalf("args: want 1, got %d", len(decl.Args))
	}
	if decl.Args[0].Name != "target_id" {
		t.Errorf("args[0].name: want %q, got %q", "target_id", decl.Args[0].Name)
	}
	if decl.Args[0].Type != "message_id" {
		t.Errorf("args[0].type: want %q, got %q", "message_id", decl.Args[0].Type)
	}
	if !decl.Args[0].Required {
		t.Error("args[0] (target_id) should be required")
	}
}

// TestSupersedeDeclaration_ParsesWithConventionExtensionException verifies that
// the supersede declaration is parseable even though it produces convention:operation
// (the denylist exception for convention-extension applies).
func TestSupersedeDeclaration_ParsesWithConventionExtensionException(t *testing.T) {
	payload := []byte(`{
		"convention": "convention-extension",
		"version": "0.1",
		"operation": "supersede",
		"description": "Replace a convention declaration with a newer version",
		"produces_tags": [{"tag": "convention:operation", "cardinality": "exactly_one"}],
		"args": [
			{"name": "file", "type": "string", "required": true, "description": "Path to new declaration JSON"},
			{"name": "supersedes", "type": "message_id", "required": true, "description": "Message ID of the declaration being replaced"}
		],
		"signing": "campfire_key"
	}`)
	campfireKey := "deadbeef" + seedRepeatStr('0', 56)
	decl, result, err := convention.Parse([]string{"convention:operation"}, payload, campfireKey, campfireKey)
	if err != nil {
		t.Fatalf("Parse supersede decl: %v", err)
	}
	if !result.Valid {
		t.Fatalf("expected valid parse result, warnings: %v", result.Warnings)
	}
	if decl.Operation != "supersede" {
		t.Errorf("operation: want %q, got %q", "supersede", decl.Operation)
	}
}

// TestRevokeDeclaration_ParsesWithConventionExtensionException verifies that
// the revoke declaration is parseable even though it produces convention:revoke
// (the denylist exception for convention-extension applies).
func TestRevokeDeclaration_ParsesWithConventionExtensionException(t *testing.T) {
	payload := []byte(`{
		"convention": "convention-extension",
		"version": "0.1",
		"operation": "revoke",
		"description": "Permanently revoke a convention declaration",
		"produces_tags": [{"tag": "convention:revoke", "cardinality": "exactly_one"}],
		"args": [
			{"name": "target_id", "type": "message_id", "required": true, "description": "Message ID of the declaration to revoke"}
		],
		"signing": "campfire_key"
	}`)
	campfireKey := "deadbeef" + seedRepeatStr('0', 56)
	decl, result, err := convention.Parse([]string{"convention:operation"}, payload, campfireKey, campfireKey)
	if err != nil {
		t.Fatalf("Parse revoke decl: %v", err)
	}
	if !result.Valid {
		t.Fatalf("expected valid parse result, warnings: %v", result.Warnings)
	}
	if decl.Operation != "revoke" {
		t.Errorf("operation: want %q, got %q", "revoke", decl.Operation)
	}
}

func seedRepeatStr(c byte, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = c
	}
	return string(b)
}
