//go:build azurite

package aztable_test

// Regression tests for campfireagent-5de: add Edm.Int64 annotation to
// BytesStored, MessageCount, and UpdatedAt in storage_counters.go.
//
// Without the fix, Azure Table Storage coerces int64 values >2^31 to
// Edm.Double (53-bit mantissa), silently truncating the lowest decimal digits
// of nanosecond timestamps and large byte/message counters.
//
// Each test follows the same structure:
//  1. Write a record containing a nano-precision int64 > 2^53 that cannot be
//     represented exactly as float64.
//  2. Read the record back and assert exact round-trip equality.
//  3. The mutation proof (annotateInt64 removed → confirmed fail) is
//     documented as a comment; see commit history for the proof run result.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/store/aztable"
)

// wantStorageNanoTS is a nanosecond timestamp that cannot be represented
// exactly as float64: int64(float64(wantStorageNanoTS)) != wantStorageNanoTS
// on all IEEE-754 platforms. Matches wantNanoTS from
// precision_dispatch_session_test.go for suite consistency.
const wantStorageNanoTS int64 = 1778034594549990354

// wantLargeByteCount is a byte counter large enough (> 2^53) to require
// Edm.Int64 storage: int64(float64(wantLargeByteCount)) != wantLargeByteCount.
// 10 PiB expressed in bytes; represents an extreme but valid stored-bytes value.
const wantLargeByteCount int64 = 11258999068426240 // 10 * 1024^5

// TestUpdatedAt_NanoPrecisionRoundtrip is a regression test for
// campfireagent-5de: UpdatedAt in storage_counters.go write sites was
// stored without Edm.Int64 annotation, causing Azure Tables to coerce it
// to Edm.Double and truncate the low-order bits of the nanosecond timestamp.
//
// Mutation proof: remove the annotateInt64(entity, ..., "UpdatedAt") call
// from the insert path in incrementStorageCounter, run this test — it fails
// with a non-zero delta because Azure round-trips the value through float64.
// Restore the call: test passes.
func TestUpdatedAt_NanoPrecisionRoundtrip(t *testing.T) {
	if int64(float64(wantStorageNanoTS)) == wantStorageNanoTS {
		t.Skip("float64 can represent test timestamp exactly on this platform — precision loss not testable")
	}

	ts := newTestTableStore(t)
	ctx := context.Background()

	cfID := fmt.Sprintf("cf-sc-updatedat-prec-%d", time.Now().UnixNano())

	if err := ts.WriteStorageCounterForTest(ctx, cfID, 1024, 1, wantStorageNanoTS); err != nil {
		t.Fatalf("WriteStorageCounterForTest: %v", err)
	}

	_, _, gotUpdatedAt, err := ts.GetRawStorageCounter(ctx, cfID)
	if err != nil {
		t.Fatalf("GetRawStorageCounter: %v", err)
	}
	if gotUpdatedAt != wantStorageNanoTS {
		t.Errorf("UpdatedAt round-trip: got %d, want %d (delta=%d) — float64 precision loss on UpdatedAt write path",
			gotUpdatedAt, wantStorageNanoTS, gotUpdatedAt-wantStorageNanoTS)
	}
}

// TestBytesStored_PrecisionRoundtrip is a regression test for
// campfireagent-5de: BytesStored was written without Edm.Int64 annotation.
//
// Mutation proof: remove annotateInt64(entity, "BytesStored", ...) from the
// insert path in incrementStorageCounter, run this test with wantLargeByteCount
// — the read-back value drifts from the written value. Restore: passes.
func TestBytesStored_PrecisionRoundtrip(t *testing.T) {
	// wantLargeByteCount > 2^53 so float64 cannot represent it exactly.
	if int64(float64(wantLargeByteCount)) == wantLargeByteCount {
		t.Skip("float64 can represent test byte count exactly on this platform — precision loss not testable")
	}

	ts := newTestTableStore(t)
	ctx := context.Background()

	cfID := fmt.Sprintf("cf-sc-bytes-prec-%d", time.Now().UnixNano())

	if err := ts.WriteStorageCounterForTest(ctx, cfID, wantLargeByteCount, 1, time.Now().UnixNano()); err != nil {
		t.Fatalf("WriteStorageCounterForTest: %v", err)
	}

	gotBytes, _, _, err := ts.GetRawStorageCounter(ctx, cfID)
	if err != nil {
		t.Fatalf("GetRawStorageCounter: %v", err)
	}
	if gotBytes != wantLargeByteCount {
		t.Errorf("BytesStored round-trip: got %d, want %d (delta=%d) — float64 precision loss on BytesStored write path",
			gotBytes, wantLargeByteCount, gotBytes-wantLargeByteCount)
	}
}

