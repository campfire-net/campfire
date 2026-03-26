package naming

import (
	"context"
	"fmt"
	"testing"
)

// mockTransport is a test transport that returns pre-configured responses.
type mockTransport struct {
	// resolveMap: key = "parentID/name" → response
	resolveMap map[string]*ResolveResponse
	// listMap: key = campfireID → response
	listMap map[string]*ListResponse
	// apiMap: key = campfireID → declarations
	apiMap map[string][]APIDeclaration
	// invokeMap: key = "campfireID/endpoint" → response
	invokeMap map[string]*InvokeResponse
}

func newMockTransport() *mockTransport {
	return &mockTransport{
		resolveMap: make(map[string]*ResolveResponse),
		listMap:    make(map[string]*ListResponse),
		apiMap:     make(map[string][]APIDeclaration),
		invokeMap:  make(map[string]*InvokeResponse),
	}
}

func (m *mockTransport) Resolve(ctx context.Context, campfireID, name string) (*ResolveResponse, error) {
	key := campfireID + "/" + name
	if resp, ok := m.resolveMap[key]; ok {
		return resp, nil
	}
	return nil, fmt.Errorf("name %q not found in campfire %s", name, campfireID[:12])
}

func (m *mockTransport) ListChildren(ctx context.Context, campfireID, prefix string) (*ListResponse, error) {
	if resp, ok := m.listMap[campfireID]; ok {
		// Filter by prefix if non-empty
		if prefix == "" {
			return resp, nil
		}
		var filtered []ListEntry
		for _, e := range resp.Names {
			if len(e.Name) >= len(prefix) && e.Name[:len(prefix)] == prefix {
				filtered = append(filtered, e)
			}
		}
		return &ListResponse{Names: filtered}, nil
	}
	return nil, fmt.Errorf("campfire %s not found", campfireID[:12])
}

func (m *mockTransport) ListAPI(ctx context.Context, campfireID string) ([]APIDeclaration, error) {
	if decls, ok := m.apiMap[campfireID]; ok {
		return decls, nil
	}
	return nil, fmt.Errorf("campfire %s not found", campfireID[:12])
}

func (m *mockTransport) Invoke(ctx context.Context, campfireID string, req *InvokeRequest) (*InvokeResponse, error) {
	key := campfireID + "/" + req.Endpoint
	if resp, ok := m.invokeMap[key]; ok {
		return resp, nil
	}
	return nil, fmt.Errorf("endpoint %q not found in campfire %s", req.Endpoint, campfireID[:12])
}

const (
	rootID   = "0000000000000000000000000000000000000000000000000000000000000000"
	aietfID  = "a1b2c3d4e5f6000000000000000000000000000000000000000000000000dead"
	socialID = "c3d4e5f6a1b2000000000000000000000000000000000000000000000000beef"
	lobbyID  = "e5f6a1b2c3d4000000000000000000000000000000000000000000000000cafe"
)

func setupTestResolver() (*Resolver, *mockTransport) {
	mt := newMockTransport()

	// root → aietf
	mt.resolveMap[rootID+"/aietf"] = &ResolveResponse{
		Name:       "aietf",
		CampfireID: aietfID,
		TTL:        3600,
	}
	// aietf → social
	mt.resolveMap[aietfID+"/social"] = &ResolveResponse{
		Name:       "social",
		CampfireID: socialID,
		TTL:        3600,
	}
	// social → lobby
	mt.resolveMap[socialID+"/lobby"] = &ResolveResponse{
		Name:       "lobby",
		CampfireID: lobbyID,
		TTL:        3600,
	}

	r := NewResolver(mt, rootID)
	return r, mt
}

