package naming_test

// post_join_verify_test.go — §11 post-join verification tests.
//
// Covers:
//   §11.3 probe-write-then-observe (happy path, send-rejected, latency)
//   §11.3.1 mechanical send-ack distinction
//   §11.5.1 canonical signing payload byte sequence
//   §11.6 honeypot detection scenarios (send-rejected suppression + latency)
//   §11.7 verification at every hop (ResolveChain)
//   Probe timeout config override
//   Tier2VerifierFunc injection

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	discovery "github.com/campfire-net/campfire/cf-conventions/cf-discovery"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/pkg/naming"
)

// --- stub Tier2Verifier for test injection ---

type stubVerifier struct {
	probeErr       error
	probeCallCount int
	unjoinCalled   bool
	probeMsgID     string
}

func (s *stubVerifier) ProbeAndObserve(_ context.Context, _ string, _ time.Duration) error {
	s.probeCallCount++
	return s.probeErr
}

func (s *stubVerifier) PostUnjoinDeclaration(_ context.Context, _ string, probeMsgID string) error {
	s.unjoinCalled = true
	s.probeMsgID = probeMsgID
	return nil
}

func newStubVerifierFunc(stub *stubVerifier) func(*protocol.Client) discovery.Tier2Verifier {
	return func(_ *protocol.Client) discovery.Tier2Verifier {
		return stub
	}
}

// --- §11.3 happy path ---

// TestPostJoinVerification_HappyPath verifies that a successful probe-write-then-observe
// cycle results in a nil error and membership is retained.
func TestPostJoinVerification_HappyPath(t *testing.T) {
	clientA, clientB, campfireID, campfireTransportDir, beaconDir := setupTwoCampfireClients(t)
	ctx := context.Background()

	// Register a name so clientB has something to resolve.
	target, err := clientA.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: t.TempDir()},
		BeaconDir: beaconDir,
	})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}
	if _, err := naming.Register(ctx, clientA, campfireID, "svc", target.CampfireID, nil); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Use the real verifier with a short probe timeout (filesystem is fast).
	opts := naming.ResolverClientOptions{
		BeaconDir:    beaconDir,
		ProbeTimeout: 3 * time.Second,
		ConfigTransportFunc: func(id string) protocol.Transport {
			if id == campfireID {
				return &protocol.FilesystemTransport{Dir: campfireTransportDir}
			}
			return nil
		},
	}

	resolver := naming.NewResolverFromClient(clientB, campfireID, opts)
	result, err := resolver.ResolveURI(ctx, "cf://svc")
	if err != nil {
		t.Fatalf("ResolveURI: %v", err)
	}
	if result.CampfireID != target.CampfireID {
		t.Errorf("CampfireID = %s, want %s", result.CampfireID, target.CampfireID)
	}

	// clientB should be a member (probe passed, membership retained).
	m, err := clientB.GetMembership(campfireID)
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if m == nil {
		t.Error("clientB should be a member after successful post-join verification")
	}
}

// --- §11.6 + §11.3.1 send-rejected suppression ---

