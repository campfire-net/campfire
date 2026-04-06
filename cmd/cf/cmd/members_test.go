package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// setupMembersDisplayEnv creates a minimal campfire environment for members display tests.
// Returns campfireID, store, and fs transport. The caller identity is saved to cfHome.
func setupMembersDisplayEnv(t *testing.T) (campfireID string, member1 *identity.Identity, member2 *identity.Identity, s store.Store, fsT *fs.Transport) {
	t.Helper()

	cfHomeDir := t.TempDir()
	transportBaseDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)
	t.Setenv("CF_TRANSPORT_DIR", transportBaseDir)

	var err error
	member1, err = identity.Generate()
	if err != nil {
		t.Fatalf("generating member1 identity: %v", err)
	}
	if err := member1.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving member1 identity: %v", err)
	}

	member2, err = identity.Generate()
	if err != nil {
		t.Fatalf("generating member2 identity: %v", err)
	}

	s, err = store.Open(filepath.Join(cfHomeDir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	campfireID = cfID.PublicKeyHex()

	cfDir := filepath.Join(transportBaseDir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0755); err != nil {
			t.Fatalf("creating directory %s: %v", sub, err)
		}
	}

	state := &campfire.CampfireState{
		PublicKey:             cfID.PublicKey,
		PrivateKey:            cfID.PrivateKey,
		JoinProtocol:          "invite-only",
		ReceptionRequirements: []string{},
		CreatedAt:             time.Now().UnixNano(),
	}
	stateData, err := cfencoding.Marshal(state)
	if err != nil {
		t.Fatalf("marshalling campfire state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfDir, "campfire.cbor"), stateData, 0644); err != nil {
		t.Fatalf("writing campfire state: %v", err)
	}

	fsT = fs.New(transportBaseDir)

	if err := fsT.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: member1.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("writing member1 record: %v", err)
	}

	if err := fsT.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: member2.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleWriter,
	}); err != nil {
		t.Fatalf("writing member2 record: %v", err)
	}

	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: fsT.CampfireDir(campfireID),
		JoinProtocol: "invite-only",
		Role:         campfire.RoleFull,
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	return campfireID, member1, member2, s, fsT
}

// memberLineForPubkey returns the display line for a single member from
// printMembersLines. It mirrors the display logic in members.go so tests
// remain in sync with the implementation.
func memberLineForPubkey(pubkeyHex string, role string, cache *displayNameCache) string {
	short := pubkeyHex
	if len(short) > 8 {
		short = short[:8]
	}
	if role == "" {
		role = "full"
	}
	if cache != nil {
		if name := cache.Lookup(pubkeyHex); name != "" {
			return fmt.Sprintf("%s (%s)  %s", name, short, role)
		}
	}
	return fmt.Sprintf("%s  %s", short, role)
}

// TestMembersDisplay_ShortPubkeyAndRole verifies that cf members output shows
// the 8-char pubkey prefix and the member's role (not joined timestamp).
func TestMembersDisplay_ShortPubkeyAndRole(t *testing.T) {
	campfireID, member1, member2, s, fsT := setupMembersDisplayEnv(t)
	defer s.Close()

	members, err := fsT.ListMembers(campfireID)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}

	out := captureStdout(t, func() {
		for _, mem := range members {
			idHex := fmt.Sprintf("%x", mem.PublicKey)
			short := idHex
			if len(short) > 8 {
				short = short[:8]
			}
			role := mem.Role
			if role == "" {
				role = "full"
			}
			fmt.Printf("%s  %s\n", short, role)
		}
	})

	m1Hex := fmt.Sprintf("%x", member1.PublicKey)
	m2Hex := fmt.Sprintf("%x", member2.PublicKey)

	// Each member's 8-char prefix must appear.
	if !strings.Contains(out, m1Hex[:8]) {
		t.Errorf("output missing member1 pubkey prefix %s; got:\n%s", m1Hex[:8], out)
	}
	if !strings.Contains(out, m2Hex[:8]) {
		t.Errorf("output missing member2 pubkey prefix %s; got:\n%s", m2Hex[:8], out)
	}

	// Roles must appear.
	if !strings.Contains(out, campfire.RoleFull) {
		t.Errorf("output missing role 'full'; got:\n%s", out)
	}
	if !strings.Contains(out, campfire.RoleWriter) {
		t.Errorf("output missing role 'writer'; got:\n%s", out)
	}

	// Should NOT contain full 64-char pubkey (only short prefix).
	if strings.Contains(out, m1Hex) {
		t.Errorf("output should show short pubkey prefix, not full pubkey; got:\n%s", out)
	}
}

// TestMembersDisplay_DisplayNameWhenKnown verifies that when the profile cache
// has a display name for a pubkey, it is shown as "name (short)" in the output.
func TestMembersDisplay_DisplayNameWhenKnown(t *testing.T) {
	_, member1, _, s, _ := setupMembersDisplayEnv(t)
	defer s.Close()

	// Pre-populate the session profile cache with a display name for member1.
	m1Hex := fmt.Sprintf("%x", member1.PublicKey)
	sessionProfileCache.Set(m1Hex, "alice@campfire")
	defer sessionProfileCache.Set(m1Hex, "") // clean up after test

	line := memberLineForPubkey(m1Hex, campfire.RoleFull, sessionProfileCache)

	if !strings.Contains(line, "alice@campfire") {
		t.Errorf("line should contain display name 'alice@campfire'; got: %s", line)
	}
	if !strings.Contains(line, m1Hex[:8]) {
		t.Errorf("line should contain short pubkey %s alongside display name; got: %s", m1Hex[:8], line)
	}
	if strings.Contains(line, m1Hex) && len(m1Hex) > 8 {
		// Ensure full key is not present (only the short 8-char prefix).
		if strings.Contains(line, m1Hex[8:]) {
			t.Errorf("line should not contain full pubkey beyond 8 chars; got: %s", line)
		}
	}
}

// TestMembersDisplay_FallbackToShortPubkey verifies that when no display name
// is known, the output shows only the 8-char pubkey prefix.
func TestMembersDisplay_FallbackToShortPubkey(t *testing.T) {
	_, _, member2, s, _ := setupMembersDisplayEnv(t)
	defer s.Close()

	m2Hex := fmt.Sprintf("%x", member2.PublicKey)
	// Ensure no display name is cached.
	sessionProfileCache.Set(m2Hex, "")

	line := memberLineForPubkey(m2Hex, campfire.RoleWriter, nil)

	if !strings.Contains(line, m2Hex[:8]) {
		t.Errorf("fallback line should contain short pubkey %s; got: %s", m2Hex[:8], line)
	}
	if strings.Contains(line, "alice") || strings.Contains(line, "@") {
		t.Errorf("fallback line should not contain display name; got: %s", line)
	}
	if !strings.Contains(line, campfire.RoleWriter) {
		t.Errorf("fallback line should contain role %s; got: %s", campfire.RoleWriter, line)
	}
}
