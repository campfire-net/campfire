// Package tests — E2E test for naming locality flow.
//
// TestNamingLocalityE2E exercises the full naming locality flow end-to-end:
// operator creates identity + root campfire + name registration, agent inherits
// config from operator, agent resolves the name via the inherited root.
//
// No mocks — real protocol.Client, real filesystem transport, real naming
// registration and resolution. This test proves the core thesis: a fresh agent
// that inherits operator config can resolve names without manual configuration.
package tests

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/campfire/pkg/protocol"
)

// TestNamingLocalityE2E validates the full naming locality flow:
//
//  1. Operator generates identity and creates a root namespace campfire.
//  2. Operator registers "galtrader" → target campfire in the root.
//  3. Operator writes operator-root.json + join-policy.json (fs-walk, join_root=rootID).
//  4. Agent inherits those config files from the operator home.
//  5. Agent creates its own protocol.Client and resolves "galtrader".
//  6. Resolved campfire ID matches the target campfire ID registered by the operator.
func TestNamingLocalityE2E(t *testing.T) {
	transportDir := t.TempDir()
	beaconDir := t.TempDir()
	ctx := context.Background()

	// -------------------------------------------------------------------------
	// Step 1: Set up operator identity + client
	// -------------------------------------------------------------------------
	operatorHome := t.TempDir()

	// Generate operator identity (required for the operator's protocol.Client).
	operatorID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generate operator identity: %v", err)
	}
	if err := operatorID.Save(filepath.Join(operatorHome, "identity.json")); err != nil {
		t.Fatalf("save operator identity: %v", err)
	}

	operatorClient, err := protocol.Init(operatorHome)
	if err != nil {
		t.Fatalf("operator Init: %v", err)
	}
	t.Cleanup(func() { operatorClient.Close() })

	// -------------------------------------------------------------------------
	// Step 2: Operator creates the root namespace campfire
	// -------------------------------------------------------------------------
	rootResult, err := operatorClient.Create(protocol.CreateRequest{
		Description: "operator root namespace",
		Transport:   protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:   beaconDir,
	})
	if err != nil {
		t.Fatalf("create root campfire: %v", err)
	}
	rootID := rootResult.CampfireID

	// -------------------------------------------------------------------------
	// Step 3: Operator creates the galtrader target campfire
	// -------------------------------------------------------------------------
	targetResult, err := operatorClient.Create(protocol.CreateRequest{
		Description: "galtrader game campfire",
		Transport:   protocol.FilesystemTransport{Dir: transportDir},
		BeaconDir:   beaconDir,
	})
	if err != nil {
		t.Fatalf("create galtrader campfire: %v", err)
	}
	targetID := targetResult.CampfireID

	// -------------------------------------------------------------------------
	// Step 4: Register "galtrader" → targetID in the root namespace
	// -------------------------------------------------------------------------
	if _, err := naming.Register(ctx, operatorClient, rootID, "galtrader", targetID, nil); err != nil {
		t.Fatalf("register galtrader: %v", err)
	}

	// -------------------------------------------------------------------------
	// Step 5: Operator writes operator-root.json and join-policy.json
	// -------------------------------------------------------------------------
	if err := naming.SaveOperatorRoot(operatorHome, &naming.OperatorRoot{
		Name:       "operator",
		CampfireID: rootID,
	}); err != nil {
		t.Fatalf("save operator-root.json: %v", err)
	}

	if err := naming.SaveJoinPolicy(operatorHome, &naming.JoinPolicy{
		JoinPolicy:      "consult",
		ConsultCampfire: naming.FSWalkSentinel,
		JoinRoot:        rootID,
	}); err != nil {
		t.Fatalf("save join-policy.json: %v", err)
	}

	// -------------------------------------------------------------------------
	// Step 6: Create agent home, generate identity, inherit config from operator
	// -------------------------------------------------------------------------
	agentHome := t.TempDir()

	agentID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generate agent identity: %v", err)
	}
	if err := agentID.Save(filepath.Join(agentHome, "identity.json")); err != nil {
		t.Fatalf("save agent identity: %v", err)
	}

	// Inherit config files from operator home (mirrors inheritAgentConfig in init.go).
	for _, filename := range []string{"join-policy.json", "operator-root.json"} {
		src := filepath.Join(operatorHome, filename)
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read %s from operator home: %v", filename, err)
		}
		if err := os.WriteFile(filepath.Join(agentHome, filename), data, 0600); err != nil {
			t.Fatalf("write %s to agent home: %v", filename, err)
		}
	}

	// -------------------------------------------------------------------------
	// Step 7: Agent creates its protocol.Client and loads join policy
	// -------------------------------------------------------------------------
	agentClient, err := protocol.Init(agentHome)
	if err != nil {
		t.Fatalf("agent Init: %v", err)
	}
	t.Cleanup(func() { agentClient.Close() })

	// Load the inherited join policy to discover the join root.
	jp, err := naming.LoadJoinPolicy(agentHome)
	if err != nil {
		t.Fatalf("load join policy: %v", err)
	}
	if jp == nil {
		t.Fatal("agent join policy is nil — inheritance failed")
	}
	if jp.JoinRoot == "" {
		t.Fatal("agent join policy has no join_root")
	}

	// Verify the join root matches the operator's root campfire.
	if jp.JoinRoot != rootID {
		t.Errorf("agent join_root = %s, want %s", jp.JoinRoot[:12], rootID[:12])
	}

	// -------------------------------------------------------------------------
	// Step 8: Agent joins the root campfire (using the transport dir from the beacon)
	// -------------------------------------------------------------------------
	// The agent hasn't joined the root campfire yet. Join it using the known
	// transport dir (which the beacon records). In a real deployment, the agent
	// would discover this via beacon scan; here we use the shared transportDir
	// directly to keep the test self-contained.
	rootCampfireDir := filepath.Join(transportDir, rootID)
	if _, err := agentClient.Join(protocol.JoinRequest{
		CampfireID: rootID,
		Transport:  &protocol.FilesystemTransport{Dir: rootCampfireDir},
	}); err != nil {
		t.Fatalf("agent join root: %v", err)
	}

	// -------------------------------------------------------------------------
	// Step 9: Agent resolves "galtrader" using the inherited root
	// -------------------------------------------------------------------------
	resolver := naming.NewResolverFromClient(agentClient, jp.JoinRoot, naming.ResolverClientOptions{
		BeaconDir: beaconDir,
	})

	result, err := resolver.ResolveURI(ctx, "cf://galtrader")
	if err != nil {
		t.Fatalf("resolve cf://galtrader: %v", err)
	}

	// -------------------------------------------------------------------------
	// Step 10: Assert resolved ID == target campfire ID
	// -------------------------------------------------------------------------
	if result.CampfireID != targetID {
		t.Errorf("resolved campfire ID = %s, want %s", result.CampfireID, targetID)
	}

	// Verify that the operator-root.json in the agent home also points to the right root.
	operatorRoot, err := naming.LoadOperatorRoot(agentHome)
	if err != nil {
		t.Fatalf("load operator-root.json from agent home: %v", err)
	}
	if operatorRoot == nil {
		t.Fatal("agent operator-root.json is nil — inheritance failed")
	}
	if operatorRoot.CampfireID != rootID {
		t.Errorf("operator-root.json campfire_id = %s, want %s", operatorRoot.CampfireID, rootID)
	}
}