// TestPostJoinVerification_SendRejected tests §11.6 scenario: the transport itself
// rejects the probe write (Send returns error). This triggers send-not-acknowledged
// → ErrPostJoinVerificationFailed → unjoin-declaration is invoked → client leaves.
//
// This is the mechanical distinction from §11.3.1: transport-level rejection is
// unambiguously suppression.
func TestPostJoinVerification_SendRejected(t *testing.T) {
	_, clientB, campfireID, campfireTransportDir, beaconDir := setupTwoCampfireClients(t)
	ctx := context.Background()

	stub := &stubVerifier{
		probeErr: discovery.ErrPostJoinVerificationFailed,
	}

	opts := naming.ResolverClientOptions{
		BeaconDir:         beaconDir,
		ProbeTimeout:      1 * time.Second,
		Tier2VerifierFunc: newStubVerifierFunc(stub),
		ConfigTransportFunc: func(id string) protocol.Transport {
			if id == campfireID {
				return &protocol.FilesystemTransport{Dir: campfireTransportDir}
			}
			return nil
		},
	}

	resolver := naming.NewResolverFromClient(clientB, campfireID, opts)
	_, err := resolver.ResolveURI(ctx, "cf://anything")
	if err == nil {
		t.Fatal("expected error for send-rejected suppression")
	}
	if !errors.Is(err, discovery.ErrPostJoinVerificationFailed) && !containsStr(err.Error(), "post-join verification failed") {
		t.Errorf("expected ErrPostJoinVerificationFailed, got: %v", err)
	}

	// Probe was called.
	if stub.probeCallCount == 0 {
		t.Error("ProbeAndObserve should have been called")
	}
	// Unjoin declaration was posted.
	if !stub.unjoinCalled {
		t.Error("PostUnjoinDeclaration should have been called on send-rejected failure")
	}

	// clientB should NOT be a member (left after failure).
	m, err2 := clientB.GetMembership(campfireID)
	if err2 != nil {
		t.Fatalf("GetMembership: %v", err2)
	}
	if m != nil {
		t.Error("clientB should have left the campfire after verification failure")
	}
}

// --- §11.6 + §11.3.1 send-ack / latency path ---

// TestPostJoinVerification_SendAckObserveTimeout tests §11.3.1 latency path:
// the campfire ACCEPTS the write (Send succeeds) but observation times out.
// Per §11.3.1, this is treated as latency, NOT suppression.
// → autoJoin returns nil (no unjoin, membership retained).
//
// §11.3.1 limitation: this same path is taken for enforcement-mode campfires
// that accept-then-suppress after ACK. The mechanical distinction cannot
// differentiate these cases — only transport-layer rejection (above) is
// unambiguous suppression. This is the explicitly acknowledged trade-off.
func TestPostJoinVerification_SendAckObserveTimeout_Latency(t *testing.T) {
	clientA, clientB, campfireID, campfireTransportDir, beaconDir := setupTwoCampfireClients(t)
	ctx := context.Background()

	// Register a name so resolution can proceed past the autoJoin step.
	target, err := clientA.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: t.TempDir()},
		BeaconDir: beaconDir,
	})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}
	if _, err := naming.Register(ctx, clientA, campfireID, "svc", target.CampfireID, nil); err != nil {
		t.Fatalf("Register: %v", err)
	}

	stub := &stubVerifier{
		probeErr: discovery.ErrPostJoinVerificationLatency,
	}

	opts := naming.ResolverClientOptions{
		BeaconDir:         beaconDir,
		ProbeTimeout:      1 * time.Second,
		Tier2VerifierFunc: newStubVerifierFunc(stub),
		ConfigTransportFunc: func(id string) protocol.Transport {
			if id == campfireID {
				return &protocol.FilesystemTransport{Dir: campfireTransportDir}
			}
			return nil
		},
	}

	resolver := naming.NewResolverFromClient(clientB, campfireID, opts)
	result, err := resolver.ResolveURI(ctx, "cf://svc")
	if err != nil {
		t.Fatalf("ResolveURI should succeed in latency path: %v", err)
	}
	if result.CampfireID != target.CampfireID {
		t.Errorf("CampfireID = %s, want %s", result.CampfireID, target.CampfireID)
	}

	// Unjoin declaration must NOT be called in the latency path.
	if stub.unjoinCalled {
		t.Error("PostUnjoinDeclaration must NOT be called in the latency path (§11.3.1)")
	}

	// clientB should still be a member (latency path does not unjoin).
	m, err2 := clientB.GetMembership(campfireID)
	if err2 != nil {
		t.Fatalf("GetMembership: %v", err2)
	}
	if m == nil {
		t.Error("clientB should remain a member in the latency path (§11.3.1)")
	}
}

