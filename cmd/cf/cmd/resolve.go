package cmd

import (
	"fmt"
	"strings"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/store"
)

// resolveCampfireID resolves a prefix (or full ID) to a full 64-char hex campfire ID.
// It searches the membership table and beacon directories.
// Returns an error if the prefix is ambiguous or matches nothing.
func resolveCampfireID(prefix string, s *store.Store) (string, error) {
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
