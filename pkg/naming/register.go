package naming

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/protocol"
)

// RegisterOptions holds optional parameters for Register.
type RegisterOptions struct {
	// TTL is the time-to-live in seconds. Defaults to DefaultTTL (3600).
	// Capped at MaxTTL (86400).
	TTL int
}

// Registration represents an active name registration in a campfire.
type Registration struct {
	Name       string
	CampfireID string
	TTL        int
	MessageID  string
	Timestamp  int64
}

// registrationPayload is the JSON payload for a naming registration message.
type registrationPayload struct {
	Name       string `json:"name"`
	CampfireID string `json:"campfire_id"`
	TTL        int    `json:"ttl,omitempty"`
	Unregister bool   `json:"unregister,omitempty"`
}

// nameTag returns the tag used for a specific name registration.
func nameTag(name string) string {
	return "naming:name:" + name
}

// Register posts a name registration to the campfire. The campfire IS the
// nameserver — resolution is direct-read, not RPC.
func Register(ctx context.Context, client *protocol.Client, campfireID, name, targetCampfireID string, opts *RegisterOptions) (*message.Message, error) {
	_ = ctx // reserved for future use (e.g. deadline propagation)

	ttl := DefaultTTL
	if opts != nil && opts.TTL > 0 {
		ttl = opts.TTL
	}
	if ttl > MaxTTL {
		ttl = MaxTTL
	}

	payload, err := json.Marshal(&registrationPayload{
		Name:       name,
		CampfireID: targetCampfireID,
		TTL:        ttl,
	})
	if err != nil {
		return nil, fmt.Errorf("marshalling registration: %w", err)
	}

	msg, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    payload,
		Tags:       []string{nameTag(name)},
	})
	if err != nil {
		return nil, fmt.Errorf("sending registration: %w", err)
	}

	return msg, nil
}

// Resolve reads the campfire to find the most recent registration for a name.
// This is direct-read — no futures, no server process. The campfire IS the nameserver.
func Resolve(ctx context.Context, client *protocol.Client, campfireID, name string) (*ResolveResponse, error) {
	_ = ctx

	result, err := client.Read(protocol.ReadRequest{
		CampfireID: campfireID,
		Tags:       []string{nameTag(name)},
	})
	if err != nil {
		return nil, fmt.Errorf("reading registrations: %w", err)
	}

	if len(result.Messages) == 0 {
		return nil, fmt.Errorf("name %q not found", name)
	}

	// Walk from newest to oldest to find the most recent non-unregister message.
	for i := len(result.Messages) - 1; i >= 0; i-- {
		msg := result.Messages[i]
		var p registrationPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			continue // skip malformed
		}
		if p.Unregister {
			// Most recent action is an unregistration — name is not found.
			return nil, fmt.Errorf("name %q not found", name)
		}
		return &ResolveResponse{
			Name:              p.Name,
			CampfireID:        p.CampfireID,
			RegistrationMsgID: msg.ID,
			TTL:               p.TTL,
		}, nil
	}

	return nil, fmt.Errorf("name %q not found", name)
}

// Unregister posts an unregistration message for a name. After this,
// Resolve will not return the name.
func Unregister(ctx context.Context, client *protocol.Client, campfireID, name string) error {
	_ = ctx

	payload, err := json.Marshal(&registrationPayload{
		Name:       name,
		Unregister: true,
	})
	if err != nil {
		return fmt.Errorf("marshalling unregistration: %w", err)
	}

	_, err = client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    payload,
		Tags:       []string{nameTag(name), "naming:unregister"},
	})
	if err != nil {
		return fmt.Errorf("sending unregistration: %w", err)
	}

	return nil
}

// List returns all active registrations in the campfire. Unregistered names
// are excluded — only the latest state of each name is considered.
func List(ctx context.Context, client *protocol.Client, campfireID string) ([]Registration, error) {
	_ = ctx

	result, err := client.Read(protocol.ReadRequest{
		CampfireID:  campfireID,
		TagPrefixes: []string{"naming:name:"},
	})
	if err != nil {
		return nil, fmt.Errorf("reading registrations: %w", err)
	}

	// Build the latest state per name by scanning messages in order.
	// Messages are ordered by timestamp, so later entries override earlier ones.
	type nameState struct {
		reg          Registration
		unregistered bool
		timestamp    int64
	}
	latest := make(map[string]*nameState)

	for _, msg := range result.Messages {
		var p registrationPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			continue
		}
		if p.Name == "" {
			continue
		}

		ts := msg.Timestamp
		existing, ok := latest[p.Name]
		if ok && existing.timestamp > ts {
			continue // already have a newer record
		}

		if p.Unregister {
			latest[p.Name] = &nameState{unregistered: true, timestamp: ts}
		} else {
			latest[p.Name] = &nameState{
				reg: Registration{
					Name:       p.Name,
					CampfireID: p.CampfireID,
					TTL:        p.TTL,
					MessageID:  msg.ID,
					Timestamp:  ts,
				},
				timestamp: ts,
			}
		}
	}

	// Collect active registrations.
	var regs []Registration
	for _, state := range latest {
		if !state.unregistered {
			regs = append(regs, state.reg)
		}
	}

	// Sort by timestamp for deterministic output.
	sortRegistrations(regs)

	return regs, nil
}

// sortRegistrations sorts registrations by timestamp (ascending).
func sortRegistrations(regs []Registration) {
	for i := 1; i < len(regs); i++ {
		for j := i; j > 0 && regs[j].Timestamp < regs[j-1].Timestamp; j-- {
			regs[j], regs[j-1] = regs[j-1], regs[j]
		}
	}
}

