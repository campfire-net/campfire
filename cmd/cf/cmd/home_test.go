package cmd

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	cfidentity "github.com/campfire-net/campfire/cf-conventions/cf-convention-extension/identity"
	cfencoding "github.com/campfire-net/campfire/cf-protocol/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/cf-protocol/message"
	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
)

// setupHomeLinkEnv creates a CF_HOME with one agent and two campfires (A and B).
// Sets the "home" alias to campfire A.
// Returns: agentID, storeS, cfHomeDir, campfireAID, campfireBID.
func setupHomeLinkEnv(t *testing.T) (*identity.Identity, store.Store, string, string, string) {
	t.Helper()

	cfHomeDir := t.TempDir()
	transportBaseDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)

	// Generate agent identity.
	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating agent identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	// Open store.
	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Create campfire A.
	campfireAID := createTestCampfire(t, agentID, s, transportBaseDir)
	// Create campfire B.
	campfireBID := createTestCampfire(t, agentID, s, transportBaseDir)

	// Set "home" alias to campfire A.
	aliasStore := naming.NewAliasStore(cfHomeDir)
	if err := aliasStore.Set("home", campfireAID); err != nil {
		t.Fatalf("setting home alias: %v", err)
	}

	return agentID, s, cfHomeDir, campfireAID, campfireBID
}

// createTestCampfire creates a filesystem campfire with the agent as creator.
// Returns the campfire ID.
func createTestCampfire(t *testing.T, agentID *identity.Identity, s store.Store, transportBaseDir string) string {
	t.Helper()

	// Generate campfire keypair.
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire keypair: %v", err)
	}
	campfireID := hex.EncodeToString(cfPub)

	// Create campfire directory structure.
	cfDir := filepath.Join(transportBaseDir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0755); err != nil {
			t.Fatalf("creating directory: %v", err)
		}
	}

	// Write campfire state.
	state := &campfire.CampfireState{
		PublicKey:             cfPub,
		PrivateKey:            cfPriv,
		JoinProtocol:          "invite-only",
		ReceptionRequirements: []string{},
		CreatedAt:             time.Now().UnixNano(),
		Threshold:             1,
	}
	stateData, err := cfencoding.Marshal(state)
	if err != nil {
		t.Fatalf("marshalling state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfDir, "campfire.cbor"), stateData, 0644); err != nil {
		t.Fatalf("writing campfire state: %v", err)
	}

	// Write agent member record.
	tr := fs.New(transportBaseDir)
	if err := tr.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("writing member record: %v", err)
	}

	// Add membership to store.
	if err := s.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  tr.CampfireDir(campfireID),
		JoinProtocol:  "invite-only",
		Role:          "creator",
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	return campfireID
}

// runHomeLinkCmd executes `cf home link <campfireBID>` and returns stdout/stderr and error.
func runHomeLinkCmd(t *testing.T, cfHomeDir, campfireBID string) (string, error) {
	t.Helper()

	var buf bytes.Buffer
	homeLinkCmd.ResetFlags()
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)

	t.Setenv("CF_HOME", cfHomeDir)
	rootCmd.SetArgs([]string{"home", "link", campfireBID})
	err := rootCmd.Execute()

	rootCmd.SetOut(nil)
	rootCmd.SetErr(nil)
	return buf.String(), err
}

