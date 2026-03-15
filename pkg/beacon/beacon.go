package beacon

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"

	cfencoding "github.com/3dl-dev/campfire/pkg/encoding"
)

// TransportConfig describes how to connect to a campfire.
type TransportConfig struct {
	Protocol string            `cbor:"1,keyasint" json:"protocol"`
	Config   map[string]string `cbor:"2,keyasint" json:"config"`
}

// Beacon advertises a campfire for discovery.
type Beacon struct {
	CampfireID            []byte          `cbor:"1,keyasint" json:"campfire_id"`
	JoinProtocol          string          `cbor:"2,keyasint" json:"join_protocol"`
	ReceptionRequirements []string        `cbor:"3,keyasint" json:"reception_requirements"`
	Transport             TransportConfig `cbor:"4,keyasint" json:"transport"`
	Description           string          `cbor:"5,keyasint" json:"description"`
	Signature             []byte          `cbor:"6,keyasint" json:"signature"`
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
	}, nil
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
func DefaultBeaconDir() string {
	if env := os.Getenv("CF_BEACON_DIR"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/campfire/beacons"
	}
	return filepath.Join(home, ".campfire", "beacons")
}

// Publish writes a beacon file to the beacon directory.
func Publish(beaconDir string, b *Beacon) error {
	if err := os.MkdirAll(beaconDir, 0755); err != nil {
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

	if err := os.WriteFile(tmp, data, 0644); err != nil {
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
		beacons = append(beacons, b)
	}
	return beacons, nil
}
