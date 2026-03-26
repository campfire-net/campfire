package seed_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/seed"
)

// TestFindSeedBeacon_ProjectLocal verifies that a seed beacon in
// <projectDir>/.campfire/seeds/ is discovered at the highest priority.
func TestFindSeedBeacon_ProjectLocal(t *testing.T) {
	projectDir := t.TempDir()
	seedsDir := filepath.Join(projectDir, ".campfire", "seeds")
	if err := os.MkdirAll(seedsDir, 0700); err != nil {
		t.Fatalf("creating seeds dir: %v", err)
	}

	// Write a seed beacon file
	sb := seed.SeedBeacon{
		CampfireID: "testcampfire",
		Protocol:   "filesystem",
		Dir:        "/tmp/seed-test-campfire",
	}
	data, err := cfencoding.Marshal(sb)
	if err != nil {
		t.Fatalf("marshaling seed beacon: %v", err)
	}
	if err := os.WriteFile(filepath.Join(seedsDir, "test.beacon"), data, 0600); err != nil {
		t.Fatalf("writing seed beacon: %v", err)
	}

	found, err := seed.FindSeedBeacon(projectDir)
	if err != nil {
		t.Fatalf("FindSeedBeacon: %v", err)
	}
	if found == nil {
		t.Fatal("expected to find seed beacon, got nil")
	}
	if found.Dir != "/tmp/seed-test-campfire" {
		t.Errorf("Dir: want %q, got %q", "/tmp/seed-test-campfire", found.Dir)
	}
}

// TestFindSeedBeacon_JSONFallback verifies that JSON-encoded seed beacons are parsed.
func TestFindSeedBeacon_JSONFallback(t *testing.T) {
	projectDir := t.TempDir()
	seedsDir := filepath.Join(projectDir, ".campfire", "seeds")
	if err := os.MkdirAll(seedsDir, 0700); err != nil {
		t.Fatalf("creating seeds dir: %v", err)
	}

	// Write a JSON seed beacon
	sb := seed.SeedBeacon{
		Protocol: "filesystem",
		Dir:      "/tmp/json-seed-test",
	}
	data, err := json.Marshal(sb)
	if err != nil {
		t.Fatalf("marshaling seed beacon as JSON: %v", err)
	}
	if err := os.WriteFile(filepath.Join(seedsDir, "test.beacon"), data, 0600); err != nil {
		t.Fatalf("writing JSON seed beacon: %v", err)
	}

	found, err := seed.FindSeedBeacon(projectDir)
	if err != nil {
		t.Fatalf("FindSeedBeacon: %v", err)
	}
	if found == nil {
		t.Fatal("expected to find JSON seed beacon, got nil")
	}
	if found.Dir != "/tmp/json-seed-test" {
		t.Errorf("Dir: want %q, got %q", "/tmp/json-seed-test", found.Dir)
	}
}

// TestFindSeedBeacon_NoBeacon verifies that (nil, nil) is returned when no
// beacon exists at any layer and the well-known URL is unreachable.
func TestFindSeedBeacon_NoBeacon(t *testing.T) {
	projectDir := t.TempDir() // empty — no seeds dir

	// No seeds exist; well-known URL will fail (not a real server).
	// FindSeedBeacon must return (nil, nil) in this case.
	found, err := seed.FindSeedBeacon(projectDir)
	if err != nil {
		t.Fatalf("FindSeedBeacon: unexpected error: %v", err)
	}
	if found != nil {
		t.Errorf("expected nil seed beacon, got %+v", found)
	}
}

// TestFindSeedBeacon_EmptyProjectDir verifies that passing an empty
// projectDir skips the project-local layer gracefully.
func TestFindSeedBeacon_EmptyProjectDir(t *testing.T) {
	found, err := seed.FindSeedBeacon("")
	if err != nil {
		t.Fatalf("FindSeedBeacon with empty projectDir: %v", err)
	}
	// Nil is expected (no user-local or system seeds in CI, network fails).
	// We only check there's no error.
	_ = found
}

// TestReadConventionMessages_FilesystemProtocol verifies that
// ReadConventionMessages reads convention:operation messages from a
// filesystem campfire directory.
func TestReadConventionMessages_FilesystemProtocol(t *testing.T) {
	// Build a fake seed campfire directory
	campfireDir := t.TempDir()
	messagesDir := filepath.Join(campfireDir, "messages")
	if err := os.MkdirAll(messagesDir, 0755); err != nil {
		t.Fatalf("creating messages dir: %v", err)
	}

	// Write two messages: one with convention:operation, one without
	writeMsg := func(name string, payload []byte, tags []string) {
		t.Helper()
		type rawMsg struct {
			Payload []byte   `cbor:"3,keyasint"`
			Tags    []string `cbor:"4,keyasint"`
		}
		data, err := cfencoding.Marshal(rawMsg{Payload: payload, Tags: tags})
		if err != nil {
			t.Fatalf("marshaling message %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(messagesDir, name), data, 0600); err != nil {
			t.Fatalf("writing message %s: %v", name, err)
		}
	}

	decl := map[string]any{
		"convention": "test-convention",
		"version":    "0.1",
		"operation":  "test-op",
	}
	declPayload, _ := json.Marshal(decl)
	writeMsg("0000000001-abc.cbor", declPayload, []string{"convention:operation"})
	writeMsg("0000000002-def.cbor", []byte("other payload"), []string{"other:tag"})

	sb := &seed.SeedBeacon{
		Protocol: "filesystem",
		Dir:      campfireDir,
	}
	msgs, err := seed.ReadConventionMessages(sb)
	if err != nil {
		t.Fatalf("ReadConventionMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 convention message, got %d", len(msgs))
	}
	if !hasTag(msgs[0].Tags, "convention:operation") {
		t.Errorf("expected convention:operation tag, got %v", msgs[0].Tags)
	}
	if string(msgs[0].Payload) != string(declPayload) {
		t.Errorf("payload mismatch: want %q, got %q", declPayload, msgs[0].Payload)
	}
}

// TestReadConventionMessages_EmptyDir verifies that (nil, nil) is returned
// for an empty messages directory.
func TestReadConventionMessages_EmptyDir(t *testing.T) {
	campfireDir := t.TempDir()
	// No messages/ subdirectory

	sb := &seed.SeedBeacon{
		Protocol: "filesystem",
		Dir:      campfireDir,
	}
	msgs, err := seed.ReadConventionMessages(sb)
	if err != nil {
		t.Fatalf("ReadConventionMessages on empty dir: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
}

// TestReadConventionMessages_NoDirInBeacon verifies that an error is returned
// when the seed beacon has no Dir set.
func TestReadConventionMessages_NoDirInBeacon(t *testing.T) {
	sb := &seed.SeedBeacon{
		Protocol: "filesystem",
		Dir:      "", // missing
	}
	_, err := seed.ReadConventionMessages(sb)
	if err == nil {
		t.Fatal("expected error when Dir is empty, got nil")
	}
}

// TestReadConventionMessages_HTTPProtocol verifies that http protocol returns
// an appropriate error (not yet supported).
func TestReadConventionMessages_HTTPProtocol(t *testing.T) {
	sb := &seed.SeedBeacon{
		Protocol: "http",
		URL:      "https://example.com/campfire",
	}
	_, err := seed.ReadConventionMessages(sb)
	if err == nil {
		t.Fatal("expected error for http protocol, got nil")
	}
}

func hasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}
