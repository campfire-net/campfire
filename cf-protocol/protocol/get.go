package protocol

import "fmt"

// Get retrieves a single message by its full ID.
// Returns nil, nil if no message with that ID exists in the local store.
//
// Scope enforcement: if the client has a scope enforcer installed, the operation
// class "read" must be permitted, and the campfire the message belongs to must
// be in the allowlist. The campfire check is applied after fetching the message
// (the ID alone does not carry campfire information). If the message is not found,
// no campfire check is performed (nil, nil is returned).
func (c *Client) Get(id string) (*Message, error) {
	if id == "" {
		return nil, fmt.Errorf("protocol.Client.Get: id is required")
	}

	// Scope enforcement: operation class check (independent of campfire ID).
	if err := c.checkOperation("read"); err != nil {
		return nil, err
	}

	r, err := c.store.GetMessage(id)
	if err != nil {
		return nil, fmt.Errorf("protocol.Client.Get: %w", err)
	}
	if r == nil {
		return nil, nil
	}

	// Scope enforcement: campfire allowlist check using the campfire ID from the record.
	if err := c.checkCampfire(r.CampfireID); err != nil {
		return nil, err
	}

	m := MessageFromRecord(*r)
	return &m, nil
}

// GetByPrefix retrieves a single message by a prefix of its ID.
// Returns nil, nil if no message matches the prefix.
// Returns an error if the prefix is ambiguous (matches more than one message).
//
// Scope enforcement: if the client has a scope enforcer installed, the operation
// class "read" must be permitted, and the campfire the message belongs to must
// be in the allowlist. The campfire check is applied after fetching the message
// (the prefix alone does not carry campfire information). If the prefix matches
// no message, no campfire check is performed (nil, nil is returned).
func (c *Client) GetByPrefix(prefix string) (*Message, error) {
	if prefix == "" {
		return nil, fmt.Errorf("protocol.Client.GetByPrefix: prefix is required")
	}

	// Scope enforcement: operation class check (independent of campfire ID).
	if err := c.checkOperation("read"); err != nil {
		return nil, err
	}

	r, err := c.store.GetMessageByPrefix(prefix)
	if err != nil {
		return nil, fmt.Errorf("protocol.Client.GetByPrefix: %w", err)
	}
	if r == nil {
		return nil, nil
	}

	// Scope enforcement: campfire allowlist check using the campfire ID from the record.
	if err := c.checkCampfire(r.CampfireID); err != nil {
		return nil, err
	}

	m := MessageFromRecord(*r)
	return &m, nil
}
