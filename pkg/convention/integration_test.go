package convention_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/trust"
)

// ---- payloads ----

var socialPostPayload = []byte(`{
    "convention": "social-post-format",
    "version": "0.3",
    "operation": "post",
    "description": "Publish a social post",
    "produces_tags": [
      {"tag": "social:post", "cardinality": "exactly_one"},
      {"tag": "content:*", "cardinality": "at_most_one",
       "values": ["content:text/plain", "content:text/markdown", "content:application/json"]},
      {"tag": "topic:*", "cardinality": "zero_to_many", "max": 10, "pattern": "[a-z0-9-]{1,64}"},
      {"tag": "social:*", "cardinality": "zero_to_many",
       "values": ["social:need", "social:have", "social:offer", "social:request", "social:question", "social:answer"]}
    ],
    "args": [
      {"name": "text", "type": "string", "required": true, "max_length": 65536, "description": "Post content"},
      {"name": "content_type", "type": "enum", "values": ["text/plain", "text/markdown", "application/json"], "default": "text/plain"},
      {"name": "topics", "type": "string", "repeated": true, "max_count": 10, "pattern": "[a-z0-9-]{1,64}", "description": "Topic tags"},
      {"name": "coordination", "type": "enum", "values": ["need", "have", "offer", "request", "question", "answer"], "repeated": true, "description": "Coordination signal tags"}
    ],
    "antecedents": "none",
    "payload_required": true,
    "signing": "member_key"
}`)

var beaconRegisterPayload = []byte(`{
    "convention": "community-beacon-metadata",
    "version": "0.3",
    "operation": "register",
    "description": "Register a campfire in this directory",
    "produces_tags": [
      {"tag": "beacon:registration", "cardinality": "exactly_one"},
      {"tag": "category:*", "cardinality": "exactly_one",
       "values": ["category:social", "category:jobs", "category:commerce", "category:search", "category:infrastructure"]},
      {"tag": "topic:*", "cardinality": "zero_to_many", "max": 5}
    ],
    "args": [
      {"name": "campfire_id", "type": "key", "required": true},
      {"name": "description", "type": "string", "required": true, "max_length": 280},
      {"name": "category", "type": "enum", "values": ["social", "jobs", "commerce", "search", "infrastructure"], "required": true}
    ],
    "payload_required": true,
    "signing": "campfire_key",
    "rate_limit": {"max": 5, "per": "campfire_id", "window": "24h"}
}`)

var profileUpdatePayload = []byte(`{
    "convention": "agent-profile",
    "version": "0.3",
    "operation": "update",
    "description": "Update profile",
    "steps": [
      {
        "action": "query",
        "description": "Find prior profile",
        "future_tags": ["future", "profile:query"],
        "future_payload": {"query_type": "by_key", "key": "$self_key"},
        "result_binding": "prior_profile"
      },
      {
        "action": "send",
        "description": "Send updated profile",
        "tags": ["profile:agent-profile"],
        "antecedents": ["$prior_profile.msg_id"]
      }
    ],
    "args": [
      {"name": "display_name", "type": "string", "max_length": 64}
    ],
    "signing": "member_key"
}`)

// ---- mock transport ----

type integrationTransport struct {
	sent         []integrationSent
	futureResult []byte
}

type integrationSent struct {
	campfireID  string
	tags        []string
	antecedents []string
	campfireKey bool
}

func (t *integrationTransport) SendMessage(_ context.Context, campfireID string, _ []byte, tags []string, antecedents []string) (string, error) {
	t.sent = append(t.sent, integrationSent{campfireID: campfireID, tags: tags, antecedents: antecedents})
	return "msg-" + campfireID, nil
}

func (t *integrationTransport) SendCampfireKeySigned(_ context.Context, campfireID string, _ []byte, tags []string, antecedents []string) (string, error) {
	t.sent = append(t.sent, integrationSent{campfireID: campfireID, tags: tags, antecedents: antecedents, campfireKey: true})
	return "msg-ck-" + campfireID, nil
}

func (t *integrationTransport) ReadMessages(_ context.Context, _ string, _ []string) ([]convention.MessageRecord, error) {
	return nil, nil
}

func (t *integrationTransport) SendFutureAndAwait(_ context.Context, campfireID string, _ []byte, _ []string, _ time.Duration) ([]byte, error) {
	if t.futureResult != nil {
		return t.futureResult, nil
	}
	return []byte(`{"msg_id":"prior-msg-123"}`), nil
}

// ---- helpers ----

func hasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

// ---- tests ----

