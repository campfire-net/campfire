package main

// Regression test for campfireagent-b991.
//
// Symptom (automata-island/automataisland-2724, Mara's embodiment worker): a
// worker on a fresh CF_HOME whose identity is already a member of a campfire
// on disk calls campfire_join → {"status":"joined"} — but the campfire's
// convention declarations never register as MCP tools, so none of the
// campfire's API surface is callable.
//
// Root cause: handleJoin's post-join step reads declarations from the SESSION
// STORE (readDeclarations → convention.ListOperations over the store), but the
// declarations live in MESSAGES on the filesystem. Nothing in the local join
// path syncs fs messages into the store (the membership rehydrate covers
// memberships only), so on a fresh CF_HOME the scan sees zero declarations and
// registers zero tools. The CLI join path already syncs post-join
// (syncCampfire in cmd/cf/cmd/join.go); cf-mcp's handleJoin did not.
//
// Fix under test: handleJoin runs the verified fs→store message sync
// (syncFSVerified) before the declaration scan.

import (
	"crypto/ed25519"
	"encoding/json"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	"github.com/campfire-net/campfire/cf-protocol/message"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
	"github.com/campfire-net/campfire/pkg/identity"
)

func TestJoin_RegistersConventionTools_FromOnDiskMessages(t *testing.T) {
	// ---- Shared filesystem transport root (the Mara/jail shape) ----
	// legion plumbs CF_TRANSPORT_DIR at the shared body-cf root; both the
	// creator and the joiner resolve the same on-disk campfire. This also
	// keeps the test hermetic (no DefaultBaseDir fallthrough to ~/.campfire),
	// and keeps srvB in stdio mode (httpTransport nil) so the FS-mode
	// post-join sync path under test actually runs.
	sharedRoot := t.TempDir()
	t.Setenv("CF_TRANSPORT_DIR", sharedRoot)

	// ---- srvA creates the campfire (state + creator member on disk) ----
	srvA, _ := newTestServerWithStore(t)
	doInit(t, srvA)
	createResp := srvA.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	fields := extractCreateResult(t, createResp)
	campfireID, _ := fields["campfire_id"].(string)
	if campfireID == "" {
		t.Fatal("campfire_create returned no campfire_id")
	}

	// ---- Publish a convention declaration into the ON-DISK messages ----
	// Signed by the campfire key with a valid provenance hop, so the verified
	// sync (signature + provenance checks) accepts it — mirroring how
	// `cf convention install` publishes declarations.
	trA := fs.New(sharedRoot)
	state, err := trA.ReadState(campfireID)
	if err != nil {
		t.Fatalf("reading campfire state: %v", err)
	}
	members, err := trA.ListMembers(campfireID)
	if err != nil {
		t.Fatalf("listing members: %v", err)
	}
	cf := state.ToCampfire(members)
	signer, err := message.NewEd25519Signer(
		ed25519.PrivateKey(state.PrivateKey),
		ed25519.PublicKey(state.PublicKey),
	)
	if err != nil {
		t.Fatalf("creating campfire-key signer: %v", err)
	}
	// signing="campfire_key" + actually signed by the campfire key →
	// SignerCampfireKey → AuthorityOperational (passes the trust filter in
	// readDeclarations). A "member_key" declaration resolves to
	// AuthorityUntrusted and is filtered regardless of the actual signer.
	declPayload := []byte(`{
		"convention": "freeso-embodiment",
		"version": "1.0",
		"operation": "query-self",
		"description": "Query the automaton's own body state",
		"args": [{"name": "detail", "type": "string", "required": false}],
		"antecedents": "none",
		"signing": "campfire_key"
	}`)
	declMsg, err := message.NewMessage(signer, declPayload, []string{"convention:operation"}, nil)
	if err != nil {
		t.Fatalf("creating declaration message: %v", err)
	}
	if err := declMsg.AddHop(
		state.PrivateKey, state.PublicKey,
		cf.MembershipHash(), len(members),
		state.JoinProtocol, state.ReceptionRequirements,
		campfire.RoleFull,
	); err != nil {
		t.Fatalf("adding provenance hop: %v", err)
	}
	if err := trA.WriteMessage(campfireID, declMsg); err != nil {
		t.Fatalf("writing declaration message: %v", err)
	}

	// ---- srvB: separate identity, PRE-ADMITTED on disk (the jail shape) ----
	srvB, storeB := newTestServerWithStore(t)
	if respB := srvB.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{}}`)); respB.Error != nil {
		t.Fatalf("init srvB: %v", respB.Error)
	}
	joinerID, err := identity.Load(srvB.identityPath())
	if err != nil {
		t.Fatalf("loading srvB identity: %v", err)
	}
	if err := trA.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: joinerID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("pre-admitting srvB on disk: %v", err)
	}

	// ---- Join. Pre-fix: succeeds but registers ZERO convention tools ----
	joinArgs, _ := json.Marshal(map[string]interface{}{"campfire_id": campfireID})
	joinResp := srvB.dispatch(makeReq("tools/call",
		`{"name":"campfire_join","arguments":`+string(joinArgs)+`}`))
	if joinResp.Error != nil {
		t.Fatalf("campfire_join: %s", joinResp.Error.Message)
	}

	// Intermediate assertion: the join must have synced the on-disk messages
	// into srvB's session store (the half of the fix the scan depends on).
	msgs, msgsErr := storeB.ListMessages(campfireID, 0)
	if msgsErr != nil || len(msgs) == 0 {
		t.Fatalf("declaration message not synced into session store by join: n=%d err=%v", len(msgs), msgsErr)
	}

	// ---- THE b991 ASSERTION: the declaration's tool must be registered ----
	if srvB.conventionTools == nil {
		t.Fatal("conventionTools map not initialized after join")
	}
	tools := srvB.conventionTools.list()
	found := false
	for _, tool := range tools {
		if tool.Name == "query-self" {
			found = true
			break
		}
	}
	if !found {
		names := make([]string, 0, len(tools))
		for _, tool := range tools {
			names = append(names, tool.Name)
		}
		t.Fatalf("convention tool 'query-self' not registered after join from on-disk messages (campfireagent-b991); registered tools: %v", names)
	}
}
