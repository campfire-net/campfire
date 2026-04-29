package protocol

// coverage_gap_internal_test.go — Internal package tests for unexported functions.
// Campfire item: campfire-agent-32b
//
// Functions covered:
//   - githubTransportDirFromConfig — encodes GitHubTransport as a transport dir string
//   - noopStore.UpsertPeerEndpoint — no-op store method used in Admit flow

import (
	"testing"

	"github.com/campfire-net/campfire/pkg/store"
)

// TestGithubTransportDirFromConfig_WithOwnerAndRepo verifies that a GitHubTransport
// with Owner and Repo returns "github:<owner>/<repo>".
func TestGithubTransportDirFromConfig_WithOwnerAndRepo(t *testing.T) {
	tr := &GitHubTransport{
		Owner:  "campfire-net",
		Repo:   "campfire",
		Branch: "main",
		Dir:    "messages",
	}
	got := githubTransportDirFromConfig(tr)
	want := "github:campfire-net/campfire"
	if got != want {
		t.Errorf("githubTransportDirFromConfig() = %q, want %q", got, want)
	}
}

// TestGithubTransportDirFromConfig_EmptyOwner verifies that an empty Owner returns "".
func TestGithubTransportDirFromConfig_EmptyOwner(t *testing.T) {
	tr := &GitHubTransport{
		Owner: "",
		Repo:  "campfire",
	}
	got := githubTransportDirFromConfig(tr)
	if got != "" {
		t.Errorf("githubTransportDirFromConfig with empty Owner = %q, want %q", got, "")
	}
}

// TestGithubTransportDirFromConfig_EmptyRepo verifies that an empty Repo returns "".
func TestGithubTransportDirFromConfig_EmptyRepo(t *testing.T) {
	tr := &GitHubTransport{
		Owner: "campfire-net",
		Repo:  "",
	}
	got := githubTransportDirFromConfig(tr)
	if got != "" {
		t.Errorf("githubTransportDirFromConfig with empty Repo = %q, want %q", got, "")
	}
}

// TestGithubTransportDirFromConfig_BothEmpty verifies that an empty Owner and Repo returns "".
func TestGithubTransportDirFromConfig_BothEmpty(t *testing.T) {
	tr := &GitHubTransport{}
	got := githubTransportDirFromConfig(tr)
	if got != "" {
		t.Errorf("githubTransportDirFromConfig with empty Owner+Repo = %q, want %q", got, "")
	}
}

// TestNoopStore_UpsertPeerEndpoint verifies that noopStore.UpsertPeerEndpoint returns nil.
// noopStore is the no-op store used in the Admit() flow when only the filesystem
// member record needs to be written (not the local store).
func TestNoopStore_UpsertPeerEndpoint(t *testing.T) {
	n := &noopStore{}

	err := n.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   "test-campfire-id",
		MemberPubkey: "test-pubkey",
		Endpoint:     "http://127.0.0.1:9001",
	})
	if err != nil {
		t.Errorf("noopStore.UpsertPeerEndpoint: expected nil error, got: %v", err)
	}
}

// TestNoopStore_AddMembership verifies that noopStore.AddMembership returns nil.
func TestNoopStore_AddMembership(t *testing.T) {
	n := &noopStore{}

	err := n.AddMembership(store.Membership{
		CampfireID:    "test-campfire-id",
		TransportDir:  "/tmp/test",
		JoinProtocol:  "open",
		Role:          "full",
		Threshold:     1,
		TransportType: "filesystem",
	})
	if err != nil {
		t.Errorf("noopStore.AddMembership: expected nil error, got: %v", err)
	}
}

// ── fulfillmentLess tiebreaker — internal unit tests (campfireagent-935) ────────