// TestPostJoinVerification_MechanicalDistinction demonstrates the §11.3.1 trade-off
// explicitly: the two paths (send-rejected vs. send-ack-observe-timeout) are tested
// side-by-side with assertions documenting the known limitation.
func TestPostJoinVerification_MechanicalDistinction(t *testing.T) {
	// Path 1: transport-level rejection → unambiguous suppression → unjoin.
	t.Run("send-rejected-triggers-unjoin", func(t *testing.T) {
		_, clientB, campfireID, campfireTransportDir, beaconDir := setupTwoCampfireClients(t)
		stub := &stubVerifier{probeErr: discovery.ErrPostJoinVerificationFailed}
		opts := naming.ResolverClientOptions{
			BeaconDir:         beaconDir,
			Tier2VerifierFunc: newStubVerifierFunc(stub),
			ConfigTransportFunc: func(id string) protocol.Transport {
				if id == campfireID {
					return &protocol.FilesystemTransport{Dir: campfireTransportDir}
				}
				return nil
			},
		}
		resolver := naming.NewResolverFromClient(clientB, campfireID, opts)
		_, err := resolver.ResolveURI(context.Background(), "cf://anything")
		if err == nil || (!errors.Is(err, discovery.ErrPostJoinVerificationFailed) && !containsStr(err.Error(), "verification failed")) {
			t.Errorf("send-rejected must yield verification failed, got: %v", err)
		}
		if !stub.unjoinCalled {
			t.Error("unjoin-declaration must fire on send-rejected")
		}
		m, _ := clientB.GetMembership(campfireID)
		if m != nil {
			t.Error("client must have left on send-rejected")
		}
	})

	// Path 2: send-ack but observe-timeout → ambiguous (latency OR enforcement-mode
	// after ACK). §11.3.1 explicitly accepts this ambiguity: treat as latency, do NOT unjoin.
	// LIMITATION: enforcement-mode campfires that ACK then suppress fall into this path.
	// They will NOT trigger unjoin unless the operator extends probe_timeout long enough
	// that the campfire's buffering/delay is exceeded.
	t.Run("send-ack-observe-timeout-is-latency-not-suppression", func(t *testing.T) {
		_, clientB, campfireID, campfireTransportDir, beaconDir := setupTwoCampfireClients(t)
		stub := &stubVerifier{probeErr: discovery.ErrPostJoinVerificationLatency}
		opts := naming.ResolverClientOptions{
			BeaconDir:         beaconDir,
			Tier2VerifierFunc: newStubVerifierFunc(stub),
			ConfigTransportFunc: func(id string) protocol.Transport {
				if id == campfireID {
					return &protocol.FilesystemTransport{Dir: campfireTransportDir}
				}
				return nil
			},
		}
		// Need a name to resolve past auto-join.
		clientA, _, _, _, _ := setupTwoCampfireClients(t)
		target, _ := clientA.Create(protocol.CreateRequest{Transport: protocol.FilesystemTransport{Dir: t.TempDir()}, BeaconDir: beaconDir})
		_ = target // illustrative — the resolve will fail at name lookup but we only care about auto-join behaviour

		// The key assertion: autoJoin must return nil (not error) in latency path.
		// We test this by calling ResolveURI and checking that the error, if any, is NOT
		// ErrPostJoinVerificationFailed — it should be a resolution error (name not found),
		// not a verification failure.
		resolver := naming.NewResolverFromClient(clientB, campfireID, opts)
		_, resolveErr := resolver.ResolveURI(context.Background(), "cf://anything")
		if errors.Is(resolveErr, discovery.ErrPostJoinVerificationFailed) {
			t.Error("latency path must NOT propagate ErrPostJoinVerificationFailed")
		}
		if stub.unjoinCalled {
			t.Error("unjoin-declaration must NOT fire in latency path")
		}
		m, _ := clientB.GetMembership(campfireID)
		if m == nil {
			t.Error("client must remain a member in the latency path")
		}
	})
}

// --- §11.5.1 canonical signing payload ---

