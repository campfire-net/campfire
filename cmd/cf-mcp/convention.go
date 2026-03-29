package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	ioPkg "io"
	"log"
	httpPkg "net/http"
	"path/filepath"

	"github.com/campfire-net/campfire/pkg/transport/fs"
	"sync"
	"time"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/provenance"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/trust"
)

const conventionToolTimeout = 2 * time.Minute

// conventionToolMap holds dynamically registered convention tools per session.
type conventionToolMap struct {
	mu    sync.RWMutex
	tools map[string]*conventionToolEntry
}

type conventionToolEntry struct {
	decl       *convention.Declaration
	campfireID string
	toolInfo   convention.MCPToolInfo
}

func newConventionToolMap() *conventionToolMap {
	return &conventionToolMap{
		tools: make(map[string]*conventionToolEntry),
	}
}

func (m *conventionToolMap) get(name string) (*conventionToolEntry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.tools[name]
	return e, ok
}

func (m *conventionToolMap) register(name string, entry *conventionToolEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tools[name] = entry
}

func (m *conventionToolMap) list() []convention.MCPToolInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]convention.MCPToolInfo, 0, len(m.tools))
	for _, e := range m.tools {
		result = append(result, e.toolInfo)
	}
	return result
}

// readDeclarations reads convention:operation messages from a campfire and
// filters through the authority resolver. Only declarations with at least
// AuthorityOperational survive (untrusted member declarations are dropped).
func readDeclarations(st store.Store, campfireID, campfireKey string) ([]*convention.Declaration, error) {
	decls, err := convention.ListOperations(context.Background(), storeReaderAdapter{st}, campfireID, campfireKey)
	if err != nil {
		return nil, fmt.Errorf("listing operations: %w", err)
	}

	var accepted []*convention.Declaration
	for _, decl := range decls {
		level := trust.ResolveAuthority(decl, nil)
		if level == trust.AuthorityUntrusted {
			continue
		}
		accepted = append(accepted, decl)
	}
	return accepted, nil
}

const wellKnownDeclBaseURL = "https://aietf.getcampfire.dev/.well-known/campfire/declarations"

// resolveDeclarationPayload resolves a declaration entry to its JSON payload bytes.
// Accepts:
//   - string URL (https://...) → fetched
//   - string name (e.g. "social-post") → resolved via well-known URL
//   - map/object → marshaled directly
func resolveDeclarationPayload(entry interface{}) ([]byte, error) {
	switch v := entry.(type) {
	case string:
		url := v
		if !isURL(url) {
			// Treat as well-known name
			url = wellKnownDeclBaseURL + "/" + v + ".json"
		}
		return fetchDeclarationURL(url)
	case map[string]interface{}:
		return json.Marshal(v)
	default:
		return nil, fmt.Errorf("unsupported declaration entry type %T", entry)
	}
}

func isURL(s string) bool {
	return len(s) > 8 && (s[:8] == "https://" || s[:7] == "http://")
}

// fetchDeclarationURL fetches a declaration JSON from a URL.
func fetchDeclarationURL(url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := httpPkg.NewRequestWithContext(ctx, httpPkg.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building request for %s: %w", url, err)
	}
	resp, err := httpPkg.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("fetching %s: HTTP %d", url, resp.StatusCode)
	}
	body, err := ioPkg.ReadAll(ioPkg.LimitReader(resp.Body, 1<<20)) // 1 MiB limit
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", url, err)
	}
	return body, nil
}

// publishDeclarations resolves and publishes declaration entries as campfire-key-signed
// convention:operation messages. Returns the count of successfully published declarations
// and the tool names registered from them.
//
// Since we sign each declaration with the campfire key ourselves, we know they're
// trusted. We parse and register them directly rather than going through
// readDeclarations (which filters on SignerType — and the signing field in the
// declaration JSON describes execution semantics, not declaration trust).
func (s *server) publishDeclarations(st store.Store, campfireID string, entries []interface{}) (int, []string) {
	if len(entries) == 0 {
		return 0, nil
	}

	adapter := &conventionTransportAdapter{server: s}
	var toolNames []string
	var decls []*convention.Declaration
	published := 0

	for _, entry := range entries {
		payload, err := resolveDeclarationPayload(entry)
		if err != nil {
			log.Printf("convention: resolving declaration: %v", err)
			continue
		}

		// Validate that the payload is valid JSON and looks like a declaration.
		var check map[string]interface{}
		if err := json.Unmarshal(payload, &check); err != nil {
			log.Printf("convention: declaration payload is not valid JSON: %v", err)
			continue
		}

		tags := []string{"convention:operation"}
		if _, err := adapter.SendCampfireKeySigned(context.Background(), campfireID, payload, tags, nil); err != nil {
			log.Printf("convention: publishing declaration to %s: %v", campfireID, err)
			continue
		}
		published++

		// Parse the declaration directly. We just signed it with the campfire key,
		// so we grant it SignerCampfireKey authority (operational) regardless of the
		// signing field in the JSON (which describes execution, not declaration trust).
		decl, result, parseErr := convention.Parse(tags, payload, campfireID, campfireID)
		if parseErr != nil {
			log.Printf("convention: parsing declaration: %v", parseErr)
			continue
		}
		if !result.Valid {
			log.Printf("convention: declaration invalid: %v", result.Warnings)
			continue
		}
		// Force campfire key signer type since we published it with the campfire key.
		decl.SignerType = convention.SignerCampfireKey
		decls = append(decls, decl)

		// Extract operation name for the response.
		if op, ok := check["operation"].(string); ok {
			toolNames = append(toolNames, op)
		}
	}

	// Register parsed declarations as convention tools.
	if len(decls) > 0 {
		if s.conventionTools == nil {
			s.conventionTools = newConventionToolMap()
		}
		registerConventionTools(s.conventionTools, campfireID, decls)
	}

	return published, toolNames
}

