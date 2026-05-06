// Package aztable — cas_mock_test.go
//
// Deterministic in-memory CAS table client for unit testing.
//
// No build tags: always compiled and run. This file provides
// casMockTable (implements dispatchTableClient) and NewMockDispatchStore,
// used by dispatch_store_cas_test.go to verify the CAS contract without
// depending on Azurite, which does not reliably enforce IfMatch under
// concurrent write pressure.
package aztable

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

// casMockTable is an in-memory table that implements dispatchTableClient with
// deterministic IfMatch enforcement. All mutations are serialised through mu so
// ETag checks and writes are atomic — guaranteed 1-winner in concurrent races.
type casMockTable struct {
	mu      sync.Mutex
	rows    map[string]mockRow
	etagSeq uint64 // monotonically incremented per write
}

type mockRow struct {
	data []byte     // JSON-encoded entity
	etag azcore.ETag
}

func newCASMockTable() *casMockTable {
	return &casMockTable{rows: make(map[string]mockRow)}
}

func mockKey(pk, rk string) string { return pk + "\x00" + rk }

func (m *casMockTable) nextETag() azcore.ETag {
	seq := atomic.AddUint64(&m.etagSeq, 1)
	return azcore.ETag(fmt.Sprintf("W/\"%d-%d\"", time.Now().UnixNano(), seq))
}

// GetEntity implements dispatchTableClient.
func (m *casMockTable) GetEntity(_ context.Context, pk, rk string, _ *aztables.GetEntityOptions) (aztables.GetEntityResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[mockKey(pk, rk)]
	if !ok {
		return aztables.GetEntityResponse{}, mockNotFoundError(pk, rk)
	}
	return aztables.GetEntityResponse{Value: row.data, ETag: row.etag}, nil
}

