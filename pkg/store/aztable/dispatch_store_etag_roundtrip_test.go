//go:build azurite

// Package aztable_test — dispatch_store_etag_roundtrip_test.go
//
// Veracity integration test: Azure Table Storage ETag round-trip for MarkBilled.
//
// Finding (campfireagent-fc5): the ETag round-trip between ListUnbilledDispatches
// (odata.etag) and MarkBilled (callerETag vs resp.ETag from GetEntity) was
// believed to be consistent but had never been verified end-to-end against a
// real Azure/Azurite backend.
//
// This test verifies:
//  1. odata.etag from list pager == resp.ETag from GetEntity (same wire format).
//  2. MarkBilled with the odata.etag from ListUnbilledDispatches succeeds (no
//     ErrConcurrentModification).
//  3. Adversarial: mutating the record out-of-band invalidates the original
//     odata.etag, and MarkBilled with the stale ETag fails with
//     ErrConcurrentModification.
//
// Run with: go test -tags azurite ./pkg/store/aztable/...
// Prerequisites: Azurite running on localhost:10002.
// CI: the existing Azurite service container in .github/workflows/ci.yml
// satisfies the prerequisite (see the "Run Azurite integration tests" step).
package aztable_test

import (
	"context"
	"errors"
	"testing"

	"github.com/campfire-net/campfire/cf-conventions/cf-convention"
)

// TestDispatchStore_ETagRoundTrip_ListToMarkBilled verifies that the ETag
// returned by ListUnbilledDispatches (from odata.etag in the list pager) is
// byte-for-byte equal to the ETag returned by a direct GetEntity call, and that
// passing this ETag to MarkBilled succeeds.
//
// This is the ground-truth validation the casMockTable cannot provide: it
// confirms that real Azurite emits odata.etag and GetEntity ETag in the same
// format, so the in-process string comparison in MarkBilled is correct.
func TestDispatchStore_ETagRoundTrip_ListToMarkBilled(t *testing.T) {
	s := newTestDispatchStore(t)
	ctx := context.Background()
	cfID := unique("cf-etag-rtt")
	msgID := unique("msg-etag-rtt")
	serverID := unique("srv")

	// Step 1: Insert a dispatch record and move it to the unbilled state.
	if _, err := s.MarkDispatched(ctx, cfID, msgID, serverID, "", "conv", "op"); err != nil {
		t.Fatalf("MarkDispatched: %v", err)
	}
	if err := s.MarkFulfilled(ctx, cfID, msgID); err != nil {
		t.Fatalf("MarkFulfilled: %v", err)
	}
	if err := s.SetTokensConsumed(ctx, cfID, msgID, 50); err != nil {
		t.Fatalf("SetTokensConsumed: %v", err)
	}

	// Step 2: Call ListUnbilledDispatches; capture record.ETag (from odata.etag).
	unbilled, err := s.ListUnbilledDispatches(ctx)
	if err != nil {
		t.Fatalf("ListUnbilledDispatches: %v", err)
	}
	var listETag string
	for _, r := range unbilled {
		if r.CampfireID == cfID && r.MessageID == msgID {
			listETag = r.ETag
			break
		}
	}
	if listETag == "" {
		t.Fatal("record not found in ListUnbilledDispatches, or ETag is empty")
	}

	// Step 3: Call GetEntity directly and capture resp.ETag.
	// These two ETags MUST be byte-for-byte equal — MarkBilled's in-process
	// string comparison (callerETag == resp.ETag) relies on this invariant.
	getEntityETag, err := s.GetDispatchEntityETag(ctx, cfID, msgID)
	if err != nil {
		t.Fatalf("GetDispatchEntityETag: %v", err)
	}
	if getEntityETag == "" {
		t.Fatal("GetEntity returned empty ETag")
	}

	if listETag != getEntityETag {
		// Format mismatch found — file a finding.
		t.Errorf("ETag format mismatch between list pager (odata.etag) and GetEntity (resp.ETag):\n"+
			"  list  ETag = %q\n"+
			"  GetEntity ETag = %q\n"+
			"MarkBilled's in-process string comparison will incorrectly reject a valid ETag from the caller.\n"+
			"FINDING: file campfireagent issue for ETag format inequality.", listETag, getEntityETag)
	}

	// Step 4: Call MarkBilled with the ETag from ListUnbilledDispatches.
	// MUST succeed — no ErrConcurrentModification.
	if err := s.MarkBilled(ctx, cfID, msgID, listETag); err != nil {
		t.Fatalf("MarkBilled with ETag from list: %v (ETag was %q)", err, listETag)
	}

	// Confirm the record is now billed.
	unbilled2, err := s.ListUnbilledDispatches(ctx)
	if err != nil {
		t.Fatalf("ListUnbilledDispatches after billing: %v", err)
	}
	for _, r := range unbilled2 {
		if r.CampfireID == cfID && r.MessageID == msgID {
			t.Error("record still appears in unbilled list after successful MarkBilled")
		}
	}
}

