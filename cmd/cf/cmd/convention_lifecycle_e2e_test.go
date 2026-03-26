package cmd

// TestConventionLifecycleE2E exercises the full convention bootstrap lifecycle
// end-to-end using real filesystem transports and real SQLite stores.
//
// Pipeline under test (PRs #39-43):
//   seed (beacon + seed campfire) → init/create (promote decl embedded) →
//   promote (fan-out via transport) → join+sync (second agent sees declarations) →
//   supersede (ListOperations returns only new version) →
//   revoke (ListOperations omits revoked declaration) →
//   registry fallback (ListOperationsWithRegistry reads from registry campfire)

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/convention"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/seed"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// ---- helpers ----

// e2eSetupSeedCampfire creates a seed campfire with a convention declaration,
// writes a seed beacon pointing to it in projectDir/.campfire/seeds/, and
// returns the seed campfire ID and transport directory.
func e2eSetupSeedCampfire(t *testing.T, transportBaseDir string, projectDir string) (seedCampfireID string) {
	t.Helper()

	seedCF, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("creating seed campfire: %v", err)
	}
	seedTr := fs.New(transportBaseDir)
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

	// Write a convention declaration into the seed campfire.
	seedDecl := convention.Declaration{
		Convention:  "test-lifecycle",
		Version:     "0.1",
		Operation:   "seed-op",
		Description: "Seeded operation from test seed campfire",
		ProducesTags: []convention.TagRule{
			{Tag: "test-lifecycle:seed-op", Cardinality: "exactly_one"},
		},
		Args: []convention.ArgDescriptor{
			{Name: "text", Type: "string", Required: true, MaxLength: 500},
		},
		Signing: "member_key",
	}
	seedPayload, err := json.Marshal(seedDecl)
	if err != nil {
		t.Fatalf("marshaling seed declaration: %v", err)
	}
	// Sign with the campfire's own key so the sender matches CampfireID.
	seedMsg, err := message.NewMessage(seedCF.PrivateKey, seedCF.PublicKey, seedPayload, []string{"convention:operation"}, nil)
	if err != nil {
		t.Fatalf("creating seed message: %v", err)
	}
	if err := seedTr.WriteMessage(seedCF.PublicKeyHex(), seedMsg); err != nil {
		t.Fatalf("writing seed message: %v", err)
	}

	// Write seed beacon into projectDir/.campfire/seeds/
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
	if err := os.WriteFile(filepath.Join(seedsDir, "lifecycle.beacon"), sbData, 0600); err != nil {
		t.Fatalf("writing seed beacon file: %v", err)
	}

	return seedCF.PublicKeyHex()
}

// e2eCreateCampfire creates an open filesystem campfire with the given agent as owner.
// Runs seedCampfireFilesystem with the given projectDir. Returns the campfire, transport,
// and the campfire directory path.
func e2eCreateCampfire(t *testing.T, agentID *identity.Identity, transportBaseDir string, projectDir string) (cf *campfire.Campfire, tr *fs.Transport) {
	t.Helper()

	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("creating campfire: %v", err)
	}
	tr = fs.New(transportBaseDir)
	if err := tr.Init(cf); err != nil {
		t.Fatalf("init campfire transport: %v", err)
	}
	if err := tr.WriteMember(cf.PublicKeyHex(), campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("writing owner member: %v", err)
	}

	// Seed: embed promote declaration + seed from beacon.
	seedCampfireFilesystem(cf.PublicKeyHex(), tr.CampfireDir(cf.PublicKeyHex()), agentID, cf, projectDir)

	return cf, tr
}

// e2eStoreWithMembership opens a store and adds a membership record for the given campfire.
func e2eStoreWithMembership(t *testing.T, cfHomeDir string, campfireID string, transportDir string) store.Store {
	t.Helper()

	s, err := store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: transportDir,
		JoinProtocol: "open",
		Role:         "member",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}
	return s
}

