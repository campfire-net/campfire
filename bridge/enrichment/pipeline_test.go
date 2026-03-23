package enrichment_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/bridge/enrichment"
	"github.com/campfire-net/campfire/bridge/state"
	"github.com/campfire-net/campfire/pkg/store"
)

// openTempDB creates a temporary SQLite state DB for testing.
func openTempDB(t *testing.T) *state.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := state.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// makeMsg returns a minimal MessageRecord for testing.
func makeMsg(campfireID, senderHex string, tags, antecedents []string, ts time.Time) store.MessageRecord {
	idSuffix := campfireID
	if len(idSuffix) > 4 {
		idSuffix = idSuffix[:4]
	}
	return store.MessageRecord{
		ID:          "msg-" + idSuffix,
		CampfireID:  campfireID,
		Sender:      senderHex,
		Payload:     []byte("hello world"),
		Tags:        tags,
		Antecedents: antecedents,
		Timestamp:   ts.Unix(),
		Instance:    "test-agent",
	}
}

// ---- Stage 1: Sender Resolution ----

func TestSenderResolution_KnownIdentity(t *testing.T) {
	db := openTempDB(t)
	pubkey := "aabbccdd11223344aabbccdd11223344aabbccdd11223344aabbccdd11223344"
	if err := db.SeedIdentity(pubkey, "Atlas", "ceo", "warning"); err != nil {
		t.Fatalf("SeedIdentity: %v", err)
	}

	msg := makeMsg("cafe0000", pubkey, nil, nil, time.Now())
	em := enrichment.Enrich(msg, enrichment.EnrichOptions{DB: db})

	if em.SenderName != "Atlas" {
		t.Errorf("SenderName = %q, want %q", em.SenderName, "Atlas")
	}
	if em.SenderRole != "ceo" {
		t.Errorf("SenderRole = %q, want %q", em.SenderRole, "ceo")
	}
	if em.SenderColor != "warning" {
		t.Errorf("SenderColor = %q, want %q", em.SenderColor, "warning")
	}
}

func TestSenderResolution_UnknownIdentity_FallsBackToHexPrefix(t *testing.T) {
	db := openTempDB(t)
	pubkey := "deadbeef99887766deadbeef99887766deadbeef99887766deadbeef99887766"

	msg := makeMsg("cafe0000", pubkey, nil, nil, time.Now())
	em := enrichment.Enrich(msg, enrichment.EnrichOptions{DB: db})

	if em.SenderName != "deadbeef" {
		t.Errorf("SenderName = %q, want first 8 hex chars %q", em.SenderName, "deadbeef")
	}
	if em.SenderRole != "" {
		t.Errorf("SenderRole = %q, want empty", em.SenderRole)
	}
	if em.SenderColor != "default" {
		t.Errorf("SenderColor = %q, want %q", em.SenderColor, "default")
	}
}

func TestSenderResolution_NilDB_FallsBackToHexPrefix(t *testing.T) {
	pubkey := "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	msg := makeMsg("cafe0000", pubkey, nil, nil, time.Now())
	em := enrichment.Enrich(msg, enrichment.EnrichOptions{DB: nil})

	if em.SenderName != "12345678" {
		t.Errorf("SenderName = %q, want %q", em.SenderName, "12345678")
	}
}

func TestSenderResolution_ShortPubkey_NoTruncation(t *testing.T) {
	db := openTempDB(t)
	pubkey := "abcd" // shorter than 8 chars
	msg := makeMsg("cafe0000", pubkey, nil, nil, time.Now())
	em := enrichment.Enrich(msg, enrichment.EnrichOptions{DB: db})

	if em.SenderName != "abcd" {
		t.Errorf("SenderName = %q, want %q", em.SenderName, "abcd")
	}
}

// ---- Stage 3: Urgency Scoring ----

func TestUrgency_BlockerTag_High(t *testing.T) {
	msg := makeMsg("cafe0000", "aa", []string{"blocker"}, nil, time.Now())
	em := enrichment.Enrich(msg, enrichment.EnrichOptions{})
	if em.Urgency != enrichment.UrgencyHigh {
		t.Errorf("Urgency = %v, want HIGH", em.Urgency)
	}
}