// TestHomeLinkCmd_EchoCeremony is the integration test for the full echo ceremony.
// It:
//  1. Creates two campfires A and B.
//  2. Runs `cf home link <B>` which executes all four ceremony steps.
//  3. Verifies all three messages (M_A, M_B, echo) exist in both campfires.
//  4. Verifies the echo signature is valid against campfire B's public key.
//  5. Verifies third-party verification: list-homes semantics (messages tagged correctly).
func TestHomeLinkCmd_EchoCeremony(t *testing.T) {
	agentID, s, cfHomeDir, campfireAID, campfireBID := setupHomeLinkEnv(t)
	_ = agentID

	stdout, err := runHomeLinkCmd(t, cfHomeDir, campfireBID)
	if err != nil {
		t.Fatalf("home link command failed: %v\nOutput: %s", err, stdout)
	}

	// Verify step output messages.
	for _, want := range []string{"step 1:", "step 2:", "step 3:", "step 4:", "home link complete"} {
		if !bytes.Contains([]byte(stdout), []byte(want)) {
			t.Errorf("output missing %q\nGot: %s", want, stdout)
		}
	}

	// Get membership info to access transport dirs.
	mA, err := s.GetMembership(campfireAID)
	if err != nil || mA == nil {
		t.Fatalf("getting campfire A membership: %v", err)
	}
	mB, err := s.GetMembership(campfireBID)
	if err != nil || mB == nil {
		t.Fatalf("getting campfire B membership: %v", err)
	}

	trA := fs.ForDir(mA.TransportDir)
	trB := fs.ForDir(mB.TransportDir)

	// Read messages from campfire A.
	msgsA, err := trA.ListMessages(campfireAID)
	if err != nil {
		t.Fatalf("reading messages from campfire A: %v", err)
	}

	// Read messages from campfire B.
	msgsB, err := trB.ListMessages(campfireBID)
	if err != nil {
		t.Fatalf("reading messages from campfire B: %v", err)
	}

	// Campfire A should have: M_A (declare-home) and echo message.
	var mAMsg, echoMsg *message.Message
	for i := range msgsA {
		m := &msgsA[i]
		for _, tag := range m.Tags {
			if tag == cfidentity.IdentityHomeDeclaredTag {
				mAMsg = m
			}
			if tag == cfidentity.IdentityHomeEchoTag {
				echoMsg = m
			}
		}
	}
	if mAMsg == nil {
		t.Fatal("campfire A: missing declare-home message (M_A)")
	}
	if echoMsg == nil {
		t.Fatal("campfire A: missing echo message")
	}

	// Campfire B should have: M_B (declare-home).
	var mBMsg *message.Message
	for i := range msgsB {
		m := &msgsB[i]
		for _, tag := range m.Tags {
			if tag == cfidentity.IdentityHomeDeclaredTag {
				mBMsg = m
			}
		}
	}
	if mBMsg == nil {
		t.Fatal("campfire B: missing declare-home message (M_B)")
	}

	// Verify M_A payload references campfire B.
	var mAPayload map[string]string
	if err := json.Unmarshal(mAMsg.Payload, &mAPayload); err != nil {
		t.Fatalf("parsing M_A payload: %v", err)
	}
	if mAPayload["campfire_id"] != campfireBID {
		t.Errorf("M_A campfire_id: want %s, got %s", campfireBID, mAPayload["campfire_id"])
	}
	if mAPayload["role"] != homeLinkRole {
		t.Errorf("M_A role: want %q, got %q", homeLinkRole, mAPayload["role"])
	}

	// Verify M_B payload references campfire A and M_A.
	var mBPayload map[string]string
	if err := json.Unmarshal(mBMsg.Payload, &mBPayload); err != nil {
		t.Fatalf("parsing M_B payload: %v", err)
	}
	if mBPayload["campfire_id"] != campfireAID {
		t.Errorf("M_B campfire_id: want %s, got %s", campfireAID, mBPayload["campfire_id"])
	}
	if mBPayload["ref_message_id"] != mAMsg.ID {
		t.Errorf("M_B ref_message_id: want %s, got %s", mAMsg.ID, mBPayload["ref_message_id"])
	}

	// Verify echo message payload and signature.
	var echoPayload map[string]string
	if err := json.Unmarshal(echoMsg.Payload, &echoPayload); err != nil {
		t.Fatalf("parsing echo payload: %v", err)
	}
	if echoPayload["echo_of"] != mBMsg.ID {
		t.Errorf("echo echo_of: want %s, got %s", mBMsg.ID, echoPayload["echo_of"])
	}

	// Verify signed_by_b against campfire B's public key.
	signedByBHex := echoPayload["signed_by_b"]
	if signedByBHex == "" {
		t.Fatal("echo payload missing signed_by_b")
	}
	signedByB, err := hex.DecodeString(signedByBHex)
	if err != nil {
		t.Fatalf("decoding signed_by_b: %v", err)
	}

	// Get campfire B's public key from its state.
	stateB, err := trB.ReadState(campfireBID)
	if err != nil {
		t.Fatalf("reading campfire B state: %v", err)
	}
	campfireBPubKey := ed25519.PublicKey(stateB.PublicKey)

	// The signature is over M_B's ID bytes.
	mBIDBytes := []byte(mBMsg.ID)
	if !ed25519.Verify(campfireBPubKey, mBIDBytes, signedByB) {
		t.Error("echo signed_by_b: signature verification FAILED against campfire B public key")
	} else {
		t.Log("echo signed_by_b: signature verified against campfire B public key ✓")
	}

	// Third-party verification: list-homes on A shows B, list-homes on B shows A.
	// (We verify this by checking the messages carry the correct campfire IDs.)
	// list-homes semantics: read messages tagged identity:home-declared.
	aShowsB := mAPayload["campfire_id"] == campfireBID
	bShowsA := mBPayload["campfire_id"] == campfireAID
	if !aShowsB {
		t.Errorf("list-homes on A should show B: %s", campfireBID[:12])
	}
	if !bShowsA {
		t.Errorf("list-homes on B should show A: %s", campfireAID[:12])
	}
	if aShowsB && bShowsA {
		t.Log("third-party verification: list-homes on A shows B, list-homes on B shows A ✓")
	}
}

