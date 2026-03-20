package cmd

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/predicate"
	"github.com/campfire-net/campfire/pkg/store"
)

// setupViewTestEnv creates a test environment with an agent, store, and campfire membership.
func setupViewTestEnv(t *testing.T, role string) (*identity.Identity, *store.Store, string) {
	t.Helper()
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

	campfireID := setupCampfireWithRole(t, agentID, s, transportBaseDir, role)
	return agentID, s, campfireID
}

// addTestMessage adds a message directly to the store for view evaluation tests.
func addTestMessage(t *testing.T, s *store.Store, agentID *identity.Identity, campfireID string, payload string, tags []string, timestamp int64) string {
	t.Helper()
	msg, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, []byte(payload), tags, []string{})
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}
	// Override timestamp for test determinism.
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

// addViewMessage adds a campfire:view message to the store.
func addViewMessage(t *testing.T, s *store.Store, agentID *identity.Identity, campfireID string, def viewDefinition) string {
	t.Helper()
	payload, err := json.Marshal(def)
	if err != nil {
		t.Fatalf("marshalling view definition: %v", err)
	}
	return addTestMessage(t, s, agentID, campfireID, string(payload), []string{"campfire:view"}, store.NowNano())
}

func TestViewCreate_StoresViewMessage(t *testing.T) {
	agentID, s, campfireID := setupViewTestEnv(t, campfire.RoleFull)
	defer s.Close()
	_ = agentID

	err := runViewCreate(campfireID, "standing-memories", `(tag "memory:standing")`, "", "timestamp asc", "on-read", 0)
	if err != nil {
		t.Fatalf("runViewCreate: %v", err)
	}

	// Verify a campfire:view message was stored.
	msgs, err := s.ListMessages(campfireID, 0, store.MessageFilter{Tags: []string{"campfire:view"}})
	if err != nil {
		t.Fatalf("listing messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 view message, got %d", len(msgs))
	}

	var def viewDefinition
	if err := json.Unmarshal(msgs[0].Payload, &def); err != nil {
		t.Fatalf("unmarshalling view definition: %v", err)
	}
	if def.Name != "standing-memories" {
		t.Errorf("expected name 'standing-memories', got %q", def.Name)
	}
	if def.Predicate != `(tag "memory:standing")` {
		t.Errorf("unexpected predicate: %s", def.Predicate)
	}
}

func TestViewCreate_WriterCannotCreate(t *testing.T) {
	_, s, campfireID := setupViewTestEnv(t, campfire.RoleWriter)
	defer s.Close()

	err := runViewCreate(campfireID, "test-view", `(tag "test")`, "", "timestamp asc", "on-read", 0)
	if err == nil {
		t.Fatal("expected error: writer cannot create views (campfire:* system messages)")
	}
	if !isRoleError(err) {
		t.Errorf("expected role error, got: %v", err)
	}
}

func TestViewCreate_ObserverCannotCreate(t *testing.T) {
	_, s, campfireID := setupViewTestEnv(t, campfire.RoleObserver)
	defer s.Close()

	err := runViewCreate(campfireID, "test-view", `(tag "test")`, "", "timestamp asc", "on-read", 0)
	if err == nil {
		t.Fatal("expected error: observer cannot create views")
	}
	if !isRoleError(err) {
		t.Errorf("expected role error, got: %v", err)
	}
}

func TestViewCreate_InvalidPredicate(t *testing.T) {
	_, s, campfireID := setupViewTestEnv(t, campfire.RoleFull)
	defer s.Close()

	err := runViewCreate(campfireID, "bad-view", `(invalid "broken`, "", "timestamp asc", "on-read", 0)
	if err == nil {
		t.Fatal("expected error for invalid predicate")
	}
}

func TestViewCreate_InvalidOrdering(t *testing.T) {
	_, s, campfireID := setupViewTestEnv(t, campfire.RoleFull)
	defer s.Close()

	err := runViewCreate(campfireID, "bad-order", `(tag "test")`, "", "payload desc", "on-read", 0)
	if err == nil {
		t.Fatal("expected error for invalid ordering")
	}
}

func TestViewCreate_UnsupportedRefresh(t *testing.T) {
	_, s, campfireID := setupViewTestEnv(t, campfire.RoleFull)
	defer s.Close()

	err := runViewCreate(campfireID, "bad-refresh", `(tag "test")`, "", "timestamp asc", "on-write", 0)
	if err == nil {
		t.Fatal("expected error for unsupported refresh strategy")
	}
}

