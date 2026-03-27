package main

import (
	"encoding/json"
	"testing"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/trust"
)

// socialPostPayload is the social:post test vector from Convention Extension §16.1.
var socialPostPayload = []byte(`{
	"convention": "social-post-format",
	"version": "0.3",
	"operation": "post",
	"description": "Publish a social post",
	"produces_tags": [
		{"tag": "social:post", "cardinality": "exactly_one"},
		{"tag": "content:*", "cardinality": "at_most_one",
		 "values": ["content:text/plain", "content:text/markdown", "content:application/json"]},
		{"tag": "topic:*", "cardinality": "zero_to_many", "max": 10, "pattern": "[a-z0-9-]{1,64}"}
	],
	"args": [
		{"name": "text", "type": "string", "required": true, "max_length": 65536},
		{"name": "content_type", "type": "enum", "values": ["text/plain", "text/markdown", "application/json"], "default": "text/plain"},
		{"name": "topics", "type": "string", "repeated": true, "max_count": 10, "pattern": "[a-z0-9-]{1,64}"}
	],
	"antecedents": "none",
	"payload_required": true,
	"signing": "member_key"
}`)

// TestConventionToolRegistration verifies that convention tools appear in the
// tool list after readDeclarations + registerConventionTools.
func TestConventionToolRegistration(t *testing.T) {
	tags := []string{convention.ConventionOperationTag}
	senderKey := "aaaa" // member key
	campfireKey := "bbbb"

	decl, result, err := convention.Parse(tags, socialPostPayload, senderKey, campfireKey)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !result.Valid {
		t.Fatalf("Parse returned invalid: %v", result.Warnings)
	}

	m := newConventionToolMap()
	registerConventionTools(m, "test-campfire-id", []*convention.Declaration{decl})

	toolList := m.list()
	if len(toolList) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(toolList))
	}
	if toolList[0].Name != "post" {
		t.Errorf("expected tool name 'post', got %q", toolList[0].Name)
	}

	// Verify the tool appears in get()
	entry, ok := m.get("post")
	if !ok {
		t.Fatal("tool 'post' not found in get()")
	}
	if entry.campfireID != "test-campfire-id" {
		t.Errorf("expected campfireID 'test-campfire-id', got %q", entry.campfireID)
	}
	if entry.decl.Convention != "social-post-format" {
		t.Errorf("expected convention 'social-post-format', got %q", entry.decl.Convention)
	}

	// Verify inputSchema has required fields
	var schema map[string]interface{}
	if err := json.Unmarshal(toolList[0].InputSchema, &schema); err != nil {
		t.Fatalf("unmarshaling schema: %v", err)
	}
	props, _ := schema["properties"].(map[string]interface{})
	if props == nil {
		t.Fatal("inputSchema has no properties")
	}
	if _, ok := props["text"]; !ok {
		t.Error("inputSchema missing 'text' property")
	}
	if _, ok := props["campfire_id"]; !ok {
		t.Error("inputSchema missing 'campfire_id' property")
	}
}

// TestConventionToolTrustGate verifies that untrusted declarations are not
// registered as tools.
func TestConventionToolTrustGate(t *testing.T) {
	// Parse with signing=campfire_key but senderKey != campfireKey
	campfireKeyPayload := []byte(`{
		"convention": "community-beacon-metadata",
		"version": "0.3",
		"operation": "register",
		"description": "Register a campfire in this directory",
		"args": [
			{"name": "campfire_id", "type": "key", "required": true},
			{"name": "description", "type": "string", "required": true, "max_length": 280}
		],
		"signing": "campfire_key"
	}`)

	tags := []string{convention.ConventionOperationTag}
	senderKey := "aaaa" // NOT the campfire key
	campfireKey := "bbbb"

	decl, result, err := convention.Parse(tags, campfireKeyPayload, senderKey, campfireKey)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Result should be invalid — campfire_key not authorized
	if result.Valid {
		t.Fatal("expected invalid result for unauthorized campfire_key declaration")
	}
	if result.CampfireKeyAuthorized {
		t.Fatal("expected CampfireKeyAuthorized=false")
	}

	// Even if we try to register it, authority resolver should block it
	level := trust.ResolveAuthority(decl, nil)
	if level != trust.AuthorityUntrusted {
		t.Errorf("expected AuthorityUntrusted, got %v", level)
	}

	// Verify it would be filtered by readDeclarations' authority check
	m := newConventionToolMap()
	// Only register if authority >= operational
	if level != trust.AuthorityUntrusted {
		registerConventionTools(m, "test-cf", []*convention.Declaration{decl})
	}
	if len(m.list()) != 0 {
		t.Error("untrusted declaration should not produce a tool")
	}
}

