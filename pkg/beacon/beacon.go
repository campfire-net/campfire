package beacon

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"time"

	cfencoding "github.com/campfire-net/campfire/cf-protocol/encoding"
)

// DefaultBeaconNotAfterDuration is the default not_after lifetime for a beacon (~30 days).
// Producers that do not set not_after explicitly use this default.
// The field is TAINTED — consumers honor it as an advisory expiry.
// Citation: design v2 §6 P2, OPEN-024.
const DefaultBeaconNotAfterDuration = 30 * 24 * time.Hour

// TransportConfig describes how to connect to a campfire.
type TransportConfig struct {
	Protocol string            `cbor:"1,keyasint" json:"protocol"`
	Config   map[string]string `cbor:"2,keyasint" json:"config"`
}

// Beacon advertises a campfire for discovery.
// The NotAfter field was added in cf 0.30.0 (OPEN-024). It is TAINTED — signed
// by the campfire key but NOT included in the BeaconSignInput, so it is advisory only.
// Default lifetime is DefaultBeaconNotAfterDuration (~30 days).
type Beacon struct {
	CampfireID            []byte          `cbor:"1,keyasint" json:"campfire_id"`
	JoinProtocol          string          `cbor:"2,keyasint" json:"join_protocol"`
	ReceptionRequirements []string        `cbor:"3,keyasint" json:"reception_requirements"`
	Transport             TransportConfig `cbor:"4,keyasint" json:"transport"`
	Description           string          `cbor:"5,keyasint" json:"description"`
	Signature             []byte          `cbor:"6,keyasint" json:"signature"`
	// NotAfter is a TAINTED advisory expiry Unix timestamp (OPEN-024).
	// Zero means no expiry declared. Consumers MUST NOT treat this as a
	// security guarantee — it is advisory only.
	NotAfter int64 `cbor:"7,keyasint,omitempty" json:"not_after,omitempty"`
}

// BeaconSignInput is the canonical form for signing.
type BeaconSignInput struct {
	CampfireID            []byte          `cbor:"1,keyasint"`
	JoinProtocol          string          `cbor:"2,keyasint"`
	ReceptionRequirements []string        `cbor:"3,keyasint"`
	Transport             TransportConfig `cbor:"4,keyasint"`
	Description           string          `cbor:"5,keyasint"`
}

// New creates a signed beacon for a campfire.
func New(
	campfirePub ed25519.PublicKey,
	campfirePriv ed25519.PrivateKey,
	joinProtocol string,
	receptionReqs []string,
	transport TransportConfig,
	description string,
) (*Beacon, error) {
	if receptionReqs == nil {
		receptionReqs = []string{}
	}

	signInput := BeaconSignInput{
		CampfireID:            campfirePub,
		JoinProtocol:          joinProtocol,
		ReceptionRequirements: receptionReqs,
		Transport:             transport,
		Description:           description,
	}
	signBytes, err := cfencoding.Marshal(signInput)
	if err != nil {
		return nil, fmt.Errorf("encoding sign input: %w", err)
	}

	sig := ed25519.Sign(campfirePriv, signBytes)

	return &Beacon{
		CampfireID:            campfirePub,
		JoinProtocol:          joinProtocol,
		ReceptionRequirements: receptionReqs,
		Transport:             transport,
		Description:           description,
		Signature:             sig,
		NotAfter:              time.Now().Add(DefaultBeaconNotAfterDuration).Unix(),
	}, nil
}

// IsExpired reports whether the beacon's not_after advisory expiry has passed.
// A beacon with NotAfter == 0 never expires. The field is TAINTED — this check
// is advisory; cryptographic validity is determined by Verify().
func (b *Beacon) IsExpired(now time.Time) bool {
	if b.NotAfter == 0 {
		return false
	}
	return now.Unix() > b.NotAfter
}

