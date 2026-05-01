package naming

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// AliasStore persists local campfire aliases in ~/.campfire/aliases.json.
// Aliases map short names (e.g. "lobby") to full 64-hex campfire IDs.
type AliasStore struct {
	path string
}

// NewAliasStore returns an AliasStore rooted at cfHome.
func NewAliasStore(cfHome string) *AliasStore {
	return &AliasStore{path: filepath.Join(cfHome, "aliases.json")}
}

// Get returns the campfire ID for an alias, or an error if not found.
func (a *AliasStore) Get(alias string) (string, error) {
	m, err := a.load()
	if err != nil {
		return "", err
	}
	id, ok := m[alias]
	if !ok {
		return "", fmt.Errorf("alias %q not found", alias)
	}
	return id, nil
}

// Set stores or overwrites an alias mapping.
func (a *AliasStore) Set(alias, campfireID string) error {
	m, err := a.load()
	if err != nil {
		return err
	}
	m[alias] = campfireID
	return a.save(m)
}

// Remove deletes an alias. Returns an error if the alias does not exist.
func (a *AliasStore) Remove(alias string) error {
	m, err := a.load()
	if err != nil {
		return err
	}
	if _, ok := m[alias]; !ok {
		return fmt.Errorf("alias %q not found", alias)
	}
	delete(m, alias)
	return a.save(m)
}

// List returns all aliases as a map[alias]campfireID.
func (a *AliasStore) List() (map[string]string, error) {
	return a.load()
}

func (a *AliasStore) load() (map[string]string, error) {
	data, err := os.ReadFile(a.path)
	if os.IsNotExist(err) {
		return make(map[string]string), nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading aliases: %w", err)
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing aliases: %w", err)
	}
	return m, nil
}

func (a *AliasStore) save(m map[string]string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling aliases: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0700); err != nil {
		return fmt.Errorf("creating aliases dir: %w", err)
	}
	return os.WriteFile(a.path, data, 0600)
}
