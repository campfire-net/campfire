package trust

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
)

// mockChainStore implements ChainStore for testing.
type mockChainStore struct {
	messages map[string][]store.MessageRecord // keyed by campfireID
}

func newMockChainStore() *mockChainStore {
	return &mockChainStore{messages: make(map[string][]store.MessageRecord)}
}

func (m *mockChainStore) ListMessages(campfireID string, afterTimestamp int64, filter ...store.MessageFilter) ([]store.MessageRecord, error) {
	msgs := m.messages[campfireID]
	if len(filter) == 0 {
		return msgs, nil
	}

	f := filter[0]
	if len(f.Tags) == 0 {
		return msgs, nil
	}

	var filtered []store.MessageRecord
	for _, msg := range msgs {
		if matchesTags(msg.Tags, f.Tags) {
			filtered = append(filtered, msg)
		}
	}
	return filtered, nil
}

func matchesTags(msgTags, filterTags []string) bool {
	tagSet := make(map[string]bool)
	for _, t := range msgTags {
		tagSet[t] = true
	}
	for _, t := range filterTags {
		if tagSet[t] {
			return true
		}
	}
	return false
}

// mockChainResolver implements ChainResolver for testing.
type mockChainResolver struct {
	rootRegistryID string
	err            error
}

func (m *mockChainResolver) ResolveRootRegistry(_ context.Context, _ string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.rootRegistryID, nil
}

// makeSignedRecord creates a message record with proper signature for the given fields.
func makeSignedRecord(campfireID string, priv ed25519.PrivateKey, pub ed25519.PublicKey, payload []byte, tags []string) store.MessageRecord {
	msg, err := message.NewMessage(priv, pub, payload, tags, nil)
	if err != nil {
		panic(fmt.Sprintf("makeSignedRecord: %v", err))
	}
	return store.MessageRecord{
		ID:          msg.ID,
		CampfireID:  campfireID,
		Sender:      hex.EncodeToString(pub),
		Payload:     msg.Payload,
		Tags:        msg.Tags,
		Antecedents: msg.Antecedents,
		Timestamp:   msg.Timestamp,
		Signature:   msg.Signature,
	}
}

func TestWalkChain_Valid(t *testing.T) {
	// Generate keys for root registry and convention registry.
	rootPub, rootPriv, _ := ed25519.GenerateKey(nil)
	convPub, convPriv, _ := ed25519.GenerateKey(nil)

	rootKeyHex := hex.EncodeToString(rootPub)
	rootRegistryID := "root-registry-campfire"
	convRegistryID := "conv-registry-campfire"

	ms := newMockChainStore()

	// Root registry has a message from the root key.
	rootMsg := makeSignedRecord(rootRegistryID, rootPriv, rootPub, []byte("hello"), []string{"general"})
	ms.messages[rootRegistryID] = append(ms.messages[rootRegistryID], rootMsg)

	// Root registry has a registration message for the convention registry, signed by root key.
	regMsg := makeSignedRecord(rootRegistryID, rootPriv, rootPub, []byte(convRegistryID), []string{"naming:registration"})
	ms.messages[rootRegistryID] = append(ms.messages[rootRegistryID], regMsg)

	// Convention registry has messages from convention key.
	convGenMsg := makeSignedRecord(convRegistryID, convPriv, convPub, []byte("conv-init"), []string{"general"})
	ms.messages[convRegistryID] = append(ms.messages[convRegistryID], convGenMsg)

	// Convention registry has declaration messages.
	declMsg := makeSignedRecord(convRegistryID, convPriv, convPub, []byte(`{"convention":"trust","operation":"verify"}`), []string{convention.ConventionOperationTag})
	ms.messages[convRegistryID] = append(ms.messages[convRegistryID], declMsg)

	resolver := &mockChainResolver{rootRegistryID: rootRegistryID}

	walker := NewChainWalker(rootKeyHex, ms, resolver)
	chain, err := walker.WalkChain(context.Background())
	if err != nil {
		t.Fatalf("WalkChain failed: %v", err)
	}

	if chain.RootKey != rootKeyHex {
		t.Errorf("RootKey = %s, want %s", chain.RootKey, rootKeyHex)
	}
	if chain.RootRegistryID != rootRegistryID {
		t.Errorf("RootRegistryID = %s, want %s", chain.RootRegistryID, rootRegistryID)
	}
	if chain.ConventionRegID != convRegistryID {
		t.Errorf("ConventionRegID = %s, want %s", chain.ConventionRegID, convRegistryID)
	}
	if len(chain.Declarations) != 1 {
		t.Errorf("Declarations count = %d, want 1", len(chain.Declarations))
	}
	if chain.VerifiedAt.IsZero() {
		t.Error("VerifiedAt is zero")
	}
	if chain.ExpiresAt.IsZero() {
		t.Error("ExpiresAt is zero")
	}
}

