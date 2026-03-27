package cmd

// Tests for campfire-agent-xsa: Trust v0.2 fingerprint comparison on join.
// Verifies that compareJoinedCampfire produces the correct trust status
// based on the joined campfire's declarations vs the local policy engine.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/trust"
)

// TestJoinFilesystem_ComparisonReport_NoCampfireDeclarations verifies that
// compareJoinedCampfire returns TrustUnknown when the joined campfire has no
// convention declarations (bare campfire, common case).
func TestJoinFilesystem_ComparisonReport_NoCampfireDeclarations(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CF_HOME", dir)
	cfHome = ""
	defer func() {
		cfHome = ""
		os.Unsetenv("CF_HOME")
	}()

	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := id.Save(filepath.Join(dir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire id: %v", err)
	}
	campfireID := cfID.PublicKeyHex()
	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: dir,
		JoinProtocol: "open",
		Role:         "member",
		JoinedAt:     1,
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	report := compareJoinedCampfire(s, campfireID)

	if report.OverallStatus != trust.TrustUnknown {
		t.Errorf("expected TrustUnknown for bare campfire, got %q", report.OverallStatus)
	}
	if len(report.Conventions) != 0 {
		t.Errorf("expected 0 conventions, got %d", len(report.Conventions))
	}
}

// TestJoinFilesystem_ComparisonReport_AdoptedDeclaration verifies that
// compareJoinedCampfire returns TrustAdopted when the joined campfire's
// convention declaration fingerprint matches the home policy.
func TestJoinFilesystem_ComparisonReport_AdoptedDeclaration(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CF_HOME", dir)
	cfHome = ""
	defer func() {
		cfHome = ""
		os.Unsetenv("CF_HOME")
	}()

	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := id.Save(filepath.Join(dir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	decl := &convention.Declaration{
		Convention: "social",
		Operation:  "post",
		Version:    "1.0",
		Signing:    "member_key",
		Args: []convention.ArgDescriptor{
			{Name: "body", Type: "string", Required: true},
		},
	}
	declBytes, err := json.Marshal(decl)
	if err != nil {
		t.Fatalf("marshalling decl: %v", err)
	}

	homeID := setupCampfireWithDecl(t, s, id, dir, declBytes)
	if err := naming.NewAliasStore(dir).Set("home", homeID); err != nil {
		t.Fatalf("setting home alias: %v", err)
	}

	joinedID := setupCampfireWithDecl(t, s, id, dir, declBytes)

	report := compareJoinedCampfire(s, joinedID)

	if report.OverallStatus != trust.TrustAdopted {
		t.Errorf("expected TrustAdopted, got %q", report.OverallStatus)
	}
	if !report.FingerprintMatch {
		t.Error("expected FingerprintMatch=true")
	}
	if len(report.Conventions) != 1 {
		t.Errorf("expected 1 convention entry, got %d", len(report.Conventions))
	}
}

// TestJoinFilesystem_ComparisonReport_DivergentDeclaration verifies that
// compareJoinedCampfire returns TrustDivergent when the joined campfire's
// declaration has a different fingerprint than what is in local policy.
// Both LocalFingerprint and RemoteFingerprint must be populated with sha256: prefix.
func TestJoinFilesystem_ComparisonReport_DivergentDeclaration(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CF_HOME", dir)
	cfHome = ""
	defer func() {
		cfHome = ""
		os.Unsetenv("CF_HOME")
	}()

	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := id.Save(filepath.Join(dir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	homeDecl := &convention.Declaration{
		Convention: "social",
		Operation:  "post",
		Version:    "1.0",
		Signing:    "member_key",
		Args: []convention.ArgDescriptor{
			{Name: "body", Type: "string", Required: true},
		},
	}
	homeDeclBytes, _ := json.Marshal(homeDecl)

	joinedDecl := &convention.Declaration{
		Convention: "social",
		Operation:  "post",
		Version:    "1.0",
		Signing:    "member_key",
		Args: []convention.ArgDescriptor{
			{Name: "content", Type: "string", Required: true},
		},
	}
	joinedDeclBytes, _ := json.Marshal(joinedDecl)

	homeID := setupCampfireWithDecl(t, s, id, dir, homeDeclBytes)
	if err := naming.NewAliasStore(dir).Set("home", homeID); err != nil {
		t.Fatalf("setting home alias: %v", err)
	}
	joinedID := setupCampfireWithDecl(t, s, id, dir, joinedDeclBytes)

	report := compareJoinedCampfire(s, joinedID)

	if report.OverallStatus != trust.TrustDivergent {
		t.Errorf("expected TrustDivergent, got %q", report.OverallStatus)
	}
	if report.FingerprintMatch {
		t.Error("expected FingerprintMatch=false")
	}
	if len(report.Conventions) != 1 {
		t.Fatalf("expected 1 convention, got %d", len(report.Conventions))
	}
	cc := report.Conventions[0]
	if !strings.HasPrefix(cc.LocalFingerprint, "sha256:") {
		t.Errorf("local fingerprint missing sha256: prefix: %q", cc.LocalFingerprint)
	}
	if !strings.HasPrefix(cc.RemoteFingerprint, "sha256:") {
		t.Errorf("remote fingerprint missing sha256: prefix: %q", cc.RemoteFingerprint)
	}
	if cc.LocalFingerprint == cc.RemoteFingerprint {
		t.Error("local and remote fingerprints should differ")
	}
}

// TestCompareJoinedCampfire_NoHomeAlias verifies that compareJoinedCampfire
// handles gracefully a campfire store without a "home" alias — returns TrustUnknown
// (not a panic or error) since local policy is empty.
func TestCompareJoinedCampfire_NoHomeAlias(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CF_HOME", dir)
	cfHome = ""
	defer func() {
		cfHome = ""
		os.Unsetenv("CF_HOME")
	}()

	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := id.Save(filepath.Join(dir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	decl := &convention.Declaration{
		Convention: "social",
		Operation:  "post",
		Version:    "1.0",
		Signing:    "member_key",
		Args: []convention.ArgDescriptor{
			{Name: "body", Type: "string", Required: true},
		},
	}
	declBytes, _ := json.Marshal(decl)
	joinedID := setupCampfireWithDecl(t, s, id, dir, declBytes)

	report := compareJoinedCampfire(s, joinedID)

	if report.OverallStatus != trust.TrustUnknown {
		t.Errorf("expected TrustUnknown (no home alias), got %q", report.OverallStatus)
	}
}

// setupCampfireWithDecl creates a campfire in the store with a single convention
// declaration message. Returns the campfireID.
func setupCampfireWithDecl(t *testing.T, s store.Store, id *identity.Identity, dir string, declBytes []byte) string {
	t.Helper()

	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire id: %v", err)
	}
	campfireID := cfID.PublicKeyHex()

	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: dir,
		JoinProtocol: "open",
		Role:         "member",
		JoinedAt:     1,
	}); err != nil {
		t.Fatalf("adding membership: %v", err)
	}

	msg, err := message.NewMessage(id.PrivateKey, id.PublicKey, declBytes, []string{convention.ConventionOperationTag}, nil)
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
		t.Fatalf("adding declaration message: %v", err)
	}

	return campfireID
}
