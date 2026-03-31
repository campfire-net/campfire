package cmd

import (
	"context"
	"encoding/hex"
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

// isValidCampfireID reports whether id is a valid campfire ID: exactly 64
// hex characters encoding a 32-byte Ed25519 public key.
func isValidCampfireID(id string) bool {
	if len(id) != 64 {
		return false
	}
	b, err := hex.DecodeString(id)
	return err == nil && len(b) == 32
}

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
			return resolveNamingURI(prefix)
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
		return resolveNamingURI("cf://" + prefix)
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

// autoJoinIfOpen auto-joins campfireID if the agent is not yet a member and the
// campfire advertises an open join protocol via a local beacon. It is best-effort:
// errors are silently swallowed so that name resolution always succeeds even when
// the join fails (e.g. transport not reachable, invite-only campfire).
//
// The store s may be nil, in which case the membership check is skipped and the
// join is attempted unconditionally (rare: s is nil only in fallback paths that
// do not hold an open store handle).
func autoJoinIfOpen(campfireID string, s store.Store) error {
	// Already a member? Skip.
	if s != nil {
		if m, _ := s.GetMembership(campfireID); m != nil {
			return nil
		}
	}

	// Load identity — required to write a member record.
	agentID, err := loadIdentity()
	if err != nil {
		return fmt.Errorf("loading identity for auto-join: %w", err)
	}

	// Delegate to the filesystem auto-join helper (join.go).
	// autoJoinRootCampfire checks join protocol, reads transport state, and
	// only joins open campfires — invite-only are silently skipped.
	if s != nil {
		return autoJoinRootCampfire(campfireID, agentID, s)
	}

	// s is nil: open a fresh store handle for the join, then close it.
	freshStore, err := openStore()
	if err != nil {
		return fmt.Errorf("opening store for auto-join: %w", err)
	}
	defer freshStore.Close()
	return autoJoinRootCampfire(campfireID, agentID, freshStore)
}

// resolveNameInRootWithAutoJoin resolves a name in rootID and, on success,
// attempts to auto-join the resolved campfire if it is open-protocol and the
// agent is not yet a member. The join is best-effort — resolution succeeds
// even if the join fails.
func resolveNameInRootWithAutoJoin(rootID, name string, s store.Store) (string, error) {
	id, err := resolveNameInRoot(rootID, name)
	if err != nil {
		return "", err
	}
	// Best-effort auto-join: ignore errors so resolution still succeeds.
	_ = autoJoinIfOpen(id, s)
	return id, nil
}

// resolveByName attempts to resolve a bare name via naming registries.
// If a join policy is configured, it uses consult-based root selection.
// Otherwise falls back to: project root (.campfire/root walk-up), then CF_ROOT_REGISTRY env var.
func resolveByName(name string, s store.Store) (string, error) {
	jp, err := naming.LoadJoinPolicy(CFHome())
	if err != nil {
		return "", fmt.Errorf("join policy: %w", err)
	}

	if jp == nil {
		// No policy configured — use legacy walk-up + CF_ROOT_REGISTRY fallback.
		return resolveByNameFallback(name, s)
	}

	// Policy configured — use consult-based root selection.
	if jp.ConsultCampfire == naming.FSWalkSentinel {
		// fs-walk: discover roots via filesystem walk-up from cwd.
		cwd, err := os.Getwd()
		if err != nil {
			return resolveByNameFallback(name, s)
		}
		roots := naming.FSWalkRoots(cwd, jp.JoinRoot)
		for _, rootID := range roots {
			if id, err := resolveNameInRootWithAutoJoin(rootID, name, s); err == nil {
				return id, nil
			}
		}
		return "", fmt.Errorf("name %q not found in any reachable root", name)
	}

	// Real consult campfire — send a join-root-query future and await the response.
	roots, err := consultRootsForName(name, jp)
	if err != nil {
		// Consult failed — fall back to legacy behavior.
		return resolveByNameFallback(name, s)
	}
	for _, rootID := range roots {
		if id, err := resolveNameInRootWithAutoJoin(rootID, name, s); err == nil {
			return id, nil
		}
	}
	return "", fmt.Errorf("name %q not found in any reachable root", name)
}

// resolveByNameFallback is the legacy resolution path: project root walk-up then CF_ROOT_REGISTRY.
func resolveByNameFallback(name string, s store.Store) (string, error) {
	// Try project root first — walk up from pwd.
	if rootID, _, ok := ProjectRoot(); ok {
		if id, err := resolveNameInRootWithAutoJoin(rootID, name, s); err == nil {
			return id, nil
		}
	}

	// Fall back to configured root registry.
	if rootID := getRootRegistryID(); rootID != "" {
		if id, err := resolveNameInRootWithAutoJoin(rootID, name, s); err == nil {
			return id, nil
		}
	}

	return "", fmt.Errorf("name %q not found in any reachable root", name)
}

// consultTimeout returns the timeout for consultRootsForName.
// It reads CF_CONSULT_TIMEOUT (a Go duration string, e.g. "30s", "2m") and falls
// back to 10s for backwards compatibility.
func consultTimeout() time.Duration {
	if raw := os.Getenv("CF_CONSULT_TIMEOUT"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return 10 * time.Second
}

// consultRootsForName sends a join-root-query future to the consult campfire
// and waits for the agent to respond with a list of root IDs. The timeout is
// controlled by the CF_CONSULT_TIMEOUT environment variable (default: 10s).
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
		Timeout:     consultTimeout(),
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
// It validates rootID before use to prevent malformed or malicious IDs
// (e.g. from untrusted consult agent responses) from reaching the protocol layer.
func resolveNameInRoot(rootID, name string) (string, error) {
	if !isValidCampfireID(rootID) {
		return "", fmt.Errorf("invalid root campfire ID %q: must be 64 hex characters", rootID)
	}

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
// protocol.Init opens its own store internally, so no store parameter is needed.
func resolveNamingURI(uri string) (string, error) {
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
