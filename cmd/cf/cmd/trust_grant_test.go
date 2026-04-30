package cmd

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	cfencoding "github.com/campfire-net/campfire/cf-protocol/encoding"
	"github.com/campfire-net/campfire/pkg/convention/delegation"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
)

// setupTrustGrantEnv builds a minimal CF_HOME with:
//   - agent identity saved at <CF_HOME>/identity.json
//   - a filesystem-transport campfire the agent is a member of
//   - a local membership record so protocol.Client.Send accepts the write
//
// Returns the campfire hex ID. The CF_HOME env is set via t.Setenv so all
// CLI invocations in the test use it.
func setupTrustGrantEnv(t *testing.T) (campfireHex string) {
	t.Helper()

	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	t.Setenv("CF_BEACON_DIR", filepath.Join(cfHomeDir, "beacons"))

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating agent identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	// Generate a campfire identity.
	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	campfireHex = cfID.PublicKeyHex()

	// Build the on-disk transport layout.
	transportBase := t.TempDir()
	cfDir := filepath.Join(transportBase, campfireHex)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	state := &campfire.CampfireState{
		PublicKey:             cfID.PublicKey,
		PrivateKey:            cfID.PrivateKey,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		CreatedAt:             time.Now().UnixNano(),
	}
	stateData, err := cfencoding.Marshal(state)
	if err != nil {
		t.Fatalf("marshalling campfire state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfDir, "campfire.cbor"), stateData, 0644); err != nil {
		t.Fatalf("writing campfire state: %v", err)
	}

	tr := fs.New(transportBase)
	if err := tr.WriteMember(campfireHex, campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("writing member: %v", err)
	}

	// Store + membership record so protocol.Init finds the campfire.
	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	if err := s.AddMembership(store.Membership{
		CampfireID:    campfireHex,
		TransportDir:  tr.CampfireDir(campfireHex),
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	return campfireHex
}

// runTrustGrant executes `cf trust grant` with the given args and captures
// stdout, stderr, and the returned error.
func runTrustGrant(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	origStdout, origStderr := os.Stdout, os.Stderr
	os.Stdout = wOut
	os.Stderr = wErr

	// Reset flag state between tests (cobra persists flags across Execute calls).
	trustGrantTTL = 24 * time.Hour
	_ = trustGrantCmd.Flags().Set("ttl", "24h")
	jsonOutput = false

	rootCmd.SetArgs(append([]string{"trust", "grant"}, args...))
	runErr := rootCmd.Execute()

	wOut.Close()
	wErr.Close()
	os.Stdout = origStdout
	os.Stderr = origStderr

	var outBuf, errBuf bytes.Buffer
	outBuf.ReadFrom(rOut) //nolint:errcheck
	errBuf.ReadFrom(rErr) //nolint:errcheck
	rOut.Close()
	rErr.Close()

	return outBuf.String(), errBuf.String(), runErr
}

// TestTrustGrantCmd_Success runs cf trust grant end-to-end and asserts the
// identity:granted message landed in the campfire.
func TestTrustGrantCmd_Success(t *testing.T) {
	campfireHex := setupTrustGrantEnv(t)

	childPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating child key: %v", err)
	}
	childHex := hex.EncodeToString(childPub)

	stdout, stderr, err := runTrustGrant(t, campfireHex, childHex)
	if err != nil {
		t.Fatalf("cf trust grant: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "grant posted:") {
		t.Errorf("expected 'grant posted:' in stdout, got: %s", stdout)
	}

	// Verify the message actually landed: read the campfire's messages dir
	// and find an identity:granted tagged message with our child pubkey.
	// Find the transport dir by loading the membership.
	s, err := store.Open(filepath.Join(os.Getenv("CF_HOME"), "store.db"))
	if err != nil {
		t.Fatalf("reopening store: %v", err)
	}
	defer s.Close()
	m, err := s.GetMembership(campfireHex)
	if err != nil || m == nil {
		t.Fatalf("getting membership: %v", err)
	}
	// Climb one level up from CampfireDir to get the base.
	tr := fs.ForDir(m.TransportDir)
	msgs, err := tr.ListMessages(campfireHex)
	if err != nil {
		t.Fatalf("listing messages: %v", err)
	}
	var found bool
	for i := range msgs {
		for _, tag := range msgs[i].Tags {
			if tag != delegation.GrantedTag {
				continue
			}
			gp, err := delegation.ParseGrantPayload(msgs[i].Payload)
			if err != nil {
				continue
			}
			if gp.ChildPubkey == childHex && gp.CampfireID == campfireHex {
				found = true
			}
		}
	}
	if !found {
		t.Error("identity:granted message with expected child pubkey not found in campfire")
	}
}

// TestTrustGrantCmd_MissingArgs asserts cobra rejects invocations missing
// positional arguments.
func TestTrustGrantCmd_MissingArgs(t *testing.T) {
	setupTrustGrantEnv(t)

	// Only one arg — should fail with cobra's ExactArgs(2) error.
	_, _, err := runTrustGrant(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err == nil {
		t.Fatal("expected error for missing second arg, got nil")
	}
	if !strings.Contains(err.Error(), "accepts 2 arg") && !strings.Contains(err.Error(), "2 arg(s)") {
		t.Errorf("expected arg count error, got: %v", err)
	}
}

// TestTrustGrantCmd_MalformedHex asserts a non-hex child pubkey errors out.
func TestTrustGrantCmd_MalformedHex(t *testing.T) {
	campfireHex := setupTrustGrantEnv(t)

	_, _, err := runTrustGrant(t, campfireHex, "not-a-hex-string")
	if err == nil {
		t.Fatal("expected error for malformed hex, got nil")
	}
	if !strings.Contains(err.Error(), "invalid child pubkey") {
		t.Errorf("expected 'invalid child pubkey' error, got: %v", err)
	}
}

// TestTrustGrantCmd_TTLExceedsCeiling asserts --ttl > 7d returns an error
// whose message carries the ceiling hint. The handler returns errors rather
// than calling os.Exit, so cobra's RunE error path produces a non-zero exit
// code while remaining testable in-process.
func TestTrustGrantCmd_TTLExceedsCeiling(t *testing.T) {
	campfireHex := setupTrustGrantEnv(t)

	childPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating child key: %v", err)
	}
	childHex := hex.EncodeToString(childPub)

	_, _, runErr := runTrustGrant(t, campfireHex, childHex, "--ttl", "200h")
	if runErr == nil {
		t.Fatal("expected error for --ttl 200h, got nil")
	}
	if !strings.Contains(runErr.Error(), "max TTL is 7 days") {
		t.Errorf("expected ceiling hint in error, got: %v", runErr)
	}
}

// TestTrustGrantCmd_JSONOutput asserts --json emits a parseable object with
// all required keys.
func TestTrustGrantCmd_JSONOutput(t *testing.T) {
	campfireHex := setupTrustGrantEnv(t)

	childPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating child key: %v", err)
	}
	childHex := hex.EncodeToString(childPub)

	// Set --json via the persistent flag; the test helper resets it each run
	// so we set it after calling runTrustGrant would — we call manually.
	rOut, wOut, _ := os.Pipe()
	origStdout := os.Stdout
	os.Stdout = wOut

	trustGrantTTL = 24 * time.Hour
	_ = trustGrantCmd.Flags().Set("ttl", "24h")
	jsonOutput = true
	defer func() { jsonOutput = false }()

	rootCmd.SetArgs([]string{"trust", "grant", campfireHex, childHex, "--json"})
	runErr := rootCmd.Execute()

	wOut.Close()
	os.Stdout = origStdout

	var out bytes.Buffer
	out.ReadFrom(rOut) //nolint:errcheck
	rOut.Close()

	if runErr != nil {
		t.Fatalf("cf trust grant --json: %v\nstdout=%s", runErr, out.String())
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("parsing JSON: %v\noutput=%s", err, out.String())
	}
	for _, key := range []string{"msg_id", "parent", "child", "campfire_id", "expires_at", "ttl_seconds"} {
		if _, ok := parsed[key]; !ok {
			t.Errorf("JSON output missing key %q: %v", key, parsed)
		}
	}
	if parsed["child"] != childHex {
		t.Errorf("child mismatch: got %v, want %s", parsed["child"], childHex)
	}
	if parsed["campfire_id"] != campfireHex {
		t.Errorf("campfire_id mismatch: got %v, want %s", parsed["campfire_id"], campfireHex)
	}
}
