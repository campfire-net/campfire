package transport_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport"
)

// TestResolveType_ExplicitField verifies that ResolveType uses the TransportType
// field directly when it is set, without consulting TransportDir.
func TestResolveType_ExplicitField(t *testing.T) {
	cases := []struct {
		name          string
		transportType string
		want          transport.Type
	}{
		{"github explicit", "github", transport.TypeGitHub},
		{"p2p-http explicit", "p2p-http", transport.TypePeerHTTP},
		{"filesystem explicit", "filesystem", transport.TypeFilesystem},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := store.Membership{
				CampfireID:    "abc123",
				TransportDir:  "/some/dir", // would be p2p-http if heuristic ran
				TransportType: tc.transportType,
			}
			got := transport.ResolveType(m)
			if got != tc.want {
				t.Errorf("ResolveType() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestResolveType_GitHub verifies GitHub detection via explicit TransportType field
// (the canonical path) and via legacy TransportDir prefix fallback.
func TestResolveType_GitHub(t *testing.T) {
	cases := []struct {
		name          string
		transportType string
		transportDir  string
	}{
		{
			name:          "explicit transport_type field",
			transportType: "github",
			transportDir:  `github:{"repo":"org/repo","issue_number":1}`,
		},
		{
			name:          "legacy TransportDir prefix fallback",
			transportType: "", // empty — falls back to heuristic
			transportDir:  `github:{"repo":"org/repo","issue_number":42,"base_url":"https://github.example.com"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := store.Membership{
				CampfireID:    "abc123",
				TransportDir:  tc.transportDir,
				TransportType: tc.transportType,
			}
			got := transport.ResolveType(m)
			if got != transport.TypeGitHub {
				t.Errorf("ResolveType() = %v, want TypeGitHub", got)
			}
		})
	}
}

// TestResolveType_PeerHTTP verifies p2p-http detection via explicit TransportType.
// The old heuristic (os.Stat for .cbor) is no longer used in ResolveType;
// the field is set at insert time by store.AddMembership/inferTransportType.
func TestResolveType_PeerHTTP(t *testing.T) {
	dir := t.TempDir()
	campfireID := "deadbeef"

	// Create the .cbor state file to simulate a real p2p-http campfire directory.
	// In production this is detected by inferTransportType at insert time, not here.
	cborPath := filepath.Join(dir, campfireID+".cbor")
	if err := os.WriteFile(cborPath, []byte("dummy"), 0644); err != nil {
		t.Fatalf("creating cbor file: %v", err)
	}

	// Explicit field takes priority — no filesystem probe needed.
	m := store.Membership{
		CampfireID:    campfireID,
		TransportDir:  dir,
		TransportType: "p2p-http",
	}
	got := transport.ResolveType(m)
	if got != transport.TypePeerHTTP {
		t.Errorf("ResolveType() = %v, want TypePeerHTTP", got)
	}
}

// TestResolveType_Filesystem verifies that explicit "filesystem" type and the
// legacy fallback (empty TransportType, non-github TransportDir) both resolve correctly.
func TestResolveType_Filesystem(t *testing.T) {
	cases := []struct {
		name          string
		transportDir  string
		campfireID    string
		transportType string
	}{
		{
			name:          "explicit filesystem type",
			transportDir:  "/tmp/campfire/abc123",
			campfireID:    "abc123",
			transportType: "filesystem",
		},
		{
			name:          "empty transport_type, empty transport dir (legacy fallback)",
			transportDir:  "",
			campfireID:    "abc123",
			transportType: "",
		},
		{
			name:          "empty transport_type, fs path (legacy fallback)",
			transportDir:  "/tmp/campfire/abc123",
			campfireID:    "abc123",
			transportType: "",
		},
		{
			name:          "empty transport_type, non-github non-cbor path (legacy fallback)",
			transportDir:  "/some/other/dir",
			campfireID:    "xyz",
			transportType: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := store.Membership{
				CampfireID:    tc.campfireID,
				TransportDir:  tc.transportDir,
				TransportType: tc.transportType,
			}
			got := transport.ResolveType(m)
			if got != transport.TypeFilesystem {
				t.Errorf("ResolveType() = %v, want TypeFilesystem", got)
			}
		})
	}
}

// TestResolveType_PeerHTTP_NoCborFile verifies that a real directory without a
// .cbor file resolves as filesystem when TransportType is empty (legacy fallback).
// The old code would probe the filesystem here; the new code skips the probe.
func TestResolveType_PeerHTTP_NoCborFile(t *testing.T) {
	dir := t.TempDir()
	m := store.Membership{
		CampfireID:    "deadbeef",
		TransportDir:  dir,
		TransportType: "", // empty — legacy fallback, no os.Stat probe
	}
	got := transport.ResolveType(m)
	if got != transport.TypeFilesystem {
		t.Errorf("ResolveType() = %v, want TypeFilesystem (no cbor file, no explicit type)", got)
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
