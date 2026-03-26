package naming

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// OperatorRoot holds the operator's root campfire configuration.
type OperatorRoot struct {
	// Name is the org/operator name (e.g. "baron").
	Name string `json:"name"`
	// CampfireID is the 64-hex campfire ID of the root campfire.
	CampfireID string `json:"campfire_id"`
}

const operatorRootFile = "operator-root.json"

// LoadOperatorRoot reads the operator root config from cfHome.
// Returns (nil, nil) if no root is configured.
func LoadOperatorRoot(cfHome string) (*OperatorRoot, error) {
	path := filepath.Join(cfHome, operatorRootFile)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading operator root: %w", err)
	}
	var root OperatorRoot
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parsing operator root: %w", err)
	}
	return &root, nil
}

// SaveOperatorRoot writes the operator root config to cfHome.
func SaveOperatorRoot(cfHome string, root *OperatorRoot) error {
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling operator root: %w", err)
	}
	path := filepath.Join(cfHome, operatorRootFile)
	if err := os.MkdirAll(cfHome, 0700); err != nil {
		return fmt.Errorf("creating campfire home: %w", err)
	}
	return os.WriteFile(path, data, 0600)
}
