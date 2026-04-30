package cmd

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/convention/delegation"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
)

// runTrustRevoke executes `cf trust revoke` with the given args and captures
// stdout, stderr, and the returned error.
func runTrustRevoke(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	origStdout, origStderr := os.Stdout, os.Stderr
	os.Stdout = wOut
	os.Stderr = wErr

	// Reset persistent flag state between tests.
	jsonOutput = false

	rootCmd.SetArgs(append([]string{"trust", "revoke"}, args...))
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

// TestTrustRevokeCmd_Success runs cf trust revoke end-to-end and asserts the
// identity:revoked message landed in the campfire.
func TestTrustRevokeCmd_Success(t *testing.T) {
	campfireHex := setupTrustGrantEnv(t)

	childPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating child key: %v", err)
	}
	childHex := hex.EncodeToString(childPub)

	stdout, stderr, err := runTrustRevoke(t, campfireHex, childHex)
	if err != nil {
		t.Fatalf("cf trust revoke: %v\nstdout=%s\nstderr=%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "revoked:") {
		t.Errorf("expected 'revoked:' in stdout, got: %s", stdout)
	}

	// Verify the message actually landed: look for identity:revoked in the campfire.
	s, err := store.Open(os.Getenv("CF_HOME") + "/store.db")
	if err != nil {
		t.Fatalf("reopening store: %v", err)
	}
	defer s.Close()
	m, err := s.GetMembership(campfireHex)
	if err != nil || m == nil {
		t.Fatalf("getting membership: %v", err)
	}
	tr := fs.ForDir(m.TransportDir)
	msgs, err := tr.ListMessages(campfireHex)
	if err != nil {
		t.Fatalf("listing messages: %v", err)
	}
	var found bool
	for i := range msgs {
		for _, tag := range msgs[i].Tags {
			if tag != delegation.RevokedTag {
				continue
			}
			if strings.Contains(string(msgs[i].Payload), childHex) {
				found = true
			}
		}
	}
	if !found {
		t.Error("identity:revoked message with expected child pubkey not found in campfire")
	}
}

// TestTrustRevokeCmd_MissingArgs asserts cobra rejects invocations missing
// positional arguments.
func TestTrustRevokeCmd_MissingArgs(t *testing.T) {
	setupTrustGrantEnv(t)

	// Only one arg — should fail with cobra's ExactArgs(2) error.
	_, _, err := runTrustRevoke(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err == nil {
		t.Fatal("expected error for missing second arg, got nil")
	}
	if !strings.Contains(err.Error(), "accepts 2 arg") && !strings.Contains(err.Error(), "2 arg(s)") {
		t.Errorf("expected arg count error, got: %v", err)
	}
}

// TestTrustRevokeCmd_MalformedHex asserts a non-hex child pubkey errors out.
func TestTrustRevokeCmd_MalformedHex(t *testing.T) {
	campfireHex := setupTrustGrantEnv(t)

	_, _, err := runTrustRevoke(t, campfireHex, "not-a-hex-string")
	if err == nil {
		t.Fatal("expected error for malformed hex, got nil")
	}
	if !strings.Contains(err.Error(), "invalid child pubkey") {
		t.Errorf("expected 'invalid child pubkey' error, got: %v", err)
	}
}

// TestTrustRevokeCmd_JSONOutput asserts --json emits a parseable object with
// all required keys (msg_id, parent, child, campfire_id) and no expires_at.
func TestTrustRevokeCmd_JSONOutput(t *testing.T) {
	campfireHex := setupTrustGrantEnv(t)

	childPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating child key: %v", err)
	}
	childHex := hex.EncodeToString(childPub)

	rOut, wOut, _ := os.Pipe()
	origStdout := os.Stdout
	os.Stdout = wOut

	jsonOutput = true
	defer func() { jsonOutput = false }()

	rootCmd.SetArgs([]string{"trust", "revoke", campfireHex, childHex, "--json"})
	runErr := rootCmd.Execute()

	wOut.Close()
	os.Stdout = origStdout

	var out bytes.Buffer
	out.ReadFrom(rOut) //nolint:errcheck
	rOut.Close()

	if runErr != nil {
		t.Fatalf("cf trust revoke --json: %v\nstdout=%s", runErr, out.String())
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("parsing JSON: %v\noutput=%s", err, out.String())
	}
	for _, key := range []string{"msg_id", "parent", "child", "campfire_id"} {
		if _, ok := parsed[key]; !ok {
			t.Errorf("JSON output missing key %q: %v", key, parsed)
		}
	}
	// expires_at must NOT be present (revoke has no TTL).
	if _, ok := parsed["expires_at"]; ok {
		t.Error("JSON output must not contain 'expires_at' for revoke")
	}
	if parsed["child"] != childHex {
		t.Errorf("child mismatch: got %v, want %s", parsed["child"], childHex)
	}
	if parsed["campfire_id"] != campfireHex {
		t.Errorf("campfire_id mismatch: got %v, want %s", parsed["campfire_id"], campfireHex)
	}
}