// TestFulfillmentLess_EarlierTimestampWins verifies that when timestamps differ,
// the record with the smaller timestamp is "less" (i.e. comes first / wins).
// This test FAILS if the timestamp comparison is removed or inverted.
func TestFulfillmentLess_EarlierTimestampWins(t *testing.T) {
	earlier := store.MessageRecord{ID: "zzz0000000000000000000000000000000000000000000000000000000000000", Timestamp: 100}
	later := store.MessageRecord{ID: "aaa0000000000000000000000000000000000000000000000000000000000000", Timestamp: 200}

	// earlier < later regardless of ID order (zzz > aaa lexicographically).
	if !fulfillmentLess(earlier, later) {
		t.Error("fulfillmentLess: earlier timestamp must beat later timestamp")
	}
	// Inverse: later is NOT less than earlier.
	if fulfillmentLess(later, earlier) {
		t.Error("fulfillmentLess: later timestamp must NOT beat earlier timestamp")
	}
}

// TestFulfillmentLess_TiebreakerLexSmallestIDWins verifies that when timestamps are
// equal the lexicographically smaller ID wins. This test FAILS if the tiebreaker
// is removed (would be non-deterministic) or reversed (wrong winner).
func TestFulfillmentLess_TiebreakerLexSmallestIDWins(t *testing.T) {
	const ts = int64(999_888_777_000)
	small := store.MessageRecord{ID: "aaa0000000000000000000000000000000000000000000000000000000000000", Timestamp: ts}
	large := store.MessageRecord{ID: "bbb0000000000000000000000000000000000000000000000000000000000000", Timestamp: ts}

	if !fulfillmentLess(small, large) {
		t.Error("fulfillmentLess: lex-smaller ID must be 'less' when timestamps tie")
	}
	if fulfillmentLess(large, small) {
		t.Error("fulfillmentLess: lex-larger ID must NOT be 'less' when timestamps tie")
	}
}

// TestFulfillmentLess_PrefixIDTiebreaker verifies that the tiebreaker handles the
// edge case where one ID is a prefix of the other. Go string comparison is
// lexicographic on bytes, so "aaa" < "aaab" — the shorter ID wins when it is a
// byte-for-byte prefix of the longer.
// This test exercises an adversarial case: short-vs-long IDs at the same timestamp.
func TestFulfillmentLess_PrefixIDTiebreaker(t *testing.T) {
	const ts = int64(12345)
	// "aaa0" is a prefix of "aaa00" in the sense that it sorts before it.
	// We pick IDs where one is a strict prefix of the other to exercise the comparison.
	shorter := store.MessageRecord{ID: "aaa", Timestamp: ts}
	longer := store.MessageRecord{ID: "aaab", Timestamp: ts}

	if !fulfillmentLess(shorter, longer) {
		t.Errorf("fulfillmentLess: shorter ID %q must beat longer ID %q at same timestamp", shorter.ID, longer.ID)
	}
	if fulfillmentLess(longer, shorter) {
		t.Errorf("fulfillmentLess: longer ID %q must NOT beat shorter ID %q", longer.ID, shorter.ID)
	}
}

// TestFulfillmentLess_ThreeWayTieSmallestWins verifies that in a three-way timestamp
// tie, the lexicographically smallest ID consistently beats both others, and the
// middle ID beats the largest. This exercises the transitivity of the ordering.
func TestFulfillmentLess_ThreeWayTieSmallestWins(t *testing.T) {
	const ts = int64(777_666_555_000)
	ids := []string{
		"ccc0000000000000000000000000000000000000000000000000000000000000",
		"aaa0000000000000000000000000000000000000000000000000000000000000", // smallest
		"bbb0000000000000000000000000000000000000000000000000000000000000",
	}
	recs := make([]store.MessageRecord, len(ids))
	for i, id := range ids {
		recs[i] = store.MessageRecord{ID: id, Timestamp: ts}
	}

	// Find the minimum using the same selection logic as findFulfillment.
	best := recs[0]
	for _, r := range recs[1:] {
		if fulfillmentLess(r, best) {
			best = r
		}
	}
	want := "aaa0000000000000000000000000000000000000000000000000000000000000"
	if best.ID != want {
		t.Errorf("three-way tie: expected winner %q, got %q", want, best.ID)
	}
}
