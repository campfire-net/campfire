package convention_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/convention"
)

// loadPeeringDecl loads and parses a core peering declaration from the declarations dir.
func loadPeeringDecl(t *testing.T, filename string) *convention.Declaration {
	t.Helper()
	// Declarations live in the same package directory.
	path := filepath.Join("declarations", filename)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read declaration file %s: %v", path, err)
	}
	tags := []string{convention.ConventionOperationTag}
	decl, _, err := convention.Parse(tags, data, provSenderKey, provCampfireKey)
	if err != nil {
		t.Fatalf("parse declaration %s: %v", filename, err)
	}
	return decl
}

// TestCorePeerEstablish_HasMinOperatorLevel2 verifies the declaration file has
// min_operator_level set to 2.
func TestCorePeerEstablish_HasMinOperatorLevel2(t *testing.T) {
	decl := loadPeeringDecl(t, "core-peer-establish.json")
	if decl.MinOperatorLevel != 2 {
		t.Errorf("core-peer-establish: expected MinOperatorLevel=2, got %d", decl.MinOperatorLevel)
	}
}

// TestCorePeerWithdraw_HasMinOperatorLevel2 verifies the declaration file has
// min_operator_level set to 2.
func TestCorePeerWithdraw_HasMinOperatorLevel2(t *testing.T) {
	decl := loadPeeringDecl(t, "core-peer-withdraw.json")
	if decl.MinOperatorLevel != 2 {
		t.Errorf("core-peer-withdraw: expected MinOperatorLevel=2, got %d", decl.MinOperatorLevel)
	}
}

