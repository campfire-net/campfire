package cmd

// Tests for declared-lifecycle gc semantics and the cf lifecycle / cf create
// --lifecycle surfaces (campfireagent-246). The incident regression tests
// (persistent survives, undeclared protected by default) live in
// gc_durability_test.go.

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/store"
)

// runGCExpectError runs cf gc expecting no error, with the include-undeclared
// flag explicitly controlled and reset afterwards (cobra flag values persist
// across Execute calls in one process).
func runGCWithUndeclared(t *testing.T, includeUndeclared bool, args ...string) string {
	t.Helper()
	t.Cleanup(func() {
		gcCmd.Flags().Set("include-undeclared", "false") //nolint:errcheck
	})
	if includeUndeclared {
		args = append(args, "--include-undeclared")
	}
	return runGC(t, args...)
}

func assertCampfirePurged(t *testing.T, s store.Store, campfireID, transportDir, label string) {
	t.Helper()
	m, err := s.GetMembership(campfireID)
	if err != nil {
		t.Fatalf("GetMembership(%s): %v", label, err)
	}
	if m != nil {
		t.Fatalf("%s: membership still present — campfire was NOT purged", label)
	}
	if transportDir != "" {
		if _, statErr := os.Stat(transportDir); !os.IsNotExist(statErr) {
			t.Fatalf("%s: transport dir still present — campfire was NOT purged", label)
		}
	}
}

// TestGC_EphemeralLifecycle: an ephemeral:<ttl> campfire is eligible per ITS
// declared timeout — idle past the ttl purges, idle within it survives, and
// the campfire-key authority requirement is enforced.
func TestGC_EphemeralLifecycle(t *testing.T) {
	s, _, transportBaseDir := setupGCEnv(t)
	now := time.Now()
	old := now.Add(-48 * time.Hour).UnixNano()

	// Elapsed: idle 48h with a 24h ephemeral ttl (campfire-key-signed) → purged.
	elapsedID, elapsedDir := makeIdleOnDiskCampfire(t, s, transportBaseDir, old)
	addLifecycleMessage(t, s, elapsedID, "status", "deadbeef", old)
	addLifecycleMessage(t, s, elapsedID, "durability:lifecycle:ephemeral:24h", elapsedID, old+1)

	// Not elapsed: idle 48h with a 720h (30d) ephemeral ttl → survives.
	youngID, youngDir := makeIdleOnDiskCampfire(t, s, transportBaseDir, old)
	addLifecycleMessage(t, s, youngID, "status", "deadbeef", old)
	addLifecycleMessage(t, s, youngID, "durability:lifecycle:ephemeral:720h", youngID, old+1)

	// Unauthorized: ephemeral declared by a NON-campfire-key sender must be
	// ignored — destructive lifecycles require campfire-key signing, so this
	// campfire is treated as undeclared (permanent-by-default) and survives.
	rogueID, rogueDir := makeIdleOnDiskCampfire(t, s, transportBaseDir, old)
	addLifecycleMessage(t, s, rogueID, "status", "deadbeef", old)
	addLifecycleMessage(t, s, rogueID, "durability:lifecycle:ephemeral:1s", "deadbeef", old+1)

	runGC(t, "--yes", "--older-than", "1s")

	assertCampfirePurged(t, s, elapsedID, elapsedDir, "ephemeral-elapsed campfire")
	assertCampfireSurvived(t, s, youngID, youngDir, "ephemeral-within-ttl campfire")
	assertCampfireSurvived(t, s, rogueID, rogueDir, "rogue-ephemeral campfire")
}

// TestGC_BoundedLifecycle: a bounded:<date> campfire is eligible once the
// declared date passes — regardless of idleness — and protected before it.
func TestGC_BoundedLifecycle(t *testing.T) {
	s, _, transportBaseDir := setupGCEnv(t)
	now := time.Now()
	old := now.Add(-48 * time.Hour).UnixNano()

	pastDate := now.Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	futureDate := now.Add(24 * time.Hour).UTC().Format(time.RFC3339)

	expiredID, expiredDir := makeIdleOnDiskCampfire(t, s, transportBaseDir, old)
	addLifecycleMessage(t, s, expiredID, "status", "deadbeef", old)
	addLifecycleMessage(t, s, expiredID, "durability:lifecycle:bounded:"+pastDate, expiredID, old+1)

	liveID, liveDir := makeIdleOnDiskCampfire(t, s, transportBaseDir, old)
	addLifecycleMessage(t, s, liveID, "status", "deadbeef", old)
	addLifecycleMessage(t, s, liveID, "durability:lifecycle:bounded:"+futureDate, liveID, old+1)

	runGC(t, "--yes", "--older-than", "1s")

	assertCampfirePurged(t, s, expiredID, expiredDir, "bounded-elapsed campfire")
	assertCampfireSurvived(t, s, liveID, liveDir, "bounded-future campfire")
}

// TestGC_IncludeUndeclaredOptIn: the explicit flag restores the idle purge for
// undeclared campfires, while empty undeclared campfires purge by default.
func TestGC_IncludeUndeclaredOptIn(t *testing.T) {
	s, _, transportBaseDir := setupGCEnv(t)
	old := time.Now().Add(-48 * time.Hour).UnixNano()

	idleID, idleDir := makeIdleOnDiskCampfire(t, s, transportBaseDir, old)
	addLifecycleMessage(t, s, idleID, "status", "deadbeef", old)

	emptyID, emptyDir := makeIdleOnDiskCampfire(t, s, transportBaseDir, old)
	// no messages at all — the abandoned-swarm-dir shape

	// Default: empty purges, idle undeclared survives.
	runGC(t, "--yes", "--older-than", "1s")
	assertCampfirePurged(t, s, emptyID, emptyDir, "empty undeclared campfire (default run)")
	assertCampfireSurvived(t, s, idleID, idleDir, "idle undeclared campfire (default run)")

	// Opt-in: idle undeclared purges too.
	runGCWithUndeclared(t, true, "--yes", "--older-than", "1s")
	assertCampfirePurged(t, s, idleID, idleDir, "idle undeclared campfire (--include-undeclared)")
}

