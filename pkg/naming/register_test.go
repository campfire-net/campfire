package naming_test

import (
	"context"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/campfire/pkg/protocol"
)

// setupRegisterTestCampfire creates two campfires: one for the nameserver,
// and one as a registration target. Returns client, nameserver campfire ID,
// and target campfire ID.
func setupRegisterTestCampfire(t *testing.T) (*protocol.Client, string, string) {
	t.Helper()

	configDir := t.TempDir()
	client, err := protocol.Init(configDir)
	if err != nil {
		t.Fatalf("protocol.Init: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	// Create nameserver campfire.
	ns, err := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: t.TempDir()},
		BeaconDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Create nameserver: %v", err)
	}

	// Create target campfire (the thing being named).
	target, err := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: t.TempDir()},
		BeaconDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Create target: %v", err)
	}

	return client, ns.CampfireID, target.CampfireID
}

func TestRegisterAndResolve(t *testing.T) {
	client, nsID, targetID := setupRegisterTestCampfire(t)
	ctx := context.Background()

	// Register a name.
	msg, err := naming.Register(ctx, client, nsID, "myservice", targetID, nil)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if msg == nil {
		t.Fatal("Register returned nil message")
	}

	// Resolve it.
	resp, err := naming.Resolve(ctx, client, nsID, "myservice")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if resp.CampfireID != targetID {
		t.Errorf("CampfireID = %s, want %s", resp.CampfireID, targetID)
	}
	if resp.Name != "myservice" {
		t.Errorf("Name = %q, want %q", resp.Name, "myservice")
	}
	if resp.TTL != naming.DefaultTTL {
		t.Errorf("TTL = %d, want %d", resp.TTL, naming.DefaultTTL)
	}
	if resp.RegistrationMsgID == "" {
		t.Error("RegistrationMsgID is empty")
	}
}

func TestRegisterWithCustomTTL(t *testing.T) {
	client, nsID, targetID := setupRegisterTestCampfire(t)
	ctx := context.Background()

	_, err := naming.Register(ctx, client, nsID, "svc", targetID, &naming.RegisterOptions{TTL: 120})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	resp, err := naming.Resolve(ctx, client, nsID, "svc")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resp.TTL != 120 {
		t.Errorf("TTL = %d, want 120", resp.TTL)
	}
}

func TestRegisterTTLCapped(t *testing.T) {
	client, nsID, targetID := setupRegisterTestCampfire(t)
	ctx := context.Background()

	_, err := naming.Register(ctx, client, nsID, "svc", targetID, &naming.RegisterOptions{TTL: 999999})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	resp, err := naming.Resolve(ctx, client, nsID, "svc")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resp.TTL != naming.MaxTTL {
		t.Errorf("TTL = %d, want MaxTTL %d", resp.TTL, naming.MaxTTL)
	}
}

