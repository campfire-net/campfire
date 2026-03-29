package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/campfire-net/campfire/pkg/identity"
)

// ---------------------------------------------------------------------------
// Helper: extract the tarball base64 from a campfire_export tool response.
// ---------------------------------------------------------------------------

func extractExportTarball(t *testing.T, resp jsonRPCResponse) string {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("campfire_export error: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}
	b, _ := json.Marshal(resp.Result)
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &result); err != nil || len(result.Content) == 0 {
		t.Fatalf("cannot extract content from export result: %v", string(b))
	}

	var payload struct {
		Tarball string `json:"tarball"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
		t.Fatalf("cannot parse export payload JSON: %v", err)
	}
	if payload.Tarball == "" {
		t.Fatal("tarball field is empty in export result")
	}
	return payload.Tarball
}

// decodeTarEntries decodes a base64-encoded gzip tar and returns a map from
// filename to content for each file entry.
func decodeTarEntries(t *testing.T, encoded string) map[string][]byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64 decode failed: %v", err)
	}

	gr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	entries := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading tar entry: %v", err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("reading tar entry data for %q: %v", hdr.Name, err)
		}
		entries[hdr.Name] = data
	}
	return entries
}

// ---------------------------------------------------------------------------
// Test: campfire_export without init returns an error.
// ---------------------------------------------------------------------------

// TestExport_WithoutInit verifies that calling campfire_export before init
// returns a -32000 error.
func TestExport_WithoutInit(t *testing.T) {
	srv := newTestServer(t)
	resp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_export","arguments":{}}`))
	if resp.Error == nil {
		t.Fatal("expected error when calling campfire_export without init")
	}
	if resp.Error.Code != -32000 {
		t.Errorf("expected -32000, got %d", resp.Error.Code)
	}
}

// ---------------------------------------------------------------------------
// Test: campfire_export returns a valid tar.gz.
// ---------------------------------------------------------------------------

// TestExport_ProducesValidTarball verifies that campfire_export after init
// returns a base64 string that decodes to a valid gzip tar archive.
func TestExport_ProducesValidTarball(t *testing.T) {
	srv := newTestServer(t)

	// Init first.
	r1 := srv.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{}}`))
	if r1.Error != nil {
		t.Fatalf("campfire_init failed: %+v", r1.Error)
	}

	// Export.
	r2 := srv.dispatch(makeReq("tools/call", `{"name":"campfire_export","arguments":{}}`))
	encoded := extractExportTarball(t, r2)

	// Must decode from base64 without error.
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64 decode failed: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("decoded tarball is empty")
	}

	// Must be a valid gzip stream.
	gr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("gzip.NewReader: not a valid gzip stream: %v", err)
	}
	defer gr.Close()

	// Must contain at least one tar entry.
	tr := tar.NewReader(gr)
	_, err = tr.Next()
	if err == io.EOF {
		t.Fatal("tarball is valid gzip but contains no files")
	}
	if err != nil {
		t.Fatalf("reading first tar entry: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test: tarball contains identity.json and store.db.
// ---------------------------------------------------------------------------

// TestExport_ContainsIdentityAndStore verifies that the exported tarball
// contains identity.json and store.db entries.
func TestExport_ContainsIdentityAndStore(t *testing.T) {
	srv := newTestServer(t)

	r1 := srv.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{}}`))
	if r1.Error != nil {
		t.Fatalf("campfire_init failed: %+v", r1.Error)
	}

	r2 := srv.dispatch(makeReq("tools/call", `{"name":"campfire_export","arguments":{}}`))
	encoded := extractExportTarball(t, r2)
	entries := decodeTarEntries(t, encoded)

	if _, ok := entries["identity.json"]; !ok {
		t.Errorf("tarball missing identity.json; entries: %v", mapKeys(entries))
	}
	if _, ok := entries["store.db"]; !ok {
		t.Errorf("tarball missing store.db; entries: %v", mapKeys(entries))
	}
}

// mapKeys returns the keys of a map[string][]byte for error messages.
func mapKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// ---------------------------------------------------------------------------
// Test: export returns an error when session data exceeds the 50 MB cap.
// ---------------------------------------------------------------------------

