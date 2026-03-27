package seed_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
// The beacon must include campfire_id (required for signature verification).
func TestFindSeedBeacon_JSONFallback(t *testing.T) {
	projectDir := t.TempDir()
	seedsDir := filepath.Join(projectDir, ".campfire", "seeds")
	if err := os.MkdirAll(seedsDir, 0700); err != nil {
		t.Fatalf("creating seeds dir: %v", err)
	}

	// Write a JSON seed beacon — campfire_id is required.
	sb := seed.SeedBeacon{
		CampfireID: "testcampfire",
		Protocol:   "filesystem",
		Dir:        "/tmp/json-seed-test",
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

// TestFindSeedBeacon_NoCampfireID verifies that a seed beacon without campfire_id
// is rejected — unsigned beacons bypass signature verification and must not be loaded.
func TestFindSeedBeacon_NoCampfireID(t *testing.T) {
	projectDir := t.TempDir()
	seedsDir := filepath.Join(projectDir, ".campfire", "seeds")
	if err := os.MkdirAll(seedsDir, 0700); err != nil {
		t.Fatalf("creating seeds dir: %v", err)
	}

	// Write a beacon without campfire_id — should be rejected by parseSeedBeacon.
	sb := seed.SeedBeacon{
		Protocol: "filesystem",
		Dir:      "/tmp/unsigned-seed-test",
		// CampfireID intentionally absent
	}
	data, err := json.Marshal(sb)
	if err != nil {
		t.Fatalf("marshaling seed beacon as JSON: %v", err)
	}
	if err := os.WriteFile(filepath.Join(seedsDir, "test.beacon"), data, 0600); err != nil {
		t.Fatalf("writing seed beacon: %v", err)
	}

	// Override well-known URL to be unreachable so the test is deterministic.
	orig := seed.WellKnownURL
	seed.WellKnownURL = "http://127.0.0.1:0/seed.beacon"
	t.Cleanup(func() { seed.WellKnownURL = orig })

	// Beacon without campfire_id must be silently rejected (treated as invalid).
	// FindSeedBeacon returns (nil, nil) when no valid beacon is found.
	found, err := seed.FindSeedBeacon(projectDir)
	if err != nil {
		t.Fatalf("FindSeedBeacon: unexpected error: %v", err)
	}
	if found != nil {
		t.Errorf("expected beacon without campfire_id to be rejected, got %+v", found)
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
// filesystem campfire directory. Signature verification is mandatory —
// messages must be signed by the key matching campfire_id.
func TestReadConventionMessages_FilesystemProtocol(t *testing.T) {
	// Generate a keypair that will sign the messages.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating keypair: %v", err)
	}

	// Build a fake seed campfire directory
	campfireDir := t.TempDir()
	messagesDir := filepath.Join(campfireDir, "messages")
	if err := os.MkdirAll(messagesDir, 0755); err != nil {
		t.Fatalf("creating messages dir: %v", err)
	}

	decl := map[string]any{
		"convention": "test-convention",
		"version":    "0.1",
		"operation":  "test-op",
	}
	declPayload, _ := json.Marshal(decl)

	// Write a convention:operation message signed by the campfire key.
	msg, err := message.NewMessage(priv, pub, declPayload, []string{convention.ConventionOperationTag}, nil)
	if err != nil {
		t.Fatalf("creating signed message: %v", err)
	}
	writeMsgToDir(t, messagesDir, "0000000001-abc.cbor", msg)

	// Write a second message with a different tag (not convention:operation) — should be filtered.
	otherMsg, err := message.NewMessage(priv, pub, []byte("other payload"), []string{"other:tag"}, nil)
	if err != nil {
		t.Fatalf("creating other message: %v", err)
	}
	writeMsgToDir(t, messagesDir, "0000000002-def.cbor", otherMsg)

	sb := &seed.SeedBeacon{
		CampfireID: hex.EncodeToString(pub),
		Protocol:   "filesystem",
		Dir:        campfireDir,
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

// TestReadConventionMessages_NoCampfireIDRejected verifies that a beacon without
// campfire_id is rejected — there is no unsigned fallback mode.
func TestReadConventionMessages_NoCampfireIDRejected(t *testing.T) {
	campfireDir := t.TempDir()
	// No messages/ subdirectory — but the error should be about missing campfire_id,
	// not about the missing directory.

	sb := &seed.SeedBeacon{
		Protocol: "filesystem",
		Dir:      campfireDir,
		// CampfireID intentionally absent
	}
	_, err := seed.ReadConventionMessages(sb)
	if err == nil {
		t.Fatal("expected error for beacon without campfire_id, got nil")
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

// TestFetchWellKnownBeacon_ValidJSON_WithURL verifies that fetchWellKnownBeacon
// correctly parses a valid JSON beacon returned by an httptest server when the
// beacon uses URL (not Dir). Network beacons may not carry a Dir field.
func TestFetchWellKnownBeacon_ValidJSON_WithURL(t *testing.T) {
	want := seed.SeedBeacon{
		CampfireID: "testcampfire",
		Protocol:   "http",
		URL:        "https://seed.example.com/campfire",
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
	if found.URL != want.URL {
		t.Errorf("URL: want %q, got %q", want.URL, found.URL)
	}
	if found.Protocol != want.Protocol {
		t.Errorf("Protocol: want %q, got %q", want.Protocol, found.Protocol)
	}
}

// TestFetchWellKnownBeacon_RejectsDir verifies that a network-fetched seed beacon
// containing a Dir field is rejected. Dir is a local filesystem path — accepting
// it from a network source would allow a remote server to redirect the client to
// read arbitrary local files.
func TestFetchWellKnownBeacon_RejectsDir(t *testing.T) {
	// Beacon carries a Dir field (filesystem path) — must be rejected when network-fetched.
	malicious := seed.SeedBeacon{
		CampfireID: "testcampfire",
		Protocol:   "filesystem",
		Dir:        "/etc/passwd",
	}
	body, err := json.Marshal(malicious)
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

	// The Dir field on a network beacon must cause the beacon to be rejected.
	// FindSeedBeacon treats a failed well-known fetch as non-fatal → (nil, nil).
	found, err := seed.FindSeedBeacon("")
	if err != nil {
		t.Fatalf("FindSeedBeacon: expected non-fatal rejection (nil, nil), got error: %v", err)
	}
	if found != nil {
		t.Errorf("expected network beacon with Dir to be rejected (got nil), but got %+v", found)
	}
}

// TestFetchWellKnownBeacon_RejectsDirCBOR verifies that the Dir rejection also
// applies to CBOR-encoded network beacons (not just JSON).
func TestFetchWellKnownBeacon_RejectsDirCBOR(t *testing.T) {
	malicious := seed.SeedBeacon{
		CampfireID: "testcampfire",
		Protocol:   "filesystem",
		Dir:        "/tmp/sensitive-data",
	}
	body, err := cfencoding.Marshal(malicious)
	if err != nil {
		t.Fatalf("marshaling beacon as CBOR: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/cbor")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	orig := seed.WellKnownURL
	seed.WellKnownURL = srv.URL
	t.Cleanup(func() { seed.WellKnownURL = orig })

	found, err := seed.FindSeedBeacon("")
	if err != nil {
		t.Fatalf("FindSeedBeacon: expected non-fatal rejection (nil, nil), got error: %v", err)
	}
	if found != nil {
		t.Errorf("expected CBOR network beacon with Dir to be rejected, but got %+v", found)
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

// TestReadConventionMessages_PathTraversal verifies that path traversal attempts
// via SeedBeacon.Dir are rejected before any filesystem reads occur.
func TestReadConventionMessages_PathTraversal(t *testing.T) {
	// Create a legitimate campfire directory so the traversal target exists.
	legitimateDir := t.TempDir()

	cases := []struct {
		name string
		dir  string
	}{
		{
			name: "dotdot relative path",
			dir:  "../../../etc/passwd",
		},
		{
			name: "dotdot from temp dir",
			// Construct a path that goes up from the temp dir then back to etc.
			dir: legitimateDir + "/../../etc",
		},
		{
			name: "null byte in path",
			dir:  "/tmp/seed\x00/../../etc/passwd",
		},
		{
			name: "relative path no traversal",
			dir:  "relative/path/to/campfire",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sb := &seed.SeedBeacon{
				Protocol: "filesystem",
				Dir:      tc.dir,
			}
			_, err := seed.ReadConventionMessages(sb)
			if err == nil {
				t.Fatalf("expected path traversal to be rejected for dir %q, got nil error", tc.dir)
			}
		})
	}
}

// TestReadConventionMessages_SymlinkTraversal verifies that a symlink pointing
// outside the seed directory is resolved and not treated as a traversal attack
// (symlinks to legitimate locations must still work, but the resolved path is used).
func TestReadConventionMessages_SymlinkTraversal(t *testing.T) {
	// Generate a keypair for signing.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating keypair: %v", err)
	}

	// Build a real campfire dir with a signed convention message.
	targetDir := t.TempDir()
	messagesDir := filepath.Join(targetDir, "messages")
	if err := os.MkdirAll(messagesDir, 0755); err != nil {
		t.Fatalf("creating messages dir: %v", err)
	}

	decl := map[string]any{"convention": "test", "version": "0.1", "operation": "op"}
	declPayload, _ := json.Marshal(decl)
	msg, err := message.NewMessage(priv, pub, declPayload, []string{convention.ConventionOperationTag}, nil)
	if err != nil {
		t.Fatalf("creating signed message: %v", err)
	}
	writeMsgToDir(t, messagesDir, "0000000001-test.cbor", msg)

	// Create a symlink pointing to the target directory.
	symlinkBase := t.TempDir()
	symlinkPath := filepath.Join(symlinkBase, "symlinked-campfire")
	if err := os.Symlink(targetDir, symlinkPath); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}

	// Reading through a symlink that points to a legitimate dir should succeed —
	// validateSeedDir resolves symlinks and the resolved path is used.
	sb := &seed.SeedBeacon{
		CampfireID: hex.EncodeToString(pub),
		Protocol:   "filesystem",
		Dir:        symlinkPath,
	}
	msgs, err := seed.ReadConventionMessages(sb)
	if err != nil {
		t.Fatalf("ReadConventionMessages through symlink: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message through symlink, got %d", len(msgs))
	}
}

// TestReadConventionMessages_LegitimateAbsPath verifies that a legitimate
// absolute path (no traversal) continues to work correctly after the fix.
// The beacon must include a campfire_id and messages must be signed.
func TestReadConventionMessages_LegitimateAbsPath(t *testing.T) {
	// Generate a keypair for signing.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating keypair: %v", err)
	}

	campfireDir := t.TempDir()
	messagesDir := filepath.Join(campfireDir, "messages")
	if err := os.MkdirAll(messagesDir, 0755); err != nil {
		t.Fatalf("creating messages dir: %v", err)
	}

	decl := map[string]any{"convention": "test", "version": "0.1", "operation": "op"}
	declPayload, _ := json.Marshal(decl)
	msg, err := message.NewMessage(priv, pub, declPayload, []string{convention.ConventionOperationTag}, nil)
	if err != nil {
		t.Fatalf("creating signed message: %v", err)
	}
	writeMsgToDir(t, messagesDir, "0000000001-ok.cbor", msg)

	sb := &seed.SeedBeacon{
		CampfireID: hex.EncodeToString(pub),
		Protocol:   "filesystem",
		Dir:        campfireDir,
	}
	msgs, err := seed.ReadConventionMessages(sb)
	if err != nil {
		t.Fatalf("ReadConventionMessages with legitimate path: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
}

// TestReadConventionMessages_FileCountLimit verifies that a seed directory
// containing more than MaxSeedFileCount .cbor files is rejected.
func TestReadConventionMessages_FileCountLimit(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating keypair: %v", err)
	}

	campfireDir := t.TempDir()
	messagesDir := filepath.Join(campfireDir, "messages")
	if err := os.MkdirAll(messagesDir, 0755); err != nil {
		t.Fatalf("creating messages dir: %v", err)
	}

	// Write MaxSeedFileCount+1 .cbor files (small, valid convention messages).
	declPayload, _ := json.Marshal(map[string]any{"convention": "test", "version": "0.1", "operation": "op"})
	for i := 0; i <= seed.MaxSeedFileCount; i++ {
		msg, err := message.NewMessage(priv, pub, declPayload, []string{convention.ConventionOperationTag}, nil)
		if err != nil {
			t.Fatalf("creating message %d: %v", i, err)
		}
		msgData, err := cfencoding.Marshal(msg)
		if err != nil {
			t.Fatalf("marshaling message %d: %v", i, err)
		}
		if err := os.WriteFile(filepath.Join(messagesDir, fmt.Sprintf("%010d-msg.cbor", i)), msgData, 0600); err != nil {
			t.Fatalf("writing message %d: %v", i, err)
		}
	}

	sb := &seed.SeedBeacon{
		CampfireID: hex.EncodeToString(pub),
		Protocol:   "filesystem",
		Dir:        campfireDir,
	}
	_, err = seed.ReadConventionMessages(sb)
	if err == nil {
		t.Fatal("expected error when file count exceeds MaxSeedFileCount, got nil")
	}
	if !strings.Contains(err.Error(), "exceeding the limit") {
		t.Errorf("expected 'exceeding the limit' in error, got: %v", err)
	}
}

// TestReadConventionMessages_PerFileSizeLimit verifies that individual files
// exceeding MaxSeedFileSizeBytes are skipped (not returned in results).
func TestReadConventionMessages_PerFileSizeLimit(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating keypair: %v", err)
	}

	campfireDir := t.TempDir()
	messagesDir := filepath.Join(campfireDir, "messages")
	if err := os.MkdirAll(messagesDir, 0755); err != nil {
		t.Fatalf("creating messages dir: %v", err)
	}

	// Write a valid small convention message (should appear in results).
	declPayload, _ := json.Marshal(map[string]any{"convention": "test", "version": "0.1", "operation": "op"})
	smallMsg, err := message.NewMessage(priv, pub, declPayload, []string{convention.ConventionOperationTag}, nil)
	if err != nil {
		t.Fatalf("creating small message: %v", err)
	}
	writeMsgToDir(t, messagesDir, "0000000001-small.cbor", smallMsg)

	// Write an oversized file (exceeds per-file limit) by writing raw bytes.
	// The file has a .cbor extension but is padded far beyond MaxSeedFileSizeBytes.
	oversized := make([]byte, seed.MaxSeedFileSizeBytes+1)
	if err := os.WriteFile(filepath.Join(messagesDir, "0000000002-big.cbor"), oversized, 0600); err != nil {
		t.Fatalf("writing oversized file: %v", err)
	}

	sb := &seed.SeedBeacon{
		CampfireID: hex.EncodeToString(pub),
		Protocol:   "filesystem",
		Dir:        campfireDir,
	}
	msgs, err := seed.ReadConventionMessages(sb)
	if err != nil {
		t.Fatalf("ReadConventionMessages: unexpected error: %v", err)
	}
	// Only the small valid message should be returned; the oversized file is skipped.
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message (oversized skipped), got %d", len(msgs))
	}
}

// TestReadConventionMessages_AggregateSizeLimit verifies that when cumulative
// bytes read exceed MaxSeedAggregateSizeBytes, the seed directory is rejected.
func TestReadConventionMessages_AggregateSizeLimit(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating keypair: %v", err)
	}

	campfireDir := t.TempDir()
	messagesDir := filepath.Join(campfireDir, "messages")
	if err := os.MkdirAll(messagesDir, 0755); err != nil {
		t.Fatalf("creating messages dir: %v", err)
	}

	// Write enough files (each just under per-file limit) to exceed the aggregate cap.
	// MaxSeedAggregateSizeBytes / MaxSeedFileSizeBytes = 10, so 11 nearly-max files
	// will push over the aggregate.
	chunkSize := seed.MaxSeedFileSizeBytes // exactly at per-file limit → skipped
	// Use per-file size just under the per-file cap so files aren't individually skipped.
	chunkSize = seed.MaxSeedFileSizeBytes - 1
	filesNeeded := int(seed.MaxSeedAggregateSizeBytes/int64(chunkSize)) + 1
	chunk := make([]byte, chunkSize)
	for i := 0; i < filesNeeded; i++ {
		if err := os.WriteFile(filepath.Join(messagesDir, fmt.Sprintf("%010d-chunk.cbor", i)), chunk, 0600); err != nil {
			t.Fatalf("writing chunk file %d: %v", i, err)
		}
	}

	sb := &seed.SeedBeacon{
		CampfireID: hex.EncodeToString(pub),
		Protocol:   "filesystem",
		Dir:        campfireDir,
	}
	_, err = seed.ReadConventionMessages(sb)
	if err == nil {
		t.Fatal("expected error when aggregate size exceeds MaxSeedAggregateSizeBytes, got nil")
	}
	if !strings.Contains(err.Error(), "aggregate size limit") {
		t.Errorf("expected 'aggregate size limit' in error, got: %v", err)
	}
}

// TestReadConventionMessages_NormalDirectoryUnchanged verifies that a normal
// seed directory (well within all limits) continues to work correctly.
func TestReadConventionMessages_NormalDirectoryUnchanged(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating keypair: %v", err)
	}

	campfireDir := t.TempDir()
	messagesDir := filepath.Join(campfireDir, "messages")
	if err := os.MkdirAll(messagesDir, 0755); err != nil {
		t.Fatalf("creating messages dir: %v", err)
	}

	// Write a few normal convention messages.
	for i := 0; i < 5; i++ {
		declPayload, _ := json.Marshal(map[string]any{"convention": "test", "version": "0.1", "operation": fmt.Sprintf("op-%d", i)})
		msg, err := message.NewMessage(priv, pub, declPayload, []string{convention.ConventionOperationTag}, nil)
		if err != nil {
			t.Fatalf("creating message %d: %v", i, err)
		}
		writeMsgToDir(t, messagesDir, fmt.Sprintf("%010d-msg.cbor", i+1), msg)
	}

	sb := &seed.SeedBeacon{
		CampfireID: hex.EncodeToString(pub),
		Protocol:   "filesystem",
		Dir:        campfireDir,
	}
	msgs, err := seed.ReadConventionMessages(sb)
	if err != nil {
		t.Fatalf("ReadConventionMessages on normal directory: %v", err)
	}
	if len(msgs) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(msgs))
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
