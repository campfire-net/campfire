package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
)

// resolveCampfireID resolves a prefix, full ID, tilde-alias, or cf:// URI to a full 64-char hex campfire ID.
// It searches cf:// URIs first, then local aliases, then the membership table and beacon directories.
// Returns an error if the prefix is ambiguous or matches nothing.
func resolveCampfireID(prefix string, s store.Store) (string, error) {
	// Handle cf:// URIs — may be named, alias, or direct
	if naming.IsCampfireURI(prefix) {
		parsed, err := naming.ParseURI(prefix)
		if err != nil {
			return "", fmt.Errorf("parsing URI: %w", err)
		}
		switch parsed.Kind {
		case naming.URIKindDirect:
			return parsed.CampfireID, nil
		case naming.URIKindAlias:
			aliases := naming.NewAliasStore(CFHome())
			return aliases.Get(parsed.Alias)
		default:
			return resolveNamingURI(prefix, s)
		}
	}

	// Tilde-alias shorthand: ~aliasname (without cf:// scheme)
	if strings.HasPrefix(prefix, "~") {
		aliasName := prefix[1:]
		aliases := naming.NewAliasStore(CFHome())
		return aliases.Get(aliasName)
	}

	// Dot-separated name shorthand: aietf.social.lobby → cf://aietf.social.lobby
	if strings.Contains(prefix, ".") && naming.LooksLikeName(prefix) {
		return resolveNamingURI("cf://"+prefix, s)
	}

	// Local alias: check bare names before prefix search.
	// This lets `cf dontguess post` work without requiring `cf ~dontguess post`.
	aliases := naming.NewAliasStore(CFHome())
	if id, err := aliases.Get(prefix); err == nil {
		return id, nil
	}

	// Exact match: 64 hex chars
	if len(prefix) == 64 {
		return prefix, nil
	}

	var matches []string
	seen := map[string]bool{}

	addMatch := func(id string) {
		if !seen[id] {
			seen[id] = true
			matches = append(matches, id)
		}
	}

	// Search membership table.
	if s != nil {
		memberships, err := s.ListMemberships()
		if err == nil {
			for _, m := range memberships {
				if strings.HasPrefix(m.CampfireID, prefix) {
					addMatch(m.CampfireID)
				}
			}
		}
	}

	// Search default beacon dir.
	searchBeaconDir(BeaconDir(), prefix, addMatch)

	// Search routing:beacon messages in campfire memberships (in-band discovery).
	if s != nil {
		if memberships, err := s.ListMemberships(); err == nil {
			for _, m := range memberships {
				bs, err := beacon.ScanCampfire(s, m.CampfireID)
				if err != nil {
					continue
				}
				for _, b := range bs {
					id := b.CampfireIDHex()
					if strings.HasPrefix(id, prefix) {
						addMatch(id)
					}
				}
			}
		}
	}

	// Zero matches.
	if len(matches) == 0 {
		return "", fmt.Errorf("no campfire found matching prefix %s", prefix)
	}

	// Exactly one match.
	if len(matches) == 1 {
		return matches[0], nil
	}

	// Multiple matches — show first few.
	shown := matches
	suffix := ""
	if len(shown) > 4 {
		shown = shown[:4]
		suffix = fmt.Sprintf(", ... (%d total)", len(matches))
	}
	return "", fmt.Errorf("ambiguous prefix %s, matches: %s%s", prefix, strings.Join(shown, ", "), suffix)
}

// searchBeaconDir scans beacons in dir and calls addMatch for each matching ID.
func searchBeaconDir(dir string, prefix string, addMatch func(string)) {
	beacons, err := beacon.Scan(dir)
	if err != nil {
		return
	}
	for _, b := range beacons {
		id := b.CampfireIDHex()
		if strings.HasPrefix(id, prefix) {
			addMatch(id)
		}
	}
}

// resolveNamingURI resolves a cf:// URI to a campfire ID using the naming protocol.
// Uses NewResolverFromClient (direct-read) instead of the deprecated CLITransport.
func resolveNamingURI(uri string, s store.Store) (string, error) {
	_ = s // store is unused — protocol.Init opens its own store

	client, err := protocol.Init(CFHome())
	if err != nil {
		return "", fmt.Errorf("initializing protocol client for name resolution: %w", err)
	}
	defer client.Close()

	rootID := getRootRegistryID()
	if rootID == "" {
		return "", fmt.Errorf("root registry not configured — set CF_ROOT_REGISTRY or join the root registry campfire")
	}

	resolver := naming.NewResolverFromClient(client, rootID, naming.ResolverClientOptions{
		BeaconDir: BeaconDir(),
	})
	result, err := resolver.ResolveURI(context.Background(), uri)
	if err != nil {
		return "", fmt.Errorf("resolving cf:// URI: %w", err)
	}
	return result.CampfireID, nil
}

// getRootRegistryID returns the root registry campfire ID from env or config.
func getRootRegistryID() string {
	if id := os.Getenv("CF_ROOT_REGISTRY"); id != "" {
		return strings.TrimSpace(id)
	}
	return ""
}