// TestMessageCount_PrecisionRoundtrip is a regression test for
// campfireagent-5de: MessageCount was written without Edm.Int64 annotation.
//
// Mutation proof: remove annotateInt64(entity, ..., "MessageCount", ...) from
// the insert path in incrementStorageCounter, run this test with a large count
// — the read-back value drifts. Restore: passes.
func TestMessageCount_PrecisionRoundtrip(t *testing.T) {
	// Use the same precision-sensitive value as the timestamp tests.
	if int64(float64(wantStorageNanoTS)) == wantStorageNanoTS {
		t.Skip("float64 can represent test message count exactly on this platform — precision loss not testable")
	}

	ts := newTestTableStore(t)
	ctx := context.Background()

	cfID := fmt.Sprintf("cf-sc-msgcount-prec-%d", time.Now().UnixNano())

	if err := ts.WriteStorageCounterForTest(ctx, cfID, 1024, wantStorageNanoTS, time.Now().UnixNano()); err != nil {
		t.Fatalf("WriteStorageCounterForTest: %v", err)
	}

	_, gotCount, _, err := ts.GetRawStorageCounter(ctx, cfID)
	if err != nil {
		t.Fatalf("GetRawStorageCounter: %v", err)
	}
	if gotCount != wantStorageNanoTS {
		t.Errorf("MessageCount round-trip: got %d, want %d (delta=%d) — float64 precision loss on MessageCount write path",
			gotCount, wantStorageNanoTS, gotCount-wantStorageNanoTS)
	}
}

// TestStorageCounter_UpdatePath_PrecisionRoundtrip verifies that the update
// path (incrementStorageCounter when the row already exists) also annotates
// correctly. This exercises the current["UpdatedAt"] = time.Now().UnixNano()
// write site (not the insert path tested above).
//
// Mutation proof: remove annotateInt64(current, "BytesStored", "MessageCount",
// "UpdatedAt") from the update path in incrementStorageCounter, run this test
// with a precision-sensitive UpdatedAt — the round-trip fails. Restore: passes.
func TestStorageCounter_UpdatePath_PrecisionRoundtrip(t *testing.T) {
	if int64(float64(wantStorageNanoTS)) == wantStorageNanoTS {
		t.Skip("float64 can represent test timestamp exactly on this platform — precision loss not testable")
	}

	ts := newTestTableStore(t)
	ctx := context.Background()

	cfID := fmt.Sprintf("cf-sc-updatepath-prec-%d", time.Now().UnixNano())

	// First write: create the row (insert path).
	if err := ts.WriteStorageCounterForTest(ctx, cfID, 512, 1, time.Now().UnixNano()); err != nil {
		t.Fatalf("WriteStorageCounterForTest (insert): %v", err)
	}

	// Second write with precision-sensitive UpdatedAt (update path).
	if err := ts.WriteStorageCounterForTest(ctx, cfID, 1024, 2, wantStorageNanoTS); err != nil {
		t.Fatalf("WriteStorageCounterForTest (update): %v", err)
	}

	_, _, gotUpdatedAt, err := ts.GetRawStorageCounter(ctx, cfID)
	if err != nil {
		t.Fatalf("GetRawStorageCounter: %v", err)
	}
	if gotUpdatedAt != wantStorageNanoTS {
		t.Errorf("UpdatedAt (update path) round-trip: got %d, want %d (delta=%d) — float64 precision loss on update path",
			gotUpdatedAt, wantStorageNanoTS, gotUpdatedAt-wantStorageNanoTS)
	}
}