// Test Vector 1: Simple Name Resolution
func TestResolveName_Simple(t *testing.T) {
	r, _ := setupTestResolver()
	ctx := context.Background()

	id, err := r.ResolveName(ctx, []string{"aietf", "social", "lobby"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != lobbyID {
		t.Errorf("got %q, want %q", id, lobbyID)
	}
}

// Test Vector 2: Future Invocation via URI
func TestResolveURI_WithFutureInvocation(t *testing.T) {
	r, mt := setupTestResolver()
	mt.invokeMap[lobbyID+"/trending"] = &InvokeResponse{
		Endpoint: "trending",
		Results:  []any{"post1", "post2"},
	}

	ctx := context.Background()
	resp, err := r.Invoke(ctx, "cf://aietf.social.lobby/trending?window=24h")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Endpoint != "trending" {
		t.Errorf("endpoint = %q, want %q", resp.Endpoint, "trending")
	}
	if len(resp.Results) != 2 {
		t.Errorf("results = %d, want 2", len(resp.Results))
	}
}

// Test Vector 3: Tab Completion (children listing)
func TestListChildren(t *testing.T) {
	r, mt := setupTestResolver()
	mt.listMap[socialID] = &ListResponse{
		Names: []ListEntry{
			{Name: "lobby", Description: "General discussion"},
			{Name: "ai-tools", Description: "AI tools and MCP servers"},
			{Name: "code-review", Description: "Peer code review"},
		},
	}

	ctx := context.Background()
	entries, err := r.ListChildren(ctx, socialID, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	if entries[0].Name != "lobby" {
		t.Errorf("first entry = %q, want %q", entries[0].Name, "lobby")
	}
}

// Test Vector 4: API Discovery
func TestListAPI(t *testing.T) {
	r, mt := setupTestResolver()
	mt.apiMap[lobbyID] = []APIDeclaration{
		{Endpoint: "trending", Description: "Popular posts"},
		{Endpoint: "new-posts", Description: "Recent posts"},
		{Endpoint: "introductions", Description: "New member intros"},
	}

	ctx := context.Background()
	decls, err := r.ListAPI(ctx, lobbyID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(decls) != 3 {
		t.Fatalf("got %d decls, want 3", len(decls))
	}
	if decls[0].Endpoint != "trending" {
		t.Errorf("first endpoint = %q, want %q", decls[0].Endpoint, "trending")
	}
}

// Test Vector 7: Circular Resolution Detection
func TestResolveName_CircularDetection(t *testing.T) {
	mt := newMockTransport()
	loopID := "1111111111111111111111111111111111111111111111111111111111111111"
	campX := "2222222222222222222222222222222222222222222222222222222222222222"

	// root → loop → campX → root (circular via campX pointing back to root)
	mt.resolveMap[rootID+"/loop"] = &ResolveResponse{
		Name: "loop", CampfireID: loopID, TTL: 0,
	}
	mt.resolveMap[loopID+"/a"] = &ResolveResponse{
		Name: "a", CampfireID: campX, TTL: 0,
	}
	mt.resolveMap[campX+"/b"] = &ResolveResponse{
		Name: "b", CampfireID: rootID, TTL: 0, // circular!
	}

	r := NewResolver(mt, rootID)
	ctx := context.Background()

	_, err := r.ResolveName(ctx, []string{"loop", "a", "b"})
	if err == nil {
		t.Fatal("expected circular resolution error")
	}
	if !containsCI(err.Error(), "circular") {
		t.Errorf("error %q does not mention circular", err.Error())
	}
}

// Test Vector 9: TOFU Pin Violation
func TestResolveName_TOFUViolation(t *testing.T) {
	r, mt := setupTestResolver()
	ctx := context.Background()

	// First resolution — pins the value
	id, err := r.ResolveName(ctx, []string{"aietf", "social", "lobby"})
	if err != nil {
		t.Fatalf("first resolution: %v", err)
	}
	if id != lobbyID {
		t.Fatalf("expected %s", lobbyID)
	}

	// Change the mapping
	newID := "ffff000000000000000000000000000000000000000000000000000000000000"
	mt.resolveMap[socialID+"/lobby"] = &ResolveResponse{
		Name: "lobby", CampfireID: newID, TTL: 0,
	}

	// Invalidate cache so resolver goes to transport
	r.InvalidateCache(socialID, "lobby")

	// Second resolution — should trigger TOFU violation
	_, err = r.ResolveName(ctx, []string{"aietf", "social", "lobby"})
	if err == nil {
		t.Fatal("expected TOFU violation error")
	}
	tofuErr, ok := err.(*TOFUViolation)
	if !ok {
		// It might be wrapped
		if !containsCI(err.Error(), "tofu") {
			t.Errorf("error %q does not mention TOFU", err.Error())
		}
	} else {
		if tofuErr.PinnedID != lobbyID {
			t.Errorf("pinned = %s, want %s", tofuErr.PinnedID, lobbyID)
		}
		if tofuErr.ResolvedID != newID {
			t.Errorf("resolved = %s, want %s", tofuErr.ResolvedID, newID)
		}
	}
}

// Test: ClearTOFUPin allows re-pinning
func TestClearTOFUPin(t *testing.T) {
	r, mt := setupTestResolver()
	ctx := context.Background()

	// First resolution
	_, err := r.ResolveName(ctx, []string{"aietf"})
	if err != nil {
		t.Fatal(err)
	}

	// Change mapping
	newID := "ffff000000000000000000000000000000000000000000000000000000000001"
	mt.resolveMap[rootID+"/aietf"] = &ResolveResponse{
		Name: "aietf", CampfireID: newID, TTL: 0,
	}
	r.InvalidateCache(rootID, "aietf")

	// Without clearing pin, should fail
	_, err = r.ResolveName(ctx, []string{"aietf"})
	if err == nil {
		t.Fatal("expected TOFU violation")
	}

	// Clear pin and retry
	r.ClearTOFUPin("aietf")
	id, err := r.ResolveName(ctx, []string{"aietf"})
	if err != nil {
		t.Fatalf("after clearing pin: %v", err)
	}
	if id != newID {
		t.Errorf("got %s, want %s", id, newID)
	}
}

// Test: Cache hit avoids transport call
func TestResolveCache(t *testing.T) {
	r, mt := setupTestResolver()
	ctx := context.Background()

	// First call populates cache
	_, err := r.ResolveName(ctx, []string{"aietf"})
	if err != nil {
		t.Fatal(err)
	}

	// Remove from transport — should still resolve from cache
	delete(mt.resolveMap, rootID+"/aietf")

	id, err := r.ResolveName(ctx, []string{"aietf"})
	if err != nil {
		t.Fatalf("cache hit should succeed: %v", err)
	}
	if id != aietfID {
		t.Errorf("got %s, want %s", id, aietfID)
	}
}

// Test: TTL 0 means no cache
func TestResolveTTLZero(t *testing.T) {
	mt := newMockTransport()
	mt.resolveMap[rootID+"/nocache"] = &ResolveResponse{
		Name: "nocache", CampfireID: aietfID, TTL: 0,
	}

	r := NewResolver(mt, rootID)
	ctx := context.Background()

	_, err := r.ResolveName(ctx, []string{"nocache"})
	if err != nil {
		t.Fatal(err)
	}

	// Remove — should fail because not cached
	delete(mt.resolveMap, rootID+"/nocache")
	_, err = r.ResolveName(ctx, []string{"nocache"})
	// This will hit TOFU since aietf pin exists. Clear it.
	r.ClearTOFUPin("nocache")
	_, err = r.ResolveName(ctx, []string{"nocache"})
	if err == nil {
		t.Fatal("expected error: not cached and not in transport")
	}
}

// Test: ResolveOrPassthrough with hex ID
func TestResolveOrPassthrough(t *testing.T) {
	r, _ := setupTestResolver()
	ctx := context.Background()

	// Hex ID — passthrough
	hexID := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	got, err := r.ResolveOrPassthrough(ctx, hexID)
	if err != nil {
		t.Fatal(err)
	}
	if got != hexID {
		t.Errorf("passthrough failed: got %q", got)
	}

	// cf:// URI — resolve
	got, err = r.ResolveOrPassthrough(ctx, "cf://aietf.social.lobby")
	if err != nil {
		t.Fatal(err)
	}
	if got != lobbyID {
		t.Errorf("resolve failed: got %q, want %q", got, lobbyID)
	}
}

// Test: Empty name
func TestResolveName_Empty(t *testing.T) {
	r, _ := setupTestResolver()
	_, err := r.ResolveName(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

// Test: Invoke without path
func TestInvoke_NoPath(t *testing.T) {
	r, _ := setupTestResolver()
	_, err := r.Invoke(context.Background(), "cf://aietf.social.lobby")
	if err == nil {
		t.Fatal("expected error for URI without path")
	}
}

// Test: Description sanitization in ListChildren
func TestListChildren_SanitizesDescriptions(t *testing.T) {
	r, mt := setupTestResolver()
	longDesc := ""
	for i := 0; i < 100; i++ {
		longDesc += "x"
	}
	mt.listMap[socialID] = &ListResponse{
		Names: []ListEntry{
			{Name: "test", Description: longDesc},
			{Name: "evil", Description: "inject\nprompt\x00here"},
		},
	}

	entries, err := r.ListChildren(context.Background(), socialID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries[0].Description) != 80 {
		t.Errorf("description not truncated: len=%d", len(entries[0].Description))
	}
	if entries[1].Description != "injectprompthere" {
		t.Errorf("description not sanitized: %q", entries[1].Description)
	}
}
