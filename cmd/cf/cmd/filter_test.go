package cmd

import (
	"testing"

	"github.com/campfire-net/campfire/pkg/store"
)

func TestFilterMessages_NoFilters(t *testing.T) {
	msgs := []store.MessageRecord{
		{ID: "a1", Sender: "aabbcc", Tags: `["status"]`},
		{ID: "a2", Sender: "ddeeff", Tags: `["blocker"]`},
	}
	result := filterMessages(msgs, nil, "")
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
}

func TestFilterMessages_SingleTag(t *testing.T) {
	msgs := []store.MessageRecord{
		{ID: "a1", Sender: "aabbcc", Tags: `["status"]`},
		{ID: "a2", Sender: "ddeeff", Tags: `["blocker"]`},
		{ID: "a3", Sender: "112233", Tags: `["status","finding"]`},
	}
	result := filterMessages(msgs, []string{"blocker"}, "")
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	if result[0].ID != "a2" {
		t.Errorf("expected message a2, got %s", result[0].ID)
	}
}

func TestFilterMessages_MultipleTags_OR(t *testing.T) {
	msgs := []store.MessageRecord{
		{ID: "a1", Sender: "aabbcc", Tags: `["status"]`},
		{ID: "a2", Sender: "ddeeff", Tags: `["blocker"]`},
		{ID: "a3", Sender: "112233", Tags: `["finding"]`},
		{ID: "a4", Sender: "445566", Tags: `["decision"]`},
	}
	result := filterMessages(msgs, []string{"blocker", "finding"}, "")
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	if result[0].ID != "a2" || result[1].ID != "a3" {
		t.Errorf("expected messages a2 and a3, got %s and %s", result[0].ID, result[1].ID)
	}
}

func TestFilterMessages_SenderPrefix(t *testing.T) {
	msgs := []store.MessageRecord{
		{ID: "a1", Sender: "aabbcc112233", Tags: `["status"]`},
		{ID: "a2", Sender: "ddeeff445566", Tags: `["blocker"]`},
		{ID: "a3", Sender: "aabbcc778899", Tags: `["finding"]`},
	}
	result := filterMessages(msgs, nil, "aabbcc")
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	if result[0].ID != "a1" || result[1].ID != "a3" {
		t.Errorf("expected messages a1 and a3, got %s and %s", result[0].ID, result[1].ID)
	}
}

func TestFilterMessages_SenderPrefixCaseInsensitive(t *testing.T) {
	msgs := []store.MessageRecord{
		{ID: "a1", Sender: "AABBCC112233", Tags: `["status"]`},
		{ID: "a2", Sender: "ddeeff445566", Tags: `["blocker"]`},
	}
	result := filterMessages(msgs, nil, "aabbcc")
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	if result[0].ID != "a1" {
		t.Errorf("expected message a1, got %s", result[0].ID)
	}
}

func TestFilterMessages_TagAndSenderCombined(t *testing.T) {
	msgs := []store.MessageRecord{
		{ID: "a1", Sender: "aabbcc112233", Tags: `["blocker"]`},
		{ID: "a2", Sender: "ddeeff445566", Tags: `["blocker"]`},
		{ID: "a3", Sender: "aabbcc778899", Tags: `["status"]`},
	}
	result := filterMessages(msgs, []string{"blocker"}, "aabbcc")
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	if result[0].ID != "a1" {
		t.Errorf("expected message a1, got %s", result[0].ID)
	}
}

func TestFilterMessages_NoMatch(t *testing.T) {
	msgs := []store.MessageRecord{
		{ID: "a1", Sender: "aabbcc", Tags: `["status"]`},
	}
	result := filterMessages(msgs, []string{"blocker"}, "")
	if len(result) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(result))
	}
}

func TestFilterMessages_EmptyTags(t *testing.T) {
	msgs := []store.MessageRecord{
		{ID: "a1", Sender: "aabbcc", Tags: `[]`},
		{ID: "a2", Sender: "ddeeff", Tags: `["blocker"]`},
	}
	result := filterMessages(msgs, []string{"blocker"}, "")
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
}

func TestFilterMessages_NullTags(t *testing.T) {
	msgs := []store.MessageRecord{
		{ID: "a1", Sender: "aabbcc", Tags: ``},
		{ID: "a2", Sender: "ddeeff", Tags: `["blocker"]`},
	}
	result := filterMessages(msgs, []string{"blocker"}, "")
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
}
