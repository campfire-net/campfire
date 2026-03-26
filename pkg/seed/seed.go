// Package seed implements the bootstrap onion for cf init and cf create.
//
// When a new campfire is created, it should be seeded with convention
// declarations so that agents can use them without any additional setup.
// The seeding process follows a priority-ordered search for a seed beacon,
// which points to a seed campfire containing convention:operation messages.
//
// Priority order (highest to lowest):
//  1. .campfire/seeds/*.beacon — project-local seeds
//  2. ~/.campfire/seeds/*.beacon — user-local seeds
//  3. /usr/share/campfire/seeds/*.beacon — system-level seeds
//  4. https://getcampfire.dev/.well-known/seed.beacon — well-known network fetch
//  5. embedded promote-only fallback — never fails (caller's responsibility)
package seed

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/campfire-net/campfire/pkg/convention"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/message"
)

// WellKnownURL is the URL for the default seed beacon.
// It is a variable (not a constant) so tests can override it with a
// controlled httptest server or a guaranteed-unreachable URL.
var WellKnownURL = "https://getcampfire.dev/.well-known/seed.beacon"

// SeedBeacon describes a seed campfire source.
// It is stored as a CBOR or JSON file in a seeds directory.
type SeedBeacon struct {
	// CampfireID is the 64-char hex ID of the seed campfire (optional;
	// used for informational purposes).
	CampfireID string `cbor:"1,keyasint" json:"campfire_id,omitempty"`

	// Protocol is the transport type: "filesystem" or "http".
	// Defaults to "filesystem" when Dir is set.
	Protocol string `cbor:"2,keyasint" json:"protocol,omitempty"`

	// Dir is the filesystem directory containing the seed campfire.
	// When Protocol is "filesystem", this is the campfire root dir
	// (parent of the messages/ subdirectory).
	Dir string `cbor:"3,keyasint" json:"dir,omitempty"`

	// URL is the HTTP endpoint for the seed campfire (reserved, not yet supported).
	URL string `cbor:"4,keyasint" json:"url,omitempty"`
}

// ConventionMessage is a single convention:operation message from a seed campfire.
type ConventionMessage struct {
	// Payload is the raw JSON declaration payload.
	Payload []byte
	// Tags includes at least "convention:operation".
	Tags []string
}

// FindSeedBeacon searches for a seed beacon in priority order.
// projectDir is the project root directory (or empty if not in a project).
//
// Priority:
//  1. <projectDir>/.campfire/seeds/*.beacon  (project-local, highest priority)
//  2. ~/.campfire/seeds/*.beacon             (user-local)
//  3. /usr/share/campfire/seeds/*.beacon     (system-level)
//  4. https://getcampfire.dev/.well-known/seed.beacon (well-known network)
//
// Returns (nil, nil) when no seed beacon is found — callers use the
// embedded promote-only fallback in that case.
func FindSeedBeacon(projectDir string) (*SeedBeacon, error) {
	// Layer 1: project-local
	if projectDir != "" {
		sb, err := scanSeedsDir(filepath.Join(projectDir, ".campfire", "seeds"))
		if err != nil {
			return nil, err
		}
		if sb != nil {
			return sb, nil
		}
	}

	// Layer 2: user-local
	home, err := os.UserHomeDir()
	if err == nil {
		sb, err := scanSeedsDir(filepath.Join(home, ".campfire", "seeds"))
		if err != nil {
			return nil, err
		}
		if sb != nil {
			return sb, nil
		}
	}

	// Layer 3: system-level
	sb, err := scanSeedsDir("/usr/share/campfire/seeds")
	if err != nil {
		return nil, err
	}
	if sb != nil {
		return sb, nil
	}

	// Layer 4: well-known URL (non-fatal on network failure)
	sb, _ = fetchWellKnownBeacon(WellKnownURL) //nolint:errcheck
	return sb, nil
}

// scanSeedsDir reads the first valid .beacon file from dir.
// Returns (nil, nil) if dir does not exist or contains no valid beacons.
func scanSeedsDir(dir string) (*SeedBeacon, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading seeds dir %s: %w", dir, err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".beacon" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		sb := parseSeedBeacon(data)
		if sb == nil {
			continue
		}
		return sb, nil
	}
	return nil, nil
}

// parseSeedBeacon parses a CBOR or JSON-encoded SeedBeacon.
// Returns nil if the data is invalid or the beacon is incomplete.
func parseSeedBeacon(data []byte) *SeedBeacon {
	var sb SeedBeacon
	if err := cfencoding.Unmarshal(data, &sb); err == nil {
		if sb.Dir != "" || sb.URL != "" {
			return &sb
		}
	}
	// Try JSON fallback
	if err := json.Unmarshal(data, &sb); err == nil {
		if sb.Dir != "" || sb.URL != "" {
			return &sb
		}
	}
	return nil
}

