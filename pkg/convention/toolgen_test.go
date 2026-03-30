package convention

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/store"
)

// votePayload is §16.2 test vector.
var votePayload = []byte(`{
    "convention": "social-post-format",
    "version": "0.3",
    "operation": "vote",
    "description": "Vote on a post",
    "antecedents": "exactly_one(target)",
    "args": [
      {"name": "target_msg_id", "type": "message_id", "required": true,
       "description": "Message to vote on"},
      {"name": "direction", "type": "enum", "required": true,
       "values": ["up", "down"], "description": "Vote direction"}
    ],
    "produces_tags": [
      {"tag": "social:vote", "cardinality": "exactly_one"}
    ],
    "signing": "member_key"
}`)

// beaconRegisterPayload is §16.5 test vector.
var beaconRegisterPayload = []byte(`{
    "convention": "beacon-protocol",
    "version": "0.1",
    "operation": "register",
    "description": "Register a beacon",
    "args": [
      {"name": "agent_key", "type": "key", "required": true,
       "description": "Agent public key"},
      {"name": "campfire_id", "type": "campfire", "required": false,
       "description": "Campfire to register in"}
    ],
    "antecedents": "none",
    "signing": "member_key"
}`)

func parseDeclForTest(t *testing.T, payload []byte) *Declaration {
	t.Helper()
	decl, _, err := Parse([]string{ConventionOperationTag}, payload, "sender", "campfire")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	return decl
}

// schemaMap decodes InputSchema into a map for easy assertions.
func schemaMap(t *testing.T, tool *MCPToolInfo) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(tool.InputSchema, &m); err != nil {
		t.Fatalf("unmarshal InputSchema: %v", err)
	}
	return m
}

func propMap(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema.properties missing or wrong type")
	}
	return props
}

func TestGenerateTool_SocialPost(t *testing.T) {
	decl := parseDeclForTest(t, socialPostPayload)
	tool, err := GenerateTool(decl, "campfire123")
	if err != nil {
		t.Fatalf("GenerateTool: %v", err)
	}

	if tool.Name != "post" {
		t.Errorf("Name = %q, want %q", tool.Name, "post")
	}

	schema := schemaMap(t, tool)
	props := propMap(t, schema)

	// campfire_id must be present
	if _, ok := props["campfire_id"]; !ok {
		t.Error("missing campfire_id property")
	}

	// text: string with maxLength
	textProp, ok := props["text"].(map[string]any)
	if !ok {
		t.Fatal("text property missing")
	}
	if textProp["type"] != "string" {
		t.Errorf("text.type = %v, want string", textProp["type"])
	}
	if textProp["maxLength"] != float64(65536) {
		t.Errorf("text.maxLength = %v, want 65536", textProp["maxLength"])
	}

	// content_type: enum
	ctProp, ok := props["content_type"].(map[string]any)
	if !ok {
		t.Fatal("content_type property missing")
	}
	if ctProp["type"] != "string" {
		t.Errorf("content_type.type = %v, want string", ctProp["type"])
	}
	if _, ok := ctProp["enum"]; !ok {
		t.Error("content_type missing enum")
	}

	// topics: array of strings with maxItems
	topicsProp, ok := props["topics"].(map[string]any)
	if !ok {
		t.Fatal("topics property missing")
	}
	if topicsProp["type"] != "array" {
		t.Errorf("topics.type = %v, want array", topicsProp["type"])
	}
	if topicsProp["maxItems"] != float64(10) {
		t.Errorf("topics.maxItems = %v, want 10", topicsProp["maxItems"])
	}
	items, ok := topicsProp["items"].(map[string]any)
	if !ok {
		t.Fatal("topics.items missing")
	}
	if items["type"] != "string" {
		t.Errorf("topics.items.type = %v, want string", items["type"])
	}

	// text must be required
	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatal("schema.required missing")
	}
	foundText := false
	for _, r := range required {
		if r == "text" {
			foundText = true
		}
	}
	if !foundText {
		t.Error("text not in required")
	}
}

func TestGenerateTool_Vote(t *testing.T) {
	decl := parseDeclForTest(t, votePayload)
	tool, err := GenerateTool(decl, "campfire123")
	if err != nil {
		t.Fatalf("GenerateTool: %v", err)
	}

	if tool.Name != "vote" {
		t.Errorf("Name = %q, want %q", tool.Name, "vote")
	}

	schema := schemaMap(t, tool)
	props := propMap(t, schema)

	if _, ok := props["target_msg_id"]; !ok {
		t.Error("missing target_msg_id property")
	}
	targetProp := props["target_msg_id"].(map[string]any)
	if targetProp["type"] != "string" {
		t.Errorf("target_msg_id.type = %v, want string", targetProp["type"])
	}

	dirProp, ok := props["direction"].(map[string]any)
	if !ok {
		t.Fatal("direction property missing")
	}
	if dirProp["type"] != "string" {
		t.Errorf("direction.type = %v, want string", dirProp["type"])
	}
	if _, ok := dirProp["enum"]; !ok {
		t.Error("direction missing enum")
	}

	// both must be required
	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatal("schema.required missing")
	}
	reqSet := make(map[string]bool)
	for _, r := range required {
		reqSet[r.(string)] = true
	}
	if !reqSet["target_msg_id"] {
		t.Error("target_msg_id not required")
	}
	if !reqSet["direction"] {
		t.Error("direction not required")
	}
}