// e2eWriteDeclarationToStore writes a convention declaration into a store directly.
// This simulates what promoteSingle does after syncing via transport.
func e2eWriteDeclarationToStore(t *testing.T, s store.Store, agentID *identity.Identity, campfireID string, payload []byte, supersedes string) string {
	t.Helper()

	var declMap map[string]any
	if err := json.Unmarshal(payload, &declMap); err != nil {
		t.Fatalf("unmarshaling declaration: %v", err)
	}
	if supersedes != "" {
		declMap["supersedes"] = supersedes
	}
	finalPayload, err := json.Marshal(declMap)
	if err != nil {
		t.Fatalf("marshaling modified declaration: %v", err)
	}

	msg, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, finalPayload, []string{"convention:operation"}, nil)
	if err != nil {
		t.Fatalf("creating declaration message: %v", err)
	}
	rec := store.MessageRecord{
		ID:         msg.ID,
		CampfireID: campfireID,
		Sender:     msg.SenderHex(),
		Payload:    msg.Payload,
		Tags:       msg.Tags,
		Timestamp:  msg.Timestamp,
		Signature:  msg.Signature,
	}
	if _, err := s.AddMessage(rec); err != nil {
		t.Fatalf("adding declaration to store: %v", err)
	}
	return msg.ID
}

// e2eWriteRevokeToStore writes a convention:revoke message to the store.
func e2eWriteRevokeToStore(t *testing.T, s store.Store, agentID *identity.Identity, campfireID string, targetID string) {
	t.Helper()

	payload, err := json.Marshal(map[string]string{"target_id": targetID})
	if err != nil {
		t.Fatalf("marshaling revoke payload: %v", err)
	}
	msg, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, payload, []string{"convention:revoke"}, nil)
	if err != nil {
		t.Fatalf("creating revoke message: %v", err)
	}
	rec := store.MessageRecord{
		ID:         msg.ID,
		CampfireID: campfireID,
		Sender:     msg.SenderHex(),
		Payload:    msg.Payload,
		Tags:       msg.Tags,
		Timestamp:  msg.Timestamp,
		Signature:  msg.Signature,
	}
	if _, err := s.AddMessage(rec); err != nil {
		t.Fatalf("adding revoke to store: %v", err)
	}
}

// ---- E2E test ----

