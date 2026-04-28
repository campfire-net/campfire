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
		var base map[string]any
		if jsonErr := json.Unmarshal(row.data, &base); jsonErr != nil {
			return aztables.UpdateEntityResponse{}, jsonErr
		}
		var patch map[string]any
		if jsonErr := json.Unmarshal(entity, &patch); jsonErr != nil {
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
		var entity map[string]any
		if err := json.Unmarshal(row.data, &entity); err != nil {
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

func mockNotFoundError(pk, rk string) error {
	return fmt.Errorf("ResourceNotFound: entity not found pk=%s rk=%s (404 Not Found)", pk, rk)
}

func mockConflictError(pk, rk string) error {
	return fmt.Errorf("EntityAlreadyExists: entity already exists pk=%s rk=%s (409 Conflict)", pk, rk)
}

func mockPreconditionError(pk, rk string) error {
	return fmt.Errorf("UpdateConditionNotSatisfied: ETag mismatch pk=%s rk=%s (412 Precondition Failed)", pk, rk)
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
	return true
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
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	}
	return 0
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
