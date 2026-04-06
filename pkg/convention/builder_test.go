package convention

import (
	"encoding/json"
	"testing"
)

func TestSimpleConvention_Defaults(t *testing.T) {
	decl := SimpleConvention("status", "1.0", "report", "Report agent status").Build()

	if decl.Convention != "status" {
		t.Errorf("Convention: got %q, want %q", decl.Convention, "status")
	}
	if decl.Version != "1.0" {
		t.Errorf("Version: got %q, want %q", decl.Version, "1.0")
	}
	if decl.Operation != "report" {
		t.Errorf("Operation: got %q, want %q", decl.Operation, "report")
	}
	if decl.Description != "Report agent status" {
		t.Errorf("Description: got %q, want %q", decl.Description, "Report agent status")
	}
	if decl.Signing != string(SignerMemberKey) {
		t.Errorf("Signing: got %q, want %q", decl.Signing, SignerMemberKey)
	}
	if decl.Response != "sync" {
		t.Errorf("Response: got %q, want %q", decl.Response, "sync")
	}
	if decl.RateLimit != nil {
		t.Errorf("RateLimit: got %v, want nil", decl.RateLimit)
	}
	if len(decl.Args) != 0 {
		t.Errorf("Args: got %d, want 0", len(decl.Args))
	}
}

func TestDeclarationBuilder_RequiredArg(t *testing.T) {
	decl := SimpleConvention("status", "1.0", "report", "Report agent status").
		RequiredArg("agent_id", "string", "Agent identifier").
		Build()

	if len(decl.Args) != 1 {
		t.Fatalf("Args: got %d, want 1", len(decl.Args))
	}
	arg := decl.Args[0]
	if arg.Name != "agent_id" {
		t.Errorf("arg.Name: got %q, want %q", arg.Name, "agent_id")
	}
	if arg.Type != "string" {
		t.Errorf("arg.Type: got %q, want %q", arg.Type, "string")
	}
	if arg.Description != "Agent identifier" {
		t.Errorf("arg.Description: got %q, want %q", arg.Description, "Agent identifier")
	}
	if !arg.Required {
		t.Errorf("arg.Required: got false, want true")
	}
}

func TestDeclarationBuilder_Arg(t *testing.T) {
	decl := SimpleConvention("status", "1.0", "report", "Report agent status").
		Arg("details", "string", "Optional details").
		Build()

	if len(decl.Args) != 1 {
		t.Fatalf("Args: got %d, want 1", len(decl.Args))
	}
	arg := decl.Args[0]
	if arg.Required {
		t.Errorf("arg.Required: got true, want false")
	}
	if arg.Name != "details" {
		t.Errorf("arg.Name: got %q, want %q", arg.Name, "details")
	}
}

func TestDeclarationBuilder_MultipleArgs(t *testing.T) {
	decl := SimpleConvention("status", "1.0", "report", "Report agent status").
		RequiredArg("agent_id", "string", "Agent identifier").
		Arg("details", "string", "Optional details").
		Arg("level", "int", "Severity level").
		Build()

	if len(decl.Args) != 3 {
		t.Fatalf("Args: got %d, want 3", len(decl.Args))
	}
	if !decl.Args[0].Required {
		t.Errorf("Args[0].Required: got false, want true")
	}
	if decl.Args[1].Required {
		t.Errorf("Args[1].Required: got true, want false")
	}
	if decl.Args[2].Required {
		t.Errorf("Args[2].Required: got true, want false")
	}
	if decl.Args[2].Name != "level" {
		t.Errorf("Args[2].Name: got %q, want %q", decl.Args[2].Name, "level")
	}
}

func TestSimpleConvention_DefaultSigning(t *testing.T) {
	decl := SimpleConvention("test", "1.0", "op", "desc").Build()
	if decl.Signing != "member_key" {
		t.Errorf("Signing: got %q, want %q", decl.Signing, "member_key")
	}
}

func TestSimpleConvention_PassesLint(t *testing.T) {
	decl := SimpleConvention("status", "1.0", "report", "Report agent status").
		RequiredArg("agent_id", "string", "Agent identifier").
		Arg("details", "string", "Optional details").
		Build()

	// Serialize to JSON and lint as the wire format would.
	payload, err := json.Marshal(decl)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	result := Lint(payload)
	if result == nil {
		t.Fatal("Lint returned nil")
	}
	if len(result.Errors) > 0 {
		t.Errorf("Lint errors: %v", result.Errors)
	}
}

func TestDeclarationBuilder_Chaining(t *testing.T) {
	// Verify the builder returns itself for method chaining.
	b := SimpleConvention("conv", "1.0", "op", "desc")
	b2 := b.Arg("a", "string", "a desc")
	b3 := b2.RequiredArg("b", "string", "b desc")

	if b != b2 || b2 != b3 {
		t.Error("builder methods should return the same builder instance")
	}

	decl := b3.Build()
	if len(decl.Args) != 2 {
		t.Errorf("Args: got %d, want 2", len(decl.Args))
	}
}
