package naming

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

// JoinPolicy holds the operator's join policy configuration.
// When join_policy is "consult", incoming join requests are forwarded
// to the consult campfire for approval before being admitted.
type JoinPolicy struct {
	// Policy is the policy type — currently always "consult".
	Policy string `json:"join_policy"`
	// ConsultCampfire is the campfire ID of the agent that approves join requests.
	ConsultCampfire string `json:"consult_campfire"`
	// JoinRoot is the default root campfire ID for joins.
	JoinRoot string `json:"join_root"`
}

const joinPolicyFile = "join-policy.json"

// joinPolicyHexRe matches exactly 64 lowercase hex characters — the canonical campfire ID format.
var joinPolicyHexRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// LoadJoinPolicy reads the join policy config from cfHome.
// Returns (nil, nil) if no join policy is configured.
func LoadJoinPolicy(cfHome string) (*JoinPolicy, error) {
	path := filepath.Join(cfHome, joinPolicyFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading join policy: %w", err)
	}
	var jp JoinPolicy
	if err := json.Unmarshal(data, &jp); err != nil {
		return nil, fmt.Errorf("parsing join policy: %w", err)
	}
	if jp.JoinRoot != "" && !joinPolicyHexRe.MatchString(jp.JoinRoot) {
		return nil, fmt.Errorf("invalid join_root %q: must be a 64-character lowercase hex campfire ID", jp.JoinRoot)
	}
	if jp.ConsultCampfire != "" && jp.ConsultCampfire != FSWalkSentinel && !joinPolicyHexRe.MatchString(jp.ConsultCampfire) {
		return nil, fmt.Errorf("invalid consult_campfire %q: must be a 64-character lowercase hex campfire ID or %q", jp.ConsultCampfire, FSWalkSentinel)
	}
	return &jp, nil
}

// SaveJoinPolicy writes the join policy config to cfHome atomically.
// It writes to a temporary file in the same directory and renames it
// into place so a crash during write cannot corrupt the existing file.
func SaveJoinPolicy(cfHome string, jp *JoinPolicy) error {
	data, err := json.MarshalIndent(jp, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling join policy: %w", err)
	}
	if err := os.MkdirAll(cfHome, 0700); err != nil {
		return fmt.Errorf("creating campfire home: %w", err)
	}
	// Write to a temp file in the same directory so the rename is on the
	// same filesystem (guaranteeing an atomic swap on POSIX systems).
	tmp, err := os.CreateTemp(cfHome, joinPolicyFile+".tmp*")
	if err != nil {
		return fmt.Errorf("creating temp file for join policy: %w", err)
	}
	tmpName := tmp.Name()
	// Clean up the temp file if anything goes wrong before the rename.
	defer func() {
		if tmpName != "" {
			os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("setting join policy permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing join policy: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("flushing join policy: %w", err)
	}
	target := filepath.Join(cfHome, joinPolicyFile)
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("installing join policy: %w", err)
	}
	tmpName = "" // rename succeeded — suppress deferred cleanup
	return nil
}