// registerConventionTools registers parsed declarations as convention tools.
func registerConventionTools(m *conventionToolMap, campfireID string, decls []*convention.Declaration) {
	existing := make(map[string]bool)
	for _, t := range tools {
		existing[t.Name] = true
	}
	m.mu.RLock()
	for name := range m.tools {
		existing[name] = true
	}
	m.mu.RUnlock()

	for _, decl := range decls {
		name := convention.GenerateToolName(decl, existing)
		info, err := convention.GenerateTool(decl, campfireID)
		if err != nil {
			log.Printf("convention: generating tool for %s/%s: %v", decl.Convention, decl.Operation, err)
			continue
		}
		info.Name = name // override with collision-aware name
		m.register(name, &conventionToolEntry{
			decl:       decl,
			campfireID: campfireID,
			toolInfo:   *info,
		})
		existing[name] = true
	}
}

// handleConventionTool dispatches a convention tool invocation through the executor.
func (s *server) handleConventionTool(id interface{}, entry *conventionToolEntry, args map[string]interface{}) jsonRPCResponse {
	transport := &conventionTransportAdapter{server: s}
	agentKey := ""
	if agentID, err := identity.Load(s.identityPath()); err == nil {
		agentKey = agentID.PublicKeyHex()
	}
	executor := convention.NewExecutor(transport, agentKey)

	// Wire in the provenance store so min_operator_level gates are enforced.
	// Without this, senderLevel defaults to 0 inside Execute and all gated
	// operations (e.g. core:peer-establish, core:peer-withdraw) are permanently
	// blocked. Operator Provenance Convention v0.1 §8.1.
	storePath := filepath.Join(s.cfHome, "attestations.json")
	var ps provenance.AttestationStore
	fs, psErr := provenance.NewFileStore(storePath, provenance.DefaultConfig())
	if psErr != nil {
		ps = provenance.NewStore(provenance.DefaultConfig())
	} else {
		ps = fs
	}
	executor.WithProvenance(&provenanceCheckerAdapter{store: ps})

	ctx, cancel := context.WithTimeout(context.Background(), conventionToolTimeout)
	defer cancel()

	if err := executor.Execute(ctx, entry.decl, entry.campfireID, args); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("convention operation failed: %v", err))
	}

	result := map[string]string{
		"status":      "ok",
		"campfire_id": entry.campfireID,
		"operation":   entry.decl.Operation,
		"convention":  entry.decl.Convention,
	}
	return s.envelopedResponse(id, entry.campfireID, result)
}

// envelopedResponse wraps a campfire content response in the safety envelope.
// Trust v0.2: trust_status is "unknown" by default. The policy engine (when
// attached) evaluates conventions to compute the actual status.
// Operator Provenance Convention v0.1 §8.2: the sender's provenance level is
// computed from the local attestation store and embedded as operator_provenance
// in runtime_computed.
func (s *server) envelopedResponse(id interface{}, campfireID string, content interface{}) jsonRPCResponse {
	status := trust.TrustUnknown

	opts := []trust.EnvelopeOption{}

	// Wire in operator provenance: load the local attestation store and compute
	// the provenance level for the agent's own public key.
	// Operator Provenance Convention v0.1 §8.2 / Trust Convention v0.2 §6.3.
	if agentID, err := identity.Load(s.identityPath()); err == nil {
		storePath := filepath.Join(s.cfHome, "attestations.json")
		var ps provenance.AttestationStore
		fs, psErr := provenance.NewFileStore(storePath, provenance.DefaultConfig())
		if psErr != nil {
			// Degrade gracefully: fall back to in-memory store (level 0 — anonymous).
			ps = provenance.NewStore(provenance.DefaultConfig())
		} else {
			ps = fs
		}
		level := int(ps.Level(agentID.PublicKeyHex()))
		opts = append(opts, trust.WithOperatorProvenance(level))
	}

	env := trust.BuildEnvelope(campfireID, status, content, opts...)
	envJSON, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("marshaling envelope: %v", err))
	}
	return okResponse(id, toolResult(string(envJSON)))
}

