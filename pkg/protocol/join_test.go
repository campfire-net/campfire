package protocol_test

// Tests for protocol.Client.Join() — campfire-agent-ykv.
//
// Done conditions:
// 1. JOIN-THEN-SEND-READ (filesystem): A creates. B joins. B sends. A reads B's message.
// 2. JOIN-THEN-SEND-READ (P2P HTTP): Same with real in-process HTTP servers.
// 3. INVITE-ONLY REJECTION: Create with invite-only. B joins without admission → error.
// 4. INVITE-ONLY ACCEPT: Create with invite-only. A admits B. B joins. B sends. A reads.
// 5. CONVENTION SYNC: Create with convention declaration. B joins. B's store has convention.
// 6. TRUST COMPARISON (pre-existing messages): A sends 3 messages. B joins. B reads all 3.
// 7. go test ./pkg/protocol/ -run TestJoin passes.
//
// No mocks. Real filesystem dirs, real in-process HTTP servers, real SQLite stores.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// portBaseJoin returns a per-process port base for join_test.go.
// Range: 23000 + pid%500. Distinct from other protocol test files.
func portBaseJoin() int {
	return 23000 + (os.Getpid() % 500)
}

// TestJoin runs all Join sub-tests.
func TestJoin(t *testing.T) {
	t.Run("FilesystemSendRead", testJoinFilesystemSendRead)
	t.Run("P2PHTTPSendRead", testJoinP2PHTTPSendRead)
	t.Run("InviteOnlyRejection", testJoinInviteOnlyRejection)
	t.Run("InviteOnlyAccept", testJoinInviteOnlyAccept)
	t.Run("ConventionSync", testJoinConventionSync)
	t.Run("TrustComparison", testJoinTrustComparison)
}

