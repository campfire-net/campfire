package provenance

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// AttestationStore is the interface satisfied by both the in-memory Store and
// the file-backed FileStore. Code that needs to work with either should accept
// this interface.
type AttestationStore interface {
	AddAttestation(a *Attestation) error
	Revoke(attestationID string) error
	Level(key string) Level
	Attestations(key string) []*Attestation
}

// persistedState is the on-disk format for the attestation store.
type persistedState struct {
	Attestations map[string][]*Attestation `json:"attestations"`
	SelfClaimed  map[string]bool           `json:"self_claimed"`
}

// NewFileStore loads an existing attestation store from path (or creates a new
// empty one if the file does not exist) and wraps it with automatic persistence.
// Every mutation flushes to disk atomically via a temp-file rename.
//
// The config parameter sets the in-memory store configuration (freshness window,
// transitivity depth, trusted verifiers). Config is NOT persisted — callers must
// supply the same config each time they open the store.
func NewFileStore(path string, cfg StoreConfig) (*FileStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}

	inner := NewStore(cfg)

	// Load existing state if the file is present.
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(data) > 0 {
		var state persistedState
		if jsonErr := json.Unmarshal(data, &state); jsonErr != nil {
			return nil, jsonErr
		}
		// Restore attestations directly into the inner store's map.
		inner.mu.Lock()
		for key, atts := range state.Attestations {
			inner.attestations[key] = atts
		}
		for key, claimed := range state.SelfClaimed {
			inner.selfClaimed[key] = claimed
		}
		inner.mu.Unlock()
	}

	return &FileStore{path: path, inner: inner}, nil
}

// FileStore is the public handle returned by NewFileStore.
// It wraps *Store and adds persistence on every mutation.
type FileStore struct {
	mu    sync.Mutex
	path  string
	inner *Store
}

// AddAttestation stores an attestation and flushes to disk.
func (fs *FileStore) AddAttestation(a *Attestation) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	err := fs.inner.AddAttestation(a)
	// ErrNotCoSigned is a warning — the attestation was stored, still flush.
	if err != nil && !errors.Is(err, ErrNotCoSigned) {
		return err
	}
	flushErr := fs.flush()
	if flushErr != nil && err == nil {
		return flushErr
	}
	return err
}

// SetSelfClaimed marks a key as self-claimed and flushes to disk.
func (fs *FileStore) SetSelfClaimed(key string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.inner.SetSelfClaimed(key)
	return fs.flush()
}

// Revoke marks an attestation as revoked and flushes to disk.
func (fs *FileStore) Revoke(attestationID string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if err := fs.inner.Revoke(attestationID); err != nil {
		return err
	}
	return fs.flush()
}

// Level delegates to the inner store.
func (fs *FileStore) Level(key string) Level {
	return fs.inner.Level(key)
}

// Attestations delegates to the inner store.
func (fs *FileStore) Attestations(key string) []*Attestation {
	return fs.inner.Attestations(key)
}

// TrustVerifier delegates to the inner store (does not persist config).
func (fs *FileStore) TrustVerifier(verifierKey string, depth int) {
	fs.inner.TrustVerifier(verifierKey, depth)
}

// flush writes state to disk atomically.
// Caller must hold fs.mu.
func (fs *FileStore) flush() error {
	fs.inner.mu.RLock()
	state := persistedState{
		Attestations: make(map[string][]*Attestation, len(fs.inner.attestations)),
		SelfClaimed:  make(map[string]bool, len(fs.inner.selfClaimed)),
	}
	for k, v := range fs.inner.attestations {
		state.Attestations[k] = v
	}
	for k, v := range fs.inner.selfClaimed {
		state.SelfClaimed[k] = v
	}
	fs.inner.mu.RUnlock()

	data, err := json.Marshal(state)
	if err != nil {
		return err
	}

	// Atomic write: write to temp file then rename.
	tmp := fs.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, fs.path)
}
