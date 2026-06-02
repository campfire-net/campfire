package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/cf-conventions/cf-convention"
	"github.com/campfire-net/campfire/cf-protocol/store"
)

// multiOpFile builds a {convention, version, operations:[...]} authoring file.
// Each op omits convention/version unless overrideVersion is set on it, so the
// test also exercises file-level injection and per-op override (campfire-aa5).
func multiOpFile(conv, fileVersion string, ops []map[string]any) []byte {
	d := map[string]any{
		"convention":  conv,
		"version":     fileVersion,
		"description": "multi-op " + conv,
		"operations":  ops,
	}
	b, _ := json.Marshal(d)
	return b
}

func opEntry(operation, overrideVersion string) map[string]any {
	op := map[string]any{
		"operation":   operation,
		"description": "op " + operation,
		"produces_tags": []map[string]any{
			{"tag": "mc:" + operation, "cardinality": "exactly_one"},
		},
		"args":    []map[string]any{{"name": "text", "type": "string", "required": true, "max_length": 1000}},
		"signing": "member_key",
	}
	if overrideVersion != "" {
		op["version"] = overrideVersion
	}
	return op
}

// TestExpandMultiOp_SingleOpUnchanged: a flat single-op file is returned as one
// source with its bytes untouched.
func TestExpandMultiOp_SingleOpUnchanged(t *testing.T) {
	payload := testInstallDecl("c", "post", "0.1")
	out, err := expandMultiOp("post.json", payload)
	if err != nil {
		t.Fatalf("expandMultiOp: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	if string(out[0].payload) != string(payload) {
		t.Error("single-op payload was modified")
	}
}

// TestExpandMultiOp_Expands: a multi-op file expands to one source per op, with
// file-level convention/version injected only where the op omits them.
func TestExpandMultiOp_Expands(t *testing.T) {
	payload := multiOpFile("mc", "0.1", []map[string]any{
		opEntry("greet", ""),      // inherits version 0.1
		opEntry("respond", "0.3"), // overrides to 0.3
	})
	out, err := expandMultiOp("mc.json", payload)
	if err != nil {
		t.Fatalf("expandMultiOp: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	got := map[string]string{} // operation -> version
	for _, src := range out {
		d, _, err := convention.Parse(
			[]string{convention.ConventionOperationTag}, src.payload,
			"00", "00", convention.DefaultDeniedTagPrefixes,
		)
		if err != nil {
			t.Fatalf("parse expanded op %q: %v", src.name, err)
		}
		if d.Convention != "mc" {
			t.Errorf("op %q convention = %q, want mc (injected)", d.Operation, d.Convention)
		}
		got[d.Operation] = d.Version
	}
	if got["greet"] != "0.1" {
		t.Errorf("greet version = %q, want 0.1 (inherited)", got["greet"])
	}
	if got["respond"] != "0.3" {
		t.Errorf("respond version = %q, want 0.3 (per-op override)", got["respond"])
	}
}

// TestConventionInstall_MultiOpFile installs a 3-op file and verifies all three
// operations land on the campfire from a single install command.
func TestConventionInstall_MultiOpFile(t *testing.T) {
	campfireID, cleanup := setupDispatchEnv(t, nil)
	defer cleanup()

	payload := multiOpFile("welcome", "0.1", []map[string]any{
		opEntry("greet-arrival", ""),
		opEntry("respond-to-greeting", "0.3"),
		opEntry("onboard", ""),
	})
	dir := t.TempDir()
	declFile := filepath.Join(dir, "welcome.json")
	if err := os.WriteFile(declFile, payload, 0600); err != nil {
		t.Fatalf("writing decl file: %v", err)
	}

	if err := runConventionInstall(conventionInstallCmd, []string{campfireID[:12], declFile}); err != nil {
		t.Fatalf("install multi-op file: %v", err)
	}

	s, err := openStore()
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	decls, err := listConventionOperations(context.Background(), s, campfireID)
	if err != nil {
		t.Fatalf("listing operations: %v", err)
	}
	ops := map[string]bool{}
	for _, d := range decls {
		ops[d.Operation] = true
	}
	for _, want := range []string{"greet-arrival", "respond-to-greeting", "onboard"} {
		if !ops[want] {
			t.Errorf("expected operation %q from multi-op install, got: %v", want, ops)
		}
	}
}

// TestConventionInstall_MultiOpAtomicValidation verifies that a multi-op file
// with one invalid op posts ZERO messages (the atomic validation gate), rather
// than leaving the valid ops partially installed.
func TestConventionInstall_MultiOpAtomicValidation(t *testing.T) {
	campfireID, cleanup := setupDispatchEnv(t, nil)
	defer cleanup()

	// Third op is invalid: missing required "signing" — and missing operation name.
	badOp := map[string]any{"description": "broken", "version": "0.1"}
	payload := multiOpFile("part", "0.1", []map[string]any{
		opEntry("good-one", ""),
		opEntry("good-two", ""),
		badOp,
	})
	dir := t.TempDir()
	declFile := filepath.Join(dir, "part.json")
	if err := os.WriteFile(declFile, payload, 0600); err != nil {
		t.Fatalf("writing decl file: %v", err)
	}

	err := runConventionInstall(conventionInstallCmd, []string{campfireID[:12], declFile})
	if err == nil {
		t.Fatal("expected install to fail on the invalid op, got nil")
	}

	s, err := openStore()
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	defer s.Close()

	msgs, err := s.ListMessages(campfireID, 0, store.MessageFilter{
		Tags: []string{convention.ConventionOperationTag},
	})
	if err != nil {
		t.Fatalf("listing messages: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("atomic gate violated: %d declaration messages posted, want 0 (valid ops must not install when a sibling op is invalid)", len(msgs))
	}
}
