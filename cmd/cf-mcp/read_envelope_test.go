package main

import (
	"encoding/json"
	"testing"

	"github.com/campfire-net/campfire/pkg/trust"
)

// TestReadEnvelopeTrustFields verifies that campfire_read wraps responses in a
// Trust v0.2 envelope with trust_status, operator_provenance, and campfire_id.
func TestReadEnvelopeTrustFields(t *testing.T) {
	srv, st := newTestServerWithStore(t)

	// Init identity and create a campfire.
	initResp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_init","arguments":{}}`))
	if initResp.Error != nil {
		t.Fatalf("campfire_init failed: %s", initResp.Error.Message)
	}

	createResp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_create","arguments":{}}`))
	if createResp.Error != nil {
		t.Fatalf("campfire_create failed: %s", createResp.Error.Message)
	}

	// Get the campfire ID from the membership list.
	memberships, err := st.ListMemberships()
	if err != nil || len(memberships) == 0 {
		t.Fatalf("no memberships found: %v", err)
	}
	campfireID := memberships[0].CampfireID

	// Send a message.
	sendArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
		"message":     "test message for envelope",
	})
	sendResp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_send","arguments":`+string(sendArgs)+`}`))
	if sendResp.Error != nil {
		t.Fatalf("campfire_send failed: %s", sendResp.Error.Message)
	}

	// Read messages.
	readArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
		"all":         true,
	})
	readResp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_read","arguments":`+string(readArgs)+`}`))
	if readResp.Error != nil {
		t.Fatalf("campfire_read failed: %s", readResp.Error.Message)
	}

	// Extract the envelope from the response.
	readText := extractResultText(t, readResp)
	var envelope trust.Envelope
	if err := json.Unmarshal([]byte(readText), &envelope); err != nil {
		t.Fatalf("campfire_read result is not a valid Trust v0.2 envelope: %v", err)
	}

	// Verify envelope fields.
	if envelope.Verified.CampfireID != campfireID {
		t.Errorf("verified.campfire_id = %q, want %q", envelope.Verified.CampfireID, campfireID)
	}
	if envelope.RuntimeComputed.TrustStatus == "" {
		t.Error("runtime_computed.trust_status is empty")
	}
	if envelope.Tainted.Content == nil {
		t.Fatal("tainted.content is nil — expected message array")
	}

	// Verify messages are in tainted.content.
	contentBytes, err := json.Marshal(envelope.Tainted.Content)
	if err != nil {
		t.Fatalf("marshaling tainted.content: %v", err)
	}
	var messages []map[string]interface{}
	if err := json.Unmarshal(contentBytes, &messages); err != nil {
		t.Fatalf("tainted.content is not a message array: %v", err)
	}
	if len(messages) == 0 {
		t.Fatal("expected at least one message in tainted.content")
	}

	// Find the user's message (skip audit entries).
	var found bool
	for _, msg := range messages {
		if _, ok := msg["campfire_id"]; !ok {
			t.Error("message missing campfire_id field")
		}
		if _, ok := msg["sender"]; !ok {
			t.Error("message missing sender field")
		}
		if payload, ok := msg["payload"].(string); ok && payload == "test message for envelope" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected message with payload %q in tainted.content", "test message for envelope")
	}
}
