package seed_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/convention"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/message"
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

	// Override the well-known URL with a guaranteed-unreachable address so
	// this test is not network-dependent. Restore the original after the test.
	orig := seed.WellKnownURL
	seed.WellKnownURL = "http://127.0.0.1:0/seed.beacon" // port 0 is always unreachable
	t.Cleanup(func() { seed.WellKnownURL = orig })

	// No seeds exist; well-known URL will fail (controlled test server).
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
	writeMsg("0000000001-abc.cbor", declPayload, []string{convention.ConventionOperationTag})
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
	if !hasTag(msgs[0].Tags, convention.ConventionOperationTag) {
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

// TestReadConventionMessages_SignatureVerification_Reject verifies that when
// SeedBeacon.CampfireID is set, messages signed by a DIFFERENT key are rejected.
// This is the regression test for the missing signature verification bug.
func TestReadConventionMessages_SignatureVerification_Reject(t *testing.T) {
	// Generate two keypairs: one for the beacon identity, one for the actual signer.
	beaconPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating beacon keypair: %v", err)
	}
	differentPub, differentPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating different keypair: %v", err)
	}

	campfireDir := t.TempDir()
	messagesDir := filepath.Join(campfireDir, "messages")
	if err := os.MkdirAll(messagesDir, 0755); err != nil {
		t.Fatalf("creating messages dir: %v", err)
	}

	// Write a convention:operation message signed by the DIFFERENT (wrong) key.
	declPayload, _ := json.Marshal(map[string]any{"convention": "test", "version": "0.1", "operation": "op"})
	msg, err := message.NewMessage(differentPriv, differentPub, declPayload, []string{convention.ConventionOperationTag}, nil)
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}
	writeMsgToDir(t, messagesDir, "0000000001-wrongkey.cbor", msg)

	// Beacon claims beaconPub as its identity, but message is signed by differentPub.
	sb := &seed.SeedBeacon{
		CampfireID: hex.EncodeToString(beaconPub),
		Protocol:   "filesystem",
		Dir:        campfireDir,
	}

	_, err = seed.ReadConventionMessages(sb)
	if err == nil {
		t.Fatal("expected signature verification to reject mismatched key, got nil error")
	}
}

// TestReadConventionMessages_SignatureVerification_Accept verifies that when
// SeedBeacon.CampfireID is set, messages signed by the MATCHING key are accepted.
func TestReadConventionMessages_SignatureVerification_Accept(t *testing.T) {
	// Generate a keypair that will be both the beacon identity and the message signer.
	beaconPub, beaconPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating beacon keypair: %v", err)
	}

	campfireDir := t.TempDir()
	messagesDir := filepath.Join(campfireDir, "messages")
	if err := os.MkdirAll(messagesDir, 0755); err != nil {
		t.Fatalf("creating messages dir: %v", err)
	}

	// Write a convention:operation message signed by the correct (matching) key.
	declPayload, _ := json.Marshal(map[string]any{"convention": "test", "version": "0.1", "operation": "op"})
	msg, err := message.NewMessage(beaconPriv, beaconPub, declPayload, []string{convention.ConventionOperationTag}, nil)
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}
	writeMsgToDir(t, messagesDir, "0000000001-correct.cbor", msg)

	// Beacon CampfireID matches the signing key.
	sb := &seed.SeedBeacon{
		CampfireID: hex.EncodeToString(beaconPub),
		Protocol:   "filesystem",
		Dir:        campfireDir,
	}

	msgs, err := seed.ReadConventionMessages(sb)
	if err != nil {
		t.Fatalf("ReadConventionMessages with matching key: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 convention message, got %d", len(msgs))
	}
	if string(msgs[0].Payload) != string(declPayload) {
		t.Errorf("payload mismatch: want %q, got %q", declPayload, msgs[0].Payload)
	}
}