func TestUrgency_GateTag_High(t *testing.T) {
	msg := makeMsg("cafe0000", "aa", []string{"gate"}, nil, time.Now())
	em := enrichment.Enrich(msg, enrichment.EnrichOptions{})
	if em.Urgency != enrichment.UrgencyHigh {
		t.Errorf("Urgency = %v, want HIGH", em.Urgency)
	}
}

func TestUrgency_EscalationTag_High(t *testing.T) {
	msg := makeMsg("cafe0000", "aa", []string{"escalation"}, nil, time.Now())
	em := enrichment.Enrich(msg, enrichment.EnrichOptions{})
	if em.Urgency != enrichment.UrgencyHigh {
		t.Errorf("Urgency = %v, want HIGH", em.Urgency)
	}
}

func TestUrgency_UrgentCampfire_Medium(t *testing.T) {
	// score = 5 → MEDIUM
	msg := makeMsg("c1a62854df1b", "aa", nil, nil, time.Now())
	em := enrichment.Enrich(msg, enrichment.EnrichOptions{
		UrgentCampfires: []string{"c1a62854df1b"},
	})
	if em.Urgency != enrichment.UrgencyMedium {
		t.Errorf("Urgency = %v, want MEDIUM", em.Urgency)
	}
}

func TestUrgency_UrgentCampfirePrefix_Medium(t *testing.T) {
	// prefix match
	msg := makeMsg("c1a62854df1bc8ee0550", "aa", nil, nil, time.Now())
	em := enrichment.Enrich(msg, enrichment.EnrichOptions{
		UrgentCampfires: []string{"c1a62854df1b"},
	})
	if em.Urgency != enrichment.UrgencyMedium {
		t.Errorf("Urgency = %v, want MEDIUM (prefix match)", em.Urgency)
	}
}

func TestUrgency_ConversationHeat_Medium(t *testing.T) {
	// >3 antecedents + recent timestamp → score = 3 → MEDIUM
	now := time.Now()
	msg := makeMsg("cafe0000", "aa", nil, []string{"a1", "a2", "a3", "a4"}, now)
	em := enrichment.Enrich(msg, enrichment.EnrichOptions{})
	if em.Urgency != enrichment.UrgencyMedium {
		t.Errorf("Urgency = %v, want MEDIUM (conversation heat)", em.Urgency)
	}
}

func TestUrgency_ConversationHeat_StaleMessage_Low(t *testing.T) {
	// >3 antecedents but old timestamp → no heat bonus
	old := time.Now().Add(-2 * time.Minute)
	msg := makeMsg("cafe0000", "aa", nil, []string{"a1", "a2", "a3", "a4"}, old)
	em := enrichment.Enrich(msg, enrichment.EnrichOptions{})
	if em.Urgency != enrichment.UrgencyLow {
		t.Errorf("Urgency = %v, want LOW (stale message, no heat)", em.Urgency)
	}
}

func TestUrgency_NoTags_NoUrgentCampfire_Low(t *testing.T) {
	msg := makeMsg("cafe0000", "aa", nil, nil, time.Now())
	em := enrichment.Enrich(msg, enrichment.EnrichOptions{})
	if em.Urgency != enrichment.UrgencyLow {
		t.Errorf("Urgency = %v, want LOW", em.Urgency)
	}
}

func TestUrgency_StatusTag_Low(t *testing.T) {
	// "status" tag doesn't trigger any bonus
	msg := makeMsg("cafe0000", "aa", []string{"status"}, nil, time.Now())
	em := enrichment.Enrich(msg, enrichment.EnrichOptions{})
	if em.Urgency != enrichment.UrgencyLow {
		t.Errorf("Urgency = %v, want LOW (status tag has no score)", em.Urgency)
	}
}

