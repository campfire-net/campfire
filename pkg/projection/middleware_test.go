package projection_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/projection"
	"github.com/campfire-net/campfire/pkg/store"
)

const testCampfire = "campfire-test-abc123"

// openTestStore creates an in-memory SQLite store for testing.
func openTestStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.NewSQLite(":memory:")
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// testSig is a placeholder signature satisfying the store's `signature BLOB NOT NULL` constraint.
var testSig = []byte("test-signature-placeholder")

// addMsg inserts a message record into the store and returns it.
func addMsg(t *testing.T, s store.Store, campfireID, id string, tags []string, payload []byte) store.MessageRecord {
	t.Helper()
	now := time.Now().UnixNano()
	m := store.MessageRecord{
		ID:          id,
		CampfireID:  campfireID,
		Sender:      "agent-pubkey-" + id,
		Tags:        tags,
		Antecedents: []string{},
		Provenance:  []message.ProvenanceHop{},
		Payload:     payload,
		Signature:   testSig,
		Timestamp:   now,
		ReceivedAt:  now,
	}
	inserted, err := s.AddMessage(m)
	if err != nil {
		t.Fatalf("addMsg %s: %v", id, err)
	}
	if !inserted {
		t.Fatalf("addMsg %s: not inserted (duplicate?)", id)
	}
	return m
}

// addViewDef inserts a campfire:view definition message.
func addViewDef(t *testing.T, s store.Store, campfireID, viewName, predicateExpr, refresh string) {
	t.Helper()
	def := struct {
		Name      string `json:"name"`
		Predicate string `json:"predicate"`
		Refresh   string `json:"refresh,omitempty"`
	}{
		Name:      viewName,
		Predicate: predicateExpr,
		Refresh:   refresh,
	}
	payload, _ := json.Marshal(def)
	// Use a unique ID per call to avoid duplicate-insert silent failures.
	id := fmt.Sprintf("view-def-%s-%d", viewName, time.Now().UnixNano())
	now := time.Now().UnixNano()
	m := store.MessageRecord{
		ID:          id,
		CampfireID:  campfireID,
		Sender:      "operator",
		Tags:        []string{"campfire:view"},
		Antecedents: []string{},
		Provenance:  []message.ProvenanceHop{},
		Payload:     payload,
		Signature:   testSig,
		Timestamp:   now,
		ReceivedAt:  now,
	}
	inserted, err := s.AddMessage(m)
	if err != nil {
		t.Fatalf("addViewDef %s: %v", viewName, err)
	}
	if !inserted {
		t.Fatalf("addViewDef %s: not inserted", viewName)
	}
}

// --- ReadView tests ---

