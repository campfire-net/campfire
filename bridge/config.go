package bridge

import (
	"fmt"
	"os"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the bridge configuration loaded from YAML.
type Config struct {
	Azure    AzureConfig     `yaml:"azure"`
	Identity string          `yaml:"identity"`
	CFHome   string          `yaml:"cf_home"`
	BridgeDB string          `yaml:"bridge_db"`
	Listen   string          `yaml:"listen"`
	Campfire []CampfireRoute `yaml:"campfires"`
	ACL      []ACLEntry      `yaml:"acl"`
	Idents   []IdentityEntry `yaml:"identities"`
	Urgent   []string        `yaml:"urgent_campfires"`
}

// AzureConfig holds Azure Bot Service credentials.
type AzureConfig struct {
	AppID       string `yaml:"app_id"`
	AppPassword string `yaml:"app_password"`
	TenantID    string `yaml:"tenant_id"`
}

// CampfireRoute maps a campfire to a Teams channel.
type CampfireRoute struct {
	ID                  string        `yaml:"id"`
	TeamsChannel        string        `yaml:"teams_channel"`
	PollInterval        time.Duration `yaml:"poll_interval"`
	UrgentPollInterval  time.Duration `yaml:"urgent_poll_interval"`
	UrgentTags          []string      `yaml:"urgent_tags"`
	WebhookURL          string        `yaml:"webhook_url"` // Phase 1: incoming webhook
}

// ACLEntry maps a Teams user to allowed campfires.
type ACLEntry struct {
	TeamsUserID string   `yaml:"teams_user_id"`
	DisplayName string   `yaml:"display_name"`
	Campfires   []string `yaml:"campfires"`
}

// IdentityEntry maps a pubkey to a display name.
type IdentityEntry struct {
	Pubkey      string `yaml:"pubkey"`
	DisplayName string `yaml:"display_name"`
	Role        string `yaml:"role"`
	Color       string `yaml:"color"`
}

var envVarRe = regexp.MustCompile(`\$\{([^}]+)\}|\$([A-Z_][A-Z0-9_]*)`)

// LoadConfig reads and parses the bridge config from a YAML file.
// Environment variables referenced as ${VAR} or $VAR are interpolated.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	// Interpolate environment variables.
	expanded := envVarRe.ReplaceAllStringFunc(string(data), func(match string) string {
		subs := envVarRe.FindStringSubmatch(match)
		name := subs[1]
		if name == "" {
			name = subs[2]
		}
		if val, ok := os.LookupEnv(name); ok {
			return val
		}
		return match // leave unresolved refs as-is
	})

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	// Apply defaults.
	if cfg.CFHome == "" {
		home, _ := os.UserHomeDir()
		cfg.CFHome = home + "/.campfire"
	}
	if cfg.BridgeDB == "" {
		cfg.BridgeDB = cfg.CFHome + "/bridge.db"
	}
	if cfg.Listen == "" {
		cfg.Listen = ":3978"
	}
	for i := range cfg.Campfire {
		if cfg.Campfire[i].PollInterval == 0 {
			cfg.Campfire[i].PollInterval = 5 * time.Second
		}
		if cfg.Campfire[i].UrgentPollInterval == 0 {
			cfg.Campfire[i].UrgentPollInterval = 1 * time.Second
		}
	}

	return &cfg, nil
}