// TestConventionLifecycleE2E exercises the full lifecycle in a single test
// using real filesystem transports and real SQLite stores.
func TestConventionLifecycleE2E(t *testing.T) {
	// Shared transport base dir — all campfires live here.
	transportBaseDir := t.TempDir()
	projectDir := t.TempDir()

	// Override well-known URL so FindSeedBeacon doesn't hit the network.
	origWellKnownURL := seed.WellKnownURL
	seed.WellKnownURL = "http://127.0.0.1:0/unreachable"
	t.Cleanup(func() { seed.WellKnownURL = origWellKnownURL })

	// --- Stage 1: Seed beacon setup ---
	//
	// Create a seed campfire containing a "seed-op" convention declaration.
	// Write a seed beacon in projectDir/.campfire/seeds/ pointing to it.
	seedCampfireID := e2eSetupSeedCampfire(t, transportBaseDir, projectDir)
	t.Logf("seed campfire: %s", seedCampfireID[:12])

	// --- Stage 2: Create / init with seed ---
	//
	// Agent A creates a new campfire. seedCampfireFilesystem posts:
	//   1. The embedded promote declaration (always).
	//   2. Convention declarations from the seed beacon.
	agentA, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating agent A: %v", err)
	}

	cf, tr := e2eCreateCampfire(t, agentA, transportBaseDir, projectDir)
	campfireID := cf.PublicKeyHex()
	t.Logf("target campfire: %s", campfireID[:12])

	// Verify: promote declaration is present in transport.
	msgs, err := tr.ListMessages(campfireID)
	if err != nil {
		t.Fatalf("listing messages after seed: %v", err)
	}
	var foundPromote, foundSeedOp bool
	for _, msg := range msgs {
		var decl convention.Declaration
		if jsonErr := json.Unmarshal(msg.Payload, &decl); jsonErr != nil {
			continue
		}
		if !hasConventionOperationTag(msg) {
			continue
		}
		if decl.Convention == convention.InfrastructureConvention && decl.Operation == "promote" {
			foundPromote = true
		}
		if decl.Convention == "test-lifecycle" && decl.Operation == "seed-op" {
			foundSeedOp = true
		}
	}
	if !foundPromote {
		t.Error("Stage 2: promote declaration not found in campfire after seedCampfireFilesystem")
	}
	if !foundSeedOp {
		t.Errorf("Stage 2: seed-op declaration not found in campfire after seedCampfireFilesystem (got %d messages)", len(msgs))
	}
	t.Logf("Stage 2 OK: promote and seed-op declarations present after init/create")

	// --- Stage 3: Promote a new declaration via transport ---
	//
	// Agent A promotes a new "post" declaration to the campfire via the filesystem transport.
	// This tests that promoteSingle fans out via transport (not just local store).
	cfHomeA := t.TempDir()
	t.Setenv("CF_HOME", cfHomeA)
	t.Setenv("CF_TRANSPORT_DIR", transportBaseDir)
	t.Cleanup(func() { cfHome = ""; os.Unsetenv("CF_HOME"); os.Unsetenv("CF_TRANSPORT_DIR") })
	cfHome = ""

	if err := agentA.Save(filepath.Join(cfHomeA, "identity.json")); err != nil {
		t.Fatalf("saving agent A identity: %v", err)
	}
	storeA := e2eStoreWithMembership(t, cfHomeA, campfireID, tr.CampfireDir(campfireID))

	postDeclPayload := minimalDecl("test-lifecycle", "post")
	postSrc := declSource{name: "post.json", payload: postDeclPayload}

	mA, err := storeA.GetMembership(campfireID)
	if err != nil || mA == nil {
		t.Fatalf("agent A membership not found: err=%v", err)
	}

	promResult := promoteSingle(postSrc, campfireID, agentA, storeA, mA, map[string]*convention.Declaration{})
	if promResult.Error != "" {
		t.Fatalf("Stage 3: promoteSingle failed: %s", promResult.Error)
	}
	if promResult.Skipped {
		t.Fatal("Stage 3: expected promote, got skipped")
	}
	postMsgID := promResult.MessageID
	t.Logf("Stage 3 OK: promoted post declaration, msgID=%s", postMsgID[:12])

	// Verify the promoted declaration is in the transport.
	transportMsgs, err := tr.ListMessages(campfireID)
	if err != nil {
		t.Fatalf("Stage 3: listing transport messages: %v", err)
	}
	foundPostInTransport := false
	for _, msg := range transportMsgs {
		if msg.ID == postMsgID {
			foundPostInTransport = true
			break
		}
	}
	if !foundPostInTransport {
		t.Errorf("Stage 3: promoted declaration %s not found in transport (got %d messages)", postMsgID[:12], len(transportMsgs))
	}

	// --- Stage 4: Join + sync from a second agent ---
	//
	// Agent B joins the campfire. joinFilesystem syncs all messages (including
	// the promoted declaration) immediately without a separate cf read.
	agentB, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating agent B: %v", err)
	}
	cfHomeB := t.TempDir()
	// Update env to simulate agent B's session.
	t.Setenv("CF_HOME", cfHomeB)
	cfHome = ""

	if err := agentB.Save(filepath.Join(cfHomeB, "identity.json")); err != nil {
		t.Fatalf("saving agent B identity: %v", err)
	}
	storeB, err := store.Open(filepath.Join(cfHomeB, "store.db"))
	if err != nil {
		t.Fatalf("opening store B: %v", err)
	}
	t.Cleanup(func() { storeB.Close() })

	if err := joinFilesystem(campfireID, agentB, storeB); err != nil {
		t.Fatalf("Stage 4: agent B joinFilesystem failed: %v", err)
	}

	// Verify agent B can see the promoted declaration without a separate read.
	bConvMsgs, err := storeB.ListMessages(campfireID, 0, store.MessageFilter{Tags: []string{"convention:operation"}})
	if err != nil {
		t.Fatalf("Stage 4: listing convention messages for agent B: %v", err)
	}
	foundPostInB := false
	for _, msg := range bConvMsgs {
		if msg.ID == postMsgID {
			foundPostInB = true
			break
		}
	}
	if !foundPostInB {
		t.Errorf("Stage 4: agent B missing promoted declaration after join+sync (got %d convention msgs)", len(bConvMsgs))
	}
	t.Logf("Stage 4 OK: agent B sees promoted declaration after join+sync (%d convention msgs)", len(bConvMsgs))

	// --- Stage 5: Supersede ---
	//
	// Supersede the "post" declaration with a new version. ListOperations must
	// return only the new version, not the original.
	// We write directly to storeA (simulating what cf convention supersede does).
	postDeclV2Payload := minimalDecl("test-lifecycle", "post-v2")
	// Write v1 with supersedes pointing to the original post declaration.
	postV2MsgID := e2eWriteDeclarationToStore(t, storeA, agentA, campfireID, postDeclPayload, postMsgID)

	// Also write the v2 payload into store B so ListOperations can see it there.
	// In a real system this flows via transport sync; here we write directly.
	_ = e2eWriteDeclarationToStore(t, storeB, agentB, campfireID, postDeclPayload, postMsgID)

	// Use a distinct v2 payload in storeA to verify supersede.
	_ = postDeclV2Payload // used for reference; actual supersede uses postDeclPayload variant

	readerA := cliStoreReader{storeA}
	decls, err := convention.ListOperations(readerA, campfireID, "")
	if err != nil {
		t.Fatalf("Stage 5: ListOperations failed: %v", err)
	}

	// The original post msgID must not appear — it was superseded.
	for _, d := range decls {
		if d.MessageID == postMsgID {
			t.Errorf("Stage 5: superseded declaration (msgID=%s) still present in ListOperations", postMsgID[:12])
		}
	}
	// The superseding declaration must be present.
	foundSuperseder := false
	for _, d := range decls {
		if d.MessageID == postV2MsgID {
			foundSuperseder = true
			break
		}
	}
	if !foundSuperseder {
		t.Errorf("Stage 5: superseding declaration (msgID=%s) not found in ListOperations (got %d decls)", postV2MsgID[:12], len(decls))
	}
	t.Logf("Stage 5 OK: supersede — only new version visible in ListOperations")

	// --- Stage 6: Revoke ---
	//
	// Revoke the superseding declaration. ListOperations must not return it.
	// We use agent A's key (campfire owner / convention authority).
	e2eWriteRevokeToStore(t, storeA, agentA, campfireID, postV2MsgID)

	declsAfterRevoke, err := convention.ListOperations(readerA, campfireID, "")
	if err != nil {
		t.Fatalf("Stage 6: ListOperations after revoke failed: %v", err)
	}
	for _, d := range declsAfterRevoke {
		if d.MessageID == postV2MsgID {
			t.Errorf("Stage 6: revoked declaration (msgID=%s) still present in ListOperations", postV2MsgID[:12])
		}
		if d.MessageID == postMsgID {
			t.Errorf("Stage 6: chain-invalidated declaration (msgID=%s) still present after superseder was revoked", postMsgID[:12])
		}
	}
	t.Logf("Stage 6 OK: revoke — declaration no longer visible in ListOperations")

	// --- Stage 7: Registry fallback ---
	//
	// Create a separate registry campfire with a "registry-op" declaration.
	// Use ListOperationsWithRegistry to verify it falls through to the registry
	// when no inline declarations match.
	registryCF, registryTr := e2eCreateCampfire(t, agentA, transportBaseDir, projectDir)
	registryID := registryCF.PublicKeyHex()
	t.Logf("registry campfire: %s", registryID[:12])

	if err := storeA.AddMembership(store.Membership{
		CampfireID:   registryID,
		TransportDir: registryTr.CampfireDir(registryID),
		JoinProtocol: "open",
		Role:         "member",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}); err != nil {
		t.Fatalf("Stage 7: adding registry membership: %v", err)
	}

	// Promote a "registry-op" declaration into the registry campfire.
	registryDeclPayload := func() []byte {
		d := map[string]any{
			"convention":  "test-lifecycle",
			"version":     "0.2",
			"operation":   "registry-op",
			"description": fmt.Sprintf("Registry operation"),
			"produces_tags": []map[string]any{
				{"tag": "test-lifecycle:registry-op", "cardinality": "exactly_one"},
			},
			"args": []map[string]any{
				{"name": "text", "type": "string", "required": true, "max_length": 1000},
			},
			"signing": "member_key",
		}
		b, _ := json.Marshal(d)
		return b
	}()

	registryMembership, err := storeA.GetMembership(registryID)
	if err != nil || registryMembership == nil {
		t.Fatalf("Stage 7: registry membership not found: err=%v", err)
	}
	regPromResult := promoteSingle(
		declSource{name: "registry-op.json", payload: registryDeclPayload},
		registryID, agentA, storeA, registryMembership,
		map[string]*convention.Declaration{},
	)
	if regPromResult.Error != "" {
		t.Fatalf("Stage 7: promoting registry-op failed: %s", regPromResult.Error)
	}

	// Create a "target" campfire with no convention declarations.
	targetCF, targetTr := e2eCreateCampfire(t, agentA, transportBaseDir, "")
	// Remove seeded declarations from targetCF transport so ListOperations sees none inline.
	// Easiest approach: use a fresh campfire with no seeding (pass "" as projectDir).
	// e2eCreateCampfire with "" projectDir still seeds the promote declaration from the binary.
	// For registry fallback, we need truly zero inline declarations in the target.
	// We create a bare campfire without seeding to simulate this.
	targetCFBare, targetTrBare := func() (*campfire.Campfire, *fs.Transport) {
		bareCF, err := campfire.New("open", nil, 1)
		if err != nil {
			t.Fatalf("Stage 7: creating bare target campfire: %v", err)
		}
		bareTr := fs.New(transportBaseDir)
		if err := bareTr.Init(bareCF); err != nil {
			t.Fatalf("Stage 7: init bare transport: %v", err)
		}
		if err := bareTr.WriteMember(bareCF.PublicKeyHex(), campfire.MemberRecord{
			PublicKey: agentA.PublicKey,
			JoinedAt:  time.Now().UnixNano(),
			Role:      campfire.RoleFull,
		}); err != nil {
			t.Fatalf("Stage 7: writing bare member: %v", err)
		}
		return bareCF, bareTr
	}()
	// Suppress unused-variable lint warnings (we only need the IDs).
	_ = targetCF
	_ = targetTr
	_ = targetTrBare

	bareTargetID := targetCFBare.PublicKeyHex()
	if err := storeA.AddMembership(store.Membership{
		CampfireID:   bareTargetID,
		TransportDir: targetTrBare.CampfireDir(bareTargetID),
		JoinProtocol: "open",
		Role:         "member",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}); err != nil {
		t.Fatalf("Stage 7: adding bare target membership: %v", err)
	}

	// Verify bare target has no convention:operation declarations inline.
	bareInlineDecls, err := convention.ListOperations(readerA, bareTargetID, "")
	if err != nil {
		t.Fatalf("Stage 7: ListOperations on bare target: %v", err)
	}
	if len(bareInlineDecls) != 0 {
		t.Errorf("Stage 7: expected 0 inline declarations for bare target, got %d", len(bareInlineDecls))
	}

	// ListOperationsWithRegistry: should return registry-op from the registry campfire.
	regDecls, err := convention.ListOperationsWithRegistry(readerA, bareTargetID, "", registryID)
	if err != nil {
		t.Fatalf("Stage 7: ListOperationsWithRegistry failed: %v", err)
	}
	foundRegistryOp := false
	for _, d := range regDecls {
		if d.Operation == "registry-op" {
			foundRegistryOp = true
			break
		}
	}
	if !foundRegistryOp {
		t.Errorf("Stage 7: registry-op not found via registry fallback (got %d decls)", len(regDecls))
	}
	t.Logf("Stage 7 OK: registry fallback — registry-op visible via ListOperationsWithRegistry")

	t.Logf("All lifecycle stages passed: seed → create → promote → join+sync → supersede → revoke → registry fallback")
}

