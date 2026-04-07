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
