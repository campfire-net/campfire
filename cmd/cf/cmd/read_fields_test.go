package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/store"
)

// helper to build a minimal MessageRecord for field projection tests.
func makeFieldsTestRecord(id, cfID, sender, instance, payload string, tags []string, timestamp int64) store.MessageRecord {
	return store.MessageRecord{
		ID:          id,
		CampfireID:  cfID,
		Sender:      sender,
		Instance:    instance,
		Payload:     []byte(payload),
		Tags:        tags,
		Antecedents: nil,
		Timestamp:   timestamp,
		Signature:   []byte("sig"),
		Provenance:  nil,
		ReceivedAt:  timestamp + 1000,
	}
}

// TestParseFieldSet_ValidFields verifies that known field names are accepted.
func TestParseFieldSet_ValidFields(t *testing.T) {
	validInputs := []string{
		"id",
		"sender",
		"payload",
		"tags",
		"timestamp",
		"antecedents",
		"signature",
		"provenance",
		"instance",
		"campfire_id",
		"id,sender,payload",
		"tags,timestamp,sender",
	}
	for _, input := range validInputs {
		t.Run(input, func(t *testing.T) {
			_, err := parseFieldSet(input)
			if err != nil {
				t.Errorf("parseFieldSet(%q) unexpected error: %v", input, err)
			}
		})
	}
}

// TestParseFieldSet_InvalidField verifies that unknown field names are rejected.
func TestParseFieldSet_InvalidField(t *testing.T) {
	_, err := parseFieldSet("id,unknown_field")
	if err == nil {
		t.Fatal("expected error for unknown field name, got nil")
	}
	if !strings.Contains(err.Error(), "unknown_field") {
		t.Errorf("error should mention the unknown field, got: %v", err)
	}
}

// TestParseFieldSet_Empty verifies empty string returns nil (all fields).
func TestParseFieldSet_Empty(t *testing.T) {
	fs, err := parseFieldSet("")
	if err != nil {
		t.Fatalf("parseFieldSet(\"\") unexpected error: %v", err)
	}
	if fs != nil {
		t.Errorf("parseFieldSet(\"\") = %v, want nil", fs)
	}
}

// TestProjectMessage_SelectedFields verifies human-readable output is filtered.
func TestProjectMessage_SelectedFields(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CF_HOME", dir)

	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}

	rec := makeFieldsTestRecord(
		"msg-fields-0000-0000-0000-000000000000",
		"cf1",
		"aabbccdd",
		"worker-1",
		"hello from projection",
		[]string{"status"},
		1000000000,
	)
	s.AddMessage(rec) //nolint:errcheck
	s.Close()

	// Capture stdout.
	origStdout := captureStdout(t, func() {
		fields, _ := parseFieldSet("id,sender,payload")
		msgs := []store.MessageRecord{rec}
		printMessagesWithFields(msgs, nil, fields)
	})

	// Should contain id and payload.
	if !strings.Contains(origStdout, rec.ID[:8]) {
		t.Errorf("output missing id prefix; got:\n%s", origStdout)
	}
	if !strings.Contains(origStdout, "hello from projection") {
		t.Errorf("output missing payload; got:\n%s", origStdout)
	}
	// Should NOT contain tags line (not requested).
	if strings.Contains(origStdout, "tags:") {
		t.Errorf("output should not contain tags when not requested; got:\n%s", origStdout)
	}
	// Should NOT contain instance (not requested).
	if strings.Contains(origStdout, "worker-1") {
		t.Errorf("output should not contain instance when not requested; got:\n%s", origStdout)
	}
}

// TestProjectMessage_DefaultAllFields verifies nil field set returns all fields.
func TestProjectMessage_DefaultAllFields(t *testing.T) {
	rec := makeFieldsTestRecord(
		"msg-allf-0000-0000-0000-000000000000",
		"cf1",
		"aabbccdd",
		"worker-2",
		"full output",
		[]string{"status"},
		2000000000,
	)

	out := captureStdout(t, func() {
		printMessagesWithFields([]store.MessageRecord{rec}, nil, nil)
	})

	if !strings.Contains(out, "tags:") {
		t.Errorf("default output should include tags; got:\n%s", out)
	}
	if !strings.Contains(out, "worker-2") {
		t.Errorf("default output should include instance; got:\n%s", out)
	}
	if !strings.Contains(out, "full output") {
		t.Errorf("default output should include payload; got:\n%s", out)
	}
}

// TestProjectMessageJSON_SelectedFields verifies JSON output is filtered.
func TestProjectMessageJSON_SelectedFields(t *testing.T) {
	rec := makeFieldsTestRecord(
		"msg-json-0000-0000-0000-000000000000",
		"cf2",
		"deadbeef",
		"",
		"json payload",
		[]string{"finding"},
		3000000000,
	)

	var buf bytes.Buffer
	fields, _ := parseFieldSet("id,sender,payload")
	err := encodeMessagesJSONWithFields([]store.MessageRecord{rec}, fields, &buf)
	if err != nil {
		t.Fatalf("encodeMessagesJSONWithFields error: %v", err)
	}

	var out []map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("JSON parse error: %v\noutput: %s", err, buf.String())
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	m := out[0]

	// Requested fields should be present.
	if _, ok := m["id"]; !ok {
		t.Error("JSON missing 'id' field")
	}
	if _, ok := m["sender"]; !ok {
		t.Error("JSON missing 'sender' field")
	}
	if _, ok := m["payload"]; !ok {
		t.Error("JSON missing 'payload' field")
	}

	// Non-requested fields should be absent.
	for _, absent := range []string{"tags", "timestamp", "antecedents", "provenance", "instance", "campfire_id"} {
		if _, ok := m[absent]; ok {
			t.Errorf("JSON should not contain %q when not requested", absent)
		}
	}
}

// TestProjectMessageJSON_DefaultAllFields verifies nil field set returns all JSON fields.
func TestProjectMessageJSON_DefaultAllFields(t *testing.T) {
	rec := makeFieldsTestRecord(
		"msg-jsona-0000-0000-0000-000000000000",
		"cf3",
		"cafebabe",
		"session-x",
		"full json",
		[]string{"status"},
		4000000000,
	)

	var buf bytes.Buffer
	err := encodeMessagesJSONWithFields([]store.MessageRecord{rec}, nil, &buf)
	if err != nil {
		t.Fatalf("encodeMessagesJSONWithFields error: %v", err)
	}

	var out []map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("JSON parse error: %v", err)
	}
	m := out[0]

	for _, expected := range []string{"id", "sender", "payload", "tags", "timestamp", "campfire_id"} {
		if _, ok := m[expected]; !ok {
			t.Errorf("default JSON missing field %q", expected)
		}
	}
}

// TestReadCmd_InvalidFieldErrors verifies the command errors on unknown field.
func TestReadCmd_InvalidFieldErrors(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CF_HOME", dir)

	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	s.AddMembership(store.Membership{CampfireID: "cf1", TransportDir: dir, JoinProtocol: "open", Role: "member", JoinedAt: 1})
	s.Close()

	if err := readCmd.Flags().Set("fields", "id,bogus_field"); err != nil {
		t.Fatalf("setting fields flag: %v", err)
	}
	defer readCmd.Flags().Set("fields", "") //nolint:errcheck

	err = readCmd.RunE(readCmd, []string{"cf1"})
	if err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
	if !strings.Contains(err.Error(), "bogus_field") {
		t.Errorf("error should mention unknown field name; got: %v", err)
	}
}

// captureStdout is a test helper that captures os.Stdout during fn().
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	buf.ReadFrom(r)
	r.Close()
	return buf.String()
}
