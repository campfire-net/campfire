package metering

import (
	"context"

	"github.com/campfire-net/campfire/pkg/store/aztable"
)

// TableStoreAdapter wraps *aztable.TableStore and implements StorageStore,
// PeerCountStore, and GCStore for use with the metering timer functions.
type TableStoreAdapter struct {
	ts *aztable.TableStore
}

// NewTableStoreAdapter creates an adapter around a raw TableStore.
func NewTableStoreAdapter(ts *aztable.TableStore) *TableStoreAdapter {
	return &TableStoreAdapter{ts: ts}
}

// ListStorageCounters implements StorageStore.
func (a *TableStoreAdapter) ListStorageCounters(ctx context.Context) ([]StorageCounter, error) {
	recs, err := a.ts.ListStorageCounters(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]StorageCounter, len(recs))
	for i, r := range recs {
		out[i] = StorageCounter{
			CampfireID:   r.CampfireID,
			BytesStored:  r.BytesStored,
			MessageCount: r.MessageCount,
		}
	}
	return out, nil
}

// ListCampfirePeerCounts implements PeerCountStore.
func (a *TableStoreAdapter) ListCampfirePeerCounts(ctx context.Context) ([]PeerCount, error) {
	recs, err := a.ts.ListCampfirePeerCounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PeerCount, len(recs))
	for i, r := range recs {
		out[i] = PeerCount{
			CampfireID: r.CampfireID,
			Count:      r.Count,
		}
	}
	return out, nil
}

// ListMessagesOlderThan implements GCStore.
func (a *TableStoreAdapter) ListMessagesOlderThan(ctx context.Context, campfireID string, cutoff int64) ([]OldMessage, error) {
	msgs, err := a.ts.ListMessagesOlderThan(ctx, campfireID, cutoff)
	if err != nil {
		return nil, err
	}
	out := make([]OldMessage, len(msgs))
	for i, m := range msgs {
		out[i] = OldMessage{
			ID:         m.ID,
			CampfireID: m.CampfireID,
		}
	}
	return out, nil
}

// DeleteMessage implements GCStore.
func (a *TableStoreAdapter) DeleteMessage(ctx context.Context, campfireID, messageID string) error {
	return a.ts.DeleteMessage(ctx, campfireID, messageID)
}
