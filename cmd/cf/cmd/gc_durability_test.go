package cmd

// Regression tests for campfireagent-246.
//
// INCIDENT (2026-06-03): legion's hourly `cf gc --yes --older-than 24h` purged
// the dontguess exchange campfire (ed4b6d62d996…) — the entire token-work
// marketplace: inventory, scrip ledger, residuals — plus ~10 other idle
// filesystem campfires. cf gc's only death test was "empty OR idle past the
// cutoff"; it never consulted the Durability Convention, which states that
// absent durability tags content is PERMANENT, and which defines
// durability:lifecycle:persistent as an explicit continuity declaration.
//
// These tests encode the incident scenario through the real CLI path:
//
//  1. A campfire declared durability:lifecycle:persistent is NEVER a gc
//     candidate, no matter how idle.
//  2. An UNDECLARED campfire with messages is not eligible by default
//     (permanent-by-default); purging idle undeclared campfires requires an
//     explicit opt-in flag.
//
// Pre-fix, cf gc purges both campfires — transport dir removed, store rows
// gone. That is the bug.

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/cf-protocol/store"
)

// setupGCEnv creates a CF_HOME with an identity and an open store, and returns
// (store, cfHome, transportBaseDir).
func setupGCEnv(t *testing.T) (store.Store, string, string) {
	t.Helper()
	cfHomeDir := t.TempDir()
	transportBaseDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	t.Setenv("CF_TRANSPORT_DIR", transportBaseDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating agent identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}
	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, cfHomeDir, transportBaseDir
}

// makeIdleOnDiskCampfire creates a real on-disk filesystem campfire (state,
// member record, transport dirs) via the home_test helper, then rewrites its
// membership with an old JoinedAt so it clears gc's joined-recently guard.
// Returns (campfireID, transportDir).
func makeIdleOnDiskCampfire(t *testing.T, s store.Store, transportBaseDir string, joinedAtNano int64) (string, string) {
	t.Helper()
	creatorID, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}
	campfireID := createTestCampfire(t, creatorID, s, transportBaseDir)
	transportDir := filepath.Join(transportBaseDir, campfireID)
	if err := s.RemoveMembership(campfireID); err != nil {
		t.Fatalf("RemoveMembership: %v", err)
	}
	if err := s.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  transportDir,
		JoinProtocol:  "invite-only",
		Role:          "creator",
		JoinedAt:      joinedAtNano,
		Threshold:     1,
		TransportType: "filesystem",
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}
	return campfireID, transportDir
}

// addLifecycleMessage stores a message carrying the given tag for the
// campfire, with a controlled timestamp and sender.
func addLifecycleMessage(t *testing.T, s store.Store, campfireID, lifecycleTag, senderHex string, tsNano int64) {
	t.Helper()
	added, err := s.AddMessage(store.MessageRecord{
		ID:         campfireID + "-" + lifecycleTag + "-msg",
		CampfireID: campfireID,
		Sender:     senderHex,
		Payload:    []byte(`{"declared_by":"test"}`),
		Tags:       []string{lifecycleTag},
		Timestamp:  tsNano,
		Signature:  []byte("sig"),
		ReceivedAt: store.NowNano(),
	})
	if err != nil {
		t.Fatalf("AddMessage(lifecycle): %v", err)
	}
	if !added {
		t.Fatalf("AddMessage(lifecycle) did not insert")
	}
}

// runGC executes `cf gc <args>` through the real cobra command path and
// returns the combined output. Output writers and value-carrying gc flags are
// reset afterwards — cobra command state persists across Execute calls in one
// process, so a --yes from one test must not leak into the next test's run.
func runGC(t *testing.T, args ...string) string {
	t.Helper()
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	defer func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		gcCmd.Flags().Set("yes", "false")        //nolint:errcheck
		gcCmd.Flags().Set("json", "false")       //nolint:errcheck
		gcCmd.Flags().Set("older-than", "24h")   //nolint:errcheck
	}()
	rootCmd.SetArgs(append([]string{"gc"}, args...))
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("cf gc %v: %v\noutput:\n%s", args, err, buf.String())
	}
	return buf.String()
}

// assertCampfireSurvived verifies both halves of the local copy are intact:
// store rows (membership + messages) and the transport directory.
func assertCampfireSurvived(t *testing.T, s store.Store, campfireID, transportDir, label string) {
	t.Helper()
	m, err := s.GetMembership(campfireID)
	if err != nil {
		t.Fatalf("GetMembership(%s): %v", label, err)
	}
	if m == nil {
		t.Fatalf("%s: membership PURGED — campfire did not survive cf gc (campfireagent-246)", label)
	}
	msgs, err := s.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatalf("ListMessages(%s): %v", label, err)
	}
	if len(msgs) == 0 {
		t.Fatalf("%s: messages PURGED — campfire did not survive cf gc (campfireagent-246)", label)
	}
	if transportDir != "" {
		if _, statErr := os.Stat(transportDir); statErr != nil {
			t.Fatalf("%s: transport dir %s gone — campfire did not survive cf gc (campfireagent-246): %v", label, transportDir, statErr)
		}
	}
}

// TestGC_PersistentLifecycleSurvives is the incident regression: an idle
// filesystem campfire that holds a store of value and is declared
// durability:lifecycle:persistent must NEVER be a gc candidate.
func TestGC_PersistentLifecycleSurvives(t *testing.T) {
	s, _, transportBaseDir := setupGCEnv(t)

	old := time.Now().Add(-48 * time.Hour).UnixNano()

	// The exchange shape: a real on-disk campfire, long idle, full of value.
	campfireID, transportDir := makeIdleOnDiskCampfire(t, s, transportBaseDir, old)

	// The store of value: an idle message ledger.
	addLifecycleMessage(t, s, campfireID, "status", "deadbeef", old)
	// The continuity declaration, also idle (declared weeks before the gc run).
	addLifecycleMessage(t, s, campfireID, "durability:lifecycle:persistent", "deadbeef", old+1)

	runGC(t, "--yes", "--older-than", "1s")

	assertCampfireSurvived(t, s, campfireID, transportDir, "persistent campfire")
}

// TestGC_UndeclaredIdleSurvivesByDefault encodes permanent-by-default: a
// campfire with NO lifecycle declaration and idle messages must not be purged
// by a default cf gc run. (The Durability Convention: "If no durability tags
// are present, the message is treated as permanent and live.")
func TestGC_UndeclaredIdleSurvivesByDefault(t *testing.T) {
	s, _, transportBaseDir := setupGCEnv(t)

	old := time.Now().Add(-48 * time.Hour).UnixNano()

	campfireID, transportDir := makeIdleOnDiskCampfire(t, s, transportBaseDir, old)

	// Idle messages, no lifecycle declaration anywhere.
	addLifecycleMessage(t, s, campfireID, "status", "deadbeef", old)

	runGC(t, "--yes", "--older-than", "1s")

	assertCampfireSurvived(t, s, campfireID, transportDir, "undeclared idle campfire")
}