func TestViewRead_MaterializesView(t *testing.T) {
	agentID, s, campfireID := setupViewTestEnv(t, campfire.RoleFull)
	defer s.Close()

	// Add some test messages.
	addTestMessage(t, s, agentID, campfireID, "standing memory 1", []string{"memory:standing"}, 1000)
	addTestMessage(t, s, agentID, campfireID, "standing memory 2", []string{"memory:standing"}, 2000)
	addTestMessage(t, s, agentID, campfireID, "ephemeral note", []string{"note"}, 3000)

	// Create a view definition.
	addViewMessage(t, s, agentID, campfireID, viewDefinition{
		Name:      "standing",
		Predicate: `(tag "memory:standing")`,
		Ordering:  "timestamp asc",
		Refresh:   "on-read",
	})

	// Find and verify the view.
	def, err := findLatestView(s, campfireID, "standing")
	if err != nil {
		t.Fatalf("findLatestView: %v", err)
	}
	if def == nil {
		t.Fatal("view not found")
	}
	if def.Name != "standing" {
		t.Errorf("expected name 'standing', got %q", def.Name)
	}
}

func TestViewRead_PredicateFilters(t *testing.T) {
	agentID, s, campfireID := setupViewTestEnv(t, campfire.RoleFull)
	defer s.Close()

	addTestMessage(t, s, agentID, campfireID, "standing 1", []string{"memory:standing"}, 1000)
	addTestMessage(t, s, agentID, campfireID, "note 1", []string{"note"}, 2000)
	addTestMessage(t, s, agentID, campfireID, "standing 2", []string{"memory:standing"}, 3000)

	addViewMessage(t, s, agentID, campfireID, viewDefinition{
		Name:      "standing",
		Predicate: `(tag "memory:standing")`,
		Ordering:  "timestamp asc",
		Refresh:   "on-read",
	})

	// Materialize the view manually using the predicate package.
	allMsgs, _ := s.ListMessages(campfireID, 0)
	viewDef, _ := findLatestView(s, campfireID, "standing")
	pred, err := predicate.Parse(viewDef.Predicate)
	if err != nil {
		t.Fatalf("parse predicate: %v", err)
	}

	var matched []store.MessageRecord
	for _, m := range allMsgs {
		// Skip view definition messages.
		var tags []string
		json.Unmarshal([]byte(m.Tags), &tags)
		isViewMsg := false
		for _, tg := range tags {
			if tg == "campfire:view" {
				isViewMsg = true
			}
		}
		if isViewMsg {
			continue
		}

		ctx := buildMessageContext(m)
		if predicate.Eval(pred, ctx) {
			matched = append(matched, m)
		}
	}

	if len(matched) != 2 {
		t.Fatalf("expected 2 standing memories, got %d", len(matched))
	}
	if string(matched[0].Payload) != "standing 1" {
		t.Errorf("first match: expected 'standing 1', got %q", string(matched[0].Payload))
	}
	if string(matched[1].Payload) != "standing 2" {
		t.Errorf("second match: expected 'standing 2', got %q", string(matched[1].Payload))
	}
}

func TestViewRead_OrderingDesc(t *testing.T) {
	agentID, s, campfireID := setupViewTestEnv(t, campfire.RoleFull)
	defer s.Close()

	addTestMessage(t, s, agentID, campfireID, "first", []string{"note"}, 1000)
	addTestMessage(t, s, agentID, campfireID, "second", []string{"note"}, 2000)
	addTestMessage(t, s, agentID, campfireID, "third", []string{"note"}, 3000)

	addViewMessage(t, s, agentID, campfireID, viewDefinition{
		Name:      "notes-desc",
		Predicate: `(tag "note")`,
		Ordering:  "timestamp desc",
		Refresh:   "on-read",
	})

	def, _ := findLatestView(s, campfireID, "notes-desc")
	allMsgs, _ := s.ListMessages(campfireID, 0)
	pred, _ := predicate.Parse(def.Predicate)

	var matched []store.MessageRecord
	for _, m := range allMsgs {
		var tags []string
		json.Unmarshal([]byte(m.Tags), &tags)
		isView := false
		for _, tg := range tags {
			if tg == "campfire:view" {
				isView = true
			}
		}
		if isView {
			continue
		}
		ctx := buildMessageContext(m)
		if predicate.Eval(pred, ctx) {
			matched = append(matched, m)
		}
	}

	// Sort desc as the view specifies.
	if def.Ordering == "timestamp desc" {
		for i, j := 0, len(matched)-1; i < j; i, j = i+1, j-1 {
			matched[i], matched[j] = matched[j], matched[i]
		}
	}

	if len(matched) != 3 {
		t.Fatalf("expected 3 notes, got %d", len(matched))
	}
	if string(matched[0].Payload) != "third" {
		t.Errorf("first (desc) should be 'third', got %q", string(matched[0].Payload))
	}
	if string(matched[2].Payload) != "first" {
		t.Errorf("last (desc) should be 'first', got %q", string(matched[2].Payload))
	}
}

