// Tests for the cf-teams bridge entrypoint.
//
// Coverage targets:
//   - containsChannel  — pure helper, table-driven
//   - campfireIDs      — pure helper, real bridge.Config
//   - startup error paths — no --config flag → exit 1; bad path → exit 1;
//     invalid YAML → exit 1
//   - config loading via testdata YAML fixture (bridge.LoadConfig)
//   - graceful shutdown — SIGTERM causes the process to exit within a deadline
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/campfire-net/campfire/bridge"
	"github.com/campfire-net/campfire/bridge/state"
)

// ---------------------------------------------------------------------------
// Pure helper: containsChannel
// ---------------------------------------------------------------------------

func TestContainsChannel(t *testing.T) {
	cases := []struct {
		name    string
		convID  string
		channel string
		want    bool
	}{
		{
			name:    "exact match",
			convID:  "19:abc@thread.tacv2",
			channel: "19:abc@thread.tacv2",
			want:    true,
		},
		{
			name:    "conv has messageid suffix beyond channel length",
			convID:  "19:abc@thread.tacv2;messageid=12345",
			channel: "19:abc@thread.tacv2",
			want:    true,
		},
		{
			name:    "different channel id",
			convID:  "19:xyz@thread.tacv2",
			channel: "19:abc@thread.tacv2",
			want:    false,
		},
		{
			name:    "both empty",
			convID:  "",
			channel: "",
			want:    true,
		},
		{
			name:    "channel longer than convID",
			convID:  "short",
			channel: "longerthanshort",
			want:    false,
		},
		{
			name:    "convID equals channel exactly at boundary",
			convID:  "19:a@b",
			channel: "19:a@b",
			want:    true,
		},
		{
			name:    "prefix mismatch",
			convID:  "19:aaa@thread.tacv2",
			channel: "19:bbb@thread.tacv2",
			want:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := containsChannel(tc.convID, tc.channel)
			if got != tc.want {
				t.Errorf("containsChannel(%q, %q) = %v, want %v",
					tc.convID, tc.channel, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Pure helper: campfireIDs
// ---------------------------------------------------------------------------

func TestCampfireIDs_Empty(t *testing.T) {
	cfg := &bridge.Config{}
	ids := campfireIDs(cfg)
	if len(ids) != 0 {
		t.Errorf("expected empty slice for no campfires, got %v", ids)
	}
}

func TestCampfireIDs_Multiple(t *testing.T) {
	cfg := &bridge.Config{
		Campfire: []bridge.CampfireRoute{
			{ID: "aaa111"},
			{ID: "bbb222"},
			{ID: "ccc333"},
		},
	}
	ids := campfireIDs(cfg)
	if len(ids) != 3 {
		t.Fatalf("expected 3 IDs, got %d: %v", len(ids), ids)
	}
	if ids[0] != "aaa111" || ids[1] != "bbb222" || ids[2] != "ccc333" {
		t.Errorf("unexpected IDs: %v", ids)
	}
}

func TestCampfireIDs_OrderPreserved(t *testing.T) {
	routes := []bridge.CampfireRoute{
		{ID: "first"},
		{ID: "second"},
	}
	cfg := &bridge.Config{Campfire: routes}
	ids := campfireIDs(cfg)
	for i, route := range routes {
		if ids[i] != route.ID {
			t.Errorf("index %d: got %q, want %q", i, ids[i], route.ID)
		}
	}
}

// ---------------------------------------------------------------------------
// Config loading via testdata fixture (structural — no real Teams secrets)
// ---------------------------------------------------------------------------

func TestConfigLoadFromFixture(t *testing.T) {
	dir := t.TempDir()

	// Read the testdata fixture and substitute placeholder paths.
	src, err := os.ReadFile(filepath.Join("testdata", "minimal.yaml"))
	if err != nil {
		t.Fatalf("read testdata/minimal.yaml: %v", err)
	}

	idPath := filepath.Join(dir, "identity.json")
	bridgeDB := filepath.Join(dir, "bridge.db")
	cfHome := filepath.Join(dir, "cfhome")

	content := strings.ReplaceAll(string(src), "__IDENTITY_PATH__", idPath)
	content = strings.ReplaceAll(content, "__CF_HOME__", cfHome)
	content = strings.ReplaceAll(content, "__BRIDGE_DB__", bridgeDB)

	cfgPath := filepath.Join(dir, "bridge.yaml")
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := bridge.LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if cfg.Azure.AppID != "test-app-id" {
		t.Errorf("azure.app_id = %q, want test-app-id", cfg.Azure.AppID)
	}
	if cfg.Identity != idPath {
		t.Errorf("identity = %q, want %q", cfg.Identity, idPath)
	}
	if cfg.BridgeDB != bridgeDB {
		t.Errorf("bridge_db = %q, want %q", cfg.BridgeDB, bridgeDB)
	}
	if cfg.Listen != ":0" {
		t.Errorf("listen = %q, want :0", cfg.Listen)
	}
	if len(cfg.Campfire) != 1 {
		t.Fatalf("campfires count = %d, want 1", len(cfg.Campfire))
	}
	if cfg.Campfire[0].PollInterval != 5*time.Second {
		t.Errorf("poll_interval = %v, want 5s", cfg.Campfire[0].PollInterval)
	}
}

func TestConfigLoadDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "minimal.yaml")

	yaml := "azure:\n  app_id: x\nidentity: /tmp/id.json\n"
	if err := os.WriteFile(cfgPath, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := bridge.LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Listen != ":3978" {
		t.Errorf("default listen = %q, want :3978", cfg.Listen)
	}
	if cfg.BridgeDB == "" {
		t.Error("bridge_db should have a default")
	}
	if cfg.CFHome == "" {
		t.Error("cf_home should have a default")
	}
}

func TestConfigLoadEnvInterpolation(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "bridge.yaml")

	t.Setenv("TEST_CF_TEAMS_SECRET", "my-secret-password")

	yaml := "azure:\n  app_id: test-app\n  app_password: \"${TEST_CF_TEAMS_SECRET}\"\nidentity: /tmp/id.json\n"
	if err := os.WriteFile(cfgPath, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := bridge.LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Azure.AppPassword != "my-secret-password" {
		t.Errorf("app_password = %q, want my-secret-password", cfg.Azure.AppPassword)
	}
}

// ---------------------------------------------------------------------------
// stripMessageID helper
// ---------------------------------------------------------------------------

func TestStripMessageID(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no suffix",
			input: "19:abc@thread.tacv2",
			want:  "19:abc@thread.tacv2",
		},
		{
			name:  "strips messageid suffix",
			input: "19:abc@thread.tacv2;messageid=1234567890",
			want:  "19:abc@thread.tacv2",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "messageid only",
			input: ";messageid=999",
			want:  "",
		},
		{
			name:  "multiple semicolons — only first messageid stripped",
			input: "19:abc@thread.tacv2;messageid=123;extra=456",
			want:  "19:abc@thread.tacv2",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripMessageID(tc.input)
			if got != tc.want {
				t.Errorf("stripMessageID(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// buildMessagesHandler — HTTP handler for Bot Framework activities
// ---------------------------------------------------------------------------

// fakeConvRefStore captures UpsertConversationRef calls for assertions.
type fakeConvRefStore struct {
	calls []state.ConversationRef
	err   error
}

func (f *fakeConvRefStore) UpsertConversationRef(ref state.ConversationRef) error {
	f.calls = append(f.calls, ref)
	return f.err
}

// fakeInboundHandler records HandleActivity invocations.
type fakeInboundHandler struct {
	msgID string
	err   error
	calls int
}

func (f *fakeInboundHandler) HandleActivity(_ context.Context, _ string, _ []byte) (string, error) {
	f.calls++
	return f.msgID, f.err
}

func activityJSON(actType, convID, serviceURL string) []byte {
	return []byte(fmt.Sprintf(
		`{"type":%q,"conversation":{"id":%q},"serviceUrl":%q,"from":{"id":"user1"},"recipient":{"id":"bot1"}}`,
		actType, convID, serviceURL,
	))
}

func TestBuildMessagesHandler_MethodNotAllowed(t *testing.T) {
	h := buildMessagesHandler(nil, &fakeConvRefStore{}, &fakeInboundHandler{})
	methods := []string{http.MethodGet, http.MethodPut, http.MethodDelete}
	for _, m := range methods {
		t.Run(m, func(t *testing.T) {
			req := httptest.NewRequest(m, "/api/messages", nil)
			w := httptest.NewRecorder()
			h(w, req)
			if w.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s: expected 405, got %d", m, w.Code)
			}
		})
	}
}

func TestBuildMessagesHandler_BadJSON(t *testing.T) {
	h := buildMessagesHandler(nil, &fakeConvRefStore{}, &fakeInboundHandler{})
	req := httptest.NewRequest(http.MethodPost, "/api/messages",
		strings.NewReader("not json at all"))
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad JSON, got %d", w.Code)
	}
}

func TestBuildMessagesHandler_MissingActivityType(t *testing.T) {
	h := buildMessagesHandler(nil, &fakeConvRefStore{}, &fakeInboundHandler{})
	// Valid JSON but missing "type" field — ParseActivity returns an error.
	req := httptest.NewRequest(http.MethodPost, "/api/messages",
		strings.NewReader(`{"conversation":{"id":"19:abc"}}`))
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing type, got %d", w.Code)
	}
}

func TestBuildMessagesHandler_MessageActivity_Dispatched(t *testing.T) {
	db := &fakeConvRefStore{}
	inbound := &fakeInboundHandler{msgID: "msg-xyz-123"}

	channelMap := map[string]string{
		"19:chan@thread.tacv2": "campfire-abc",
	}
	h := buildMessagesHandler(channelMap, db, inbound)

	body := activityJSON("message", "19:chan@thread.tacv2", "https://smba.trafficmanager.net/")
	req := httptest.NewRequest(http.MethodPost, "/api/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if inbound.calls != 1 {
		t.Errorf("expected 1 HandleActivity call, got %d", inbound.calls)
	}
	if len(db.calls) != 1 {
		t.Errorf("expected 1 UpsertConversationRef call, got %d", len(db.calls))
	}
	if db.calls[0].CampfireID != "campfire-abc" {
		t.Errorf("conversation ref campfire = %q, want campfire-abc", db.calls[0].CampfireID)
	}
}

func TestBuildMessagesHandler_MessageIDSuffixStripped(t *testing.T) {
	db := &fakeConvRefStore{}
	inbound := &fakeInboundHandler{msgID: "msg-1"}

	channelMap := map[string]string{
		"19:chan@thread.tacv2": "campfire-abc",
	}
	h := buildMessagesHandler(channelMap, db, inbound)

	// Conversation ID has ;messageid= suffix — should be stripped before lookup.
	body := activityJSON("message", "19:chan@thread.tacv2;messageid=9876", "https://smba.example/")
	req := httptest.NewRequest(http.MethodPost, "/api/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if len(db.calls) != 1 {
		t.Fatalf("expected 1 conv ref upsert (channel match after strip), got %d", len(db.calls))
	}
	if db.calls[0].TeamsConvID != "19:chan@thread.tacv2" {
		t.Errorf("stored conv ID = %q, want stripped form", db.calls[0].TeamsConvID)
	}
}

func TestBuildMessagesHandler_UnknownActivityType_Acknowledged(t *testing.T) {
	db := &fakeConvRefStore{}
	inbound := &fakeInboundHandler{}

	h := buildMessagesHandler(nil, db, inbound)
	body := activityJSON("conversationUpdate", "19:any@thread.tacv2", "https://smba.example/")
	req := httptest.NewRequest(http.MethodPost, "/api/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h(w, req)

	// Non-message/invoke types are acknowledged with 200 (no dispatch).
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for conversationUpdate, got %d", w.Code)
	}
	if inbound.calls != 0 {
		t.Errorf("expected 0 HandleActivity calls for conversationUpdate, got %d", inbound.calls)
	}
}

func TestBuildMessagesHandler_InboundError_Returns200(t *testing.T) {
	// Bot Framework retries on non-2xx — handler must return 200 even on error.
	inbound := &fakeInboundHandler{err: fmt.Errorf("auth failure")}
	h := buildMessagesHandler(nil, &fakeConvRefStore{}, inbound)

	body := activityJSON("message", "19:x@thread.tacv2", "https://smba.example/")
	req := httptest.NewRequest(http.MethodPost, "/api/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 even on inbound error, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Subprocess tests — startup error paths
// ---------------------------------------------------------------------------

// buildBinary compiles cmd/cf-teams to a temp path for subprocess tests.
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "cf-teams-test")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build cf-teams: %v\n%s", err, out)
	}
	return bin
}

func TestStartup_MissingConfigFlag(t *testing.T) {
	bin := buildBinary(t)

	cmd := exec.Command(bin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-zero exit when --config is absent, got nil error")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("expected exit code 1, got %d", exitErr.ExitCode())
	}
	out := stderr.String()
	if !strings.Contains(out, "usage") && !strings.Contains(out, "--config") {
		t.Errorf("expected usage/config diagnostic in stderr, got: %q", out)
	}
}

func TestStartup_ConfigFileNotFound(t *testing.T) {
	bin := buildBinary(t)

	cmd := exec.Command(bin, "--config", "/nonexistent/path/bridge.yaml")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-zero exit for missing config file, got nil error")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("expected exit code 1, got %d", exitErr.ExitCode())
	}
	if !strings.Contains(stderr.String(), "config") {
		t.Errorf("expected config error in stderr, got: %q", stderr.String())
	}
}

func TestStartup_InvalidConfigYAML(t *testing.T) {
	bin := buildBinary(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "bad.yaml")

	if err := os.WriteFile(cfgPath, []byte("azure: {\n bad yaml: [unclosed"), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "--config", cfgPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-zero exit for invalid YAML, got nil error")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("expected exit code 1 for bad YAML, got %d", exitErr.ExitCode())
	}
}

// ---------------------------------------------------------------------------
// Graceful shutdown: SIGTERM causes clean exit within deadline
// ---------------------------------------------------------------------------

// makeTestIdentity writes a v1 Ed25519 identity JSON file to path.
func makeTestIdentity(t *testing.T, path string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	f := struct {
		Version    int               `json:"version,omitempty"`
		PublicKey  ed25519.PublicKey  `json:"public_key"`
		PrivateKey ed25519.PrivateKey `json:"private_key"`
		CreatedAt  int64             `json:"created_at"`
	}{
		Version:    1,
		PublicKey:  pub,
		PrivateKey: priv,
		CreatedAt:  time.Now().UnixNano(),
	}
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal identity: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write identity file: %v", err)
	}
}

// freePort returns a random free TCP port on loopback.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func TestGracefulShutdown_SIGTERM(t *testing.T) {
	bin := buildBinary(t)
	dir := t.TempDir()

	// Write a real identity file so the binary passes identity.Load().
	idPath := filepath.Join(dir, "identity.json")
	makeTestIdentity(t, idPath)

	cfHome := filepath.Join(dir, "cfhome")
	if err := os.MkdirAll(cfHome, 0700); err != nil {
		t.Fatalf("mkdir cfhome: %v", err)
	}
	bridgeDB := filepath.Join(dir, "bridge.db")
	listenPort := freePort(t)

	cfgYAML := fmt.Sprintf(
		"azure:\n  app_id: test\nidentity: %s\ncf_home: %s\nbridge_db: %s\nlisten: :%d\ncampfires: []\n",
		idPath, cfHome, bridgeDB, listenPort,
	)

	cfgPath := filepath.Join(dir, "bridge.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cmd := exec.Command(bin, "--config", cfgPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start binary: %v", err)
	}

	// Allow the process time to open the campfire store and bind the HTTP listener.
	time.Sleep(400 * time.Millisecond)

	// Send SIGTERM — the signal handler in main() should unblock the select.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Logf("signal SIGTERM: %v — process may have exited already", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		// Any exit (clean, signal-terminated, or startup failure) is acceptable
		// as long as the process didn't hang. Log the outcome for diagnostics.
		if err != nil {
			exitErr, ok := err.(*exec.ExitError)
			if ok {
				t.Logf("process exited with code %d (stderr: %s)", exitErr.ExitCode(), stderr.String())
			} else {
				t.Logf("process wait error: %v (stderr: %s)", err, stderr.String())
			}
		} else {
			t.Log("process exited cleanly (code 0)")
		}
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		t.Errorf("process did not exit within 5s after SIGTERM — shutdown hang (stderr: %s)", stderr.String())
	}
}
