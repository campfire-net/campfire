package admission_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	campfire "github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/admission"
	"github.com/campfire-net/campfire/pkg/store"
)

// --- mock implementations ---

type mockFSTransport struct {
	writtenCampfireID string
	writtenMember     campfire.MemberRecord
	called            bool
	err               error
}

func (m *mockFSTransport) WriteMember(campfireID string, member campfire.MemberRecord) error {
	m.called = true
	m.writtenCampfireID = campfireID
	m.writtenMember = member
	return m.err
}

type mockStore struct {
	memberships   []store.Membership
	peerEndpoints []store.PeerEndpoint
	membershipErr error
	peerEndpointErr error
	seenCampfireIDs map[string]bool
}

func (m *mockStore) AddMembership(ms store.Membership) error {
	if m.membershipErr != nil {
		return m.membershipErr
	}
	// Reject duplicates, mirroring the SQLiteStore PRIMARY KEY constraint.
	if m.seenCampfireIDs == nil {
		m.seenCampfireIDs = make(map[string]bool)
	}
	if m.seenCampfireIDs[ms.CampfireID] {
		return errors.New("mockStore: duplicate campfire_id")
	}
	m.seenCampfireIDs[ms.CampfireID] = true
	m.memberships = append(m.memberships, ms)
	return nil
}

func (m *mockStore) UpsertPeerEndpoint(e store.PeerEndpoint) error {
	if m.peerEndpointErr != nil {
		return m.peerEndpointErr
	}
	m.peerEndpoints = append(m.peerEndpoints, e)
	return nil
}

type mockHTTPTransport struct {
	addPeerCalls []addPeerCall
}

type addPeerCall struct {
	campfireID string
	pubKeyHex  string
	endpoint   string
}

func (m *mockHTTPTransport) AddPeer(campfireID, pubKeyHex, endpoint string) {
	m.addPeerCalls = append(m.addPeerCalls, addPeerCall{campfireID, pubKeyHex, endpoint})
}

// validPubKeyHex is a valid 32-byte hex-encoded public key for testing.
const validPubKeyHex = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

func baseRequest() admission.AdmissionRequest {
	return admission.AdmissionRequest{
		CampfireID:    "test-campfire-id",
		MemberPubKeyHex: validPubKeyHex,
		Endpoint:      "https://example.com/peer",
		Role:          "",
		Encrypted:     false,
		Source:        "test-source",
		ParticipantID: 1,
		JoinProtocol:  "v1",
		TransportDir:  "/tmp/transport",
		TransportType: "filesystem",
		Description:   "Test campfire",
		CreatorPubkey: "creator-pubkey",
	}
}

