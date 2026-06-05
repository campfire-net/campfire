package cmd

// Tests for cf gc (campfireagent-4b9): local garbage collection of dead campfires.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/store"
)

// addGCMembership inserts a membership with a controlled JoinedAt and transport
// type, optionally seeding a single message at a controlled timestamp.
func addGCMembership(t *testing.T, s store.Store, id, transportDir, transportType string, joinedAtNano, msgTSNano int64) {
	t.Helper()
	if err := s.AddMembership(store.Membership{
		CampfireID:    id,
		TransportDir:  transportDir,
		JoinProtocol:  "open",
		Role:          "full",
		JoinedAt:      joinedAtNano,
		Threshold:     1,
		TransportType: transportType,
	}); err != nil {
		t.Fatalf("AddMembership(%s): %v", id, err)
	}
	if msgTSNano > 0 {
		// signature is NOT NULL in the schema; a nil slice would be silently
		// dropped by INSERT OR IGNORE.
		added, err := s.AddMessage(store.MessageRecord{
			ID:         id + "-msg",
			CampfireID: id,
			Sender:     "deadbeef",
			Payload:    []byte("x"),
			Tags:       []string{"status"},
			Timestamp:  msgTSNano,
			Signature:  []byte("sig"),
			ReceivedAt: store.NowNano(),
		})
		if err != nil {
			t.Fatalf("AddMessage(%s): %v", id, err)
		}
		if !added {
			t.Fatalf("AddMessage(%s) did not insert (constraint?)", id)
		}
	}
}

func TestGCSelectCandidates(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	now := time.Now().UnixNano()
	cutoff := now - (24 * time.Hour).Nanoseconds()
	old := now - (48 * time.Hour).Nanoseconds()   // before cutoff
	recent := now - (1 * time.Hour).Nanoseconds() // after cutoff

	// home: old + empty, but protected because it is the home campfire.
	addGCMembership(t, s, "home0", "/tmp/home0", "filesystem", old, 0)
	// recentEmpty: empty but joined recently — protected by JoinedAt.
	addGCMembership(t, s, "recentempty", "/tmp/recentempty", "filesystem", recent, 0)
	// oldEmpty: joined long ago, no messages — CANDIDATE (empty).
	addGCMembership(t, s, "oldempty", "/tmp/oldempty", "filesystem", old, 0)
	// oldIdle: joined long ago, newest message before cutoff — CANDIDATE (idle).
	addGCMembership(t, s, "oldidle", "/tmp/oldidle", "filesystem", old, old)
	// oldActive: joined long ago but a recent message — NOT a candidate.
	addGCMembership(t, s, "oldactive", "/tmp/oldactive", "filesystem", old, recent)
	// p2pOld: old + empty but p2p-http transport — skipped (not fs cruft).
	addGCMembership(t, s, "p2pold", "", "p2p-http", old, 0)

	memberships, err := s.ListMemberships()
	if err != nil {
		t.Fatalf("ListMemberships: %v", err)
	}

	// Default run: permanent-by-default (campfireagent-246) — oldidle has
	// messages and no lifecycle declaration, so only the EMPTY campfire is a
	// candidate.
	candidates, err := gcSelectCandidates(memberships, "home0", cutoff, now, s, false)
	if err != nil {
		t.Fatalf("gcSelectCandidates: %v", err)
	}

	got := map[string]string{}
	for _, c := range candidates {
		got[c.CampfireID] = c.Reason
	}
	want := map[string]string{"oldempty": "empty"}
	if len(got) != len(want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	for id, reason := range want {
		if got[id] != reason {
			t.Errorf("candidate %s reason = %q, want %q (all: %v)", id, got[id], reason, got)
		}
	}
	// Explicit protections — oldidle is now protected by permanent-by-default.
	for _, protected := range []string{"home0", "recentempty", "oldactive", "p2pold", "oldidle"} {
		if _, bad := got[protected]; bad {
			t.Errorf("%s should NOT be a candidate", protected)
		}
	}

	// Opt-in run: --include-undeclared restores the idle purge for undeclared
	// campfires (the pre-convention behavior, now explicit).
	candidates, err = gcSelectCandidates(memberships, "home0", cutoff, now, s, true)
	if err != nil {
		t.Fatalf("gcSelectCandidates(includeUndeclared): %v", err)
	}
	got = map[string]string{}
	for _, c := range candidates {
		got[c.CampfireID] = c.Reason
	}
	want = map[string]string{"oldempty": "empty", "oldidle": "idle-undeclared"}
	if len(got) != len(want) {
		t.Fatalf("includeUndeclared candidates = %v, want %v", got, want)
	}
	for id, reason := range want {
		if got[id] != reason {
			t.Errorf("includeUndeclared candidate %s reason = %q, want %q (all: %v)", id, got[id], reason, got)
		}
	}
	for _, protected := range []string{"home0", "recentempty", "oldactive", "p2pold"} {
		if _, bad := got[protected]; bad {
			t.Errorf("includeUndeclared: %s should NOT be a candidate", protected)
		}
	}
}

func TestGCPurge_RemovesStoreRowsAndTransportDir(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	// Create a real transport directory with a file so RemoveAll has work to do.
	transportDir := filepath.Join(t.TempDir(), "deadcampfire")
	if err := os.MkdirAll(filepath.Join(transportDir, "messages"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(transportDir, "campfire.cbor"), []byte("x"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	const id = "deadcampfire000"
	addGCMembership(t, s, id, transportDir, "filesystem", 1, 100)
	if err := s.SetFSSyncCursor(id, "cursor-leaf"); err != nil {
		t.Fatalf("SetFSSyncCursor: %v", err)
	}

	// Sanity: rows present before purge.
	if m, _ := s.GetMembership(id); m == nil {
		t.Fatal("membership should exist before purge")
	}

	purged, failed := gcPurge([]gcCandidate{{CampfireID: id, TransportDir: transportDir, Reason: "idle"}}, s)
	if purged != 1 || failed != 0 {
		t.Fatalf("gcPurge = (purged=%d, failed=%d), want (1, 0)", purged, failed)
	}

	// Transport dir gone.
	if _, err := os.Stat(transportDir); !os.IsNotExist(err) {
		t.Errorf("transport dir still exists after purge: %v", err)
	}
	// Store rows gone.
	if m, _ := s.GetMembership(id); m != nil {
		t.Error("membership still present after purge")
	}
	msgs, _ := s.ListMessages(id, 0)
	if len(msgs) != 0 {
		t.Errorf("messages still present after purge: %d", len(msgs))
	}
	if leaf, _ := s.GetFSSyncCursor(id); leaf != "" {
		t.Errorf("fs sync cursor still present after purge: %q", leaf)
	}
}