func TestWalkChain_BrokenAtRoot(t *testing.T) {
	// Root registry has messages from a different key.
	_, rootPriv, _ := ed25519.GenerateKey(nil)
	otherPub, otherPriv, _ := ed25519.GenerateKey(nil)

	// Claim this is the root key, but messages are signed by other key.
	fakeRootKeyHex := hex.EncodeToString(otherPub)
	_ = fakeRootKeyHex

	// Generate the "claimed" root key — different from what signs the messages.
	claimedRootPub, _, _ := ed25519.GenerateKey(nil)
	claimedRootKeyHex := hex.EncodeToString(claimedRootPub)

	rootRegistryID := "root-registry-campfire"

	ms := newMockChainStore()

	// Root registry has messages from the OTHER key (not the claimed root key).
	msg := makeSignedRecord(rootRegistryID, otherPriv, otherPub, []byte("hello"), []string{"general"})
	ms.messages[rootRegistryID] = append(ms.messages[rootRegistryID], msg)

	resolver := &mockChainResolver{rootRegistryID: rootRegistryID}

	walker := NewChainWalker(claimedRootKeyHex, ms, resolver)
	_, err := walker.WalkChain(context.Background())
	if err == nil {
		t.Fatal("WalkChain should fail when root key doesn't match")
	}

	_ = rootPriv // key generated but root messages use otherPriv
}

func TestWalkChain_BrokenAtConventionRegistry(t *testing.T) {
	rootPub, rootPriv, _ := ed25519.GenerateKey(nil)
	otherPub, otherPriv, _ := ed25519.GenerateKey(nil)

	rootKeyHex := hex.EncodeToString(rootPub)
	rootRegistryID := "root-registry-campfire"
	convRegistryID := "conv-registry-campfire"

	ms := newMockChainStore()

	// Root registry has a message from root key.
	rootMsg := makeSignedRecord(rootRegistryID, rootPriv, rootPub, []byte("hello"), []string{"general"})
	ms.messages[rootRegistryID] = append(ms.messages[rootRegistryID], rootMsg)

	// Registration message is signed by OTHER key (not root key) — should fail.
	regMsg := makeSignedRecord(rootRegistryID, otherPriv, otherPub, []byte(convRegistryID), []string{"naming:registration"})
	ms.messages[rootRegistryID] = append(ms.messages[rootRegistryID], regMsg)

	resolver := &mockChainResolver{rootRegistryID: rootRegistryID}

	walker := NewChainWalker(rootKeyHex, ms, resolver)
	_, err := walker.WalkChain(context.Background())
	if err == nil {
		t.Fatal("WalkChain should fail when registration not signed by root key")
	}
}

func TestWalkChain_BrokenAtDeclarations(t *testing.T) {
	rootPub, rootPriv, _ := ed25519.GenerateKey(nil)
	convPub, convPriv, _ := ed25519.GenerateKey(nil)
	attackerPub, attackerPriv, _ := ed25519.GenerateKey(nil)

	rootKeyHex := hex.EncodeToString(rootPub)
	rootRegistryID := "root-registry-campfire"
	convRegistryID := "conv-registry-campfire"

	ms := newMockChainStore()

	// Root registry setup (valid).
	rootMsg := makeSignedRecord(rootRegistryID, rootPriv, rootPub, []byte("hello"), []string{"general"})
	ms.messages[rootRegistryID] = append(ms.messages[rootRegistryID], rootMsg)

	regMsg := makeSignedRecord(rootRegistryID, rootPriv, rootPub, []byte(convRegistryID), []string{"naming:registration"})
	ms.messages[rootRegistryID] = append(ms.messages[rootRegistryID], regMsg)

	// Convention registry — first message establishes the key.
	convGenMsg := makeSignedRecord(convRegistryID, convPriv, convPub, []byte("conv-init"), []string{"general"})
	ms.messages[convRegistryID] = append(ms.messages[convRegistryID], convGenMsg)

	// Declaration signed by attacker (not the convention registry key).
	declMsg := makeSignedRecord(convRegistryID, attackerPriv, attackerPub, []byte(`{"convention":"trust","operation":"verify"}`), []string{convention.ConventionOperationTag})
	ms.messages[convRegistryID] = append(ms.messages[convRegistryID], declMsg)

	resolver := &mockChainResolver{rootRegistryID: rootRegistryID}

	walker := NewChainWalker(rootKeyHex, ms, resolver)
	chain, err := walker.WalkChain(context.Background())
	if err != nil {
		t.Fatalf("WalkChain should succeed but with 0 verified declarations, got error: %v", err)
	}
	// Declarations signed by the attacker should NOT be included.
	if len(chain.Declarations) != 0 {
		t.Errorf("Declarations count = %d, want 0 (attacker-signed declarations should be filtered)", len(chain.Declarations))
	}
}