// TestFetchWellKnownBeacon_ValidJSON verifies that fetchWellKnownBeacon correctly
// parses a valid JSON beacon returned by an httptest server.
func TestFetchWellKnownBeacon_ValidJSON(t *testing.T) {
	want := seed.SeedBeacon{
		Protocol: "filesystem",
		Dir:      "/tmp/seed-campfire",
	}
	body, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshaling beacon: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	orig := seed.WellKnownURL
	seed.WellKnownURL = srv.URL
	t.Cleanup(func() { seed.WellKnownURL = orig })

	found, err := seed.FindSeedBeacon("")
	if err != nil {
		t.Fatalf("FindSeedBeacon: unexpected error: %v", err)
	}
	if found == nil {
		t.Fatal("expected beacon from httptest server, got nil")
	}
	if found.Dir != want.Dir {
		t.Errorf("Dir: want %q, got %q", want.Dir, found.Dir)
	}
	if found.Protocol != want.Protocol {
		t.Errorf("Protocol: want %q, got %q", want.Protocol, found.Protocol)
	}
}

// TestFetchWellKnownBeacon_404 verifies that fetchWellKnownBeacon returns nil
// when the server responds with HTTP 404.
func TestFetchWellKnownBeacon_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	orig := seed.WellKnownURL
	seed.WellKnownURL = srv.URL
	t.Cleanup(func() { seed.WellKnownURL = orig })

	// FindSeedBeacon treats a failed well-known fetch as non-fatal → (nil, nil).
	found, err := seed.FindSeedBeacon("")
	if err != nil {
		t.Fatalf("FindSeedBeacon: expected no error on 404, got %v", err)
	}
	if found != nil {
		t.Errorf("expected nil beacon on 404, got %+v", found)
	}
}

// TestFetchWellKnownBeacon_OversizedBody verifies that fetchWellKnownBeacon
// rejects a response body that exceeds the 64 KiB limit.
func TestFetchWellKnownBeacon_OversizedBody(t *testing.T) {
	// Produce a body slightly larger than 64 KiB that still looks like JSON
	// (to defeat any early-parse shortcut), but whose real content exceeds the cap.
	// We build a valid-prefix JSON padded far past the limit so the limit reader
	// truncates it before json.Unmarshal can see a complete object.
	over64k := strings.Repeat("x", 65*1024)
	body := `{"dir":"` + over64k + `"}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	orig := seed.WellKnownURL
	seed.WellKnownURL = srv.URL
	t.Cleanup(func() { seed.WellKnownURL = orig })

	// The truncated body will fail JSON/CBOR parsing → FindSeedBeacon returns nil.
	found, err := seed.FindSeedBeacon("")
	if err != nil {
		t.Fatalf("FindSeedBeacon: expected no error (oversized = non-fatal), got %v", err)
	}
	if found != nil {
		t.Errorf("expected nil beacon for oversized body, got %+v", found)
	}
}

// TestFetchWellKnownBeacon_InvalidJSON verifies that fetchWellKnownBeacon returns
// nil when the server responds with a 200 but invalid JSON/CBOR body.
func TestFetchWellKnownBeacon_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("this is not valid json or cbor !!!"))
	}))
	defer srv.Close()

	orig := seed.WellKnownURL
	seed.WellKnownURL = srv.URL
	t.Cleanup(func() { seed.WellKnownURL = orig })

	// Invalid body → parse fails → FindSeedBeacon returns nil non-fatally.
	found, err := seed.FindSeedBeacon("")
	if err != nil {
		t.Fatalf("FindSeedBeacon: expected no error on invalid JSON, got %v", err)
	}
	if found != nil {
		t.Errorf("expected nil beacon for invalid JSON, got %+v", found)
	}
}

// writeMsgToDir writes a signed message.Message as CBOR to the given directory.
func writeMsgToDir(t *testing.T, dir, filename string, msg *message.Message) {
	t.Helper()
	data, err := cfencoding.Marshal(msg)
	if err != nil {
		t.Fatalf("marshaling message %s: %v", filename, err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), data, 0600); err != nil {
		t.Fatalf("writing message %s: %v", filename, err)
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
