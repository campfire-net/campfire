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
	"sync"
	"time"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/predicate"
	"github.com/campfire-net/campfire/pkg/provenance"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	"github.com/campfire-net/campfire/pkg/trust"
)

const conventionToolTimeout = 2 * time.Minute

// conventionToolMap holds dynamically registered convention tools per session.
type conventionToolMap struct {
	mu    sync.RWMutex
	tools map[string]*conventionToolEntry
	views map[string]*viewToolEntry
}

type conventionToolEntry struct {
	decl       *convention.Declaration
	campfireID string
	toolInfo   convention.MCPToolInfo
}

// viewToolEntry holds a registered convention view tool.
type viewToolEntry struct {
	name        string
	description string
	predicate   string // S-expression predicate
	campfireID  string
	toolInfo    convention.MCPToolInfo
}

func newConventionToolMap() *conventionToolMap {
	return &conventionToolMap{
		tools: make(map[string]*conventionToolEntry),
		views: make(map[string]*viewToolEntry),
	}
}

func (m *conventionToolMap) get(name string) (*conventionToolEntry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.tools[name]
	return e, ok
}

func (m *conventionToolMap) getView(name string) (*viewToolEntry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.views[name]
	return e, ok
}

func (m *conventionToolMap) register(name string, entry *conventionToolEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tools[name] = entry
}

func (m *conventionToolMap) registerView(name string, entry *viewToolEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.views[name] = entry
}

func (m *conventionToolMap) list() []convention.MCPToolInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]convention.MCPToolInfo, 0, len(m.tools)+len(m.views))
	for _, e := range m.tools {
		result = append(result, e.toolInfo)
	}
	for _, v := range m.views {
		result = append(result, v.toolInfo)
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
		if _, err := s.sendCampfireKeySignedMessage(context.Background(), campfireID, payload, tags, nil); err != nil {
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
	}

	// Register parsed declarations as convention tools.
	if len(decls) > 0 {
		if s.conventionTools == nil {
			s.conventionTools = newConventionToolMap()
		}
		registeredNames := registerConventionTools(s.conventionTools, campfireID, decls)
		toolNames = registeredNames // use collision-aware names, not raw operations
		if s.sess != nil {
			s.sess.conventionTools = s.conventionTools
		}
	}

	// Auto-publish views declared inside conventions.
	for _, decl := range decls {
		for _, v := range decl.Views {
			if v.Name == "" || v.Predicate == "" {
				continue
			}
			viewEntries := []interface{}{
				map[string]interface{}{
					"name":        v.Name,
					"predicate":   v.Predicate,
					"description": v.Description,
				},
			}
			vCount, vNames := s.publishViews(st, campfireID, viewEntries)
			if vCount > 0 {
				toolNames = append(toolNames, vNames...)
			}
		}
	}

	return published, toolNames
}

// registerConventionTools registers parsed declarations as convention tools.
// Returns the list of tool names actually registered (collision-aware).
func registerConventionTools(m *conventionToolMap, campfireID string, decls []*convention.Declaration) []string {
	existing := make(map[string]bool)
	for _, t := range tools {
		existing[t.Name] = true
	}

	// Snapshot existing convention tools for collision detection.
	m.mu.RLock()
	existingByName := make(map[string]*conventionToolEntry)
	for name, entry := range m.tools {
		existing[name] = true
		existingByName[name] = entry
	}
	m.mu.RUnlock()

	// Pre-scan: count operation names across new decls + existing bare-name tools.
	// A "bare name" is one where the tool name equals the raw operation (not yet namespaced).
	opCount := make(map[string]int)
	for _, decl := range decls {
		opCount[decl.Operation]++
	}
	for name, entry := range existingByName {
		if name == entry.decl.Operation { // bare name
			opCount[name]++
		}
	}
	collisions := make(map[string]bool)
	for op, count := range opCount {
		if count > 1 {
			collisions[op] = true
		}
	}

	// Rename existing bare-name tools that now collide.
	for name, entry := range existingByName {
		if collisions[name] && name == entry.decl.Operation {
			newName := convention.NamespacedToolName(entry.decl)
			entry.toolInfo.Name = newName
			m.mu.Lock()
			m.tools[newName] = entry
			delete(m.tools, name)
			m.mu.Unlock()
			existing[newName] = true
			delete(existing, name)
		}
	}

	var registeredNames []string
	for _, decl := range decls {
		var name string
		if collisions[decl.Operation] {
			name = convention.NamespacedToolName(decl)
		} else {
			name = convention.GenerateToolName(decl, existing)
		}
		info, err := convention.GenerateTool(decl, campfireID)
		if err != nil {
			log.Printf("convention: generating tool for %s/%s: %v", decl.Convention, decl.Operation, err)
			continue
		}
		info.Name = name
		m.register(name, &conventionToolEntry{
			decl:       decl,
			campfireID: campfireID,
			toolInfo:   *info,
		})
		existing[name] = true
		registeredNames = append(registeredNames, name)
	}
	return registeredNames
}