// TestHomeLinkCmd_SameCampfire verifies that linking a campfire to itself is rejected.
func TestHomeLinkCmd_SameCampfire(t *testing.T) {
	_, _, cfHomeDir, campfireAID, _ := setupHomeLinkEnv(t)

	_, err := runHomeLinkCmd(t, cfHomeDir, campfireAID)
	if err == nil {
		t.Error("expected error when linking campfire to itself, got nil")
	}
}

// TestHomeLinkCmd_NotMemberOfB verifies that linking fails when the agent is not a member of campfire B.
func TestHomeLinkCmd_NotMemberOfB(t *testing.T) {
	_, _, cfHomeDir, _, _ := setupHomeLinkEnv(t)

	// Use a fake campfire ID the agent is not a member of.
	fakeCampfireID := "abcd1234" + "0000000000000000000000000000000000000000000000000000000000000000"
	fakeCampfireID = fakeCampfireID[:64] // ensure 64 hex chars

	_, err := runHomeLinkCmd(t, cfHomeDir, fakeCampfireID)
	if err == nil {
		t.Error("expected error when not a member of campfire B, got nil")
	}
}

// TestHomeDisplay_ShowsCurrentIdentity verifies that homeDisplayCmd prints the machine key.
func TestHomeDisplay_ShowsCurrentIdentity(t *testing.T) {
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)

	// Generate and save agent identity.
	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	var buf bytes.Buffer
	homeDisplayCmd.ResetFlags()
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs([]string{"home", "display"})
	err = rootCmd.Execute()
	rootCmd.SetOut(nil)
	rootCmd.SetErr(nil)

	if err != nil {
		t.Fatalf("home display command failed: %v\nOutput: %s", err, buf.String())
	}

	out := buf.String()
	if !bytes.Contains([]byte(out), []byte("Machine key:")) {
		t.Errorf("output missing 'Machine key:': %s", out)
	}
}

// createTestCampfireNoPrivKey creates a campfire membership without a local private key,
// simulating a remote campfire the agent is a member of but does not control.
func createTestCampfireNoPrivKey(t *testing.T, agentID *identity.Identity, s store.Store, transportBaseDir string) string {
	t.Helper()

	// Generate campfire keypair — but only store the public key in the state.
	cfPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire keypair: %v", err)
	}
	campfireID := hex.EncodeToString(cfPub)

	// Create campfire directory structure.
	cfDir := filepath.Join(transportBaseDir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0755); err != nil {
			t.Fatalf("creating directory: %v", err)
		}
	}

	// Write campfire state WITHOUT private key (remote campfire — we don't control it).
	state := &campfire.CampfireState{
		PublicKey:             cfPub,
		PrivateKey:            nil, // no private key — caller does not hold it
		JoinProtocol:          "invite-only",
		ReceptionRequirements: []string{},
		CreatedAt:             time.Now().UnixNano(),
		Threshold:             1,
	}
	stateData, err := cfencoding.Marshal(state)
	if err != nil {
		t.Fatalf("marshalling state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfDir, "campfire.cbor"), stateData, 0644); err != nil {
		t.Fatalf("writing campfire state: %v", err)
	}

	// Write agent member record.
	tr := fs.New(transportBaseDir)
	if err := tr.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("writing member record: %v", err)
	}

	// Add membership to store.
	if err := s.AddMembership(store.Membership{
		CampfireID:    campfireID,
		TransportDir:  tr.CampfireDir(campfireID),
		JoinProtocol:  "invite-only",
		Role:          "full", // member role, not creator
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	return campfireID
}

// TestHomeBe_RequiresLocalKey verifies that checkLocalCampfireKey returns an error
// when campfire B has no local private key (remote campfire the caller does not control).
func TestHomeBe_RequiresLocalKey(t *testing.T) {
	agentID, s, cfHomeDir, _, _ := setupHomeLinkEnv(t)
	t.Setenv("CF_HOME", cfHomeDir)

	// Create a campfire without a private key (remote — caller does not control it).
	transportBaseDir := t.TempDir()
	remoteCampfireID := createTestCampfireNoPrivKey(t, agentID, s, transportBaseDir)

	// checkLocalCampfireKey should return an error for a campfire with no private key.
	err := checkLocalCampfireKey(s, remoteCampfireID)
	if err == nil {
		t.Error("expected checkLocalCampfireKey to return error for campfire without private key, got nil")
	}

	// Also verify it succeeds for a campfire with a local private key.
	localTransportBaseDir := t.TempDir()
	localCampfireID := createTestCampfire(t, agentID, s, localTransportBaseDir)
	err = checkLocalCampfireKey(s, localCampfireID)
	if err != nil {
		t.Errorf("expected checkLocalCampfireKey to succeed for campfire with private key, got: %v", err)
	}
}

// TestHomeRevoke_RequiresAdminRole verifies that homeRevokeCmd returns an error
// when the caller does not have admin or creator role in the home campfire.
func TestHomeRevoke_RequiresAdminRole(t *testing.T) {
	agentID, s, cfHomeDir, _, _ := setupHomeLinkEnv(t)
	_ = agentID

	// Create a new campfire where the agent has "full" (non-admin) role,
	// and set it as the home alias — simulating a campfire the agent joined
	// but does not administrate.
	transportBaseDir := t.TempDir()

	// Create a campfire with non-admin role.
	cfPub, cfPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating campfire keypair: %v", err)
	}
	nonAdminCampfireID := hex.EncodeToString(cfPub)
	cfDir := filepath.Join(transportBaseDir, nonAdminCampfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0755); err != nil {
			t.Fatalf("creating directory: %v", err)
		}
	}
	state := &campfire.CampfireState{
		PublicKey:             cfPub,
		PrivateKey:            cfPriv,
		JoinProtocol:          "invite-only",
		ReceptionRequirements: []string{},
		CreatedAt:             time.Now().UnixNano(),
		Threshold:             1,
	}
	stateData, err := cfencoding.Marshal(state)
	if err != nil {
		t.Fatalf("marshalling state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfDir, "campfire.cbor"), stateData, 0644); err != nil {
		t.Fatalf("writing campfire state: %v", err)
	}
	tr := fs.New(transportBaseDir)
	if err := tr.WriteMember(nonAdminCampfireID, campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("writing member record: %v", err)
	}
	// Store membership with a non-admin role ("full" = regular member).
	if err := s.AddMembership(store.Membership{
		CampfireID:    nonAdminCampfireID,
		TransportDir:  tr.CampfireDir(nonAdminCampfireID),
		JoinProtocol:  "invite-only",
		Role:          "full", // NOT creator or admin
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	// Override the home alias to point to the non-admin campfire.
	aliasStore := naming.NewAliasStore(cfHomeDir)
	if err := aliasStore.Set("home", nonAdminCampfireID); err != nil {
		t.Fatalf("setting home alias: %v", err)
	}

	// Generate a fake key to revoke.
	fakePub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating fake key: %v", err)
	}
	fakeKeyHex := hex.EncodeToString(fakePub)

	var buf bytes.Buffer
	homeRevokeCmd.ResetFlags()
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)

	t.Setenv("CF_HOME", cfHomeDir)
	rootCmd.SetArgs([]string{"home", "revoke", fakeKeyHex})
	err = rootCmd.Execute()
	rootCmd.SetOut(nil)
	rootCmd.SetErr(nil)

	if err == nil {
		t.Errorf("expected homeRevokeCmd to return error for non-admin caller, got nil\nOutput: %s", buf.String())
	}
}

