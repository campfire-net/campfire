package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
)

// ErrNotImplemented is returned by stub transport methods that are not yet implemented.
var ErrNotImplemented = errors.New("not yet implemented")

// cliTransportAdapter implements convention.ExecutorTransport using the local store.
type cliTransportAdapter struct {
	agentID *identity.Identity
	store   store.Store
}

func (a *cliTransportAdapter) SendMessage(ctx context.Context, campfireID string, payload []byte, tags []string, antecedents []string) (string, error) {
	msg, err := message.NewMessage(a.agentID.PrivateKey, a.agentID.PublicKey, payload, tags, antecedents)
	if err != nil {
		return "", fmt.Errorf("creating message: %w", err)
	}
	rec := store.MessageRecord{
		ID:          msg.ID,
		CampfireID:  campfireID,
		Sender:      msg.SenderHex(),
		Payload:     msg.Payload,
		Tags:        msg.Tags,
		Antecedents: msg.Antecedents,
		Timestamp:   msg.Timestamp,
		Signature:   msg.Signature,
	}
	if _, err := a.store.AddMessage(rec); err != nil {
		return "", fmt.Errorf("writing message: %w", err)
	}
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
