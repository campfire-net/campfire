// Package aztable — storage_counters.go
//
// CampfireStorageCounters table tracks per-campfire payload byte usage.
// The counter is incremented on every successful AddMessage and decremented
// when compaction supersedes messages. It is used by the hourly metering timer
// to compute GB-day usage for billing.
//
// Table: CampfireStorageCounters
//
//	PK = encodeKey(campfireID)  (namespace-prefixed via ts.pk())
//	RK = "counter"
package aztable

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

const (
	storageCountersTable  = "CampfireStorageCounters"
	storageCountersRowKey = "counter"
	// counterMaxRetries is the maximum number of ETag-conflict retries for
	// atomic counter updates. Set to 10 to handle bursts of concurrent writes
	// to the same campfire. The design notes contention is normally low
	// (campfires are typically single-writer), but hosted environments may see
	// occasional bursts.
	counterMaxRetries = 10
)

// StorageCounter holds the current storage metrics for a campfire.
type StorageCounter struct {
	BytesStored  int64
	MessageCount int64
	UpdatedAt    int64 // nanosecond timestamp
}

// storageCounterStore wraps an aztable client for the CampfireStorageCounters table.
type storageCounterStore struct {
	client *aztables.Client
}

// NewStorageCounterStore creates a client backed by CampfireStorageCounters.
// The table is created if it does not already exist.
func NewStorageCounterStore(connectionString string) (*storageCounterStore, error) {
	svc, err := aztables.NewServiceClientFromConnectionString(connectionString, nil)
	if err != nil {
		return nil, fmt.Errorf("aztable: StorageCounterStore: creating service client: %w", err)
	}
	client := svc.NewClient(storageCountersTable)
	ctx := context.Background()
	_, createErr := client.CreateTable(ctx, nil)
	if createErr != nil && !isTableExistsError(createErr) {
		return nil, fmt.Errorf("aztable: StorageCounterStore: ensuring table: %w", createErr)
	}
	return &storageCounterStore{client: client}, nil
}

// GetStorageCounter returns the current byte and message counters for a campfire.
// Returns (0, 0, nil) if no counter row exists yet (campfire has no messages).
func (ts *TableStore) GetStorageCounter(ctx context.Context, campfireID string) (bytesStored, messageCount int64, err error) {
	pk := ts.pk(campfireID)
	raw, err := getEntity(ctx, ts.counters, pk, storageCountersRowKey)
	if err != nil {
		return 0, 0, fmt.Errorf("aztable: GetStorageCounter: %w", err)
	}
	if raw == nil {
		return 0, 0, nil
	}
	return toInt64(raw["BytesStored"]), toInt64(raw["MessageCount"]), nil
}

// StorageCounterRecord is a single row from CampfireStorageCounters, returned
// by ListStorageCounters for use by the hourly metering timer.
type StorageCounterRecord struct {
	CampfireID   string
	BytesStored  int64
	MessageCount int64
}

// ListStorageCounters returns all campfires that have at least one byte stored.
// This is used by the hourly storage-emission timer to iterate over all counters
// without knowing the campfire IDs in advance.
func (ts *TableStore) ListStorageCounters(ctx context.Context) ([]StorageCounterRecord, error) {
	pager := ts.counters.NewListEntitiesPager(nil)
	var results []StorageCounterRecord
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("aztable: ListStorageCounters: %w", err)
		}
		for _, raw := range page.Entities {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				return nil, fmt.Errorf("aztable: ListStorageCounters unmarshal: %w", err)
			}
			bytes := toInt64(m["BytesStored"])
			if bytes <= 0 {
				continue
			}
			campfireID, _ := m["CampfireID"].(string)
			if campfireID == "" {
				// Legacy rows written before CampfireID was added: skip; they will
				// be updated on the next increment.
				continue
			}
			results = append(results, StorageCounterRecord{
				CampfireID:   campfireID,
				BytesStored:  bytes,
				MessageCount: toInt64(m["MessageCount"]),
			})
		}
	}
	return results, nil
}

