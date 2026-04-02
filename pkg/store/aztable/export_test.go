package aztable

import "context"

// DecrementStorageCounter exposes decrementStorageCounter for testing.
func (ts *TableStore) DecrementStorageCounter(ctx context.Context, campfireID string, deltaBytes, deltaMessages int64) error {
	return ts.decrementStorageCounter(ctx, campfireID, deltaBytes, deltaMessages)
}
