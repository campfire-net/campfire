// Package aztable — dispatch_store.go
//
// Azure Table Storage implementation of convention.DispatchStore.
//
// Tables:
//   - CampfireConventionCursors    PK=encodeKey(serverID)   RK=encodeKey(campfireID)
//   - CampfireConventionDispatched PK=encodeKey(campfireID) RK=encodeKey(messageID)
//
// See also convention_servers.go for the CampfireConventionServers registry table
// (provisioning/registry side, not part of DispatchStore).
package aztable

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
	"github.com/campfire-net/campfire/pkg/convention"
)

// Compile-time assertion that *TableDispatchStore implements convention.DispatchStore.
var _ convention.DispatchStore = (*TableDispatchStore)(nil)

// Table names for the convention server dispatch state.
const (
	conventionCursorsTable    = "CampfireConventionCursors"
	conventionDispatchedTable = "CampfireConventionDispatched"
)

// TableDispatchStore implements convention.DispatchStore against Azure Table Storage.
type TableDispatchStore struct {
	cursors    *aztables.Client // CampfireConventionCursors
	dispatched *aztables.Client // CampfireConventionDispatched
}

// NewDispatchStore connects to Azure Table Storage and ensures the cursor and
// dispatched tables exist. Returns a convention.DispatchStore.
func NewDispatchStore(connectionString string) (*TableDispatchStore, error) {
	svc, err := aztables.NewServiceClientFromConnectionString(connectionString, nil)
	if err != nil {
		return nil, fmt.Errorf("aztable: DispatchStore: creating service client: %w", err)
	}

	s := &TableDispatchStore{}
	tables := []struct {
		name   string
		target **aztables.Client
	}{
		{conventionCursorsTable, &s.cursors},
		{conventionDispatchedTable, &s.dispatched},
	}

	ctx := context.Background()
	for _, t := range tables {
		client := svc.NewClient(t.name)
		_, createErr := client.CreateTable(ctx, nil)
		if createErr != nil && !isTableExistsError(createErr) {
			return nil, fmt.Errorf("aztable: DispatchStore: ensuring table %s: %w", t.name, createErr)
		}
		*t.target = client
	}

	return s, nil
}

// ---------------------------------------------------------------------------
// GetCursor / AdvanceCursor
// ---------------------------------------------------------------------------

// GetCursor returns the last-dispatched message timestamp for a (serverID, campfireID) pair.
// Returns 0 if no cursor exists.
func (s *TableDispatchStore) GetCursor(ctx context.Context, serverID, campfireID string) (int64, error) {
	pk := encodeKey(serverID)
	rk := encodeKey(campfireID)
	raw, err := getEntity(ctx, s.cursors, pk, rk)
	if err != nil {
		return 0, fmt.Errorf("aztable: DispatchStore.GetCursor: %w", err)
	}
	if raw == nil {
		return 0, nil
	}
	return cursorFromEntity(raw), nil
}

// cursorFromEntity extracts the Cursor value from a raw entity map.
// Stored as a decimal string to preserve nanosecond precision across the
// JSON float64 round-trip (Azure Table Storage returns int64 values as
// float64 in JSON, losing the low-order bits for large nanosecond values).
func cursorFromEntity(m map[string]any) int64 {
	if s, ok := m["Cursor"].(string); ok {
		v, _ := strconv.ParseInt(s, 10, 64)
		return v
	}
	// Legacy fallback for values stored as numeric before this change.
	return toInt64(m["Cursor"])
}