func TestGenerateTool_BeaconRegister(t *testing.T) {
	decl := parseDeclForTest(t, beaconRegisterPayload)
	tool, err := GenerateTool(decl, "campfire456")
	if err != nil {
		t.Fatalf("GenerateTool: %v", err)
	}

	if tool.Name != "register" {
		t.Errorf("Name = %q, want %q", tool.Name, "register")
	}

	schema := schemaMap(t, tool)
	props := propMap(t, schema)

	// campfire_id property present (pre-filled)
	cfProp, ok := props["campfire_id"].(map[string]any)
	if !ok {
		t.Fatal("campfire_id property missing")
	}
	if cfProp["type"] != "string" {
		t.Errorf("campfire_id.type = %v, want string", cfProp["type"])
	}

	// agent_key: key type -> string with hex pattern
	keyProp, ok := props["agent_key"].(map[string]any)
	if !ok {
		t.Fatal("agent_key property missing")
	}
	if keyProp["type"] != "string" {
		t.Errorf("agent_key.type = %v, want string", keyProp["type"])
	}
	if keyProp["pattern"] != "^[0-9a-f]{64}$" {
		t.Errorf("agent_key.pattern = %v, want ^[0-9a-f]{64}$", keyProp["pattern"])
	}
}

func TestGenerateToolName_NoCollision(t *testing.T) {
	decl := parseDeclForTest(t, socialPostPayload)
	existing := map[string]bool{}
	name := GenerateToolName(decl, existing)
	if name != "post" {
		t.Errorf("Name = %q, want %q", name, "post")
	}
}

func TestGenerateToolName_Collision(t *testing.T) {
	decl1 := parseDeclForTest(t, socialPostPayload)
	// second declaration with same operation from different convention
	decl2Payload := []byte(`{
        "convention": "other-format",
        "version": "1.0",
        "operation": "post",
        "description": "Another post op",
        "antecedents": "none",
        "signing": "member_key"
    }`)
	decl2 := parseDeclForTest(t, decl2Payload)

	existing := map[string]bool{}
	name1 := GenerateToolName(decl1, existing)
	existing[name1] = true

	name2 := GenerateToolName(decl2, existing)

	if name1 != "post" {
		t.Errorf("name1 = %q, want %q", name1, "post")
	}
	// On collision, name2 should be prefixed
	if name2 == "post" {
		t.Error("name2 should not be 'post' (collision)")
	}
	if name2 == "" {
		t.Error("name2 should not be empty")
	}
}

func TestNamespacedToolName(t *testing.T) {
	decl := parseDeclForTest(t, beaconRegisterPayload)
	name := NamespacedToolName(decl)
	if name != "beacon_protocol_register" {
		t.Errorf("NamespacedToolName = %q, want %q", name, "beacon_protocol_register")
	}
}

func TestListOperations(t *testing.T) {
	mock := &mockStore{
		records: []store.MessageRecord{
			{
				ID:      "msg1",
				Sender:  "sender1",
				Payload: socialPostPayload,
				Tags:    []string{ConventionOperationTag},
			},
			{
				ID:      "msg2",
				Sender:  "sender2",
				Payload: []byte(`{"not":"valid convention"}`),
				Tags:    []string{ConventionOperationTag},
			},
		},
	}

	decls, err := ListOperations(context.Background(), mock, "campfire123", "")
	if err != nil {
		t.Fatalf("ListOperations: %v", err)
	}
	if len(decls) != 1 {
		t.Errorf("len(decls) = %d, want 1", len(decls))
	}
	if decls[0].Operation != "post" {
		t.Errorf("operation = %q, want %q", decls[0].Operation, "post")
	}
}

