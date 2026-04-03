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

// SignerType identifies the authority level of a declaration signer.
type SignerType string

const (
	SignerConventionRegistry SignerType = "convention_registry"
	SignerCampfireKey        SignerType = "campfire_key"
	SignerMemberKey          SignerType = "member_key"
)

// signerAuthority returns the authority level of a signer type.
// Higher number = higher authority.
func signerAuthority(s SignerType) int {
	switch s {
	case SignerConventionRegistry:
		return 3
	case SignerCampfireKey:
		return 2
	case SignerMemberKey:
		return 1
	default:
		return 0
	}
}

// PinAction is the result of a pin check.
type PinAction string

const (
	PinNew    PinAction = "new"    // no existing pin, accept and store
	PinAccept PinAction = "accept" // matches existing pin
	PinReject PinAction = "reject" // lower authority, rejected
	PinHold   PinAction = "hold"   // ambiguous conflict, held for review
)

// Pin records a TOFU-pinned declaration.
type Pin struct {
	ContentHash string      `json:"content_hash"` // SHA-256 of declaration payload
	SignerKey   string      `json:"signer_key"`   // hex-encoded public key
	SignerType  SignerType  `json:"signer_type"`
	TrustStatus TrustStatus `json:"trust_status"`
	PinnedAt   time.Time   `json:"pinned_at"`
}

// PinScope defines the scope for ClearPins.
type PinScope struct {
	CampfireID string // clear pins for a specific campfire (empty = no filter)
	Convention string // clear pins for a specific convention (empty = no filter)
	All        bool   // clear all pins
}

// pinFile is the serialized pin store format.
// Pins is stored as a raw JSON value so the HMAC can be computed over the
// exact bytes that appear in the file — eliminating the TOCTOU between the
// HMAC computation and the final serialization.
type pinFile struct {
	Pins json.RawMessage `json:"pins"`
	HMAC string          `json:"hmac"`
}

// PinStore provides TOFU pin persistence per Trust Convention §8.
type PinStore struct {
	path   string // file path for pin storage
	hmacKey []byte // derived from agent private key
	mu     sync.RWMutex
	pins   map[string]*Pin // key: "campfireID:convention:operation"
}

// NewPinStore creates a pin store at the given path. The HMAC key is derived
// from the agent's private key using SHA-256("campfire-trust-pins" || privKey[:32]).
func NewPinStore(path string, privKey []byte) (*PinStore, error) {
	if len(privKey) < 32 {
		return nil, fmt.Errorf("private key must be at least 32 bytes")
	}

	h := sha256.New()
	h.Write([]byte("campfire-trust-pins"))
	h.Write(privKey[:32])
	hmacKey := h.Sum(nil)

	ps := &PinStore{
		path:    path,
		hmacKey: hmacKey,
		pins:    make(map[string]*Pin),
	}

	// Load existing pins if the file exists.
	if _, err := os.Stat(path); err == nil {
		if err := ps.Load(); err != nil {
			return nil, fmt.Errorf("loading pin store: %w", err)
		}
	}

	return ps, nil
}

// pinKey constructs the map key for a pin.
func pinKey(campfireID, convention, operation string) string {
	return campfireID + ":" + convention + ":" + operation
}

// CheckPin evaluates a declaration against existing pins per §8.2.
func (ps *PinStore) CheckPin(campfireID, convention, operation string, payload []byte, signerKey string, signerType SignerType) (PinAction, error) {
	key := pinKey(campfireID, convention, operation)
	contentHash := sha256Hex(payload)

	ps.mu.RLock()
	existing, exists := ps.pins[key]
	ps.mu.RUnlock()

	if !exists {
		return PinNew, nil
	}

	// Same content, same signer — no change needed.
	if existing.ContentHash == contentHash && existing.SignerKey == signerKey {
		return PinAccept, nil
	}

	existingAuth := signerAuthority(existing.SignerType)
	newAuth := signerAuthority(signerType)

	// Higher authority replaces lower: apply immediately.
	if newAuth > existingAuth {
		return PinAccept, nil
	}

	// Lower authority attempts to replace higher: reject.
	if newAuth < existingAuth {
		return PinReject, nil
	}

	// Same authority, different content.
	// Prefer convention registry version as tiebreaker.
	if signerType == SignerConventionRegistry {
		return PinAccept, nil
	}

	// Same authority, same version, different content, no valid supersedes:
	// hold and log warning.
	return PinHold, nil
}

// SetPin stores or replaces a pin.
func (ps *PinStore) SetPin(campfireID, convention, operation string, pin *Pin) {
	key := pinKey(campfireID, convention, operation)

	ps.mu.Lock()
	ps.pins[key] = pin
	ps.mu.Unlock()
}