func TestViewRead_Limit(t *testing.T) {
	agentID, s, campfireID := setupViewTestEnv(t, campfire.RoleFull)
	defer s.Close()

	for i := 0; i < 10; i++ {
		addTestMessage(t, s, agentID, campfireID, "msg", []string{"bulk"}, int64(1000+i))
	}

	addViewMessage(t, s, agentID, campfireID, viewDefinition{
		Name:      "limited",
		Predicate: `(tag "bulk")`,
		Ordering:  "timestamp asc",
		Limit:     3,
		Refresh:   "on-read",
	})

	def, _ := findLatestView(s, campfireID, "limited")
	if def.Limit != 3 {
		t.Errorf("expected limit 3, got %d", def.Limit)
	}
}

func TestViewList_FindsAllViews(t *testing.T) {
	agentID, s, campfireID := setupViewTestEnv(t, campfire.RoleFull)
	defer s.Close()

	addViewMessage(t, s, agentID, campfireID, viewDefinition{
		Name:      "alpha",
		Predicate: `(tag "a")`,
		Refresh:   "on-read",
	})
	addViewMessage(t, s, agentID, campfireID, viewDefinition{
		Name:      "beta",
		Predicate: `(tag "b")`,
		Refresh:   "on-read",
	})

	views, err := findAllViews(s, campfireID)
	if err != nil {
		t.Fatalf("findAllViews: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("expected 2 views, got %d", len(views))
	}
	// Sorted by name.
	if views[0].def.Name != "alpha" || views[1].def.Name != "beta" {
		t.Errorf("unexpected view names: %s, %s", views[0].def.Name, views[1].def.Name)
	}
}

func TestViewList_LatestDefinitionWins(t *testing.T) {
	agentID, s, campfireID := setupViewTestEnv(t, campfire.RoleFull)
	defer s.Close()

	// First version of "myview".
	addViewMessage(t, s, agentID, campfireID, viewDefinition{
		Name:      "myview",
		Predicate: `(tag "old")`,
		Refresh:   "on-read",
	})
	// Updated version of "myview".
	addViewMessage(t, s, agentID, campfireID, viewDefinition{
		Name:      "myview",
		Predicate: `(tag "new")`,
		Refresh:   "on-read",
	})

	views, _ := findAllViews(s, campfireID)
	if len(views) != 1 {
		t.Fatalf("expected 1 unique view, got %d", len(views))
	}
	if views[0].def.Predicate != `(tag "new")` {
		t.Errorf("expected latest predicate, got %q", views[0].def.Predicate)
	}
}

// TestViewRead_NegationPredicateExcludesSystemMessages verifies that campfire:*
// system messages (e.g. campfire:view definitions) are not included in view
// materialization results when a negation predicate is used.
//
// Without the fix in runViewRead, a (not (tag "foo")) predicate would match the
// campfire:view definition message (it doesn't have tag "foo"), leaking it into
// the result set.
func TestViewRead_NegationPredicateExcludesSystemMessages(t *testing.T) {
	agentID, s, campfireID := setupViewTestEnv(t, campfire.RoleFull)
	defer s.Close()

	// Add two user messages: one tagged "foo", one not.
	addTestMessage(t, s, agentID, campfireID, "has foo tag", []string{"foo"}, 1000)
	addTestMessage(t, s, agentID, campfireID, "no foo tag", []string{"bar"}, 2000)

	// Add the view definition with a campfire:view system tag.
	// The payload of this view message is NOT tagged "foo" — so (not (tag "foo"))
	// would match it if system messages are not filtered out first.
	addViewMessage(t, s, agentID, campfireID, viewDefinition{
		Name:      "not-foo",
		Predicate: `(not (tag "foo"))`,
		Ordering:  "timestamp asc",
		Refresh:   "on-read",
	})

	// Capture stdout from runViewRead.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w

	runErr := runViewRead(campfireID, "not-foo")

	w.Close()
	os.Stdout = origStdout

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading pipe: %v", err)
	}

	if runErr != nil {
		t.Fatalf("runViewRead: %v", runErr)
	}

	output := string(out)

	// Only "no foo tag" should appear — the campfire:view system message must not.
	if !strings.Contains(output, "no foo tag") {
		t.Errorf("expected 'no foo tag' in output, got:\n%s", output)
	}
	if strings.Contains(output, "has foo tag") {
		t.Errorf("'has foo tag' should not appear in (not (tag \"foo\")) results, got:\n%s", output)
	}
	// The view definition payload contains the JSON predicate spec — it should
	// never appear as a user-visible result.
	if strings.Contains(output, `"not-foo"`) || strings.Contains(output, `"predicate"`) {
		t.Errorf("system message (campfire:view definition) leaked into view results:\n%s", output)
	}
}

