package main

// Tests for blind relay wiring (design-mcp-security.md §5.c, bead campfire-agent-2sc).
//
// TDD sequence:
//   1. handleJoin on encrypted campfire → service member record has role "blind-relay"
//   2. handleJoin on non-encrypted campfire → service member record has default role (empty/full)
//   3. handleCreate with encrypted=true → service member record has role "blind-relay"
//   4. handleCreate without encrypted → service member record has default role
//   5. Rate limiter counts messages correctly with opaque (random bytes) payloads

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/ratelimit"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newTransportDir creates a temp directory and sets CF_TRANSPORT_DIR to it,
// so that fs.DefaultBaseDir() returns this temp dir. This isolates tests from
// each other and from /tmp/campfire. Returns the dir and a cleanup function.
func newTransportDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CF_TRANSPORT_DIR", dir)
	return dir
}

// readServiceMemberRoleFromTransport reads the member record for the given
// identity (by public key) from the fs transport at transportDir.
func readServiceMemberRoleFromTransport(t *testing.T, transportDir, campfireID, agentPubKeyHex string) string {
	t.Helper()
	transport := fs.New(transportDir)
	members, err := transport.ListMembers(campfireID)
	if err != nil {
		t.Fatalf("listing members for campfire %s: %v", campfireID, err)
	}
	for _, m := range members {
		if fmt.Sprintf("%x", m.PublicKey) == agentPubKeyHex {
			return m.Role
		}
	}
	t.Fatalf("member record not found for pubkey %s in campfire %s", agentPubKeyHex, campfireID)
	return ""
}

// setupEncryptedCampfire creates a campfire (with Encrypted flag as specified)
// in the given transport dir, registers another agent as a full member, and
// returns the campfireID. This simulates a pre-existing campfire that the
// service will join.
func setupEncryptedCampfire(t *testing.T, transportDir string, encrypted bool) string {
	t.Helper()
	// Generate a standalone keypair for the "other agent" that created the campfire.
	otherID, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}

	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}
	cf.Encrypted = encrypted
	cf.AddMember(otherID.PublicKey)

	transport := fs.New(transportDir)
	if err := transport.Init(cf); err != nil {
		t.Fatalf("transport.Init: %v", err)
	}
	if err := transport.WriteMember(cf.PublicKeyHex(), campfire.MemberRecord{
		PublicKey: otherID.PublicKey,
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("transport.WriteMember: %v", err)
	}
	return cf.PublicKeyHex()
}

// ---------------------------------------------------------------------------
// Test 1: handleJoin on encrypted campfire → service member record is blind-relay
// ---------------------------------------------------------------------------