// ListPins returns all stored pins.
func (ps *PinStore) ListPins() map[string]*Pin {
	ps.mu.RLock()
	defer ps.mu.RUnlock()

	result := make(map[string]*Pin, len(ps.pins))
	for k, v := range ps.pins {
		pinCopy := *v
		result[k] = &pinCopy
	}
	return result
}

// ClearPins removes pins matching the given scope.
func (ps *PinStore) ClearPins(scope PinScope) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if scope.All {
		ps.pins = make(map[string]*Pin)
		return
	}

	for key := range ps.pins {
		if scope.CampfireID != "" && !keyMatchesCampfire(key, scope.CampfireID) {
			continue
		}
		if scope.Convention != "" && !keyMatchesConvention(key, scope.Convention) {
			continue
		}
		delete(ps.pins, key)
	}
}

// Save writes the pin store to disk with HMAC integrity and 0600 permissions.
// The write is atomic: data is written to a temp file in the same directory
// and then renamed over the target, so a crash mid-write never corrupts the
// existing file.
//
// The HMAC is computed over the exact bytes of the serialized pins map before
// they are embedded in the outer pinFile struct, eliminating any TOCTOU
// between the HMAC computation and the final serialization.
func (ps *PinStore) Save() error {
	ps.mu.RLock()
	defer ps.mu.RUnlock()

	// Serialize the pins map first — this is the canonical byte sequence the
	// HMAC will cover.
	pinsJSON, err := json.MarshalIndent(ps.pins, "  ", "  ")
	if err != nil {
		return fmt.Errorf("marshaling pins: %w", err)
	}

	// Compute HMAC over the exact bytes that will be stored.
	mac := hmac.New(sha256.New, ps.hmacKey)
	mac.Write(pinsJSON)
	macHex := hex.EncodeToString(mac.Sum(nil))

	// Embed the already-serialized pins as a raw JSON value so that the bytes
	// that appear in the file are identical to what the HMAC covers.
	pf := pinFile{
		Pins: json.RawMessage(pinsJSON),
		HMAC: macHex,
	}

	data, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling pin file: %w", err)
	}

	// Atomic write: write to a temp file then rename.
	dir := filepath.Dir(ps.path)
	tmp, err := os.CreateTemp(dir, ".pins-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()

	// Clean up the temp file if anything goes wrong before the rename.
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
		return fmt.Errorf("writing pin file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("syncing pin file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}

	if err := os.Rename(tmpPath, ps.path); err != nil {
		return fmt.Errorf("renaming temp file: %w", err)
	}
	success = true
	return nil
}

// Load reads the pin store from disk and verifies HMAC integrity.
// The HMAC is verified against the raw bytes of the "pins" JSON value —
// the same bytes that Save() used when computing the HMAC — so no
// re-serialization is required and the check is exact.
func (ps *PinStore) Load() error {
	data, err := os.ReadFile(ps.path)
	if err != nil {
		return fmt.Errorf("reading pin file: %w", err)
	}

	var pf pinFile
	if err := json.Unmarshal(data, &pf); err != nil {
		return fmt.Errorf("parsing pin file: %w", err)
	}

	// Verify HMAC against the raw bytes of the stored pins value.
	mac := hmac.New(sha256.New, ps.hmacKey)
	mac.Write([]byte(pf.Pins))
	expectedMAC := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(pf.HMAC), []byte(expectedMAC)) {
		return fmt.Errorf("HMAC verification failed: pin file may be tampered")
	}

	// Decode the pins map from the raw JSON.
	var pins map[string]*Pin
	if err := json.Unmarshal(pf.Pins, &pins); err != nil {
		return fmt.Errorf("decoding pins: %w", err)
	}

	ps.mu.Lock()
	ps.pins = pins
	if ps.pins == nil {
		ps.pins = make(map[string]*Pin)
	}
	ps.mu.Unlock()

	return nil
}

// sha256Hex returns the hex-encoded SHA-256 hash of data.
func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// keyMatchesCampfire checks if a pin key starts with the given campfire ID.
func keyMatchesCampfire(key, campfireID string) bool {
	return len(key) > len(campfireID) && key[:len(campfireID)+1] == campfireID+":"
}

// keyMatchesConvention checks if a pin key contains the given convention.
// Key format: "campfireID:convention:operation"
func keyMatchesConvention(key, convention string) bool {
	// Find first colon (end of campfireID).
	firstColon := -1
	for i := 0; i < len(key); i++ {
		if key[i] == ':' {
			firstColon = i
			break
		}
	}
	if firstColon < 0 {
		return false
	}
	rest := key[firstColon+1:]
	// Find second colon (end of convention).
	secondColon := -1
	for i := 0; i < len(rest); i++ {
		if rest[i] == ':' {
			secondColon = i
			break
		}
	}
	if secondColon < 0 {
		return rest == convention
	}
	return rest[:secondColon] == convention
}
