package protocol

import (
	"encoding/json"
	"os"
)

// ProfileFile is the schema for ~/.cf/profile.json.
type ProfileFile struct {
	DisplayName string `json:"display_name"`
}

// LoadProfile reads ~/.cf/profile.json from cfHome.
// Returns a zero-value ProfileFile if the file does not exist or cannot be parsed.
func LoadProfile(cfHome string) ProfileFile {
	path := profilePath(cfHome)
	data, err := os.ReadFile(path)
	if err != nil {
		return ProfileFile{}
	}
	var p ProfileFile
	if err := json.Unmarshal(data, &p); err != nil {
		return ProfileFile{}
	}
	return p
}

// SaveProfile writes a ProfileFile to ~/.cf/profile.json in cfHome.
func SaveProfile(cfHome string, p ProfileFile) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(profilePath(cfHome), data, 0600)
}

func profilePath(cfHome string) string {
	return cfHome + "/profile.json"
}
