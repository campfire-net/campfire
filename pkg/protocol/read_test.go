package protocol_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// setupTestCampfire creates a temporary campfire with a filesystem transport and
// a store. Returns the campfire ID, the campfire identity, the transport, and the store.
// The caller is responsible for calling s.Close().
func setupTestCampfire(t *testing.T) (campfireID string, cfID *identity.Identity, tr *fs.Transport, s store.Store) {
	t.Helper()

	transportBaseDir := t.TempDir()
	cfHomeDir := t.TempDir()

	// Generate campfire identity.
	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	campfireID = cfID.PublicKeyHex()

	// Create transport directory structure.
	cfDir := filepath.Join(transportBaseDir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0755); err != nil {
			t.Fatalf("creating directory %s: %v", sub, err)
		}
	}

	// Write campfire state.
	state := &campfire.CampfireState{
		PublicKey:             cfID.PublicKey,
		PrivateKey:            cfID.PrivateKey,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		CreatedAt:             time.Now().UnixNano(),
	}
	stateData, err := cfencoding.Marshal(state)
	if err != nil {
		t.Fatalf("marshalling campfire state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfDir, "campfire.cbor"), stateData, 0644); err != nil {
		t.Fatalf("writing campfire state: %v", err)
	}

	tr = fs.New(transportBaseDir)

	// Open store and register membership.
	s, err = store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}

	if err := s.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  tr.CampfireDir(campfireID),
		JoinProtocol:  "open",
		Role:          "full",
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	return campfireID, cfID, tr, s
}

// writeTransportMessage writes a signed message directly to the filesystem transport.
// This simulates another agent writing to the campfire without going through the
// local store — exactly the scenario that sync-before-query must handle.
// A provenance hop is added so the message passes zero-provenance rejection.
func writeTransportMessage(t *testing.T, cfID *identity.Identity, tr *fs.Transport, campfireID string, payload string, tags []string) *message.Message {
	t.Helper()

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating sender identity: %v", err)
	}

	msg, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, []byte(payload), tags, []string{})
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}

	if err := msg.AddHop(cfID.PrivateKey, cfID.PublicKey, []byte("testhash"), 1, "open", []string{}, "full"); err != nil {
		t.Fatalf("adding provenance hop: %v", err)
	}

	if err := tr.WriteMessage(campfireID, msg); err != nil {
		t.Fatalf("writing message to transport: %v", err)
	}
	return msg
}

// writeTransportMessageWithAntecedents writes a signed message with the given
// antecedents directly to the filesystem transport. It mirrors writeTransportMessage
// but allows specifying antecedent message IDs (used for testing Await fulfillment).
// A provenance hop is added so the message passes zero-provenance rejection.
func writeTransportMessageWithAntecedents(t *testing.T, cfID *identity.Identity, tr *fs.Transport, campfireID string, payload string, tags []string, antecedents []string) *message.Message {
	t.Helper()

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating sender identity: %v", err)
	}

	msg, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, []byte(payload), tags, antecedents)
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}

	if err := msg.AddHop(cfID.PrivateKey, cfID.PublicKey, []byte("testhash"), 1, "open", []string{}, "full"); err != nil {
		t.Fatalf("adding provenance hop: %v", err)
	}

	if err := tr.WriteMessage(campfireID, msg); err != nil {
		t.Fatalf("writing message to transport: %v", err)
	}
	return msg
}

// TestClientRead_SyncBeforeQuery verifies that Read() syncs from the filesystem
// transport before querying, so messages written via transport but not yet in
// the local store are visible.
func TestClientRead_SyncBeforeQuery(t *testing.T) {
	campfireID, cfID, tr, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	// Write a message to the filesystem transport (not to the store).
	msg := writeTransportMessage(t, cfID, tr, campfireID, "hello from transport", []string{"status"})

	// Read via Client — should sync from transport and return the message.
	result, err := client.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		IncludeCompacted: true,
		AfterTimestamp:   0,
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(result.Messages) == 0 {
		t.Fatal("expected at least one message after sync, got none")
	}

	// Verify the message we wrote is present.
	found := false
	for _, m := range result.Messages {
		if m.ID == msg.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("message %q written via transport not found after sync (got %d messages)", msg.ID, len(result.Messages))
	}
}

