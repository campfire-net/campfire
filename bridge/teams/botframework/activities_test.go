package botframework

import (
	"encoding/json"
	"testing"
	"time"
)

// TestParseActivity_Message verifies a well-formed message activity is decoded correctly.
func TestParseActivity_Message(t *testing.T) {
	raw := `{
		"type": "message",
		"id": "abc123",
		"timestamp": "2026-03-22T01:00:00Z",
		"serviceUrl": "https://smba.trafficmanager.net/amer/",
		"channelId": "msteams",
		"from": {"id": "29:user-aad-id", "name": "Baron"},
		"conversation": {"id": "19:xxx@thread.tacv2", "tenantId": "tenant-id"},
		"recipient": {"id": "28:bot-app-id", "name": "Campfire Bridge"},
		"text": "hello world",
		"replyToId": "parent-id"
	}`

	a, err := ParseActivity([]byte(raw))
	if err != nil {
		t.Fatalf("ParseActivity: %v", err)
	}

	if a.Type != ActivityTypeMessage {
		t.Errorf("Type = %q, want %q", a.Type, ActivityTypeMessage)
	}
	if a.ID != "abc123" {
		t.Errorf("ID = %q, want %q", a.ID, "abc123")
	}
	if a.From.ID != "29:user-aad-id" {
		t.Errorf("From.ID = %q, want 29:user-aad-id", a.From.ID)
	}
	if a.Conversation.TenantID != "tenant-id" {
		t.Errorf("Conversation.TenantID = %q, want tenant-id", a.Conversation.TenantID)
	}
	if a.Text != "hello world" {
		t.Errorf("Text = %q, want %q", a.Text, "hello world")
	}
	if a.ReplyToID != "parent-id" {
		t.Errorf("ReplyToID = %q, want parent-id", a.ReplyToID)
	}
}

// TestParseActivity_Invoke verifies an invoke activity (Adaptive Card action) is decoded.
func TestParseActivity_Invoke(t *testing.T) {
	raw := `{
		"type": "invoke",
		"id": "inv-1",
		"serviceUrl": "https://smba.trafficmanager.net/amer/",
		"channelId": "msteams",
		"from": {"id": "29:user"},
		"conversation": {"id": "19:conv"},
		"recipient": {"id": "28:bot"},
		"value": {"action": {"type": "Action.Execute", "verb": "approve"}}
	}`

	a, err := ParseActivity([]byte(raw))
	if err != nil {
		t.Fatalf("ParseActivity: %v", err)
	}
	if a.Type != ActivityTypeInvoke {
		t.Errorf("Type = %q, want %q", a.Type, ActivityTypeInvoke)
	}
	if len(a.Value) == 0 {
		t.Error("Value should be non-empty for invoke")
	}
}

// TestParseActivity_MissingType verifies an error is returned when type is absent.
func TestParseActivity_MissingType(t *testing.T) {
	raw := `{"id": "x", "text": "hi"}`
	_, err := ParseActivity([]byte(raw))
	if err == nil {
		t.Fatal("expected error for missing type, got nil")
	}
}

// TestParseActivity_Malformed verifies malformed JSON returns an error.
func TestParseActivity_Malformed(t *testing.T) {
	_, err := ParseActivity([]byte(`{bad json`))
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

// TestActivity_Serialisation round-trips an Activity through JSON.
func TestActivity_Serialisation(t *testing.T) {
	ts := time.Date(2026, 3, 22, 1, 0, 0, 0, time.UTC)
	cardJSON := json.RawMessage(`{"type":"AdaptiveCard","version":"1.4"}`)

	a := &Activity{
		Type:      ActivityTypeMessage,
		ID:        "msg-1",
		Timestamp: ts,
		ServiceURL: "https://smba.trafficmanager.net/amer/",
		ChannelID: "msteams",
		From:      ChannelAccount{ID: "28:bot-id", Name: "Campfire Bridge"},
		Conversation: ConversationAccount{
			ID:       "19:conv@thread.tacv2",
			TenantID: "tenant-abc",
		},
		Recipient:  ChannelAccount{ID: "29:user-id", Name: "Baron"},
		Text:       "Here is your card",
		Importance: "urgent",
		Attachments: []Attachment{
			{
				ContentType: "application/vnd.microsoft.card.adaptive",
				Content:     cardJSON,
			},
		},
	}

	data, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got Activity
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Type != ActivityTypeMessage {
		t.Errorf("Type = %q", got.Type)
	}
	if got.Importance != "urgent" {
		t.Errorf("Importance = %q, want urgent", got.Importance)
	}
	if len(got.Attachments) != 1 {
		t.Fatalf("Attachments len = %d, want 1", len(got.Attachments))
	}
	if got.Attachments[0].ContentType != "application/vnd.microsoft.card.adaptive" {
		t.Errorf("Attachment ContentType = %q", got.Attachments[0].ContentType)
	}
}

// TestConversationParameters_Serialisation verifies ConversationParameters marshals correctly.
func TestConversationParameters_Serialisation(t *testing.T) {
	params := &ConversationParameters{
		IsGroup: true,
		Bot:     ChannelAccount{ID: "28:bot-id", Name: "Campfire Bridge"},
		Members: []ChannelAccount{
			{ID: "29:user-aad-id", Name: "Baron"},
		},
		TenantID:  "tenant-abc",
		TopicName: "Test conversation",
	}

	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got ConversationParameters
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if !got.IsGroup {
		t.Error("IsGroup should be true")
	}
	if got.Bot.ID != "28:bot-id" {
		t.Errorf("Bot.ID = %q, want 28:bot-id", got.Bot.ID)
	}
	if len(got.Members) != 1 || got.Members[0].ID != "29:user-aad-id" {
		t.Errorf("Members = %v", got.Members)
	}
	if got.TenantID != "tenant-abc" {
		t.Errorf("TenantID = %q", got.TenantID)
	}
}
