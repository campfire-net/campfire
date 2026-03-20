package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/store"
)

func TestFormatDAGLine(t *testing.T) {
	tests := []struct {
		name     string
		msg      store.MessageRecord
		expected string
	}{
		{
			name: "root message with no antecedents",
			msg: store.MessageRecord{
				ID:          "abc123def456abc123def456abc123de",
				Sender:      "aabbccdd11223344",
				Tags:        []string{"status"},
				Antecedents: nil,
				Instance:    "strategist",
			},
			expected: "abc123 [status] agent:aabbcc (strategist) → (root)",
		},
		{
			name: "message with one antecedent",
			msg: store.MessageRecord{
				ID:          "789ghijkl012789ghijkl012789ghijk",
				Sender:      "aaa11122233344455",
				Tags:        []string{"finding"},
				Antecedents: []string{"abc123def456abc123def456abc123de"},
				Instance:    "cfo",
			},
			expected: "789ghi [finding] agent:aaa111 (cfo) → abc123",
		},
		{
			name: "message with multiple antecedents",
			msg: store.MessageRecord{
				ID:          "ddd444eee555ddd444eee555ddd444ee",
				Sender:      "def456789abcdef0",
				Tags:        []string{"decision"},
				Antecedents: []string{"abc123def456abc123def456abc123de", "789ghijkl012789ghijkl012789ghijk"},
				Instance:    "strategist",
			},
			expected: "ddd444 [decision] agent:def456 (strategist) → abc123, 789ghi",
		},
		{
			name: "message with no instance",
			msg: store.MessageRecord{
				ID:          "eee555fff666eee555fff666eee555ff",
				Sender:      "112233445566aabb",
				Tags:        []string{"blocker"},
				Antecedents: nil,
				Instance:    "",
			},
			expected: "eee555 [blocker] agent:112233 → (root)",
		},
		{
			name: "message with multiple tags",
			msg: store.MessageRecord{
				ID:          "fff666ggg777fff666ggg777fff666ff",
				Sender:      "aabbccdd11223344",
				Tags:        []string{"status", "blocker"},
				Antecedents: nil,
				Instance:    "pm",
			},
			expected: "fff666 [status,blocker] agent:aabbcc (pm) → (root)",
		},
		{
			name: "message with no tags",
			msg: store.MessageRecord{
				ID:          "aaa111bbb222aaa111bbb222aaa111bb",
				Sender:      "aabbccdd11223344",
				Tags:        nil,
				Antecedents: []string{"abc123def456abc123def456abc123de"},
				Instance:    "ops",
			},
			expected: "aaa111 agent:aabbcc (ops) → abc123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatDAGLine(tt.msg)
			if result != tt.expected {
				t.Errorf("formatDAGLine() =\n  %q\nwant\n  %q", result, tt.expected)
			}
		})
	}
}

func TestFormatDAGOutput(t *testing.T) {
	msgs := []store.MessageRecord{
		{
			ID:          "abc123def456abc123def456abc123de",
			Sender:      "def456789abcdef0",
			Tags:        []string{"status"},
			Antecedents: nil,
			Instance:    "strategist",
			Timestamp:   1000,
		},
		{
			ID:          "789ghijkl012789ghijkl012789ghijk",
			Sender:      "aaa11122233344455",
			Tags:        []string{"finding"},
			Antecedents: []string{"abc123def456abc123def456abc123de"},
			Instance:    "cfo",
			Timestamp:   2000,
		},
		{
			ID:          "bbb222ccc333bbb222ccc333bbb222cc",
			Sender:      "ccc33344455566677",
			Tags:        []string{"blocker"},
			Antecedents: []string{"789ghijkl012789ghijkl012789ghijk"},
			Instance:    "marketing",
			Timestamp:   3000,
		},
		{
			ID:          "ddd444eee555ddd444eee555ddd444ee",
			Sender:      "def456789abcdef0",
			Tags:        []string{"decision"},
			Antecedents: []string{"bbb222ccc333bbb222ccc333bbb222cc"},
			Instance:    "strategist",
			Timestamp:   4000,
		},
	}

	var buf bytes.Buffer
	formatDAGOutput(msgs, &buf)
	output := buf.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")

	if len(lines) != 4 {
		t.Fatalf("expected 4 lines, got %d: %q", len(lines), output)
	}

	expected := []string{
		"abc123 [status] agent:def456 (strategist) → (root)",
		"789ghi [finding] agent:aaa111 (cfo) → abc123",
		"bbb222 [blocker] agent:ccc333 (marketing) → 789ghi",
		"ddd444 [decision] agent:def456 (strategist) → bbb222",
	}

	for i, exp := range expected {
		if lines[i] != exp {
			t.Errorf("line %d:\n  got  %q\n  want %q", i, lines[i], exp)
		}
	}
}

func TestFormatDAGJSON(t *testing.T) {
	msgs := []store.MessageRecord{
		{
			ID:          "abc123def456abc123def456abc123de",
			Sender:      "def456789abcdef0",
			Tags:        []string{"status"},
			Antecedents: nil,
			Instance:    "strategist",
			Timestamp:   1000,
		},
		{
			ID:          "789ghijkl012789ghijkl012789ghijk",
			Sender:      "aaa11122233344455",
			Tags:        []string{"finding"},
			Antecedents: []string{"abc123def456abc123def456abc123de"},
			Instance:    "",
			Timestamp:   2000,
		},
	}

	var buf bytes.Buffer
	formatDAGJSON(msgs, &buf)

	type dagEntry struct {
		ID          string   `json:"id"`
		Tags        []string `json:"tags"`
		Sender      string   `json:"sender"`
		Instance    string   `json:"instance,omitempty"`
		Antecedents []string `json:"antecedents"`
	}

	var entries []dagEntry
	if err := json.Unmarshal(buf.Bytes(), &entries); err != nil {
		t.Fatalf("invalid JSON output: %v\nraw: %s", err, buf.String())
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// First entry
	if entries[0].ID != "abc123def456abc123def456abc123de" {
		t.Errorf("entry 0 id = %q, want full id", entries[0].ID)
	}
	if len(entries[0].Tags) != 1 || entries[0].Tags[0] != "status" {
		t.Errorf("entry 0 tags = %v, want [status]", entries[0].Tags)
	}
	if entries[0].Sender != "def456789abcdef0" {
		t.Errorf("entry 0 sender = %q, want full sender", entries[0].Sender)
	}
	if entries[0].Instance != "strategist" {
		t.Errorf("entry 0 instance = %q, want strategist", entries[0].Instance)
	}
	if len(entries[0].Antecedents) != 0 {
		t.Errorf("entry 0 antecedents = %v, want empty", entries[0].Antecedents)
	}

	// Second entry — no instance
	if entries[1].Instance != "" {
		t.Errorf("entry 1 instance = %q, want empty", entries[1].Instance)
	}
	if len(entries[1].Antecedents) != 1 || entries[1].Antecedents[0] != "abc123def456abc123def456abc123de" {
		t.Errorf("entry 1 antecedents = %v, want [abc123...]", entries[1].Antecedents)
	}
}

func TestFormatDAGEmptyMessages(t *testing.T) {
	var buf bytes.Buffer
	formatDAGOutput(nil, &buf)
	output := strings.TrimSpace(buf.String())
	if output != "No messages." {
		t.Errorf("empty DAG output = %q, want %q", output, "No messages.")
	}

	buf.Reset()
	formatDAGJSON(nil, &buf)
	output = strings.TrimSpace(buf.String())
	if output != "[]" {
		t.Errorf("empty DAG JSON = %q, want %q", output, "[]")
	}
}
