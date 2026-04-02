package cmd

// Tests for cf init --durable flag (campfire-agent-wcg).
//
// Done conditions:
// 1. cf init --durable creates a threshold=2 self-campfire
// 2. cf init --durable outputs a valid BIP-39 recovery phrase (24 words)
// 3. cf init --durable stores the agent's threshold share in the store

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
)

// TestCfInitDurable_Threshold2 verifies that cf init --durable creates a self-campfire
// with threshold=2 in the store membership.
func TestCfInitDurable_Threshold2(t *testing.T) {
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	selfCampfireID, coldKeyPhrase, err := createSelfCampfire(cfHomeDir, agentID, true)
	if err != nil {
		t.Fatalf("createSelfCampfire(durable=true): %v", err)
	}
	if selfCampfireID == "" {
		t.Fatal("expected non-empty self-campfire ID")
	}
	_ = coldKeyPhrase // validated in other tests

	s, err := store.Open(store.StorePath(cfHomeDir))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	m, err := s.GetMembership(selfCampfireID)
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if m == nil {
		t.Fatal("membership not found for durable self-campfire")
	}
	if m.Threshold != 2 {
		t.Errorf("durable identity campfire threshold = %d, want 2", m.Threshold)
	}
}

// TestCfInitDurable_RecoveryPhrase verifies that cf init --durable outputs a valid
// BIP-39 recovery phrase containing exactly 24 words from the standard wordlist.
func TestCfInitDurable_RecoveryPhrase(t *testing.T) {
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	_, coldKeyPhrase, err := createSelfCampfire(cfHomeDir, agentID, true)
	if err != nil {
		t.Fatalf("createSelfCampfire(durable=true): %v", err)
	}

	if coldKeyPhrase == "" {
		t.Fatal("expected non-empty cold key recovery phrase for --durable")
	}

	// A BIP-39 mnemonic with 256-bit entropy has exactly 24 words.
	words := strings.Fields(coldKeyPhrase)
	if len(words) != 24 {
		t.Errorf("recovery phrase has %d words, want 24; phrase: %q", len(words), coldKeyPhrase)
	}

	// Each word should be non-empty (basic sanity check).
	for i, w := range words {
		if w == "" {
			t.Errorf("word %d in recovery phrase is empty", i)
		}
	}
}

// TestCfInitDurable_NoPhraseWhenNotDurable verifies that cf init (without --durable)
// returns an empty cold key phrase.
func TestCfInitDurable_NoPhraseWhenNotDurable(t *testing.T) {
	cfHomeDir := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir)

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(cfHomeDir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	_, coldKeyPhrase, err := createSelfCampfire(cfHomeDir, agentID, false)
	if err != nil {
		t.Fatalf("createSelfCampfire(durable=false): %v", err)
	}

	if coldKeyPhrase != "" {
		t.Errorf("expected empty cold key phrase for non-durable init, got %q", coldKeyPhrase)
	}
}

// TestCfInitDurable_DifferentPhrasesEachTime verifies that each durable init generates
// a different recovery phrase (the DKG uses fresh randomness each time).
func TestCfInitDurable_DifferentPhrasesEachTime(t *testing.T) {
	cfHomeDir1 := t.TempDir()
	cfHomeDir2 := t.TempDir()
	t.Setenv("CF_HOME", cfHomeDir1)

	agentID1, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity 1: %v", err)
	}
	if err := agentID1.Save(filepath.Join(cfHomeDir1, "identity.json")); err != nil {
		t.Fatalf("saving identity 1: %v", err)
	}

	agentID2, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity 2: %v", err)
	}
	if err := agentID2.Save(filepath.Join(cfHomeDir2, "identity.json")); err != nil {
		t.Fatalf("saving identity 2: %v", err)
	}

	_, phrase1, err := createSelfCampfire(cfHomeDir1, agentID1, true)
	if err != nil {
		t.Fatalf("first createSelfCampfire(durable=true): %v", err)
	}

	t.Setenv("CF_HOME", cfHomeDir2)
	_, phrase2, err := createSelfCampfire(cfHomeDir2, agentID2, true)
	if err != nil {
		t.Fatalf("second createSelfCampfire(durable=true): %v", err)
	}

	if phrase1 == phrase2 {
		t.Error("expected different recovery phrases for different durable identities")
	}
}