// TestHomeRevoke_PostsRevokeMessage verifies that homeRevokeCmd posts an identity:revoked message.
func TestHomeRevoke_PostsRevokeMessage(t *testing.T) {
	agentID, s, cfHomeDir, campfireAID, _ := setupHomeLinkEnv(t)
	_ = agentID

	// Get the store for message verification.
	mA, err := s.GetMembership(campfireAID)
	if err != nil || mA == nil {
		t.Fatalf("getting campfire A membership: %v", err)
	}
	trA := fs.ForDir(mA.TransportDir)

	// Generate a fake key to revoke.
	fakePub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generating fake key: %v", err)
	}
	fakeKeyHex := hex.EncodeToString(fakePub)

	// Write a fake member record so Evict() finds the member.
	tr := fs.New(filepath.Dir(mA.TransportDir))
	if err := tr.WriteMember(campfireAID, campfire.MemberRecord{
		PublicKey: fakePub,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("writing fake member record: %v", err)
	}

	var buf bytes.Buffer
	homeRevokeCmd.ResetFlags()
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)

	t.Setenv("CF_HOME", cfHomeDir)
	rootCmd.SetArgs([]string{"home", "revoke", fakeKeyHex})
	err = rootCmd.Execute()
	rootCmd.SetOut(nil)
	rootCmd.SetErr(nil)

	if err != nil {
		t.Fatalf("home revoke command failed: %v\nOutput: %s", err, buf.String())
	}

	// Verify that an identity:revoked message was posted to the home campfire.
	msgs, err := trA.ListMessages(campfireAID)
	if err != nil {
		t.Fatalf("reading messages from home campfire: %v", err)
	}

	var found bool
	for _, m := range msgs {
		for _, tag := range m.Tags {
			if tag == cfidentity.IdentityRevokedTag {
				found = true
				break
			}
		}
	}
	if !found {
		t.Errorf("no identity:revoked message found in home campfire after revoke")
	}
}