// TestConventionLifecycleE2E_SeedBeaconIntegration verifies the seed.FindSeedBeacon
// and seed.ReadConventionMessages functions work end-to-end with a real filesystem
// beacon and campfire.
func TestConventionLifecycleE2E_SeedBeaconIntegration(t *testing.T) {
	transportBaseDir := t.TempDir()
	projectDir := t.TempDir()

	// Override well-known URL to prevent network calls.
	origURL := seed.WellKnownURL
	seed.WellKnownURL = "http://127.0.0.1:0/unreachable"
	t.Cleanup(func() { seed.WellKnownURL = origURL })

	seedCampfireID := e2eSetupSeedCampfire(t, transportBaseDir, projectDir)

	// FindSeedBeacon must find the project-local beacon.
	sb, err := seed.FindSeedBeacon(projectDir)
	if err != nil {
		t.Fatalf("FindSeedBeacon: %v", err)
	}
	if sb == nil {
		t.Fatal("FindSeedBeacon returned nil — project-local beacon not found")
	}
	if sb.CampfireID != seedCampfireID {
		t.Errorf("seed beacon CampfireID: want %s, got %s", seedCampfireID[:12], sb.CampfireID[:12])
	}

	// ReadConventionMessages must return the seed-op declaration.
	convMsgs, err := seed.ReadConventionMessages(sb)
	if err != nil {
		t.Fatalf("ReadConventionMessages: %v", err)
	}
	if len(convMsgs) == 0 {
		t.Fatal("ReadConventionMessages returned no messages — seed campfire has no convention declarations")
	}
	foundSeedOp := false
	for _, msg := range convMsgs {
		var decl convention.Declaration
		if json.Unmarshal(msg.Payload, &decl) == nil {
			if decl.Operation == "seed-op" {
				foundSeedOp = true
				break
			}
		}
	}
	if !foundSeedOp {
		t.Errorf("seed-op declaration not found in ReadConventionMessages output (got %d messages)", len(convMsgs))
	}
	t.Logf("SeedBeacon integration OK: FindSeedBeacon + ReadConventionMessages return seed-op declaration")
}

