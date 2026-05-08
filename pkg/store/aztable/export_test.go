package aztable

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
	"github.com/campfire-net/campfire/cf-conventions/cf-convention"
)

// WriteStorageCounterForTest directly writes a counter row with the given
// UpdatedAt value (bypassing the internal time.Now() call). This is a test
// helper for precision regression tests — it allows injecting a
// precision-sensitive nanosecond timestamp to verify Edm.Int64 annotation.
func (ts *TableStore) WriteStorageCounterForTest(ctx context.Context, campfireID string, bytesStored, messageCount, updatedAt int64) error {
	pk := ts.pk(campfireID)
	entity := map[string]any{
		"PartitionKey": pk,
		"RowKey":       storageCountersRowKey,
		"CampfireID":   campfireID,
		"BytesStored":  bytesStored,
		"MessageCount": messageCount,
		"UpdatedAt":    updatedAt,
	}
	annotateInt64(entity, "BytesStored", "MessageCount", "UpdatedAt")
	data, err := json.Marshal(entity)
	if err != nil {
		return fmt.Errorf("WriteStorageCounterForTest: marshal: %w", err)
	}
	_, err = ts.counters.UpsertEntity(ctx, data, &aztables.UpsertEntityOptions{
		UpdateMode: aztables.UpdateModeReplace,
	})
	return err
}

// GetRawStorageCounter reads the counter row and returns (bytesStored,
// messageCount, updatedAt, nil). Unlike GetStorageCounter, it also returns
// UpdatedAt so precision tests can verify the full round-trip.
func (ts *TableStore) GetRawStorageCounter(ctx context.Context, campfireID string) (bytesStored, messageCount, updatedAt int64, err error) {
	pk := ts.pk(campfireID)
	raw, err := getEntity(ctx, ts.counters, pk, storageCountersRowKey)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("GetRawStorageCounter: %w", err)
	}
	if raw == nil {
		return 0, 0, 0, nil
	}
	return toInt64(raw["BytesStored"]), toInt64(raw["MessageCount"]), toInt64(raw["UpdatedAt"]), nil
}

// GetDispatchEntityETag calls GetEntity on the dispatched table for the given
// campfireID/messageID and returns the raw ETag reported by Azure/Azurite.
// Used by integration tests to compare the ETag from GetEntity against the
// odata.etag field returned by the NewListEntitiesPager — verifying the two
// formats are byte-for-byte equal (campfireagent-f92 veracity check).
func (s *TableDispatchStore) GetDispatchEntityETag(ctx context.Context, campfireID, messageID string) (string, error) {
	pk := encodeKey(campfireID)
	rk := encodeKey(messageID)
	resp, err := s.dispatched.GetEntity(ctx, pk, rk, nil)
	if err != nil {
		return "", err
	}
	return string(resp.ETag), nil
}

// DecrementStorageCounter exposes decrementStorageCounter for testing.
func (ts *TableStore) DecrementStorageCounter(ctx context.Context, campfireID string, deltaBytes, deltaMessages int64) error {
	return ts.decrementStorageCounter(ctx, campfireID, deltaBytes, deltaMessages)
}

// UpdateDispatchStatusWithBarrier is a test helper that splits the read and
// write phases of updateDispatchStatus, calling afterRead() between them.
// This allows tests to inject a synchronization point so multiple goroutines
// can all complete their reads before any write fires — producing a guaranteed
// ETag collision with the same entity.
func (s *TableDispatchStore) UpdateDispatchStatusWithBarrier(ctx context.Context, campfireID, messageID, status string, afterRead func()) error {
	pk := encodeKey(campfireID)
	rk := encodeKey(messageID)

	resp, err := s.dispatched.GetEntity(ctx, pk, rk, nil)
	if err != nil {
		if isNotFoundError(err) {
			return convention.ErrDispatchNotFound
		}
		return fmt.Errorf("aztable: DispatchStore.%s: get: %w", status, err)
	}

	// Signal that the read is complete, then wait for the barrier to release.
	if afterRead != nil {
		afterRead()
	}

	m := map[string]any{
		"PartitionKey": pk,
		"RowKey":       rk,
		"Status":       status,
	}
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("aztable: DispatchStore.%s: marshal: %w", status, err)
	}
	etag := resp.ETag
	_, updateErr := s.dispatched.UpdateEntity(ctx, data, &aztables.UpdateEntityOptions{
		UpdateMode: aztables.UpdateModeMerge,
		IfMatch:    &etag,
	})
	if updateErr != nil {
		// Check precondition failure (412) BEFORE not-found (404): isNotFoundError
		// uses a substring match on "404" which can false-positive on entity keys
		// that happen to contain that digit sequence (e.g. nanosecond timestamps).
		if isPreconditionFailedError(updateErr) {
			return fmt.Errorf("%w: Azure ETag mismatch on %s/%s", convention.ErrConcurrentModification, campfireID, messageID)
		}
		if isNotFoundError(updateErr) || isMergeNotFoundError(updateErr) {
			return convention.ErrDispatchNotFound
		}
		return fmt.Errorf("aztable: DispatchStore.%s: update: %w", status, updateErr)
	}
	return nil
}

