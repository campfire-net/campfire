package cmd

// Tests for three bugs:
//   workspace-4a8: runViewRead ignores compaction (superseded messages appear in results)
//   workspace-qem: runPull --fields flag is parsed but ignored in --pull output path
//   workspace-xsr: NAT poll path ignores --tag and --sender filters

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
)

// ---- workspace-4a8: view read respects compaction ----------------------------

// TestViewRead_RespectsCompaction verifies that runViewRead excludes superseded
// messages. The store.MessageFilter{RespectCompaction: true} call in runViewRead
// must filter out messages superseded by a campfire:compact event.
func TestViewRead_RespectsCompaction(t *testing.T) {
	cfHomeDir := t.TempDir()
	transportBaseDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	t.Setenv("CF_TRANSPORT_DIR", transportBaseDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}

	campfireID := setupCampfireWithRole(t, agentID, s, transportBaseDir, campfire.RoleFull)

	// Add two messages with matching predicate.
	msg1ID := addTestMessage(t, s, agentID, campfireID, "old note", []string{"note"}, 1000)
	addTestMessage(t, s, agentID, campfireID, "new note", []string{"note"}, 2000)

	// Create a campfire:compact event that supersedes msg1.
	// Use store.CompactionPayload so Summary is marshalled as base64 bytes (not a plain string).
	compactPayloadObj := store.CompactionPayload{
		Supersedes: []string{msg1ID},
		Summary:    []byte("compacted"),
		Retention:  "archive",
	}
	compactionPayloadBytes, err := json.Marshal(compactPayloadObj)
	if err != nil {
		t.Fatalf("marshalling compaction payload: %v", err)
	}
	addTestMessageRaw(t, s, agentID, campfireID, string(compactionPayloadBytes), []string{"campfire:compact"}, 3000)

	// Create a view that matches "note" messages.
	addViewMessage(t, s, agentID, campfireID, viewDefinition{
		Name:      "notes",
		Predicate: `(tag "note")`,
		Ordering:  "timestamp asc",
		Refresh:   "on-read",
	})
	s.Close()

	// Verify via the store API that compaction filtering works.
	s2, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store 2: %v", err)
	}
	defer s2.Close()

	allMsgs, err := s2.ListMessages(campfireID, 0, store.MessageFilter{RespectCompaction: true})
	if err != nil {
		t.Fatalf("ListMessages with compaction: %v", err)
	}

	// Count "note"-tagged messages in the compaction-filtered result.
	noteMsgs := 0
	for _, m := range allMsgs {
		var tags []string
		json.Unmarshal([]byte(m.Tags), &tags)
		for _, tg := range tags {
			if tg == "note" {
				noteMsgs++
			}
		}
	}

	if noteMsgs != 1 {
		t.Errorf("expected 1 note message after compaction filtering, got %d (bug: runViewRead must pass RespectCompaction: true)", noteMsgs)
	}

	// Also run runViewRead end-to-end and verify msg1ID payload doesn't appear.
	out := captureStdout(t, func() {
		if err := runViewRead(campfireID, "notes"); err != nil {
			t.Errorf("runViewRead: %v", err)
		}
	})

	if strings.Contains(out, "old note") {
		t.Errorf("runViewRead output should not contain superseded 'old note'; got:\n%s\n(bug workspace-4a8: missing RespectCompaction: true)", out)
	}
	if !strings.Contains(out, "new note") {
		t.Errorf("runViewRead output should contain 'new note'; got:\n%s", out)
	}
}

// addTestMessageRaw adds a message with a raw string payload (for compaction event testing).
func addTestMessageRaw(t *testing.T, s *store.Store, agentID *identity.Identity, campfireID string, payload string, tags []string, timestamp int64) string {
	t.Helper()
	msg, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, []byte(payload), tags, []string{})
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}
	msg.Timestamp = timestamp

	tagsJSON, _ := json.Marshal(msg.Tags)
	anteJSON, _ := json.Marshal(msg.Antecedents)
	provJSON, _ := json.Marshal(msg.Provenance)
	if _, err := s.AddMessage(store.MessageRecord{
		ID:          msg.ID,
		CampfireID:  campfireID,
		Sender:      agentID.PublicKeyHex(),
		Payload:     msg.Payload,
		Tags:        string(tagsJSON),
		Antecedents: string(anteJSON),
		Timestamp:   msg.Timestamp,
		Signature:   msg.Signature,
		Provenance:  string(provJSON),
		ReceivedAt:  store.NowNano(),
	}); err != nil {
		t.Fatalf("adding message: %v", err)
	}
	return msg.ID
}

