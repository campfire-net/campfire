package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

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

	// Zero matches from local sources — try naming resolution as a last resort.
	// This enables `cf galtrader help` to resolve "galtrader" via the naming
	// registry without requiring an alias or cf:// URI prefix.
	if len(matches) == 0 {
		if id, err := resolveByName(prefix, s); err == nil {
			return id, nil
		}
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

// resolveByName attempts to resolve a bare name via naming registries.
// If a join policy is configured, it uses consult-based root selection.
// Otherwise falls back to: project root (.campfire/root walk-up), then CF_ROOT_REGISTRY env var.
func resolveByName(name string, s store.Store) (string, error) {
	jp, err := naming.LoadJoinPolicy(CFHome())
	if err != nil {
		// Policy file exists but is malformed — fall back to legacy behavior.
		jp = nil
	}

	if jp == nil {
		// No policy configured — use legacy walk-up + CF_ROOT_REGISTRY fallback.
		return resolveByNameFallback(name)
	}

	// Policy configured — use consult-based root selection.
	if jp.ConsultCampfire == naming.FSWalkSentinel {
		// fs-walk: discover roots via filesystem walk-up from cwd.
		cwd, err := os.Getwd()
		if err != nil {
			return resolveByNameFallback(name)
		}
		roots := naming.FSWalkRoots(cwd, jp.JoinRoot)
		for _, rootID := range roots {
			if id, err := resolveNameInRoot(rootID, name); err == nil {
				return id, nil
			}
		}
		return "", fmt.Errorf("name %q not found in any reachable root", name)
	}

	// Real consult campfire — send a join-root-query future and await the response.
	roots, err := consultRootsForName(name, jp)
	if err != nil {
		// Consult failed — fall back to legacy behavior.
		return resolveByNameFallback(name)
	}
	for _, rootID := range roots {
		if id, err := resolveNameInRoot(rootID, name); err == nil {
			return id, nil
		}
	}
	return "", fmt.Errorf("name %q not found in any reachable root", name)
}

// resolveByNameFallback is the legacy resolution path: project root walk-up then CF_ROOT_REGISTRY.
func resolveByNameFallback(name string) (string, error) {
	// Try project root first — walk up from pwd.
	if rootID, _, ok := ProjectRoot(); ok {
		if id, err := resolveNameInRoot(rootID, name); err == nil {
			return id, nil
		}
	}

	// Fall back to configured root registry.
	if rootID := getRootRegistryID(); rootID != "" {
		if id, err := resolveNameInRoot(rootID, name); err == nil {
			return id, nil
		}
	}

	return "", fmt.Errorf("name %q not found in any reachable root", name)
}

// consultRootsForName sends a join-root-query future to the consult campfire
// and waits up to 10 seconds for the agent to respond with a list of root IDs.
func consultRootsForName(name string, jp *naming.JoinPolicy) ([]string, error) {
	client, err := protocol.Init(CFHome())
	if err != nil {
		return nil, fmt.Errorf("init protocol client for consult: %w", err)
	}
	defer client.Close()

	type queryPayload struct {
		Name     string `json:"name"`
		JoinRoot string `json:"join_root"`
	}
	queryJSON, err := json.Marshal(queryPayload{Name: name, JoinRoot: jp.JoinRoot})
	if err != nil {
		return nil, fmt.Errorf("marshaling consult query: %w", err)
	}

	msg, err := client.Send(protocol.SendRequest{
		CampfireID: jp.ConsultCampfire,
		Payload:    queryJSON,
		Tags:       []string{"join-root-selection:query", "future"},
	})
	if err != nil {
		return nil, fmt.Errorf("sending consult query: %w", err)
	}

	resp, err := client.Await(protocol.AwaitRequest{
		CampfireID:  jp.ConsultCampfire,
		TargetMsgID: msg.ID,
		Timeout:     10 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("awaiting consult response: %w", err)
	}

	type responsePayload struct {
		Roots []string `json:"roots"`
	}
	var rp responsePayload
	if err := json.Unmarshal(resp.Payload, &rp); err != nil {
		return nil, fmt.Errorf("parsing consult response: %w", err)
	}
	return rp.Roots, nil
}

// resolveNameInRoot tries to resolve a name in the given root campfire.
func resolveNameInRoot(rootID, name string) (string, error) {
	client, err := protocol.Init(CFHome())
	if err != nil {
		return "", err
	}
	defer client.Close()

	resp, err := naming.Resolve(context.Background(), client, rootID, name)
	if err != nil {
		return "", err
	}
	return resp.CampfireID, nil
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
