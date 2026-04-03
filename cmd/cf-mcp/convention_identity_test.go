package main

// Regression test for campfire-agent-blu: handleConventionTool silently swallowed
// identity load errors, causing convention operations to fail with a misleading
// "convention operation failed" error instead of a clear "identity not loaded" message.
//
// Fix: when identity.Load returns an error, handleConventionTool now logs the
// error and returns an explicit "identity not loaded: ..." error response.
//
// This test verifies that when the identity file is absent, the response error
// message clearly identifies the root cause rather than propagating a cryptic
// downstream error.

import (
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/convention"
)

// TestHandleConventionTool_IdentityLoadFailure verifies that when no identity
// file exists, handleConventionTool returns a clear "identity not loaded" error
// rather than silently proceeding with nil identity and producing a misleading
// downstream failure.
//
// Before the fix:
//
//	if loaded, err := identity.Load(s.identityPath()); err == nil {
//	    agentKey = loaded.PublicKeyHex()
//	    agentID = loaded
//	}
//	// agentID is nil → executor.Execute fails with cryptic error
//
// After the fix, when identity.Load fails, handleConventionTool returns:
//
//	errResponse(rpcID, -32000, "identity not loaded: ...")
func TestHandleConventionTool_IdentityLoadFailure(t *testing.T) {
	// newTestServer creates a server with a temp cfHome but no identity file.
	// identityPath() returns cfHome/identity.json which does not exist.
	srv, _ := newTestServerWithStore(t)
	// Confirm no identity file exists — this is the precondition.
	// (newTestServer uses a fresh TempDir with no files in it.)

	// Build a minimal convention entry to invoke.
	payload := []byte(`{
		"convention": "test-convention",
		"version": "0.1",
		"operation": "noop",
		"description": "No-op for identity failure test",
		"produces_tags": [],
		"args": [],
		"antecedents": "none",
		"signing": "member_key"
	}`)
	tags := []string{convention.ConventionOperationTag}
	decl, _, parseErr := convention.Parse(tags, payload, strings.Repeat("a", 64), strings.Repeat("b", 64))
	if parseErr != nil {
		t.Fatalf("convention.Parse: %v", parseErr)
	}

	entry := &conventionToolEntry{
		decl:       decl,
		campfireID: "test-campfire-id",
	}

	resp := srv.handleConventionTool(float64(1), entry, map[string]interface{}{})

	// Must return an error — no identity file exists.
	if resp.Error == nil {
		t.Fatal("expected error response when identity file is absent, got nil error")
	}
	if resp.Error.Code != -32000 {
		t.Errorf("expected error code -32000, got %d", resp.Error.Code)
	}
	// The error message must clearly state that identity loading failed,
	// not a cryptic downstream executor error.
	if !strings.Contains(resp.Error.Message, "identity not loaded") {
		t.Errorf("expected 'identity not loaded' in error message, got: %q", resp.Error.Message)
	}
}