// fetchWellKnownBeacon fetches and parses a seed beacon from the given URL.
// Returns (nil, err) on any failure — callers treat this as non-fatal.
func fetchWellKnownBeacon(url string) (*SeedBeacon, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("seed beacon URL %s returned HTTP %d", url, resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024)) // 64 KiB limit
	if err != nil {
		return nil, fmt.Errorf("reading seed beacon response from %s: %w", url, err)
	}

	sb := parseSeedBeacon(data)
	if sb == nil {
		return nil, fmt.Errorf("invalid or incomplete seed beacon at %s", url)
	}
	return sb, nil
}

// ReadConventionMessages reads convention:operation messages from a seed campfire.
//
// For filesystem protocol: reads CBOR message files from <Dir>/messages/ and
// returns those tagged with "convention:operation".
//
// When sb.CampfireID is set, at least one convention message must be signed by
// the key matching CampfireID. If no message validates, the seed is rejected
// and an error is returned.
//
// Returns (nil, nil) when the messages directory is absent (empty seed campfire).
func ReadConventionMessages(sb *SeedBeacon) ([]ConventionMessage, error) {
	proto := sb.Protocol
	if proto == "" {
		proto = "filesystem"
	}
	switch proto {
	case "http":
		return nil, fmt.Errorf("http seed campfire not yet supported")
	case "filesystem":
		if sb.Dir == "" {
			return nil, fmt.Errorf("seed beacon has no dir for filesystem transport")
		}
		msgs, err := readFilesystemConventionMessages(sb.Dir)
		if err != nil {
			return nil, err
		}
		// When CampfireID is set, verify at least one message is signed by the
		// campfire key. Reject the entire seed if none validates.
		if sb.CampfireID != "" {
			if err := verifySeedBeaconSignatures(sb.CampfireID, sb.Dir); err != nil {
				return nil, err
			}
		}
		return msgs, nil
	default:
		return nil, fmt.Errorf("unknown seed beacon protocol %q", proto)
	}
}

// verifySeedBeaconSignatures checks that at least one message in campfireDir/messages/
// is validly signed by the key whose hex encoding matches campfireID.
// Returns an error if no valid message is found.
func verifySeedBeaconSignatures(campfireID string, campfireDir string) error {
	expectedPub, err := hex.DecodeString(campfireID)
	if err != nil {
		return fmt.Errorf("invalid campfire_id %q: %w", campfireID, err)
	}
	if len(expectedPub) != ed25519.PublicKeySize {
		return fmt.Errorf("campfire_id %q has wrong length (want %d bytes, got %d)", campfireID, ed25519.PublicKeySize, len(expectedPub))
	}

	messagesDir := filepath.Join(campfireDir, "messages")
	entries, err := os.ReadDir(messagesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("seed beacon campfire_id set but messages directory is absent: signature verification failed")
		}
		return fmt.Errorf("reading seed campfire messages at %s: %w", messagesDir, err)
	}

	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".cbor" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(messagesDir, e.Name()))
		if err != nil {
			continue
		}
		var msg message.Message
		if err := cfencoding.Unmarshal(data, &msg); err != nil {
			continue
		}
		if !hasTag(msg.Tags, convention.ConventionOperationTag) {
			continue
		}
		// Check sender matches expected campfire key and signature is valid.
		if len(msg.Sender) == ed25519.PublicKeySize &&
			hex.EncodeToString(msg.Sender) == campfireID &&
			msg.VerifySignature() {
			return nil // at least one valid message found
		}
	}

	return fmt.Errorf("seed beacon signature verification failed: no convention:operation message signed by campfire_id %q", campfireID)
}

// readFilesystemConventionMessages reads convention:operation messages from
// the messages directory of a filesystem campfire at campfireDir.
func readFilesystemConventionMessages(campfireDir string) ([]ConventionMessage, error) {
	messagesDir := filepath.Join(campfireDir, "messages")
	entries, err := os.ReadDir(messagesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading seed campfire messages at %s: %w", messagesDir, err)
	}

	type rawMessage struct {
		Payload []byte   `cbor:"3,keyasint"`
		Tags    []string `cbor:"4,keyasint"`
	}

	var result []ConventionMessage
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".cbor" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(messagesDir, e.Name()))
		if err != nil {
			continue // skip unreadable files
		}
		var raw rawMessage
		if err := cfencoding.Unmarshal(data, &raw); err != nil {
			continue // skip unparseable files
		}
		if !hasTag(raw.Tags, convention.ConventionOperationTag) {
			continue // only convention declarations
		}
		result = append(result, ConventionMessage{
			Payload: raw.Payload,
			Tags:    raw.Tags,
		})
	}
	return result, nil
}

// hasTag reports whether tags contains the given tag string.
func hasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}