// ---- workspace-qem: --pull respects --fields --------------------------------

// TestRunPull_FieldsProjectionJSON verifies that --fields limits JSON output fields in --pull mode.
func TestRunPull_FieldsProjectionJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CF_HOME", dir)

	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}

	s.AddMembership(store.Membership{CampfireID: "cf1", TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1})
	s.AddMessage(store.MessageRecord{
		ID: "pull-fields-0000-0000-0000-000000000000", CampfireID: "cf1",
		Sender: "aabbccdd", Payload: []byte("pull fields test"), Tags: `["status"]`, Antecedents: "[]",
		Timestamp: 1000000000, Signature: []byte("sig"), Provenance: "[]", ReceivedAt: 2000000000,
	})
	s.Close()

	origJSON := jsonOutput
	defer func() {
		jsonOutput = origJSON
	}()
	fieldsStr := "id,payload"
	jsonOutput = true

	out := captureStdout(t, func() {
		fieldSet, err := parseFieldSet(fieldsStr)
		if err != nil {
			t.Errorf("parseFieldSet: %v", err)
			return
		}
		if err := runPull("pull-fields-0000-0000-0000-000000000000", fieldSet); err != nil {
			t.Errorf("runPull error: %v", err)
		}
	})

	var result []map[string]interface{}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("JSON parse error: %v\noutput: %s", err, out)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	m := result[0]

	if _, ok := m["id"]; !ok {
		t.Error("JSON output missing 'id' field")
	}
	if _, ok := m["payload"]; !ok {
		t.Error("JSON output missing 'payload' field")
	}
	// Fields not in fieldSet must be absent.
	for _, absent := range []string{"sender", "tags", "timestamp", "campfire_id", "antecedents", "provenance"} {
		if _, ok := m[absent]; ok {
			t.Errorf("JSON output should not contain %q when not in --fields (workspace-qem bug)", absent)
		}
	}
}

// TestRunPull_FieldsProjectionHumanReadable verifies --fields limits human-readable output in --pull.
func TestRunPull_FieldsProjectionHumanReadable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CF_HOME", dir)

	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}

	s.AddMembership(store.Membership{CampfireID: "cf1", TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1})
	s.AddMessage(store.MessageRecord{
		ID: "pull-hr-fields-0000-0000-0000-000000000000", CampfireID: "cf1",
		Sender: "aabbccdd", Payload: []byte("human readable pull"), Tags: `["status"]`, Antecedents: "[]",
		Timestamp: 1000000000, Signature: []byte("sig"), Provenance: "[]", ReceivedAt: 2000000000,
		Instance: "worker-pull",
	})
	s.Close()

	origJSON := jsonOutput
	defer func() {
		jsonOutput = origJSON
	}()
	fieldsStr := "payload"
	jsonOutput = false

	out := captureStdout(t, func() {
		fieldSet, _ := parseFieldSet(fieldsStr)
		runPull("pull-hr-fields-0000-0000-0000-000000000000", fieldSet) //nolint:errcheck
	})

	if !strings.Contains(out, "human readable pull") {
		t.Errorf("output missing payload; got: %s", out)
	}
	// instance was not requested; must not appear.
	if strings.Contains(out, "worker-pull") {
		t.Errorf("output should not contain instance when not requested (workspace-qem bug); got: %s", out)
	}
	// tags was not requested.
	if strings.Contains(out, "tags:") {
		t.Errorf("output should not contain tags when not requested; got: %s", out)
	}
}

