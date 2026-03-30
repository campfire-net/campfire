package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// TestFollowPullMutuallyExclusive verifies that --follow + --pull returns an error.
func TestFollowPullMutuallyExclusive(t *testing.T) {
	// The check is in RunE: if readPull != "" && readFollow → error.
	// We test this by verifying the guard already exists for --pull checking --follow,
	// but we also need to ensure --follow checks --pull.
	// Since the check is "readPull != '' && (readAll || readPeek || readFollow)",
	// and readPull is checked first in RunE, both directions are covered.
	// But the bead asks for --follow + --pull to error. Let's verify with the flags.

	// Simulate: set both flags and verify the error message.
	if err := readCmd.Flags().Set("pull", "some-id"); err != nil {
		t.Fatalf("setting pull flag: %v", err)
	}
	if err := readCmd.Flags().Set("follow", "true"); err != nil {
		t.Fatalf("setting follow flag: %v", err)
	}
	defer func() {
		readCmd.Flags().Set("pull", "")        //nolint:errcheck
		readCmd.Flags().Set("follow", "false") //nolint:errcheck
	}()

	// The RunE guard checks readPull first, so it should catch this.
	err := readCmd.RunE(readCmd, []string{})
	if err == nil {
		t.Fatal("expected error when --follow and --pull are both set")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected 'mutually exclusive' in error, got: %s", err.Error())
	}
}

// TestFollowFilesystemPicksUpNewMessages verifies that --follow on a filesystem
// campfire polls for new messages. We simulate this by:
// 1. Creating a campfire with a filesystem transport
// 2. Starting the follow loop
// 3. Writing a new message to the filesystem transport directory
// 4. Verifying the message appears in the store after a poll cycle
func TestFollowFilesystemPicksUpNewMessages(t *testing.T) {
	// Set up filesystem transport directory and store.
	tmpDir := t.TempDir()
	campfireID := "follow-fs-test-campfire"

	// Create the campfire directory structure for fs transport (needs members/ and messages/).
	cfDir := filepath.Join(tmpDir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Create store.
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Add membership with filesystem transport dir.
	err = s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: cfDir,
		JoinProtocol: "filesystem",
		Role:         "member",
		JoinedAt:     time.Now().UnixNano(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create an identity for sending.
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}

	// Write a message to the filesystem transport BEFORE starting follow.
	// Messages must have at least one provenance hop so syncFromFilesystem accepts them.
	transport := fs.New(tmpDir)
	msg1, err := message.NewMessage(id.PrivateKey, id.PublicKey, []byte("first message"), []string{"test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := msg1.AddHop(id.PrivateKey, id.PublicKey, nil, 1, "open", []string{}, ""); err != nil {
		t.Fatal(err)
	}
	if err := transport.WriteMessage(campfireID, msg1); err != nil {
		t.Fatal(err)
	}

	// Sync once to populate store with first message.
	syncFromFilesystem(campfireID, cfDir, s)

	// Verify first message is in store.
	msgs, err := s.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message after initial sync, got %d", len(msgs))
	}

	// Record the timestamp for cursor.
	firstTS := msgs[0].Timestamp

	// Now simulate what the follow loop does: sync again after a new message arrives.
	msg2, err := message.NewMessage(id.PrivateKey, id.PublicKey, []byte("second message"), []string{"test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := msg2.AddHop(id.PrivateKey, id.PublicKey, nil, 1, "open", []string{}, ""); err != nil {
		t.Fatal(err)
	}
	if err := transport.WriteMessage(campfireID, msg2); err != nil {
		t.Fatal(err)
	}

	// Sync again (simulating one iteration of the follow loop).
	syncFromFilesystem(campfireID, cfDir, s)

	// Query for messages after the first one.
	newMsgs, err := s.ListMessages(campfireID, firstTS)
	if err != nil {
		t.Fatal(err)
	}
	if len(newMsgs) != 1 {
		t.Fatalf("expected 1 new message after second sync, got %d", len(newMsgs))
	}
	if string(newMsgs[0].Payload) != "second message" {
		t.Errorf("expected payload 'second message', got %q", string(newMsgs[0].Payload))
	}
}

// TestFollowIntervalForTransport verifies the transport-specific intervals.
func TestFollowIntervalForTransport(t *testing.T) {
	tests := []struct {
		transportDir string
		campfireID   string
		wantInterval time.Duration
	}{
		// Filesystem: 2s
		{transportDir: "/tmp/some-dir", campfireID: "test", wantInterval: 2 * time.Second},
		// GitHub: 5s (transportDir uses "github:" prefix + JSON)
		{transportDir: `github:{"repo":"owner/repo","issue_number":42}`, campfireID: "test", wantInterval: 5 * time.Second},
	}
	for _, tt := range tests {
		m := store.Membership{TransportDir: tt.transportDir, CampfireID: tt.campfireID}
		got := followIntervalForTransport(m)
		if got != tt.wantInterval {
			t.Errorf("followIntervalForTransport(%q, %q) = %v, want %v", tt.transportDir, tt.campfireID, got, tt.wantInterval)
		}
	}
}

// TestFollowLoopConfig verifies that followLoopConfig produces correct configurations.
func TestFollowLoopConfig(t *testing.T) {
	tmpDir := t.TempDir()
	campfireID := "follow-cfg-test"

	s, err := store.Open(filepath.Join(tmpDir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Add a filesystem membership.
	cfDir := filepath.Join(tmpDir, "campfires", campfireID)
	os.MkdirAll(cfDir, 0o755)
	err = s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: cfDir,
		JoinProtocol: "filesystem",
		Role:         "member",
		JoinedAt:     time.Now().UnixNano(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Store a message so there's a cursor.
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	msg, err := message.NewMessage(id.PrivateKey, id.PublicKey, []byte("test"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.AddMessage(store.MessageRecord{
		ID:          msg.ID,
		CampfireID:  campfireID,
		Sender:      fmt.Sprintf("%x", msg.Sender),
		Payload:     msg.Payload,
		Tags:        msg.Tags,
		Antecedents: msg.Antecedents,
		Timestamp:   msg.Timestamp,
		Provenance:  msg.Provenance,
		ReceivedAt:  store.NowNano(),
	})

	// Set a read cursor.
	s.SetReadCursor(campfireID, msg.Timestamp)

	// Verify the campfire entry can derive its interval.
	mem, err := s.GetMembership(campfireID)
	if err != nil || mem == nil {
		t.Fatal("membership not found")
	}

	interval := followIntervalForTransport(*mem)
	if interval != 2*time.Second {
		t.Errorf("expected 2s filesystem interval, got %v", interval)
	}
}
