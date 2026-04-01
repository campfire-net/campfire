// Package metering implements the three timer-triggered metering functions for
// campfire-hosting:
//
//   - EmitStorageUsage      — hourly: reads CampfireStorageCounters and emits
//     "message-storage-gb-day" usage events to Forge.
//   - EmitPeerEndpointUsage — daily: counts active peer endpoints per campfire
//     and emits "peer-endpoint-day" usage events to Forge.
//   - GarbageCollectZeroBalance — periodic: deletes messages older than maxAge
//     from campfires whose operator has zero or negative Forge balance.
//
// All three functions are fail-safe: per-campfire errors are logged and skipped
// rather than aborting the run.
package metering

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/campfire-net/campfire/pkg/forge"
)

// StorageCounter holds per-campfire byte storage metrics as returned by the
// aztable store's ListStorageCounters method.
type StorageCounter struct {
	CampfireID   string
	BytesStored  int64
	MessageCount int64
}

// PeerCount holds per-campfire peer endpoint count as returned by
// ListCampfirePeerCounts.
type PeerCount struct {
	CampfireID string
	Count      int
}

// OldMessage is a minimal message record for GC scanning.
type OldMessage struct {
	ID         string
	CampfireID string
}

// StorageStore is the interface used by EmitStorageUsage.
type StorageStore interface {
	ListStorageCounters(ctx context.Context) ([]StorageCounter, error)
}

// PeerCountStore is the interface used by EmitPeerEndpointUsage.
type PeerCountStore interface {
	ListCampfirePeerCounts(ctx context.Context) ([]PeerCount, error)
}

// GCStore is the interface used by GarbageCollectZeroBalance.
type GCStore interface {
	// ListMessagesOlderThan returns all messages whose age exceeds cutoff (nanoseconds).
	// If campfireID is empty, all campfires are scanned.
	ListMessagesOlderThan(ctx context.Context, campfireID string, cutoff int64) ([]OldMessage, error)
	// DeleteMessage removes a single message by campfireID + messageID.
	DeleteMessage(ctx context.Context, campfireID, messageID string) error
}

// BalanceChecker returns the Forge balance in micro-USD for an account.
type BalanceChecker interface {
	Balance(ctx context.Context, accountID string) (int64, error)
}

// EmitStorageUsage reads all CampfireStorageCounters rows and emits a
// "message-storage-gb-day" UsageEvent to Forge for each campfire with
// BytesStored > 0.
//
// Quantity:
//
//	gb_days = (BytesStored / 1073741824.0) * (1.0 / 24.0)   // 1 hour = 1/24 day
//
// IdempotencyKey = campfireID + ":" + hourTimestamp
// where hourTimestamp = UTC hour formatted as "2006-01-02T15".
//
// accountLookup maps campfireID → Forge accountID. A blank return skips that
// campfire (not yet provisioned or campfire has no operator mapping).
func EmitStorageUsage(
	ctx context.Context,
	store StorageStore,
	emitter *forge.ForgeEmitter,
	accountLookup func(campfireID string) string,
) error {
	now := time.Now().UTC()
	hourTS := now.Format("2006-01-02T15")

	counters, err := store.ListStorageCounters(ctx)
	if err != nil {
		return fmt.Errorf("metering: EmitStorageUsage: list counters: %w", err)
	}

	var emitted, skipped int
	for _, c := range counters {
		if c.BytesStored <= 0 {
			continue
		}
		accountID := accountLookup(c.CampfireID)
		if accountID == "" {
			log.Printf("metering: EmitStorageUsage: no account for campfire %s, skipping", c.CampfireID)
			skipped++
			continue
		}

		// 1 byte = 1/1073741824 GB; 1 hour = 1/24 day.
		gbDays := (float64(c.BytesStored) / 1073741824.0) * (1.0 / 24.0)

		emitter.Emit(forge.UsageEvent{
			AccountID:      accountID,
			ServiceID:      "campfire-hosting",
			UnitType:       "message-storage-gb-day",
			Quantity:       gbDays,
			IdempotencyKey: c.CampfireID + ":" + hourTS,
			Timestamp:      now,
		})
		emitted++
	}

	log.Printf("metering: EmitStorageUsage: emitted=%d skipped=%d hour=%s", emitted, skipped, hourTS)
	return nil
}