// publishViews resolves and publishes view definitions as campfire-key-signed
// campfire:view messages. Returns the count of views published and their names.
func (s *server) publishViews(st store.Store, campfireID string, entries []interface{}) (int, []string) {
	if len(entries) == 0 {
		return 0, nil
	}

	var viewNames []string
	published := 0

	for _, entry := range entries {
		vd, ok := entry.(map[string]interface{})
		if !ok {
			log.Printf("convention: view entry is not an object, skipping")
			continue
		}

		name, _ := vd["name"].(string)
		pred, _ := vd["predicate"].(string)
		desc, _ := vd["description"].(string)

		if name == "" || pred == "" {
			log.Printf("convention: view entry missing name or predicate, skipping")
			continue
		}

		// Validate the predicate parses.
		if _, err := predicate.Parse(pred); err != nil {
			log.Printf("convention: view %q predicate invalid: %v", name, err)
			continue
		}

		viewDef := map[string]interface{}{
			"name":      name,
			"predicate": pred,
			"refresh":   "on-read",
		}
		payload, err := json.Marshal(viewDef)
		if err != nil {
			log.Printf("convention: marshaling view %q: %v", name, err)
			continue
		}

		tags := []string{"campfire:view"}
		if _, err := s.sendCampfireKeySignedMessage(context.Background(), campfireID, payload, tags, nil); err != nil {
			log.Printf("convention: publishing view %q to %s: %v", name, campfireID, err)
			continue
		}
		published++
		viewNames = append(viewNames, name)

		// Register as an MCP tool immediately.
		if s.conventionTools == nil {
			s.conventionTools = newConventionToolMap()
		}
		registerViewTool(s.conventionTools, campfireID, name, desc, pred)
		if s.sess != nil {
			s.sess.conventionTools = s.conventionTools
		}
	}

	return published, viewNames
}

// readAndRegisterViews reads campfire:view messages from a campfire and registers
// them as MCP tools. Called on join to discover views that were seeded at create time.
func (s *server) readAndRegisterViews(st store.Store, campfireID string) (int, []string) {
	msgs, err := st.ListMessages(campfireID, 0, store.MessageFilter{Tags: []string{"campfire:view"}})
	if err != nil {
		log.Printf("convention: reading views for %s: %v", campfireID, err)
		return 0, nil
	}

	// Collect latest definition per name (later message overrides earlier).
	latest := make(map[string]struct {
		name, desc, pred string
	})
	for _, m := range msgs {
		var vd struct {
			Name      string `json:"name"`
			Predicate string `json:"predicate"`
		}
		if err := json.Unmarshal(m.Payload, &vd); err != nil || vd.Name == "" || vd.Predicate == "" {
			continue
		}
		latest[vd.Name] = struct{ name, desc, pred string }{vd.Name, "", vd.Predicate}
	}

	if len(latest) == 0 {
		return 0, nil
	}

	if s.conventionTools == nil {
		s.conventionTools = newConventionToolMap()
	}

	var names []string
	for _, v := range latest {
		registerViewTool(s.conventionTools, campfireID, v.name, v.desc, v.pred)
		names = append(names, v.name)
	}
	if s.sess != nil {
		s.sess.conventionTools = s.conventionTools
	}
	return len(names), names
}

// registerViewTool registers a single view as an MCP tool.
func registerViewTool(m *conventionToolMap, campfireID, name, description, pred string) {
	if description == "" {
		description = fmt.Sprintf("Read %q view — returns messages matching the convention-defined filter.", name)
	}

	schema, _ := json.Marshal(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"campfire_id": map[string]interface{}{
				"type":        "string",
				"description": "Campfire ID to read from.",
				"default":     campfireID,
			},
		},
		"required": []string{},
	})

	m.registerView(name, &viewToolEntry{
		name:        name,
		description: description,
		predicate:   pred,
		campfireID:  campfireID,
		toolInfo: convention.MCPToolInfo{
			Name:        name,
			Description: description,
			InputSchema: schema,
		},
	})
}

