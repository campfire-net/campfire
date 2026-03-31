package cmd

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
)

// setupNameTestEnv creates a temp CF_HOME with identity, store, and operator root campfire.
// Returns the cfHome dir, a protocol client, and the operator root campfire ID.
func setupNameTestEnv(t *testing.T) (dir string, client *protocol.Client, rootCampfireID string) {
	t.Helper()

	dir = t.TempDir()
	t.Setenv("CF_HOME", dir)
	t.Cleanup(func() { cfHome = "" })
	cfHome = ""

	// Generate and save identity.
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating identity: %v", err)
	}
	if err := id.Save(filepath.Join(dir, "identity.json")); err != nil {
		t.Fatalf("saving identity: %v", err)
	}

	// Open store.
	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Create operator root campfire.
	rootID, err := ensureOperatorRoot("testop", id.PublicKey, s)
	if err != nil {
		t.Fatalf("ensureOperatorRoot: %v", err)
	}

	// Save operator-root.json so the name commands can find it.
	root := &naming.OperatorRoot{Name: "testop", CampfireID: rootID}
	if err := naming.SaveOperatorRoot(dir, root); err != nil {
		t.Fatalf("saving operator root: %v", err)
	}

	// Open protocol client for direct assertions.
	c, err := protocol.Init(dir)
	if err != nil {
		t.Fatalf("protocol.Init: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	return dir, c, rootID
}

func TestNameRegisterAndResolve(t *testing.T) {
	_, client, rootID := setupNameTestEnv(t)

	// Create a target campfire.
	target, err := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: t.TempDir()},
		BeaconDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("creating target campfire: %v", err)
	}

	// Run register via RunE directly.
	var out bytes.Buffer
	nameRegisterCmd.SetOut(&out)
	if err := nameRegisterCmd.RunE(nameRegisterCmd, []string{"galtrader", target.CampfireID}); err != nil {
		t.Fatalf("name register: %v", err)
	}

	if !strings.Contains(out.String(), "galtrader") {
		t.Errorf("expected output to mention 'galtrader', got: %s", out.String())
	}

	// Verify via direct resolve.
	resp, err := naming.Resolve(context.Background(), client, rootID, "galtrader")
	if err != nil {
		t.Fatalf("Resolve after register: %v", err)
	}
	if resp.CampfireID != target.CampfireID {
		t.Errorf("resolved ID = %s, want %s", resp.CampfireID, target.CampfireID)
	}
}

func TestNameUnregister(t *testing.T) {
	_, client, rootID := setupNameTestEnv(t)

	// Create a target and register it directly.
	target, err := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: t.TempDir()},
		BeaconDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("creating target campfire: %v", err)
	}

	if _, err := naming.Register(context.Background(), client, rootID, "ephemeral", target.CampfireID, nil); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Unregister via RunE.
	var out bytes.Buffer
	nameUnregisterCmd.SetOut(&out)
	if err := nameUnregisterCmd.RunE(nameUnregisterCmd, []string{"ephemeral"}); err != nil {
		t.Fatalf("name unregister: %v", err)
	}

	if !strings.Contains(out.String(), "ephemeral") {
		t.Errorf("expected output to mention 'ephemeral', got: %s", out.String())
	}

	// Verify it no longer resolves.
	if _, err := naming.Resolve(context.Background(), client, rootID, "ephemeral"); err == nil {
		t.Fatal("Resolve should fail after unregister")
	}
}

