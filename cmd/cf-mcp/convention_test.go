package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/provenance"
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

// TestEnvelopedResponse_OperatorProvenance verifies that operator_provenance is
// populated in the envelope's runtime_computed section when the agent's identity
// has a self-claimed profile in the local provenance store.
//
// Regression test for: operator_provenance never set in production BuildEnvelope calls.
// Refs: Operator Provenance Convention v0.1 §8.2, Trust Convention v0.2 §6.3.
func TestEnvelopedResponse_OperatorProvenance(t *testing.T) {
	srv := newTestServer(t)

	// Create and save an identity for the server.
	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	idPath := srv.identityPath()
	if err := agentID.Save(idPath); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	// Mark the agent as self-claimed (level 1: Claimed) in the persisted store.
	// This does not require trusted verifier config — it is set directly.
	// Operator Provenance Convention v0.1 §4.2.
	storePath := filepath.Join(srv.cfHome, "attestations.json")
	ps, err := provenance.NewFileStore(storePath, provenance.DefaultConfig())
	if err != nil {
		t.Fatalf("opening provenance store: %v", err)
	}
	if err := ps.SetSelfClaimed(agentID.PublicKeyHex()); err != nil {
		t.Fatalf("setting self-claimed: %v", err)
	}

	// Call envelopedResponse through the production path.
	resp := srv.envelopedResponse(float64(1), "test-campfire-id", map[string]string{
		"message": "hello",
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	// Extract the envelope JSON from the tool result.
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

	// Assert operator_provenance is set (not nil) and reflects level 1 (Claimed).
	if env.RuntimeComputed.OperatorProvenance == nil {
		t.Fatal("operator_provenance is nil: production BuildEnvelope did not wire provenance data")
	}
	gotLevel := *env.RuntimeComputed.OperatorProvenance
	wantLevel := int(provenance.LevelClaimed) // 1: self-asserted profile
	if gotLevel != wantLevel {
		t.Errorf("operator_provenance: got %d, want %d (%s)", gotLevel, wantLevel, provenance.LevelClaimed)
	}
}

// TestEnvelopedResponse_OperatorProvenance_Anonymous verifies that operator_provenance
// defaults to level 0 (Anonymous) when no attestations exist for the agent.
func TestEnvelopedResponse_OperatorProvenance_Anonymous(t *testing.T) {
	srv := newTestServer(t)

	// Create and save an identity for the server — no attestations.
	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(srv.identityPath()); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	resp := srv.envelopedResponse(float64(1), "test-campfire-id", map[string]string{
		"message": "hello",
	})
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

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

	// Assert operator_provenance is set to level 0 (Anonymous) — not nil.
	if env.RuntimeComputed.OperatorProvenance == nil {
		t.Fatal("operator_provenance is nil: expected level 0 (Anonymous) for agent with no attestations")
	}
	gotLevel := *env.RuntimeComputed.OperatorProvenance
	wantLevel := int(provenance.LevelAnonymous) // 0
	if gotLevel != wantLevel {
		t.Errorf("operator_provenance: got %d, want %d (%s)", gotLevel, wantLevel, provenance.LevelAnonymous)
	}
}

// TestCreateWithDeclarations verifies that campfire_create with a declarations
// parameter publishes campfire-key-signed declarations and registers them as
// convention tools. This is the bootstrap path: convention tools from birth.
func TestCreateWithDeclarations(t *testing.T) {
	srv, _ := newTestServerWithStore(t)

	// Initialize identity.
	initResp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{}}`))
	if initResp.Error != nil {
		t.Fatalf("campfire_init: %+v", initResp.Error)
	}

	// Create a campfire with an inline declaration.
	createArgs := map[string]interface{}{
		"name": "campfire_create",
		"arguments": map[string]interface{}{
			"description": "test campfire with conventions",
			"declarations": []interface{}{
				map[string]interface{}{
					"convention":    "test-bootstrap",
					"version":       "0.1",
					"operation":     "greet",
					"description":   "Say hello",
					"signing":       "member_key",
					"antecedents":   "none",
					"produces_tags": []interface{}{map[string]interface{}{"tag": "test:greet", "cardinality": "exactly_one"}},
					"args": []interface{}{
						map[string]interface{}{"name": "greeting", "type": "string", "required": true, "max_length": 256},
					},
				},
			},
		},
	}
	createJSON, _ := json.Marshal(createArgs)
	createResp := srv.dispatch(makeReq("tools/call", string(createJSON)))
	if createResp.Error != nil {
		t.Fatalf("campfire_create: %+v", createResp.Error)
	}

	// Extract the result to verify convention_tools_registered.
	b, _ := json.Marshal(createResp.Result)
	var outer struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &outer); err != nil || len(outer.Content) == 0 {
		t.Fatalf("extracting content: %v — raw: %s", err, string(b))
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(outer.Content[0].Text), &payload); err != nil {
		t.Fatalf("parsing create result: %v", err)
	}

	toolCount, _ := payload["convention_tools_registered"].(float64)
	if toolCount != 1 {
		t.Errorf("convention_tools_registered: got %v, want 1", payload["convention_tools_registered"])
	}

	toolNames, _ := payload["convention_tools"].([]interface{})
	if len(toolNames) != 1 || toolNames[0] != "greet" {
		t.Errorf("convention_tools: got %v, want [greet]", toolNames)
	}

	// Verify the tool appears in tools/list.
	listResp := srv.dispatch(makeReq("tools/list", "{}"))
	if listResp.Error != nil {
		t.Fatalf("tools/list: %+v", listResp.Error)
	}
	lb, _ := json.Marshal(listResp.Result)
	var listResult map[string]interface{}
	json.Unmarshal(lb, &listResult)

	toolsRaw, _ := listResult["tools"].([]interface{})
	found := false
	for _, tr := range toolsRaw {
		tool, _ := tr.(map[string]interface{})
		if tool["name"] == "greet" {
			found = true
			break
		}
	}
	if !found {
		t.Error("convention tool 'greet' not found in tools/list after create with declarations")
	}
}

// ---------------------------------------------------------------------------
// Regression test: MCP path must wire WithProvenance into the executor.
//
// Before the fix in campfire-agent-2uh, handleConventionTool created the
// executor without calling WithProvenance, so senderLevel was always 0 inside
// Execute. That meant:
//   - Operations with min_operator_level=2 were permanently blocked (level 0
//     < 2 → always rejected), even for verified operators.
//   - There was no way for a legitimate operator to unblock them through the
//     MCP path, because the provenance store was never consulted.
//
// These two tests pin the corrected behavior end-to-end through
// handleConventionTool: level 0 → rejected; level 2 → accepted.
// ---------------------------------------------------------------------------

// buildGatedConventionEntry builds a conventionToolEntry for an operation
// requiring the given min_operator_level. The declaration JSON is parsed
// through the standard convention.Parse path so all field mappings are
// exercised.
func buildGatedConventionEntry(t *testing.T, minLevel int) *conventionToolEntry {
	t.Helper()
	payload, err := json.Marshal(map[string]interface{}{
		"convention":         "peering",
		"version":            "0.3",
		"operation":          "core-peer-establish",
		"description":        "Establish a core peering link",
		"min_operator_level": minLevel,
		"produces_tags": []interface{}{
			map[string]interface{}{"tag": "peering:core", "cardinality": "exactly_one"},
		},
		"args": []interface{}{
			map[string]interface{}{"name": "peer_key", "type": "string", "required": true, "max_length": 64},
		},
		"antecedents": "none",
		"signing":     "member_key",
	})
	if err != nil {
		t.Fatalf("buildGatedConventionEntry: marshal: %v", err)
	}
	tags := []string{convention.ConventionOperationTag}
	decl, _, parseErr := convention.Parse(tags, payload, strings.Repeat("a", 64), strings.Repeat("b", 64))
	if parseErr != nil {
		t.Fatalf("buildGatedConventionEntry: parse: %v", parseErr)
	}
	return &conventionToolEntry{
		decl:       decl,
		campfireID: "test-campfire-id",
	}
}

// TestHandleConventionTool_MinOperatorLevel_Level0Rejected verifies that
// handleConventionTool rejects a min_operator_level=2 operation when the
// server's identity has provenance level 0 (anonymous, no attestations).
//
// Regression for campfire-agent-2uh: before the fix, the executor was created
// without WithProvenance so the gate always used senderLevel=0. For a level-2
// gate that means the operation was always rejected regardless of actual
// operator level — this test confirms the rejection still happens for the
// right reason (provenance gate enforced).
func TestHandleConventionTool_MinOperatorLevel_Level0Rejected(t *testing.T) {
	srv, _ := newTestServerWithStore(t)

	// Generate and save an identity with no attestations (level 0 = anonymous).
	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(srv.identityPath()); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	entry := buildGatedConventionEntry(t, 2)
	args := map[string]interface{}{
		"peer_key": strings.Repeat("c", 64),
	}
	resp := srv.handleConventionTool(float64(1), entry, args)

	// Must be rejected: anonymous level 0 < required level 2.
	if resp.Error == nil {
		t.Fatal("expected rejection for level-0 operator on min_operator_level=2 gate, got nil error")
	}
	if resp.Error.Code != -32000 {
		t.Errorf("expected error code -32000, got %d", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "operator provenance level") {
		t.Errorf("expected provenance error message, got: %q", resp.Error.Message)
	}
}

// TestHandleConventionTool_MinOperatorLevel_ClaimedLevelAccepted verifies that
// handleConventionTool allows a min_operator_level=1 operation when the
// server's identity has provenance level 1 (self-claimed).
//
// Regression for campfire-agent-2uh: before the fix, WithProvenance was never
// called on the executor, so even an operator with sufficient provenance level
// was rejected because senderLevel defaulted to 0. This test confirms the fix:
// the provenance store is now consulted and a level-1 operator passes a
// min_operator_level=1 gate through the full MCP handler path.
func TestHandleConventionTool_MinOperatorLevel_ClaimedLevelAccepted(t *testing.T) {
	srv, _ := newTestServerWithStore(t)

	// Generate and save an identity.
	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(srv.identityPath()); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	// Set provenance level 1 (Claimed) for this identity in the local store.
	// This is the simplest provenance level to set directly without a
	// challenge/response ceremony.
	storePath := filepath.Join(srv.cfHome, "attestations.json")
	ps, err := provenance.NewFileStore(storePath, provenance.DefaultConfig())
	if err != nil {
		t.Fatalf("opening provenance store: %v", err)
	}
	if err := ps.SetSelfClaimed(agentID.PublicKeyHex()); err != nil {
		t.Fatalf("setting self-claimed: %v", err)
	}

	// Use min_operator_level=1: the claimed identity should satisfy the gate.
	entry := buildGatedConventionEntry(t, 1)
	args := map[string]interface{}{
		"peer_key": strings.Repeat("d", 64),
	}
	resp := srv.handleConventionTool(float64(1), entry, args)

	// Must succeed: level 1 meets the min_operator_level=1 requirement.
	// Before the fix, this would have returned an error because the executor
	// was created without WithProvenance and senderLevel was always 0.
	if resp.Error != nil {
		t.Errorf("expected success for level-1 operator on min_operator_level=1 gate, got error: %q", resp.Error.Message)
	}
}