// EmitPeerEndpointUsage counts active peer endpoints per campfire and emits a
// "peer-endpoint-day" UsageEvent to Forge for each campfire with >= 1 endpoint.
//
// IdempotencyKey = campfireID + ":" + dateString
// where dateString = UTC date formatted as "2006-01-02".
//
// accountLookup maps campfireID → Forge accountID. A blank return skips that
// campfire.
func EmitPeerEndpointUsage(
	ctx context.Context,
	store PeerCountStore,
	emitter *forge.ForgeEmitter,
	accountLookup func(campfireID string) string,
) error {
	now := time.Now().UTC()
	dateStr := now.Format("2006-01-02")

	peerCounts, err := store.ListCampfirePeerCounts(ctx)
	if err != nil {
		return fmt.Errorf("metering: EmitPeerEndpointUsage: list peer counts: %w", err)
	}

	var emitted, skipped int
	for _, pc := range peerCounts {
		if pc.Count <= 0 {
			continue
		}
		accountID := accountLookup(pc.CampfireID)
		if accountID == "" {
			log.Printf("metering: EmitPeerEndpointUsage: no account for campfire %s, skipping", pc.CampfireID)
			skipped++
			continue
		}

		emitter.Emit(forge.UsageEvent{
			AccountID:      accountID,
			ServiceID:      "campfire-hosting",
			UnitType:       "peer-endpoint-day",
			Quantity:       float64(pc.Count),
			IdempotencyKey: pc.CampfireID + ":" + dateStr,
			Timestamp:      now,
		})
		emitted++
	}

	log.Printf("metering: EmitPeerEndpointUsage: emitted=%d skipped=%d date=%s", emitted, skipped, dateStr)
	return nil
}

// GarbageCollectZeroBalance deletes messages older than maxAge from campfires
// whose operator has zero or negative Forge balance.
//
// This is cost optimization, not a billing gate. Operators with positive
// balances are never affected.
//
// accountLookup maps campfireID → Forge accountID. Campfires without a mapping
// are skipped.
//
// Returns the number of messages deleted. Per-campfire errors are logged and
// skipped (fail-safe).
func GarbageCollectZeroBalance(
	ctx context.Context,
	store GCStore,
	balanceChecker BalanceChecker,
	accountLookup func(campfireID string) string,
	maxAge time.Duration,
) (int, error) {
	cutoff := time.Now().Add(-maxAge).UnixNano()

	// Scan all messages older than cutoff across all campfires.
	old, err := store.ListMessagesOlderThan(ctx, "", cutoff)
	if err != nil {
		return 0, fmt.Errorf("metering: GarbageCollectZeroBalance: list old messages: %w", err)
	}

	// Group messages by campfire.
	byCampfire := make(map[string][]OldMessage)
	for _, m := range old {
		byCampfire[m.CampfireID] = append(byCampfire[m.CampfireID], m)
	}

	// Cache balance checks: one Forge call per unique accountID.
	balanceCache := make(map[string]int64) // accountID → balance_micro

	var totalDeleted int
	for campfireID, msgs := range byCampfire {
		accountID := accountLookup(campfireID)
		if accountID == "" {
			continue
		}

		balance, cached := balanceCache[accountID]
		if !cached {
			var balErr error
			balance, balErr = balanceChecker.Balance(ctx, accountID)
			if balErr != nil {
				log.Printf("metering: GarbageCollectZeroBalance: balance check account=%s: %v", accountID, balErr)
				continue
			}
			balanceCache[accountID] = balance
		}

		// Only GC campfires with zero or negative balance.
		if balance > 0 {
			continue
		}

		deleted := 0
		for _, msg := range msgs {
			if err := store.DeleteMessage(ctx, campfireID, msg.ID); err != nil {
				log.Printf("metering: GarbageCollectZeroBalance: delete message=%s campfire=%s: %v",
					msg.ID, campfireID, err)
				continue
			}
			deleted++
		}
		totalDeleted += deleted
		log.Printf("metering: GarbageCollectZeroBalance: campfire=%s account=%s balance_micro=%d deleted=%d",
			campfireID, accountID, balance, deleted)
	}

	log.Printf("metering: GarbageCollectZeroBalance: total_deleted=%d", totalDeleted)
	return totalDeleted, nil
}
