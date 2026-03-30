package cmd

// TestSeedCampfireFilesystem verifies that seedCampfireFilesystem posts the
// embedded promote declaration into a newly created campfire.
//
// Done condition: after calling seedCampfireFilesystem, the campfire's messages
// directory contains at least one file whose payload is the promote declaration.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/convention"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// TestSeedCampfireFilesystem_PostsPromoteDeclaration verifies that
// seedCampfireFilesystem writes the embedded promote declaration as a
// convention:operation message in the campfire.
func TestSeedCampfireFilesystem_PostsPromoteDeclaration(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CF_TRANSPORT_DIR", tmpDir)

	// Create agent identity
	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating agent identity: %v", err)
	}

	// Create a campfire
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("creating campfire: %v", err)
	}

	tr := fs.New(tmpDir)
	if err := tr.Init(cf); err != nil {
		t.Fatalf("init transport: %v", err)
	}
	if err := tr.WriteMember(cf.PublicKeyHex(), campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
	}); err != nil {
		t.Fatalf("writing member: %v", err)
	}

	// Open a store and register membership so protocol.Client.Send can look it up.
	s, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()
	if err := s.AddMembership(store.Membership{
		CampfireID:   cf.PublicKeyHex(),
		TransportDir: tr.CampfireDir(cf.PublicKeyHex()),
		JoinProtocol: cf.JoinProtocol,
		Role:         store.PeerRoleCreator,
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    cf.Threshold,
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	// Exercise seedCampfireFilesystem (no project dir, so no seed beacon search)
	seedCampfireFilesystem(cf.PublicKeyHex(), tr.CampfireDir(cf.PublicKeyHex()), agentID, cf, "", s)

	// Verify at least one convention:operation message was written
	msgs, err := tr.ListMessages(cf.PublicKeyHex())
	if err != nil {
		t.Fatalf("listing messages: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected at least one message after seeding, got none")
	}

	// Find the promote declaration
	var foundPromote bool
	for _, msg := range msgs {
		if !hasConventionOperationTag(msg) {
			continue
		}
		var decl convention.Declaration
		if err := json.Unmarshal(msg.Payload, &decl); err != nil {
			continue
		}
		if decl.Convention == convention.InfrastructureConvention && decl.Operation == "promote" {
			foundPromote = true
			break
		}
	}
	if !foundPromote {
		t.Error("expected promote declaration in campfire messages, not found")
	}
}

// TestSeedCampfireFilesystem_WithSeedBeacon verifies that when a seed beacon
// is found, its convention:operation messages are copied into the campfire.
func TestSeedCampfireFilesystem_WithSeedBeacon(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CF_TRANSPORT_DIR", tmpDir)

	// Create agent identity
	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating agent identity: %v", err)
	}

	// --- Set up a seed campfire with a test declaration ---
	seedCF, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("creating seed campfire: %v", err)
	}
	seedTr := fs.New(tmpDir)
	if err := seedTr.Init(seedCF); err != nil {
		t.Fatalf("init seed transport: %v", err)
	}
	// The seed campfire signs its own messages — CampfireID in the beacon matches
	// the signing key so verifySeedBeaconSignatures passes.
	if err := seedTr.WriteMember(seedCF.PublicKeyHex(), campfire.MemberRecord{
		PublicKey: seedCF.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
	}); err != nil {
		t.Fatalf("writing seed member: %v", err)
	}

	// Write a test convention declaration to the seed campfire
	testDecl := convention.Declaration{
		Convention:  "test-seed-convention",
		Version:     "0.1",
		Operation:   "test-seeded-op",
		Description: "A test declaration from the seed campfire",
		Signing:     "member_key",
	}
	testPayload, err := json.Marshal(testDecl)
	if err != nil {
		t.Fatalf("marshaling test declaration: %v", err)
	}
	// Sign with the campfire's own key so the sender matches CampfireID.
	testMsg, err := message.NewMessage(seedCF.PrivateKey, seedCF.PublicKey, testPayload, []string{convention.ConventionOperationTag}, nil)
	if err != nil {
		t.Fatalf("creating test message: %v", err)
	}
	if err := seedTr.WriteMessage(seedCF.PublicKeyHex(), testMsg); err != nil {
		t.Fatalf("writing test message: %v", err)
	}

	// --- Set up a project directory with a seed beacon pointing to the seed campfire ---
	projectDir := t.TempDir()
	seedsDir := filepath.Join(projectDir, ".campfire", "seeds")
	if err := os.MkdirAll(seedsDir, 0700); err != nil {
		t.Fatalf("creating seeds dir: %v", err)
	}

	// Write seed beacon
	type seedBeaconCBOR struct {
		CampfireID string `cbor:"1,keyasint"`
		Protocol   string `cbor:"2,keyasint"`
		Dir        string `cbor:"3,keyasint"`
	}
	sbData, err := cfencoding.Marshal(seedBeaconCBOR{
		CampfireID: seedCF.PublicKeyHex(),
		Protocol:   "filesystem",
		Dir:        seedTr.CampfireDir(seedCF.PublicKeyHex()),
	})
	if err != nil {
		t.Fatalf("marshaling seed beacon: %v", err)
	}
	if err := os.WriteFile(filepath.Join(seedsDir, "test.beacon"), sbData, 0600); err != nil {
		t.Fatalf("writing seed beacon: %v", err)
	}

	// --- Create the target campfire and seed it ---
	targetCF, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("creating target campfire: %v", err)
	}
	targetTr := fs.New(tmpDir)
	if err := targetTr.Init(targetCF); err != nil {
		t.Fatalf("init target transport: %v", err)
	}
	if err := targetTr.WriteMember(targetCF.PublicKeyHex(), campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
	}); err != nil {
		t.Fatalf("writing target member: %v", err)
	}

	// Open a store and register membership for targetCF.
	s2, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s2.Close()
	if err := s2.AddMembership(store.Membership{
		CampfireID:   targetCF.PublicKeyHex(),
		TransportDir: targetTr.CampfireDir(targetCF.PublicKeyHex()),
		JoinProtocol: targetCF.JoinProtocol,
		Role:         store.PeerRoleCreator,
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    targetCF.Threshold,
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	seedCampfireFilesystem(targetCF.PublicKeyHex(), targetTr.CampfireDir(targetCF.PublicKeyHex()), agentID, targetCF, projectDir, s2)

	// Verify the promote declaration is present
	msgs, err := targetTr.ListMessages(targetCF.PublicKeyHex())
	if err != nil {
		t.Fatalf("listing messages: %v", err)
	}

	var foundPromote, foundSeedDecl bool
	for _, msg := range msgs {
		if !hasConventionOperationTag(msg) {
			continue
		}
		var decl convention.Declaration
		if err := json.Unmarshal(msg.Payload, &decl); err != nil {
			continue
		}
		if decl.Convention == convention.InfrastructureConvention && decl.Operation == "promote" {
			foundPromote = true
		}
		if decl.Convention == "test-seed-convention" && decl.Operation == "test-seeded-op" {
			foundSeedDecl = true
		}
	}

	if !foundPromote {
		t.Error("expected promote declaration in target campfire messages, not found")
	}
	if !foundSeedDecl {
		t.Errorf("expected seeded test declaration in target campfire messages, not found (got %d messages)", len(msgs))
	}
}

// TestInitCreatesSeedCampfire verifies that cf init creates a home campfire
// with at least the promote declaration.
func TestInitCreatesSeedCampfire(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CF_HOME", tmpDir)
	t.Setenv("CF_TRANSPORT_DIR", filepath.Join(tmpDir, "transport"))

	// Reset init flags
	initCmd.Flags().Set("force", "false")   //nolint:errcheck
	initCmd.Flags().Set("name", "")         //nolint:errcheck
	initCmd.Flags().Set("session", "false") //nolint:errcheck
	rootCmd.SetArgs([]string{"init"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("cf init failed: %v", err)
	}

	// Identity must exist
	if _, err := os.Stat(filepath.Join(tmpDir, "identity.json")); err != nil {
		t.Fatalf("identity.json not found: %v", err)
	}

	// Store must exist (home campfire recorded)
	if _, err := os.Stat(filepath.Join(tmpDir, "store.db")); err != nil {
		t.Fatalf("store.db not found after init: %v", err)
	}

	// Transport directory must contain at least one campfire with a promote message
	transportDir := filepath.Join(tmpDir, "transport")
	campfireDirs, err := os.ReadDir(transportDir)
	if err != nil {
		t.Fatalf("reading transport dir: %v", err)
	}
	if len(campfireDirs) == 0 {
		t.Fatal("expected at least one campfire dir in transport, got none")
	}

	// Check the first campfire dir for messages
	firstCampfireDir := filepath.Join(transportDir, campfireDirs[0].Name())
	messagesDir := filepath.Join(firstCampfireDir, "messages")
	msgFiles, err := os.ReadDir(messagesDir)
	if err != nil {
		t.Fatalf("reading messages dir in home campfire: %v", err)
	}
	if len(msgFiles) == 0 {
		t.Fatal("expected at least one message in home campfire, got none")
	}

	// Verify one of the messages is the promote declaration
	var foundPromote bool
	for _, mf := range msgFiles {
		if filepath.Ext(mf.Name()) != ".cbor" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(messagesDir, mf.Name()))
		if err != nil {
			continue
		}
		var msg message.Message
		if err := cfencoding.Unmarshal(data, &msg); err != nil {
			continue
		}
		if !hasConventionOperationTag(msg) {
			continue
		}
		var decl convention.Declaration
		if err := json.Unmarshal(msg.Payload, &decl); err != nil {
			continue
		}
		if decl.Convention == convention.InfrastructureConvention && decl.Operation == "promote" {
			foundPromote = true
			break
		}
	}
	if !foundPromote {
		t.Error("expected promote declaration in home campfire messages, not found")
	}
}

// TestCreateFilesystem_SeedsPromoteDeclaration verifies that cf create (filesystem)
// posts the embedded promote declaration into the new campfire.
func TestCreateFilesystem_SeedsPromoteDeclaration(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CF_HOME", tmpDir)
	t.Setenv("CF_TRANSPORT_DIR", filepath.Join(tmpDir, "transport"))
	t.Setenv("CF_BEACON_DIR", filepath.Join(tmpDir, "beacons"))

	// Generate and save identity
	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating agent identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(tmpDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	// Run cf create
	createCmd.Flags().Set("description", "test campfire") //nolint:errcheck
	createCmd.Flags().Set("protocol", "open")             //nolint:errcheck
	createCmd.Flags().Set("transport", "filesystem")      //nolint:errcheck
	rootCmd.SetArgs([]string{"create", "--description", "test campfire"})

	// Capture output to suppress it
	r, w, _ := os.Pipe()
	origStdout := os.Stdout
	os.Stdout = w
	runErr := rootCmd.Execute()
	w.Close()
	os.Stdout = origStdout
	buf := make([]byte, 256)
	n, _ := r.Read(buf)
	r.Close()
	campfireIDHex := string(buf[:n])
	if len(campfireIDHex) > 64 {
		campfireIDHex = campfireIDHex[:64]
	}

	if runErr != nil {
		t.Fatalf("cf create failed: %v", runErr)
	}

	// Find the campfire in transport dir
	transportDir := filepath.Join(tmpDir, "transport")
	campfireDirs, err := os.ReadDir(transportDir)
	if err != nil {
		t.Fatalf("reading transport dir: %v", err)
	}
	if len(campfireDirs) == 0 {
		t.Fatal("no campfire dirs in transport after cf create")
	}

	// Check each campfire dir for the promote declaration
	var foundPromote bool
	for _, cd := range campfireDirs {
		messagesDir := filepath.Join(transportDir, cd.Name(), "messages")
		msgFiles, err := os.ReadDir(messagesDir)
		if err != nil {
			continue
		}
		for _, mf := range msgFiles {
			if filepath.Ext(mf.Name()) != ".cbor" {
				continue
			}
			data, err := os.ReadFile(filepath.Join(messagesDir, mf.Name()))
			if err != nil {
				continue
			}
			var msg message.Message
			if err := cfencoding.Unmarshal(data, &msg); err != nil {
				continue
			}
			if !hasConventionOperationTag(msg) {
				continue
			}
			var decl convention.Declaration
			if err := json.Unmarshal(msg.Payload, &decl); err != nil {
				continue
			}
			if decl.Convention == convention.InfrastructureConvention && decl.Operation == "promote" {
				foundPromote = true
			}
		}
	}

	if !foundPromote {
		t.Error("expected promote declaration in new campfire after cf create, not found")
	}
}

// TestSeedCampfireFilesystem_RejectsDeniedTags is a regression test for
// campfire-agent-icq: seed payloads must be validated through convention.Parse
// before being copied into the new campfire.
//
// Done condition:
//   - A seed campfire message whose payload produces a denied tag (naming: prefix)
//     is NOT copied into the new campfire.
//   - A seed campfire message with a valid payload IS copied.
func TestSeedCampfireFilesystem_RejectsDeniedTags(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CF_TRANSPORT_DIR", tmpDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating agent identity: %v", err)
	}

	// --- Build seed campfire ---
	seedCF, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("creating seed campfire: %v", err)
	}
	seedTr := fs.New(tmpDir)
	if err := seedTr.Init(seedCF); err != nil {
		t.Fatalf("init seed transport: %v", err)
	}
	// The seed campfire signs its own messages — CampfireID in the beacon matches
	// the signing key so verifySeedBeaconSignatures passes.
	if err := seedTr.WriteMember(seedCF.PublicKeyHex(), campfire.MemberRecord{
		PublicKey: seedCF.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
	}); err != nil {
		t.Fatalf("writing seed member: %v", err)
	}

	// Message 1: invalid — produces_tags uses the denied "naming:" prefix.
	// This declaration would bypass convention enforcement without the fix.
	badDecl := convention.Declaration{
		Convention: "bad-convention",
		Version:    "0.1",
		Operation:  "bad-op",
		Signing:    "member_key",
		ProducesTags: []convention.TagRule{
			{Tag: "naming:bad-tag", Cardinality: "exactly_one"},
		},
	}
	badPayload, err := json.Marshal(badDecl)
	if err != nil {
		t.Fatalf("marshaling bad declaration: %v", err)
	}
	// Sign with the campfire's own key so the sender matches CampfireID.
	badMsg, err := message.NewMessage(seedCF.PrivateKey, seedCF.PublicKey, badPayload, []string{convention.ConventionOperationTag}, nil)
	if err != nil {
		t.Fatalf("creating bad message: %v", err)
	}
	if err := seedTr.WriteMessage(seedCF.PublicKeyHex(), badMsg); err != nil {
		t.Fatalf("writing bad message: %v", err)
	}

	// Message 2: valid — a well-formed declaration that should be copied.
	goodDecl := convention.Declaration{
		Convention:  "valid-seed-convention",
		Version:     "0.1",
		Operation:   "valid-seeded-op",
		Description: "A well-formed declaration that should be copied",
		Signing:     "member_key",
	}
	goodPayload, err := json.Marshal(goodDecl)
	if err != nil {
		t.Fatalf("marshaling good declaration: %v", err)
	}
	goodMsg, err := message.NewMessage(seedCF.PrivateKey, seedCF.PublicKey, goodPayload, []string{convention.ConventionOperationTag}, nil)
	if err != nil {
		t.Fatalf("creating good message: %v", err)
	}
	if err := seedTr.WriteMessage(seedCF.PublicKeyHex(), goodMsg); err != nil {
		t.Fatalf("writing good message: %v", err)
	}

	// --- Point a project dir's seed beacon at the seed campfire ---
	projectDir := t.TempDir()
	seedsDir := filepath.Join(projectDir, ".campfire", "seeds")
	if err := os.MkdirAll(seedsDir, 0700); err != nil {
		t.Fatalf("creating seeds dir: %v", err)
	}
	type seedBeaconCBOR struct {
		CampfireID string `cbor:"1,keyasint"`
		Protocol   string `cbor:"2,keyasint"`
		Dir        string `cbor:"3,keyasint"`
	}
	sbData, err := cfencoding.Marshal(seedBeaconCBOR{
		CampfireID: seedCF.PublicKeyHex(),
		Protocol:   "filesystem",
		Dir:        seedTr.CampfireDir(seedCF.PublicKeyHex()),
	})
	if err != nil {
		t.Fatalf("marshaling seed beacon: %v", err)
	}
	if err := os.WriteFile(filepath.Join(seedsDir, "test.beacon"), sbData, 0600); err != nil {
		t.Fatalf("writing seed beacon: %v", err)
	}

	// --- Create target campfire and seed it ---
	targetCF, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("creating target campfire: %v", err)
	}
	targetTr := fs.New(tmpDir)
	if err := targetTr.Init(targetCF); err != nil {
		t.Fatalf("init target transport: %v", err)
	}
	if err := targetTr.WriteMember(targetCF.PublicKeyHex(), campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
	}); err != nil {
		t.Fatalf("writing target member: %v", err)
	}

	// Open a store and register membership for targetCF.
	s3, err := store.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s3.Close()
	if err := s3.AddMembership(store.Membership{
		CampfireID:   targetCF.PublicKeyHex(),
		TransportDir: targetTr.CampfireDir(targetCF.PublicKeyHex()),
		JoinProtocol: targetCF.JoinProtocol,
		Role:         store.PeerRoleCreator,
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    targetCF.Threshold,
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	seedCampfireFilesystem(targetCF.PublicKeyHex(), targetTr.CampfireDir(targetCF.PublicKeyHex()), agentID, targetCF, projectDir, s3)

	// --- Inspect target campfire messages ---
	msgs, err := targetTr.ListMessages(targetCF.PublicKeyHex())
	if err != nil {
		t.Fatalf("listing messages: %v", err)
	}

	var foundBad, foundGood bool
	for _, msg := range msgs {
		if !hasConventionOperationTag(msg) {
			continue
		}
		var decl convention.Declaration
		if err := json.Unmarshal(msg.Payload, &decl); err != nil {
			continue
		}
		if decl.Convention == "bad-convention" && decl.Operation == "bad-op" {
			foundBad = true
		}
		if decl.Convention == "valid-seed-convention" && decl.Operation == "valid-seeded-op" {
			foundGood = true
		}
	}

	if foundBad {
		t.Error("bad declaration (denied naming: tag) was copied into target campfire — validation bypass not fixed")
	}
	if !foundGood {
		t.Error("valid declaration was NOT copied into target campfire — seeding is broken")
	}
}

// hasConventionOperationTag checks whether a message has the convention:operation tag.
func hasConventionOperationTag(msg message.Message) bool {
	for _, tag := range msg.Tags {
		if tag == convention.ConventionOperationTag {
			return true
		}
	}
	return false
}
