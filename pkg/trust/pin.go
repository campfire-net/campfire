package trust

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
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
type pinFile struct {
	Pins map[string]*Pin `json:"pins"`
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
func (ps *PinStore) Save() error {
	ps.mu.RLock()
	defer ps.mu.RUnlock()

	pinsJSON, err := json.Marshal(ps.pins)
	if err != nil {
		return fmt.Errorf("marshaling pins: %w", err)
	}

	mac := hmac.New(sha256.New, ps.hmacKey)
	mac.Write(pinsJSON)
	macHex := hex.EncodeToString(mac.Sum(nil))

	pf := pinFile{
		Pins: ps.pins,
		HMAC: macHex,
	}

	data, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling pin file: %w", err)
	}

	return os.WriteFile(ps.path, data, 0600)
}

// Load reads the pin store from disk and verifies HMAC integrity.
func (ps *PinStore) Load() error {
	data, err := os.ReadFile(ps.path)
	if err != nil {
		return fmt.Errorf("reading pin file: %w", err)
	}

	var pf pinFile
	if err := json.Unmarshal(data, &pf); err != nil {
		return fmt.Errorf("parsing pin file: %w", err)
	}

	// Verify HMAC.
	pinsJSON, err := json.Marshal(pf.Pins)
	if err != nil {
		return fmt.Errorf("re-marshaling pins for HMAC: %w", err)
	}

	mac := hmac.New(sha256.New, ps.hmacKey)
	mac.Write(pinsJSON)
	expectedMAC := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(pf.HMAC), []byte(expectedMAC)) {
		return fmt.Errorf("HMAC verification failed: pin file may be tampered")
	}

	ps.mu.Lock()
	ps.pins = pf.Pins
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
