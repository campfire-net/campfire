package main

// delivery_modes_test.go — Integration tests for delivery_modes on campfire_create.
//
// Bead: campfire-agent-kiv
//
// Scenarios:
//   1. campfire_create with delivery_modes=["pull","push"] stores modes in campfire.cbor
//   2. campfire_create with no delivery_modes defaults to ["pull"]
//   3. campfire_create with invalid delivery_mode returns -32602 error
//   4. campfire_create with delivery_modes=["push"] stores single mode
//   5. Legacy campfire.cbor (no field 9) reads back with EffectiveDeliveryModes = ["pull"]

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

// readTestCampfireStateFromDir reads the on-disk CampfireState from a specific
// transport dir that was returned in the campfire_create response.
// The transport dir from the response is the direct campfire directory (path-rooted),
// so we use fs.NewPathRooted to read the state.
func readTestCampfireStateFromDir(t *testing.T, transportDir string) *campfire.CampfireState {
	t.Helper()
	transport := fs.NewPathRooted(transportDir)
	state, err := transport.ReadState("")
	if err != nil {
		t.Fatalf("ReadState from %s: %v", transportDir, err)
	}
	return state
}

// extractDeliveryModes extracts delivery_modes from a campfire_create result map.
func extractDeliveryModes(fields map[string]interface{}) []string {
	raw, ok := fields["delivery_modes"]
	if !ok {
		return nil
	}
	arr, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// TestDeliveryModes_PullAndPushStored verifies that campfire_create with
// delivery_modes=["pull","push"] stores both modes in campfire.cbor and reports them.
func TestDeliveryModes_PullAndPushStored(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	resp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{"delivery_modes":["pull","push"]}}`))
	fields := extractCreateResult(t, resp)

	campfireID, _ := fields["campfire_id"].(string)
	if campfireID == "" {
		t.Fatal("missing campfire_id")
	}
	transportDir, _ := fields["transport_dir"].(string)
	if transportDir == "" {
		t.Fatal("missing transport_dir in campfire_create response")
	}

	// Verify response includes delivery_modes = ["pull","push"].
	modes := extractDeliveryModes(fields)
	if len(modes) != 2 {
		t.Fatalf("response delivery_modes = %v, want [pull push]", modes)
	}
	if modes[0] != "pull" || modes[1] != "push" {
		t.Errorf("response delivery_modes = %v, want [pull push]", modes)
	}

	// Verify on-disk state has both modes.
	state := readTestCampfireStateFromDir(t, transportDir)
	if len(state.DeliveryModes) != 2 {
		t.Fatalf("campfire.cbor DeliveryModes = %v, want [pull push]", state.DeliveryModes)
	}
	if state.DeliveryModes[0] != "pull" || state.DeliveryModes[1] != "push" {
		t.Errorf("campfire.cbor DeliveryModes = %v, want [pull push]", state.DeliveryModes)
	}
}

// TestDeliveryModes_DefaultIsPull verifies that campfire_create with no
// delivery_modes parameter defaults to ["pull"] in the response and on disk.
func TestDeliveryModes_DefaultIsPull(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	resp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	fields := extractCreateResult(t, resp)

	campfireID, _ := fields["campfire_id"].(string)
	if campfireID == "" {
		t.Fatal("missing campfire_id")
	}
	transportDir, _ := fields["transport_dir"].(string)
	if transportDir == "" {
		t.Fatal("missing transport_dir in campfire_create response")
	}

	// Response must include delivery_modes = ["pull"].
	modes := extractDeliveryModes(fields)
	if len(modes) != 1 || modes[0] != "pull" {
		t.Errorf("response delivery_modes = %v, want [pull]", modes)
	}

	// On-disk: EffectiveDeliveryModes must be ["pull"].
	state := readTestCampfireStateFromDir(t, transportDir)
	effective := campfire.EffectiveDeliveryModes(state.DeliveryModes)
	if len(effective) != 1 || effective[0] != campfire.DeliveryModePull {
		t.Errorf("campfire.cbor EffectiveDeliveryModes = %v, want [pull]", effective)
	}
}

// TestDeliveryModes_InvalidModeRejected verifies that campfire_create with an
// unrecognized delivery_mode returns a -32602 error (invalid params).
func TestDeliveryModes_InvalidModeRejected(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	resp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{"delivery_modes":["webhook"]}}`))
	if resp.Error == nil {
		t.Fatal("expected error response for invalid delivery_mode, got success")
	}
	if resp.Error.Code != -32602 {
		t.Errorf("error code = %d, want -32602", resp.Error.Code)
	}
}

