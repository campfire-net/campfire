package naming

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
)

// Deprecated: Use NewResolverFromClient instead. CLITransport uses futures which
// require a server process. NewResolverFromClient uses direct-read resolution.
//
// CLITransport implements the naming.Transport interface using the local store
// and identity for sending futures and polling for fulfillment.
type CLITransport struct {
	Identity *identity.Identity
	Store    store.Store
	// SyncFunc is called before polling to sync messages from remote transport.
	// If nil, only local messages are checked.
	SyncFunc func(campfireID string)
}

// pollInterval is the time between polls for fulfillment.
const pollInterval = 200 * time.Millisecond

// Resolve sends a naming:resolve future and awaits fulfillment.
func (t *CLITransport) Resolve(ctx context.Context, campfireID, name string) (*ResolveResponse, error) {
	payload, err := json.Marshal(&ResolveRequest{Name: name})
	if err != nil {
		return nil, err
	}

	msgID, err := t.sendFuture(campfireID, payload, []string{"naming:resolve"})
	if err != nil {
		return nil, fmt.Errorf("sending resolve future: %w", err)
	}

	fulfillment, err := t.awaitFulfillment(ctx, campfireID, msgID)
	if err != nil {
		return nil, fmt.Errorf("awaiting resolve fulfillment: %w", err)
	}

	var resp ResolveResponse
	if err := json.Unmarshal(fulfillment, &resp); err != nil {
		return nil, fmt.Errorf("parsing resolve response: %w", err)
	}
	return &resp, nil
}

// ListChildren sends a naming:resolve-list future and awaits fulfillment.
func (t *CLITransport) ListChildren(ctx context.Context, campfireID, prefix string) (*ListResponse, error) {
	payload, err := json.Marshal(&ListRequest{Prefix: prefix})
	if err != nil {
		return nil, err
	}

	msgID, err := t.sendFuture(campfireID, payload, []string{"naming:resolve-list"})
	if err != nil {
		return nil, fmt.Errorf("sending list future: %w", err)
	}

	fulfillment, err := t.awaitFulfillment(ctx, campfireID, msgID)
	if err != nil {
		return nil, fmt.Errorf("awaiting list fulfillment: %w", err)
	}

	var resp ListResponse
	if err := json.Unmarshal(fulfillment, &resp); err != nil {
		return nil, fmt.Errorf("parsing list response: %w", err)
	}
	return &resp, nil
}

// ListAPI reads naming:api messages from the given campfire.
func (t *CLITransport) ListAPI(ctx context.Context, campfireID string) ([]APIDeclaration, error) {
	// Sync first if available
	if t.SyncFunc != nil {
		t.SyncFunc(campfireID)
	}

	msgs, err := t.Store.ListMessages(campfireID, 0, store.MessageFilter{
		Tags: []string{"naming:api"},
	})
	if err != nil {
		return nil, fmt.Errorf("reading api declarations: %w", err)
	}

	var decls []APIDeclaration
	for _, msg := range msgs {
		var decl APIDeclaration
		if err := json.Unmarshal(msg.Payload, &decl); err != nil {
			continue // skip malformed declarations
		}
		decls = append(decls, decl)
	}
	return decls, nil
}

// Invoke sends a naming:api-invoke future and awaits fulfillment.
func (t *CLITransport) Invoke(ctx context.Context, campfireID string, req *InvokeRequest) (*InvokeResponse, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	msgID, err := t.sendFuture(campfireID, payload, []string{"naming:api-invoke"})
	if err != nil {
		return nil, fmt.Errorf("sending invoke future: %w", err)
	}

	fulfillment, err := t.awaitFulfillment(ctx, campfireID, msgID)
	if err != nil {
		return nil, fmt.Errorf("awaiting invoke fulfillment: %w", err)
	}

	var resp InvokeResponse
	if err := json.Unmarshal(fulfillment, &resp); err != nil {
		return nil, fmt.Errorf("parsing invoke response: %w", err)
	}
	return &resp, nil
}

// sendFuture sends a message with the given tags plus "future" tag and returns its ID.
func (t *CLITransport) sendFuture(campfireID string, payload []byte, tags []string) (string, error) {
	tags = append(tags, "future")

	msg, err := message.NewMessage(
		t.Identity.PrivateKey, t.Identity.PublicKey,
		payload, tags, nil,
	)
	if err != nil {
		return "", fmt.Errorf("creating message: %w", err)
	}

	rec := store.MessageRecord{
		ID:          msg.ID,
		CampfireID:  campfireID,
		Sender:      fmt.Sprintf("%x", msg.Sender),
		Payload:     msg.Payload,
		Tags:        msg.Tags,
		Antecedents: msg.Antecedents,
		Timestamp:   msg.Timestamp,
		Signature:   msg.Signature,
	}
	if _, err := t.Store.AddMessage(rec); err != nil {
		return "", fmt.Errorf("writing message: %w", err)
	}

	return msg.ID, nil
}

// awaitFulfillment polls for a message with "fulfills" tag whose antecedents
// contain the target message ID.
func (t *CLITransport) awaitFulfillment(ctx context.Context, campfireID, targetMsgID string) ([]byte, error) {
	for {
		// Sync if available
		if t.SyncFunc != nil {
			t.SyncFunc(campfireID)
		}

		msgs, err := t.Store.ListMessages(campfireID, 0, store.MessageFilter{
			Tags: []string{"fulfills"},
		})
		if err != nil {
			return nil, fmt.Errorf("querying messages: %w", err)
		}

		for _, msg := range msgs {
			for _, ant := range msg.Antecedents {
				if ant == targetMsgID {
					return msg.Payload, nil
				}
			}
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}