func TestUrgency_BlockerPlusUrgentCampfire_High(t *testing.T) {
	// score = 10 + 5 = 15 → HIGH
	msg := makeMsg("c1a62854df1b", "aa", []string{"blocker"}, nil, time.Now())
	em := enrichment.Enrich(msg, enrichment.EnrichOptions{
		UrgentCampfires: []string{"c1a62854df1b"},
	})
	if em.Urgency != enrichment.UrgencyHigh {
		t.Errorf("Urgency = %v, want HIGH (blocker+urgent campfire)", em.Urgency)
	}
}

// ---- Pipeline: field propagation ----

func TestEnrich_FieldPropagation(t *testing.T) {
	db := openTempDB(t)
	pubkey := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	if err := db.SeedIdentity(pubkey, "Bot", "bridge", "accent"); err != nil {
		t.Fatalf("SeedIdentity: %v", err)
	}

	campfireID := "abcdef123456789000001111222233334444"
	ts := time.Date(2026, 3, 22, 12, 0, 0, 0, time.UTC)
	msg := store.MessageRecord{
		ID:          "msg-xyz",
		CampfireID:  campfireID,
		Sender:      pubkey,
		Payload:     []byte("test payload"),
		Tags:        []string{"finding"},
		Antecedents: []string{"prev-msg"},
		Timestamp:   ts.Unix(),
		Instance:    "mybot",
	}

	em := enrichment.Enrich(msg, enrichment.EnrichOptions{DB: db})

	if em.MessageID != "msg-xyz" {
		t.Errorf("MessageID = %q, want %q", em.MessageID, "msg-xyz")
	}
	if em.CampfireID != campfireID {
		t.Errorf("CampfireID = %q, want %q", em.CampfireID, campfireID)
	}
	if em.CampfireShortID != campfireID[:12] {
		t.Errorf("CampfireShortID = %q, want %q", em.CampfireShortID, campfireID[:12])
	}
	if em.Instance != "mybot" {
		t.Errorf("Instance = %q, want %q", em.Instance, "mybot")
	}
	if em.Payload != "test payload" {
		t.Errorf("Payload = %q, want %q", em.Payload, "test payload")
	}
	if len(em.Tags) != 1 || em.Tags[0] != "finding" {
		t.Errorf("Tags = %v, want [finding]", em.Tags)
	}
	if len(em.Antecedents) != 1 || em.Antecedents[0] != "prev-msg" {
		t.Errorf("Antecedents = %v, want [prev-msg]", em.Antecedents)
	}
	if !em.Timestamp.Equal(ts) {
		t.Errorf("Timestamp = %v, want %v", em.Timestamp, ts)
	}
	if em.SenderName != "Bot" {
		t.Errorf("SenderName = %q, want Bot", em.SenderName)
	}
}

func TestEnrich_ShortCampfireID_NoTruncation(t *testing.T) {
	msg := makeMsg("abc", "aa", nil, nil, time.Now())
	em := enrichment.Enrich(msg, enrichment.EnrichOptions{})
	if em.CampfireShortID != "abc" {
		t.Errorf("CampfireShortID = %q, want %q", em.CampfireShortID, "abc")
	}
}

// ---- Integration: real SQLite DB ----

func TestEnrich_RealSQLiteDB(t *testing.T) {
	dir, err := os.MkdirTemp("", "enrichment-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(dir)

	db, err := state.Open(filepath.Join(dir, "bridge.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer db.Close()

	pubkey := "1e224c14bc7959bf6bec7d4e56331e224c14bc7959bf6bec7d4e56331e224c1"
	if err := db.SeedIdentity(pubkey, "Campfire Agent", "bridge", "accent"); err != nil {
		t.Fatalf("SeedIdentity: %v", err)
	}

	msg := makeMsg("deadbeef0000", pubkey, []string{"status"}, nil, time.Now())
	em := enrichment.Enrich(msg, enrichment.EnrichOptions{DB: db})

	if em.SenderName != "Campfire Agent" {
		t.Errorf("SenderName = %q, want %q", em.SenderName, "Campfire Agent")
	}
	if em.SenderColor != "accent" {
		t.Errorf("SenderColor = %q, want %q", em.SenderColor, "accent")
	}
	if em.Urgency != enrichment.UrgencyLow {
		t.Errorf("Urgency = %v, want LOW", em.Urgency)
	}
}