// incrementStorageCounter atomically increments BytesStored by deltaBytes and
// MessageCount by 1 for the given campfireID. Uses ETag-based optimistic
// concurrency with up to counterMaxRetries retries on conflict.
func (ts *TableStore) incrementStorageCounter(ctx context.Context, campfireID string, deltaBytes int64) error {
	pk := ts.pk(campfireID)
	for attempt := 0; attempt < counterMaxRetries; attempt++ {
		resp, err := ts.counters.GetEntity(ctx, pk, storageCountersRowKey, nil)
		if err != nil {
			if !isNotFoundError(err) {
				return fmt.Errorf("aztable: incrementStorageCounter get: %w", err)
			}
			// Counter row doesn't exist — insert with initial values.
			entity := map[string]any{
				"PartitionKey": pk,
				"RowKey":       storageCountersRowKey,
				"CampfireID":   campfireID,
				"BytesStored":  deltaBytes,
				"MessageCount": int64(1),
				"UpdatedAt":    time.Now().UnixNano(),
			}
			data, merr := json.Marshal(entity)
			if merr != nil {
				return fmt.Errorf("aztable: incrementStorageCounter marshal: %w", merr)
			}
			_, addErr := ts.counters.AddEntity(ctx, data, nil)
			if addErr == nil {
				return nil
			}
			if isConflictError(addErr) {
				// Another writer inserted concurrently — retry the read+update path.
				continue
			}
			return fmt.Errorf("aztable: incrementStorageCounter insert: %w", addErr)
		}

		// Row exists — decode, increment, write back with ETag guard.
		var current map[string]any
		if merr := json.Unmarshal(resp.Value, &current); merr != nil {
			return fmt.Errorf("aztable: incrementStorageCounter unmarshal: %w", merr)
		}
		current["BytesStored"] = toInt64(current["BytesStored"]) + deltaBytes
		current["MessageCount"] = toInt64(current["MessageCount"]) + 1
		current["UpdatedAt"] = time.Now().UnixNano()

		data, merr := json.Marshal(current)
		if merr != nil {
			return fmt.Errorf("aztable: incrementStorageCounter marshal update: %w", merr)
		}
		etag := resp.ETag
		_, updateErr := ts.counters.UpdateEntity(ctx, data, &aztables.UpdateEntityOptions{
			UpdateMode: aztables.UpdateModeReplace,
			IfMatch:    &etag,
		})
		if updateErr == nil {
			return nil
		}
		if isPreconditionFailedError(updateErr) {
			// ETag conflict — retry.
			continue
		}
		return fmt.Errorf("aztable: incrementStorageCounter update: %w", updateErr)
	}
	return fmt.Errorf("aztable: incrementStorageCounter: exceeded %d retries for campfire %s", counterMaxRetries, campfireID)
}

// decrementStorageCounter atomically decrements BytesStored by deltaBytes for
// the given campfireID. The counter is clamped at 0 (never goes negative).
// Uses ETag-based optimistic concurrency with up to counterMaxRetries retries.
func (ts *TableStore) decrementStorageCounter(ctx context.Context, campfireID string, deltaBytes int64) error {
	if deltaBytes <= 0 {
		return nil
	}
	pk := ts.pk(campfireID)
	for attempt := 0; attempt < counterMaxRetries; attempt++ {
		resp, err := ts.counters.GetEntity(ctx, pk, storageCountersRowKey, nil)
		if err != nil {
			if isNotFoundError(err) {
				// No counter row — nothing to decrement.
				return nil
			}
			return fmt.Errorf("aztable: decrementStorageCounter get: %w", err)
		}

		var current map[string]any
		if merr := json.Unmarshal(resp.Value, &current); merr != nil {
			return fmt.Errorf("aztable: decrementStorageCounter unmarshal: %w", merr)
		}

		existing := toInt64(current["BytesStored"])
		newBytes := existing - deltaBytes
		if newBytes < 0 {
			newBytes = 0 // clamp
		}
		current["BytesStored"] = newBytes
		current["UpdatedAt"] = time.Now().UnixNano()

		data, merr := json.Marshal(current)
		if merr != nil {
			return fmt.Errorf("aztable: decrementStorageCounter marshal: %w", merr)
		}
		etag := resp.ETag
		_, updateErr := ts.counters.UpdateEntity(ctx, data, &aztables.UpdateEntityOptions{
			UpdateMode: aztables.UpdateModeReplace,
			IfMatch:    &etag,
		})
		if updateErr == nil {
			return nil
		}
		if isPreconditionFailedError(updateErr) {
			continue
		}
		return fmt.Errorf("aztable: decrementStorageCounter update: %w", updateErr)
	}
	return fmt.Errorf("aztable: decrementStorageCounter: exceeded %d retries for campfire %s", counterMaxRetries, campfireID)
}