// TestListOperations_SupersedesNewerWins verifies that when a declaration has a
// non-empty Supersedes field, the superseded declaration is excluded and only the
// newer one appears in the output.
func TestListOperations_SupersedesNewerWins(t *testing.T) {
	// v1: the original declaration (msg1)
	v1Payload := mustJSON(map[string]any{
		"convention":  "social-post-format",
		"version":     "0.3",
		"operation":   "post",
		"description": "Original post op",
		"antecedents": "none",
		"signing":     "member_key",
	})
	// v2: supersedes v1 (msg2, newer timestamp)
	v2Payload := mustJSON(map[string]any{
		"convention":  "social-post-format",
		"version":     "0.4",
		"operation":   "post",
		"description": "Updated post op",
		"supersedes":  "msg1",
		"antecedents": "none",
		"signing":     "member_key",
	})

	mock := &mockStore{
		records: []store.MessageRecord{
			{
				ID:        "msg1",
				Sender:    "sender1",
				Payload:   v1Payload,
				Tags:      []string{ConventionOperationTag},
				Timestamp: 1000,
			},
			{
				ID:        "msg2",
				Sender:    "sender1",
				Payload:   v2Payload,
				Tags:      []string{ConventionOperationTag},
				Timestamp: 2000,
			},
		},
	}

	decls, err := ListOperations(context.Background(), mock, "campfire123", "")
	if err != nil {
		t.Fatalf("ListOperations: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("len(decls) = %d, want 1 (only the superseding version)", len(decls))
	}
	if decls[0].Version != "0.4" {
		t.Errorf("expected version 0.4 (newer), got %q", decls[0].Version)
	}
	if decls[0].MessageID != "msg2" {
		t.Errorf("expected messageID msg2, got %q", decls[0].MessageID)
	}
}

// TestListOperations_SupersedesOlderLoses verifies that when two declarations
// both claim to supersede the same message, only the one with the later timestamp wins.
func TestListOperations_SupersedesOlderLoses(t *testing.T) {
	origPayload := mustJSON(map[string]any{
		"convention":  "social-post-format",
		"version":     "0.3",
		"operation":   "post",
		"description": "Original",
		"antecedents": "none",
		"signing":     "member_key",
	})
	newerPayload := mustJSON(map[string]any{
		"convention":  "social-post-format",
		"version":     "0.5",
		"operation":   "post",
		"description": "Newer superseder",
		"supersedes":  "msg1",
		"antecedents": "none",
		"signing":     "member_key",
	})
	olderPayload := mustJSON(map[string]any{
		"convention":  "social-post-format",
		"version":     "0.4",
		"operation":   "post",
		"description": "Older superseder",
		"supersedes":  "msg1",
		"antecedents": "none",
		"signing":     "member_key",
	})

	mock := &mockStore{
		records: []store.MessageRecord{
			{ID: "msg1", Sender: "s1", Payload: origPayload, Tags: []string{ConventionOperationTag}, Timestamp: 1000},
			{ID: "msg3", Sender: "s1", Payload: newerPayload, Tags: []string{ConventionOperationTag}, Timestamp: 3000},
			{ID: "msg2", Sender: "s1", Payload: olderPayload, Tags: []string{ConventionOperationTag}, Timestamp: 2000},
		},
	}

	decls, err := ListOperations(context.Background(), mock, "campfire123", "")
	if err != nil {
		t.Fatalf("ListOperations: %v", err)
	}
	// msg1 is superseded. msg2 is a losing superseder (msg3 has newer timestamp).
	// Only msg3 should survive.
	if len(decls) != 1 {
		ids := make([]string, len(decls))
		for i, d := range decls {
			ids[i] = d.MessageID
		}
		t.Fatalf("len(decls) = %d, want 1; got %v", len(decls), ids)
	}
	if decls[0].Version != "0.5" {
		t.Errorf("expected version 0.5 (newest superseder), got %q", decls[0].Version)
	}
}

// TestListOperations_RevokeRemovesDeclaration verifies that a convention:revoke
// message with target_id pointing at a declaration causes it to disappear.
// In offline mode (empty campfireKey), the revoke sender must match the original signer.
func TestListOperations_RevokeRemovesDeclaration(t *testing.T) {
	mock := &mockStore{
		records: []store.MessageRecord{
			{
				ID:        "msg1",
				Sender:    "sender1",
				Payload:   socialPostPayload,
				Tags:      []string{ConventionOperationTag},
				Timestamp: 1000,
			},
			{
				ID:      "revoke1",
				Sender:  "sender1", // must match original signer in offline mode
				Payload: []byte(`{"target_id":"msg1"}`),
				Tags:    []string{"convention:revoke"},
				Timestamp: 2000,
			},
		},
	}

	decls, err := ListOperations(context.Background(), mock, "campfire123", "")
	if err != nil {
		t.Fatalf("ListOperations: %v", err)
	}
	if len(decls) != 0 {
		t.Errorf("expected 0 decls after revoke, got %d", len(decls))
	}
}

// TestListOperations_RevokeDoesNotAffectOthers verifies that a revoke only removes
// the targeted declaration, not all declarations.
// In offline mode (empty campfireKey), the revoke sender must match the original signer.
func TestListOperations_RevokeDoesNotAffectOthers(t *testing.T) {
	otherPayload := mustJSON(map[string]any{
		"convention":  "other-conv",
		"version":     "0.1",
		"operation":   "act",
		"description": "Other operation",
		"antecedents": "none",
		"signing":     "member_key",
	})

	mock := &mockStore{
		records: []store.MessageRecord{
			{
				ID:        "msg1",
				Sender:    "sender1",
				Payload:   socialPostPayload,
				Tags:      []string{ConventionOperationTag},
				Timestamp: 1000,
			},
			{
				ID:        "msg2",
				Sender:    "sender2",
				Payload:   otherPayload,
				Tags:      []string{ConventionOperationTag},
				Timestamp: 1100,
			},
			{
				ID:      "revoke1",
				Sender:  "sender1", // must match original signer of msg1 in offline mode
				Payload: []byte(`{"target_id":"msg1"}`),
				Tags:    []string{"convention:revoke"},
				Timestamp: 2000,
			},
		},
	}

	decls, err := ListOperations(context.Background(), mock, "campfire123", "")
	if err != nil {
		t.Fatalf("ListOperations: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("expected 1 decl (msg2 survives), got %d", len(decls))
	}
	if decls[0].Operation != "act" {
		t.Errorf("expected operation 'act', got %q", decls[0].Operation)
	}
}

// TestListOperations_RevokeSupersededTarget verifies that revoking a superseded
// declaration also removes the superseding declaration (chain invalidation).
func TestListOperations_RevokeSupersededTarget(t *testing.T) {
	v1Payload := mustJSON(map[string]any{
		"convention":  "social-post-format",
		"version":     "0.3",
		"operation":   "post",
		"description": "Original",
		"antecedents": "none",
		"signing":     "member_key",
	})
	v2Payload := mustJSON(map[string]any{
		"convention":  "social-post-format",
		"version":     "0.4",
		"operation":   "post",
		"description": "Updated",
		"supersedes":  "msg1",
		"antecedents": "none",
		"signing":     "member_key",
	})

	mock := &mockStore{
		records: []store.MessageRecord{
			{ID: "msg1", Sender: "s1", Payload: v1Payload, Tags: []string{ConventionOperationTag}, Timestamp: 1000},
			{ID: "msg2", Sender: "s1", Payload: v2Payload, Tags: []string{ConventionOperationTag}, Timestamp: 2000},
			// Revoke targets msg1 (the superseded one): should also remove msg2.
			// Sender must match msg1's original signer ("s1") in offline mode.
			{ID: "rev1", Sender: "s1", Payload: []byte(`{"target_id":"msg1"}`), Tags: []string{"convention:revoke"}, Timestamp: 3000},
		},
	}

	decls, err := ListOperations(context.Background(), mock, "campfire123", "")
	if err != nil {
		t.Fatalf("ListOperations: %v", err)
	}
	if len(decls) != 0 {
		t.Errorf("expected 0 decls (revoke on superseded target invalidates chain), got %d", len(decls))
	}
}

func TestGenerateTool_RepeatedArg(t *testing.T) {
	payload := []byte(`{
        "convention": "test-conv",
        "version": "0.1",
        "operation": "multi",
        "description": "Operation with repeated arg",
        "antecedents": "none",
        "args": [
          {"name": "tags", "type": "string", "repeated": true, "max_count": 5,
           "description": "Multiple tags"}
        ],
        "signing": "member_key"
    }`)
	decl := parseDeclForTest(t, payload)
	tool, err := GenerateTool(decl, "c1")
	if err != nil {
		t.Fatalf("GenerateTool: %v", err)
	}

	schema := schemaMap(t, tool)
	props := propMap(t, schema)

	tagsProp, ok := props["tags"].(map[string]any)
	if !ok {
		t.Fatal("tags property missing")
	}
	if tagsProp["type"] != "array" {
		t.Errorf("tags.type = %v, want array", tagsProp["type"])
	}
	if tagsProp["maxItems"] != float64(5) {
		t.Errorf("tags.maxItems = %v, want 5", tagsProp["maxItems"])
	}
	items, ok := tagsProp["items"].(map[string]any)
	if !ok {
		t.Fatal("tags.items missing")
	}
	if items["type"] != "string" {
		t.Errorf("tags.items.type = %v, want string", items["type"])
	}
}

func TestGenerateTool_DescriptionTruncation(t *testing.T) {
	longDesc := "This is a very long description that exceeds eighty characters and should be truncated by the generator implementation to fit within the limit."
	payload := mustJSON(map[string]any{
		"convention":  "test-conv",
		"version":     "0.1",
		"operation":   "longdesc",
		"description": longDesc,
		"antecedents": "none",
		"signing":     "member_key",
	})
	decl := parseDeclForTest(t, payload)
	tool, err := GenerateTool(decl, "c1")
	if err != nil {
		t.Fatalf("GenerateTool: %v", err)
	}
	if len(tool.Description) > maxToolDescriptionLen {
		t.Errorf("Description length = %d, want <= %d", len(tool.Description), maxToolDescriptionLen)
	}
}

// TestGenerateTool_ResponseSuffix_Sync verifies that a sync declaration appends
// " Returns response directly." to the tool description.
func TestGenerateTool_ResponseSuffix_Sync(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention":  "test-conv",
		"version":     "0.1",
		"operation":   "ask",
		"description": "Ask a question",
		"antecedents": "none",
		"signing":     "member_key",
		"response":    "sync",
	})
	decl := parseDeclForTest(t, payload)
	if !decl.ResponseExplicit {
		t.Fatal("expected ResponseExplicit=true for explicit response field")
	}
	tool, err := GenerateTool(decl, "c1")
	if err != nil {
		t.Fatalf("GenerateTool: %v", err)
	}
	if !strings.HasSuffix(tool.Description, " Returns response directly.") {
		t.Errorf("description should end with sync suffix, got: %q", tool.Description)
	}
	if len(tool.Description) > maxToolDescriptionLen {
		t.Errorf("description exceeds max: len=%d", len(tool.Description))
	}
}