// TestBlindRelay_JoinEncryptedCampfire verifies that when a server joins a
// campfire with Encrypted=true, the server's own MemberRecord has role "blind-relay".
func TestBlindRelay_JoinEncryptedCampfire(t *testing.T) {
	// Redirect CF_TRANSPORT_DIR to a temp dir so both servers use the same transport.
	transportDir := newTransportDir(t)

	srv, stB := newTestServerWithStore(t)
	doInit(t, srv)
	srv.st = stB

	// Create an encrypted campfire in the shared transport dir.
	campfireID := setupEncryptedCampfire(t, transportDir, true /* encrypted */)

	joinArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
	})
	resp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_join","arguments":`+string(joinArgs)+`}`))
	if resp.Error != nil {
		t.Fatalf("campfire_join failed: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}

	// Load service identity and read its member record from the transport.
	agentID, err := identity.Load(srv.identityPath())
	if err != nil {
		t.Fatalf("loading service identity: %v", err)
	}
	role := readServiceMemberRoleFromTransport(t, transportDir, campfireID, agentID.PublicKeyHex())
	if role != campfire.RoleBlindRelay {
		t.Errorf("service member role = %q, want %q (blind-relay) for encrypted campfire",
			role, campfire.RoleBlindRelay)
	}
}

// ---------------------------------------------------------------------------
// Test 2: handleJoin on non-encrypted campfire → service member record is default
// ---------------------------------------------------------------------------

// TestBlindRelay_JoinNonEncryptedCampfire verifies that when a server joins a
// non-encrypted campfire, its MemberRecord does NOT have the blind-relay role.
func TestBlindRelay_JoinNonEncryptedCampfire(t *testing.T) {
	transportDir := newTransportDir(t)

	srv, stB := newTestServerWithStore(t)
	doInit(t, srv)
	srv.st = stB

	// Create a NON-encrypted campfire.
	campfireID := setupEncryptedCampfire(t, transportDir, false /* not encrypted */)

	joinArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
	})
	resp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_join","arguments":`+string(joinArgs)+`}`))
	if resp.Error != nil {
		t.Fatalf("campfire_join failed: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}

	agentID, err := identity.Load(srv.identityPath())
	if err != nil {
		t.Fatalf("loading service identity: %v", err)
	}
	role := readServiceMemberRoleFromTransport(t, transportDir, campfireID, agentID.PublicKeyHex())
	// Non-encrypted campfire: role must NOT be blind-relay.
	if role == campfire.RoleBlindRelay {
		t.Errorf("service member role = %q for non-encrypted campfire; expected non-blind-relay role",
			role)
	}
}

// ---------------------------------------------------------------------------
// Test 3: handleCreate with encrypted=true → service member record is blind-relay
// ---------------------------------------------------------------------------

// TestBlindRelay_CreateEncryptedCampfire verifies that when a server creates an
// encrypted campfire (encrypted=true param), its own MemberRecord has role "blind-relay".
func TestBlindRelay_CreateEncryptedCampfire(t *testing.T) {
	// handleCreate (non-HTTP path) uses DefaultBaseDir for the fs transport.
	// Redirect it to a temp dir.
	transportDir := newTransportDir(t)

	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	createArgs, _ := json.Marshal(map[string]interface{}{
		"encrypted": true,
	})
	resp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_create","arguments":`+string(createArgs)+`}`))
	if resp.Error != nil {
		t.Fatalf("campfire_create failed: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}

	fields := extractCreateResult(t, resp)
	campfireID, ok := fields["campfire_id"].(string)
	if !ok || campfireID == "" {
		t.Fatalf("campfire_id missing in create response: %v", fields)
	}

	agentID, err := identity.Load(srv.identityPath())
	if err != nil {
		t.Fatalf("loading service identity: %v", err)
	}
	role := readServiceMemberRoleFromTransport(t, transportDir, campfireID, agentID.PublicKeyHex())
	if role != campfire.RoleBlindRelay {
		t.Errorf("service member role = %q for encrypted campfire create, want %q",
			role, campfire.RoleBlindRelay)
	}
}

// ---------------------------------------------------------------------------
// Test 4: handleCreate without encrypted → service member record is default
// ---------------------------------------------------------------------------

// TestBlindRelay_CreateNonEncryptedCampfire verifies that creating a campfire
// without encrypted=true gives the service a non-blind-relay member record.
func TestBlindRelay_CreateNonEncryptedCampfire(t *testing.T) {
	transportDir := newTransportDir(t)

	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	resp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_create","arguments":{}}`))
	if resp.Error != nil {
		t.Fatalf("campfire_create failed: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}

	fields := extractCreateResult(t, resp)
	campfireID, ok := fields["campfire_id"].(string)
	if !ok || campfireID == "" {
		t.Fatalf("campfire_id missing: %v", fields)
	}

	agentID, err := identity.Load(srv.identityPath())
	if err != nil {
		t.Fatalf("loading service identity: %v", err)
	}
	role := readServiceMemberRoleFromTransport(t, transportDir, campfireID, agentID.PublicKeyHex())
	if role == campfire.RoleBlindRelay {
		t.Errorf("service member role = %q for non-encrypted campfire create; expected non-blind-relay",
			role)
	}
}

// ---------------------------------------------------------------------------
// Test 5: Rate limiter counts messages correctly with opaque (random bytes) payloads
// ---------------------------------------------------------------------------

// fakeStoreForRateLimit is a minimal store.Store stub for rate limit testing.
type fakeStoreForRateLimit struct {
	calls int
	err   error
}

func (f *fakeStoreForRateLimit) AddMessage(m store.MessageRecord) (bool, error) {
	f.calls++
	return f.err == nil, f.err
}
func (f *fakeStoreForRateLimit) AddMembership(m store.Membership) error                { return nil }
func (f *fakeStoreForRateLimit) UpdateMembershipRole(campfireID, role string) error    { return nil }
func (f *fakeStoreForRateLimit) RemoveMembership(campfireID string) error              { return nil }
func (f *fakeStoreForRateLimit) GetMembership(campfireID string) (*store.Membership, error) {
	return nil, nil
}
func (f *fakeStoreForRateLimit) ListMemberships() ([]store.Membership, error) { return nil, nil }
func (f *fakeStoreForRateLimit) HasMessage(id string) (bool, error)           { return false, nil }
func (f *fakeStoreForRateLimit) GetMessage(id string) (*store.MessageRecord, error) {
	return nil, nil
}
func (f *fakeStoreForRateLimit) GetMessageByPrefix(prefix string) (*store.MessageRecord, error) {
	return nil, nil
}
func (f *fakeStoreForRateLimit) ListMessages(campfireID string, afterTimestamp int64, filter ...store.MessageFilter) ([]store.MessageRecord, error) {
	return nil, nil
}
func (f *fakeStoreForRateLimit) MaxMessageTimestamp(campfireID string, afterTS int64) (int64, error) {
	return 0, nil
}
func (f *fakeStoreForRateLimit) ListReferencingMessages(messageID string) ([]store.MessageRecord, error) {
	return nil, nil
}
func (f *fakeStoreForRateLimit) ListCompactionEvents(campfireID string) ([]store.MessageRecord, error) {
	return nil, nil
}
func (f *fakeStoreForRateLimit) GetReadCursor(campfireID string) (int64, error)         { return 0, nil }
func (f *fakeStoreForRateLimit) SetReadCursor(campfireID string, ts int64) error        { return nil }
func (f *fakeStoreForRateLimit) UpsertPeerEndpoint(e store.PeerEndpoint) error          { return nil }
func (f *fakeStoreForRateLimit) DeletePeerEndpoint(campfireID, pk string) error         { return nil }
func (f *fakeStoreForRateLimit) ListPeerEndpoints(campfireID string) ([]store.PeerEndpoint, error) {
	return nil, nil
}
func (f *fakeStoreForRateLimit) GetPeerRole(campfireID, pk string) (string, error) {
	return "", nil
}
func (f *fakeStoreForRateLimit) UpsertThresholdShare(share store.ThresholdShare) error { return nil }
func (f *fakeStoreForRateLimit) GetThresholdShare(campfireID string) (*store.ThresholdShare, error) {
	return nil, nil
}
func (f *fakeStoreForRateLimit) StorePendingThresholdShare(campfireID string, participantID uint32, shareData []byte) error {
	return nil
}
func (f *fakeStoreForRateLimit) ClaimPendingThresholdShare(campfireID string) (uint32, []byte, error) {
	return 0, nil, nil
}
func (f *fakeStoreForRateLimit) UpdateCampfireID(oldID, newID string) error { return nil }
func (f *fakeStoreForRateLimit) Close() error                               { return nil }
func (f *fakeStoreForRateLimit) CreateInvite(inv store.InviteRecord) error  { return nil }
func (f *fakeStoreForRateLimit) ValidateInvite(campfireID, inviteCode string) (*store.InviteRecord, error) {
	return nil, nil
}
func (f *fakeStoreForRateLimit) RevokeInvite(campfireID, inviteCode string) error            { return nil }
func (f *fakeStoreForRateLimit) ListInvites(campfireID string) ([]store.InviteRecord, error)  { return nil, nil }
func (f *fakeStoreForRateLimit) LookupInvite(inviteCode string) (*store.InviteRecord, error) { return nil, nil }
func (f *fakeStoreForRateLimit) HasAnyInvites(campfireID string) (bool, error)               { return false, nil }
func (f *fakeStoreForRateLimit) IncrementInviteUse(inviteCode string) error { return nil }
func (f *fakeStoreForRateLimit) ValidateAndUseInvite(campfireID, inviteCode string) (*store.InviteRecord, error) {
	return nil, nil
}
func (f *fakeStoreForRateLimit) UpsertEpochSecret(secret store.EpochSecret) error            { return nil }
func (f *fakeStoreForRateLimit) GetEpochSecret(campfireID string, epoch uint64) (*store.EpochSecret, error) {
	return nil, nil
}
func (f *fakeStoreForRateLimit) GetLatestEpochSecret(campfireID string) (*store.EpochSecret, error) {
	return nil, nil
}
func (f *fakeStoreForRateLimit) SetMembershipEncrypted(campfireID string, encrypted bool) error {
	return nil
}
func (f *fakeStoreForRateLimit) ApplyMembershipCommitAtomically(campfireID string, newMember *store.Membership, secret store.EpochSecret) error {
	return nil
}

// ProjectionStore stubs — required by store.Store interface, not exercised by blind-relay rate limit tests.
func (f *fakeStoreForRateLimit) InsertProjectionEntry(campfireID, viewName, messageID string, indexedAt int64) error {
	return nil
}
func (f *fakeStoreForRateLimit) DeleteProjectionEntries(campfireID, viewName string, messageIDs []string) error {
	return nil
}
func (f *fakeStoreForRateLimit) DeleteAllProjectionEntries(campfireID, viewName string) error {
	return nil
}
func (f *fakeStoreForRateLimit) ListProjectionEntries(campfireID, viewName string) ([]store.ProjectionEntry, error) {
	return nil, nil
}
func (f *fakeStoreForRateLimit) GetProjectionMetadata(campfireID, viewName string) (*store.ProjectionMetadata, error) {
	return nil, nil
}
func (f *fakeStoreForRateLimit) SetProjectionMetadata(campfireID, viewName string, meta store.ProjectionMetadata) error {
	return nil
}

// TestRateLimiter_OpaquePayloads verifies that the rate limiter correctly
// counts AddMessage calls when payloads are opaque (random bytes, as would
// be the case for encrypted messages in blind-relay mode).
//
// The rate limiter must not inspect payload content — it counts message
// envelopes, not message content. This test confirms the invariant holds
// for opaque binary payloads of varying sizes.
func TestRateLimiter_OpaquePayloads(t *testing.T) {
	fake := &fakeStoreForRateLimit{}
	w := ratelimit.New(fake, ratelimit.Config{
		MaxMessagesPerMinute: 100,
		MaxMessageBytes:      1024,
		MonthlyMessageCap:    10,
	})

	// Opaque payloads: random-looking bytes (simulating ciphertext).
	// The rate limiter must count them by envelope size, not content.
	opaquePayloads := [][]byte{
		// 32 bytes — a typical encrypted short message
		{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
			0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
			0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
			0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20},
		// 64 bytes — larger encrypted payload
		make([]byte, 64),
		// 1 byte — minimal payload
		{0xff},
		// 128 bytes
		make([]byte, 128),
	}

	for i, payload := range opaquePayloads {
		rec := store.MessageRecord{
			ID:         fmt.Sprintf("msg-%d", i),
			CampfireID: "test-campfire",
			Payload:    payload,
		}
		ok, err := w.AddMessage(rec)
		if err != nil {
			t.Fatalf("AddMessage[%d] (payload size %d): unexpected error: %v", i, len(payload), err)
		}
		if !ok {
			t.Fatalf("AddMessage[%d] (payload size %d): expected ok=true", i, len(payload))
		}
	}

	// All 4 messages passed: verify inner store was called 4 times.
	if fake.calls != len(opaquePayloads) {
		t.Errorf("inner store AddMessage called %d times, want %d", fake.calls, len(opaquePayloads))
	}

	// Opaque payload exceeding MaxMessageBytes must be rejected (size check on
	// ciphertext bytes, not content — confirmed by rejection of oversized envelope).
	oversized := make([]byte, 1025) // > 1024 bytes limit
	_, err := w.AddMessage(store.MessageRecord{
		ID:         "oversized",
		CampfireID: "test-campfire",
		Payload:    oversized,
	})
	if !errors.Is(err, ratelimit.ErrMessageTooLarge) {
		t.Errorf("oversized opaque payload: expected ErrMessageTooLarge, got %v", err)
	}
	// Inner store must NOT have been called for the oversized message.
	if fake.calls != len(opaquePayloads) {
		t.Errorf("inner store called for oversized opaque payload (should be rejected before store)")
	}
}

// ---------------------------------------------------------------------------
// Test 6: handleCreateHTTP stores serviceRole in Membership (not PeerRoleCreator)
// ---------------------------------------------------------------------------

// closeSessionForTest closes a session's store so that t.TempDir cleanup can
// remove the SQLite files without "directory not empty" errors.
func closeSessionForTest(t *testing.T, sm *SessionManager, token string) {
	t.Helper()
	sess := sm.getSession(token)
	if sess == nil {
		return
	}
	sess.Close()
}

// TestBlindRelay_CreateEncryptedCampfireHTTPPath verifies that when a hosted
// server creates an encrypted campfire via the HTTP transport path
// (handleCreateHTTP), the Membership.Role stored in SQLite is
// campfire.RoleBlindRelay — not store.PeerRoleCreator.
//
// Regression test for campfire-agent-qy3: handleCreateHTTP was hardcoding
// store.PeerRoleCreator in AddMembership instead of passing serviceRole.
func TestBlindRelay_CreateEncryptedCampfireHTTPPath(t *testing.T) {
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})
	cfhttp.OverridePollTransportForTest(http.DefaultTransport)

	srv, _, tsURL := newTestServerWithHTTPTransport(t)

	initResp := mcpCall(t, tsURL, "", "campfire_init", map[string]interface{}{})
	token := extractTokenFromInit(t, initResp)
	if token == "" {
		t.Fatal("expected non-empty session token")
	}
	t.Cleanup(func() { closeSessionForTest(t, srv.sessManager, token) })

	createResp := mcpCall(t, tsURL, token, "campfire_create", map[string]interface{}{
		"encrypted": true,
	})
	createText := extractResultText(t, createResp)

	var createResult struct {
		CampfireID string `json:"campfire_id"`
	}
	if err := json.Unmarshal([]byte(createText), &createResult); err != nil || createResult.CampfireID == "" {
		t.Fatalf("parsing campfire_create result: %v (text: %s)", err, createText)
	}

	sess := srv.sessManager.getSession(token)
	if sess == nil {
		t.Fatal("session not found")
	}
	sess.mu.Lock()
	st := sess.st
	sess.mu.Unlock()
	if st == nil {
		t.Fatal("session store is nil")
	}

	mem, err := st.GetMembership(createResult.CampfireID)
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if mem == nil {
		t.Fatal("membership record not found")
	}
	if mem.Role != campfire.RoleBlindRelay {
		t.Errorf("Membership.Role = %q for encrypted campfire (HTTP path), want %q",
			mem.Role, campfire.RoleBlindRelay)
	}
}

// TestBlindRelay_CreateNonEncryptedCampfireHTTPPath verifies that when a
// hosted server creates a non-encrypted campfire via handleCreateHTTP,
// Membership.Role is not campfire.RoleBlindRelay.
func TestBlindRelay_CreateNonEncryptedCampfireHTTPPath(t *testing.T) {
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})
	cfhttp.OverridePollTransportForTest(http.DefaultTransport)

	srv, _, tsURL := newTestServerWithHTTPTransport(t)

	initResp := mcpCall(t, tsURL, "", "campfire_init", map[string]interface{}{})
	token := extractTokenFromInit(t, initResp)
	if token == "" {
		t.Fatal("expected non-empty session token")
	}
	t.Cleanup(func() { closeSessionForTest(t, srv.sessManager, token) })

	createResp := mcpCall(t, tsURL, token, "campfire_create", map[string]interface{}{})
	createText := extractResultText(t, createResp)

	var createResult struct {
		CampfireID string `json:"campfire_id"`
	}
	if err := json.Unmarshal([]byte(createText), &createResult); err != nil || createResult.CampfireID == "" {
		t.Fatalf("parsing campfire_create result: %v (text: %s)", err, createText)
	}

	sess := srv.sessManager.getSession(token)
	if sess == nil {
		t.Fatal("session not found")
	}
	sess.mu.Lock()
	st := sess.st
	sess.mu.Unlock()
	if st == nil {
		t.Fatal("session store is nil")
	}

	mem, err := st.GetMembership(createResult.CampfireID)
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if mem == nil {
		t.Fatal("membership record not found")
	}
	if mem.Role == campfire.RoleBlindRelay {
		t.Errorf("Membership.Role = %q for non-encrypted campfire (HTTP path); expected non-blind-relay",
			mem.Role)
	}
}

// ---------------------------------------------------------------------------
// Test 7: handleCreateHTTP stores Encrypted=true for encrypted campfire (bug campfire-agent-vcw)
// ---------------------------------------------------------------------------

// TestBlindRelay_CreateHTTP_EncryptedFlagSet verifies that the Encrypted field
// in the SQLite store membership is set to true when an encrypted campfire is
// created via the HTTP transport path (handleCreateHTTP).
//
// Regression test for campfire-agent-vcw: handleCreateHTTP was not passing
// Encrypted to AddMembership, so the creator's own record was stored with
// Encrypted=false even for encrypted campfires. This bypasses the downgrade
// prevention checks in AddMessage that inspect Membership.Encrypted.
func TestBlindRelay_CreateHTTP_EncryptedFlagSet(t *testing.T) {
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})
	cfhttp.OverridePollTransportForTest(http.DefaultTransport)

	srv, _, tsURL := newTestServerWithHTTPTransport(t)

	initResp := mcpCall(t, tsURL, "", "campfire_init", map[string]interface{}{})
	token := extractTokenFromInit(t, initResp)
	if token == "" {
		t.Fatal("expected non-empty session token")
	}
	t.Cleanup(func() { closeSessionForTest(t, srv.sessManager, token) })

	createResp := mcpCall(t, tsURL, token, "campfire_create", map[string]interface{}{
		"encrypted": true,
	})
	createText := extractResultText(t, createResp)

	var createResult struct {
		CampfireID string `json:"campfire_id"`
	}
	if err := json.Unmarshal([]byte(createText), &createResult); err != nil || createResult.CampfireID == "" {
		t.Fatalf("parsing campfire_create result: %v (text: %s)", err, createText)
	}

	sess := srv.sessManager.getSession(token)
	if sess == nil {
		t.Fatal("session not found")
	}
	sess.mu.Lock()
	st := sess.st
	sess.mu.Unlock()
	if st == nil {
		t.Fatal("session store is nil")
	}

	mem, err := st.GetMembership(createResult.CampfireID)
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if mem == nil {
		t.Fatal("membership record not found")
	}
	if !mem.Encrypted {
		t.Errorf("Membership.Encrypted = false for encrypted campfire (HTTP path); want true — downgrade prevention will not fire for creator's own record")
	}
}