// TestClientRead_TagFilter verifies that Read() returns only messages with the
// requested tags when tag filtering is specified.
func TestClientRead_TagFilter(t *testing.T) {
	campfireID, cfID, tr, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	// Write messages with different tags to the filesystem transport.
	msgStatus := writeTransportMessage(t, cfID, tr, campfireID, "status update", []string{"status"})
	msgBlocker := writeTransportMessage(t, cfID, tr, campfireID, "blocked on X", []string{"blocker"})
	_ = writeTransportMessage(t, cfID, tr, campfireID, "found something", []string{"finding"})

	// Read with tag filter for "status".
	result, err := client.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		Tags:             []string{"status"},
		IncludeCompacted: true,
	})
	if err != nil {
		t.Fatalf("Read with status tag: %v", err)
	}

	if len(result.Messages) != 1 {
		t.Errorf("expected 1 message with tag 'status', got %d", len(result.Messages))
	}
	if len(result.Messages) > 0 && result.Messages[0].ID != msgStatus.ID {
		t.Errorf("expected message %q, got %q", msgStatus.ID, result.Messages[0].ID)
	}

	// Read with tag filter for "blocker".
	result, err = client.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		Tags:             []string{"blocker"},
		IncludeCompacted: true,
	})
	if err != nil {
		t.Fatalf("Read with blocker tag: %v", err)
	}

	if len(result.Messages) != 1 {
		t.Errorf("expected 1 message with tag 'blocker', got %d", len(result.Messages))
	}
	if len(result.Messages) > 0 && result.Messages[0].ID != msgBlocker.ID {
		t.Errorf("expected message %q, got %q", msgBlocker.ID, result.Messages[0].ID)
	}
}

// TestClientRead_TagPrefixFilter verifies that Read() returns messages matching
// a tag prefix (e.g. "galtrader:" matches "galtrader:move").
func TestClientRead_TagPrefixFilter(t *testing.T) {
	campfireID, cfID, tr, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	// Write messages with and without the target prefix.
	msgGame := writeTransportMessage(t, cfID, tr, campfireID, "game move", []string{"galtrader:move"})
	_ = writeTransportMessage(t, cfID, tr, campfireID, "other tag", []string{"status"})

	result, err := client.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		TagPrefixes:      []string{"galtrader:"},
		IncludeCompacted: true,
	})
	if err != nil {
		t.Fatalf("Read with tag prefix: %v", err)
	}

	if len(result.Messages) != 1 {
		t.Errorf("expected 1 message matching prefix 'galtrader:', got %d", len(result.Messages))
	}
	if len(result.Messages) > 0 && result.Messages[0].ID != msgGame.ID {
		t.Errorf("expected message %q, got %q", msgGame.ID, result.Messages[0].ID)
	}
}

// TestClientRead_ExcludeTag verifies that Read() excludes messages matching
// the ExcludeTags filter.
func TestClientRead_ExcludeTag(t *testing.T) {
	campfireID, cfID, tr, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	// Write messages.
	_ = writeTransportMessage(t, cfID, tr, campfireID, "should appear", []string{"status"})
	_ = writeTransportMessage(t, cfID, tr, campfireID, "should be excluded", []string{"convention:operation"})

	result, err := client.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		ExcludeTags:      []string{"convention:operation"},
		IncludeCompacted: true,
	})
	if err != nil {
		t.Fatalf("Read with exclude tag: %v", err)
	}

	for _, m := range result.Messages {
		for _, tg := range m.Tags {
			if tg == "convention:operation" {
				t.Errorf("message %q with tag 'convention:operation' should have been excluded", m.ID)
			}
		}
	}
}

// TestClientRead_CursorPagination verifies that AfterTimestamp is respected and
// that MaxTimestamp in the result can be used as a cursor for the next call.
func TestClientRead_CursorPagination(t *testing.T) {
	campfireID, _, _, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	// Add messages directly to the store with fixed timestamps.
	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	addDirectMsg := func(payload string, tags []string, ts int64) string {
		t.Helper()
		msg, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, []byte(payload), tags, []string{})
		if err != nil {
			t.Fatalf("creating message: %v", err)
		}
		msg.Timestamp = ts
		if _, err := s.AddMessage(store.MessageRecordFromMessage(campfireID, msg, store.NowNano())); err != nil {
			t.Fatalf("adding message: %v", err)
		}
		return msg.ID
	}

	msg1ID := addDirectMsg("first", []string{"note"}, 1000)
	msg2ID := addDirectMsg("second", []string{"note"}, 2000)
	msg3ID := addDirectMsg("third", []string{"note"}, 3000)

	// Read all messages.
	result, err := client.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		IncludeCompacted: true,
		SkipSync:         true, // messages are directly in store, no filesystem to sync
	})
	if err != nil {
		t.Fatalf("Read all: %v", err)
	}

	if len(result.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result.Messages))
	}
	if result.MaxTimestamp != 3000 {
		t.Errorf("expected MaxTimestamp=3000, got %d", result.MaxTimestamp)
	}

	// Use the cursor from the first call to get only new messages.
	cursor := result.MaxTimestamp

	msg4ID := addDirectMsg("fourth", []string{"note"}, 4000)

	result2, err := client.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		AfterTimestamp:   cursor,
		IncludeCompacted: true,
		SkipSync:         true,
	})
	if err != nil {
		t.Fatalf("Read after cursor: %v", err)
	}

	if len(result2.Messages) != 1 {
		t.Fatalf("expected 1 new message after cursor, got %d", len(result2.Messages))
	}
	if result2.Messages[0].ID != msg4ID {
		t.Errorf("expected message %q, got %q", msg4ID, result2.Messages[0].ID)
	}

	// Ensure old message IDs are not in the second result.
	for _, m := range result2.Messages {
		for _, old := range []string{msg1ID, msg2ID, msg3ID} {
			if m.ID == old {
				t.Errorf("old message %q appeared after cursor advancement", old)
			}
		}
	}
}