// TestGenerateTool_ResponseSuffix_Async verifies that an async declaration appends
// " Returns message ID." to the tool description.
func TestGenerateTool_ResponseSuffix_Async(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention":  "test-conv",
		"version":     "0.1",
		"operation":   "notify",
		"description": "Send a notification",
		"antecedents": "none",
		"signing":     "member_key",
		"response":    "async",
	})
	decl := parseDeclForTest(t, payload)
	tool, err := GenerateTool(decl, "c1")
	if err != nil {
		t.Fatalf("GenerateTool: %v", err)
	}
	if !strings.HasSuffix(tool.Description, " Returns message ID.") {
		t.Errorf("description should end with async suffix, got: %q", tool.Description)
	}
}

// TestGenerateTool_ResponseSuffix_None verifies that a none declaration does not
// append any suffix to the tool description.
func TestGenerateTool_ResponseSuffix_None(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention":  "test-conv",
		"version":     "0.1",
		"operation":   "broadcast",
		"description": "Send a broadcast",
		"antecedents": "none",
		"signing":     "member_key",
		"response":    "none",
	})
	decl := parseDeclForTest(t, payload)
	tool, err := GenerateTool(decl, "c1")
	if err != nil {
		t.Fatalf("GenerateTool: %v", err)
	}
	if strings.Contains(tool.Description, "Returns") {
		t.Errorf("expected no suffix for none response, got: %q", tool.Description)
	}
}

// TestGenerateTool_ResponseSuffix_Legacy verifies that a declaration without an
// explicit response field (ResponseExplicit=false) does not append any suffix.
func TestGenerateTool_ResponseSuffix_Legacy(t *testing.T) {
	payload := mustJSON(map[string]any{
		"convention":  "test-conv",
		"version":     "0.1",
		"operation":   "post",
		"description": "Post something",
		"antecedents": "none",
		"signing":     "member_key",
		// no "response" field — legacy
	})
	decl := parseDeclForTest(t, payload)
	if decl.ResponseExplicit {
		t.Fatal("expected ResponseExplicit=false for missing response field")
	}
	tool, err := GenerateTool(decl, "c1")
	if err != nil {
		t.Fatalf("GenerateTool: %v", err)
	}
	if strings.Contains(tool.Description, "Returns") {
		t.Errorf("expected no suffix for legacy declaration, got: %q", tool.Description)
	}
}