// AddEntity implements dispatchTableClient (insert-if-not-exists).
func (m *casMockTable) AddEntity(_ context.Context, entity []byte, _ *aztables.AddEntityOptions) (aztables.AddEntityResponse, error) {
	pk, rk, err := pkrkFromJSON(entity)
	if err != nil {
		return aztables.AddEntityResponse{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := mockKey(pk, rk)
	if _, exists := m.rows[key]; exists {
		return aztables.AddEntityResponse{}, mockConflictError(pk, rk)
	}
	etag := m.nextETag()
	m.rows[key] = mockRow{data: entity, etag: etag}
	return aztables.AddEntityResponse{ETag: etag}, nil
}

// UpdateEntity implements dispatchTableClient with deterministic IfMatch enforcement.
// Merge mode merges top-level fields; Replace mode replaces the whole entity.
func (m *casMockTable) UpdateEntity(_ context.Context, entity []byte, opts *aztables.UpdateEntityOptions) (aztables.UpdateEntityResponse, error) {
	pk, rk, err := pkrkFromJSON(entity)
	if err != nil {
		return aztables.UpdateEntityResponse{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := mockKey(pk, rk)
	row, exists := m.rows[key]
	if !exists {
		return aztables.UpdateEntityResponse{}, mockNotFoundError(pk, rk)
	}
	// Enforce IfMatch deterministically under the mutex.
	if opts != nil && opts.IfMatch != nil && *opts.IfMatch != "" && *opts.IfMatch != azcore.ETagAny {
		if row.etag != *opts.IfMatch {
			return aztables.UpdateEntityResponse{}, mockPreconditionError(pk, rk)
		}
	}
	var newData []byte
	if opts != nil && opts.UpdateMode == aztables.UpdateModeMerge {
		// Use unmarshalEntity (UseNumber) to preserve large int64 values through
		// the merge — plain json.Unmarshal would truncate them to float64.
		var base map[string]any
		if jsonErr := unmarshalEntity(row.data, &base); jsonErr != nil {
			return aztables.UpdateEntityResponse{}, jsonErr
		}
		var patch map[string]any
		if jsonErr := unmarshalEntity(entity, &patch); jsonErr != nil {
			return aztables.UpdateEntityResponse{}, jsonErr
		}
		for k, v := range patch {
			base[k] = v
		}
		newData, err = json.Marshal(base)
		if err != nil {
			return aztables.UpdateEntityResponse{}, err
		}
	} else {
		newData = entity
	}
	etag := m.nextETag()
	m.rows[key] = mockRow{data: newData, etag: etag}
	return aztables.UpdateEntityResponse{ETag: etag}, nil
}

// UpsertEntity implements dispatchTableClient (insert-or-replace).
func (m *casMockTable) UpsertEntity(_ context.Context, entity []byte, _ *aztables.UpsertEntityOptions) (aztables.UpsertEntityResponse, error) {
	pk, rk, err := pkrkFromJSON(entity)
	if err != nil {
		return aztables.UpsertEntityResponse{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	etag := m.nextETag()
	m.rows[mockKey(pk, rk)] = mockRow{data: entity, etag: etag}
	return aztables.UpsertEntityResponse{}, nil
}

// DeleteEntity implements dispatchTableClient.
func (m *casMockTable) DeleteEntity(_ context.Context, pk, rk string, _ *aztables.DeleteEntityOptions) (aztables.DeleteEntityResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, mockKey(pk, rk))
	return aztables.DeleteEntityResponse{}, nil
}

// NewListEntitiesPager implements dispatchTableClient. Returns a single-page
// pager of all entities matching the filter. Injects odata.etag into each entity
// so dispatchRecordFromEntity can populate DispatchRecord.ETag.
func (m *casMockTable) NewListEntitiesPager(opts *aztables.ListEntitiesOptions) *runtime.Pager[aztables.ListEntitiesResponse] {
	m.mu.Lock()
	snapshot := make([]mockRow, 0, len(m.rows))
	for _, row := range m.rows {
		snapshot = append(snapshot, row)
	}
	m.mu.Unlock()

	var filtered [][]byte
	for _, row := range snapshot {
		// Use unmarshalEntity (UseNumber) so large int64 values (e.g. nanosecond
		// timestamps) are preserved as json.Number rather than truncated to float64.
		// This mirrors how Azure Table Storage returns numeric values on the wire —
		// as JSON numbers that Go decodes without precision loss when UseNumber is set.
		var entity map[string]any
		if err := unmarshalEntity(row.data, &entity); err != nil {
			continue
		}
		entity["odata.etag"] = string(row.etag)
		if opts == nil || opts.Filter == nil || mockMatchesFilter(entity, *opts.Filter) {
			merged, _ := json.Marshal(entity)
			filtered = append(filtered, merged)
		}
	}

	done := false
	return runtime.NewPager(runtime.PagingHandler[aztables.ListEntitiesResponse]{
		More: func(_ aztables.ListEntitiesResponse) bool { return !done },
		Fetcher: func(_ context.Context, _ *aztables.ListEntitiesResponse) (aztables.ListEntitiesResponse, error) {
			done = true
			return aztables.ListEntitiesResponse{Entities: filtered}, nil
		},
	})
}

// ---------------------------------------------------------------------------
// Sentinel errors matching the string-detection helpers in aztable.go
// ---------------------------------------------------------------------------
//
// IMPORTANT: these errors must NOT embed pk or rk values in the error message
// body before the status-code keyword. The production error classifiers
// (isNotFoundError, isPreconditionFailedError) use substring matching on
// "ResourceNotFound"/"404"/"UpdateConditionNotSatisfied"/"412". Entity keys
// are derived from nanosecond timestamps and can contain those digit sequences
// (e.g. a key like "cf-17774…404…" would cause isNotFoundError to return true
// for a precondition error, silently swallowing the collision and letting both
// concurrent MarkBilled writers succeed — the root cause of the flake in
// campfireagent-426).
//
// The sentinel strings are matched by keyword, not by entity location, so
// pk/rk context is omitted here to prevent false-positive substring matches.

func mockNotFoundError(pk, rk string) error {
	// Matches isNotFoundError: contains "ResourceNotFound".
	// pk/rk intentionally not embedded — see note above.
	_ = pk
	_ = rk
	return fmt.Errorf("ResourceNotFound: entity not found (404 Not Found)")
}

func mockConflictError(pk, rk string) error {
	// Matches isConflictError: contains "EntityAlreadyExists".
	_ = pk
	_ = rk
	return fmt.Errorf("EntityAlreadyExists: entity already exists (409 Conflict)")
}

func mockPreconditionError(pk, rk string) error {
	// Matches isPreconditionFailedError: contains "UpdateConditionNotSatisfied".
	// pk/rk intentionally not embedded — see note above.
	_ = pk
	_ = rk
	return fmt.Errorf("UpdateConditionNotSatisfied: ETag mismatch (412 Precondition Failed)")
}

// ---------------------------------------------------------------------------
// OData filter evaluation (subset sufficient for the dispatch store filters)
// ---------------------------------------------------------------------------

func mockMatchesFilter(entity map[string]any, filter string) bool {
	return evalODataAnd(entity, filter)
}

func evalODataAnd(entity map[string]any, expr string) bool {
	for _, part := range splitOData(expr, " and ") {
		if !evalODataOr(entity, part) {
			return false
		}
	}
	return true
}

func evalODataOr(entity map[string]any, expr string) bool {
	expr = trimParens(expr)
	for _, part := range splitOData(expr, " or ") {
		if evalODataPrimitive(entity, trimParens(part)) {
			return true
		}
	}
	return false
}

func splitOData(expr, sep string) []string {
	var parts []string
	depth, start := 0, 0
	for i := 0; i <= len(expr)-len(sep); i++ {
		switch expr[i] {
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth == 0 && expr[i:i+len(sep)] == sep {
			parts = append(parts, expr[start:i])
			start = i + len(sep)
			i += len(sep) - 1
		}
	}
	return append(parts, expr[start:])
}

func trimParens(s string) string {
	for len(s) >= 2 && s[0] == '(' && s[len(s)-1] == ')' {
		s = s[1 : len(s)-1]
	}
	return s
}

func evalODataPrimitive(entity map[string]any, expr string) bool {
	if field, val, ok := parseStringEq(expr); ok {
		v, exists := entity[field]
		if !exists {
			return false
		}
		return fmt.Sprintf("%v", v) == val
	}
	if field, op, num, ok := parseNumericOp(expr); ok {
		v := entityNumeric(entity, field)
		switch op {
		case "eq":
			return v == num
		case "gt":
			return v > num
		case "lt":
			return v < num
		case "ge":
			return v >= num
		case "le":
			return v <= num
		}
	}
	// Default-deny: unrecognised filter expression does NOT match any row.
	// Returning true here would silently return ALL rows when a new filter
	// pattern is added but not yet taught to the mock — masking bugs.
	// Supported patterns: string " eq '<val>'", numeric " eq/gt/lt/ge/le <n>".
	// Callers verified (2026-04-29): evalODataOr (via evalODataAnd/mockMatchesFilter),
	// exercised by: TestODataFilter_UnsupportedExpr (new), TestCAS_MarkBilled_* via
	// ListUnbilledDispatches, helperCASSetupUnbilled, NewListEntitiesPager usage in
	// ListStaleDispatches / ListExpiredDispatches — none rely on default-allow.
	return false
}

func parseStringEq(expr string) (field, val string, ok bool) {
	const op = " eq '"
	idx := indexOf(expr, op)
	if idx < 0 {
		return "", "", false
	}
	rest := expr[idx+len(op):]
	if len(rest) == 0 || rest[len(rest)-1] != '\'' {
		return "", "", false
	}
	return expr[:idx], rest[:len(rest)-1], true
}

func parseNumericOp(expr string) (field, op string, val int64, ok bool) {
	for _, o := range []string{" eq ", " gt ", " lt ", " ge ", " le "} {
		idx := indexOf(expr, o)
		if idx < 0 {
			continue
		}
		rest := expr[idx+len(o):]
		var n int64
		if _, err := fmt.Sscanf(rest, "%d", &n); err != nil {
			continue
		}
		trimmed := o
		for len(trimmed) > 0 && trimmed[0] == ' ' {
			trimmed = trimmed[1:]
		}
		for len(trimmed) > 0 && trimmed[len(trimmed)-1] == ' ' {
			trimmed = trimmed[:len(trimmed)-1]
		}
		return expr[:idx], trimmed, n, true
	}
	return "", "", 0, false
}

func entityNumeric(entity map[string]any, field string) int64 {
	v, ok := entity[field]
	if !ok {
		return 0
	}
	// Delegate to the package-level toInt64 which handles json.Number (from
	// unmarshalEntity/UseNumber), float64, int64, and int uniformly.
	return toInt64(v)
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func pkrkFromJSON(data []byte) (pk, rk string, err error) {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return "", "", err
	}
	pkVal, _ := m["PartitionKey"].(string)
	rkVal, _ := m["RowKey"].(string)
	if pkVal == "" || rkVal == "" {
		return "", "", fmt.Errorf("cas_mock: entity missing PartitionKey or RowKey")
	}
	return pkVal, rkVal, nil
}

// NewMockDispatchStore returns a TableDispatchStore backed by two deterministic
// in-memory CAS tables. ETag semantics are enforced by a Go mutex — no Azurite.
// Used by CAS contract tests in dispatch_store_cas_test.go.
func NewMockDispatchStore() *TableDispatchStore {
	return NewDispatchStoreFromClients(newCASMockTable(), newCASMockTable())
}

// TestODataFilter_UnsupportedExpr verifies that evalODataPrimitive returns false
// (default-deny) for filter expressions that it does not recognise.
//
// Before the fix, it returned true — silently returning ALL rows when a caller
// supplied a filter the mock didn't understand (e.g. " ne ", " contains ", or
// a PartitionKey range filter). This would mask bugs in future callers.
func TestODataFilter_UnsupportedExpr(t *testing.T) {
	tbl := newCASMockTable()

	// Seed two rows with distinct PartitionKeys.
	entity := func(pk, rk string) []byte {
		b, _ := json.Marshal(map[string]any{
			"PartitionKey": pk,
			"RowKey":       rk,
			"Status":       "fulfilled",
		})
		return b
	}
	ctx := context.Background()
	if _, err := tbl.AddEntity(ctx, entity("pk-a", "rk-1"), nil); err != nil {
		t.Fatalf("AddEntity pk-a: %v", err)
	}
	if _, err := tbl.AddEntity(ctx, entity("pk-b", "rk-2"), nil); err != nil {
		t.Fatalf("AddEntity pk-b: %v", err)
	}

	unsupportedFilters := []string{
		"Status ne 'fulfilled'",                      // ne operator — not implemented
		"substringof('foo', Status)",                 // OData function — not implemented
		"PartitionKey ge 'pk-a' and PartitionKey lt 'pk-z'", // range filter — not implemented
		"unknownField bogusOp 42",                    // completely unrecognised expression
	}

	for _, filter := range unsupportedFilters {
		filter := filter
		t.Run(filter, func(t *testing.T) {
			pager := tbl.NewListEntitiesPager(&aztables.ListEntitiesOptions{
				Filter: &filter,
			})
			var rows []aztables.ListEntitiesResponse
			for pager.More() {
				page, err := pager.NextPage(ctx)
				if err != nil {
					t.Fatalf("NextPage: %v", err)
				}
				rows = append(rows, page)
			}
			var total int
			for _, page := range rows {
				total += len(page.Entities)
			}
			if total != 0 {
				t.Errorf("unsupported filter %q: expected 0 rows (default-deny), got %d", filter, total)
			}
		})
	}

	// Sanity: a supported filter still returns matching rows.
	t.Run("supported_filter_still_works", func(t *testing.T) {
		filter := "Status eq 'fulfilled'"
		pager := tbl.NewListEntitiesPager(&aztables.ListEntitiesOptions{
			Filter: &filter,
		})
		var total int
		for pager.More() {
			page, err := pager.NextPage(ctx)
			if err != nil {
				t.Fatalf("NextPage: %v", err)
			}
			total += len(page.Entities)
		}
		if total != 2 {
			t.Errorf("supported filter %q: expected 2 rows, got %d", filter, total)
		}
	})
}

// TestMockSentinelErrors_NoFalsePositive is a regression test for the
// campfireagent-426 flake.
//
// Root cause: mockPreconditionError embedded pk/rk in the error message.
// Entity keys are derived from nanosecond timestamps and can contain "404" as
// a digit subsequence. isNotFoundError checks for the substring "404", so a
// precondition-failure error whose pk/rk happened to contain "404" would be
// misclassified as a not-found error — silently returning nil from
// MarkBilledWithBarrier instead of ErrConcurrentModification. Both concurrent
// goroutines would win, breaking the "exactly 1 winner" invariant.
//
// Fix: mock sentinel errors do NOT embed pk/rk values. This test verifies
// that mockPreconditionError is not misclassified as a not-found error
// (even when called with keys that contain status-code digit sequences),
// and that mockNotFoundError is not misclassified as a precondition error.
func TestMockSentinelErrors_NoFalsePositive(t *testing.T) {
	// Keys that contain the "404" and "412" status code substrings, exercising
	// the exact collision that caused the flake.
	pkWith404 := "cf-177743534233489404"
	rkWith412 := "msg-177743534233412000"

	precondErr := mockPreconditionError(pkWith404, rkWith412)
	notFoundErr := mockNotFoundError(pkWith404, rkWith412)

	// Precondition error must NOT be classified as not-found.
	if isNotFoundError(precondErr) {
		t.Errorf("mockPreconditionError misclassified as not-found: %v", precondErr)
	}
	// Precondition error MUST be classified as precondition-failed.
	if !isPreconditionFailedError(precondErr) {
		t.Errorf("mockPreconditionError not classified as precondition-failed: %v", precondErr)
	}

	// Not-found error MUST be classified as not-found.
	if !isNotFoundError(notFoundErr) {
		t.Errorf("mockNotFoundError not classified as not-found: %v", notFoundErr)
	}
	// Not-found error must NOT be classified as precondition-failed.
	if isPreconditionFailedError(notFoundErr) {
		t.Errorf("mockNotFoundError misclassified as precondition-failed: %v", notFoundErr)
	}
}