// NotAfterTime returns the not_after field as a time.Time.
// Returns the zero value if not_after is not set.
func (b *Beacon) NotAfterTime() time.Time {
	if b.NotAfter == 0 {
		return time.Time{}
	}
	return time.Unix(b.NotAfter, 0)
}

// Verify checks the beacon's signature.
func (b *Beacon) Verify() bool {
	signInput := BeaconSignInput{
		CampfireID:            b.CampfireID,
		JoinProtocol:          b.JoinProtocol,
		ReceptionRequirements: b.ReceptionRequirements,
		Transport:             b.Transport,
		Description:           b.Description,
	}
	signBytes, err := cfencoding.Marshal(signInput)
	if err != nil {
		return false
	}
	return ed25519.Verify(b.CampfireID, signBytes, b.Signature)
}

// CampfireIDHex returns the hex-encoded campfire public key.
func (b *Beacon) CampfireIDHex() string {
	return fmt.Sprintf("%x", b.CampfireID)
}

// DefaultBeaconDir returns the default beacon directory.
// Resolution order:
//  1. CF_BEACON_DIR env var (explicit override)
//  2. $CF_HOME/beacons when CF_HOME is set (propagated from --cf-home by the CLI)
//  3. ~/.cf/beacons
//  4. ~/.campfire/beacons if ~/.campfire exists but ~/.cf does not (deprecated; support will be removed in v0.17)
func DefaultBeaconDir() string {
	if env := os.Getenv("CF_BEACON_DIR"); env != "" {
		return env
	}
	// CF_HOME is set by the CLI's PersistentPreRun when --cf-home is passed.
	// Respect it so that isolated --cf-home invocations (e.g. tests, demo
	// scripts, multi-identity setups) do not scan the global ~/.cf/beacons.
	if cfHome := os.Getenv("CF_HOME"); cfHome != "" {
		return filepath.Join(cfHome, "beacons")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/campfire/beacons"
	}
	cfDir := filepath.Join(home, ".cf")
	campfireDir := filepath.Join(home, ".campfire")
	if _, err := os.Stat(cfDir); err == nil {
		return filepath.Join(cfDir, "beacons")
	}
	if _, err := os.Stat(campfireDir); err == nil {
		return filepath.Join(campfireDir, "beacons")
	}
	return filepath.Join(cfDir, "beacons")
}

// Publish writes a beacon file to the beacon directory.
func Publish(beaconDir string, b *Beacon) error {
	if err := os.MkdirAll(beaconDir, 0700); err != nil {
		return fmt.Errorf("creating beacon directory: %w", err)
	}

	data, err := cfencoding.Marshal(b)
	if err != nil {
		return fmt.Errorf("encoding beacon: %w", err)
	}

	filename := fmt.Sprintf("%x.beacon", b.CampfireID)
	path := filepath.Join(beaconDir, filename)

	// Atomic write
	var randBytes [8]byte
	rand.Read(randBytes[:])
	tmp := fmt.Sprintf("%s.tmp.%x", path, randBytes)

	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("writing temp beacon: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("renaming beacon: %w", err)
	}
	return nil
}

// Remove deletes a beacon file from the beacon directory.
func Remove(beaconDir string, campfireID []byte) error {
	filename := fmt.Sprintf("%x.beacon", campfireID)
	path := filepath.Join(beaconDir, filename)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing beacon: %w", err)
	}
	return nil
}

// Scan reads all beacon files from the beacon directory.
func Scan(beaconDir string) ([]Beacon, error) {
	entries, err := os.ReadDir(beaconDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading beacon directory: %w", err)
	}

	var beacons []Beacon
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".beacon" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(beaconDir, e.Name()))
		if err != nil {
			continue // skip unreadable files
		}
		var b Beacon
		if err := cfencoding.Unmarshal(data, &b); err != nil {
			continue // skip corrupted files
		}
		if !b.Verify() {
			continue // skip beacons with invalid signatures
		}
		beacons = append(beacons, b)
	}
	return beacons, nil
}