// AdvanceCursor conditionally advances the cursor for (serverID, campfireID) to
// newTimestamp. Uses ETag-based optimistic concurrency to prevent lost updates.
// Returns true if advanced, false if the cursor was already at or past newTimestamp.
func (s *TableDispatchStore) AdvanceCursor(ctx context.Context, serverID, campfireID string, newTimestamp int64) (bool, error) {
	const maxRetries = 5
	pk := encodeKey(serverID)
	rk := encodeKey(campfireID)

	for attempt := 0; attempt < maxRetries; attempt++ {
		// Read current entity (with ETag).
		resp, err := s.cursors.GetEntity(ctx, pk, rk, nil)
		if err != nil {
			if isNotFoundError(err) {
				// No cursor yet — insert fresh.
				// Store Cursor as a decimal string to preserve nanosecond
				// precision across the JSON float64 round-trip.
				entity := map[string]any{
					"PartitionKey": pk,
					"RowKey":       rk,
					"Cursor":       strconv.FormatInt(newTimestamp, 10),
					"UpdatedAt":    strconv.FormatInt(time.Now().UnixNano(), 10),
				}
				insertErr := insertEntity(ctx, s.cursors, entity)
				if insertErr == nil {
					return true, nil
				}
				if isConflictError(insertErr) {
					// Concurrent insert — retry the read.
					continue
				}
				return false, fmt.Errorf("aztable: DispatchStore.AdvanceCursor: insert: %w", insertErr)
			}
			return false, fmt.Errorf("aztable: DispatchStore.AdvanceCursor: get: %w", err)
		}

		var m map[string]any
		if err := json.Unmarshal(resp.Value, &m); err != nil {
			return false, fmt.Errorf("aztable: DispatchStore.AdvanceCursor: unmarshal: %w", err)
		}

		current := cursorFromEntity(m)
		if newTimestamp <= current {
			return false, nil
		}

		// Advance with ETag guard. Store as string to preserve nanosecond precision.
		m["Cursor"] = strconv.FormatInt(newTimestamp, 10)
		m["UpdatedAt"] = strconv.FormatInt(time.Now().UnixNano(), 10)
		data, err := json.Marshal(m)
		if err != nil {
			return false, fmt.Errorf("aztable: DispatchStore.AdvanceCursor: marshal: %w", err)
		}
		etag := resp.ETag
		_, updateErr := s.cursors.UpdateEntity(ctx, data, &aztables.UpdateEntityOptions{
			UpdateMode: aztables.UpdateModeReplace,
			IfMatch:    &etag,
		})
		if updateErr == nil {
			return true, nil
		}
		if isPreconditionFailedError(updateErr) {
			// Concurrent write — retry.
			continue
		}
		return false, fmt.Errorf("aztable: DispatchStore.AdvanceCursor: update: %w", updateErr)
	}

	return false, fmt.Errorf("aztable: DispatchStore.AdvanceCursor: too many retries on concurrency conflict")
}

// ---------------------------------------------------------------------------
// MarkDispatched / MarkFulfilled / MarkFailed / GetDispatchStatus
// ---------------------------------------------------------------------------

// MarkDispatched records that a message was dispatched to a handler.
// Returns false if the message was already marked (insert-if-not-exists semantics).
func (s *TableDispatchStore) MarkDispatched(ctx context.Context, campfireID, messageID, serverID, forgeAccountID, conv, operation string) (bool, error) {
	pk := encodeKey(campfireID)
	rk := encodeKey(messageID)

	entity := map[string]any{
		"PartitionKey":    pk,
		"RowKey":          rk,
		"CampfireID":      campfireID,
		"MessageID":       messageID,
		"ServerID":        serverID,
		"ForgeAccountID":  forgeAccountID,
		"Convention":      conv,
		"Operation":       operation,
		"DispatchedAt":    time.Now().UnixNano(),
		"Status":          "dispatched",
		"HandlerURL":      "",
		"RedispatchCount": int64(0),
		"TokensConsumed":  int64(0),
		"BilledAt":        int64(0),
	}

	data, err := json.Marshal(entity)
	if err != nil {
		return false, fmt.Errorf("aztable: DispatchStore.MarkDispatched: marshal: %w", err)
	}
	_, addErr := s.dispatched.AddEntity(ctx, data, nil)
	if addErr != nil {
		if isConflictError(addErr) {
			// Already dispatched.
			return false, nil
		}
		return false, fmt.Errorf("aztable: DispatchStore.MarkDispatched: %w", addErr)
	}
	return true, nil
}

// updateDispatchStatus sets the Status field of a dispatch record.
func (s *TableDispatchStore) updateDispatchStatus(ctx context.Context, campfireID, messageID, status string) error {
	pk := encodeKey(campfireID)
	rk := encodeKey(messageID)

	raw, err := getEntity(ctx, s.dispatched, pk, rk)
	if err != nil {
		return fmt.Errorf("aztable: DispatchStore.%s: get: %w", status, err)
	}
	if raw == nil {
		// No record — nothing to update (matches MemoryDispatchStore behaviour).
		return nil
	}
	raw["Status"] = status
	if err := upsertEntity(ctx, s.dispatched, raw); err != nil {
		return fmt.Errorf("aztable: DispatchStore.%s: upsert: %w", status, err)
	}
	return nil
}

// MarkFulfilled updates the dispatch marker status to "fulfilled".
func (s *TableDispatchStore) MarkFulfilled(ctx context.Context, campfireID, messageID string) error {
	return s.updateDispatchStatus(ctx, campfireID, messageID, "fulfilled")
}

// MarkFailed updates the dispatch marker status to "failed".
func (s *TableDispatchStore) MarkFailed(ctx context.Context, campfireID, messageID string) error {
	return s.updateDispatchStatus(ctx, campfireID, messageID, "failed")
}

// GetDispatchStatus returns the status of a dispatched message.
// Returns "", nil if no dispatch record exists.
func (s *TableDispatchStore) GetDispatchStatus(ctx context.Context, campfireID, messageID string) (string, error) {
	pk := encodeKey(campfireID)
	rk := encodeKey(messageID)
	raw, err := getEntity(ctx, s.dispatched, pk, rk)
	if err != nil {
		return "", fmt.Errorf("aztable: DispatchStore.GetDispatchStatus: %w", err)
	}
	if raw == nil {
		return "", nil
	}
	return str(raw, "Status"), nil
}