// TestListOperations_RealSQLiteRoundtrip verifies that supersede/revoke filtering
// works correctly against a real store.Open SQLite database, not just the mock.
func TestListOperations_RealSQLiteRoundtrip(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	campfireID := "cf-real-test"
	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: dir,
		JoinProtocol: "test",
		Role:         "full",
		JoinedAt:     1000,
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	v1Payload := mustJSON(map[string]any{
		"convention":  "social-post-format",
		"version":     "0.3",
		"operation":   "post",
		"description": "Original post op",
		"antecedents": "none",
		"signing":     "member_key",
	})
	v2Payload := mustJSON(map[string]any{
		"convention":  "social-post-format",
		"version":     "0.4",
		"operation":   "post",
		"description": "Updated post op",
		"supersedes":  "msg1",
		"antecedents": "none",
		"signing":     "member_key",
	})

	msg1 := store.MessageRecord{
		ID:         "msg1",
		CampfireID: campfireID,
		Sender:     "sender1",
		Payload:    v1Payload,
		Tags:       []string{ConventionOperationTag},
		Timestamp:  1000,
		Signature:  []byte("sig1"),
		ReceivedAt: 1000,
	}
	msg2 := store.MessageRecord{
		ID:         "msg2",
		CampfireID: campfireID,
		Sender:     "sender1",
		Payload:    v2Payload,
		Tags:       []string{ConventionOperationTag},
		Timestamp:  2000,
		Signature:  []byte("sig2"),
		ReceivedAt: 2000,
	}

	if _, err := s.AddMessage(msg1); err != nil {
		t.Fatalf("AddMessage msg1: %v", err)
	}
	if _, err := s.AddMessage(msg2); err != nil {
		t.Fatalf("AddMessage msg2: %v", err)
	}

	decls, err := ListOperations(context.Background(), s, campfireID, "")
	if err != nil {
		t.Fatalf("ListOperations: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("len(decls) = %d, want 1 (only superseding version)", len(decls))
	}
	if decls[0].Version != "0.4" {
		t.Errorf("expected version 0.4 (newer), got %q", decls[0].Version)
	}
	if decls[0].MessageID != "msg2" {
		t.Errorf("expected messageID msg2, got %q", decls[0].MessageID)
	}
}

// TestListOperations_RevokeUnauthorizedSenderIgnored verifies that a revoke message
// from a sender that is not the campfire key is ignored when campfireKey is non-empty.
// The signing: campfire_key authority check must be enforced.
func TestListOperations_RevokeUnauthorizedSenderIgnored(t *testing.T) {
	mock := &mockStore{
		records: []store.MessageRecord{
			{
				ID:        "msg1",
				Sender:    "sender1",
				Payload:   socialPostPayload,
				Tags:      []string{ConventionOperationTag},
				Timestamp: 1000,
			},
			{
				ID:        "revoke1",
				Sender:    "not-the-campfire-key", // unauthorized sender
				Payload:   []byte(`{"target_id":"msg1"}`),
				Tags:      []string{"convention:revoke"},
				Timestamp: 2000,
			},
		},
	}

	campfireKey := "the-real-campfire-key"
	decls, err := ListOperations(context.Background(), mock, "campfire123", campfireKey)
	if err != nil {
		t.Fatalf("ListOperations: %v", err)
	}
	// The revoke is from an unauthorized sender: msg1 must still be present.
	if len(decls) != 1 {
		t.Errorf("expected 1 decl (unauthorized revoke ignored), got %d", len(decls))
	}
	if len(decls) > 0 && decls[0].Operation != "post" {
		t.Errorf("expected operation 'post', got %q", decls[0].Operation)
	}
}

// TestListOperationsWithRegistry_FallsThrough verifies that when a campfire has no
// inline declarations, ListOperationsWithRegistry reads from the registry campfire.
func TestListOperationsWithRegistry_FallsThrough(t *testing.T) {
	// "inline" campfire has no declarations.
	// "registry" campfire has one declaration.
	multi := &multiCampfireStore{
		stores: map[string][]store.MessageRecord{
			"inline-cf": {},
			"reg-cf": {
				{
					ID:      "reg-msg1",
					Sender:  "reg-sender",
					Payload: socialPostPayload,
					Tags:    []string{ConventionOperationTag},
				},
			},
		},
	}

	decls, err := ListOperationsWithRegistry(context.Background(), multi, "inline-cf", "", "reg-cf")
	if err != nil {
		t.Fatalf("ListOperationsWithRegistry: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("expected 1 decl from registry, got %d", len(decls))
	}
	if decls[0].Operation != "post" {
		t.Errorf("operation = %q, want %q", decls[0].Operation, "post")
	}
}

// TestListOperationsWithRegistry_RegistrySupersedes verifies that a registry
// declaration with a Supersedes field pointing to an inline declaration ID
// causes the inline declaration to be replaced by the registry version.
func TestListOperationsWithRegistry_RegistrySupersedes(t *testing.T) {
	inlinePayload := mustJSON(map[string]any{
		"convention":  "social-post-format",
		"version":     "0.3",
		"operation":   "post",
		"description": "Inline post op",
		"antecedents": "none",
		"signing":     "member_key",
	})
	registryPayload := mustJSON(map[string]any{
		"convention":  "social-post-format",
		"version":     "0.4",
		"operation":   "post",
		"description": "Registry post op (supersedes inline)",
		"supersedes":  "inline-msg1",
		"antecedents": "none",
		"signing":     "member_key",
	})

	multi := &multiCampfireStore{
		stores: map[string][]store.MessageRecord{
			"inline-cf": {
				{
					ID:        "inline-msg1",
					Sender:    "sender1",
					Payload:   inlinePayload,
					Tags:      []string{ConventionOperationTag},
					Timestamp: 1000,
				},
			},
			"reg-cf": {
				{
					ID:        "reg-msg1",
					Sender:    "sender1",
					Payload:   registryPayload,
					Tags:      []string{ConventionOperationTag},
					Timestamp: 2000,
				},
			},
		},
	}

	decls, err := ListOperationsWithRegistry(context.Background(), multi, "inline-cf", "", "reg-cf")
	if err != nil {
		t.Fatalf("ListOperationsWithRegistry: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("expected 1 decl (registry supersedes inline), got %d", len(decls))
	}
	if decls[0].Version != "0.4" {
		t.Errorf("expected version 0.4 (registry), got %q", decls[0].Version)
	}
	if decls[0].MessageID != "reg-msg1" {
		t.Errorf("expected messageID reg-msg1, got %q", decls[0].MessageID)
	}
}

// TestListOperationsWithRegistry_EmptyRegistry verifies that when registryCampfireID
// is empty, ListOperationsWithRegistry behaves identically to ListOperations.
func TestListOperationsWithRegistry_EmptyRegistry(t *testing.T) {
	mock := &mockStore{
		records: []store.MessageRecord{
			{
				ID:      "msg1",
				Sender:  "sender1",
				Payload: socialPostPayload,
				Tags:    []string{ConventionOperationTag},
			},
		},
	}

	decls, err := ListOperationsWithRegistry(context.Background(), mock, "campfire123", "", "")
	if err != nil {
		t.Fatalf("ListOperationsWithRegistry: %v", err)
	}
	if len(decls) != 1 {
		t.Errorf("expected 1 decl, got %d", len(decls))
	}
}

// TestListOperationsWithRegistry_SameCampfire verifies that when
// registryCampfireID equals campfireID, messages are not double-counted.
func TestListOperationsWithRegistry_SameCampfire(t *testing.T) {
	mock := &mockStore{
		records: []store.MessageRecord{
			{
				ID:      "msg1",
				Sender:  "sender1",
				Payload: socialPostPayload,
				Tags:    []string{ConventionOperationTag},
			},
		},
	}

	// Same campfire for both inline and registry — should still return 1 decl.
	decls, err := ListOperationsWithRegistry(context.Background(), mock, "campfire123", "", "campfire123")
	if err != nil {
		t.Fatalf("ListOperationsWithRegistry: %v", err)
	}
	if len(decls) != 1 {
		t.Errorf("expected 1 decl (no double-counting), got %d", len(decls))
	}
}

// TestListOperations_RevokeOfflineMode_OriginalSignerHonored verifies that when
// campfireKey is empty (offline mode), a revoke from the original declaration's
// signer IS honored.
//
// Regression test for: empty campfireKey allowed any sender to revoke any declaration.
func TestListOperations_RevokeOfflineMode_OriginalSignerHonored(t *testing.T) {
	mock := &mockStore{
		records: []store.MessageRecord{
			{
				ID:        "msg1",
				Sender:    "original-signer",
				Payload:   socialPostPayload,
				Tags:      []string{ConventionOperationTag},
				Timestamp: 1000,
			},
			{
				ID:        "revoke1",
				Sender:    "original-signer", // same key that signed the declaration
				Payload:   []byte(`{"target_id":"msg1"}`),
				Tags:      []string{"convention:revoke"},
				Timestamp: 2000,
			},
		},
	}

	// campfireKey is empty — offline mode
	decls, err := ListOperations(context.Background(), mock, "campfire123", "")
	if err != nil {
		t.Fatalf("ListOperations: %v", err)
	}
	// The original signer revokes their own declaration — must be honored.
	if len(decls) != 0 {
		t.Errorf("expected 0 decls (original signer revoke honored), got %d", len(decls))
	}
}

// TestListOperations_RevokeOfflineMode_DifferentSenderIgnored verifies that when
// campfireKey is empty (offline mode), a revoke from a different sender (not the
// original declaration's signer) is NOT honored.
//
// Regression test for: empty campfireKey skipped all authorization, allowing any
// sender to revoke any declaration.
func TestListOperations_RevokeOfflineMode_DifferentSenderIgnored(t *testing.T) {
	mock := &mockStore{
		records: []store.MessageRecord{
			{
				ID:        "msg1",
				Sender:    "original-signer",
				Payload:   socialPostPayload,
				Tags:      []string{ConventionOperationTag},
				Timestamp: 1000,
			},
			{
				ID:        "revoke1",
				Sender:    "attacker-key", // different key — NOT the original signer
				Payload:   []byte(`{"target_id":"msg1"}`),
				Tags:      []string{"convention:revoke"},
				Timestamp: 2000,
			},
		},
	}

	// campfireKey is empty — offline mode
	decls, err := ListOperations(context.Background(), mock, "campfire123", "")
	if err != nil {
		t.Fatalf("ListOperations: %v", err)
	}
	// Revoke from a different sender must be ignored — declaration must still be present.
	if len(decls) != 1 {
		t.Errorf("expected 1 decl (unauthorized revoke ignored in offline mode), got %d", len(decls))
	}
	if len(decls) > 0 && decls[0].Operation != "post" {
		t.Errorf("expected operation 'post', got %q", decls[0].Operation)
	}
}

// TestListOperationsWithRegistry_OfflineRevoke_OriginalSignerHonored verifies that
// when campfireKey is empty (offline mode), a revoke targeting a registry-merged
// declaration IS honored when the revoker matches the original declaration's signer.
//
// Regression test for: opSenderByMsgID built before registry merge — offline revoke
// of registry declarations fails silently (3q8 finding 6). The sender index must
// include registry-merged messages so that offline revoke authorization succeeds.
func TestListOperationsWithRegistry_OfflineRevoke_OriginalSignerHonored(t *testing.T) {
	// inline campfire has no operation declarations.
	// registry campfire has one declaration posted by "reg-signer".
	// inline campfire has a revoke targeting the registry declaration, also by "reg-signer".
	multi := &multiCampfireStore{
		stores: map[string][]store.MessageRecord{
			"inline-cf": {
				{
					ID:        "revoke1",
					Sender:    "reg-signer", // same key that signed the registry declaration
					Payload:   []byte(`{"target_id":"reg-msg1"}`),
					Tags:      []string{"convention:revoke"},
					Timestamp: 2000,
				},
			},
			"reg-cf": {
				{
					ID:        "reg-msg1",
					Sender:    "reg-signer",
					Payload:   socialPostPayload,
					Tags:      []string{ConventionOperationTag},
					Timestamp: 1000,
				},
			},
		},
	}

	// campfireKey is empty — offline mode
	decls, err := ListOperationsWithRegistry(context.Background(), multi, "inline-cf", "", "reg-cf")
	if err != nil {
		t.Fatalf("ListOperationsWithRegistry: %v", err)
	}
	// The original signer revokes their own registry declaration — must be honored.
	if len(decls) != 0 {
		t.Errorf("expected 0 decls (original signer revoke of registry decl honored), got %d", len(decls))
	}
}

// TestListOperationsWithRegistry_OfflineRevoke_DifferentSenderIgnored verifies that
// when campfireKey is empty (offline mode), a revoke targeting a registry-merged
// declaration from a different sender is NOT honored.
//
// Regression test for: opSenderByMsgID built before registry merge — offline revoke
// of registry declarations fails silently (3q8 finding 6). When the sender index
// includes registry-merged messages, unauthorized revoke attempts must be rejected.
func TestListOperationsWithRegistry_OfflineRevoke_DifferentSenderIgnored(t *testing.T) {
	// inline campfire has no operation declarations, but has a revoke from "attacker-key".
	// registry campfire has one declaration posted by "reg-signer".
	multi := &multiCampfireStore{
		stores: map[string][]store.MessageRecord{
			"inline-cf": {
				{
					ID:        "revoke1",
					Sender:    "attacker-key", // NOT the registry declaration's signer
					Payload:   []byte(`{"target_id":"reg-msg1"}`),
					Tags:      []string{"convention:revoke"},
					Timestamp: 2000,
				},
			},
			"reg-cf": {
				{
					ID:        "reg-msg1",
					Sender:    "reg-signer",
					Payload:   socialPostPayload,
					Tags:      []string{ConventionOperationTag},
					Timestamp: 1000,
				},
			},
		},
	}

	// campfireKey is empty — offline mode
	decls, err := ListOperationsWithRegistry(context.Background(), multi, "inline-cf", "", "reg-cf")
	if err != nil {
		t.Fatalf("ListOperationsWithRegistry: %v", err)
	}
	// Revoke from attacker must be ignored — registry declaration must still be present.
	if len(decls) != 1 {
		t.Errorf("expected 1 decl (unauthorized revoke of registry decl ignored), got %d", len(decls))
	}
	if len(decls) > 0 && decls[0].Operation != "post" {
		t.Errorf("expected operation 'post', got %q", decls[0].Operation)
	}
}

// TestListOperations_TransitiveRevokeChain_RealSQLite verifies that revoking msg1
// cascades transitively: msg2.supersedes=msg1, msg3.supersedes=msg2 →
// all three are excluded from ListOperations.
// Uses a real store.Open SQLite database (not mocks) per the done condition.
func TestListOperations_TransitiveRevokeChain_RealSQLite(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "transitive.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	campfireID := "cf-transitive-test"
	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: dir,
		JoinProtocol: "test",
		Role:         "full",
		JoinedAt:     1000,
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	// msg1: original declaration
	msg1Payload := mustJSON(map[string]any{
		"convention":  "social-post-format",
		"version":     "0.3",
		"operation":   "post",
		"description": "Original v1",
		"antecedents": "none",
		"signing":     "member_key",
	})
	// msg2: supersedes msg1
	msg2Payload := mustJSON(map[string]any{
		"convention":  "social-post-format",
		"version":     "0.4",
		"operation":   "post",
		"description": "Updated v2",
		"supersedes":  "msg1",
		"antecedents": "none",
		"signing":     "member_key",
	})
	// msg3: supersedes msg2
	msg3Payload := mustJSON(map[string]any{
		"convention":  "social-post-format",
		"version":     "0.5",
		"operation":   "post",
		"description": "Updated v3",
		"supersedes":  "msg2",
		"antecedents": "none",
		"signing":     "member_key",
	})

	msgs := []store.MessageRecord{
		{
			ID: "msg1", CampfireID: campfireID, Sender: "signer1",
			Payload: msg1Payload, Tags: []string{ConventionOperationTag},
			Timestamp: 1000, Signature: []byte("sig1"), ReceivedAt: 1000,
		},
		{
			ID: "msg2", CampfireID: campfireID, Sender: "signer1",
			Payload: msg2Payload, Tags: []string{ConventionOperationTag},
			Timestamp: 2000, Signature: []byte("sig2"), ReceivedAt: 2000,
		},
		{
			ID: "msg3", CampfireID: campfireID, Sender: "signer1",
			Payload: msg3Payload, Tags: []string{ConventionOperationTag},
			Timestamp: 3000, Signature: []byte("sig3"), ReceivedAt: 3000,
		},
		// Revoke targets msg1 only; the transitive chain must cascade to msg2 and msg3.
		// Sender must match msg1's original signer ("signer1") in offline mode.
		{
			ID: "rev1", CampfireID: campfireID, Sender: "signer1",
			Payload: []byte(`{"target_id":"msg1"}`), Tags: []string{"convention:revoke"},
			Timestamp: 4000, Signature: []byte("rev-sig1"), ReceivedAt: 4000,
		},
	}
	for _, m := range msgs {
		if _, addErr := s.AddMessage(m); addErr != nil {
			t.Fatalf("AddMessage %s: %v", m.ID, addErr)
		}
	}

	decls, err := ListOperations(context.Background(), s, campfireID, "")
	if err != nil {
		t.Fatalf("ListOperations: %v", err)
	}
	if len(decls) != 0 {
		ids := make([]string, len(decls))
		for i, d := range decls {
			ids[i] = d.MessageID
		}
		t.Errorf("expected 0 decls (transitive revoke chain: msg1 revoked → msg2, msg3 also excluded), got %d: %v", len(decls), ids)
	}
}

// mockStore implements StoreReader for testing.
// It filters records by tags when a MessageFilter is provided.
type mockStore struct {
	records []store.MessageRecord
}

func (m *mockStore) ListMessages(campfireID string, afterTimestamp int64, filter ...store.MessageFilter) ([]store.MessageRecord, error) {
	if len(filter) == 0 || len(filter[0].Tags) == 0 {
		return m.records, nil
	}
	// Filter: return records that have ANY of the requested tags (OR semantics),
	// matching the real store implementation in pkg/store/store.go.
	wantTags := filter[0].Tags
	var result []store.MessageRecord
	for _, rec := range m.records {
		if mockRecordHasAnyTag(rec, wantTags) {
			result = append(result, rec)
		}
	}
	return result, nil
}

// mockRecordHasAnyTag returns true if the record contains ANY of wantTags (OR semantics).
// This matches pkg/store/store.go ListMessages which uses ANY-of-given-tags filtering.
func mockRecordHasAnyTag(rec store.MessageRecord, wantTags []string) bool {
	for _, want := range wantTags {
		for _, have := range rec.Tags {
			if have == want {
				return true
			}
		}
	}
	return false
}

// multiCampfireStore implements StoreReader for tests that need multiple campfires.
// Messages are partitioned by campfireID.
type multiCampfireStore struct {
	stores map[string][]store.MessageRecord
}

func (m *multiCampfireStore) ListMessages(campfireID string, afterTimestamp int64, filter ...store.MessageFilter) ([]store.MessageRecord, error) {
	recs := m.stores[campfireID]
	if len(filter) == 0 || len(filter[0].Tags) == 0 {
		return recs, nil
	}
	wantTags := filter[0].Tags
	var result []store.MessageRecord
	for _, rec := range recs {
		if mockRecordHasAnyTag(rec, wantTags) {
			result = append(result, rec)
		}
	}
	return result, nil
}

func TestListOperations_RevokeOfflineMode_EmptySenderRejected(t *testing.T) {
	mock := &mockStore{
		records: []store.MessageRecord{
			{
				ID:        "msg1",
				Sender:    "",
				Payload:   socialPostPayload,
				Tags:      []string{ConventionOperationTag},
				Timestamp: 1000,
			},
			{
				ID:        "revoke1",
				Sender:    "",
				Payload:   []byte(`{"target_id":"msg1"}`),
				Tags:      []string{"convention:revoke"},
				Timestamp: 2000,
			},
		},
	}
	decls, err := ListOperations(context.Background(), mock, "test-cf", "")
	if err != nil {
		t.Fatalf("ListOperations failed: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("expected 1 declaration (empty-sender revoke rejected), got %d", len(decls))
	}
}

func TestListOperations_RevokeOfflineMode_EmptySenderRevokerIgnored(t *testing.T) {
	mock := &mockStore{
		records: []store.MessageRecord{
			{
				ID:        "msg1",
				Sender:    "real-signer",
				Payload:   socialPostPayload,
				Tags:      []string{ConventionOperationTag},
				Timestamp: 1000,
			},
			{
				ID:        "revoke1",
				Sender:    "",
				Payload:   []byte(`{"target_id":"msg1"}`),
				Tags:      []string{"convention:revoke"},
				Timestamp: 2000,
			},
		},
	}
	decls, err := ListOperations(context.Background(), mock, "test-cf", "")
	if err != nil {
		t.Fatalf("ListOperations failed: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("expected 1 declaration (empty-sender revoker ignored), got %d", len(decls))
	}
}

// TestListOperations_OfflineRevoke_RealSQLite verifies that in offline mode
// (campfireKey empty), a revoke from the original declaration signer is honored
// and a revoke from a different sender is ignored — tested against a real
// store.Open SQLite database (not a mock) for the 3q8 logic path.
func TestListOperations_OfflineRevoke_RealSQLite(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "offline-revoke.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	campfireID := "cf-offline-revoke-test"
	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: dir,
		JoinProtocol: "test",
		Role:         "full",
		JoinedAt:     1000,
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	// msg1: declaration posted by "original-signer".
	msgs := []store.MessageRecord{
		{
			ID:        "osr-msg1",
			CampfireID: campfireID,
			Sender:    "original-signer",
			Payload:   socialPostPayload,
			Tags:      []string{ConventionOperationTag},
			Timestamp: 1000,
			Signature: []byte("sig1"),
			ReceivedAt: 1000,
		},
		// revoke1: from the original signer — must be honored in offline mode.
		{
			ID:        "osr-revoke1",
			CampfireID: campfireID,
			Sender:    "original-signer",
			Payload:   []byte(`{"target_id":"osr-msg1"}`),
			Tags:      []string{"convention:revoke"},
			Timestamp: 2000,
			Signature: []byte("rev-sig1"),
			ReceivedAt: 2000,
		},
	}
	for _, m := range msgs {
		if _, addErr := s.AddMessage(m); addErr != nil {
			t.Fatalf("AddMessage %s: %v", m.ID, addErr)
		}
	}

	// campfireKey is empty — offline mode.
	decls, err := ListOperations(context.Background(), s, campfireID, "")
	if err != nil {
		t.Fatalf("ListOperations (original-signer revoke): %v", err)
	}
	// The original signer's own revoke must be honored.
	if len(decls) != 0 {
		t.Errorf("expected 0 decls (original-signer revoke honored), got %d", len(decls))
	}

	// Now add a second declaration and a revoke from a different sender.
	moreRecords := []store.MessageRecord{
		{
			ID:        "osr-msg2",
			CampfireID: campfireID,
			Sender:    "legit-signer",
			Payload:   socialPostPayload,
			Tags:      []string{ConventionOperationTag},
			Timestamp: 3000,
			Signature: []byte("sig2"),
			ReceivedAt: 3000,
		},
		// revoke2: from an attacker — must be ignored in offline mode.
		{
			ID:        "osr-revoke2",
			CampfireID: campfireID,
			Sender:    "attacker-key",
			Payload:   []byte(`{"target_id":"osr-msg2"}`),
			Tags:      []string{"convention:revoke"},
			Timestamp: 4000,
			Signature: []byte("atk-sig"),
			ReceivedAt: 4000,
		},
	}
	for _, m := range moreRecords {
		if _, addErr := s.AddMessage(m); addErr != nil {
			t.Fatalf("AddMessage %s: %v", m.ID, addErr)
		}
	}

	decls2, err := ListOperations(context.Background(), s, campfireID, "")
	if err != nil {
		t.Fatalf("ListOperations (attacker revoke): %v", err)
	}
	// Attacker's revoke must be ignored; legit-signer's declaration survives.
	// (osr-msg1 is still revoked from the first half of the test.)
	if len(decls2) != 1 {
		ids := make([]string, len(decls2))
		for i, d := range decls2 {
			ids[i] = d.MessageID
		}
		t.Errorf("expected 1 decl (attacker revoke ignored, legit decl survives), got %d: %v", len(decls2), ids)
	}
	if len(decls2) > 0 && decls2[0].MessageID != "osr-msg2" {
		t.Errorf("expected surviving decl to be osr-msg2, got %s", decls2[0].MessageID)
	}
}