// TestRunPull_AllFieldsByDefault verifies nil fieldSet returns all fields (regression guard).
func TestRunPull_AllFieldsByDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CF_HOME", dir)

	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}

	s.AddMembership(store.Membership{CampfireID: "cf1", TransportDir: "/tmp", JoinProtocol: "open", Role: "member", JoinedAt: 1})
	s.AddMessage(store.MessageRecord{
		ID: "pull-allfields-0000-0000-0000-000000000000", CampfireID: "cf1",
		Sender: "aabbccdd", Payload: []byte("all fields"), Tags: `["status"]`, Antecedents: "[]",
		Timestamp: 1000000000, Signature: []byte("sig"), Provenance: "[]", ReceivedAt: 2000000000,
	})
	s.Close()

	origJSON := jsonOutput
	defer func() {
		jsonOutput = origJSON
	}()
	fieldsStr := ""
	jsonOutput = true

	out := captureStdout(t, func() {
		fieldSet, _ := parseFieldSet(fieldsStr) // nil fieldSet = all fields
		runPull("pull-allfields-0000-0000-0000-000000000000", fieldSet) //nolint:errcheck
	})

	var result []map[string]interface{}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("JSON parse error: %v\noutput: %s", err, out)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	m := result[0]

	for _, expected := range []string{"id", "sender", "payload", "tags", "timestamp", "campfire_id"} {
		if _, ok := m[expected]; !ok {
			t.Errorf("default JSON output missing field %q", expected)
		}
	}
}

// ---- workspace-xsr: filterNATMessages --------------------------------------

// TestFilterNATMessages_ByTag verifies that filterNATMessages keeps only
// messages that have one of the required tags.
func TestFilterNATMessages_ByTag(t *testing.T) {
	id1, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	makeNATMsg := func(tags []string, payload string) message.Message {
		msg, err := message.NewMessage(id1.PrivateKey, id1.PublicKey, []byte(payload), tags, nil)
		if err != nil {
			t.Fatalf("creating message: %v", err)
		}
		return *msg
	}

	msgs := []message.Message{
		makeNATMsg([]string{"keep"}, "should keep"),
		makeNATMsg([]string{"drop"}, "should drop"),
		makeNATMsg([]string{"keep", "extra"}, "also keep"),
	}

	filtered := filterNATMessages(msgs, []string{"keep"}, "")
	if len(filtered) != 2 {
		t.Errorf("expected 2 messages after tag filter, got %d (workspace-xsr bug)", len(filtered))
	}
	for _, m := range filtered {
		if string(m.Payload) == "should drop" {
			t.Error("filtered result should not contain 'should drop'")
		}
	}
}

// TestFilterNATMessages_BySender verifies that filterNATMessages keeps only
// messages from senders matching the hex prefix.
func TestFilterNATMessages_BySender(t *testing.T) {
	id1, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	id2, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	makeMsg := func(id *identity.Identity, payload string) message.Message {
		msg, err := message.NewMessage(id.PrivateKey, id.PublicKey, []byte(payload), []string{"test"}, nil)
		if err != nil {
			t.Fatalf("creating message: %v", err)
		}
		return *msg
	}

	msgs := []message.Message{
		makeMsg(id1, "from-id1"),
		makeMsg(id2, "from-id2"),
	}

	senderPrefix := id1.PublicKeyHex()[:8]
	filtered := filterNATMessages(msgs, nil, senderPrefix)
	if len(filtered) != 1 {
		t.Errorf("expected 1 message after sender filter, got %d (workspace-xsr bug)", len(filtered))
	}
	if string(filtered[0].Payload) != "from-id1" {
		t.Errorf("expected 'from-id1', got %q", string(filtered[0].Payload))
	}
}

// TestFilterNATMessages_NoFilters verifies that empty filters return all messages.
func TestFilterNATMessages_NoFilters(t *testing.T) {
	id1, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	makeMsg := func(payload string) message.Message {
		msg, err := message.NewMessage(id1.PrivateKey, id1.PublicKey, []byte(payload), []string{"test"}, nil)
		if err != nil {
			t.Fatalf("creating message: %v", err)
		}
		return *msg
	}

	msgs := []message.Message{makeMsg("a"), makeMsg("b"), makeMsg("c")}
	filtered := filterNATMessages(msgs, nil, "")
	if len(filtered) != 3 {
		t.Errorf("expected 3 messages with no filters, got %d", len(filtered))
	}
}

