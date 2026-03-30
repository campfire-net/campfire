package naming

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/campfire-net/campfire/pkg/protocol"
)

// clientTransport implements the naming.Transport interface using a
// protocol.Client. It wraps client.Read() for ListAPI and
// client.Send()+client.Await() for Resolve/ListChildren/Invoke.
type clientTransport struct {
	client *protocol.Client
}

// NewResolverFromClient creates a Resolver backed by a protocol.Client.
// All naming futures are sent via client.Send() and awaited via client.Await().
// ListAPI reads naming:api messages via client.Read().
func NewResolverFromClient(client *protocol.Client, rootID string) *Resolver {
	return NewResolver(&clientTransport{client: client}, rootID)
}

// Resolve sends a naming:resolve future and awaits fulfillment.
func (t *clientTransport) Resolve(ctx context.Context, campfireID string, name string) (*ResolveResponse, error) {
	payload, err := json.Marshal(&ResolveRequest{Name: name})
	if err != nil {
		return nil, err
	}

	msgID, err := t.sendFuture(ctx, campfireID, payload, []string{"naming:resolve"})
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
func (t *clientTransport) ListChildren(ctx context.Context, campfireID string, prefix string) (*ListResponse, error) {
	payload, err := json.Marshal(&ListRequest{Prefix: prefix})
	if err != nil {
		return nil, err
	}

	msgID, err := t.sendFuture(ctx, campfireID, payload, []string{"naming:resolve-list"})
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

// ListAPI reads naming:api messages from the given campfire via client.Read().
func (t *clientTransport) ListAPI(ctx context.Context, campfireID string) ([]APIDeclaration, error) {
	result, err := t.client.Read(protocol.ReadRequest{
		CampfireID: campfireID,
		Tags:       []string{"naming:api"},
	})
	if err != nil {
		return nil, fmt.Errorf("reading api declarations: %w", err)
	}

	var decls []APIDeclaration
	for _, msg := range result.Messages {
		var decl APIDeclaration
		if err := json.Unmarshal(msg.Payload, &decl); err != nil {
			continue // skip malformed declarations
		}
		decls = append(decls, decl)
	}
	return decls, nil
}

// Invoke sends a naming:api-invoke future and awaits fulfillment.
func (t *clientTransport) Invoke(ctx context.Context, campfireID string, req *InvokeRequest) (*InvokeResponse, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	msgID, err := t.sendFuture(ctx, campfireID, payload, []string{"naming:api-invoke"})
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

// sendFuture sends a message with the given tags plus "future" and returns its ID.
func (t *clientTransport) sendFuture(ctx context.Context, campfireID string, payload []byte, tags []string) (string, error) {
	tags = append(tags, "future")
	msg, err := t.client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    payload,
		Tags:       tags,
	})
	if err != nil {
		return "", fmt.Errorf("sending future: %w", err)
	}
	return msg.ID, nil
}

// awaitFulfillment polls via client.Await() for a message fulfilling targetMsgID.
// Honours ctx cancellation by using a deadline derived from ctx.
func (t *clientTransport) awaitFulfillment(ctx context.Context, campfireID, targetMsgID string) ([]byte, error) {
	timeout := time.Duration(0)
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
	}

	rec, err := t.client.Await(protocol.AwaitRequest{
		CampfireID:   campfireID,
		TargetMsgID:  targetMsgID,
		Timeout:      timeout,
		PollInterval: 200 * time.Millisecond,
	})
	if err != nil {
		return nil, err
	}
	return rec.Payload, nil
}

// PublishAPI sends a naming:api tagged message to campfireID with decl as the
// JSON payload. The message is readable by anyone with access to the campfire
// and is used by naming resolvers to discover the campfire's API endpoints.
func PublishAPI(client *protocol.Client, campfireID string, decl APIDeclaration) error {
	payload, err := json.Marshal(&decl)
	if err != nil {
		return fmt.Errorf("marshalling api declaration: %w", err)
	}

	_, err = client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    payload,
		Tags:       []string{"naming:api"},
	})
	if err != nil {
		return fmt.Errorf("publishing api declaration: %w", err)
	}
	return nil
}