// TestDispatchStore_ETagRoundTrip_StaleETagRejected verifies the adversarial
// case: after the record is mutated out-of-band, the original odata.etag from
// ListUnbilledDispatches is stale and MarkBilled must reject it.
//
// This complements TestDispatchStore_ETagRoundTrip_ListToMarkBilled: the happy
// path test proves the format is consistent; this test proves the stale-ETag
// guard fires correctly using only ETags sourced from ListUnbilledDispatches.
func TestDispatchStore_ETagRoundTrip_StaleETagRejected(t *testing.T) {
	s := newTestDispatchStore(t)
	ctx := context.Background()
	cfID := unique("cf-etag-stale")
	msgID := unique("msg-etag-stale")
	serverID := unique("srv")

	// Step 1: Insert and set up unbilled record.
	if _, err := s.MarkDispatched(ctx, cfID, msgID, serverID, "", "conv", "op"); err != nil {
		t.Fatalf("MarkDispatched: %v", err)
	}
	if err := s.MarkFulfilled(ctx, cfID, msgID); err != nil {
		t.Fatalf("MarkFulfilled: %v", err)
	}
	if err := s.SetTokensConsumed(ctx, cfID, msgID, 50); err != nil {
		t.Fatalf("SetTokensConsumed: %v", err)
	}

	// Step 2: Capture odata.etag from ListUnbilledDispatches.
	unbilled, err := s.ListUnbilledDispatches(ctx)
	if err != nil {
		t.Fatalf("ListUnbilledDispatches: %v", err)
	}
	var originalETag string
	for _, r := range unbilled {
		if r.CampfireID == cfID && r.MessageID == msgID {
			originalETag = r.ETag
			break
		}
	}
	if originalETag == "" {
		t.Fatal("record not found in ListUnbilledDispatches")
	}

	// Step 3: Mutate the record out-of-band to invalidate the ETag.
	if _, err := s.IncrementRedispatchCount(ctx, cfID, msgID); err != nil {
		t.Fatalf("IncrementRedispatchCount (out-of-band mutation): %v", err)
	}

	// Verify the ETag changed after mutation.
	newETag, err := s.GetDispatchEntityETag(ctx, cfID, msgID)
	if err != nil {
		t.Fatalf("GetDispatchEntityETag after mutation: %v", err)
	}
	if newETag == originalETag {
		t.Logf("WARNING: ETag did not change after IncrementRedispatchCount (originalETag=%q, newETag=%q). "+
			"Azurite may not be updating ETags after writes — this would make the stale-ETag guard "+
			"non-functional against Azurite (known Azurite limitation). "+
			"The in-process string comparison in MarkBilled compensates for this.", originalETag, newETag)
		// Do not skip: MarkBilled's in-process guard should still reject a stale ETag
		// even when Azurite does not update ETags after writes (see MarkBilled source).
	}

	// Step 4: Call MarkBilled with the now-stale ETag; MUST fail.
	err = s.MarkBilled(ctx, cfID, msgID, originalETag)
	if err == nil {
		t.Fatal("MarkBilled with stale ETag succeeded — lost-update bug: the in-process " +
			"stale-ETag guard in MarkBilled did not fire")
	}
	if !errors.Is(err, convention.ErrConcurrentModification) {
		t.Fatalf("expected ErrConcurrentModification for stale ETag, got: %v", err)
	}

	// Step 5: Re-read fresh ETag from ListUnbilledDispatches; MUST succeed.
	unbilled2, err := s.ListUnbilledDispatches(ctx)
	if err != nil {
		t.Fatalf("ListUnbilledDispatches for retry: %v", err)
	}
	var freshETag string
	for _, r := range unbilled2 {
		if r.CampfireID == cfID && r.MessageID == msgID {
			freshETag = r.ETag
			break
		}
	}
	if freshETag == "" {
		t.Fatal("record not found in ListUnbilledDispatches for retry")
	}

	if err := s.MarkBilled(ctx, cfID, msgID, freshETag); err != nil {
		t.Fatalf("MarkBilled with fresh ETag: %v", err)
	}
}
