package convention

import (
	"encoding/json"
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
	decl, _, err := Parse([]string{"convention:operation"}, payload, "sender", "campfire")
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

	// agent_key: key type → string with hex pattern
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

func TestListOperations(t *testing.T) {
	mock := &mockStore{
		records: []store.MessageRecord{
			{
				ID:      "msg1",
				Sender:  "sender1",
				Payload: socialPostPayload,
				Tags:    []string{"convention:operation"},
			},
			{
				ID:      "msg2",
				Sender:  "sender2",
				Payload: []byte(`{"not":"valid convention"}`),
				Tags:    []string{"convention:operation"},
			},
		},
	}

	decls, err := ListOperations(mock, "campfire123", "")
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
	if len(tool.Description) > 80 {
		t.Errorf("Description length = %d, want <= 80", len(tool.Description))
	}
}

// mockStore implements StoreReader for testing.
type mockStore struct {
	records []store.MessageRecord
}

func (m *mockStore) ListMessages(campfireID string, afterTimestamp int64, filter ...store.MessageFilter) ([]store.MessageRecord, error) {
	return m.records, nil
}