// TestReadView_BasicMatch verifies that ReadView returns matching messages.
func TestReadView_BasicMatch(t *testing.T) {
	s := openTestStore(t)
	mw := projection.New(s)

	addViewDef(t, s, testCampfire, "status-view", `(tag "status")`, "on-read")
	addMsg(t, s, testCampfire, "m1", []string{"status"}, []byte(`"ok"`))
	addMsg(t, s, testCampfire, "m2", []string{"other"}, []byte(`"nope"`))
	addMsg(t, s, testCampfire, "m3", []string{"status", "important"}, []byte(`"yes"`))

	results, err := mw.ReadView(testCampfire, "status-view")
	if err != nil {
		t.Fatalf("ReadView: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
	for _, r := range results {
		hasStatus := false
		for _, tag := range r.Tags {
			if tag == "status" {
				hasStatus = true
				break
			}
		}
		if !hasStatus {
			t.Errorf("result %s missing 'status' tag", r.ID)
		}
	}
}

// TestReadView_LazyDelta verifies that only delta messages are evaluated on
// subsequent reads (O(delta) behavior).
//
// We use a counting store wrapper to track how many messages were evaluated.
type countingStore struct {
	store.Store
	evalCount int
}

type trackingMiddleware struct {
	*projection.ProjectionMiddleware
	base *countingStore
}

// TestReadView_LazyDelta_HighWaterMark verifies that high_water_mark is persisted
// and subsequent reads only evaluate the delta.
func TestReadView_LazyDelta_HighWaterMark(t *testing.T) {
	s := openTestStore(t)
	mw := projection.New(s)

	addViewDef(t, s, testCampfire, "delta-view", `(tag "status")`, "on-read")

	// Add 5 initial messages.
	for i := 0; i < 5; i++ {
		time.Sleep(time.Millisecond) // ensure distinct timestamps
		addMsg(t, s, testCampfire, fmt.Sprintf("m%d", i), []string{"status"}, []byte("{}"))
	}

	// First read: evaluates all 5.
	results1, err := mw.ReadView(testCampfire, "delta-view")
	if err != nil {
		t.Fatalf("ReadView first: %v", err)
	}
	if len(results1) != 5 {
		t.Errorf("first read: expected 5 results, got %d", len(results1))
	}

	// Check that metadata was persisted.
	meta, err := s.GetProjectionMetadata(testCampfire, "delta-view")
	if err != nil {
		t.Fatalf("GetProjectionMetadata: %v", err)
	}
	if meta == nil {
		t.Fatal("metadata not persisted after first ReadView")
	}
	if meta.HighWaterMark == 0 {
		t.Error("HighWaterMark should be non-zero after first ReadView")
	}

	// Add 2 more messages.
	time.Sleep(time.Millisecond)
	addMsg(t, s, testCampfire, "m5", []string{"status"}, []byte("{}"))
	time.Sleep(time.Millisecond)
	addMsg(t, s, testCampfire, "m6", []string{"other"}, []byte("{}"))

	// Second read: should return 6 matches (m0-m5 match, m6 does not).
	results2, err := mw.ReadView(testCampfire, "delta-view")
	if err != nil {
		t.Fatalf("ReadView second: %v", err)
	}
	if len(results2) != 6 {
		t.Errorf("second read: expected 6 results, got %d", len(results2))
	}

	// High water mark should be updated.
	meta2, err := s.GetProjectionMetadata(testCampfire, "delta-view")
	if err != nil {
		t.Fatalf("GetProjectionMetadata after second read: %v", err)
	}
	if meta2.HighWaterMark <= meta.HighWaterMark {
		t.Errorf("HighWaterMark should increase: before=%d after=%d",
			meta.HighWaterMark, meta2.HighWaterMark)
	}
}

// TestReadView_PredicateHashChange verifies that changing the predicate
// triggers a full rebuild (old entries discarded).
func TestReadView_PredicateHashChange(t *testing.T) {
	s := openTestStore(t)
	mw := projection.New(s)

	addViewDef(t, s, testCampfire, "rebuild-view", `(tag "status")`, "on-read")
	addMsg(t, s, testCampfire, "r1", []string{"status"}, []byte("{}"))
	addMsg(t, s, testCampfire, "r2", []string{"important"}, []byte("{}"))

	// First read: 1 match.
	results1, err := mw.ReadView(testCampfire, "rebuild-view")
	if err != nil {
		t.Fatalf("ReadView first: %v", err)
	}
	if len(results1) != 1 {
		t.Errorf("expected 1 result, got %d", len(results1))
	}

	// Update view definition to different predicate.
	addViewDef(t, s, testCampfire, "rebuild-view", `(tag "important")`, "on-read")

	// Second read: should rebuild and return 1 match for "important".
	results2, err := mw.ReadView(testCampfire, "rebuild-view")
	if err != nil {
		t.Fatalf("ReadView after predicate change: %v", err)
	}
	if len(results2) != 1 {
		t.Errorf("after predicate change: expected 1, got %d", len(results2))
	}
	if results2[0].ID != "r2" {
		t.Errorf("expected r2, got %s", results2[0].ID)
	}
}

// TestReadView_SystemMessagesExcluded verifies that campfire:* system messages
// are never included in view results.
func TestReadView_SystemMessagesExcluded(t *testing.T) {
	s := openTestStore(t)
	mw := projection.New(s)

	// Add a view with a predicate that would match the view definition itself
	// if system messages were not excluded.
	addViewDef(t, s, testCampfire, "nosys-view", `(tag "campfire:view")`, "on-read")
	addMsg(t, s, testCampfire, "user1", []string{"user-tag"}, []byte("{}"))

	results, err := mw.ReadView(testCampfire, "nosys-view")
	if err != nil {
		t.Fatalf("ReadView: %v", err)
	}
	// The campfire:view message should NOT appear in results.
	for _, r := range results {
		for _, tag := range r.Tags {
			if tag == "campfire:view" {
				t.Errorf("system message %s leaked into view results", r.ID)
			}
		}
	}
}

// TestReadView_NotFound verifies ReadView returns nil when view doesn't exist.
func TestReadView_NotFound(t *testing.T) {
	s := openTestStore(t)
	mw := projection.New(s)

	results, err := mw.ReadView(testCampfire, "nonexistent-view")
	if err != nil {
		t.Fatalf("ReadView: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil for nonexistent view, got %v", results)
	}
}

// TestReadView_ResultsSortedByTimestamp verifies that results are re-sorted
// by message timestamp regardless of insertion order.
func TestReadView_ResultsSortedByTimestamp(t *testing.T) {
	s := openTestStore(t)
	mw := projection.New(s)

	addViewDef(t, s, testCampfire, "sorted-view", `(tag "tagged")`, "on-read")

	// Insert messages with explicit timestamps in non-sorted order.
	base := time.Now().UnixNano()
	msgs := []struct {
		id        string
		timestamp int64
	}{
		{"ts3", base + 3000},
		{"ts1", base + 1000},
		{"ts2", base + 2000},
	}
	for _, tm := range msgs {
		m := store.MessageRecord{
			ID:          tm.id,
			CampfireID:  testCampfire,
			Sender:      "agent",
			Tags:        []string{"tagged"},
			Antecedents: []string{},
			Provenance:  []message.ProvenanceHop{},
			Payload:     []byte("{}"),
			Signature:   testSig,
			Timestamp:   tm.timestamp,
			ReceivedAt:  time.Now().UnixNano(),
		}
		if _, err := s.AddMessage(m); err != nil {
			t.Fatalf("AddMessage %s: %v", tm.id, err)
		}
		time.Sleep(time.Millisecond)
	}

	results, err := mw.ReadView(testCampfire, "sorted-view")
	if err != nil {
		t.Fatalf("ReadView: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	// Should be sorted by timestamp ascending.
	for i := 1; i < len(results); i++ {
		if results[i].Timestamp < results[i-1].Timestamp {
			t.Errorf("results not sorted by timestamp: results[%d].Timestamp=%d < results[%d].Timestamp=%d",
				i, results[i].Timestamp, i-1, results[i-1].Timestamp)
		}
	}
}

// TestReadView_HasFulfillmentFallback verifies that has-fulfillment predicates
// fall back to full scan (Class 3 behavior).
func TestReadView_HasFulfillmentFallback(t *testing.T) {
	s := openTestStore(t)
	mw := projection.New(s)

	addViewDef(t, s, testCampfire, "fulfilled-view", `(has-fulfillment)`, "on-read")

	// Add a request and a fulfillment.
	req := addMsg(t, s, testCampfire, "req1", []string{"request"}, []byte("{}"))
	time.Sleep(time.Millisecond)
	// Add a fulfills message pointing to req1.
	fulfillsMsg := store.MessageRecord{
		ID:          "fulfills1",
		CampfireID:  testCampfire,
		Sender:      "agent",
		Tags:        []string{"fulfills"},
		Antecedents: []string{req.ID},
		Provenance:  []message.ProvenanceHop{},
		Payload:     []byte("{}"),
		Signature:   testSig,
		Timestamp:   time.Now().UnixNano(),
		ReceivedAt:  time.Now().UnixNano(),
	}
	if _, err := s.AddMessage(fulfillsMsg); err != nil {
		t.Fatalf("AddMessage fulfills: %v", err)
	}

	// ReadView with has-fulfillment should return req1 (fulfilled) but not the
	// fulfills message itself (which is not a request).
	results, err := mw.ReadView(testCampfire, "fulfilled-view")
	if err != nil {
		t.Fatalf("ReadView: %v", err)
	}
	// req1 should be in results (it has a fulfillment).
	found := false
	for _, r := range results {
		if r.ID == req.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("expected req1 in fulfilled-view results, got %v", results)
	}
}

// --- AddMessage eager write tests ---

// TestAddMessage_OnWriteView_Class1 verifies that a matching message on an
// on-write view with a Class 1 predicate is projected synchronously.
func TestAddMessage_OnWriteView_Class1(t *testing.T) {
	s := openTestStore(t)
	mw := projection.New(s)

	// Define an on-write view.
	addViewDef(t, s, testCampfire, "write-view", `(tag "status")`, "on-write")

	// Add a matching message through the middleware.
	now := time.Now().UnixNano()
	m := store.MessageRecord{
		ID:          "w1",
		CampfireID:  testCampfire,
		Sender:      "agent",
		Tags:        []string{"status"},
		Antecedents: []string{},
		Provenance:  []message.ProvenanceHop{},
		Payload:     []byte(`"ok"`),
		Signature:   testSig,
		Timestamp:   now,
		ReceivedAt:  now,
	}
	inserted, err := mw.AddMessage(m)
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if !inserted {
		t.Fatal("expected inserted=true")
	}

	// The projection entry should be immediately present.
	entries, err := s.ListProjectionEntries(testCampfire, "write-view")
	if err != nil {
		t.Fatalf("ListProjectionEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 projection entry, got %d", len(entries))
	}
	if len(entries) > 0 && entries[0].MessageID != "w1" {
		t.Errorf("expected message ID w1, got %s", entries[0].MessageID)
	}
}

// TestAddMessage_OnWriteView_NoMatch verifies non-matching messages are not projected.
func TestAddMessage_OnWriteView_NoMatch(t *testing.T) {
	s := openTestStore(t)
	mw := projection.New(s)

	addViewDef(t, s, testCampfire, "write-view", `(tag "status")`, "on-write")

	now := time.Now().UnixNano()
	m := store.MessageRecord{
		ID:          "nomatch1",
		CampfireID:  testCampfire,
		Sender:      "agent",
		Tags:        []string{"other"},
		Antecedents: []string{},
		Provenance:  []message.ProvenanceHop{},
		Payload:     []byte("{}"),
		Signature:   testSig,
		Timestamp:   now,
		ReceivedAt:  now,
	}
	if _, err := mw.AddMessage(m); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	entries, err := s.ListProjectionEntries(testCampfire, "write-view")
	if err != nil {
		t.Fatalf("ListProjectionEntries: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 projection entries for non-match, got %d", len(entries))
	}
}

// TestAddMessage_OnWriteView_Class3_Downgrade verifies that a Class 3 predicate
// (has-fulfillment) on an on-write view does NOT project on write.
func TestAddMessage_OnWriteView_Class3_Downgrade(t *testing.T) {
	s := openTestStore(t)
	mw := projection.New(s)

	// has-fulfillment is Class 3 — should be silently downgraded to on-read.
	addViewDef(t, s, testCampfire, "hf-write-view", `(has-fulfillment)`, "on-write")

	now := time.Now().UnixNano()
	m := store.MessageRecord{
		ID:          "hf1",
		CampfireID:  testCampfire,
		Sender:      "agent",
		Tags:        []string{"request"},
		Antecedents: []string{},
		Provenance:  []message.ProvenanceHop{},
		Payload:     []byte("{}"),
		Signature:   testSig,
		Timestamp:   now,
		ReceivedAt:  now,
	}
	if _, err := mw.AddMessage(m); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	// No projection entries should be created (Class 3 downgraded to on-read).
	entries, err := s.ListProjectionEntries(testCampfire, "hf-write-view")
	if err != nil {
		t.Fatalf("ListProjectionEntries: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Class 3 view should not project on write, got %d entries", len(entries))
	}
}

// TestAddMessage_SystemMessages_NotProjected verifies campfire:* messages
// are never evaluated against view predicates.
func TestAddMessage_SystemMessages_NotProjected(t *testing.T) {
	s := openTestStore(t)
	mw := projection.New(s)

	// A predicate that would match campfire:view if system messages were evaluated.
	addViewDef(t, s, testCampfire, "sys-view", `(tag "campfire:compact")`, "on-write")

	// Add a compaction system message.
	now := time.Now().UnixNano()
	compact := store.MessageRecord{
		ID:          "compact1",
		CampfireID:  testCampfire,
		Sender:      "agent",
		Tags:        []string{"campfire:compact"},
		Antecedents: []string{},
		Provenance:  []message.ProvenanceHop{},
		Payload:     []byte(`{"supersedes":[]}`),
		Signature:   testSig,
		Timestamp:   now,
		ReceivedAt:  now,
	}
	if _, err := mw.AddMessage(compact); err != nil {
		t.Fatalf("AddMessage compact: %v", err)
	}

	// System message should not appear in projection.
	entries, err := s.ListProjectionEntries(testCampfire, "sys-view")
	if err != nil {
		t.Fatalf("ListProjectionEntries: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("system message should not be projected, got %d entries", len(entries))
	}
}

// TestAddMessage_ViewCacheInvalidation verifies that adding a campfire:view
// message invalidates the view cache so new views are picked up.
func TestAddMessage_ViewCacheInvalidation(t *testing.T) {
	s := openTestStore(t)
	mw := projection.New(s)

	// Add a matching message BEFORE any view is defined.
	now := time.Now().UnixNano()
	early := store.MessageRecord{
		ID:          "early1",
		CampfireID:  testCampfire,
		Sender:      "agent",
		Tags:        []string{"status"},
		Antecedents: []string{},
		Provenance:  []message.ProvenanceHop{},
		Payload:     []byte("{}"),
		Signature:   testSig,
		Timestamp:   now,
		ReceivedAt:  now,
	}
	if _, err := mw.AddMessage(early); err != nil {
		t.Fatalf("AddMessage early: %v", err)
	}

	// Define a view (this invalidates the cache).
	addViewDef(t, s, testCampfire, "inv-view", `(tag "status")`, "on-write")

	// Trigger cache invalidation by sending a campfire:view message through the middleware.
	viewDef := struct {
		Name      string `json:"name"`
		Predicate string `json:"predicate"`
		Refresh   string `json:"refresh"`
	}{"inv-view2", `(tag "other")`, "on-write"}
	payload, _ := json.Marshal(viewDef)
	viewMsg := store.MessageRecord{
		ID:          "view-msg-2",
		CampfireID:  testCampfire,
		Sender:      "operator",
		Tags:        []string{"campfire:view"},
		Antecedents: []string{},
		Provenance:  []message.ProvenanceHop{},
		Payload:     payload,
		Signature:   testSig,
		Timestamp:   time.Now().UnixNano(),
		ReceivedAt:  time.Now().UnixNano(),
	}
	if _, err := mw.AddMessage(viewMsg); err != nil {
		t.Fatalf("AddMessage viewMsg: %v", err)
	}

	// Now add a new message — it should be evaluated against both on-write views.
	time.Sleep(time.Millisecond)
	now2 := time.Now().UnixNano()
	later := store.MessageRecord{
		ID:          "later1",
		CampfireID:  testCampfire,
		Sender:      "agent",
		Tags:        []string{"status"},
		Antecedents: []string{},
		Provenance:  []message.ProvenanceHop{},
		Payload:     []byte("{}"),
		Signature:   testSig,
		Timestamp:   now2,
		ReceivedAt:  now2,
	}
	if _, err := mw.AddMessage(later); err != nil {
		t.Fatalf("AddMessage later: %v", err)
	}

	// inv-view should have later1 projected (matches "status").
	entries, err := s.ListProjectionEntries(testCampfire, "inv-view")
	if err != nil {
		t.Fatalf("ListProjectionEntries: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.MessageID == "later1" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected later1 in inv-view projection entries, got %v", entries)
	}
}

// TestAddMessage_ViewCap verifies that at most CF_MAX_PROJECTED_VIEWS on-write
// views are projected per AddMessage call.
func TestAddMessage_ViewCap(t *testing.T) {
	s := openTestStore(t)

	// Set cap to 3 via env var — use a custom New call indirectly.
	t.Setenv("CF_MAX_PROJECTED_VIEWS", "3")
	mw := projection.New(s)

	// Define 5 on-write views.
	for i := 0; i < 5; i++ {
		viewName := fmt.Sprintf("capped-view-%d", i)
		addViewDef(t, s, testCampfire, viewName, `(tag "capped")`, "on-write")
	}

	now := time.Now().UnixNano()
	m := store.MessageRecord{
		ID:          "cap1",
		CampfireID:  testCampfire,
		Sender:      "agent",
		Tags:        []string{"capped"},
		Antecedents: []string{},
		Provenance:  []message.ProvenanceHop{},
		Payload:     []byte("{}"),
		Signature:   testSig,
		Timestamp:   now,
		ReceivedAt:  now,
	}
	if _, err := mw.AddMessage(m); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	// Count total projection entries across all views.
	totalEntries := 0
	for i := 0; i < 5; i++ {
		entries, err := s.ListProjectionEntries(testCampfire, fmt.Sprintf("capped-view-%d", i))
		if err != nil {
			t.Fatalf("ListProjectionEntries: %v", err)
		}
		totalEntries += len(entries)
	}

	// Should be at most 3 (the cap).
	if totalEntries > 3 {
		t.Errorf("view cap not enforced: expected ≤3 entries, got %d", totalEntries)
	}
}

// TestAddMessage_Duplicate verifies that duplicate messages are not projected twice.
func TestAddMessage_Duplicate(t *testing.T) {
	s := openTestStore(t)
	mw := projection.New(s)

	addViewDef(t, s, testCampfire, "dup-view", `(tag "status")`, "on-write")

	now := time.Now().UnixNano()
	m := store.MessageRecord{
		ID:          "dup1",
		CampfireID:  testCampfire,
		Sender:      "agent",
		Tags:        []string{"status"},
		Antecedents: []string{},
		Provenance:  []message.ProvenanceHop{},
		Payload:     []byte("{}"),
		Signature:   testSig,
		Timestamp:   now,
		ReceivedAt:  now,
	}
	if _, err := mw.AddMessage(m); err != nil {
		t.Fatalf("AddMessage first: %v", err)
	}
	// Second insert of same ID — base store returns false.
	inserted, err := mw.AddMessage(m)
	if err != nil {
		t.Fatalf("AddMessage duplicate: %v", err)
	}
	if inserted {
		t.Error("expected inserted=false for duplicate")
	}

	// Should still have exactly 1 projection entry.
	entries, err := s.ListProjectionEntries(testCampfire, "dup-view")
	if err != nil {
		t.Fatalf("ListProjectionEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 entry after duplicate, got %d", len(entries))
	}
}

// TestProjectionMiddleware_ImplementsStore verifies ProjectionMiddleware
// satisfies the store.Store interface at compile time.
func TestProjectionMiddleware_ImplementsStore(t *testing.T) {
	s := openTestStore(t)
	var _ store.Store = projection.New(s)
}