// newJoinClient creates a fresh protocol.Client with a new identity and store.
func newJoinClient(t *testing.T) *protocol.Client {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	storeDir := t.TempDir()
	s, err := store.Open(filepath.Join(storeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return protocol.New(s, id)
}

// createFSCampfire creates a filesystem campfire via Create().
// Returns the campfire ID and the campfire-specific transport dir.
func createFSCampfire(t *testing.T, client *protocol.Client, joinProtocol string) (campfireID, campfireDir string) {
	t.Helper()
	base := t.TempDir()
	beaconDir := t.TempDir()
	result, err := client.Create(protocol.CreateRequest{
		JoinProtocol:  joinProtocol,
		TransportDir:  base,
		TransportType: "filesystem",
		BeaconDir:     beaconDir,
	})
	if err != nil {
		t.Fatalf("Create(%s): %v", joinProtocol, err)
	}
	// The campfire-specific dir is {base}/{campfireID}
	return result.CampfireID, filepath.Join(base, result.CampfireID)
}

// testJoinFilesystemSendRead: Client A creates. B joins. B sends "hello from B". A reads it.
// Done condition 1.
func testJoinFilesystemSendRead(t *testing.T) {
	t.Helper()

	clientA := newJoinClient(t)
	campfireID, campfireDir := createFSCampfire(t, clientA, "open")

	clientB := newJoinClient(t)
	_, err := clientB.Join(protocol.JoinRequest{
		CampfireID:    campfireID,
		TransportDir:  campfireDir,
		TransportType: "filesystem",
	})
	if err != nil {
		t.Fatalf("B.Join: %v", err)
	}

	// B sends a message.
	want := "hello from B (filesystem join)"
	_, err = clientB.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte(want),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("B.Send: %v", err)
	}

	// A reads — must see B's message.
	result, err := clientA.Read(protocol.ReadRequest{CampfireID: campfireID})
	if err != nil {
		t.Fatalf("A.Read: %v", err)
	}
	assertContainsPayload(t, result.Messages, want, "A.Read after filesystem join")
}

// testJoinP2PHTTPSendRead: Same join-send-read but over real in-process HTTP servers.
// Done condition 2.
func testJoinP2PHTTPSendRead(t *testing.T) {
	t.Helper()

	base := portBaseJoin()
	addrA := fmt.Sprintf("127.0.0.1:%d", base+0)
	addrB := fmt.Sprintf("127.0.0.1:%d", base+1)
	endpointA := fmt.Sprintf("http://%s", addrA)
	endpointB := fmt.Sprintf("http://%s", addrB)

	transportDirA := t.TempDir()
	transportDirB := t.TempDir()
	beaconDir := t.TempDir()

	// Client A: creator with running HTTP transport.
	clientA := newJoinClient(t)
	sA := clientA.Store()
	trA := cfhttp.New(addrA, sA)
	if err := trA.Start(); err != nil {
		t.Fatalf("start transport A: %v", err)
	}
	t.Cleanup(func() { trA.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	createResult, err := clientA.Create(protocol.CreateRequest{
		TransportDir:   transportDirA,
		TransportType:  "p2p-http",
		BeaconDir:      beaconDir,
		HTTPTransport:  trA,
		MyHTTPEndpoint: endpointA,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	campfireID := createResult.CampfireID

	// Client B: joiner with running HTTP transport.
	clientB := newJoinClient(t)
	sB := clientB.Store()
	trB := cfhttp.New(addrB, sB)
	if err := trB.Start(); err != nil {
		t.Fatalf("start transport B: %v", err)
	}
	t.Cleanup(func() { trB.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond)

	_, err = clientB.Join(protocol.JoinRequest{
		CampfireID:     campfireID,
		TransportDir:   transportDirB,
		TransportType:  "p2p-http",
		PeerEndpoint:   endpointA,
		MyHTTPEndpoint: endpointB,
		HTTPTransport:  trB,
	})
	if err != nil {
		t.Fatalf("B.Join: %v", err)
	}

	// B sends a message.
	want := "hello from B (P2P HTTP join)"
	_, err = clientB.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte(want),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("B.Send: %v", err)
	}

	// A reads from A's store (message delivered via HTTP).
	readResult, err := clientA.Read(protocol.ReadRequest{
		CampfireID: campfireID,
		SkipSync:   true,
	})
	if err != nil {
		t.Fatalf("A.Read: %v", err)
	}
	assertContainsPayload(t, readResult.Messages, want, "A.Read after P2P HTTP join")
}

// testJoinInviteOnlyRejection: Create with invite-only. B calls Join without
// being admitted first. Join must return an error.
// Done condition 3.
func testJoinInviteOnlyRejection(t *testing.T) {
	t.Helper()

	clientA := newJoinClient(t)
	campfireID, campfireDir := createFSCampfire(t, clientA, "invite-only")

	clientB := newJoinClient(t)
	_, err := clientB.Join(protocol.JoinRequest{
		CampfireID:    campfireID,
		TransportDir:  campfireDir,
		TransportType: "filesystem",
	})
	if err == nil {
		t.Fatal("expected error joining invite-only campfire without admission, got nil")
	}
}

// testJoinInviteOnlyAccept: Create with invite-only. A admits B. B joins.
// B can send and A can read.
// Done condition 4.
func testJoinInviteOnlyAccept(t *testing.T) {
	t.Helper()

	clientA := newJoinClient(t)
	campfireID, campfireDir := createFSCampfire(t, clientA, "invite-only")

	clientB := newJoinClient(t)

	// A pre-admits B.
	if err := clientA.Admit(protocol.AdmitRequest{
		CampfireID:      campfireID,
		MemberPubKeyHex: clientB.Identity().PublicKeyHex(),
		TransportDir:    campfireDir,
	}); err != nil {
		t.Fatalf("A.Admit(B): %v", err)
	}

	// Now B joins — should succeed.
	_, err := clientB.Join(protocol.JoinRequest{
		CampfireID:    campfireID,
		TransportDir:  campfireDir,
		TransportType: "filesystem",
	})
	if err != nil {
		t.Fatalf("B.Join after admission: %v", err)
	}

	// B sends a message.
	want := "hello from B (invite-only accept)"
	_, err = clientB.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte(want),
		Tags:       []string{"status"},
	})
	if err != nil {
		t.Fatalf("B.Send after invite-only join: %v", err)
	}

	// A reads and sees B's message.
	result, err := clientA.Read(protocol.ReadRequest{CampfireID: campfireID})
	if err != nil {
		t.Fatalf("A.Read after invite-only B join: %v", err)
	}
	assertContainsPayload(t, result.Messages, want, "A.Read after invite-only admit+join")
}

// testJoinConventionSync: A creates a campfire and publishes a convention declaration.
// B joins. B's store contains the convention:operation message immediately after join.
// Done condition 5.
func testJoinConventionSync(t *testing.T) {
	t.Helper()

	clientA := newJoinClient(t)
	campfireID, campfireDir := createFSCampfire(t, clientA, "open")

	// A publishes a convention:operation declaration as a signed message.
	declPayload := []byte(`{"convention":"test-join-conv","version":"0.1","operation":"emit","description":"Test convention for join sync","signing":"member_key"}`)
	_, err := clientA.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    declPayload,
		Tags:       []string{convention.ConventionOperationTag},
	})
	if err != nil {
		t.Fatalf("A.Send(convention): %v", err)
	}

	// B joins.
	clientB := newJoinClient(t)
	_, err = clientB.Join(protocol.JoinRequest{
		CampfireID:    campfireID,
		TransportDir:  campfireDir,
		TransportType: "filesystem",
	})
	if err != nil {
		t.Fatalf("B.Join: %v", err)
	}

	// B's store must contain the convention:operation message.
	msgs, err := clientB.Store().ListMessages(campfireID, 0, store.MessageFilter{
		Tags: []string{convention.ConventionOperationTag},
	})
	if err != nil {
		t.Fatalf("B.Store.ListMessages(convention): %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("B's store has no convention:operation messages after join — convention sync failed")
	}
}

// testJoinTrustComparison: A creates and sends 3 messages. B joins.
// B can read all 3 pre-existing messages.
// Done condition 6.
func testJoinTrustComparison(t *testing.T) {
	t.Helper()

	clientA := newJoinClient(t)
	campfireID, campfireDir := createFSCampfire(t, clientA, "open")

	// A sends 3 messages before B joins.
	wants := []string{
		"pre-existing message 1",
		"pre-existing message 2",
		"pre-existing message 3",
	}
	for _, payload := range wants {
		_, err := clientA.Send(protocol.SendRequest{
			CampfireID: campfireID,
			Payload:    []byte(payload),
			Tags:       []string{"status"},
		})
		if err != nil {
			t.Fatalf("A.Send(%q): %v", payload, err)
		}
	}

	// B joins AFTER A has sent messages.
	clientB := newJoinClient(t)
	_, err := clientB.Join(protocol.JoinRequest{
		CampfireID:    campfireID,
		TransportDir:  campfireDir,
		TransportType: "filesystem",
	})
	if err != nil {
		t.Fatalf("B.Join: %v", err)
	}

	// B reads — must see all 3 of A's pre-existing messages.
	// SkipSync=true because syncIfFilesystem was already called during Join.
	result, err := clientB.Read(protocol.ReadRequest{
		CampfireID: campfireID,
		SkipSync:   true,
	})
	if err != nil {
		t.Fatalf("B.Read after join: %v", err)
	}
	for _, want := range wants {
		assertContainsPayload(t, result.Messages, want, "B.Read pre-existing messages")
	}
}

// assertContainsPayload verifies that msgs contains a message with the given payload.
func assertContainsPayload(t *testing.T, msgs []protocol.Message, want, context string) {
	t.Helper()
	for _, m := range msgs {
		if string(m.Payload) == want {
			return
		}
	}
	payloads := make([]string, len(msgs))
	for i, m := range msgs {
		payloads[i] = string(m.Payload)
	}
	t.Errorf("%s: message %q not found; got %d messages: %v", context, want, len(msgs), payloads)
}

// newJoinClientFromIdentity creates a protocol.Client with the given identity.
// Used by invite-only tests to access the joiner's public key before creating the client.
func newJoinClientFromIdentity(t *testing.T, id *identity.Identity) *protocol.Client {
	t.Helper()
	storeDir := t.TempDir()
	s, err := store.Open(filepath.Join(storeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return protocol.New(s, id)
}

// Ensure newJoinClientFromIdentity is used (suppresses "declared and not used").
var _ = newJoinClientFromIdentity