// TestUnjoinDeclaration_CanonicalPayload verifies that the canonical JSON byte sequence
// produced by buildCanonicalUnjoinJSON matches the §11.5.1 spec example exactly.
//
// Spec example from line 707 of cf-discovery-spec.md:
//
//	discovery:unjoin-declaration\n
//	{"campfire_id":"a1b2c3d4e5f6","joiner_pubkey":"4a6f686e446f65456432353531394b657948657846656564426162654361666500","observed_inconsistency":"probe message not visible on read after join","probe_msg_id":"msg-7890abcdef12","reason":"probe-verification-failed"}
func TestUnjoinDeclaration_CanonicalPayload(t *testing.T) {
	campfireID := "a1b2c3d4e5f6"
	joinerPubkey := "4a6f686e446f65456432353531394b657948657846656564426162654361666500"
	probeMsgID := "msg-7890abcdef12"

	// Expected canonical JSON from §11.5.1 example.
	wantJSON := `{"campfire_id":"a1b2c3d4e5f6","joiner_pubkey":"4a6f686e446f65456432353531394b657948657846656564426162654361666500","observed_inconsistency":"probe message not visible on read after join","probe_msg_id":"msg-7890abcdef12","reason":"probe-verification-failed"}`

	// Expected full signing payload: prefix + LF + canonical JSON.
	wantPayload := "discovery:unjoin-declaration\n" + wantJSON

	// Use the exported CanonicalUnjoinSigningPayload helper.
	gotPayload := discovery.CanonicalUnjoinSigningPayload(campfireID, joinerPubkey, probeMsgID)

	if !bytes.Equal(gotPayload, []byte(wantPayload)) {
		t.Errorf("canonical signing payload mismatch:\n got: %q\nwant: %q", string(gotPayload), wantPayload)
	}
}

// --- §11.7 verification at every hop ---

// TestPostJoinVerification_EveryHop verifies that §11.7 is enforced: post-join
// verification runs for every intermediate campfire in a multi-hop chain. The
// stub verifier records how many times ProbeAndObserve is called; we expect it
// to be called once for each hop that triggers an auto-join.
func TestPostJoinVerification_EveryHop(t *testing.T) {
	// Create a 2-hop chain: root → child → leaf.
	rootTransportDir := t.TempDir()
	childTransportDir := t.TempDir()
	leafTransportDir := t.TempDir()
	beaconDir := t.TempDir()

	configDirA := t.TempDir()
	clientA, _, err := protocol.Init(configDirA)
	if err != nil {
		t.Fatalf("Init A: %v", err)
	}
	t.Cleanup(func() { clientA.Close() })

	rootResult, err := clientA.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: rootTransportDir},
		BeaconDir: beaconDir,
	})
	if err != nil {
		t.Fatalf("Create root: %v", err)
	}
	rootID := rootResult.CampfireID

	childResult, err := clientA.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: childTransportDir},
		BeaconDir: beaconDir,
	})
	if err != nil {
		t.Fatalf("Create child: %v", err)
	}
	childID := childResult.CampfireID

	leafResult, err := clientA.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: leafTransportDir},
		BeaconDir: beaconDir,
	})
	if err != nil {
		t.Fatalf("Create leaf: %v", err)
	}
	leafID := leafResult.CampfireID

	ctx := context.Background()
	if _, err := naming.Register(ctx, clientA, rootID, "child", childID, nil); err != nil {
		t.Fatalf("Register child: %v", err)
	}
	if _, err := naming.Register(ctx, clientA, childID, "leaf", leafID, nil); err != nil {
		t.Fatalf("Register leaf: %v", err)
	}

	configDirB := t.TempDir()
	clientB, _, err := protocol.Init(configDirB)
	if err != nil {
		t.Fatalf("Init B: %v", err)
	}
	t.Cleanup(func() { clientB.Close() })

	// B joins root directly (no verification needed for explicit join).
	rootCampfireDir := filepath.Join(rootTransportDir, rootID)
	if _, err := clientB.Join(protocol.JoinRequest{
		CampfireID: rootID,
		Transport:  &protocol.FilesystemTransport{Dir: rootCampfireDir},
	}); err != nil {
		t.Fatalf("B.Join root: %v", err)
	}

	stub := &stubVerifier{probeErr: nil} // passes verification
	opts := naming.ResolverClientOptions{
		BeaconDir:         beaconDir,
		Tier2VerifierFunc: newStubVerifierFunc(stub),
	}

	resolver := naming.NewResolverFromClient(clientB, rootID, opts)
	result, err := resolver.ResolveURI(ctx, "cf://child.leaf")
	if err != nil {
		t.Fatalf("ResolveURI: %v", err)
	}
	if result.CampfireID != leafID {
		t.Errorf("CampfireID = %s, want %s", result.CampfireID, leafID)
	}

	// Probe should have been called once for each hop that triggered auto-join.
	// Here: "child" hop triggers auto-join → probe called once.
	if stub.probeCallCount == 0 {
		t.Errorf("ProbeAndObserve should have been called for the intermediate auto-join hop; got %d calls", stub.probeCallCount)
	}
}

