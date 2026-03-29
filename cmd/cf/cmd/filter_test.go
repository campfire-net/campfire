package cmd

import (
	"testing"

	"github.com/campfire-net/campfire/pkg/store"
)

func TestFilterMessages_NoFilters(t *testing.T) {
	msgs := []store.MessageRecord{
		{ID: "a1", Sender: "aabbcc", Tags: []string{"status"}},
		{ID: "a2", Sender: "ddeeff", Tags: []string{"blocker"}},
	}
	result := filterMessages(msgs, store.MessageFilter{})
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
}

func TestFilterMessages_SingleTag(t *testing.T) {
	msgs := []store.MessageRecord{
		{ID: "a1", Sender: "aabbcc", Tags: []string{"status"}},
		{ID: "a2", Sender: "ddeeff", Tags: []string{"blocker"}},
		{ID: "a3", Sender: "112233", Tags: []string{"status", "finding"}},
	}
	result := filterMessages(msgs, store.MessageFilter{Tags: []string{"blocker"}})
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	if result[0].ID != "a2" {
		t.Errorf("expected message a2, got %s", result[0].ID)
	}
}

func TestFilterMessages_MultipleTags_OR(t *testing.T) {
	msgs := []store.MessageRecord{
		{ID: "a1", Sender: "aabbcc", Tags: []string{"status"}},
		{ID: "a2", Sender: "ddeeff", Tags: []string{"blocker"}},
		{ID: "a3", Sender: "112233", Tags: []string{"finding"}},
		{ID: "a4", Sender: "445566", Tags: []string{"decision"}},
	}
	result := filterMessages(msgs, store.MessageFilter{Tags: []string{"blocker", "finding"}})
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	if result[0].ID != "a2" || result[1].ID != "a3" {
		t.Errorf("expected messages a2 and a3, got %s and %s", result[0].ID, result[1].ID)
	}
}

func TestFilterMessages_SenderPrefix(t *testing.T) {
	msgs := []store.MessageRecord{
		{ID: "a1", Sender: "aabbcc112233", Tags: []string{"status"}},
		{ID: "a2", Sender: "ddeeff445566", Tags: []string{"blocker"}},
		{ID: "a3", Sender: "aabbcc778899", Tags: []string{"finding"}},
	}
	result := filterMessages(msgs, store.MessageFilter{Sender: "aabbcc"})
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	if result[0].ID != "a1" || result[1].ID != "a3" {
		t.Errorf("expected messages a1 and a3, got %s and %s", result[0].ID, result[1].ID)
	}
}

func TestFilterMessages_SenderPrefixCaseInsensitive(t *testing.T) {
	msgs := []store.MessageRecord{
		{ID: "a1", Sender: "AABBCC112233", Tags: []string{"status"}},
		{ID: "a2", Sender: "ddeeff445566", Tags: []string{"blocker"}},
	}
	result := filterMessages(msgs, store.MessageFilter{Sender: "aabbcc"})
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	if result[0].ID != "a1" {
		t.Errorf("expected message a1, got %s", result[0].ID)
	}
}

func TestFilterMessages_TagAndSenderCombined(t *testing.T) {
	msgs := []store.MessageRecord{
		{ID: "a1", Sender: "aabbcc112233", Tags: []string{"blocker"}},
		{ID: "a2", Sender: "ddeeff445566", Tags: []string{"blocker"}},
		{ID: "a3", Sender: "aabbcc778899", Tags: []string{"status"}},
	}
	result := filterMessages(msgs, store.MessageFilter{Tags: []string{"blocker"}, Sender: "aabbcc"})
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	if result[0].ID != "a1" {
		t.Errorf("expected message a1, got %s", result[0].ID)
	}
}

func TestFilterMessages_NoMatch(t *testing.T) {
	msgs := []store.MessageRecord{
		{ID: "a1", Sender: "aabbcc", Tags: []string{"status"}},
	}
	result := filterMessages(msgs, store.MessageFilter{Tags: []string{"blocker"}})
	if len(result) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(result))
	}
}

func TestFilterMessages_EmptyTags(t *testing.T) {
	msgs := []store.MessageRecord{
		{ID: "a1", Sender: "aabbcc", Tags: nil},
		{ID: "a2", Sender: "ddeeff", Tags: []string{"blocker"}},
	}
	result := filterMessages(msgs, store.MessageFilter{Tags: []string{"blocker"}})
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
}

func TestFilterMessages_NullTags(t *testing.T) {
	msgs := []store.MessageRecord{
		{ID: "a1", Sender: "aabbcc", Tags: nil},
		{ID: "a2", Sender: "ddeeff", Tags: []string{"blocker"}},
	}
	result := filterMessages(msgs, store.MessageFilter{Tags: []string{"blocker"}})
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
}

func TestFilterMessages_TagPrefix(t *testing.T) {
	msgs := []store.MessageRecord{
		{ID: "a1", Tags: []string{"galtrader:buy"}},
		{ID: "a2", Tags: []string{"galtrader:sell"}},
		{ID: "a3", Tags: []string{"status"}},
		{ID: "a4", Tags: []string{"galtrader:query-status"}},
	}
	result := filterMessages(msgs, store.MessageFilter{TagPrefixes: []string{"galtrader:"}})
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
}

func TestFilterMessages_ExcludeTag(t *testing.T) {
	msgs := []store.MessageRecord{
		{ID: "a1", Tags: []string{"galtrader:buy"}},
		{ID: "a2", Tags: []string{"galtrader:sell", "convention:operation"}},
		{ID: "a3", Tags: []string{"galtrader:query-status"}},
	}
	result := filterMessages(msgs, store.MessageFilter{
		TagPrefixes: []string{"galtrader:"},
		ExcludeTags: []string{"convention:operation"},
	})
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	if result[0].ID != "a1" || result[1].ID != "a3" {
		t.Errorf("expected a1 and a3, got %s and %s", result[0].ID, result[1].ID)
	}
}

func TestFilterMessages_ExcludeTagPrefix(t *testing.T) {
	msgs := []store.MessageRecord{
		{ID: "a1", Tags: []string{"galtrader:buy"}},
		{ID: "a2", Tags: []string{"convention:operation"}},
		{ID: "a3", Tags: []string{"convention:declare"}},
		{ID: "a4", Tags: []string{"status"}},
	}
	result := filterMessages(msgs, store.MessageFilter{ExcludeTagPrefixes: []string{"convention:"}})
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	if result[0].ID != "a1" || result[1].ID != "a4" {
		t.Errorf("expected a1 and a4, got %s and %s", result[0].ID, result[1].ID)
	}
}

func TestFilterMessages_MixedExactAndPrefix(t *testing.T) {
	msgs := []store.MessageRecord{
		{ID: "a1", Tags: []string{"galtrader:buy"}},
		{ID: "a2", Tags: []string{"status"}},
		{ID: "a3", Tags: []string{"blocker"}},
		{ID: "a4", Tags: []string{"galtrader:sell"}},
	}
	result := filterMessages(msgs, store.MessageFilter{
		Tags:        []string{"status"},
		TagPrefixes: []string{"galtrader:"},
	})
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
}