func TestUnregister(t *testing.T) {
	client, nsID, targetID := setupRegisterTestCampfire(t)
	ctx := context.Background()

	// Register.
	_, err := naming.Register(ctx, client, nsID, "ephemeral", targetID, nil)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Verify it resolves.
	_, err = naming.Resolve(ctx, client, nsID, "ephemeral")
	if err != nil {
		t.Fatalf("Resolve before unregister: %v", err)
	}

	// Unregister.
	if err := naming.Unregister(ctx, client, nsID, "ephemeral"); err != nil {
		t.Fatalf("Unregister: %v", err)
	}

	// Verify it no longer resolves.
	_, err = naming.Resolve(ctx, client, nsID, "ephemeral")
	if err == nil {
		t.Fatal("Resolve after unregister should fail, got nil error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestReReregisterAfterUnregister(t *testing.T) {
	client, nsID, targetID := setupRegisterTestCampfire(t)
	ctx := context.Background()

	// Register, unregister, re-register.
	_, err := naming.Register(ctx, client, nsID, "bounce", targetID, nil)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := naming.Unregister(ctx, client, nsID, "bounce"); err != nil {
		t.Fatalf("Unregister: %v", err)
	}

	// Create a different target for re-registration.
	target2, err := client.Create(protocol.CreateRequest{
		Transport: protocol.FilesystemTransport{Dir: t.TempDir()},
		BeaconDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Create target2: %v", err)
	}

	_, err = naming.Register(ctx, client, nsID, "bounce", target2.CampfireID, nil)
	if err != nil {
		t.Fatalf("Re-register: %v", err)
	}

	resp, err := naming.Resolve(ctx, client, nsID, "bounce")
	if err != nil {
		t.Fatalf("Resolve after re-register: %v", err)
	}
	if resp.CampfireID != target2.CampfireID {
		t.Errorf("CampfireID = %s, want %s", resp.CampfireID, target2.CampfireID)
	}
}

func TestList(t *testing.T) {
	client, nsID, _ := setupRegisterTestCampfire(t)
	ctx := context.Background()

	// Create multiple targets and register them.
	targets := make([]string, 3)
	names := []string{"alpha", "beta", "gamma"}
	for i, name := range names {
		result, err := client.Create(protocol.CreateRequest{
			Transport: protocol.FilesystemTransport{Dir: t.TempDir()},
			BeaconDir: t.TempDir(),
		})
		if err != nil {
			t.Fatalf("Create target %d: %v", i, err)
		}
		targets[i] = result.CampfireID

		_, err = naming.Register(ctx, client, nsID, name, targets[i], nil)
		if err != nil {
			t.Fatalf("Register %s: %v", name, err)
		}
	}

	regs, err := naming.List(ctx, client, nsID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(regs) != 3 {
		t.Fatalf("List returned %d registrations, want 3", len(regs))
	}

	// Build a map for easier assertions.
	byName := make(map[string]naming.Registration)
	for _, r := range regs {
		byName[r.Name] = r
	}

	for i, name := range names {
		reg, ok := byName[name]
		if !ok {
			t.Errorf("name %q not found in List", name)
			continue
		}
		if reg.CampfireID != targets[i] {
			t.Errorf("%s CampfireID = %s, want %s", name, reg.CampfireID, targets[i])
		}
		if reg.MessageID == "" {
			t.Errorf("%s MessageID is empty", name)
		}
		if reg.Timestamp == 0 {
			t.Errorf("%s Timestamp is 0", name)
		}
	}
}

func TestListExcludesUnregistered(t *testing.T) {
	client, nsID, _ := setupRegisterTestCampfire(t)
	ctx := context.Background()

	// Register two names.
	for _, name := range []string{"keep", "remove"} {
		target, err := client.Create(protocol.CreateRequest{
			Transport: protocol.FilesystemTransport{Dir: t.TempDir()},
			BeaconDir: t.TempDir(),
		})
		if err != nil {
			t.Fatalf("Create target: %v", err)
		}
		_, err = naming.Register(ctx, client, nsID, name, target.CampfireID, nil)
		if err != nil {
			t.Fatalf("Register %s: %v", name, err)
		}
	}

	// Unregister one.
	if err := naming.Unregister(ctx, client, nsID, "remove"); err != nil {
		t.Fatalf("Unregister: %v", err)
	}

	regs, err := naming.List(ctx, client, nsID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(regs) != 1 {
		t.Fatalf("List returned %d, want 1", len(regs))
	}
	if regs[0].Name != "keep" {
		t.Errorf("Name = %q, want %q", regs[0].Name, "keep")
	}
}

func TestResolveNotFound(t *testing.T) {
	client, nsID, _ := setupRegisterTestCampfire(t)
	ctx := context.Background()

	_, err := naming.Resolve(ctx, client, nsID, "nonexistent")
	if err == nil {
		t.Fatal("Resolve should fail for non-existent name")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestListEmpty(t *testing.T) {
	client, nsID, _ := setupRegisterTestCampfire(t)
	ctx := context.Background()

	regs, err := naming.List(ctx, client, nsID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(regs) != 0 {
		t.Errorf("List returned %d, want 0", len(regs))
	}
}