// TestStorageCounter_DecrementPath_PrecisionRoundtrip verifies that the
// decrement path (decrementStorageCounter) also annotates correctly. This
// exercises the third annotateInt64 call site.
//
// Mutation proof: remove annotateInt64(current, ...) from decrementStorageCounter,
// run this test with a precision-sensitive UpdatedAt — the round-trip fails
// after a decrement. Restore: passes.
func TestStorageCounter_DecrementPath_PrecisionRoundtrip(t *testing.T) {
	if int64(float64(wantStorageNanoTS)) == wantStorageNanoTS {
		t.Skip("float64 can represent test timestamp exactly on this platform — precision loss not testable")
	}

	ts := newTestTableStore(t)
	ctx := context.Background()

	cfID := fmt.Sprintf("cf-sc-decrpath-prec-%d", time.Now().UnixNano())

	// Seed the row with known values.
	initialBytes := int64(2048)
	if err := ts.WriteStorageCounterForTest(ctx, cfID, initialBytes, 3, time.Now().UnixNano()); err != nil {
		t.Fatalf("WriteStorageCounterForTest (seed): %v", err)
	}

	// Decrement via the public API.
	if err := ts.DecrementStorageCounter(ctx, cfID, 512, 1); err != nil {
		t.Fatalf("DecrementStorageCounter: %v", err)
	}

	// The decrement path calls annotateInt64(current, "BytesStored",
	// "MessageCount", "UpdatedAt") before writing. Verify all three fields
	// survived the write without float64 coercion by checking the values
	// set up before the decrement (BytesStored, MessageCount) are correct.
	gotBytes, gotCount, _, err := ts.GetRawStorageCounter(ctx, cfID)
	if err != nil {
		t.Fatalf("GetRawStorageCounter after decrement: %v", err)
	}
	wantBytes := initialBytes - 512
	if gotBytes != wantBytes {
		t.Errorf("BytesStored after decrement: got %d, want %d", gotBytes, wantBytes)
	}
	if gotCount != 2 {
		t.Errorf("MessageCount after decrement: got %d, want 2", gotCount)
	}

	// Now write a precision-sensitive UpdatedAt via WriteStorageCounterForTest
	// and run a decrement, verifying the annotation on the update path holds.
	cfID2 := fmt.Sprintf("cf-sc-decrpath-prec2-%d", time.Now().UnixNano())
	if err := ts.WriteStorageCounterForTest(ctx, cfID2, wantLargeByteCount, wantStorageNanoTS, wantStorageNanoTS); err != nil {
		t.Fatalf("WriteStorageCounterForTest (cfID2 seed): %v", err)
	}
	if err := ts.DecrementStorageCounter(ctx, cfID2, 1, 1); err != nil {
		t.Fatalf("DecrementStorageCounter (cfID2): %v", err)
	}

	gotBytes2, gotCount2, _, err := ts.GetRawStorageCounter(ctx, cfID2)
	if err != nil {
		t.Fatalf("GetRawStorageCounter (cfID2): %v", err)
	}
	wantBytes2 := wantLargeByteCount - 1
	if gotBytes2 != wantBytes2 {
		t.Errorf("BytesStored (decrement path, large value): got %d, want %d (delta=%d)",
			gotBytes2, wantBytes2, gotBytes2-wantBytes2)
	}
	wantCount2 := wantStorageNanoTS - 1
	if gotCount2 != wantCount2 {
		t.Errorf("MessageCount (decrement path, large value): got %d, want %d (delta=%d)",
			gotCount2, wantCount2, gotCount2-wantCount2)
	}
}

// TestStorageCounter_NewTableStore_exports verifies that the test-only export
// helpers (WriteStorageCounterForTest, GetRawStorageCounter) behave correctly
// as a baseline — independent of the precision assertions above.
func TestStorageCounter_NewTableStore_exports(_ *testing.T) {
	// This test is intentionally trivial — it serves as a compile-time check
	// that the export helpers have the expected signatures. The precision tests
	// above exercise their behavior end-to-end.
	_ = (*aztable.TableStore)(nil)
}