// ---------------------------------------------------------------------------
// ListStaleDispatches / CleanupOldDispatches
// ---------------------------------------------------------------------------

// ListStaleDispatches returns dispatched-but-not-fulfilled entries older than
// the given threshold. Used by the fallback sweep.
func (s *TableDispatchStore) ListStaleDispatches(ctx context.Context, olderThan time.Duration) ([]convention.DispatchRecord, error) {
	threshold := time.Now().Add(-olderThan).UnixNano()
	// OData filter: Status eq 'dispatched' and DispatchedAt lt <threshold>
	filter := fmt.Sprintf("Status eq 'dispatched' and DispatchedAt lt %d", threshold)
	opts := &aztables.ListEntitiesOptions{
		Filter: strPtr(filter),
	}
	pager := s.dispatched.NewListEntitiesPager(opts)

	var result []convention.DispatchRecord
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("aztable: DispatchStore.ListStaleDispatches: %w", err)
		}
		for _, rawBytes := range page.Entities {
			var m map[string]any
			if err := json.Unmarshal(rawBytes, &m); err != nil {
				continue
			}
			rec := dispatchRecordFromEntity(m)
			result = append(result, rec)
		}
	}
	return result, nil
}

// CleanupOldDispatches removes fulfilled/failed entries older than maxAge.
// Returns the number of entries removed.
func (s *TableDispatchStore) CleanupOldDispatches(ctx context.Context, maxAge time.Duration) (int, error) {
	threshold := time.Now().Add(-maxAge).UnixNano()
	// OData filter: (Status eq 'fulfilled' or Status eq 'failed') and DispatchedAt lt <threshold>
	filter := fmt.Sprintf("(Status eq 'fulfilled' or Status eq 'failed') and DispatchedAt lt %d", threshold)
	opts := &aztables.ListEntitiesOptions{
		Filter: strPtr(filter),
	}
	pager := s.dispatched.NewListEntitiesPager(opts)

	count := 0
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return count, fmt.Errorf("aztable: DispatchStore.CleanupOldDispatches: list: %w", err)
		}
		for _, rawBytes := range page.Entities {
			var m map[string]any
			if err := json.Unmarshal(rawBytes, &m); err != nil {
				continue
			}
			pk := str(m, "PartitionKey")
			rk := str(m, "RowKey")
			if err := deleteEntity(ctx, s.dispatched, pk, rk); err != nil {
				return count, fmt.Errorf("aztable: DispatchStore.CleanupOldDispatches: delete: %w", err)
			}
			count++
		}
	}
	return count, nil
}

// IncrementRedispatchCount atomically increments the RedispatchCount field for a
// dispatch record and returns the new count. Returns 0, nil if no record exists.
func (s *TableDispatchStore) IncrementRedispatchCount(ctx context.Context, campfireID, messageID string) (int, error) {
	pk := encodeKey(campfireID)
	rk := encodeKey(messageID)

	const maxRetries = 5
	for attempt := 0; attempt < maxRetries; attempt++ {
		resp, err := s.dispatched.GetEntity(ctx, pk, rk, nil)
		if err != nil {
			if isNotFoundError(err) {
				return 0, nil
			}
			return 0, fmt.Errorf("aztable: DispatchStore.IncrementRedispatchCount: get: %w", err)
		}
		var m map[string]any
		if err := json.Unmarshal(resp.Value, &m); err != nil {
			return 0, fmt.Errorf("aztable: DispatchStore.IncrementRedispatchCount: unmarshal: %w", err)
		}
		current := int(toInt64(m["RedispatchCount"]))
		newCount := current + 1
		m["RedispatchCount"] = int64(newCount)
		data, err := json.Marshal(m)
		if err != nil {
			return 0, fmt.Errorf("aztable: DispatchStore.IncrementRedispatchCount: marshal: %w", err)
		}
		etag := resp.ETag
		_, updateErr := s.dispatched.UpdateEntity(ctx, data, &aztables.UpdateEntityOptions{
			UpdateMode: aztables.UpdateModeReplace,
			IfMatch:    &etag,
		})
		if updateErr == nil {
			return newCount, nil
		}
		if isPreconditionFailedError(updateErr) {
			continue
		}
		return 0, fmt.Errorf("aztable: DispatchStore.IncrementRedispatchCount: update: %w", updateErr)
	}
	return 0, fmt.Errorf("aztable: DispatchStore.IncrementRedispatchCount: too many retries on concurrency conflict")
}

// ---------------------------------------------------------------------------
// ListUnbilledDispatches / MarkBilled
// ---------------------------------------------------------------------------