// TestDeliveryModes_PushOnlyStored verifies campfire_create with delivery_modes=["push"]
// stores exactly ["push"] on disk and reports it in the response.
func TestDeliveryModes_PushOnlyStored(t *testing.T) {
	srv, _ := newTestServerWithStore(t)
	doInit(t, srv)

	resp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{"delivery_modes":["push"]}}`))
	fields := extractCreateResult(t, resp)

	campfireID, _ := fields["campfire_id"].(string)
	if campfireID == "" {
		t.Fatal("missing campfire_id")
	}
	transportDir, _ := fields["transport_dir"].(string)
	if transportDir == "" {
		t.Fatal("missing transport_dir in campfire_create response")
	}

	modes := extractDeliveryModes(fields)
	if len(modes) != 1 || modes[0] != "push" {
		t.Errorf("response delivery_modes = %v, want [push]", modes)
	}

	state := readTestCampfireStateFromDir(t, transportDir)
	if len(state.DeliveryModes) != 1 || state.DeliveryModes[0] != "push" {
		t.Errorf("campfire.cbor DeliveryModes = %v, want [push]", state.DeliveryModes)
	}
}

// TestDeliveryModes_CBORBackwardCompat verifies that a campfire.cbor written
// without field 9 (legacy format) reads back with EffectiveDeliveryModes = ["pull"].
// This exercises the fs.ReadState backward compat path directly via the fs transport.
func TestDeliveryModes_CBORBackwardCompat(t *testing.T) {
	// Write a legacy CampfireState (no DeliveryModes field) to disk.
	type legacyCampfireState struct {
		PublicKey             []byte   `cbor:"1,keyasint"`
		PrivateKey            []byte   `cbor:"2,keyasint"`
		JoinProtocol          string   `cbor:"3,keyasint"`
		ReceptionRequirements []string `cbor:"4,keyasint"`
		CreatedAt             int64    `cbor:"5,keyasint"`
		Threshold             uint     `cbor:"6,keyasint"`
	}

	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New: %v", err)
	}
	s := cf.State()

	legacy := legacyCampfireState{
		PublicKey:             s.PublicKey,
		PrivateKey:            s.PrivateKey,
		JoinProtocol:          s.JoinProtocol,
		ReceptionRequirements: s.ReceptionRequirements,
		CreatedAt:             s.CreatedAt,
		Threshold:             s.Threshold,
	}

	data, err := cfencoding.Marshal(legacy)
	if err != nil {
		t.Fatalf("Marshal legacy: %v", err)
	}

	// Write the legacy blob to the expected transport directory layout.
	baseDir := t.TempDir()
	campfireID := cf.PublicKeyHex()
	campfireDir := filepath.Join(baseDir, campfireID)
	if err := os.MkdirAll(campfireDir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", campfireDir, err)
	}
	if err := os.WriteFile(filepath.Join(campfireDir, "campfire.cbor"), data, 0600); err != nil {
		t.Fatalf("writing legacy campfire.cbor: %v", err)
	}

	transport := fs.New(baseDir)
	got, err := transport.ReadState(campfireID)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}

	// DeliveryModes must be nil/empty — field 9 was absent in legacy blob.
	if len(got.DeliveryModes) != 0 {
		t.Errorf("legacy campfire.cbor: DeliveryModes = %v, want nil/empty", got.DeliveryModes)
	}

	// EffectiveDeliveryModes must default to ["pull"].
	effective := campfire.EffectiveDeliveryModes(got.DeliveryModes)
	if len(effective) != 1 || effective[0] != campfire.DeliveryModePull {
		t.Errorf("EffectiveDeliveryModes(legacy) = %v, want [pull]", effective)
	}
}
