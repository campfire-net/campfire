// Package aztable_test contains unit tests for the aztable store helpers.
// These tests do not require a live Azure Table Storage endpoint and run
// without any build tags.
package aztable

import (
	"fmt"
	"testing"
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
		{"404 Not Found", isNotFoundError, true},
		{"TableNotFound", isNotFoundError, true},
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