func TestNameList(t *testing.T) {
	_, client, rootID := setupNameTestEnv(t)

	// Register a couple of names directly.
	for _, name := range []string{"alpha", "beta"} {
		target, err := client.Create(protocol.CreateRequest{
			Transport: protocol.FilesystemTransport{Dir: t.TempDir()},
			BeaconDir: t.TempDir(),
		})
		if err != nil {
			t.Fatalf("creating target: %v", err)
		}
		if _, err := naming.Register(context.Background(), client, rootID, name, target.CampfireID, nil); err != nil {
			t.Fatalf("Register %s: %v", name, err)
		}
	}

	var out bytes.Buffer
	nameListCmd.SetOut(&out)
	if err := nameListCmd.RunE(nameListCmd, []string{}); err != nil {
		t.Fatalf("name list: %v", err)
	}

	result := out.String()
	if !strings.Contains(result, "alpha") {
		t.Errorf("expected 'alpha' in list output, got: %s", result)
	}
	if !strings.Contains(result, "beta") {
		t.Errorf("expected 'beta' in list output, got: %s", result)
	}
}

func TestNameLookup(t *testing.T) {
	_, client, rootID := setupNameTestEnv(t)

	// Register a name.
	target, err := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: t.TempDir()},
		BeaconDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("creating target: %v", err)
	}

	if _, err := naming.Register(context.Background(), client, rootID, "myservice", target.CampfireID, nil); err != nil {
		t.Fatalf("Register: %v", err)
	}

	var out bytes.Buffer
	nameLookupCmd.SetOut(&out)
	if err := nameLookupCmd.RunE(nameLookupCmd, []string{"myservice"}); err != nil {
		t.Fatalf("name lookup: %v", err)
	}

	result := out.String()
	if !strings.Contains(result, "Resolved") {
		t.Errorf("expected 'Resolved' in lookup output, got: %s", result)
	}
	if !strings.Contains(result, "myservice") {
		t.Errorf("expected 'myservice' in lookup output, got: %s", result)
	}
}

func TestNameLookupNotFound(t *testing.T) {
	setupNameTestEnv(t)

	var out bytes.Buffer
	nameLookupCmd.SetOut(&out)
	if err := nameLookupCmd.RunE(nameLookupCmd, []string{"nonexistent-name-xyz"}); err != nil {
		t.Fatalf("name lookup returned unexpected error: %v", err)
	}

	if !strings.Contains(out.String(), "not found") {
		t.Errorf("expected 'not found' in output, got: %s", out.String())
	}
}

// TestNameResolveRootPublicWithoutRegistry verifies that --public flag without
// CF_ROOT_REGISTRY set returns a clear error from nameResolveRoot.
func TestNameResolveRootPublicWithoutRegistry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CF_HOME", dir)
	t.Cleanup(func() { cfHome = "" })
	cfHome = ""

	// Ensure CF_ROOT_REGISTRY is not set.
	t.Setenv("CF_ROOT_REGISTRY", "")

	if err := nameListCmd.Flags().Set("public", "true"); err != nil {
		t.Fatalf("setting --public flag: %v", err)
	}
	t.Cleanup(func() { nameListCmd.Flags().Set("public", "false") }) //nolint:errcheck

	err := nameListCmd.RunE(nameListCmd, []string{})
	if err == nil {
		t.Fatal("expected error when --public is set and CF_ROOT_REGISTRY is not set, got nil")
	}
	if !strings.Contains(err.Error(), "CF_ROOT_REGISTRY") {
		t.Errorf("expected error to mention CF_ROOT_REGISTRY, got: %v", err)
	}
}

// TestNameResolveRootNoOperatorRoot verifies that when no operator root is configured
// and --public/--root flags are not set, nameResolveRoot returns a useful error.
func TestNameResolveRootNoOperatorRoot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CF_HOME", dir)
	t.Cleanup(func() { cfHome = "" })
	cfHome = ""

	// Ensure CF_ROOT_REGISTRY is not set.
	t.Setenv("CF_ROOT_REGISTRY", "")

	// No operator-root.json created — no operator root configured.
	// No --public or --root flags set.

	err := nameListCmd.RunE(nameListCmd, []string{})
	if err == nil {
		t.Fatal("expected error when no operator root configured, got nil")
	}
	if !strings.Contains(err.Error(), "no operator root configured") {
		t.Errorf("expected 'no operator root configured' in error, got: %v", err)
	}
}
