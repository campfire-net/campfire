// gen/main.go — Fixture generator for D6 owner-ceiling conformance cases 13a and 13b.
//
// Generates two CBOR GrantPayload fixtures under testdata/chains/:
//
//   13-owner-blanket-deny.cbor       — case 13a (glob pattern "ready:*")
//   13b-owner-blanket-deny-exact.cbor — case 13b (exact pattern "ready:claim")
//
// Both fixtures encode the SAME GrantPayload: a one-hop grant from a deterministic
// root to a deterministic sender key, granting convention="ready", op_pattern="claim",
// not expired at canonical t0 per §5.4.
//
// The distinction between cases 13a and 13b is in the OwnerPolicy.BlanketDeny
// patterns used at evaluation time, NOT in the grant chain. Both cases test that
// the owner ceiling fires before the grant chain is evaluated.
//
// Run from the module root:
//
//	cd ~/projects/campfire
//	go run ./cf-conventions/cf-authority/trust/conformance/gen/
//
// Or from conformance/gen/:
//
//	go run .
package main

import (
	"crypto/ed25519"
	"fmt"
	"os"
	"path/filepath"

	"github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"
)

// Canonical t0 per §5.4: 2026-01-01T00:00:00Z = 1767225600000000000 ns
const canonicalT0 int64 = 1767225600000000000

// 24h in nanoseconds — grant expires 24h after canonical t0.
const oneDay int64 = 86_400_000_000_000

func main() {
	// Deterministic sender key from fixed seed (same seed used in cbor_test.go).
	// Seed bytes: 1, 2, 3, ..., 32.
	senderSeed := make([]byte, 32)
	for i := range senderSeed {
		senderSeed[i] = byte(i + 1)
	}
	senderPriv := ed25519.NewKeyFromSeed(senderSeed)
	senderPub := senderPriv.Public().(ed25519.PublicKey)

	// Deterministic nonce (16 bytes): 7, 8, 9, ..., 22.
	nonce := make([]byte, 16)
	for i := range nonce {
		nonce[i] = byte(i + 7)
	}

	// Capability: convention="ready", op_pattern="claim", until=t0+24h.
	cap := trust.Capability{
		Convention: "ready",
		OpPattern:  "claim",
		Where:      []trust.WhereMatcherCBOR{}, // no where restriction
		Bounds:     map[string]any{},            // empty bounds
		Until:      canonicalT0 + oneDay,        // expires 24h after canonical t0
		Nonce:      nonce,
	}

	// GrantPayload: owner-root grant (ParentGrantID=nil, Depth=0).
	gp := trust.GrantPayload{
		ParentGrantID: nil,          // owner-root grant — no parent
		ChildPubkey:   senderPub,    // 32-byte Ed25519 public key
		Capabilities:  []trust.Capability{cap},
		Depth:         0,            // owner-root grant
	}

	encoded, err := trust.MarshalGrantPayloadCBOR(gp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gen: MarshalGrantPayloadCBOR: %v\n", err)
		os.Exit(1)
	}

	// Both case 13a (glob) and 13b (exact) use the same GrantPayload.
	// The owner-ceiling pattern difference is a test-setup concern, not a chain concern.
	outDir := filepath.Join("testdata", "chains")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "gen: MkdirAll: %v\n", err)
		os.Exit(1)
	}

	fixtures := []string{
		"13-owner-blanket-deny.cbor",
		"13b-owner-blanket-deny-exact.cbor",
	}
	for _, name := range fixtures {
		path := filepath.Join(outDir, name)
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "gen: WriteFile %q: %v\n", path, err)
			os.Exit(1)
		}
		fmt.Printf("wrote %s (%d bytes)\n", path, len(encoded))
	}
	fmt.Println("done")
}