// TestConventionLifecycleE2E_SupersedeRevoke_Isolated tests supersede and revoke
// semantics in isolation using convention.ListOperations directly.
func TestConventionLifecycleE2E_SupersedeRevoke_Isolated(t *testing.T) {
	dir := t.TempDir()
	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}

	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire ID: %v", err)
	}
	campfireID := cfID.PublicKeyHex()

	// Write v1 declaration.
	v1Payload := minimalDecl("iso-conv", "post")
	v1ID := e2eWriteDeclarationToStore(t, s, agentID, campfireID, v1Payload, "")

	// Verify v1 is visible.
	reader := cliStoreReader{s}
	decls, err := convention.ListOperations(reader, campfireID, "")
	if err != nil {
		t.Fatalf("ListOperations after v1: %v", err)
	}
	if !e2eFindByMsgID(decls, v1ID) {
		t.Fatalf("v1 declaration not found in ListOperations")
	}

	// Write v2 superseding v1.
	v2Payload := minimalDecl("iso-conv", "post")
	v2ID := e2eWriteDeclarationToStore(t, s, agentID, campfireID, v2Payload, v1ID)

	// v1 must be gone, v2 must be present.
	decls, err = convention.ListOperations(reader, campfireID, "")
	if err != nil {
		t.Fatalf("ListOperations after supersede: %v", err)
	}
	if e2eFindByMsgID(decls, v1ID) {
		t.Error("v1 still present after supersede")
	}
	if !e2eFindByMsgID(decls, v2ID) {
		t.Error("v2 (superseder) not present")
	}

	// Revoke v2.
	e2eWriteRevokeToStore(t, s, agentID, campfireID, v2ID)

	// Neither v1 nor v2 should appear (chain invalidation).
	decls, err = convention.ListOperations(reader, campfireID, "")
	if err != nil {
		t.Fatalf("ListOperations after revoke: %v", err)
	}
	if e2eFindByMsgID(decls, v1ID) {
		t.Error("v1 appeared after v2 was revoked (chain invalidation broken)")
	}
	if e2eFindByMsgID(decls, v2ID) {
		t.Error("v2 still present after revoke")
	}
	t.Logf("Isolated supersede/revoke OK")
}