func TestChainCache_TTL(t *testing.T) {
	rootPub, rootPriv, _ := ed25519.GenerateKey(nil)
	convPub, convPriv, _ := ed25519.GenerateKey(nil)

	rootKeyHex := hex.EncodeToString(rootPub)
	rootRegistryID := "root-registry"
	convRegistryID := "conv-registry"

	ms := newMockChainStore()

	rootMsg := makeSignedRecord(rootRegistryID, rootPriv, rootPub, []byte("hello"), []string{"general"})
	ms.messages[rootRegistryID] = append(ms.messages[rootRegistryID], rootMsg)

	regMsg := makeSignedRecord(rootRegistryID, rootPriv, rootPub, []byte(convRegistryID), []string{"naming:registration"})
	ms.messages[rootRegistryID] = append(ms.messages[rootRegistryID], regMsg)

	convGenMsg := makeSignedRecord(convRegistryID, convPriv, convPub, []byte("conv-init"), []string{"general"})
	ms.messages[convRegistryID] = append(ms.messages[convRegistryID], convGenMsg)

	resolver := &mockChainResolver{rootRegistryID: rootRegistryID}

	// Use minimum TTL for fast test.
	walker := NewChainWalker(rootKeyHex, ms, resolver, WithCacheTTL(30*time.Second))

	ctx := context.Background()

	// First walk — should populate cache.
	chain1, err := walker.WalkChain(ctx)
	if err != nil {
		t.Fatalf("first WalkChain: %v", err)
	}

	// Second walk — should return cached result (same VerifiedAt).
	chain2, err := walker.WalkChain(ctx)
	if err != nil {
		t.Fatalf("second WalkChain: %v", err)
	}

	if !chain1.VerifiedAt.Equal(chain2.VerifiedAt) {
		t.Error("expected cached chain (same VerifiedAt)")
	}

	// Invalidate cache and force a re-walk by manipulating the cache entry.
	walker.mu.Lock()
	if entry, ok := walker.cache[rootKeyHex]; ok {
		entry.expiresAt = time.Now().Add(-1 * time.Second) // expire it
	}
	// Also clear rate limit to allow re-walk.
	delete(walker.lastWalkTime, rootKeyHex)
	walker.mu.Unlock()

	chain3, err := walker.WalkChain(ctx)
	if err != nil {
		t.Fatalf("third WalkChain after cache expiry: %v", err)
	}

	if chain3.VerifiedAt.Equal(chain1.VerifiedAt) {
		t.Error("expected fresh chain after cache expiry (different VerifiedAt)")
	}
}