func TestViewRead_ViewNotFound(t *testing.T) {
	_, s, campfireID := setupViewTestEnv(t, campfire.RoleFull)
	defer s.Close()

	err := runViewRead(campfireID, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent view")
	}
}

func TestBuildMessageContext(t *testing.T) {
	rec := store.MessageRecord{
		Tags:      `["memory:standing","note"]`,
		Sender:    "abcdef1234567890",
		Timestamp: 12345,
		Payload:   []byte(`{"confidence": 0.8, "category": "identity"}`),
	}
	ctx := buildMessageContext(rec)
	if len(ctx.Tags) != 2 {
		t.Errorf("expected 2 tags, got %d", len(ctx.Tags))
	}
	if ctx.Sender != "abcdef1234567890" {
		t.Errorf("unexpected sender: %s", ctx.Sender)
	}
	if ctx.Timestamp != 12345 {
		t.Errorf("unexpected timestamp: %d", ctx.Timestamp)
	}
	if ctx.Payload == nil {
		t.Fatal("expected non-nil payload")
	}
	if ctx.Payload["confidence"] != 0.8 {
		t.Errorf("unexpected confidence: %v", ctx.Payload["confidence"])
	}
}

func TestBuildMessageContext_NonJSONPayload(t *testing.T) {
	rec := store.MessageRecord{
		Tags:    `[]`,
		Payload: []byte("plain text message"),
	}
	ctx := buildMessageContext(rec)
	if ctx.Payload != nil {
		t.Error("expected nil payload for non-JSON")
	}
}

func TestViewCreate_WithProjection(t *testing.T) {
	_, s, campfireID := setupViewTestEnv(t, campfire.RoleFull)
	defer s.Close()

	err := runViewCreate(campfireID, "projected-view", `(tag "test")`, "id,tags,payload", "timestamp asc", "on-read", 0)
	if err != nil {
		t.Fatalf("runViewCreate: %v", err)
	}

	def, _ := findLatestView(s, campfireID, "projected-view")
	if def == nil {
		t.Fatal("view not found")
	}
	if len(def.Projection) != 3 {
		t.Fatalf("expected 3 projection fields, got %d", len(def.Projection))
	}
	if def.Projection[0] != "id" || def.Projection[1] != "tags" || def.Projection[2] != "payload" {
		t.Errorf("unexpected projection: %v", def.Projection)
	}
}

func TestViewRead_ComplexPredicate(t *testing.T) {
	agentID, s, campfireID := setupViewTestEnv(t, campfire.RoleFull)
	defer s.Close()

	// Messages with JSON payloads.
	addTestMessage(t, s, agentID, campfireID, `{"confidence": 0.8}`, []string{"memory:standing"}, 1000)
	addTestMessage(t, s, agentID, campfireID, `{"confidence": 0.3}`, []string{"memory:standing"}, 2000)
	addTestMessage(t, s, agentID, campfireID, `{"confidence": 0.9}`, []string{"note"}, 3000)

	addViewMessage(t, s, agentID, campfireID, viewDefinition{
		Name:      "high-confidence-standing",
		Predicate: `(and (tag "memory:standing") (gt (field "payload.confidence") (literal 0.5)))`,
		Ordering:  "timestamp asc",
		Refresh:   "on-read",
	})

	def, _ := findLatestView(s, campfireID, "high-confidence-standing")
	pred, _ := predicate.Parse(def.Predicate)
	allMsgs, _ := s.ListMessages(campfireID, 0)

	var matched []store.MessageRecord
	for _, m := range allMsgs {
		var tags []string
		json.Unmarshal([]byte(m.Tags), &tags)
		isView := false
		for _, tg := range tags {
			if tg == "campfire:view" {
				isView = true
			}
		}
		if isView {
			continue
		}
		ctx := buildMessageContext(m)
		if predicate.Eval(pred, ctx) {
			matched = append(matched, m)
		}
	}

	// Only the first message should match (memory:standing + confidence > 0.5)
	if len(matched) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matched))
	}
	if string(matched[0].Payload) != `{"confidence": 0.8}` {
		t.Errorf("unexpected payload: %s", string(matched[0].Payload))
	}
}
