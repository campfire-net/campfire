package trust

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// KeyPin records an operator-pinned public key for a campfire.
// This is the TOFU acceptance record: the operator explicitly acknowledges
// "I trust this pubkey as the key for this campfire."
type KeyPin struct {
	// CampfireID is the hex-encoded campfire identifier this key is pinned for.
	CampfireID string `json:"campfire_id"`
	// PubKey is the hex-encoded ed25519 public key pinned for this campfire.
	PubKey string `json:"pubkey"`
	// PinnedAt is when the operator pinned this key.
	PinnedAt time.Time `json:"pinned_at"`
	// LastUsed is the last time this pin was consulted during trust evaluation.
	// Zero value means the pin has not been used since it was created.
	LastUsed time.Time `json:"last_used,omitempty"`
}

// keypinFile is the serialized key pin store format.
// Same HMAC-over-raw-bytes approach as pinFile to eliminate TOCTOU.
type keypinFile struct {
	Pins json.RawMessage `json:"pins"`
	HMAC string          `json:"hmac"`
}

// KeyPinStore provides per-campfire TOFU key pin persistence.
// Storage: ~/.cf/key-pins.json, HMAC-integrity protected using the same
// derivation as PinStore (SHA-256 of ed25519.Sign(privKey, "campfire-trust-pins-v1")).
type KeyPinStore struct {
	path    string
	hmacKey []byte
	mu      sync.RWMutex
	pins    map[string]*KeyPin // key: campfireID (hex)
}

// NewKeyPinStore creates a key pin store at the given path.
// The HMAC key is derived from the agent's private key using the same
// derivation as NewPinStore — SHA-256(ed25519.Sign(privKey, "campfire-trust-pins-v1")).
//
// privKey must be an ed25519 seed (32 bytes) or a full private key (64 bytes).
func NewKeyPinStore(path string, privKey []byte) (*KeyPinStore, error) {
	if len(privKey) < 32 {
		return nil, fmt.Errorf("private key must be at least 32 bytes")
	}

	hmacKey := deriveHMACFromSeed(privKey)

	kps := &KeyPinStore{
		path:    path,
		hmacKey: hmacKey,
		pins:    make(map[string]*KeyPin),
	}

	if _, err := os.Stat(path); err == nil {
		if err := kps.Load(); err != nil {
			return nil, fmt.Errorf("loading key pin store: %w", err)
		}
	}

	return kps, nil
}

// SetKeyPin stores or replaces a key pin for a campfire.
func (kps *KeyPinStore) SetKeyPin(campfireID, pubKey string) {
	kps.mu.Lock()
	kps.pins[campfireID] = &KeyPin{
		CampfireID: campfireID,
		PubKey:     pubKey,
		PinnedAt:   time.Now().UTC(),
	}
	kps.mu.Unlock()
}

// GetKeyPin returns the key pin for a campfire, or nil if not found.
func (kps *KeyPinStore) GetKeyPin(campfireID string) *KeyPin {
	kps.mu.RLock()
	defer kps.mu.RUnlock()
	p := kps.pins[campfireID]
	if p == nil {
		return nil
	}
	cp := *p
	return &cp
}

// RemoveKeyPin removes the key pin for a campfire. No-op if not present.
func (kps *KeyPinStore) RemoveKeyPin(campfireID string) {
	kps.mu.Lock()
	delete(kps.pins, campfireID)
	kps.mu.Unlock()
}

// ListKeyPins returns a snapshot of all stored key pins.
func (kps *KeyPinStore) ListKeyPins() []*KeyPin {
	kps.mu.RLock()
	defer kps.mu.RUnlock()
	result := make([]*KeyPin, 0, len(kps.pins))
	for _, p := range kps.pins {
		cp := *p
		result = append(result, &cp)
	}
	return result
}

// PruneKeyPins removes pins for campfires that are NOT in the provided set
// of active campfire IDs. Returns the list of pruned campfire IDs.
func (kps *KeyPinStore) PruneKeyPins(activeCampfireIDs map[string]struct{}) []string {
	kps.mu.Lock()
	defer kps.mu.Unlock()

	var pruned []string
	for campfireID := range kps.pins {
		if _, active := activeCampfireIDs[campfireID]; !active {
			pruned = append(pruned, campfireID)
			delete(kps.pins, campfireID)
		}
	}
	return pruned
}

// Save writes the key pin store to disk with HMAC integrity and 0600 permissions.
// Write is atomic: temp file + rename.
func (kps *KeyPinStore) Save() error {
	kps.mu.RLock()
	defer kps.mu.RUnlock()

	pinsJSON, err := json.MarshalIndent(kps.pins, "  ", "  ")
	if err != nil {
		return fmt.Errorf("marshaling key pins: %w", err)
	}

	mac := hmac.New(sha256.New, kps.hmacKey)
	mac.Write(pinsJSON)
	macHex := hex.EncodeToString(mac.Sum(nil))

	pf := keypinFile{
		Pins: json.RawMessage(pinsJSON),
		HMAC: macHex,
	}

	data, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling key pin file: %w", err)
	}

	dir := filepath.Dir(kps.path)
	tmp, err := os.CreateTemp(dir, ".key-pins-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()

	success := false
	defer func() {
		if !success {
			os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("setting temp file permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing key pin file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("syncing key pin file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}

	if err := os.Rename(tmpPath, kps.path); err != nil {
		return fmt.Errorf("renaming temp file: %w", err)
	}
	success = true
	return nil
}

// Load reads the key pin store from disk and verifies HMAC integrity.
func (kps *KeyPinStore) Load() error {
	data, err := os.ReadFile(kps.path)
	if err != nil {
		return fmt.Errorf("reading key pin file: %w", err)
	}

	var pf keypinFile
	if err := json.Unmarshal(data, &pf); err != nil {
		return fmt.Errorf("parsing key pin file: %w", err)
	}

	mac := hmac.New(sha256.New, kps.hmacKey)
	mac.Write([]byte(pf.Pins))
	expectedMAC := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(pf.HMAC), []byte(expectedMAC)) {
		return fmt.Errorf("HMAC verification failed: key pin file may be tampered")
	}

	var pins map[string]*KeyPin
	if err := json.Unmarshal(pf.Pins, &pins); err != nil {
		return fmt.Errorf("decoding key pins: %w", err)
	}

	kps.mu.Lock()
	kps.pins = pins
	if kps.pins == nil {
		kps.pins = make(map[string]*KeyPin)
	}
	kps.mu.Unlock()

	return nil
}