// TestExport_SizeCap verifies that campfire_export returns a -32000 error when
// the session directory contains more than maxExportSize bytes of data.
func TestExport_SizeCap(t *testing.T) {
	srv := newTestServer(t)

	r1 := srv.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{}}`))
	if r1.Error != nil {
		t.Fatalf("campfire_init failed: %+v", r1.Error)
	}

	// Write a file larger than maxExportSize into the session directory.
	bigFile := srv.cfHome + "/bigfile.bin"
	if err := os.WriteFile(bigFile, make([]byte, maxExportSize+1), 0600); err != nil {
		t.Fatalf("writing large file: %v", err)
	}

	r2 := srv.dispatch(makeReq("tools/call", `{"name":"campfire_export","arguments":{}}`))
	if r2.Error == nil {
		t.Fatal("expected error from campfire_export when session exceeds size cap, got nil")
	}
	if r2.Error.Code != -32000 {
		t.Errorf("expected error code -32000, got %d", r2.Error.Code)
	}
	if r2.Error.Message != "export too large: session data exceeds 50 MB limit" {
		t.Errorf("unexpected error message: %q", r2.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// Test: symlinks in the session directory are not included in the tarball.
// ---------------------------------------------------------------------------

// TestExport_SkipsSymlinks verifies that symlinks in the session directory
// are not archived by campfire_export.
func TestExport_SkipsSymlinks(t *testing.T) {
	srv := newTestServer(t)

	r1 := srv.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{}}`))
	if r1.Error != nil {
		t.Fatalf("campfire_init failed: %+v", r1.Error)
	}

	// Create a real file and a symlink pointing to it inside the session dir.
	realFile := srv.cfHome + "/real.txt"
	if err := os.WriteFile(realFile, []byte("hello"), 0600); err != nil {
		t.Fatalf("writing real file: %v", err)
	}
	symlinkPath := srv.cfHome + "/link.txt"
	if err := os.Symlink(realFile, symlinkPath); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}

	r2 := srv.dispatch(makeReq("tools/call", `{"name":"campfire_export","arguments":{}}`))
	encoded := extractExportTarball(t, r2)
	entries := decodeTarEntries(t, encoded)

	if _, ok := entries["link.txt"]; ok {
		t.Error("tarball contains symlink entry link.txt; symlinks must be skipped")
	}
	// real.txt should still be present.
	if _, ok := entries["real.txt"]; !ok {
		t.Error("tarball is missing real.txt; regular files must be included")
	}
}

// ---------------------------------------------------------------------------
// Test: exported identity.json has the same public key as the session.
// ---------------------------------------------------------------------------

// TestExport_IdentityMatchesSession verifies that the identity.json in the
// tarball contains the same Ed25519 public key as the running session.
// Dropping the tarball into a CF_HOME and running `cf id` would show the same key.
func TestExport_IdentityMatchesSession(t *testing.T) {
	srv := newTestServer(t)

	r1 := srv.dispatch(makeReq("tools/call", `{"name":"campfire_init","arguments":{}}`))
	if r1.Error != nil {
		t.Fatalf("campfire_init failed: %+v", r1.Error)
	}

	// Get the session's public key.
	r2 := srv.dispatch(makeReq("tools/call", `{"name":"campfire_id","arguments":{}}`))
	if r2.Error != nil {
		t.Fatalf("campfire_id failed: %+v", r2.Error)
	}
	b, _ := json.Marshal(r2.Result)
	var idResult struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &idResult); err != nil || len(idResult.Content) == 0 {
		t.Fatalf("cannot extract campfire_id content: %v", string(b))
	}
	var idPayload struct {
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal([]byte(idResult.Content[0].Text), &idPayload); err != nil {
		t.Fatalf("cannot parse campfire_id text: %v", err)
	}
	sessionPublicKey := idPayload.PublicKey
	if len(sessionPublicKey) != 64 {
		t.Fatalf("expected 64-char hex public key, got %q", sessionPublicKey)
	}

	// Export and extract identity.json from tarball.
	r3 := srv.dispatch(makeReq("tools/call", `{"name":"campfire_export","arguments":{}}`))
	encoded := extractExportTarball(t, r3)
	entries := decodeTarEntries(t, encoded)

	identityData, ok := entries["identity.json"]
	if !ok {
		t.Fatal("identity.json not found in tarball")
	}

	// Write identity.json to a temp file and load it with identity.Load.
	tmpDir := t.TempDir()
	idPath := tmpDir + "/identity.json"
	if err := os.WriteFile(idPath, identityData, 0600); err != nil {
		t.Fatalf("writing exported identity.json to temp file: %v", err)
	}
	exportedID, err := identity.Load(idPath)
	if err != nil {
		t.Fatalf("loading identity from exported JSON: %v", err)
	}
	if exportedID.PublicKeyHex() != sessionPublicKey {
		t.Errorf("exported public key %q does not match session public key %q",
			exportedID.PublicKeyHex(), sessionPublicKey)
	}
}

// ---------------------------------------------------------------------------
// Test: campfire_export appears in tools/list.
// ---------------------------------------------------------------------------

// TestExport_InToolsList verifies that campfire_export is listed in tools/list when
// --expose-primitives is set (campfire_export is a primitive tool).
func TestExport_InToolsList(t *testing.T) {
	srv := newTestServerWithPrimitives(t)
	resp := srv.dispatch(makeReq("tools/list", "{}"))
	if resp.Error != nil {
		t.Fatalf("tools/list error: %+v", resp.Error)
	}

	b, _ := json.Marshal(resp.Result)
	var result struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshaling tools/list: %v", err)
	}

	found := false
	for _, tool := range result.Tools {
		if tool.Name == "campfire_export" {
			found = true
			break
		}
	}
	if !found {
		t.Error("campfire_export not found in tools/list")
	}
}
