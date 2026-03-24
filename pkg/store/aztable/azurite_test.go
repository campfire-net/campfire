//go:build azurite

// Package aztable_test contains Azurite integration tests.
// Run with: go test -tags azurite ./pkg/store/aztable/...
//
// Prerequisites:
//   - Azurite must be running on localhost:10002
//   - docker run -p 10000:10000 -p 10001:10001 -p 10002:10002 mcr.microsoft.com/azure-storage/azurite
//
// See campfire-agent-<prerequisite-bead> for Azurite CI setup.
package aztable_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/store/aztable"
)

// azuriteConnStr is the well-known Azurite Table Storage connection string.
const azuriteConnStr = "DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;AccountKey=Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==;TableEndpoint=http://127.0.0.1:10002/devstoreaccount1;"

func newTestStore(t *testing.T) store.Store {
	t.Helper()
	s, err := aztable.NewTableStore(azuriteConnStr)
	if err != nil {
		t.Fatalf("NewTableStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestMembership exercises AddMembership, GetMembership, UpdateMembershipRole,
// ListMemberships, and RemoveMembership.
func TestMembership(t *testing.T) {
	s := newTestStore(t)
	id := fmt.Sprintf("cf-test-%d", time.Now().UnixNano())

	m := store.Membership{
		CampfireID:   id,
		TransportDir: "/tmp/test",
		JoinProtocol: "direct",
		Role:         "full",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
		Description:  "test campfire",
	}
	if err := s.AddMembership(m); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	got, err := s.GetMembership(id)
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if got == nil {
		t.Fatal("GetMembership returned nil")
	}
	if got.CampfireID != id {
		t.Errorf("CampfireID: got %q, want %q", got.CampfireID, id)
	}
	if got.Role != "full" {
		t.Errorf("Role: got %q, want full", got.Role)
	}

	if err := s.UpdateMembershipRole(id, "observer"); err != nil {
		t.Fatalf("UpdateMembershipRole: %v", err)
	}
	got2, err := s.GetMembership(id)
	if err != nil {
		t.Fatalf("GetMembership after update: %v", err)
	}
	if got2.Role != "observer" {
		t.Errorf("Role after update: got %q, want observer", got2.Role)
	}

	all, err := s.ListMemberships()
	if err != nil {
		t.Fatalf("ListMemberships: %v", err)
	}
	found := false
	for _, mm := range all {
		if mm.CampfireID == id {
			found = true
		}
	}
	if !found {
		t.Errorf("ListMemberships did not contain %s", id)
	}

	if err := s.RemoveMembership(id); err != nil {
		t.Fatalf("RemoveMembership: %v", err)
	}
	gone, err := s.GetMembership(id)
	if err != nil {
		t.Fatalf("GetMembership after remove: %v", err)
	}
	if gone != nil {
		t.Error("membership still present after remove")
	}
}

// TestMessages exercises AddMessage, HasMessage, GetMessage, ListMessages,
// MaxMessageTimestamp, and GetMessageByPrefix.
func TestMessages(t *testing.T) {
	s := newTestStore(t)
	cfID := fmt.Sprintf("cf-msg-%d", time.Now().UnixNano())

	// AddMembership is needed for downgrade check.
	mem := store.Membership{
		CampfireID:   cfID,
		TransportDir: "/tmp",
		JoinProtocol: "direct",
		Role:         "full",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}
	if err := s.AddMembership(mem); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	now := time.Now().UnixNano()
	rec := store.MessageRecord{
		ID:         fmt.Sprintf("msg-%d", now),
		CampfireID: cfID,
		Sender:     "aabbccdd",
		Payload:    []byte(`{"hello":"world"}`),
		Tags:       []string{"status"},
		Timestamp:  now,
		ReceivedAt: now,
		Signature:  []byte("sig"),
		Antecedents: []string{},
		Provenance: []message.ProvenanceHop{},
	}

	inserted, err := s.AddMessage(rec)
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if !inserted {
		t.Error("AddMessage: expected inserted=true")
	}

	// Duplicate insert should return false.
	inserted2, err := s.AddMessage(rec)
	if err != nil {
		t.Fatalf("AddMessage duplicate: %v", err)
	}
	if inserted2 {
		t.Error("AddMessage duplicate: expected inserted=false")
	}

	has, err := s.HasMessage(rec.ID)
	if err != nil {
		t.Fatalf("HasMessage: %v", err)
	}
	if !has {
		t.Error("HasMessage returned false for known message")
	}

	got, err := s.GetMessage(rec.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got == nil {
		t.Fatal("GetMessage returned nil")
	}
	if got.Sender != rec.Sender {
		t.Errorf("Sender: got %q, want %q", got.Sender, rec.Sender)
	}

	// GetMessageByPrefix.
	prefix := rec.ID[:8]
	byPrefix, err := s.GetMessageByPrefix(prefix)
	if err != nil {
		t.Fatalf("GetMessageByPrefix: %v", err)
	}
	if byPrefix == nil || byPrefix.ID != rec.ID {
		t.Errorf("GetMessageByPrefix: expected %s, got %v", rec.ID, byPrefix)
	}

	// ListMessages.
	msgs, err := s.ListMessages(cfID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) == 0 {
		t.Error("ListMessages returned empty")
	}

	// MaxMessageTimestamp.
	maxTS, err := s.MaxMessageTimestamp(cfID, 0)
	if err != nil {
		t.Fatalf("MaxMessageTimestamp: %v", err)
	}
	if maxTS == 0 {
		t.Error("MaxMessageTimestamp returned 0")
	}
}

// TestReadCursor exercises GetReadCursor and SetReadCursor.
func TestReadCursor(t *testing.T) {
	s := newTestStore(t)
	cfID := fmt.Sprintf("cf-cursor-%d", time.Now().UnixNano())

	cur, err := s.GetReadCursor(cfID)
	if err != nil {
		t.Fatalf("GetReadCursor (absent): %v", err)
	}
	if cur != 0 {
		t.Errorf("expected 0 for absent cursor, got %d", cur)
	}

	ts := time.Now().UnixNano()
	if err := s.SetReadCursor(cfID, ts); err != nil {
		t.Fatalf("SetReadCursor: %v", err)
	}

	got, err := s.GetReadCursor(cfID)
	if err != nil {
		t.Fatalf("GetReadCursor after set: %v", err)
	}
	if got != ts {
		t.Errorf("cursor: got %d, want %d", got, ts)
	}
}

// TestPeerEndpoints exercises UpsertPeerEndpoint, ListPeerEndpoints, GetPeerRole,
// and DeletePeerEndpoint.
func TestPeerEndpoints(t *testing.T) {
	s := newTestStore(t)
	cfID := fmt.Sprintf("cf-peer-%d", time.Now().UnixNano())
	pubkey := "aabbccddeeff0011"

	ep := store.PeerEndpoint{
		CampfireID:   cfID,
		MemberPubkey: pubkey,
		Endpoint:     "https://example.com",
		Role:         store.PeerRoleCreator,
	}
	if err := s.UpsertPeerEndpoint(ep); err != nil {
		t.Fatalf("UpsertPeerEndpoint: %v", err)
	}

	role, err := s.GetPeerRole(cfID, pubkey)
	if err != nil {
		t.Fatalf("GetPeerRole: %v", err)
	}
	if role != store.PeerRoleCreator {
		t.Errorf("role: got %q, want %q", role, store.PeerRoleCreator)
	}

	list, err := s.ListPeerEndpoints(cfID)
	if err != nil {
		t.Fatalf("ListPeerEndpoints: %v", err)
	}
	if len(list) != 1 || list[0].MemberPubkey != pubkey {
		t.Errorf("ListPeerEndpoints: unexpected result %v", list)
	}

	// Default role for unknown peer.
	defRole, err := s.GetPeerRole(cfID, "unknown")
	if err != nil {
		t.Fatalf("GetPeerRole unknown: %v", err)
	}
	if defRole != store.PeerRoleMember {
		t.Errorf("default role: got %q, want %q", defRole, store.PeerRoleMember)
	}

	if err := s.DeletePeerEndpoint(cfID, pubkey); err != nil {
		t.Fatalf("DeletePeerEndpoint: %v", err)
	}
	list2, err := s.ListPeerEndpoints(cfID)
	if err != nil {
		t.Fatalf("ListPeerEndpoints after delete: %v", err)
	}
	if len(list2) != 0 {
		t.Errorf("expected empty after delete, got %v", list2)
	}
}

// TestThresholdShares exercises UpsertThresholdShare and GetThresholdShare.
func TestThresholdShares(t *testing.T) {
	s := newTestStore(t)
	cfID := fmt.Sprintf("cf-thresh-%d", time.Now().UnixNano())

	absent, err := s.GetThresholdShare(cfID)
	if err != nil {
		t.Fatalf("GetThresholdShare absent: %v", err)
	}
	if absent != nil {
		t.Error("expected nil for absent threshold share")
	}

	share := store.ThresholdShare{
		CampfireID:    cfID,
		ParticipantID: 1,
		SecretShare:   []byte("secret-share-data"),
		PublicData:    []byte("public-data"),
	}
	if err := s.UpsertThresholdShare(share); err != nil {
		t.Fatalf("UpsertThresholdShare: %v", err)
	}

	got, err := s.GetThresholdShare(cfID)
	if err != nil {
		t.Fatalf("GetThresholdShare: %v", err)
	}
	if got == nil {
		t.Fatal("GetThresholdShare returned nil")
	}
	if got.ParticipantID != 1 {
		t.Errorf("ParticipantID: got %d, want 1", got.ParticipantID)
	}
	if string(got.SecretShare) != "secret-share-data" {
		t.Errorf("SecretShare: got %q, want %q", got.SecretShare, "secret-share-data")
	}
}

// TestPendingThresholdShares exercises StorePendingThresholdShare and ClaimPendingThresholdShare.
func TestPendingThresholdShares(t *testing.T) {
	s := newTestStore(t)
	cfID := fmt.Sprintf("cf-pending-%d", time.Now().UnixNano())

	// Claim from empty returns (0, nil, nil).
	pid, data, err := s.ClaimPendingThresholdShare(cfID)
	if err != nil {
		t.Fatalf("ClaimPendingThresholdShare empty: %v", err)
	}
	if pid != 0 || data != nil {
		t.Errorf("expected (0, nil), got (%d, %v)", pid, data)
	}

	// Store two shares.
	if err := s.StorePendingThresholdShare(cfID, 2, []byte("share-2")); err != nil {
		t.Fatalf("StorePendingThresholdShare 2: %v", err)
	}
	if err := s.StorePendingThresholdShare(cfID, 1, []byte("share-1")); err != nil {
		t.Fatalf("StorePendingThresholdShare 1: %v", err)
	}

	// Claim should return lowest participant ID first.
	pid1, data1, err := s.ClaimPendingThresholdShare(cfID)
	if err != nil {
		t.Fatalf("ClaimPendingThresholdShare 1: %v", err)
	}
	if pid1 != 1 {
		t.Errorf("expected participant 1, got %d", pid1)
	}
	if string(data1) != "share-1" {
		t.Errorf("expected share-1, got %q", data1)
	}

	pid2, data2, err := s.ClaimPendingThresholdShare(cfID)
	if err != nil {
		t.Fatalf("ClaimPendingThresholdShare 2: %v", err)
	}
	if pid2 != 2 {
		t.Errorf("expected participant 2, got %d", pid2)
	}
	if string(data2) != "share-2" {
		t.Errorf("expected share-2, got %q", data2)
	}

	// Now empty.
	pid3, data3, err := s.ClaimPendingThresholdShare(cfID)
	if err != nil {
		t.Fatalf("ClaimPendingThresholdShare after empty: %v", err)
	}
	if pid3 != 0 || data3 != nil {
		t.Errorf("expected empty, got (%d, %v)", pid3, data3)
	}
}

// TestEpochSecrets exercises UpsertEpochSecret, GetEpochSecret, and GetLatestEpochSecret.
func TestEpochSecrets(t *testing.T) {
	s := newTestStore(t)
	cfID := fmt.Sprintf("cf-epoch-%d", time.Now().UnixNano())

	absent, err := s.GetEpochSecret(cfID, 0)
	if err != nil {
		t.Fatalf("GetEpochSecret absent: %v", err)
	}
	if absent != nil {
		t.Error("expected nil for absent epoch secret")
	}

	for i := uint64(1); i <= 3; i++ {
		es := store.EpochSecret{
			CampfireID: cfID,
			Epoch:      i,
			RootSecret: []byte(fmt.Sprintf("root-%d", i)),
			CEK:        []byte(fmt.Sprintf("cek-%d", i)),
			CreatedAt:  time.Now().UnixNano(),
		}
		if err := s.UpsertEpochSecret(es); err != nil {
			t.Fatalf("UpsertEpochSecret %d: %v", i, err)
		}
	}

	got, err := s.GetEpochSecret(cfID, 2)
	if err != nil {
		t.Fatalf("GetEpochSecret 2: %v", err)
	}
	if got == nil || got.Epoch != 2 {
		t.Errorf("GetEpochSecret 2: got %v", got)
	}

	latest, err := s.GetLatestEpochSecret(cfID)
	if err != nil {
		t.Fatalf("GetLatestEpochSecret: %v", err)
	}
	if latest == nil || latest.Epoch != 3 {
		t.Errorf("GetLatestEpochSecret: expected epoch 3, got %v", latest)
	}
}

// TestSetMembershipEncrypted exercises the encrypted flag.
func TestSetMembershipEncrypted(t *testing.T) {
	s := newTestStore(t)
	cfID := fmt.Sprintf("cf-enc-%d", time.Now().UnixNano())

	mem := store.Membership{
		CampfireID:   cfID,
		TransportDir: "/tmp",
		JoinProtocol: "direct",
		Role:         "full",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}
	if err := s.AddMembership(mem); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	if err := s.SetMembershipEncrypted(cfID, true); err != nil {
		t.Fatalf("SetMembershipEncrypted true: %v", err)
	}

	got, err := s.GetMembership(cfID)
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if !got.Encrypted {
		t.Error("expected Encrypted=true")
	}

	if err := s.SetMembershipEncrypted(cfID, false); err != nil {
		t.Fatalf("SetMembershipEncrypted false: %v", err)
	}
	got2, err := s.GetMembership(cfID)
	if err != nil {
		t.Fatalf("GetMembership after false: %v", err)
	}
	if got2.Encrypted {
		t.Error("expected Encrypted=false")
	}
}

// TestApplyMembershipCommitAtomically exercises atomic commit.
func TestApplyMembershipCommitAtomically(t *testing.T) {
	s := newTestStore(t)
	cfID := fmt.Sprintf("cf-atomic-%d", time.Now().UnixNano())

	newMem := store.Membership{
		CampfireID:   cfID,
		TransportDir: "/tmp",
		JoinProtocol: "direct",
		Role:         "full",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}
	secret := store.EpochSecret{
		CampfireID: cfID,
		Epoch:      1,
		RootSecret: []byte("root"),
		CEK:        []byte("cek"),
		CreatedAt:  time.Now().UnixNano(),
	}
	if err := s.ApplyMembershipCommitAtomically(cfID, &newMem, secret); err != nil {
		t.Fatalf("ApplyMembershipCommitAtomically: %v", err)
	}

	// Both records should be visible.
	m, err := s.GetMembership(cfID)
	if err != nil || m == nil {
		t.Fatalf("GetMembership after atomic commit: %v, %v", m, err)
	}
	es, err := s.GetEpochSecret(cfID, 1)
	if err != nil || es == nil {
		t.Fatalf("GetEpochSecret after atomic commit: %v, %v", es, err)
	}
}

// TestUpdateCampfireID exercises the rename operation.
func TestUpdateCampfireID(t *testing.T) {
	s := newTestStore(t)
	oldID := fmt.Sprintf("cf-old-%d", time.Now().UnixNano())
	newID := fmt.Sprintf("cf-new-%d", time.Now().UnixNano())

	mem := store.Membership{
		CampfireID:   oldID,
		TransportDir: "/tmp",
		JoinProtocol: "direct",
		Role:         "full",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}
	if err := s.AddMembership(mem); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	if err := s.UpdateCampfireID(oldID, newID); err != nil {
		t.Fatalf("UpdateCampfireID: %v", err)
	}

	// Old ID should be gone.
	old, err := s.GetMembership(oldID)
	if err != nil {
		t.Fatalf("GetMembership old: %v", err)
	}
	if old != nil {
		t.Errorf("old membership still present after rename")
	}

	// New ID should exist.
	newM, err := s.GetMembership(newID)
	if err != nil {
		t.Fatalf("GetMembership new: %v", err)
	}
	if newM == nil {
		t.Error("new membership not found after rename")
	}
}

// TestLargePayload verifies chunking for payloads larger than chunkSize.
func TestLargePayload(t *testing.T) {
	s := newTestStore(t)
	cfID := fmt.Sprintf("cf-large-%d", time.Now().UnixNano())

	mem := store.Membership{
		CampfireID:   cfID,
		TransportDir: "/tmp",
		JoinProtocol: "direct",
		Role:         "full",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}
	if err := s.AddMembership(mem); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	// Create a payload larger than 60 KB.
	payload := make([]byte, 150*1024)
	for i := range payload {
		payload[i] = byte(i % 251)
	}

	now := time.Now().UnixNano()
	rec := store.MessageRecord{
		ID:          fmt.Sprintf("large-msg-%d", now),
		CampfireID:  cfID,
		Sender:      "aabbccdd",
		Payload:     payload,
		Tags:        []string{"status"},
		Timestamp:   now,
		ReceivedAt:  now,
		Signature:   []byte("sig"),
		Antecedents: []string{},
		Provenance:  []message.ProvenanceHop{},
	}

	inserted, err := s.AddMessage(rec)
	if err != nil {
		t.Fatalf("AddMessage large: %v", err)
	}
	if !inserted {
		t.Error("expected inserted=true")
	}

	got, err := s.GetMessage(rec.ID)
	if err != nil {
		t.Fatalf("GetMessage large: %v", err)
	}
	if got == nil {
		t.Fatal("GetMessage returned nil")
	}
	if len(got.Payload) != len(payload) {
		t.Errorf("payload length: got %d, want %d", len(got.Payload), len(payload))
	}
	for i := range payload {
		if got.Payload[i] != payload[i] {
			t.Errorf("payload byte %d mismatch: got %d, want %d", i, got.Payload[i], payload[i])
			break
		}
	}
}

// TestValidateAndUseInvite_Basic verifies that ValidateAndUseInvite returns the
// invite record and increments UseCount on a successful call.
func TestValidateAndUseInvite_Basic(t *testing.T) {
	s := newTestStore(t)
	cfID := fmt.Sprintf("cf-invite-basic-%d", time.Now().UnixNano())
	code := fmt.Sprintf("code-basic-%d", time.Now().UnixNano())

	inv := store.InviteRecord{
		CampfireID: cfID,
		InviteCode: code,
		CreatedBy:  "test-agent",
		CreatedAt:  time.Now().UnixNano(),
		MaxUses:    3,
		UseCount:   0,
	}
	if err := s.CreateInvite(inv); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	got, err := s.ValidateAndUseInvite(cfID, code)
	if err != nil {
		t.Fatalf("ValidateAndUseInvite: %v", err)
	}
	if got == nil {
		t.Fatal("ValidateAndUseInvite returned nil")
	}
	if got.UseCount != 1 {
		t.Errorf("UseCount: got %d, want 1", got.UseCount)
	}
	if got.CampfireID != cfID {
		t.Errorf("CampfireID: got %q, want %q", got.CampfireID, cfID)
	}

	// Second use should also succeed and increment to 2.
	got2, err := s.ValidateAndUseInvite(cfID, code)
	if err != nil {
		t.Fatalf("ValidateAndUseInvite second: %v", err)
	}
	if got2.UseCount != 2 {
		t.Errorf("UseCount second: got %d, want 2", got2.UseCount)
	}
}

// TestValidateAndUseInvite_ExhaustedReturnsError verifies that calling
// ValidateAndUseInvite on a fully-used invite returns ErrInviteExhausted.
func TestValidateAndUseInvite_ExhaustedReturnsError(t *testing.T) {
	s := newTestStore(t)
	cfID := fmt.Sprintf("cf-invite-exhaust-%d", time.Now().UnixNano())
	code := fmt.Sprintf("code-exhaust-%d", time.Now().UnixNano())

	inv := store.InviteRecord{
		CampfireID: cfID,
		InviteCode: code,
		CreatedBy:  "test-agent",
		CreatedAt:  time.Now().UnixNano(),
		MaxUses:    1,
		UseCount:   1, // already exhausted
	}
	if err := s.CreateInvite(inv); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	_, err := s.ValidateAndUseInvite(cfID, code)
	if err == nil {
		t.Fatal("expected ErrInviteExhausted, got nil")
	}
	if err != store.ErrInviteExhausted {
		t.Errorf("expected ErrInviteExhausted, got: %v", err)
	}
}

// TestValidateAndUseInvite_UnlimitedMaxUses verifies that an invite with
// MaxUses==0 is never treated as exhausted regardless of UseCount.
func TestValidateAndUseInvite_UnlimitedMaxUses(t *testing.T) {
	s := newTestStore(t)
	cfID := fmt.Sprintf("cf-invite-unlimited-%d", time.Now().UnixNano())
	code := fmt.Sprintf("code-unlimited-%d", time.Now().UnixNano())

	inv := store.InviteRecord{
		CampfireID: cfID,
		InviteCode: code,
		CreatedBy:  "test-agent",
		CreatedAt:  time.Now().UnixNano(),
		MaxUses:    0, // unlimited
		UseCount:   999,
	}
	if err := s.CreateInvite(inv); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	got, err := s.ValidateAndUseInvite(cfID, code)
	if err != nil {
		t.Fatalf("ValidateAndUseInvite unlimited: %v", err)
	}
	if got == nil {
		t.Fatal("ValidateAndUseInvite returned nil")
	}
	if got.UseCount != 1000 {
		t.Errorf("UseCount: got %d, want 1000", got.UseCount)
	}
}

// TestValidateAndUseInvite_Concurrent verifies that concurrent calls to
// ValidateAndUseInvite on the same invite do not corrupt the use count.
// Each goroutine increments once; the final count must equal the number of
// successful calls (some may get ErrInviteExhausted if MaxUses is reached).
func TestValidateAndUseInvite_Concurrent(t *testing.T) {
	s := newTestStore(t)
	cfID := fmt.Sprintf("cf-invite-concurrent-%d", time.Now().UnixNano())
	code := fmt.Sprintf("code-concurrent-%d", time.Now().UnixNano())

	const maxUses = 5
	const goroutines = 10

	inv := store.InviteRecord{
		CampfireID: cfID,
		InviteCode: code,
		CreatedBy:  "test-agent",
		CreatedAt:  time.Now().UnixNano(),
		MaxUses:    maxUses,
		UseCount:   0,
	}
	if err := s.CreateInvite(inv); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	type result struct {
		inv *store.InviteRecord
		err error
	}
	results := make(chan result, goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			inv, err := s.ValidateAndUseInvite(cfID, code)
			results <- result{inv, err}
		}()
	}

	successCount := 0
	exhaustedCount := 0
	for i := 0; i < goroutines; i++ {
		r := <-results
		if r.err == nil {
			successCount++
		} else if r.err == store.ErrInviteExhausted {
			exhaustedCount++
		} else {
			t.Errorf("unexpected error: %v", r.err)
		}
	}

	if successCount != maxUses {
		t.Errorf("successCount: got %d, want %d", successCount, maxUses)
	}
	if successCount+exhaustedCount != goroutines {
		t.Errorf("total calls: got %d, want %d", successCount+exhaustedCount, goroutines)
	}
}