// TestIntegration_SocialPostRoundtrip exercises the full stack: parse → authority → toolgen → execute.
func TestIntegration_SocialPostRoundtrip(t *testing.T) {
	tags := []string{convention.ConventionOperationTag}
	senderKey := "aabbcc" + strings.Repeat("0", 58)
	campfireKey := "deadbeef" + strings.Repeat("0", 56)

	// 1. Parse.
	decl, result, err := convention.Parse(tags, socialPostPayload, senderKey, campfireKey)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if !result.Valid {
		t.Fatalf("expected Valid=true, got warnings: %v", result.Warnings)
	}

	// 2. Authority — member_key signing with no chain → untrusted.
	auth := trust.ResolveAuthority(decl, nil)
	if auth != trust.AuthorityUntrusted {
		t.Errorf("expected AuthorityUntrusted, got %s", auth)
	}

	// 3. GenerateTool — schema must include text arg and campfire_id.
	campfireID := "cf-integration-test"
	tool, err := convention.GenerateTool(decl, campfireID)
	if err != nil {
		t.Fatalf("GenerateTool failed: %v", err)
	}
	if tool.Name != "post" {
		t.Errorf("tool name: want %q, got %q", "post", tool.Name)
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("unmarshal InputSchema: %v", err)
	}
	props, _ := schema["properties"].(map[string]any)
	if props == nil {
		t.Fatal("schema missing properties")
	}
	if _, ok := props["text"]; !ok {
		t.Error("schema missing 'text' property")
	}
	if cidProp, ok := props["campfire_id"].(map[string]any); !ok {
		t.Error("schema missing campfire_id property")
	} else if cidProp["default"] != campfireID {
		t.Errorf("campfire_id default: want %q, got %v", campfireID, cidProp["default"])
	}

	// 4. Execute — verify message sent with social:post tag.
	tr := &integrationTransport{}
	exec := convention.NewExecutor(tr, senderKey)
	err = exec.Execute(context.Background(), decl, campfireID, map[string]any{
		"text": "Hello campfire world",
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if len(tr.sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(tr.sent))
	}
	if !hasTag(tr.sent[0].tags, "social:post") {
		t.Errorf("sent message missing social:post tag; got %v", tr.sent[0].tags)
	}
}

// TestIntegration_TrustGate verifies that a member-signed campfire_key declaration is gated.
func TestIntegration_TrustGate(t *testing.T) {
	tags := []string{convention.ConventionOperationTag}
	senderKey := "aabbcc" + strings.Repeat("0", 58)
	campfireKey := "deadbeef" + strings.Repeat("0", 56) // different from sender

	decl, result, err := convention.Parse(tags, beaconRegisterPayload, senderKey, campfireKey)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	// CampfireKeyAuthorized must be false — sender != campfire key.
	if result.CampfireKeyAuthorized {
		t.Error("expected CampfireKeyAuthorized=false")
	}
	if result.Valid {
		t.Error("expected Valid=false when campfire_key not authorized")
	}

	// SignerType must be member_key — the security fix: claiming campfire_key signing
	// while not being the campfire key must not grant campfire_key authority.
	if decl.SignerType != convention.SignerMemberKey {
		t.Errorf("expected SignerType=member_key, got %s", decl.SignerType)
	}

	// ResolveAuthority must gate this as untrusted.
	auth := trust.ResolveAuthority(decl, nil)
	if auth != trust.AuthorityUntrusted {
		t.Errorf("expected AuthorityUntrusted, got %s", auth)
	}
}

// TestIntegration_CampfireKeyAccepted verifies that campfire-key-signed operations are accepted.
func TestIntegration_CampfireKeyAccepted(t *testing.T) {
	tags := []string{convention.ConventionOperationTag}
	// When senderKey == campfireKey the operation is authorized.
	campfireKey := "deadbeef" + strings.Repeat("0", 56)
	senderKey := campfireKey

	decl, result, err := convention.Parse(tags, beaconRegisterPayload, senderKey, campfireKey)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if !result.CampfireKeyAuthorized {
		t.Error("expected CampfireKeyAuthorized=true")
	}
	if !result.Valid {
		t.Errorf("expected Valid=true, warnings: %v", result.Warnings)
	}
	if decl.SignerType != convention.SignerCampfireKey {
		t.Errorf("expected SignerType=campfire_key, got %s", decl.SignerType)
	}

	// GenerateTool must include campfire_id and category args.
	tool, err := convention.GenerateTool(decl, "cf-test")
	if err != nil {
		t.Fatalf("GenerateTool failed: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	props, _ := schema["properties"].(map[string]any)
	if _, ok := props["campfire_id"]; !ok {
		t.Error("schema missing campfire_id property")
	}
	if _, ok := props["category"]; !ok {
		t.Error("schema missing category property")
	}
}

// TestIntegration_OperationalOverride validates tightening vs. loosening of constraints.
func TestIntegration_OperationalOverride(t *testing.T) {
	tags := []string{convention.ConventionOperationTag}
	key := strings.Repeat("a", 64)

	registry, _, err := convention.Parse(tags, socialPostPayload, key, key)
	if err != nil {
		t.Fatalf("Parse registry decl: %v", err)
	}

	// Valid override: max_length tightened from 65536 to 32768.
	tightPayload := []byte(`{
		"convention": "social-post-format",
		"version": "0.3",
		"operation": "post",
		"signing": "member_key",
		"args": [
			{"name": "text", "type": "string", "required": true, "max_length": 32768}
		]
	}`)
	local, _, err := convention.Parse(tags, tightPayload, key, key)
	if err != nil {
		t.Fatalf("Parse local (tight) decl: %v", err)
	}
	if err := trust.ValidateOperationalOverride(registry, local); err != nil {
		t.Errorf("expected valid tightening override, got error: %v", err)
	}

	// Invalid override: max_length loosened beyond registry value (65536 → 100000).
	loosePayload := []byte(`{
		"convention": "social-post-format",
		"version": "0.3",
		"operation": "post",
		"signing": "member_key",
		"args": [
			{"name": "text", "type": "string", "required": true, "max_length": 100000}
		]
	}`)
	localLoose, _, err := convention.Parse(tags, loosePayload, key, key)
	if err != nil {
		t.Fatalf("Parse local (loose) decl: %v", err)
	}
	if err := trust.ValidateOperationalOverride(registry, localLoose); err == nil {
		t.Error("expected error for loosening override, got nil")
	}
}

// TestIntegration_EnvelopeWrapping verifies the envelope structure and content sanitization.
func TestIntegration_EnvelopeWrapping(t *testing.T) {
	campfireID := "cf-envelope-test"
	content := map[string]any{
		"message": "hello world",
	}

	env := trust.BuildEnvelope(campfireID, trust.TrustVerified, content)

	// verified.campfire_id must be present.
	if env.Verified.CampfireID != campfireID {
		t.Errorf("verified.campfire_id: want %q, got %q", campfireID, env.Verified.CampfireID)
	}

	// runtime_computed.trust_chain must be "verified".
	if env.RuntimeComputed.TrustChain != trust.TrustVerified {
		t.Errorf("runtime_computed.trust_chain: want %q, got %q", trust.TrustVerified, env.RuntimeComputed.TrustChain)
	}

	// tainted.content_classification must be "tainted".
	if env.Tainted.ContentClassification != "tainted" {
		t.Errorf("tainted.content_classification: want %q, got %q", "tainted", env.Tainted.ContentClassification)
	}

	// tainted.content must preserve original content (no sanitization needed here).
	sanitized, ok := env.Tainted.Content.(map[string]any)
	if !ok {
		t.Fatalf("tainted.content not a map, got %T", env.Tainted.Content)
	}
	if sanitized["message"] != "hello world" {
		t.Errorf("tainted.content.message: want %q, got %v", "hello world", sanitized["message"])
	}

	// Content using a reserved envelope key must be prefixed.
	mimicContent := map[string]any{
		"verified": "i am definitely verified",
	}
	env2 := trust.BuildEnvelope(campfireID, trust.TrustVerified, mimicContent)
	sanitized2, ok := env2.Tainted.Content.(map[string]any)
	if !ok {
		t.Fatalf("tainted.content not a map: %T", env2.Tainted.Content)
	}
	if _, bad := sanitized2["verified"]; bad {
		t.Error("reserved key 'verified' was not escaped in sanitized content")
	}
	if _, ok := sanitized2["content_verified"]; !ok {
		t.Error("expected sanitized content to have 'content_verified' key after escape")
	}
}

// TestIntegration_WorkflowExecution verifies multi-step workflow: query binds result, send uses it as antecedent.
func TestIntegration_WorkflowExecution(t *testing.T) {
	tags := []string{convention.ConventionOperationTag}
	key := strings.Repeat("b", 64)

	decl, _, err := convention.Parse(tags, profileUpdatePayload, key, key)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(decl.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(decl.Steps))
	}

	// Transport returns a fulfillment with msg_id so the send step can bind it as antecedent.
	tr := &integrationTransport{
		futureResult: []byte(`{"msg_id":"prior-msg-abc"}`),
	}
	exec := convention.NewExecutor(tr, key)
	err = exec.Execute(context.Background(), decl, "cf-workflow-test", map[string]any{
		"display_name": "Test Agent",
	})
	if err != nil {
		t.Fatalf("Execute workflow failed: %v", err)
	}

	// One send step should have fired (the query step uses SendFutureAndAwait, not SendMessage).
	if len(tr.sent) != 1 {
		t.Fatalf("expected 1 sent message from send step, got %d", len(tr.sent))
	}
	sent := tr.sent[0]

	// The send step must have used the bound msg_id as an antecedent.
	if len(sent.antecedents) != 1 || sent.antecedents[0] != "prior-msg-abc" {
		t.Errorf("send step antecedents: want [%q], got %v", "prior-msg-abc", sent.antecedents)
	}

	// The send step must have the profile tag.
	if !hasTag(sent.tags, "profile:agent-profile") {
		t.Errorf("send step missing profile:agent-profile tag; got %v", sent.tags)
	}
}
