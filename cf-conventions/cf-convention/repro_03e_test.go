package convention

import (
	"context"
	"testing"

	"github.com/campfire-net/campfire/cf-protocol/store"
)

// TestRepro03e_LatestVersionWins is the regression test for campfire-03e:
// installing multiple versions of the same (convention, operation) without
// explicit Supersedes links must return only the latest version, not all of them.
//
// Symptom before fix: ListOperations returned 3 declarations for one
// (convention, operation), and CLI dispatch picked the oldest (v0.1) args.
func TestRepro03e_LatestVersionWins(t *testing.T) {
	mk := func(version string) []byte {
		return mustJSON(map[string]any{
			"convention":  "welcome-center",
			"version":     version,
			"operation":   "respond-to-greeting",
			"description": "Respond to a greeting (" + version + ")",
			"antecedents": "none",
			"signing":     "member_key",
		})
	}

	mock := &mockStore{
		records: []store.MessageRecord{
			{ID: "v01", Sender: "op", Payload: mk("0.1"), Tags: []string{ConventionOperationTag}, Timestamp: 1000},
			{ID: "v02", Sender: "op", Payload: mk("0.2"), Tags: []string{ConventionOperationTag}, Timestamp: 2000},
			{ID: "v03", Sender: "op", Payload: mk("0.3"), Tags: []string{ConventionOperationTag}, Timestamp: 3000},
		},
	}

	decls, err := ListOperations(context.Background(), mock, "cf", "")
	if err != nil {
		t.Fatalf("ListOperations: %v", err)
	}

	if len(decls) != 1 {
		t.Errorf("got %d declarations, want 1 (latest version only); versions: %v", len(decls), declVersions(decls))
	}
	if len(decls) > 0 && decls[0].Version != "0.3" {
		t.Errorf("resolved version %q, want 0.3 (latest)", decls[0].Version)
	}
}

// TestRepro03e_MultipleOpsOnSameConvention verifies that dedup is per-operation,
// not per-convention: two distinct operations on the same convention each keep
// their latest version independently.
func TestRepro03e_MultipleOpsOnSameConvention(t *testing.T) {
	mkOp := func(op, version string) []byte {
		return mustJSON(map[string]any{
			"convention":  "welcome-center",
			"version":     version,
			"operation":   op,
			"description": op + " v" + version,
			"antecedents": "none",
			"signing":     "member_key",
		})
	}

	mock := &mockStore{
		records: []store.MessageRecord{
			// greet-arrival: v0.1 and v0.2
			{ID: "ga01", Sender: "op", Payload: mkOp("greet-arrival", "0.1"), Tags: []string{ConventionOperationTag}, Timestamp: 1000},
			{ID: "ga02", Sender: "op", Payload: mkOp("greet-arrival", "0.2"), Tags: []string{ConventionOperationTag}, Timestamp: 2000},
			// respond-to-greeting: v0.1 and v0.3
			{ID: "rg01", Sender: "op", Payload: mkOp("respond-to-greeting", "0.1"), Tags: []string{ConventionOperationTag}, Timestamp: 1000},
			{ID: "rg03", Sender: "op", Payload: mkOp("respond-to-greeting", "0.3"), Tags: []string{ConventionOperationTag}, Timestamp: 3000},
		},
	}

	decls, err := ListOperations(context.Background(), mock, "cf", "")
	if err != nil {
		t.Fatalf("ListOperations: %v", err)
	}

	if len(decls) != 2 {
		t.Fatalf("got %d declarations, want 2 (one per operation); ops: %v", len(decls), declOps(decls))
	}
	byOp := make(map[string]string)
	for _, d := range decls {
		byOp[d.Operation] = d.Version
	}
	if v := byOp["greet-arrival"]; v != "0.2" {
		t.Errorf("greet-arrival version = %q, want 0.2", v)
	}
	if v := byOp["respond-to-greeting"]; v != "0.3" {
		t.Errorf("respond-to-greeting version = %q, want 0.3", v)
	}
}

// TestRepro03e_SupersedesAndDedupCoexist verifies that the Supersedes chain
// filter and the implicit-dedup filter compose correctly: a versioned chain
// with both explicit supersedes AND residual older copies leaves exactly the
// newest non-superseded winner.
func TestRepro03e_SupersedesAndDedupCoexist(t *testing.T) {
	mkPayload := func(version, supersedes string) []byte {
		m := map[string]any{
			"convention":  "my-conv",
			"version":     version,
			"operation":   "my-op",
			"description": "my-op " + version,
			"antecedents": "none",
			"signing":     "member_key",
		}
		if supersedes != "" {
			m["supersedes"] = supersedes
		}
		return mustJSON(m)
	}

	// msg1 (v0.1) → msg2 supersedes msg1 (v0.2) → msg3 is an independent reinstall (v0.3, no supersedes).
	mock := &mockStore{
		records: []store.MessageRecord{
			{ID: "msg1", Sender: "op", Payload: mkPayload("0.1", ""), Tags: []string{ConventionOperationTag}, Timestamp: 1000},
			{ID: "msg2", Sender: "op", Payload: mkPayload("0.2", "msg1"), Tags: []string{ConventionOperationTag}, Timestamp: 2000},
			{ID: "msg3", Sender: "op", Payload: mkPayload("0.3", ""), Tags: []string{ConventionOperationTag}, Timestamp: 3000},
		},
	}

	decls, err := ListOperations(context.Background(), mock, "cf", "")
	if err != nil {
		t.Fatalf("ListOperations: %v", err)
	}

	if len(decls) != 1 {
		t.Fatalf("got %d declarations, want 1; versions: %v", len(decls), declVersions(decls))
	}
	if decls[0].Version != "0.3" {
		t.Errorf("resolved version %q, want 0.3", decls[0].Version)
	}
}

// TestCompareVersions exercises the internal version comparator.
func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.1", "0.2", -1},
		{"0.2", "0.1", 1},
		{"0.3", "0.3", 0},
		{"0.9", "0.10", -1}, // numeric: 9 < 10
		{"1.0", "0.9", 1},
		{"0.1.0", "0.1.1", -1},
		{"0.1.1", "0.1.0", 1},
		{"1", "2", -1},
		{"2", "1", 1},
		{"1", "1", 0},
	}
	for _, tc := range cases {
		got := compareVersions(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// declVersions returns a slice of version strings for diagnostic logging.
func declVersions(decls []*Declaration) []string {
	vs := make([]string, len(decls))
	for i, d := range decls {
		vs[i] = d.Convention + ":" + d.Operation + "@" + d.Version
	}
	return vs
}

// declOps returns a slice of operation names for diagnostic logging.
func declOps(decls []*Declaration) []string {
	ops := make([]string, len(decls))
	for i, d := range decls {
		ops[i] = d.Operation + "@" + d.Version
	}
	return ops
}
