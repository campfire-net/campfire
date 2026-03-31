package naming

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// JoinPolicy holds the operator's join policy configuration.
// When join_policy is "consult", incoming join requests are forwarded
// to the consult campfire for approval before being admitted.
type JoinPolicy struct {
	// JoinPolicy is the policy type — currently always "consult".
	JoinPolicy string `json:"join_policy"`
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
	if os.IsNotExist(err) {
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
func SaveJoinPolicy(cfHome string, jp *JoinPolicy) error {
	data, err := json.MarshalIndent(jp, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling join policy: %w", err)
	}
	path := filepath.Join(cfHome, joinPolicyFile)
	if err := os.MkdirAll(cfHome, 0700); err != nil {
		return fmt.Errorf("creating campfire home: %w", err)
	}
	return os.WriteFile(path, data, 0600)
}
