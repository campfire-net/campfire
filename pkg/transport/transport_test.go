package transport_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport"
)

func TestResolveType_GitHub(t *testing.T) {
	cases := []struct {
		name         string
		transportDir string
	}{
		{
			name:         "minimal github prefix",
			transportDir: `github:{"repo":"org/repo","issue_number":1}`,
		},
		{
			name:         "github with base_url",
			transportDir: `github:{"repo":"org/repo","issue_number":42,"base_url":"https://github.example.com"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := store.Membership{
				CampfireID:   "abc123",
				TransportDir: tc.transportDir,
			}
			got := transport.ResolveType(m)
			if got != transport.TypeGitHub {
				t.Errorf("ResolveType() = %v, want TypeGitHub", got)
			}
		})
	}
}

func TestResolveType_PeerHTTP(t *testing.T) {
	dir := t.TempDir()
	campfireID := "deadbeef"

	// Create the .cbor state file that signals a p2p-http campfire.
	cborPath := filepath.Join(dir, campfireID+".cbor")
	if err := os.WriteFile(cborPath, []byte("dummy"), 0644); err != nil {
		t.Fatalf("creating cbor file: %v", err)
	}

	m := store.Membership{
		CampfireID:   campfireID,
		TransportDir: dir,
	}
	got := transport.ResolveType(m)
	if got != transport.TypePeerHTTP {
		t.Errorf("ResolveType() = %v, want TypePeerHTTP", got)
	}
}

func TestResolveType_Filesystem(t *testing.T) {
	cases := []struct {
		name         string
		transportDir string
		campfireID   string
	}{
		{
			name:         "empty transport dir",
			transportDir: "",
			campfireID:   "abc123",
		},
		{
			name:         "fs path without cbor file",
			transportDir: "/tmp/campfire/abc123",
			campfireID:   "abc123",
		},
		{
			name:         "non-github non-cbor path",
			transportDir: "/some/other/dir",
			campfireID:   "xyz",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := store.Membership{
				CampfireID:   tc.campfireID,
				TransportDir: tc.transportDir,
			}
			got := transport.ResolveType(m)
			if got != transport.TypeFilesystem {
				t.Errorf("ResolveType() = %v, want TypeFilesystem", got)
			}
		})
	}
}

func TestResolveType_PeerHTTP_NoCborFile(t *testing.T) {
	// A real directory path but no .cbor file — should fall through to filesystem.
	dir := t.TempDir()
	m := store.Membership{
		CampfireID:   "deadbeef",
		TransportDir: dir,
	}
	got := transport.ResolveType(m)
	if got != transport.TypeFilesystem {
		t.Errorf("ResolveType() = %v, want TypeFilesystem (no cbor file)", got)
	}
}

func TestType_String(t *testing.T) {
	cases := []struct {
		typ  transport.Type
		want string
	}{
		{transport.TypeFilesystem, "filesystem"},
		{transport.TypeGitHub, "github"},
		{transport.TypePeerHTTP, "p2p-http"},
		{transport.Type(99), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.typ.String(); got != tc.want {
			t.Errorf("Type(%d).String() = %q, want %q", tc.typ, got, tc.want)
		}
	}
}