// TestLifecycleCommand_DeclareAndInspect drives the real `cf lifecycle` CLI:
// declare persistent on an idle campfire, inspect it, and verify gc protection
// end-to-end — the exact remediation dontguess will run on its exchange.
func TestLifecycleCommand_DeclareAndInspect(t *testing.T) {
	s, _, transportBaseDir := setupGCEnv(t)
	old := time.Now().Add(-48 * time.Hour).UnixNano()

	campfireID, transportDir := makeIdleOnDiskCampfire(t, s, transportBaseDir, old)
	addLifecycleMessage(t, s, campfireID, "status", "deadbeef", old)

	// Declare via the real command.
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs([]string{"lifecycle", campfireID, "persistent"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("cf lifecycle declare: %v\n%s", err, buf.String())
	}

	// Inspect via the real command (JSON).
	buf.Reset()
	rootCmd.SetArgs([]string{"lifecycle", campfireID, "--json"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("cf lifecycle inspect: %v\n%s", err, buf.String())
	}
	// The command writes JSON to os.Stdout via json.NewEncoder(os.Stdout);
	// resolve the effective lifecycle through the store instead.
	lc, err := resolveCampfireLifecycle(s, campfireID)
	if err != nil {
		t.Fatalf("resolveCampfireLifecycle: %v", err)
	}
	if !lc.Declared || string(lc.Type) != "persistent" {
		t.Fatalf("lifecycle after declare = %+v, want declared persistent", lc)
	}

	// The declaration must be on the TRANSPORT too (so other members sync it),
	// not just in the local store.
	declOnDisk := false
	trMsgs := listTransportMessages(t, transportDir)
	for _, tags := range trMsgs {
		for _, tag := range tags {
			if tag == "durability:lifecycle:persistent" {
				declOnDisk = true
			}
		}
	}
	if !declOnDisk {
		t.Fatal("lifecycle declaration not written to the transport — other members would never see it")
	}

	// And gc must now be unable to touch it. The declaration message is
	// recent, but force idleness wouldn't matter — persistent is never
	// eligible. Use a long-idle ledger plus the fresh declaration.
	runGC(t, "--yes", "--older-than", "1s")
	assertCampfireSurvived(t, s, campfireID, transportDir, "cf-lifecycle-declared campfire")
}

// TestCreateWithLifecycleFlag creates a campfire through the real
// `cf create --lifecycle persistent` path and verifies the declaration is
// effective immediately.
func TestCreateWithLifecycleFlag(t *testing.T) {
	s, _, _ := setupGCEnv(t)

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs([]string{"create", "--lifecycle", "persistent", "--no-config", "--description", "lifecycle-flag-test"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("cf create --lifecycle: %v\n%s", err, buf.String())
	}

	memberships, err := s.ListMemberships()
	if err != nil {
		t.Fatalf("ListMemberships: %v", err)
	}
	var campfireID string
	for _, m := range memberships {
		if m.Description == "lifecycle-flag-test" {
			campfireID = m.CampfireID
		}
	}
	if campfireID == "" {
		t.Fatalf("created campfire not found in memberships (%d rows)", len(memberships))
	}

	lc, err := resolveCampfireLifecycle(s, campfireID)
	if err != nil {
		t.Fatalf("resolveCampfireLifecycle: %v", err)
	}
	if !lc.Declared || string(lc.Type) != "persistent" {
		t.Fatalf("lifecycle after create --lifecycle = %+v, want declared persistent", lc)
	}

	// Invalid declarations are rejected up front.
	buf.Reset()
	rootCmd.SetArgs([]string{"create", "--lifecycle", "eternal", "--no-config"})
	if err := rootCmd.Execute(); err == nil || !strings.Contains(err.Error(), "invalid --lifecycle") {
		t.Fatalf("cf create --lifecycle eternal: err = %v, want invalid --lifecycle", err)
	}
	// Reset the persisted flag value for later tests in this process.
	createCmd.Flags().Set("lifecycle", "") //nolint:errcheck
	createCmd.Flags().Set("description", "") //nolint:errcheck
}

// listTransportMessages reads every on-disk message in the campfire's
// transport dir and returns their tag sets, via the fs transport layout
// (messages/YYYY-MM/DD/*.cbor).
func listTransportMessages(t *testing.T, transportDir string) [][]string {
	t.Helper()
	var out [][]string
	root := transportDir + "/messages"
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading messages dir: %v", err)
	}
	var walk func(dir string)
	walk = func(dir string) {
		es, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range es {
			p := dir + "/" + e.Name()
			if e.IsDir() {
				walk(p)
				continue
			}
			if !strings.HasSuffix(e.Name(), ".cbor") {
				continue
			}
			data, rerr := os.ReadFile(p)
			if rerr != nil {
				continue
			}
			// Cheap tag extraction: tags are plain strings inside the CBOR;
			// presence-match rather than full decode keeps this helper free of
			// encoding internals.
			if bytes.Contains(data, []byte("durability:lifecycle:persistent")) {
				out = append(out, []string{"durability:lifecycle:persistent"})
			} else {
				out = append(out, nil)
			}
		}
	}
	for _, e := range entries {
		if e.IsDir() {
			walk(root + "/" + e.Name())
		}
	}
	return out
}