// MarkBilledWithBarrier is a test helper that splits the read and write phases
// of MarkBilled, calling afterRead() between the GetEntity and UpdateEntity
// calls. This allows tests to inject a synchronisation point so multiple
// goroutines can all complete their reads before any write fires — producing a
// guaranteed ETag collision on the same entity. The callerETag parameter is
// validated as in MarkBilled; after the read the returned resp.ETag is used for
// the conditional write, mirroring the production implementation.
func (s *TableDispatchStore) MarkBilledWithBarrier(ctx context.Context, campfireID, messageID, callerETag string, afterRead func()) error {
	if callerETag == "*" || callerETag == "" {
		return fmt.Errorf("%w: invalid ETag %q passed to MarkBilledWithBarrier", convention.ErrConcurrentModification, callerETag)
	}

	pk := encodeKey(campfireID)
	rk := encodeKey(messageID)

	resp, err := s.dispatched.GetEntity(ctx, pk, rk, nil)
	if err != nil {
		if isNotFoundError(err) {
			return nil
		}
		return fmt.Errorf("aztable: DispatchStore.MarkBilledWithBarrier: get: %w", err)
	}

	var current map[string]any
	if err := unmarshalEntity(resp.Value, &current); err != nil {
		return fmt.Errorf("aztable: DispatchStore.MarkBilledWithBarrier: unmarshal: %w", err)
	}
	// Check BilledAt before stale-ETag (mirrors production MarkBilled ordering).
	if billedAt := toInt64(current["BilledAt"]); billedAt != 0 {
		return fmt.Errorf("%w: campfireID=%q messageID=%q", convention.ErrAlreadyBilled, campfireID, messageID)
	}

	// Stale-ETag check mirrors production MarkBilled.
	if string(resp.ETag) != callerETag {
		return fmt.Errorf("%w: stale ETag on %s/%s (caller=%q server=%q)",
			convention.ErrConcurrentModification, campfireID, messageID, callerETag, resp.ETag)
	}

	// Signal that the read phase is complete, then wait for the barrier.
	if afterRead != nil {
		afterRead()
	}

	patch := map[string]any{
		"PartitionKey": pk,
		"RowKey":       rk,
		"BilledAt":     time.Now().UnixNano(),
	}
	annotateInt64(patch, "BilledAt")
	data, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("aztable: DispatchStore.MarkBilledWithBarrier: marshal: %w", err)
	}
	etag := resp.ETag
	_, updateErr := s.dispatched.UpdateEntity(ctx, data, &aztables.UpdateEntityOptions{
		UpdateMode: aztables.UpdateModeMerge,
		IfMatch:    &etag,
	})
	if updateErr != nil {
		if isNotFoundError(updateErr) || isMergeNotFoundError(updateErr) {
			return nil
		}
		if isPreconditionFailedError(updateErr) {
			_, getErr := s.dispatched.GetEntity(ctx, pk, rk, nil)
			if getErr != nil && isNotFoundError(getErr) {
				return nil
			}
			return fmt.Errorf("%w: Azure ETag mismatch on %s/%s", convention.ErrConcurrentModification, campfireID, messageID)
		}
		return fmt.Errorf("aztable: DispatchStore.MarkBilledWithBarrier: update: %w", updateErr)
	}
	return nil
}
