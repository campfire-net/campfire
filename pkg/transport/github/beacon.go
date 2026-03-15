package github

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

// Beacon represents a campfire beacon stored in .campfire/beacons/{campfire_id}.json
// in the coordination repository.
type Beacon struct {
	CampfireID            string          `json:"campfire_id"`
	JoinProtocol          string          `json:"join_protocol"`
	ReceptionRequirements []string        `json:"reception_requirements"`
	Transport             BeaconTransport `json:"transport"`
	Description           string          `json:"description"`
	// Signature is the hex-encoded Ed25519 signature over the canonical beacon JSON
	// (all fields except signature itself, marshaled in deterministic field order).
	Signature string `json:"signature"`
}

// BeaconTransport holds the transport-layer location of the campfire.
type BeaconTransport struct {
	Protocol string              `json:"protocol"`
	Config   BeaconTransportConfig `json:"config"`
}

// BeaconTransportConfig holds GitHub-transport-specific connection details.
type BeaconTransportConfig struct {
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issue_number"`
	IssueURL    string `json:"issue_url,omitempty"`
}

// beaconSignPayload is the canonical representation of a beacon used for signing.
// It matches Beacon but omits the Signature field to prevent circular dependency.
type beaconSignPayload struct {
	CampfireID            string          `json:"campfire_id"`
	JoinProtocol          string          `json:"join_protocol"`
	ReceptionRequirements []string        `json:"reception_requirements"`
	Transport             BeaconTransport `json:"transport"`
	Description           string          `json:"description"`
}

// signPayloadBytes returns the canonical JSON bytes of a beacon for signing/verification.
// The signature field is excluded so that sign and verify operate on the same bytes.
func signPayloadBytes(b Beacon) ([]byte, error) {
	payload := beaconSignPayload{
		CampfireID:            b.CampfireID,
		JoinProtocol:          b.JoinProtocol,
		ReceptionRequirements: b.ReceptionRequirements,
		Transport:             b.Transport,
		Description:           b.Description,
	}
	return json.Marshal(payload)
}

// SignBeacon signs the beacon with the given Ed25519 private key and returns the
// hex-encoded signature. The signature covers all beacon fields except the signature itself.
func SignBeacon(b Beacon, priv ed25519.PrivateKey) (string, error) {
	payload, err := signPayloadBytes(b)
	if err != nil {
		return "", fmt.Errorf("marshal beacon for signing: %w", err)
	}
	sig := ed25519.Sign(priv, payload)
	return hex.EncodeToString(sig), nil
}

// VerifyBeacon verifies the beacon's Ed25519 signature against its campfire_id (the public key).
// Returns an error if the signature is invalid or the campfire_id is not a valid Ed25519 public key.
func VerifyBeacon(b Beacon) error {
	pubBytes, err := hex.DecodeString(b.CampfireID)
	if err != nil {
		return fmt.Errorf("decode campfire_id as hex: %w", err)
	}
	if len(pubBytes) != ed25519.PublicKeySize {
		return fmt.Errorf("campfire_id has wrong length: got %d bytes, want %d", len(pubBytes), ed25519.PublicKeySize)
	}
	pub := ed25519.PublicKey(pubBytes)

	sigBytes, err := hex.DecodeString(b.Signature)
	if err != nil {
		return fmt.Errorf("decode signature as hex: %w", err)
	}

	payload, err := signPayloadBytes(b)
	if err != nil {
		return fmt.Errorf("marshal beacon for verification: %w", err)
	}

	if !ed25519.Verify(pub, payload, sigBytes) {
		return fmt.Errorf("beacon signature verification failed")
	}
	return nil
}

// beaconPath returns the repository path for a beacon file given its campfire ID.
func beaconPath(campfireID string) string {
	return ".campfire/beacons/" + campfireID + ".json"
}

const beaconDir = ".campfire/beacons"

// githubDirEntry is a single entry from the GitHub Contents API directory listing.
type githubDirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"`
}

// listBeaconFiles retrieves the list of .json files in .campfire/beacons/ using
// the GitHub Contents API. Returns an empty slice if the directory is empty or not found.
func listBeaconFiles(client *githubClient, repo string) ([]githubDirEntry, error) {
	data, err := client.GetFile(repo, beaconDir)
	if err != nil {
		// If the directory does not exist yet, treat as empty.
		if strings.Contains(err.Error(), "not found") {
			return []githubDirEntry{}, nil
		}
		return nil, fmt.Errorf("list beacon directory: %w", err)
	}

	// GetFile returns raw bytes. For a directory listing, GitHub returns a JSON array.
	// For a single file, it returns an object. Detect by first non-whitespace byte.
	trimmed := strings.TrimSpace(string(data))
	if !strings.HasPrefix(trimmed, "[") {
		// Unexpected: a file at the beacons path? Return empty.
		return []githubDirEntry{}, nil
	}

	var entries []githubDirEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse beacon directory listing: %w", err)
	}
	return entries, nil
}

// PublishBeacon commits a beacon JSON file to the coordination repository at
// .campfire/beacons/{campfire_id}.json.
//
// Requires a GitHub token with Contents: Read+Write scope on the coordination repository.
//
// The beacon must have a non-empty CampfireID and a valid Signature (use SignBeacon to create one).
func PublishBeacon(client *githubClient, repo string, b Beacon) error {
	if b.CampfireID == "" {
		return fmt.Errorf("beacon campfire_id must not be empty")
	}

	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal beacon: %w", err)
	}

	path := beaconPath(b.CampfireID)
	commitMsg := fmt.Sprintf("campfire: publish beacon for %s", b.CampfireID[:min(16, len(b.CampfireID))])

	if err := client.PutFile(repo, path, commitMsg, data); err != nil {
		return fmt.Errorf("publish beacon: %w", err)
	}
	return nil
}

// DiscoverBeacons lists and verifies beacon files from .campfire/beacons/ in the
// coordination repository. Beacons with invalid or missing signatures are silently
// skipped (logged at debug level). Returns an empty (non-nil) slice when no valid
// beacons are found.
func DiscoverBeacons(client *githubClient, repo string) ([]Beacon, error) {
	entries, err := listBeaconFiles(client, repo)
	if err != nil {
		return nil, err
	}

	result := []Beacon{}
	for _, entry := range entries {
		if entry.Type != "file" || !strings.HasSuffix(entry.Name, ".json") {
			continue
		}

		raw, err := client.GetFile(repo, entry.Path)
		if err != nil {
			log.Printf("campfire/github: discover: skip %q: fetch failed: %v", entry.Path, err)
			continue
		}

		var b Beacon
		if err := json.Unmarshal(raw, &b); err != nil {
			log.Printf("campfire/github: discover: skip %q: json unmarshal failed: %v", entry.Path, err)
			continue
		}

		if err := VerifyBeacon(b); err != nil {
			log.Printf("campfire/github: discover: skip %q: signature invalid: %v", entry.Path, err)
			continue
		}

		result = append(result, b)
	}
	return result, nil
}

