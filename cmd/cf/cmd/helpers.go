package cmd

import (
	"fmt"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
)

// loadIdentity loads the agent identity from the identity file.
func loadIdentity() (*identity.Identity, error) {
	return identity.Load(IdentityPath())
}

// openStore opens the campfire store at the default path.
// The caller is responsible for calling s.Close() (typically via defer).
func openStore() (store.Store, error) {
	s, err := store.Open(store.StorePath(CFHome()))
	if err != nil {
		return nil, fmt.Errorf("opening store: %w", err)
	}
	return s, nil
}

// requireAgentAndStore loads the agent identity and opens the campfire store.
// The caller is responsible for calling s.Close() (typically via defer).
func requireAgentAndStore() (*identity.Identity, store.Store, error) {
	agentID, err := identity.Load(IdentityPath())
	if err != nil {
		return nil, nil, fmt.Errorf("loading identity: %w", err)
	}
	s, err := store.Open(store.StorePath(CFHome()))
	if err != nil {
		return nil, nil, fmt.Errorf("opening store: %w", err)
	}
	return agentID, s, nil
}