func TestChainCache_RateLimit(t *testing.T) {
	rootPub, rootPriv, _ := ed25519.GenerateKey(nil)
	convPub, convPriv, _ := ed25519.GenerateKey(nil)

	rootKeyHex := hex.EncodeToString(rootPub)
	rootRegistryID := "root-registry"
	convRegistryID := "conv-registry"

	ms := newMockChainStore()

	rootMsg := makeSignedRecord(rootRegistryID, rootPriv, rootPub, []byte("hello"), []string{"general"})
	ms.messages[rootRegistryID] = append(ms.messages[rootRegistryID], rootMsg)

	regMsg := makeSignedRecord(rootRegistryID, rootPriv, rootPub, []byte(convRegistryID), []string{"naming:registration"})
	ms.messages[rootRegistryID] = append(ms.messages[rootRegistryID], regMsg)

	convGenMsg := makeSignedRecord(convRegistryID, convPriv, convPub, []byte("conv-init"), []string{"general"})
	ms.messages[convRegistryID] = append(ms.messages[convRegistryID], convGenMsg)

	resolver := &mockChainResolver{rootRegistryID: rootRegistryID}
	walker := NewChainWalker(rootKeyHex, ms, resolver, WithCacheTTL(30*time.Second))

	ctx := context.Background()

	// First walk.
	chain1, err := walker.WalkChain(ctx)
	if err != nil {
		t.Fatalf("first WalkChain: %v", err)
	}

	// Expire cache but keep rate limit.
	walker.mu.Lock()
	if entry, ok := walker.cache[rootKeyHex]; ok {
		entry.expiresAt = time.Now().Add(-1 * time.Second)
	}
	// lastWalkTime is recent, so rate limit should kick in.
	walker.mu.Unlock()

	// Second walk — rate-limited, should return stale cached result.
	chain2, err := walker.WalkChain(ctx)
	if err != nil {
		t.Fatalf("second WalkChain (rate-limited): %v", err)
	}

	if !chain1.VerifiedAt.Equal(chain2.VerifiedAt) {
		t.Error("expected rate-limited walk to return cached chain")
	}
}

func TestChainStatus_RegistryCampfires(t *testing.T) {
	rootPub, rootPriv, _ := ed25519.GenerateKey(nil)
	convPub, convPriv, _ := ed25519.GenerateKey(nil)

	rootKeyHex := hex.EncodeToString(rootPub)
	rootRegistryID := "root-registry-status"
	convRegistryID := "conv-registry-status"

	ms := newMockChainStore()

	rootMsg := makeSignedRecord(rootRegistryID, rootPriv, rootPub, []byte("hello"), []string{"general"})
	ms.messages[rootRegistryID] = append(ms.messages[rootRegistryID], rootMsg)

	regMsg := makeSignedRecord(rootRegistryID, rootPriv, rootPub, []byte(convRegistryID), []string{"naming:registration"})
	ms.messages[rootRegistryID] = append(ms.messages[rootRegistryID], regMsg)

	convGenMsg := makeSignedRecord(convRegistryID, convPriv, convPub, []byte("conv-init"), []string{"general"})
	ms.messages[convRegistryID] = append(ms.messages[convRegistryID], convGenMsg)

	resolver := &mockChainResolver{rootRegistryID: rootRegistryID}
	walker := NewChainWalker(rootKeyHex, ms, resolver)

	ctx := context.Background()

	// Root registry itself should be TrustVerified.
	status, err := walker.ChainStatus(ctx, rootRegistryID)
	if err != nil {
		t.Fatalf("ChainStatus(rootRegistry): %v", err)
	}
	if status != TrustVerified {
		t.Errorf("root registry: expected TrustVerified, got %q", status)
	}

	// Convention registry itself should be TrustVerified.
	status, err = walker.ChainStatus(ctx, convRegistryID)
	if err != nil {
		t.Fatalf("ChainStatus(convRegistry): %v", err)
	}
	if status != TrustVerified {
		t.Errorf("convention registry: expected TrustVerified, got %q", status)
	}

	// An unrelated campfire should be TrustCrossRoot (chain valid but campfire not in registry).
	status, err = walker.ChainStatus(ctx, "some-other-campfire")
	if err != nil {
		t.Fatalf("ChainStatus(other): %v", err)
	}
	if status != TrustCrossRoot {
		t.Errorf("other campfire: expected TrustCrossRoot, got %q", status)
	}
}

func TestChainStatus_BrokenChain(t *testing.T) {
	// Resolver fails — chain cannot be walked.
	resolver := &mockChainResolver{err: fmt.Errorf("no root registry")}
	ms := newMockChainStore()
	walker := NewChainWalker("deadbeef", ms, resolver)

	status, err := walker.ChainStatus(context.Background(), "any-campfire")
	// ChainStatus swallows chain errors and returns TrustUnverified.
	if err != nil {
		t.Fatalf("ChainStatus with broken chain should not return error: %v", err)
	}
	if status != TrustUnverified {
		t.Errorf("broken chain: expected TrustUnverified, got %q", status)
	}
}
