package cmd

// Tests for convention promote transport fan-out (campfire-agent-ykl).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/convention"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// setupPromoteEnv creates a filesystem campfire registry suitable for testing convention promote.
// It returns the campfire ID, agent identity, store, fs transport, and transportBaseDir.
func setupPromoteEnv(t *testing.T) (campfireID string, agentID *identity.Identity, s store.Store, tr *fs.Transport, transportBaseDir string) {
	t.Helper()

	cfHomeDir := t.TempDir()
	transportBaseDir = t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	t.Setenv("CF_TRANSPORT_DIR", transportBaseDir)
	t.Cleanup(func() { cfHome = ""; os.Unsetenv("CF_HOME"); os.Unsetenv("CF_TRANSPORT_DIR") })
	cfHome = ""

	// Generate and save agent identity.
	var err error
	agentID, err = identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	// Open store.
	s, err = store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Generate campfire identity.
	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	campfireID = cfID.PublicKeyHex()

	// Create campfire directory structure.
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
		Threshold:             1,
	}
	stateData, err := cfencoding.Marshal(state)
	if err != nil {
		t.Fatalf("marshalling state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfDir, "campfire.cbor"), stateData, 0644); err != nil {
		t.Fatalf("writing state: %v", err)
	}

	// Register agent as a member in the transport directory.
	tr = fs.New(transportBaseDir)
	if err := tr.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("writing member record: %v", err)
	}

	// Record membership in the local store.
	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: tr.CampfireDir(campfireID),
		JoinProtocol: "open",
		Role:         "member",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	return campfireID, agentID, s, tr, transportBaseDir
}

// minimalDecl returns a valid convention declaration JSON for the given operation name.
func minimalDecl(convention, operation string) []byte {
	d := map[string]any{
		"convention":  convention,
		"version":     "0.1",
		"operation":   operation,
		"description": fmt.Sprintf("Test %s operation", operation),
		"produces_tags": []map[string]any{
			{"tag": fmt.Sprintf("%s:%s", convention, operation), "cardinality": "exactly_one"},
		},
		"args": []map[string]any{
			{"name": "text", "type": "string", "required": true, "max_length": 1000},
		},
		"signing": "member_key",
	}
	b, _ := json.Marshal(d)
	return b
}

// TestConventionPromote_WritesToTransport verifies that promoteSingle writes the declaration
// to the filesystem transport so that other agents can sync and see it — not just local store.
func TestConventionPromote_WritesToTransport(t *testing.T) {
	campfireID, agentID, s, tr, _ := setupPromoteEnv(t)

	payload := minimalDecl("myconv", "create")
	src := declSource{name: "test.json", payload: payload}

	m, err := s.GetMembership(campfireID)
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}

	result := promoteSingle(src, campfireID, agentID, s, m, map[string]*convention.Declaration{})
	if result.Error != "" {
		t.Fatalf("promoteSingle failed: %s", result.Error)
	}
	if result.Skipped {
		t.Fatal("expected promote, got skipped")
	}
	if result.MessageID == "" {
		t.Fatal("expected non-empty message ID")
	}

	// The declaration MUST appear in the filesystem transport, not just the local store.
	msgs, err := tr.ListMessages(campfireID)
	if err != nil {
		t.Fatalf("ListMessages from transport: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("declaration not found in filesystem transport — transport fan-out broken")
	}

	// Verify the message ID matches.
	found := false
	for _, msg := range msgs {
		if msg.ID == result.MessageID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("message ID %s not found in transport messages (got %d messages)", result.MessageID, len(msgs))
	}
}

// TestConventionPromote_TransportTaggedAsConventionOperation verifies the message in the
// transport has the convention:operation tag so consumers can filter it correctly.
func TestConventionPromote_TransportTaggedAsConventionOperation(t *testing.T) {
	campfireID, agentID, s, tr, _ := setupPromoteEnv(t)

	payload := minimalDecl("myconv", "update")
	src := declSource{name: "test.json", payload: payload}

	m, err := s.GetMembership(campfireID)
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}

	result := promoteSingle(src, campfireID, agentID, s, m, map[string]*convention.Declaration{})
	if result.Error != "" {
		t.Fatalf("promoteSingle failed: %s", result.Error)
	}

	msgs, err := tr.ListMessages(campfireID)
	if err != nil {
		t.Fatalf("ListMessages from transport: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("no messages in transport")
	}

	for _, msg := range msgs {
		if msg.ID == result.MessageID {
			for _, tag := range msg.Tags {
				if tag == "convention:operation" {
					return // tag present — test passes
				}
			}
			t.Errorf("message %s missing convention:operation tag; tags: %v", msg.ID, msg.Tags)
			return
		}
	}
	t.Errorf("message %s not found in transport", result.MessageID)
}

// TestConventionPromote_SkipsConflict verifies that promoting the same declaration twice
// (same convention+operation+version) is skipped on the second call.
func TestConventionPromote_SkipsConflict(t *testing.T) {
	campfireID, agentID, s, _, _ := setupPromoteEnv(t)

	payload := minimalDecl("myconv", "delete")
	src := declSource{name: "test.json", payload: payload}

	m, err := s.GetMembership(campfireID)
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}

	// First promote — should succeed.
	r1 := promoteSingle(src, campfireID, agentID, s, m, map[string]*convention.Declaration{})
	if r1.Error != "" {
		t.Fatalf("first promote failed: %s", r1.Error)
	}

	// Build existing map from what we know was promoted.
	decl, _, err := convention.Parse([]string{"convention:operation"}, payload, agentID.PublicKeyHex(), agentID.PublicKeyHex())
	if err != nil {
		t.Fatalf("parsing declaration: %v", err)
	}
	existing := map[string]*convention.Declaration{
		decl.Convention + ":" + decl.Operation + "@" + decl.Version: decl,
	}

	// Second promote — should be skipped.
	r2 := promoteSingle(src, campfireID, agentID, s, m, existing)
	if r2.Error != "" {
		t.Fatalf("second promote returned error: %s", r2.Error)
	}
	if !r2.Skipped {
		t.Fatal("expected second promote to be skipped (same convention+operation+version)")
	}
}