// TestClientRead_Limit verifies that the Limit field caps returned messages.
func TestClientRead_Limit(t *testing.T) {
	campfireID, _, _, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	for i := 0; i < 5; i++ {
		msg, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, []byte("msg"), []string{"note"}, []string{})
		if err != nil {
			t.Fatalf("creating message: %v", err)
		}
		msg.Timestamp = int64(1000 * (i + 1))
		if _, err := s.AddMessage(store.MessageRecordFromMessage(campfireID, msg, store.NowNano())); err != nil {
			t.Fatalf("adding message: %v", err)
		}
	}

	result, err := client.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		IncludeCompacted: true,
		SkipSync:         true,
		Limit:            3,
	})
	if err != nil {
		t.Fatalf("Read with limit: %v", err)
	}

	if len(result.Messages) != 3 {
		t.Errorf("expected 3 messages with Limit=3, got %d", len(result.Messages))
	}
}

// TestClientRead_SkipSync verifies that SkipSync=true skips the filesystem sync.
// When SkipSync is true, messages written to the filesystem transport but not
// yet in the store should NOT appear in results.
func TestClientRead_SkipSync(t *testing.T) {
	campfireID, cfID, tr, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	// Write a message to the filesystem transport only.
	writeTransportMessage(t, cfID, tr, campfireID, "transport only", []string{"status"})

	// Read with SkipSync=true — should NOT see the transport-only message.
	result, err := client.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		IncludeCompacted: true,
		SkipSync:         true,
	})
	if err != nil {
		t.Fatalf("Read with SkipSync: %v", err)
	}

	if len(result.Messages) != 0 {
		t.Errorf("expected 0 messages with SkipSync=true (transport not synced), got %d", len(result.Messages))
	}

	// Now read without SkipSync — should see the message.
	result, err = client.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		IncludeCompacted: true,
		SkipSync:         false,
	})
	if err != nil {
		t.Fatalf("Read without SkipSync: %v", err)
	}

	if len(result.Messages) != 1 {
		t.Errorf("expected 1 message after sync, got %d", len(result.Messages))
	}
}

// TestClientRead_RequiresCampfireID verifies that Read returns an error when
// CampfireID is empty.
func TestClientRead_RequiresCampfireID(t *testing.T) {
	_, _, _, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	_, err := client.Read(protocol.ReadRequest{})
	if err == nil {
		t.Error("expected error when CampfireID is empty, got nil")
	}
}

// TestClientRead_MultipleTagsOR verifies that multiple tags in Tags are OR-combined:
// a message is included if it matches any of the listed tags.
func TestClientRead_MultipleTagsOR(t *testing.T) {
	campfireID, cfID, tr, s := setupTestCampfire(t)
	defer s.Close()

	client := protocol.New(s, nil)

	msgStatus := writeTransportMessage(t, cfID, tr, campfireID, "status update", []string{"status"})
	msgBlocker := writeTransportMessage(t, cfID, tr, campfireID, "blocker", []string{"blocker"})
	_ = writeTransportMessage(t, cfID, tr, campfireID, "finding", []string{"finding"})

	result, err := client.Read(protocol.ReadRequest{
		CampfireID:       campfireID,
		Tags:             []string{"status", "blocker"},
		IncludeCompacted: true,
	})
	if err != nil {
		t.Fatalf("Read with multiple tags: %v", err)
	}

	if len(result.Messages) != 2 {
		t.Errorf("expected 2 messages with tags status OR blocker, got %d", len(result.Messages))
	}

	ids := map[string]bool{}
	for _, m := range result.Messages {
		ids[m.ID] = true
	}
	if !ids[msgStatus.ID] {
		t.Errorf("expected status message %q in results", msgStatus.ID)
	}
	if !ids[msgBlocker.ID] {
		t.Errorf("expected blocker message %q in results", msgBlocker.ID)
	}
}