// --- §11.3.1 probe timeout config override ---

// TestPostJoinVerification_ProbeTimeoutOverride verifies that ProbeTimeout in
// ResolverClientOptions overrides the default probe timeout.
func TestPostJoinVerification_ProbeTimeoutOverride(t *testing.T) {
	var capturedTimeout time.Duration
	_, clientB, campfireID, campfireTransportDir, beaconDir := setupTwoCampfireClients(t)

	stub := &capturingTimeoutVerifier{}
	opts := naming.ResolverClientOptions{
		BeaconDir:    beaconDir,
		ProbeTimeout: 42 * time.Second,
		Tier2VerifierFunc: func(c *protocol.Client) discovery.Tier2Verifier {
			return stub
		},
		ConfigTransportFunc: func(id string) protocol.Transport {
			if id == campfireID {
				return &protocol.FilesystemTransport{Dir: campfireTransportDir}
			}
			return nil
		},
	}

	resolver := naming.NewResolverFromClient(clientB, campfireID, opts)
	_, _ = resolver.ResolveURI(context.Background(), "cf://anything")

	capturedTimeout = stub.capturedTimeout
	if capturedTimeout != 42*time.Second {
		t.Errorf("ProbeTimeout = %v, want 42s", capturedTimeout)
	}
}

// capturingTimeoutVerifier captures the probeTimeout passed to ProbeAndObserve.
type capturingTimeoutVerifier struct {
	capturedTimeout time.Duration
}

func (v *capturingTimeoutVerifier) ProbeAndObserve(_ context.Context, _ string, timeout time.Duration) error {
	v.capturedTimeout = timeout
	return nil // pass
}
func (v *capturingTimeoutVerifier) PostUnjoinDeclaration(_ context.Context, _, _ string) error {
	return nil
}

// --- Tier2VerifierFunc injection ---

// TestPostJoinVerification_VerifierInjection verifies that a non-nil Tier2VerifierFunc
// is used instead of the default verifier.
func TestPostJoinVerification_VerifierInjection(t *testing.T) {
	_, clientB, campfireID, campfireTransportDir, beaconDir := setupTwoCampfireClients(t)
	stub := &stubVerifier{probeErr: nil}
	injected := false

	opts := naming.ResolverClientOptions{
		BeaconDir: beaconDir,
		Tier2VerifierFunc: func(c *protocol.Client) discovery.Tier2Verifier {
			injected = true
			return stub
		},
		ConfigTransportFunc: func(id string) protocol.Transport {
			if id == campfireID {
				return &protocol.FilesystemTransport{Dir: campfireTransportDir}
			}
			return nil
		},
	}

	resolver := naming.NewResolverFromClient(clientB, campfireID, opts)
	_, _ = resolver.ResolveURI(context.Background(), "cf://anything")

	if !injected {
		t.Error("Tier2VerifierFunc was not called; injected verifier should be used when non-nil")
	}
	if stub.probeCallCount == 0 {
		t.Error("ProbeAndObserve was not called via injected verifier")
	}
}

// containsStr is a test helper to check error strings without importing strings.
func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || findStr(s, sub))
}

func findStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
