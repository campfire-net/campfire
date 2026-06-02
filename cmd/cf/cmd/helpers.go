package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	cfidentity "github.com/campfire-net/campfire/cf-conventions/cf-identity"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/storage"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
)

// shortIDLen is the number of characters used when displaying a campfire or
// sub-campfire ID in user-facing output.
const shortIDLen = 12

// loadIdentity loads the agent identity from the identity file.
func loadIdentity() (*identity.Identity, error) {
	return identity.Load(IdentityPath())
}

// wrapLocalStore wraps a raw SQLite store in a storage.LocalStorage so that
// membership reads consult the filesystem transport directory as the source of
// truth (rehydrating a cold SQLite cache) instead of treating a cache miss as
// "not a member". When pubkeyHex is non-empty, the fs-rehydrate fallback is
// enabled (it needs to know which on-disk member record is "me"); otherwise the
// wrapper degrades to pure SQLite passthrough. The transport base dir is left
// to fs.DefaultBaseDir() resolution (CF_TRANSPORT_DIR / CF_HOME / config /
// ~/.campfire), matching how every other filesystem-transport read resolves it.
func wrapLocalStore(s store.Store, pubkeyHex string) store.Store {
	if pubkeyHex == "" {
		return storage.NewLocalStorage(s)
	}
	return storage.NewLocalStorage(s, storage.WithSelfPubkeyHex(pubkeyHex))
}

// openStore opens the campfire store at the default path, wrapped so membership
// reads fall back to the filesystem transport on a cold cache. The identity is
// loaded best-effort to enable the fs-rehydrate fallback; admin/read commands
// that have no identity still work (passthrough).
// The caller is responsible for calling s.Close() (typically via defer).
func openStore() (store.Store, error) {
	s, err := store.Open(store.StorePath(CFHome()))
	if err != nil {
		return nil, fmt.Errorf("opening store: %w", err)
	}
	pubkeyHex := ""
	if agentID, idErr := identity.Load(IdentityPath()); idErr == nil {
		pubkeyHex = agentID.PublicKeyHex()
	}
	return wrapLocalStore(s, pubkeyHex), nil
}

// requireAgentAndStore loads the agent identity and opens the campfire store,
// wrapped so membership reads fall back to the filesystem transport on a cold
// cache (the identity supplies the self-pubkey the fs-rehydrate fallback needs).
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
	return agentID, wrapLocalStore(s, agentID.PublicKeyHex()), nil
}

// printAutoJoinWarnings prints auto-join warnings from an InitResult to stderr.
// Auto-join warnings are always printed (not gated on CF_VERBOSE) because they
// are actionable: they tell the user which campfire failed and how to join manually.
func printAutoJoinWarnings(result *protocol.InitResult) {
	if result == nil {
		return
	}
	for _, w := range result.Warnings {
		if strings.Contains(w, "auto_join") {
			fmt.Fprintf(os.Stderr, "note: %s\n", w)
		}
	}
}

// populateProfileCacheFromStore loads identity:profile messages for a campfire from
// the local store and populates sessionProfileCache. Best-effort: errors are ignored.
func populateProfileCacheFromStore(s store.Store, campfireID string) {
	records, err := s.ListMessages(campfireID, 0, store.MessageFilter{
		Tags: []string{"identity:profile"},
	})
	if err != nil {
		return
	}
	msgs := make([]protocol.Message, len(records))
	for i, r := range records {
		msgs[i] = protocol.MessageFromRecord(r)
	}
	sessionProfileCache.LoadFromMessages(msgs)
}

// maybeSendProfileMessage auto-sends an identity:profile message to campfireID
// if the agent has a display name stored in profile.json. Best-effort: errors
// are silently ignored so join/create continue regardless.
// Display names are SELF-DECLARED and UNVERIFIED.
func maybeSendProfileMessage(campfireID string, agentID *identity.Identity, s store.Store) {
	profile := protocol.LoadProfile(CFHome())
	if profile.DisplayName == "" {
		return
	}
	payload, err := json.Marshal(map[string]string{"display_name": profile.DisplayName})
	if err != nil {
		return
	}
	client := protocol.New(s, agentID)
	client.Send(protocol.SendRequest{ //nolint:errcheck
		CampfireID: campfireID,
		Payload:    payload,
		Tags:       []string{cfidentity.IdentityProfileTag},
	})
}
