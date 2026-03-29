package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
)

// ErrNotImplemented is returned by stub transport methods that are not yet implemented.
var ErrNotImplemented = errors.New("not yet implemented")

// cliTransportAdapter implements convention.ExecutorTransport by routing
// messages through the same transport layer that `cf send` uses.
type cliTransportAdapter struct {
	agentID *identity.Identity
	store   store.Store
}

func (a *cliTransportAdapter) SendMessage(ctx context.Context, campfireID string, payload []byte, tags []string, antecedents []string) (string, error) {
	m, err := a.store.GetMembership(campfireID)
	if err != nil {
		return "", fmt.Errorf("querying membership: %w", err)
	}
	if m == nil {
		return "", fmt.Errorf("not a member of campfire %s", campfireID[:min(12, len(campfireID))])
	}

	msg, err := routeMessage(campfireID, string(payload), tags, antecedents, "", a.agentID, a.store, m)
	if err != nil {
		return "", fmt.Errorf("send message: %w", err)
	}

	// Store locally so the sender can read back their own messages without a sync.
	a.store.AddMessage(store.MessageRecordFromMessage(campfireID, msg, store.NowNano())) //nolint:errcheck

	return msg.ID, nil
}

func (a *cliTransportAdapter) SendCampfireKeySigned(ctx context.Context, campfireID string, payload []byte, tags []string, antecedents []string) (string, error) {
	return "", fmt.Errorf("campfire-key signing: %w", ErrNotImplemented)
}

func (a *cliTransportAdapter) ReadMessages(ctx context.Context, campfireID string, tags []string) ([]convention.MessageRecord, error) {
	msgs, err := a.store.ListMessages(campfireID, 0, store.MessageFilter{Tags: tags})
	if err != nil {
		return nil, err
	}
	result := make([]convention.MessageRecord, len(msgs))
	for i, m := range msgs {
		result[i] = convention.MessageRecord{
			ID:     m.ID,
			Sender: m.Sender,
			Tags:   m.Tags,
		}
	}
	return result, nil
}

func (a *cliTransportAdapter) SendFutureAndAwait(ctx context.Context, campfireID string, payload []byte, tags []string, timeout time.Duration) ([]byte, error) {
	return nil, fmt.Errorf("future/await: %w", ErrNotImplemented)
}
