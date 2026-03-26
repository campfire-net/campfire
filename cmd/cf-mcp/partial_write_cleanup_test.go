package main

// partial_write_cleanup_test.go — Tests for handleRemoteJoin partial-write cleanup.
//
// Verifies that handleRemoteJoin rolls back the campfire directory when any
// step after MkdirAll fails, and leaves pre-existing directories untouched.
//
// Bead: campfire-agent-pmv

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// ---------------------------------------------------------------------------
// Fake store that fails AddMembership on demand.
// ---------------------------------------------------------------------------

// failAddMembershipStore wraps a real store.Store and returns an error from
// AddMembership. All other methods delegate to the underlying store so that
// the rest of the join path (cfhttp.Join, WriteMember, dir creation) works.
type failAddMembershipStore struct {
	store.Store
	addMembershipErr error
}

func (f *failAddMembershipStore) AddMembership(m store.Membership) error {
	if f.addMembershipErr != nil {
		return f.addMembershipErr
	}
	return f.Store.AddMembership(m)
}

// ---------------------------------------------------------------------------
// Helper: set up Server A (owns the campfire) and return campfireID + Server A URL.
// setupServerAWithCampfire must be called AFTER newTransportDir (if used) so
// the SSRF override and HTTP client override are set before mcpCall.
// ---------------------------------------------------------------------------

func setupServerAWithCampfire(t *testing.T) (campfireID, tsURL string) {
	t.Helper()

	cfhttp.OverrideValidateJoinerEndpointForTest()
	t.Cleanup(cfhttp.RestoreValidateJoinerEndpoint)
	origValidate := ssrfValidateEndpoint
	ssrfValidateEndpoint = func(string) error { return nil }
	t.Cleanup(func() { ssrfValidateEndpoint = origValidate })
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})

	_, _, tsURL = newTestServerWithHTTPTransport(t)

	initResp := mcpCall(t, tsURL, "", "campfire_init", map[string]interface{}{})
	tokenA := extractTokenFromInit(t, initResp)

	createResp := mcpCall(t, tsURL, tokenA, "campfire_create", map[string]interface{}{
		"description": "partial-write cleanup test",
	})
	createText := extractResultText(t, createResp)

	var result struct {
		CampfireID string `json:"campfire_id"`
	}
	if err := json.Unmarshal([]byte(createText), &result); err != nil {
		t.Fatalf("parsing create result: %v (text: %s)", err, createText)
	}
	if result.CampfireID == "" {
		t.Fatal("campfire_id is empty")
	}
	return result.CampfireID, tsURL
}

// newCleanupTransportDir creates an isolated temp dir for the fs transport and
// returns its path. It sets CF_TRANSPORT_DIR and restores it after the test.
// Named differently from newTransportDir (blindrelay_test.go) to avoid
// redeclaration conflicts with other test files in the package.
func newCleanupTransportDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CF_TRANSPORT_DIR", dir)
	return dir
}

// ---------------------------------------------------------------------------
// Test 1: Failed join cleans up orphaned campfire directory.
// ---------------------------------------------------------------------------

// TestPartialWriteCleanup_FailedJoinRemovesDir verifies that when handleRemoteJoin
// fails after creating the campfire directory (due to AddMembership returning an
// error), the campfire directory is removed from the filesystem.
func TestPartialWriteCleanup_FailedJoinRemovesDir(t *testing.T) {
	// Control the fs transport directory so we can verify filesystem state.
	transportDir := newCleanupTransportDir(t)

	campfireID, tsURL := setupServerAWithCampfire(t)

	// Server B: real store, but AddMembership always fails.
	srvB, realStore := newTestServerWithStore(t)
	doInit(t, srvB)

	failStore := &failAddMembershipStore{
		Store:            realStore,
		addMembershipErr: errors.New("injected AddMembership failure"),
	}
	srvB.st = failStore

	// Determine the campfire dir that handleRemoteJoin would create.
	// Server B has no httpTransport, so fsTransport() uses CF_TRANSPORT_DIR.
	transport := fs.New(transportDir)
	campfireDir := transport.CampfireDir(campfireID)

	// Pre-condition: dir does not exist.
	if _, err := os.Stat(campfireDir); err == nil {
		t.Fatalf("pre-condition failed: campfire dir already exists at %s", campfireDir)
	}

	// Attempt the remote join via dispatch. It must fail.
	joinArgs := `{"name":"campfire_join","arguments":{"campfire_id":"` + campfireID + `","peer_endpoint":"` + tsURL + `"}}`
	resp := srvB.dispatch(makeReq("tools/call", joinArgs))
	if resp.Error == nil {
		t.Fatal("expected error from remote join when AddMembership fails, got nil")
	}

	// The campfire directory must have been cleaned up.
	if _, err := os.Stat(campfireDir); err == nil {
		t.Errorf("campfire dir %s still exists after failed join — partial-write cleanup did not run", campfireDir)
	}
}