// provenanceCheckerAdapter adapts provenance.AttestationStore to
// convention.ProvenanceChecker. The AttestationStore.Level method returns
// provenance.Level (a named int type), while ProvenanceChecker.Level must
// return a plain int — an explicit adapter is required.
type provenanceCheckerAdapter struct {
	store provenance.AttestationStore
}

func (a *provenanceCheckerAdapter) Level(key string) int {
	return int(a.store.Level(key))
}

// storeReaderAdapter adapts store.Store to convention.StoreReader.
type storeReaderAdapter struct {
	st store.Store
}

func (a storeReaderAdapter) ListMessages(campfireID string, afterTimestamp int64, filter ...store.MessageFilter) ([]store.MessageRecord, error) {
	return a.st.ListMessages(campfireID, afterTimestamp, filter...)
}

// conventionTransportAdapter adapts the MCP server to convention.ExecutorTransport.
type conventionTransportAdapter struct {
	server *server
}

func (a *conventionTransportAdapter) SendMessage(ctx context.Context, campfireID string, payload []byte, tags []string, antecedents []string) (string, error) {
	agentID, err := identity.Load(a.server.identityPath())
	if err != nil {
		return "", fmt.Errorf("loading identity: %w", err)
	}

	st := a.server.st
	if st == nil {
		var openErr error
		st, openErr = store.Open(a.server.storePath())
		if openErr != nil {
			return "", fmt.Errorf("opening store: %w", openErr)
		}
		defer st.Close()
	}

	msg, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, payload, tags, antecedents)
	if err != nil {
		return "", fmt.Errorf("creating message: %w", err)
	}

	rec := store.MessageRecord{
		ID:          msg.ID,
		CampfireID:  campfireID,
		Sender:      msg.SenderHex(),
		Payload:     msg.Payload,
		Tags:        msg.Tags,
		Antecedents: msg.Antecedents,
		Timestamp:   msg.Timestamp,
		Signature:   msg.Signature,
	}
	if _, err := st.AddMessage(rec); err != nil {
		return "", fmt.Errorf("writing message: %w", err)
	}
	return msg.ID, nil
}

func (a *conventionTransportAdapter) SendCampfireKeySigned(ctx context.Context, campfireID string, payload []byte, tags []string, antecedents []string) (string, error) {
	// Resolve the campfire's Ed25519 keypair from the filesystem state.
	// Try the transport base dir first (production), then cfHome (tests, hosted).
	fsT := a.server.fsTransport()
	if _, err := fsT.ReadState(campfireID); err != nil && a.server.cfHome != "" {
		fsT = fs.New(a.server.cfHome)
	}
	state, err := fsT.ReadState(campfireID)
	if err != nil {
		return "", fmt.Errorf("loading campfire key for %s: %w", campfireID, err)
	}
	if len(state.PrivateKey) == 0 || len(state.PublicKey) == 0 {
		return "", fmt.Errorf("campfire %s has no keypair in local state", campfireID)
	}

	campfirePriv := ed25519.PrivateKey(state.PrivateKey)
	campfirePub := ed25519.PublicKey(state.PublicKey)

	st := a.server.st
	if st == nil {
		var openErr error
		st, openErr = store.Open(a.server.storePath())
		if openErr != nil {
			return "", fmt.Errorf("opening store: %w", openErr)
		}
		defer st.Close()
	}

	msg, err := message.NewMessage(campfirePriv, campfirePub, payload, tags, antecedents)
	if err != nil {
		return "", fmt.Errorf("creating campfire-key-signed message: %w", err)
	}

	rec := store.MessageRecord{
		ID:          msg.ID,
		CampfireID:  campfireID,
		Sender:      msg.SenderHex(),
		Payload:     msg.Payload,
		Tags:        msg.Tags,
		Antecedents: msg.Antecedents,
		Timestamp:   msg.Timestamp,
		Signature:   msg.Signature,
	}
	if _, err := st.AddMessage(rec); err != nil {
		return "", fmt.Errorf("writing message: %w", err)
	}
	return msg.ID, nil
}

func (a *conventionTransportAdapter) ReadMessages(ctx context.Context, campfireID string, tags []string) ([]convention.MessageRecord, error) {
	st := a.server.st
	if st == nil {
		var openErr error
		st, openErr = store.Open(a.server.storePath())
		if openErr != nil {
			return nil, fmt.Errorf("opening store: %w", openErr)
		}
		defer st.Close()
	}

	msgs, err := st.ListMessages(campfireID, 0, store.MessageFilter{Tags: tags})
	if err != nil {
		return nil, err
	}

	result := make([]convention.MessageRecord, len(msgs))
	for i, m := range msgs {
		result[i] = convention.MessageRecord{
			ID:     m.ID,
			Sender: m.Sender,
			Tags:   m.Tags,
		}
	}
	return result, nil
}

func (a *conventionTransportAdapter) SendFutureAndAwait(ctx context.Context, campfireID string, payload []byte, tags []string, _ time.Duration) ([]byte, error) {
	return nil, fmt.Errorf("future/await not yet implemented in MCP transport adapter")
}