// TestCorePeerEstablish_Level1Rejected verifies that an operator at level 1
// cannot execute core-peer-establish.
func TestCorePeerEstablish_Level1Rejected(t *testing.T) {
	decl := loadPeeringDecl(t, "core-peer-establish.json")

	transport := &noopTransport{}
	exec := convention.NewExecutor(transport, provSenderKey).
		WithProvenance(&staticProvenanceChecker{levels: map[string]int{provSenderKey: 1}})

	err := exec.Execute(context.Background(), decl, "campfire-abc", map[string]any{
		"peer_key":      strings.Repeat("a", 64),
		"peer_endpoint": "https://peer.example.com",
		"transport":     "https",
	})

	if err == nil {
		t.Fatal("expected level 1 operator to be rejected from core-peer-establish")
	}
	if !strings.Contains(err.Error(), "operator provenance level") {
		t.Errorf("expected provenance level error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "requires level 2") {
		t.Errorf("expected 'requires level 2' in error, got: %v", err)
	}
	if len(transport.sent) != 0 {
		t.Errorf("expected no messages sent on rejection, got %d", len(transport.sent))
	}
}

// TestCorePeerEstablish_Level2Accepted verifies that an operator at level 2
// can execute core-peer-establish.
func TestCorePeerEstablish_Level2Accepted(t *testing.T) {
	decl := loadPeeringDecl(t, "core-peer-establish.json")

	transport := &noopTransport{}
	exec := convention.NewExecutor(transport, provSenderKey).
		WithProvenance(&staticProvenanceChecker{levels: map[string]int{provSenderKey: 2}})

	err := exec.Execute(context.Background(), decl, "campfire-abc", map[string]any{
		"peer_key":      strings.Repeat("a", 64),
		"peer_endpoint": "https://peer.example.com",
		"transport":     "https",
	})

	if err != nil {
		t.Fatalf("expected level 2 operator to be accepted for core-peer-establish, got: %v", err)
	}
	if len(transport.sent) != 1 {
		t.Errorf("expected 1 message sent, got %d", len(transport.sent))
	}
}

// TestCorePeerWithdraw_Level1Rejected verifies that an operator at level 1
// cannot execute core-peer-withdraw.
func TestCorePeerWithdraw_Level1Rejected(t *testing.T) {
	decl := loadPeeringDecl(t, "core-peer-withdraw.json")

	transport := &noopTransport{}
	exec := convention.NewExecutor(transport, provSenderKey).
		WithProvenance(&staticProvenanceChecker{levels: map[string]int{provSenderKey: 1}})

	err := exec.Execute(context.Background(), decl, "campfire-abc", map[string]any{
		"peer_key": strings.Repeat("b", 64),
	})

	if err == nil {
		t.Fatal("expected level 1 operator to be rejected from core-peer-withdraw")
	}
	if !strings.Contains(err.Error(), "operator provenance level") {
		t.Errorf("expected provenance level error, got: %v", err)
	}
	if len(transport.sent) != 0 {
		t.Errorf("expected no messages sent on rejection, got %d", len(transport.sent))
	}
}

// TestCorePeerWithdraw_Level2Accepted verifies that an operator at level 2
// can execute core-peer-withdraw.
func TestCorePeerWithdraw_Level2Accepted(t *testing.T) {
	decl := loadPeeringDecl(t, "core-peer-withdraw.json")

	transport := &noopTransport{}
	exec := convention.NewExecutor(transport, provSenderKey).
		WithProvenance(&staticProvenanceChecker{levels: map[string]int{provSenderKey: 2}})

	err := exec.Execute(context.Background(), decl, "campfire-abc", map[string]any{
		"peer_key": strings.Repeat("b", 64),
	})

	if err != nil {
		t.Fatalf("expected level 2 operator to be accepted for core-peer-withdraw, got: %v", err)
	}
	if len(transport.sent) != 1 {
		t.Errorf("expected 1 message sent, got %d", len(transport.sent))
	}
}

// TestCorePeerEstablish_Level0Rejected verifies that an anonymous operator (level 0)
// cannot execute core-peer-establish.
func TestCorePeerEstablish_Level0Rejected(t *testing.T) {
	decl := loadPeeringDecl(t, "core-peer-establish.json")

	transport := &noopTransport{}
	exec := convention.NewExecutor(transport, provSenderKey).
		WithProvenance(&staticProvenanceChecker{levels: map[string]int{}}) // level 0 = not in map

	err := exec.Execute(context.Background(), decl, "campfire-abc", map[string]any{
		"peer_key":      strings.Repeat("a", 64),
		"peer_endpoint": "https://peer.example.com",
		"transport":     "https",
	})

	if err == nil {
		t.Fatal("expected level 0 operator to be rejected from core-peer-establish")
	}
}

// TestCorePeerEstablish_Level3Accepted verifies that an operator at level 3
// satisfies the level 2 minimum for core-peer-establish.
func TestCorePeerEstablish_Level3Accepted(t *testing.T) {
	decl := loadPeeringDecl(t, "core-peer-establish.json")

	transport := &noopTransport{}
	exec := convention.NewExecutor(transport, provSenderKey).
		WithProvenance(&staticProvenanceChecker{levels: map[string]int{provSenderKey: 3}})

	err := exec.Execute(context.Background(), decl, "campfire-abc", map[string]any{
		"peer_key":      strings.Repeat("a", 64),
		"peer_endpoint": "https://peer.example.com",
		"transport":     "https",
	})

	if err != nil {
		t.Fatalf("expected level 3 operator to be accepted (level 3 >= 2), got: %v", err)
	}
}

// TestCorePeerEstablish_DeclarationFields verifies the parsed declaration has
// the expected convention, operation, and signing fields.
func TestCorePeerEstablish_DeclarationFields(t *testing.T) {
	decl := loadPeeringDecl(t, "core-peer-establish.json")

	if decl.Convention != "peering" {
		t.Errorf("expected convention=peering, got %q", decl.Convention)
	}
	if decl.Operation != "core-peer-establish" {
		t.Errorf("expected operation=core-peer-establish, got %q", decl.Operation)
	}
	if decl.Signing != "member_key" {
		t.Errorf("expected signing=member_key, got %q", decl.Signing)
	}
}

// TestCorePeeringDeclarations_ValidJSON verifies both declaration files are
// valid JSON and parse without error.
func TestCorePeeringDeclarations_ValidJSON(t *testing.T) {
	files := []string{"core-peer-establish.json", "core-peer-withdraw.json"}
	for _, f := range files {
		t.Run(f, func(t *testing.T) {
			path := filepath.Join("declarations", f)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", f, err)
			}
			var raw map[string]any
			if err := json.Unmarshal(data, &raw); err != nil {
				t.Fatalf("%s: invalid JSON: %v", f, err)
			}
			// Verify min_operator_level field is present and equals 2.
			level, ok := raw["min_operator_level"]
			if !ok {
				t.Errorf("%s: missing min_operator_level field", f)
			} else {
				// JSON numbers decode as float64.
				if levelFloat, ok := level.(float64); !ok || int(levelFloat) != 2 {
					t.Errorf("%s: expected min_operator_level=2, got %v", f, level)
				}
			}
		})
	}
}