// TestBlindRelay_CreateHTTP_NonEncryptedFlagClear verifies that the Encrypted
// field is false when a non-encrypted campfire is created via handleCreateHTTP.
func TestBlindRelay_CreateHTTP_NonEncryptedFlagClear(t *testing.T) {
	cfhttp.OverrideHTTPClientForTest(&http.Client{Timeout: 10 * time.Second})
	cfhttp.OverridePollTransportForTest(http.DefaultTransport)

	srv, _, tsURL := newTestServerWithHTTPTransport(t)

	initResp := mcpCall(t, tsURL, "", "campfire_init", map[string]interface{}{})
	token := extractTokenFromInit(t, initResp)
	if token == "" {
		t.Fatal("expected non-empty session token")
	}
	t.Cleanup(func() { closeSessionForTest(t, srv.sessManager, token) })

	createResp := mcpCall(t, tsURL, token, "campfire_create", map[string]interface{}{})
	createText := extractResultText(t, createResp)

	var createResult struct {
		CampfireID string `json:"campfire_id"`
	}
	if err := json.Unmarshal([]byte(createText), &createResult); err != nil || createResult.CampfireID == "" {
		t.Fatalf("parsing campfire_create result: %v (text: %s)", err, createText)
	}

	sess := srv.sessManager.getSession(token)
	if sess == nil {
		t.Fatal("session not found")
	}
	sess.mu.Lock()
	st := sess.st
	sess.mu.Unlock()
	if st == nil {
		t.Fatal("session store is nil")
	}

	mem, err := st.GetMembership(createResult.CampfireID)
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if mem == nil {
		t.Fatal("membership record not found")
	}
	if mem.Encrypted {
		t.Errorf("Membership.Encrypted = true for non-encrypted campfire (HTTP path); want false")
	}
}
