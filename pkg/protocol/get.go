package protocol

import "fmt"

// Get retrieves a single message by its full ID.
// Returns nil, nil if no message with that ID exists in the local store.
func (c *Client) Get(id string) (*Message, error) {
	if id == "" {
		return nil, fmt.Errorf("protocol.Client.Get: id is required")
	}
	r, err := c.store.GetMessage(id)
	if err != nil {
		return nil, fmt.Errorf("protocol.Client.Get: %w", err)
	}
	if r == nil {
		return nil, nil
	}
	m := MessageFromRecord(*r)
	return &m, nil
}

// GetByPrefix retrieves a single message by a prefix of its ID.
// Returns nil, nil if no message matches the prefix.
// Returns an error if the prefix is ambiguous (matches more than one message).
func (c *Client) GetByPrefix(prefix string) (*Message, error) {
	if prefix == "" {
		return nil, fmt.Errorf("protocol.Client.GetByPrefix: prefix is required")
	}
	r, err := c.store.GetMessageByPrefix(prefix)
	if err != nil {
		return nil, fmt.Errorf("protocol.Client.GetByPrefix: %w", err)
	}
	if r == nil {
		return nil, nil
	}
	m := MessageFromRecord(*r)
	return &m, nil
}
