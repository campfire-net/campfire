package bridge

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "bridge.yaml")

	os.Setenv("TEST_APP_PASSWORD", "secret123")
	defer os.Unsetenv("TEST_APP_PASSWORD")

	yaml := `
azure:
  app_id: "test-app-id"
  app_password: "${TEST_APP_PASSWORD}"
  tenant_id: "test-tenant"

identity: "/tmp/test-identity.json"
cf_home: "` + dir + `"
bridge_db: "` + dir + `/bridge.db"
listen: ":4000"

campfires:
  - id: "abc123"
    teams_channel: "19:test@thread.tacv2"
    poll_interval: 3s
    urgent_poll_interval: 500ms
    urgent_tags: ["blocker", "gate"]
    webhook_url: "https://example.com/webhook"

  - id: "def456"
    teams_channel: "19:other@thread.tacv2"

acl:
  - teams_user_id: "29:baron"
    display_name: "Baron"
    campfires: ["*"]

identities:
  - pubkey: "8205ae"
    display_name: "Atlas (CEO)"
    role: "ceo"
    color: "warning"

urgent_campfires:
  - "abc123"
`

	if err := os.WriteFile(cfgPath, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	// Env var interpolation
	if cfg.Azure.AppPassword != "secret123" {
		t.Errorf("app_password = %q, want %q", cfg.Azure.AppPassword, "secret123")
	}

	if cfg.Azure.AppID != "test-app-id" {
		t.Errorf("app_id = %q, want %q", cfg.Azure.AppID, "test-app-id")
	}

	if cfg.Listen != ":4000" {
		t.Errorf("listen = %q, want %q", cfg.Listen, ":4000")
	}

	// Campfire routes
	if len(cfg.Campfire) != 2 {
		t.Fatalf("campfires count = %d, want 2", len(cfg.Campfire))
	}

	c0 := cfg.Campfire[0]
	if c0.PollInterval != 3*time.Second {
		t.Errorf("poll_interval = %v, want 3s", c0.PollInterval)
	}
	if c0.UrgentPollInterval != 500*time.Millisecond {
		t.Errorf("urgent_poll_interval = %v, want 500ms", c0.UrgentPollInterval)
	}
	if c0.WebhookURL != "https://example.com/webhook" {
		t.Errorf("webhook_url = %q", c0.WebhookURL)
	}

	// Defaults applied to second campfire
	c1 := cfg.Campfire[1]
	if c1.PollInterval != 5*time.Second {
		t.Errorf("default poll_interval = %v, want 5s", c1.PollInterval)
	}
	if c1.UrgentPollInterval != 1*time.Second {
		t.Errorf("default urgent_poll_interval = %v, want 1s", c1.UrgentPollInterval)
	}

	// ACL
	if len(cfg.ACL) != 1 || cfg.ACL[0].DisplayName != "Baron" {
		t.Errorf("acl = %+v", cfg.ACL)
	}

	// Identities
	if len(cfg.Idents) != 1 || cfg.Idents[0].Role != "ceo" {
		t.Errorf("identities = %+v", cfg.Idents)
	}

	// Urgent campfires
	if len(cfg.Urgent) != 1 || cfg.Urgent[0] != "abc123" {
		t.Errorf("urgent_campfires = %v", cfg.Urgent)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "minimal.yaml")

	yaml := `
azure:
  app_id: "x"
identity: "/tmp/id.json"
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if cfg.Listen != ":3978" {
		t.Errorf("default listen = %q, want :3978", cfg.Listen)
	}
	if cfg.BridgeDB == "" {
		t.Error("bridge_db should have a default")
	}
}