// TestConventionToolNameCollision verifies collision handling when two
// declarations have the same operation name.
func TestConventionToolNameCollision(t *testing.T) {
	tags := []string{convention.ConventionOperationTag}
	senderKey := "aaaa"
	campfireKey := "bbbb"

	decl1, _, err := convention.Parse(tags, socialPostPayload, senderKey, campfireKey)
	if err != nil {
		t.Fatalf("Parse decl1: %v", err)
	}

	// Second declaration with same operation but different convention
	otherPayload := []byte(`{
		"convention": "other-format",
		"version": "0.1",
		"operation": "post",
		"description": "Another post operation",
		"args": [{"name": "text", "type": "string", "required": true}],
		"antecedents": "none",
		"signing": "member_key"
	}`)
	decl2, _, err := convention.Parse(tags, otherPayload, senderKey, campfireKey)
	if err != nil {
		t.Fatalf("Parse decl2: %v", err)
	}

	m := newConventionToolMap()
	// Register one at a time to debug
	registerConventionTools(m, "cf1", []*convention.Declaration{decl1})
	if len(m.list()) != 1 {
		t.Fatalf("after first: expected 1 tool, got %d", len(m.list()))
	}
	registerConventionTools(m, "cf2", []*convention.Declaration{decl2})

	toolList := m.list()
	if len(toolList) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(toolList))
	}

	// One should be "post", the other should be prefixed
	names := make(map[string]bool)
	for _, tool := range toolList {
		names[tool.Name] = true
	}
	if !names["post"] {
		t.Error("expected tool named 'post'")
	}
	// The second should be "other_format_post" (hyphens → underscores)
	if !names["other_format_post"] {
		t.Errorf("expected tool named 'other_format_post', got names: %v", names)
	}
}

// TestEnvelopedResponse verifies the envelope structure.
func TestEnvelopedResponse(t *testing.T) {
	srv := newTestServer(t)
	resp := srv.envelopedResponse(float64(1), "test-campfire-id", map[string]string{
		"message": "hello world",
	})

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	// The result should contain an envelope JSON in the text content
	b, _ := json.Marshal(resp.Result)
	var result map[string]interface{}
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshaling result: %v", err)
	}

	content, _ := result["content"].([]interface{})
	if len(content) == 0 {
		t.Fatal("expected content in result")
	}
	textEntry, _ := content[0].(map[string]interface{})
	text, _ := textEntry["text"].(string)

	var env trust.Envelope
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		t.Fatalf("unmarshaling envelope: %v", err)
	}

	if env.Verified.CampfireID != "test-campfire-id" {
		t.Errorf("expected campfire_id 'test-campfire-id', got %q", env.Verified.CampfireID)
	}
	if env.RuntimeComputed.TrustStatus != trust.TrustUnknown {
		t.Errorf("expected trust_status 'unknown' (no policy engine attached), got %q", env.RuntimeComputed.TrustStatus)
	}
	if env.Tainted.ContentClassification != "tainted" {
		t.Errorf("expected content_classification 'tainted', got %q", env.Tainted.ContentClassification)
	}
}

// TestConventionToolsInToolsList verifies convention tools appear alongside static tools.
func TestConventionToolsInToolsList(t *testing.T) {
	srv := newTestServer(t)
	srv.conventionTools = newConventionToolMap()

	tags := []string{convention.ConventionOperationTag}
	decl, _, err := convention.Parse(tags, socialPostPayload, "aaaa", "bbbb")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	registerConventionTools(srv.conventionTools, "cf-123", []*convention.Declaration{decl})

	resp := srv.dispatch(makeReq("tools/list", "{}"))
	if resp.Error != nil {
		t.Fatalf("tools/list error: %+v", resp.Error)
	}

	b, _ := json.Marshal(resp.Result)
	var result map[string]interface{}
	json.Unmarshal(b, &result)

	toolsRaw, _ := result["tools"].([]interface{})
	found := false
	for _, tr := range toolsRaw {
		tool, _ := tr.(map[string]interface{})
		if tool["name"] == "post" {
			found = true
			break
		}
	}
	if !found {
		t.Error("convention tool 'post' not found in tools/list response")
	}

	// Also verify static tools are still present
	staticFound := false
	for _, tr := range toolsRaw {
		tool, _ := tr.(map[string]interface{})
		if tool["name"] == "campfire_init" {
			staticFound = true
			break
		}
	}
	if !staticFound {
		t.Error("static tool 'campfire_init' missing from tools/list response")
	}
}