// e2eFindByMsgID returns true if any declaration in decls has the given message ID.
func e2eFindByMsgID(decls []*convention.Declaration, msgID string) bool {
	for _, d := range decls {
		if d.MessageID == msgID {
			return true
		}
	}
	return false
}

// TestConventionLifecycleE2E_RegistryFallback_ViaChainWalker tests that
// listConventionOperations (which uses the ChainWalker) falls through to a
// convention registry when no inline declarations exist.
func TestConventionLifecycleE2E_RegistryFallback_ViaChainWalker(t *testing.T) {
	// Reuse setupRegistryEnv which already wires the full chain:
	// operator root → naming:registration → convRegistry → convention:operation declaration.
	targetCampfireID, cleanup := setupRegistryEnv(t, testDecl)
	defer cleanup()

	s, err := openStore()
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	decls, err := listConventionOperations(context.Background(), s, targetCampfireID)
	if err != nil {
		t.Fatalf("listConventionOperations: %v", err)
	}
	if len(decls) == 0 {
		t.Fatal("expected declarations from registry via ChainWalker, got none")
	}
	found := false
	for _, d := range decls {
		if d.Operation == "post" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'post' operation from registry, got: %v", func() []string {
			var ops []string
			for _, d := range decls {
				ops = append(ops, d.Operation)
			}
			return ops
		}())
	}
	t.Logf("ChainWalker registry fallback OK: %d declarations from registry", len(decls))
}