func TestAdmitMember_FullPath(t *testing.T) {
	fs := &mockFSTransport{}
	st := &mockStore{}
	http := &mockHTTPTransport{}

	deps := admission.AdmitterDeps{
		FSTransport:   fs,
		Store:         st,
		HTTPTransport: http,
		ExternalAddr:  "https://myserver.com",
	}
	req := baseRequest()

	result, err := admission.AdmitMember(context.Background(), deps, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.MemberFileWritten {
		t.Error("expected MemberFileWritten=true")
	}
	if !result.MembershipRecorded {
		t.Error("expected MembershipRecorded=true")
	}
	if !result.PeerEndpointRegistered {
		t.Error("expected PeerEndpointRegistered=true")
	}

	if !fs.called {
		t.Error("expected FSTransport.WriteMember to be called")
	}

	// Finding 2: assert WriteMember received the correct arguments.
	if fs.writtenCampfireID != req.CampfireID {
		t.Errorf("WriteMember campfireID: want %q, got %q", req.CampfireID, fs.writtenCampfireID)
	}
	wantPubKey, _ := hex.DecodeString(validPubKeyHex)
	if !bytes.Equal(fs.writtenMember.PublicKey, wantPubKey) {
		t.Errorf("WriteMember PublicKey: want %x, got %x", wantPubKey, fs.writtenMember.PublicKey)
	}
	if fs.writtenMember.Role != result.EffectiveRole {
		t.Errorf("WriteMember Role: want %q, got %q", result.EffectiveRole, fs.writtenMember.Role)
	}

	if len(st.memberships) != 1 {
		t.Errorf("expected 1 membership, got %d", len(st.memberships))
	}
	if len(st.peerEndpoints) != 1 {
		t.Errorf("expected 1 peer endpoint, got %d", len(st.peerEndpoints))
	}
	if len(http.addPeerCalls) != 1 {
		t.Errorf("expected 1 AddPeer call, got %d", len(http.addPeerCalls))
	}
}

func TestAdmitMember_NoFS(t *testing.T) {
	st := &mockStore{}
	http := &mockHTTPTransport{}

	deps := admission.AdmitterDeps{
		FSTransport:   nil,
		Store:         st,
		HTTPTransport: http,
	}
	req := baseRequest()

	result, err := admission.AdmitMember(context.Background(), deps, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.MemberFileWritten {
		t.Error("expected MemberFileWritten=false when FSTransport is nil")
	}
	if !result.MembershipRecorded {
		t.Error("expected MembershipRecorded=true")
	}
	if !result.PeerEndpointRegistered {
		t.Error("expected PeerEndpointRegistered=true")
	}
}

func TestAdmitMember_NoHTTP(t *testing.T) {
	fs := &mockFSTransport{}
	st := &mockStore{}

	deps := admission.AdmitterDeps{
		FSTransport:   fs,
		Store:         st,
		HTTPTransport: nil,
	}
	req := baseRequest()

	result, err := admission.AdmitMember(context.Background(), deps, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// PeerEndpointRegistered is still true — store gets the record
	if !result.PeerEndpointRegistered {
		t.Error("expected PeerEndpointRegistered=true even without HTTPTransport")
	}
	if len(st.peerEndpoints) != 1 {
		t.Errorf("expected peer endpoint in store, got %d", len(st.peerEndpoints))
	}
}

func TestAdmitMember_NoEndpoint(t *testing.T) {
	fs := &mockFSTransport{}
	st := &mockStore{}
	http := &mockHTTPTransport{}

	deps := admission.AdmitterDeps{
		FSTransport:   fs,
		Store:         st,
		HTTPTransport: http,
	}
	req := baseRequest()
	req.Endpoint = ""

	result, err := admission.AdmitMember(context.Background(), deps, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.PeerEndpointRegistered {
		t.Error("expected PeerEndpointRegistered=false when Endpoint is empty")
	}
	if len(st.peerEndpoints) != 0 {
		t.Errorf("expected no peer endpoint records, got %d", len(st.peerEndpoints))
	}
	if len(http.addPeerCalls) != 0 {
		t.Errorf("expected no AddPeer calls, got %d", len(http.addPeerCalls))
	}
}

func TestAdmitMember_EncryptedDefaultsToBlindRelay(t *testing.T) {
	st := &mockStore{}

	deps := admission.AdmitterDeps{
		Store: st,
	}
	req := baseRequest()
	req.Encrypted = true
	req.Role = ""
	req.Endpoint = "" // skip peer endpoint to keep it simple

	result, err := admission.AdmitMember(context.Background(), deps, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.EffectiveRole != campfire.RoleBlindRelay {
		t.Errorf("expected EffectiveRole=%q, got %q", campfire.RoleBlindRelay, result.EffectiveRole)
	}
}

func TestAdmitMember_ExplicitRoleOverridesEncrypted(t *testing.T) {
	st := &mockStore{}

	deps := admission.AdmitterDeps{
		Store: st,
	}
	req := baseRequest()
	req.Encrypted = true
	req.Role = campfire.RoleWriter
	req.Endpoint = ""

	result, err := admission.AdmitMember(context.Background(), deps, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.EffectiveRole != campfire.RoleWriter {
		t.Errorf("expected EffectiveRole=%q, got %q", campfire.RoleWriter, result.EffectiveRole)
	}
}

func TestAdmitMember_UnencryptedNoRoleDefaultsFull(t *testing.T) {
	st := &mockStore{}

	deps := admission.AdmitterDeps{
		Store: st,
	}
	req := baseRequest()
	req.Encrypted = false
	req.Role = ""
	req.Endpoint = ""

	result, err := admission.AdmitMember(context.Background(), deps, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.EffectiveRole != campfire.RoleFull {
		t.Errorf("expected EffectiveRole=%q, got %q", campfire.RoleFull, result.EffectiveRole)
	}
}

func TestAdmitMember_ErrorOnStoreFail(t *testing.T) {
	fs := &mockFSTransport{}
	st := &mockStore{membershipErr: errors.New("store failure")}
	http := &mockHTTPTransport{}

	deps := admission.AdmitterDeps{
		FSTransport:   fs,
		Store:         st,
		HTTPTransport: http,
	}
	req := baseRequest()

	_, err := admission.AdmitMember(context.Background(), deps, req)
	if err == nil {
		t.Fatal("expected error from store failure, got nil")
	}

	// Finding 3: document the partial-state orphan — WriteMember is called
	// before AddMembership, so a member file is written even when the store
	// fails. This is the current behavior; this assertion pins it so any
	// future change to the ordering is deliberate.
	if !fs.called {
		t.Error("expected FSTransport.WriteMember to have been called before store failure (orphan member file)")
	}

	// No peer endpoint should be registered after a membership error
	if len(st.peerEndpoints) != 0 {
		t.Errorf("expected no peer endpoints registered after membership error, got %d", len(st.peerEndpoints))
	}
}

func TestAdmitMember_DuplicateCampfireRejected(t *testing.T) {
	st := &mockStore{}

	deps := admission.AdmitterDeps{
		Store: st,
	}
	req := baseRequest()
	req.Endpoint = ""

	// First admission should succeed.
	_, err := admission.AdmitMember(context.Background(), deps, req)
	if err != nil {
		t.Fatalf("first admission: unexpected error: %v", err)
	}

	// Second admission with the same campfire_id must be rejected, mirroring
	// the SQLiteStore PRIMARY KEY constraint.
	_, err = admission.AdmitMember(context.Background(), deps, req)
	if err == nil {
		t.Fatal("second admission with duplicate campfire_id: expected error, got nil")
	}
}

func TestAdmitMember_MembershipFields(t *testing.T) {
	st := &mockStore{}

	deps := admission.AdmitterDeps{
		Store: st,
	}
	req := baseRequest()
	req.Endpoint = ""

	before := time.Now().Unix()
	_, err := admission.AdmitMember(context.Background(), deps, req)
	after := time.Now().Unix()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(st.memberships) != 1 {
		t.Fatalf("expected 1 membership, got %d", len(st.memberships))
	}
	m := st.memberships[0]
	if m.CampfireID != req.CampfireID {
		t.Errorf("CampfireID: want %q, got %q", req.CampfireID, m.CampfireID)
	}
	if m.TransportDir != req.TransportDir {
		t.Errorf("TransportDir: want %q, got %q", req.TransportDir, m.TransportDir)
	}
	if m.JoinProtocol != req.JoinProtocol {
		t.Errorf("JoinProtocol: want %q, got %q", req.JoinProtocol, m.JoinProtocol)
	}
	if m.TransportType != req.TransportType {
		t.Errorf("TransportType: want %q, got %q", req.TransportType, m.TransportType)
	}
	if m.Description != req.Description {
		t.Errorf("Description: want %q, got %q", req.Description, m.Description)
	}
	if m.CreatorPubkey != req.CreatorPubkey {
		t.Errorf("CreatorPubkey: want %q, got %q", req.CreatorPubkey, m.CreatorPubkey)
	}
	if m.Encrypted != req.Encrypted {
		t.Errorf("Encrypted: want %v, got %v", req.Encrypted, m.Encrypted)
	}
	if m.JoinedAt < before || m.JoinedAt > after {
		t.Errorf("JoinedAt %d out of range [%d, %d]", m.JoinedAt, before, after)
	}
}