// handleViewTool executes a view read — filters campfire messages through the
// view's predicate and returns matching messages in a trust envelope.
func (s *server) handleViewTool(id interface{}, entry *viewToolEntry, args map[string]interface{}) jsonRPCResponse {
	campfireID := entry.campfireID
	if cid, ok := args["campfire_id"].(string); ok && cid != "" {
		campfireID = cid
	}

	st := s.st
	if st == nil {
		var openErr error
		st, openErr = store.Open(s.storePath())
		if openErr != nil {
			return errResponse(id, -32000, fmt.Sprintf("opening store: %v", openErr))
		}
		defer st.Close()
	}

	// Sync from filesystem transport (same as handleRead — in fs mode,
	// messages live on disk and must be synced to SQLite before querying).
	if s.httpTransport == nil {
		fsT := s.fsTransport()
		if fsMessages, err := fsT.ListMessages(campfireID); err == nil {
			for _, fsMsg := range fsMessages {
				st.AddMessage(store.MessageRecordFromMessage(campfireID, &fsMsg, store.NowNano())) //nolint:errcheck
			}
		}
	}

	pred, err := predicate.Parse(entry.predicate)
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("parsing view predicate: %v", err))
	}

	msgs, err := st.ListMessages(campfireID, 0, store.MessageFilter{RespectCompaction: true})
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("reading messages: %v", err))
	}

	// Build fulfillment index for has-fulfillment predicate.
	fulfillmentIndex := make(map[string]bool)
	for _, m := range msgs {
		hasFulfills := false
		for _, t := range m.Tags {
			if t == "fulfills" {
				hasFulfills = true
				break
			}
		}
		if hasFulfills {
			for _, ant := range m.Antecedents {
				fulfillmentIndex[ant] = true
			}
		}
	}

	var matched []map[string]interface{}
	for _, m := range msgs {
		// Skip system messages (campfire:* tags).
		isSystem := false
		for _, t := range m.Tags {
			if len(t) > 9 && t[:9] == "campfire:" {
				isSystem = true
				break
			}
		}
		if isSystem {
			continue
		}

		ctx := &predicate.MessageContext{
			MessageID:        m.ID,
			Tags:             m.Tags,
			Sender:           m.Sender,
			Timestamp:        m.Timestamp,
			FulfillmentIndex: fulfillmentIndex,
		}
		if len(m.Payload) > 0 {
			var payload map[string]interface{}
			if err := json.Unmarshal(m.Payload, &payload); err == nil {
				ctx.Payload = payload
			}
			ctx.RawPayload = m.Payload
		}

		if predicate.Eval(pred, ctx) {
			// Payload may be plain text or JSON — try JSON first, fall back to string.
			var payloadVal interface{}
			if json.Valid(m.Payload) {
				payloadVal = json.RawMessage(m.Payload)
			} else {
				payloadVal = string(m.Payload)
			}
			matched = append(matched, map[string]interface{}{
				"id":        m.ID,
				"sender":    m.Sender,
				"tags":      m.Tags,
				"payload":   payloadVal,
				"timestamp": m.Timestamp,
			})
		}
	}

	return s.envelopedResponse(id, campfireID, map[string]interface{}{
		"view":     entry.name,
		"count":    len(matched),
		"messages": matched,
	})
}

// handleConventionTool dispatches a convention tool invocation through the executor.
func (s *server) handleConventionTool(rpcID interface{}, entry *conventionToolEntry, args map[string]interface{}) jsonRPCResponse {
	agentKey := ""
	var agentID *identity.Identity
	if loaded, err := identity.Load(s.identityPath()); err == nil {
		agentKey = loaded.PublicKeyHex()
		agentID = loaded
	}

	st := s.st
	if st == nil {
		var openErr error
		st, openErr = store.Open(s.storePath())
		if openErr != nil {
			return errResponse(rpcID, -32000, fmt.Sprintf("opening store: %v", openErr))
		}
		defer st.Close()
	}

	client := newProtocolClient(st, agentID)
	executor := convention.NewExecutor(client, agentKey)

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
		return errResponse(rpcID, -32000, fmt.Sprintf("convention operation failed: %v", err))
	}

	result := map[string]string{
		"status":      "ok",
		"campfire_id": entry.campfireID,
		"operation":   entry.decl.Operation,
		"convention":  entry.decl.Convention,
	}
	return s.envelopedResponse(rpcID, entry.campfireID, result)
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

// sendCampfireKeySignedMessage creates a message signed with the campfire's own
// Ed25519 keypair and writes it directly to the store. Used when publishing
// convention declarations and view definitions at campfire creation time.
//
// The campfire keypair is resolved from the filesystem state, trying the transport
// base dir first (production) and falling back to cfHome (HTTP session mode).
// Messages are written directly to the store so they are immediately visible to
// local readers without a filesystem sync round-trip.
func (s *server) sendCampfireKeySignedMessage(_ context.Context, campfireID string, payload []byte, tags []string, antecedents []string) (string, error) {
	// Resolve the campfire's Ed25519 keypair from the filesystem state.
	// Try the transport base dir first (production), then cfHome (tests, hosted).
	fsT := s.fsTransport()
	if _, err := fsT.ReadState(campfireID); err != nil && s.cfHome != "" {
		fsT = fs.New(s.cfHome)
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

	st := s.st
	if st == nil {
		var openErr error
		st, openErr = store.Open(s.storePath())
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
