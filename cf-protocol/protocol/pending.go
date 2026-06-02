package protocol

import (
	"fmt"

	cfencoding "github.com/campfire-net/campfire/cf-protocol/internal/encoding"
	"github.com/campfire-net/campfire/cf-protocol/internal/message"
	"github.com/campfire-net/campfire/cf-protocol/internal/store"
)

// BuildPending builds and signs a message offline and buffers it locally,
// returning the message's stable ID. It performs NO transport I/O — the message
// is committed to the local pending buffer only, so a consumer that is offline
// (or batching) can obtain a durable, forgery-proof ID now and deliver later via
// FlushPending.
//
// The returned ID is the canonical message ID: it is minted inside the signed
// envelope (the signature commits to it), so it is identical to the ID the
// message carries once delivered — the caller never chooses it. Re-building an
// identical request is NOT deduplicated (each call mints a fresh ID); idempotency
// is on the flush side, keyed by the stable ID.
//
// Requires a store that implements offline buffering (the local SQLite store);
// returns an error for stores that do not (e.g. the always-online hosted store).
func (c *Client) BuildPending(req SendRequest) (string, error) {
	if c.identity == nil {
		return "", fmt.Errorf("identity required to build a message")
	}

	resolvedID, _, err := resolveInput(req.CampfireID, c.opts.namingResolver)
	if err != nil {
		return "", fmt.Errorf("resolving campfire address: %w", err)
	}
	req.CampfireID = resolvedID

	if err := c.checkCampfire(req.CampfireID); err != nil {
		return "", err
	}
	if err := c.checkOperation("write"); err != nil {
		return "", err
	}

	m, err := c.store.GetMembership(req.CampfireID)
	if err != nil {
		return "", fmt.Errorf("querying membership: %w", err)
	}
	if m == nil {
		return "", &ErrNotMember{CampfireID: req.CampfireID}
	}
	if err := checkRoleCanSend(m.Role, req.Tags); err != nil {
		return "", err
	}

	ps, ok := c.store.(store.PendingMessageStore)
	if !ok {
		return "", fmt.Errorf("offline pending send is not supported by this store")
	}

	// Build and sign the envelope (mints the stable ID). The provenance hop is
	// deliberately deferred to FlushPending, where it is signed from current
	// campfire membership state.
	signer := c.identity.NewSigner()
	msg, err := message.NewMessage(signer, req.Payload, req.Tags, req.Antecedents)
	if err != nil {
		return "", fmt.Errorf("creating message: %w", err)
	}
	msg.Instance = req.Instance

	blob, err := cfencoding.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("encoding pending message: %w", err)
	}

	if err := ps.AddPendingMessage(store.PendingMessage{
		ID:           msg.ID,
		CampfireID:   req.CampfireID,
		Blob:         blob,
		StateDir:     req.StateDir,
		RoleOverride: req.RoleOverride,
		BuiltAt:      store.NowNano(),
	}); err != nil {
		return "", fmt.Errorf("buffering pending message: %w", err)
	}

	return msg.ID, nil
}

// FlushPending delivers all buffered messages for campfireID to the transport,
// in build order, and removes each from the buffer once delivered. It returns
// the number of messages delivered.
//
// Each message keeps the stable ID assigned at BuildPending; FlushPending adds
// the provenance hop from current campfire state and delivers via the same path
// as Send. Delivery is idempotent on the stable ID: a re-flush after a crash
// between transport write and buffer removal re-writes the same message (the
// transport and local store both dedupe by ID) and then clears the buffer entry,
// so no duplicate is created.
//
// On the first delivery error, FlushPending stops and returns the count
// delivered so far plus the error; the remaining buffered messages are left in
// place for a later retry.
func (c *Client) FlushPending(campfireID string) (int, error) {
	if c.identity == nil {
		return 0, fmt.Errorf("identity required to flush messages")
	}

	resolvedID, _, err := resolveInput(campfireID, c.opts.namingResolver)
	if err != nil {
		return 0, fmt.Errorf("resolving campfire address: %w", err)
	}
	campfireID = resolvedID

	ps, ok := c.store.(store.PendingMessageStore)
	if !ok {
		return 0, fmt.Errorf("offline pending send is not supported by this store")
	}

	pending, err := ps.ListPendingMessages(campfireID)
	if err != nil {
		return 0, fmt.Errorf("listing pending messages: %w", err)
	}
	if len(pending) == 0 {
		return 0, nil
	}

	m, err := c.store.GetMembership(campfireID)
	if err != nil {
		return 0, fmt.Errorf("querying membership: %w", err)
	}
	if m == nil {
		return 0, &ErrNotMember{CampfireID: campfireID}
	}

	flushed := 0
	for _, p := range pending {
		var msg message.Message
		if err := cfencoding.Unmarshal(p.Blob, &msg); err != nil {
			return flushed, fmt.Errorf("decoding pending message %s: %w", p.ID, err)
		}
		req := SendRequest{
			CampfireID:   campfireID,
			StateDir:     p.StateDir,
			RoleOverride: p.RoleOverride,
		}
		if _, err := c.deliver(req, m, &msg); err != nil {
			return flushed, fmt.Errorf("delivering pending message %s: %w", p.ID, err)
		}
		if err := ps.DeletePendingMessage(p.ID); err != nil {
			return flushed, fmt.Errorf("removing flushed pending message %s: %w", p.ID, err)
		}
		flushed++
	}
	return flushed, nil
}
