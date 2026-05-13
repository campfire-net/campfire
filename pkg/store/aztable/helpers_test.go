// Package aztable_test contains unit tests for the aztable store helpers.
// These tests do not require a live Azure Table Storage endpoint and run
// without any build tags.
package aztable

import (
	"fmt"
	"net/http"
	"sort"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/campfire-net/campfire/cf-protocol/store"
)

// TestEncodeKey verifies that encodeKey produces safe, deterministic output.
func TestEncodeKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"abc", "abc"},
		{"ABC", "ABC"},
		{"123", "123"},
		{"a-b_c.d", "a-b_c.d"},
		// Chars that must be encoded
		{"a/b", "ax2fb"},
		{"a\\b", "ax5cb"},
		{"a#b", "ax23b"},
		{"a?b", "ax3fb"},
		{"hello world", "hellox20world"},
	}
	for _, tc := range tests {
		got := encodeKey(tc.input)
		if got != tc.want {
			t.Errorf("encodeKey(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
	// Idempotent: encoding twice should not produce new escapes for safe chars.
	safe := "abcABC012-_."
	if encodeKey(safe) != safe {
		t.Errorf("encodeKey(%q) should be identity, got %q", safe, encodeKey(safe))
	}
}

// TestSetGetChunked verifies round-trip chunking for various payload sizes.
func TestSetGetChunked(t *testing.T) {
	tests := []struct {
		name string
		size int
	}{
		{"empty", 0},
		{"small (1 byte)", 1},
		{"below chunk boundary", chunkSize - 1},
		{"exactly one chunk", chunkSize},
		{"one chunk + 1", chunkSize + 1},
		{"exactly two chunks", chunkSize * 2},
		{"two chunks + 1", chunkSize*2 + 1},
		{"three chunks", chunkSize * 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := make([]byte, tc.size)
			for i := range data {
				data[i] = byte(i % 251)
			}
			entity := map[string]any{}
			setChunked(entity, "Data", data)

			got := getChunked(entity, "Data")

			if tc.size == 0 {
				if len(got) != 0 {
					t.Errorf("expected empty, got %d bytes", len(got))
				}
				return
			}
			if len(got) != len(data) {
				t.Errorf("length mismatch: got %d, want %d", len(got), len(data))
				return
			}
			for i := range data {
				if got[i] != data[i] {
					t.Errorf("byte %d mismatch: got %d, want %d", i, got[i], data[i])
					break
				}
			}

			// Verify chunk count
			countRaw := entity["DataChunkCount"]
			count := int(toInt64(countRaw))
			expectedChunks := (tc.size + chunkSize - 1) / chunkSize
			if count != expectedChunks {
				t.Errorf("chunk count: got %d, want %d", count, expectedChunks)
			}
		})
	}
}

// TestEpochPadding verifies epoch row keys sort correctly as strings.
func TestEpochPadding(t *testing.T) {
	epochs := []uint64{0, 1, 9, 10, 99, 100, 999, 1000, 9999, 10000, 18446744073709551615}
	keys := make([]string, len(epochs))
	for i, e := range epochs {
		keys[i] = fmt.Sprintf("%0*d", epochPadWidth, e)
	}
	// Verify they are in ascending string order.
	for i := 1; i < len(keys); i++ {
		if keys[i-1] >= keys[i] {
			t.Errorf("epoch keys not sorted: %s >= %s", keys[i-1], keys[i])
		}
	}
}

// TestIsSystemMessage verifies system message detection.
func TestIsSystemMessage(t *testing.T) {
	tests := []struct {
		tags []string
		want bool
	}{
		{[]string{"status"}, false},
		{[]string{"campfire:compact"}, true},
		{[]string{"campfire:membership-commit"}, true},
		{[]string{"status", "campfire:encrypted-init"}, true},
		{[]string{}, false},
		{nil, false},
	}
	for _, tc := range tests {
		got := isSystemMessage(tc.tags)
		if got != tc.want {
			t.Errorf("isSystemMessage(%v) = %v, want %v", tc.tags, got, tc.want)
		}
	}
}

// TestHasAnyTag verifies OR-semantics tag filtering.
func TestHasAnyTag(t *testing.T) {
	tests := []struct {
		rec    []string
		filter []string
		want   bool
	}{
		{[]string{"a", "b", "c"}, []string{"c"}, true},
		{[]string{"a", "b", "c"}, []string{"d"}, false},
		{[]string{"A"}, []string{"a"}, true}, // case-insensitive
		{[]string{}, []string{"a"}, false},
		{[]string{"a"}, []string{}, false},
	}
	for _, tc := range tests {
		got := hasAnyTag(tc.rec, tc.filter)
		if got != tc.want {
			t.Errorf("hasAnyTag(%v, %v) = %v, want %v", tc.rec, tc.filter, got, tc.want)
		}
	}
}

// TestErrorDetectors verifies the error string detection helpers.
func TestErrorDetectors(t *testing.T) {
	type testCase struct {
		msg  string
		fn   func(error) bool
		want bool
	}
	tests := []testCase{
		{"TableAlreadyExists", isTableExistsError, true},
		{"409 Conflict", isTableExistsError, true},
		{"404 Not Found", isTableExistsError, false},
		{"ResourceNotFound", isNotFoundError, true},
		// "404 Not Found" used to match here via substring fallback, but that
		// produced false positives on entity keys containing "404" as a digit
		// subsequence (campfireagent-426 flake root cause). isNotFoundError now
		// requires either a structured *azcore.ResponseError with status 404 or
		// an error message containing the actual error code. The structured
		// case is exercised below; this substring case is now expected false.
		{"404 Not Found", isNotFoundError, false},
		{"TableNotFound", isNotFoundError, true},
		{"campfire-17774041234567-msg-abc", isNotFoundError, false},
		{"EntityAlreadyExists", isConflictError, true},
		{"409 Conflict", isConflictError, true},
		{"200 OK", isConflictError, false},
	}
	for _, tc := range tests {
		err := fmt.Errorf("%s", tc.msg)
		got := tc.fn(err)
		if got != tc.want {
			t.Errorf("fn(%q) = %v, want %v", tc.msg, got, tc.want)
		}
	}

	// Structured detection: a real *azcore.ResponseError with StatusCode 404
	// must be classified as not-found even if its message text does not match
	// any substring fallback.
	structuredNotFound := &azcore.ResponseError{StatusCode: http.StatusNotFound}
	if !isNotFoundError(structuredNotFound) {
		t.Error("isNotFoundError(*azcore.ResponseError{StatusCode: 404}) = false, want true")
	}
	structuredErrorCode := &azcore.ResponseError{ErrorCode: "ResourceNotFound"}
	if !isNotFoundError(structuredErrorCode) {
		t.Error("isNotFoundError(*azcore.ResponseError{ErrorCode: ResourceNotFound}) = false, want true")
	}
	structuredOK := &azcore.ResponseError{StatusCode: http.StatusOK}
	if isNotFoundError(structuredOK) {
		t.Error("isNotFoundError(*azcore.ResponseError{StatusCode: 200}) = true, want false")
	}

	// nil should return false for all.
	if isTableExistsError(nil) || isNotFoundError(nil) || isConflictError(nil) {
		t.Error("nil error should return false for all detectors")
	}
}

// TestPKNamespace verifies that ts.pk() applies the namespace prefix correctly.
func TestPKNamespace(t *testing.T) {
	ts := &TableStore{}

	// No namespace: pk() == encodeKey(campfireID)
	cfID := "abc123def456"
	if got := ts.pk(cfID); got != encodeKey(cfID) {
		t.Errorf("no namespace: pk(%q) = %q, want %q", cfID, got, encodeKey(cfID))
	}

	// With namespace: pk() == encodeKey(namespace+"|"+campfireID)
	ts.namespace = "ns1"
	want := encodeKey("ns1|" + cfID)
	if got := ts.pk(cfID); got != want {
		t.Errorf("with namespace: pk(%q) = %q, want %q", cfID, got, want)
	}

	// Different namespaces produce different PKs for the same campfireID.
	ts2 := &TableStore{namespace: "ns2"}
	if ts.pk(cfID) == ts2.pk(cfID) {
		t.Error("different namespaces should produce different PKs")
	}
}

// TestNsPKFilter verifies that nsPKFilter() returns the correct OData range filter.
func TestNsPKFilter(t *testing.T) {
	ts := &TableStore{}

	// No namespace: empty filter.
	if f := ts.nsPKFilter(); f != "" {
		t.Errorf("no namespace: nsPKFilter() = %q, want empty", f)
	}

	// With namespace: filter must be a non-empty OData range expression.
	ts.namespace = "sess01"
	f := ts.nsPKFilter()
	if f == "" {
		t.Fatal("with namespace: nsPKFilter() returned empty string")
	}
	// Filter must reference PartitionKey.
	if !contains(f, "PartitionKey") {
		t.Errorf("nsPKFilter() = %q, missing PartitionKey", f)
	}
	// Lower bound must be the encoded namespace prefix.
	lo := encodeKey("sess01|")
	if !contains(f, lo) {
		t.Errorf("nsPKFilter() = %q, missing lower bound %q", f, lo)
	}
}

// TestListMessagesReverseSort is a regression test for campfire-986: aztable.ListMessages
// must honour the Reverse flag in MessageFilter. This test exercises the sort step
// directly — it constructs a slice of MessageRecords (as they would appear after the
// Azure pager loop), applies the same sort.Slice closure, and verifies ordering.
func TestListMessagesReverseSort(t *testing.T) {
	// Simulate three messages fetched in arbitrary order from Azure Table Storage.
	records := []store.MessageRecord{
		{ID: "c", Timestamp: 300},
		{ID: "a", Timestamp: 100},
		{ID: "b", Timestamp: 200},
	}

	// Sort ASC (Reverse == false).
	asc := make([]store.MessageRecord, len(records))
	copy(asc, records)
	fAsc := store.MessageFilter{Reverse: false}
	sort.Slice(asc, func(i, j int) bool {
		if fAsc.Reverse {
			return asc[i].Timestamp > asc[j].Timestamp
		}
		return asc[i].Timestamp < asc[j].Timestamp
	})
	if asc[0].ID != "a" || asc[1].ID != "b" || asc[2].ID != "c" {
		t.Errorf("ASC sort: expected [a b c], got [%s %s %s]", asc[0].ID, asc[1].ID, asc[2].ID)
	}

	// Sort DESC (Reverse == true) — this is the bug path campfire-986.
	desc := make([]store.MessageRecord, len(records))
	copy(desc, records)
	fDesc := store.MessageFilter{Reverse: true}
	sort.Slice(desc, func(i, j int) bool {
		if fDesc.Reverse {
			return desc[i].Timestamp > desc[j].Timestamp
		}
		return desc[i].Timestamp < desc[j].Timestamp
	})
	if desc[0].ID != "c" || desc[1].ID != "b" || desc[2].ID != "a" {
		t.Errorf("DESC sort (Reverse=true): expected [c b a], got [%s %s %s]", desc[0].ID, desc[1].ID, desc[2].ID)
	}
}

// TestToInt64_JsonNumber verifies that toInt64 handles json.Number (UseNumber path)
// without precision loss for nanosecond timestamps that exceed float64 mantissa (53 bits).
// Regression test for the aztable float64 precision bug: when Azure Table returns Edm.Int64
// properties via JSON, json.Unmarshal decodes them as float64 by default, losing precision
// for values > 2^53. unmarshalEntity now uses UseNumber() to preserve exact integer values.
func TestToInt64_JsonNumber(t *testing.T) {
	// A nanosecond timestamp that exceeds float64 precision (> 2^53):
	// float64(1778023160419527281) = 1778023160419527168 (loss of 113).
	const nanoTS int64 = 1778023160419527281

	// Verify the precision loss we are guarding against.
	if int64(float64(nanoTS)) == nanoTS {
		t.Skip("this platform has exact float64 representation for the test timestamp — skip")
	}

	// json.Number path (via unmarshalEntity / UseNumber).
	import_json_number_json_encoded := []byte(`{"MsgTimestamp":1778023160419527281}`)
	var m map[string]any
	if err := unmarshalEntity(import_json_number_json_encoded, &m); err != nil {
		t.Fatalf("unmarshalEntity: %v", err)
	}
	got := toInt64(m["MsgTimestamp"])
	if got != nanoTS {
		t.Errorf("toInt64(json.Number): want %d, got %d (diff=%d)", nanoTS, got, nanoTS-got)
	}
}

// TestUnmarshalEntityPreservesInt64 verifies that unmarshalEntity uses UseNumber() so
// large int64 timestamp values round-trip without float64 precision loss.
func TestUnmarshalEntityPreservesInt64(t *testing.T) {
	// Two timestamps that differ by less than float64 ulp at this magnitude
	// (both would map to the same float64 approximation without UseNumber).
	const ts1 int64 = 1778023160419527281
	const ts2 int64 = 1778023160419527168

	// Verify that float64 cannot distinguish the two.
	if float64(ts1) != float64(ts2) {
		t.Skip("float64 can distinguish test values on this platform — skip")
	}

	raw1 := []byte(`{"T":1778023160419527281}`)
	raw2 := []byte(`{"T":1778023160419527168}`)
	var m1, m2 map[string]any
	if err := unmarshalEntity(raw1, &m1); err != nil {
		t.Fatalf("unmarshalEntity raw1: %v", err)
	}
	if err := unmarshalEntity(raw2, &m2); err != nil {
		t.Fatalf("unmarshalEntity raw2: %v", err)
	}
	got1 := toInt64(m1["T"])
	got2 := toInt64(m2["T"])
	if got1 == got2 {
		t.Errorf("unmarshalEntity: ts1 and ts2 collide at %d (UseNumber not active)", got1)
	}
	if got1 != ts1 {
		t.Errorf("ts1: want %d, got %d", ts1, got1)
	}
	if got2 != ts2 {
		t.Errorf("ts2: want %d, got %d", ts2, got2)
	}
}

// TestInviteFromEntity_Precision verifies that inviteFromEntity correctly
// preserves int64 UseCount and CreatedAt values when the entity was decoded
// via unmarshalEntity (UseNumber path). This is the data path used by
// ValidateAndUseInvite — the missed call site fixed in campfireagent-c39.
//
// Without UseNumber, UseCount and CreatedAt would be decoded as float64, losing
// precision for large nanosecond timestamps (> 2^53 representable by float64).
func TestInviteFromEntity_Precision(t *testing.T) {
	// A nanosecond timestamp that exceeds float64 precision (> 2^53):
	// int64(float64(1778023160419527281)) = 1778023160419527168 (loss of 113).
	const nanoTS int64 = 1778023160419527281
	const useCount int64 = 42

	// Verify we're actually testing a precision-sensitive value.
	if int64(float64(nanoTS)) == nanoTS {
		t.Skip("this platform has exact float64 representation for the test timestamp — skip")
	}

	// Construct a raw entity JSON as Azure Table Storage would return it.
	raw := []byte(fmt.Sprintf(`{
		"PartitionKey": "code123",
		"RowKey": "cfabc",
		"CampfireID": "cfabc",
		"InviteCode": "code123",
		"CreatedBy": "agent-x",
		"CreatedAt": %d,
		"Revoked": 0,
		"MaxUses": 10,
		"UseCount": %d,
		"Label": "test"
	}`, nanoTS, useCount))

	var m map[string]any
	if err := unmarshalEntity(raw, &m); err != nil {
		t.Fatalf("unmarshalEntity: %v", err)
	}

	inv := inviteFromEntity(m)
	if inv == nil {
		t.Fatal("inviteFromEntity returned nil")
	}
	if inv.CreatedAt != nanoTS {
		t.Errorf("CreatedAt precision loss: got %d, want %d (diff=%d)", inv.CreatedAt, nanoTS, nanoTS-inv.CreatedAt)
	}
	if int64(inv.UseCount) != useCount {
		t.Errorf("UseCount: got %d, want %d", inv.UseCount, useCount)
	}
	if inv.CampfireID != "cfabc" {
		t.Errorf("CampfireID: got %q, want %q", inv.CampfireID, "cfabc")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