// ---------------------------------------------------------------------------
// Test 2: Successful join leaves the campfire directory and files in place.
// ---------------------------------------------------------------------------

// TestPartialWriteCleanup_SuccessfulJoinPreservesDir verifies that when
// handleRemoteJoin succeeds, the campfire directory and its contents persist
// on the filesystem.
func TestPartialWriteCleanup_SuccessfulJoinPreservesDir(t *testing.T) {
	// Control the fs transport directory so we can verify filesystem state.
	transportDir := newCleanupTransportDir(t)

	campfireID, tsURL := setupServerAWithCampfire(t)

	// Server B: standard server with a working store.
	srvB, _ := newTestServerWithStore(t)
	doInit(t, srvB)

	// Determine the campfire dir.
	transport := fs.New(transportDir)
	campfireDir := transport.CampfireDir(campfireID)

	// Attempt the remote join.
	joinArgs := `{"name":"campfire_join","arguments":{"campfire_id":"` + campfireID + `","peer_endpoint":"` + tsURL + `"}}`
	resp := srvB.dispatch(makeReq("tools/call", joinArgs))
	if resp.Error != nil {
		t.Fatalf("remote join failed unexpectedly: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}

	// The campfire directory must still exist.
	if _, err := os.Stat(campfireDir); err != nil {
		t.Errorf("campfire dir %s missing after successful join: %v", campfireDir, err)
	}

	// campfire.cbor must exist.
	cbor := campfireDir + "/campfire.cbor"
	if _, err := os.Stat(cbor); err != nil {
		t.Errorf("campfire.cbor missing after successful join: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test 3: Pre-existing campfire directory is NOT deleted on failed join.
// ---------------------------------------------------------------------------

// TestPartialWriteCleanup_PreExistingDirSurvivesFailure verifies that when
// the campfire directory already existed before handleRemoteJoin was called,
// a failed join does NOT delete it.
func TestPartialWriteCleanup_PreExistingDirSurvivesFailure(t *testing.T) {
	// Control the fs transport directory so we can verify filesystem state.
	transportDir := newCleanupTransportDir(t)

	campfireID, tsURL := setupServerAWithCampfire(t)

	// Server B: real store, but AddMembership always fails.
	srvB, realStore := newTestServerWithStore(t)
	doInit(t, srvB)

	failStore := &failAddMembershipStore{
		Store:            realStore,
		addMembershipErr: errors.New("injected AddMembership failure"),
	}
	srvB.st = failStore

	// Pre-create the campfire directory to simulate a pre-existing state.
	transport := fs.New(transportDir)
	campfireDir := transport.CampfireDir(campfireID)
	if err := os.MkdirAll(campfireDir, 0755); err != nil {
		t.Fatalf("pre-creating campfire dir: %v", err)
	}
	// Write a sentinel file to detect if the directory is wiped.
	sentinelPath := campfireDir + "/sentinel.txt"
	if err := os.WriteFile(sentinelPath, []byte("pre-existing"), 0600); err != nil {
		t.Fatalf("writing sentinel: %v", err)
	}

	// Attempt the remote join. It must fail.
	joinArgs := `{"name":"campfire_join","arguments":{"campfire_id":"` + campfireID + `","peer_endpoint":"` + tsURL + `"}}`
	resp := srvB.dispatch(makeReq("tools/call", joinArgs))
	if resp.Error == nil {
		t.Fatal("expected error from remote join when AddMembership fails, got nil")
	}

	// The pre-existing campfire directory must still exist.
	if _, err := os.Stat(campfireDir); err != nil {
		t.Errorf("pre-existing campfire dir %s was deleted by failed join: %v", campfireDir, err)
	}
	// The sentinel file must still be present.
	if _, err := os.Stat(sentinelPath); err != nil {
		t.Errorf("sentinel file missing — pre-existing dir was wiped by failed join: %v", err)
	}
}