// ListUnbilledDispatches returns fulfilled dispatch records where
// TokensConsumed > 0 and BilledAt == 0. Used by the billing sweep.
func (s *TableDispatchStore) ListUnbilledDispatches(ctx context.Context) ([]convention.DispatchRecord, error) {
	// OData filter: Status eq 'fulfilled' and TokensConsumed gt 0 and BilledAt eq 0
	filter := "Status eq 'fulfilled' and TokensConsumed gt 0 and BilledAt eq 0"
	opts := &aztables.ListEntitiesOptions{
		Filter: strPtr(filter),
	}
	pager := s.dispatched.NewListEntitiesPager(opts)

	var result []convention.DispatchRecord
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("aztable: DispatchStore.ListUnbilledDispatches: %w", err)
		}
		for _, rawBytes := range page.Entities {
			var m map[string]any
			if err := json.Unmarshal(rawBytes, &m); err != nil {
				continue
			}
			rec := dispatchRecordFromEntity(m)
			result = append(result, rec)
		}
	}
	return result, nil
}

// MarkBilled sets BilledAt on a dispatch record to the current time.
// Uses ETag-based optimistic concurrency to prevent lost updates: if the
// record was modified since the caller read it (e.g. by a concurrent
// IncrementRedispatchCount), returns convention.ErrConcurrentModification.
// No-op (returns nil) if the record does not exist.
//
// The etag parameter from the interface is not used directly — Azure Table
// Storage provides its own authoritative ETag via GetEntity. The guard is
// the Azure ETag read at MarkBilled time, which detects any concurrent write.
func (s *TableDispatchStore) MarkBilled(ctx context.Context, campfireID, messageID, _ string) error {
	pk := encodeKey(campfireID)
	rk := encodeKey(messageID)

	resp, err := s.dispatched.GetEntity(ctx, pk, rk, nil)
	if err != nil {
		if isNotFoundError(err) {
			return nil
		}
		return fmt.Errorf("aztable: DispatchStore.MarkBilled: get: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(resp.Value, &m); err != nil {
		return fmt.Errorf("aztable: DispatchStore.MarkBilled: unmarshal: %w", err)
	}
	m["BilledAt"] = time.Now().UnixNano()
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("aztable: DispatchStore.MarkBilled: marshal: %w", err)
	}
	etag := resp.ETag
	_, updateErr := s.dispatched.UpdateEntity(ctx, data, &aztables.UpdateEntityOptions{
		UpdateMode: aztables.UpdateModeReplace,
		IfMatch:    &etag,
	})
	if updateErr != nil {
		if isPreconditionFailedError(updateErr) {
			return fmt.Errorf("%w: Azure ETag mismatch on %s/%s", convention.ErrConcurrentModification, campfireID, messageID)
		}
		return fmt.Errorf("aztable: DispatchStore.MarkBilled: update: %w", updateErr)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// dispatchRecordFromEntity converts a raw Table Storage entity map to a DispatchRecord.
// The odata.etag field from list responses is mapped to ETag for optimistic concurrency.
func dispatchRecordFromEntity(m map[string]any) convention.DispatchRecord {
	dispatchedAtNs := toInt64(m["DispatchedAt"])
	return convention.DispatchRecord{
		CampfireID:      campfireIDFromDispatchEntity(m),
		MessageID:       messageIDFromDispatchEntity(m),
		ServerID:        str(m, "ServerID"),
		ForgeAccountID:  str(m, "ForgeAccountID"),
		Convention:      str(m, "Convention"),
		Operation:       str(m, "Operation"),
		DispatchedAt:    time.Unix(0, dispatchedAtNs),
		Status:          str(m, "Status"),
		HandlerURL:      str(m, "HandlerURL"),
		RedispatchCount: int(toInt64(m["RedispatchCount"])),
		TokensConsumed:  toInt64(m["TokensConsumed"]),
		BilledAt:        toInt64(m["BilledAt"]),
		ETag:            str(m, "odata.etag"),
	}
}

// campfireIDFromDispatchEntity decodes the campfire ID from the PartitionKey.
// The PartitionKey is encodeKey(campfireID); we store the raw campfireID in a
// dedicated property so we can reconstruct it without reversing the encoding.
// However, since we don't store CampfireID as a property in MarkDispatched,
// we fall back to the encoded PK. For accurate round-tripping, callers that
// need the original campfireID should store it explicitly. Here we store it.
func campfireIDFromDispatchEntity(m map[string]any) string {
	if v := str(m, "CampfireID"); v != "" {
		return v
	}
	return str(m, "PartitionKey")
}

// messageIDFromDispatchEntity decodes the message ID from the RowKey.
func messageIDFromDispatchEntity(m map[string]any) string {
	if v := str(m, "MessageID"); v != "" {
		return v
	}
	return str(m, "RowKey")
}
