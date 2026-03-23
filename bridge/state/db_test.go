package state

import (
	"path/filepath"
	"testing"
	"time"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test-bridge.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestSchemaCreation(t *testing.T) {
	db := openTestDB(t)

	// Verify all 5 tables exist by querying each.
	tables := []string{"message_map", "conversation_refs", "teams_acl", "dedup_log", "identity_registry"}
	for _, table := range tables {
		var name string
		err := db.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", table, err)
		}
	}
}

func TestMessageMap(t *testing.T) {
	db := openTestDB(t)

	// Insert mapping.
	if err := db.MapMessage("cf-msg-1", "teams-act-1", "conv-1", "campfire-abc"); err != nil {
		t.Fatal(err)
	}

	// Lookup by campfire ID.
	actID, convID, err := db.LookupTeamsActivity("cf-msg-1")
	if err != nil {
		t.Fatal(err)
	}
	if actID != "teams-act-1" || convID != "conv-1" {
		t.Errorf("got (%q, %q), want (teams-act-1, conv-1)", actID, convID)
	}

	// Lookup by Teams ID.
	cfMsg, err := db.LookupCampfireMsg("teams-act-1")
	if err != nil {
		t.Fatal(err)
	}
	if cfMsg != "cf-msg-1" {
		t.Errorf("got %q, want cf-msg-1", cfMsg)
	}

	// Lookup missing.
	actID, _, err = db.LookupTeamsActivity("nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if actID != "" {
		t.Errorf("expected empty for missing, got %q", actID)
	}
}

func TestConversationRefs(t *testing.T) {
	db := openTestDB(t)

	ref := ConversationRef{
		CampfireID:  "campfire-abc",
		TeamsConvID: "19:test@thread",
		ServiceURL:  "https://smba.trafficmanager.net/amer/",
		TenantID:    "tenant-123",
		ChannelID:   "channel-1",
		BotID:       "bot-1",
	}

	if err := db.UpsertConversationRef(ref); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetConversationRef("campfire-abc")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected ref, got nil")
	}
	if got.ServiceURL != ref.ServiceURL {
		t.Errorf("service_url = %q, want %q", got.ServiceURL, ref.ServiceURL)
	}

	// Missing.
	got, err = db.GetConversationRef("nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil for missing, got %+v", got)
	}
}

func TestGetCampfireForConversation(t *testing.T) {
	db := openTestDB(t)

	// Seed a conversation ref.
	if err := db.UpsertConversationRef(ConversationRef{
		CampfireID:  "campfire-xyz",
		TeamsConvID: "19:conv-lookup@thread",
		ServiceURL:  "https://smba.trafficmanager.net/",
		TenantID:    "tenant-lookup",
		BotID:       "bot-lookup",
	}); err != nil {
		t.Fatal(err)
	}

	// Found case.
	got, err := db.GetCampfireForConversation("19:conv-lookup@thread")
	if err != nil {
		t.Fatal(err)
	}
	if got != "campfire-xyz" {
		t.Errorf("GetCampfireForConversation = %q, want campfire-xyz", got)
	}

	// Missing case: unknown conversation returns empty string, nil error.
	got, err = db.GetCampfireForConversation("19:unknown@thread")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("GetCampfireForConversation (missing) = %q, want empty", got)
	}
}

func TestACL(t *testing.T) {
	db := openTestDB(t)

	// Seed wildcard ACL.
	if err := db.SeedACL("user-1", "*", "Baron"); err != nil {
		t.Fatal(err)
	}
	// Seed specific ACL.
	if err := db.SeedACL("user-2", "campfire-abc", "Other"); err != nil {
		t.Fatal(err)
	}

	// Wildcard allows any campfire.
	ok, err := db.CheckACL("user-1", "campfire-abc")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("wildcard user should have access")
	}

	ok, err = db.CheckACL("user-1", "campfire-xyz")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("wildcard user should have access to any campfire")
	}

	// Specific only allows matching campfire.
	ok, err = db.CheckACL("user-2", "campfire-abc")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("specific user should have access to campfire-abc")
	}

	ok, err = db.CheckACL("user-2", "campfire-xyz")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("specific user should NOT have access to campfire-xyz")
	}

	// Unknown user.
	ok, err = db.CheckACL("unknown", "campfire-abc")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("unknown user should not have access")
	}
}

func TestDedupLog(t *testing.T) {
	db := openTestDB(t)

	exists, err := db.CheckDedup("act-1")
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("should not exist yet")
	}

	if err := db.RecordDedup("act-1", "cf-msg-1"); err != nil {
		t.Fatal(err)
	}

	exists, err = db.CheckDedup("act-1")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Error("should exist after record")
	}
}

func TestIdentityRegistry(t *testing.T) {
	db := openTestDB(t)

	if err := db.SeedIdentity("8205ae", "Atlas (CEO)", "ceo", "warning"); err != nil {
		t.Fatal(err)
	}

	info, err := db.LookupIdentity("8205ae")
	if err != nil {
		t.Fatal(err)
	}
	if info == nil {
		t.Fatal("expected identity, got nil")
	}
	if info.DisplayName != "Atlas (CEO)" || info.Role != "ceo" {
		t.Errorf("got %+v", info)
	}

	// Missing.
	info, err = db.LookupIdentity("unknown")
	if err != nil {
		t.Fatal(err)
	}
	if info != nil {
		t.Errorf("expected nil, got %+v", info)
	}
}

func TestPruneOldEntries(t *testing.T) {
	db := openTestDB(t)

	// Insert entries.
	if err := db.MapMessage("old-msg", "old-act", "conv", "cf"); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordDedup("old-act", "old-msg"); err != nil {
		t.Fatal(err)
	}

	// Prune with 0 max age removes entries at or before now.
	if err := db.PruneOldEntries(0, 0); err != nil {
		t.Fatal(err)
	}

	actID, _, _ := db.LookupTeamsActivity("old-msg")
	if actID != "" {
		t.Error("expected pruned message_map entry")
	}

	exists, _ := db.CheckDedup("old-act")
	if exists {
		t.Error("expected pruned dedup entry")
	}

	// Insert fresh entries, prune with large max age keeps them.
	if err := db.MapMessage("new-msg", "new-act", "conv", "cf"); err != nil {
		t.Fatal(err)
	}
	if err := db.PruneOldEntries(24*time.Hour, 24*time.Hour); err != nil {
		t.Fatal(err)
	}

	actID, _, _ = db.LookupTeamsActivity("new-msg")
	if actID == "" {
		t.Error("fresh entry should not be pruned")
	}
}
