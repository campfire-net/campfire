package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
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
func (s *server) envelopedResponse(id interface{}, campfireID string, content interface{}) jsonRPCResponse {
	status := trust.TrustUnknown
	env := trust.BuildEnvelope(campfireID, status, content)
	envJSON, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("marshaling envelope: %v", err))
	}
	return okResponse(id, toolResult(string(envJSON)))
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
	return "", fmt.Errorf("campfire-key signing not yet implemented in MCP transport adapter")
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