// TestRunNATPoll_TagFilterApplied verifies runNATPoll applies tagFilters from natPollConfig.
func TestRunNATPoll_TagFilterApplied(t *testing.T) {
	campfireID := "nat-tagfilter-test"
	id := tempTestIdentity(t)
	s := tempTestStore(t)

	addTestMembership(t, s, campfireID)
	ep, _ := startTestTransport(t, campfireID, id, s)

	// Store a message tagged "keep" and one tagged "drop".
	storeNATTestMessageWithTags(t, s, campfireID, id, "keep-payload", []string{"keep"})
	storeNATTestMessageWithTags(t, s, campfireID, id, "drop-payload", []string{"drop"})

	cfg := natPollConfig{
		campfireID:  campfireID,
		peers:       []store.PeerEndpoint{{CampfireID: campfireID, MemberPubkey: id.PublicKeyHex(), Endpoint: ep}},
		cursor:      0,
		follow:      false,
		id:          id,
		timeoutSecs: 2,
		tagFilters:  []string{"keep"},
		stopCh:      nil,
	}

	var out bytes.Buffer
	if err := runNATPoll(cfg, &out); err != nil {
		t.Fatalf("runNATPoll: %v", err)
	}

	output := out.String()
	if strings.Contains(output, "drop-payload") {
		t.Errorf("NAT poll should not print 'drop-payload' (workspace-xsr bug: tag filter not applied); got:\n%s", output)
	}
}

// TestRunNATPoll_SenderFilterApplied verifies runNATPoll applies senderFilter from natPollConfig.
func TestRunNATPoll_SenderFilterApplied(t *testing.T) {
	campfireID := "nat-senderfilter-test"
	id1 := tempTestIdentity(t)
	id2 := tempTestIdentity(t)
	s := tempTestStore(t)

	addTestMembership(t, s, campfireID)

	// Register id2 as a peer endpoint (required for store membership checks).
	s.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:   campfireID,
		MemberPubkey: id2.PublicKeyHex(),
		Endpoint:     "",
	})

	ep, _ := startTestTransport(t, campfireID, id1, s)

	storeNATTestMessageWithTags(t, s, campfireID, id1, "from-id1", []string{"test"})
	storeNATTestMessageWithTags(t, s, campfireID, id2, "from-id2", []string{"test"})

	senderPrefix := id1.PublicKeyHex()[:8]

	cfg := natPollConfig{
		campfireID:   campfireID,
		peers:        []store.PeerEndpoint{{CampfireID: campfireID, MemberPubkey: id1.PublicKeyHex(), Endpoint: ep}},
		cursor:       0,
		follow:       false,
		id:           id1,
		timeoutSecs:  2,
		senderFilter: senderPrefix,
		stopCh:       nil,
	}

	var out bytes.Buffer
	if err := runNATPoll(cfg, &out); err != nil {
		t.Fatalf("runNATPoll: %v", err)
	}

	output := out.String()
	if strings.Contains(output, "from-id2") {
		t.Errorf("NAT poll should not print 'from-id2' (workspace-xsr bug: sender filter not applied); got:\n%s", output)
	}
}

// storeNATTestMessageWithTags stores a signed message for NAT poll filter tests.
func storeNATTestMessageWithTags(t *testing.T, s *store.Store, campfireID string, id *identity.Identity, payload string, tags []string) store.MessageRecord {
	t.Helper()
	msg, err := message.NewMessage(id.PrivateKey, id.PublicKey, []byte(payload), tags, nil)
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}
	tagsJSON, _ := json.Marshal(msg.Tags)
	rec := store.MessageRecord{
		ID:          msg.ID,
		CampfireID:  campfireID,
		Sender:      id.PublicKeyHex(),
		Payload:     msg.Payload,
		Tags:        string(tagsJSON),
		Antecedents: `[]`,
		Timestamp:   msg.Timestamp,
		Signature:   msg.Signature,
		Provenance:  `[]`,
		ReceivedAt:  time.Now().UnixNano(),
	}
	if _, err := s.AddMessage(rec); err != nil {
		t.Fatalf("storing message: %v", err)
	}
	return rec
}
