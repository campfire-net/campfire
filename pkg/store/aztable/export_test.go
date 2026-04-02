package aztable

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
	"github.com/campfire-net/campfire/pkg/convention"
)

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
