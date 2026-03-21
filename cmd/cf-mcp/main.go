// cmd/cf-mcp/main.go — Campfire MCP server
//
// Implements the Model Context Protocol (MCP) over stdio so that any
// MCP-compatible AI model can use Campfire as a coordination layer.
// Protocol: JSON-RPC 2.0, stdio transport, stateless between tool calls
// (identity is loaded from CF_HOME on each call).
//
// Usage:
//
//	cf-mcp [--cf-home <path>] [--beacon-dir <path>] [--http <addr>]
package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// ---------------------------------------------------------------------------
// JSON-RPC 2.0 types
// ---------------------------------------------------------------------------

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ---------------------------------------------------------------------------
// MCP types
// ---------------------------------------------------------------------------

type mcpToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type mcpServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type mcpCapabilities struct {
	Tools map[string]interface{} `json:"tools"`
}

// ---------------------------------------------------------------------------
// Server state
// ---------------------------------------------------------------------------

type server struct {
	cfHome          string
	beaconDir       string
	cfHomeExplicit  bool
	lockFile        *os.File
	sessManager     *SessionManager   // non-nil only in HTTP+session mode
	httpTransport   *cfhttp.Transport // non-nil when this server has an embedded HTTP transport
	transportRouter *TransportRouter  // non-nil in hosted HTTP mode (shared across sessions)
	externalAddr    string            // public URL of the hosted server (e.g. "http://localhost:8080")
	sessionToken    string            // non-empty in session mode; used for campfire ownership tracking in the router
	st              *store.Store      // non-nil in session mode; already-open store shared from Session
}

func (s *server) identityPath() string {
	return filepath.Join(s.cfHome, "identity.json")
}

func (s *server) storePath() string {
	return store.StorePath(s.cfHome)
}

// fsTransport returns a filesystem transport rooted at the correct base dir.
// In hosted HTTP mode, campfire state (campfire.cbor, members/) lives under
// the session's cfHome. In filesystem mode, it uses the shared transport dir.
func (s *server) fsTransport() *fs.Transport {
	if s.httpTransport != nil {
		return fs.New(s.cfHome)
	}
	return fs.New(fs.DefaultBaseDir())
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func errResponse(id interface{}, code int, msg string) jsonRPCResponse {
	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: msg},
	}
}

func okResponse(id interface{}, result interface{}) jsonRPCResponse {
	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
}

// getStr extracts a string param (returns "" if missing).
func getStr(params map[string]interface{}, key string) string {
	if v, ok := params[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// getStringSlice extracts a []string param (returns nil if missing).
func getStringSlice(params map[string]interface{}, key string) []string {
	if v, ok := params[key]; ok {
		switch t := v.(type) {
		case []interface{}:
			out := make([]string, 0, len(t))
			for _, item := range t {
				if s, ok := item.(string); ok {
					out = append(out, s)
				}
			}
			return out
		case []string:
			return t
		}
	}
	return nil
}

// getBool extracts a bool param.
func getBool(params map[string]interface{}, key string) bool {
	if v, ok := params[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// toolResult wraps text content for MCP tool response.
func toolResult(text string) map[string]interface{} {
	return map[string]interface{}{
		"content": []map[string]interface{}{
			{"type": "text", "text": text},
		},
	}
}

func toolResultJSON(v interface{}) (map[string]interface{}, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return toolResult(string(b)), nil
}

// campfireFromState reconstructs a Campfire for membership hash computation.
func campfireFromState(state *campfire.CampfireState, members []campfire.MemberRecord) *campfire.Campfire {
	return state.ToCampfire(members)
}

// ---------------------------------------------------------------------------
// Tool definitions
// ---------------------------------------------------------------------------

var tools []mcpToolInfo

func init() {
	tools = []mcpToolInfo{
		{
			Name:        "campfire_init",
			Description: "Generate a campfire identity. No name = disposable session identity. With name = persistent agent identity that survives across sessions.",
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Persistent agent name (e.g. 'worker-1'). Omit for a disposable session identity.",
					},
					"force": map[string]interface{}{
						"type":        "boolean",
						"description": "Overwrite existing identity",
					},
				},
				"required": []string{},
			}),
		},
		{
			Name:        "campfire_id",
			Description: "Show this agent's public key.",
			InputSchema: mustJSON(map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
				"required":   []string{},
			}),
		},
		{
			Name:        "campfire_create",
			Description: "Create a new campfire. Returns the campfire_id.",
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"protocol": map[string]interface{}{
						"type":        "string",
						"description": "Join protocol: open (default) or invite-only",
						"enum":        []string{"open", "invite-only"},
					},
					"require": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Reception requirements (required message tags)",
					},
					"description": map[string]interface{}{
						"type":        "string",
						"description": "Human-readable campfire description",
					},
				},
				"required": []string{},
			}),
		},
		{
			Name:        "campfire_join",
			Description: "Join an existing campfire by its campfire_id.",
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"campfire_id": map[string]interface{}{
						"type":        "string",
						"description": "Campfire ID (hex public key)",
					},
				},
				"required": []string{"campfire_id"},
			}),
		},
		{
			Name:        "campfire_send",
			Description: "Send a message to a campfire. Supports future messages, fulfillment references, and reply-to chains.",
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"campfire_id": map[string]interface{}{
						"type":        "string",
						"description": "Campfire ID (hex public key)",
					},
					"message": map[string]interface{}{
						"type":        "string",
						"description": "Message payload text",
					},
					"tags": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Message tags",
					},
					"reply_to": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Message IDs this message replies to (causal dependencies / DAG parents)",
					},
					"future": map[string]interface{}{
						"type":        "boolean",
						"description": "Tag this message as a future (commitment to respond)",
					},
					"fulfills": map[string]interface{}{
						"type":        "string",
						"description": "Message ID this message fulfills (adds fulfills tag + reply-to in one step)",
					},
					"instance": map[string]interface{}{
						"type":        "string",
						"description": "Sender instance/role name (tainted, not verified). E.g. 'strategist', 'cfo'.",
					},
				},
				"required": []string{"campfire_id", "message"},
			}),
		},
		{
			Name:        "campfire_read",
			Description: "Read messages from a campfire (or all campfires). Returns unread by default.",
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"campfire_id": map[string]interface{}{
						"type":        "string",
						"description": "Campfire ID to read from (omit for all campfires)",
					},
					"all": map[string]interface{}{
						"type":        "boolean",
						"description": "Return all messages, not just unread",
					},
					"peek": map[string]interface{}{
						"type":        "boolean",
						"description": "Return unread without advancing the read cursor",
					},
				},
				"required": []string{},
			}),
		},
		{
			Name:        "campfire_inspect",
			Description: "Inspect a message: full provenance chain, DAG context, signature verification.",
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"message_id": map[string]interface{}{
						"type":        "string",
						"description": "Message ID to inspect",
					},
				},
				"required": []string{"message_id"},
			}),
		},
		{
			Name:        "campfire_discover",
			Description: "List campfire beacons visible from this agent.",
			InputSchema: mustJSON(map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
				"required":   []string{},
			}),
		},
		{
			Name:        "campfire_ls",
			Description: "List campfires this agent is a member of.",
			InputSchema: mustJSON(map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
				"required":   []string{},
			}),
		},
		{
			Name:        "campfire_members",
			Description: "List members of a campfire.",
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"campfire_id": map[string]interface{}{
						"type":        "string",
						"description": "Campfire ID",
					},
				},
				"required": []string{"campfire_id"},
			}),
		},
		{
			Name:        "campfire_dm",
			Description: "Send a private message to another agent. Creates or reuses a 2-member invite-only campfire.",
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"target_key": map[string]interface{}{
						"type":        "string",
						"description": "Target agent public key (hex)",
					},
					"message": map[string]interface{}{
						"type":        "string",
						"description": "Message payload text",
					},
					"tags": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Message tags",
					},
				},
				"required": []string{"target_key", "message"},
			}),
		},
		{
			Name:        "campfire_await",
			Description: "Block until a future message is fulfilled. Polls the campfire for a message with the 'fulfills' tag whose antecedents include the target message ID. Returns the fulfilling message. Useful for in-session escalation without losing context.",
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"campfire_id": map[string]interface{}{
						"type":        "string",
						"description": "Campfire ID (hex public key)",
					},
					"msg_id": map[string]interface{}{
						"type":        "string",
						"description": "Message ID to await fulfillment of",
					},
					"timeout": map[string]interface{}{
						"type":        "string",
						"description": "Maximum time to wait (e.g. '30s', '5m', '1h'). Omit to wait indefinitely.",
					},
				},
				"required": []string{"campfire_id", "msg_id"},
			}),
		},
		{
			Name:        "campfire_trust",
			Description: "Set a human-readable pet name for an agent public key, or look up the resolved display name.",
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"public_key": map[string]interface{}{
						"type":        "string",
						"description": "Agent public key hex to name",
					},
					"label": map[string]interface{}{
						"type":        "string",
						"description": "Pet name to assign (omit to just resolve the current display name)",
					},
				},
				"required": []string{"public_key"},
			}),
		},
		{
			Name:        "campfire_export",
			Description: "Export this agent's session directory as a base64-encoded tar.gz. Contains identity.json, store.db, and campfire state files. Drop the contents into a local CF_HOME directory to migrate to a self-hosted cf-mcp or standalone cf binary.",
			InputSchema: mustJSON(map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
				"required":   []string{},
			}),
		},
	}
}

func mustJSON(v interface{}) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return json.RawMessage(b)
}

// ---------------------------------------------------------------------------
// Tool handlers
// ---------------------------------------------------------------------------

func (s *server) handleInit(id interface{}, params map[string]interface{}) jsonRPCResponse {
	name := getStr(params, "name")

	// Named identity: persistent agent across sessions
	// No name: session-scoped identity (disposable)
	if name != "" {
		home, _ := os.UserHomeDir()
		namedHome := filepath.Join(home, ".campfire", "agents", name)
		if err := os.MkdirAll(namedHome, 0700); err != nil {
			return errResponse(id, -32000, fmt.Sprintf("creating named identity dir: %v", err))
		}
		lockPath := filepath.Join(namedHome, "lock")
		lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return errResponse(id, -32000, fmt.Sprintf("opening lock file: %v", err))
		}
		if err := tryFlock(lf.Fd()); err != nil {
			lf.Close()
			return errResponse(id, -32000, fmt.Sprintf("identity '%s' is already in use by another session", name))
		}
		lf.Truncate(0)
		lf.Seek(0, 0)
		fmt.Fprintf(lf, "%d\n", os.Getpid())
		s.lockFile = lf
		s.cfHome = namedHome
	} else if !s.cfHomeExplicit {
		tmpDir, err := os.MkdirTemp("", "campfire-session-*")
		if err != nil {
			return errResponse(id, -32000, fmt.Sprintf("creating session temp dir: %v", err))
		}
		s.cfHome = tmpDir
	}

	force := getBool(params, "force")
	path := s.identityPath()

	status := "created"
	var agentID *identity.Identity

	if identity.Exists(path) && !force {
		var err error
		agentID, err = identity.Load(path)
		if err != nil {
			return errResponse(id, -32000, fmt.Sprintf("loading identity: %v", err))
		}
		status = "exists"
	} else {
		var err error
		agentID, err = identity.Generate()
		if err != nil {
			return errResponse(id, -32000, fmt.Sprintf("generating identity: %v", err))
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return errResponse(id, -32000, fmt.Sprintf("creating directory: %v", err))
		}
		if err := agentID.Save(path); err != nil {
			return errResponse(id, -32000, fmt.Sprintf("saving identity: %v", err))
		}
	}

	identityType := "session (disposable)"
	if name != "" {
		identityType = fmt.Sprintf("persistent agent '%s'", name)
	}

	guide := fmt.Sprintf(`Identity %s: %s
Type: %s
Location: %s

You are now a campfire identity. Next steps:
- campfire_discover() — find campfires via beacons
- campfire_join({campfire_id: "..."}) — join one
- campfire_create({description: "..."}) — start one
- campfire_send / campfire_read — communicate

Identity model:
- Session identity (no name): disposable, other agents won't recognize you next time.
- Named identity (name: "worker-1"): persistent, survives across sessions. Use when others need to recognize you.`,
		status, agentID.PublicKeyHex(), identityType, s.cfHome)

	return okResponse(id, toolResult(guide))
}

func (s *server) handleID(id interface{}, _ map[string]interface{}) jsonRPCResponse {
	agentID, err := identity.Load(s.identityPath())
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("loading identity (run campfire_init first): %v", err))
	}
	result, _ := toolResultJSON(map[string]string{"public_key": agentID.PublicKeyHex()})
	return okResponse(id, result)
}

func (s *server) handleCreate(id interface{}, params map[string]interface{}) jsonRPCResponse {
	protocol := getStr(params, "protocol")
	if protocol == "" {
		protocol = "open"
	}
	require := getStringSlice(params, "require")
	description := getStr(params, "description")

	agentID, err := identity.Load(s.identityPath())
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("loading identity (run campfire_init first): %v", err))
	}

	cf, err := campfire.New(protocol, require, 1)
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("creating campfire: %v", err))
	}

	cf.AddMember(agentID.PublicKey)

	// In hosted HTTP mode, use the session's local fs for campfire state and
	// the HTTP transport for message delivery. Beacons point to the server URL.
	if s.httpTransport != nil {
		return s.handleCreateHTTP(id, cf, agentID, description)
	}

	transport := s.fsTransport()
	if err := transport.Init(cf); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("initializing transport: %v", err))
	}

	if err := transport.WriteMember(cf.PublicKeyHex(), campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
	}); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("writing member record: %v", err))
	}

	b, err := beacon.New(
		cf.PublicKey, cf.PrivateKey,
		cf.JoinProtocol, cf.ReceptionRequirements,
		beacon.TransportConfig{
			Protocol: "filesystem",
			Config:   map[string]string{"dir": transport.CampfireDir(cf.PublicKeyHex())},
		},
		description,
	)
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("creating beacon: %v", err))
	}
	if err := beacon.Publish(s.beaconDir, b); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("publishing beacon: %v", err))
	}

	st, err := store.Open(s.storePath())
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("opening store: %v", err))
	}
	defer st.Close()

	if err := st.AddMembership(store.Membership{
		CampfireID:   cf.PublicKeyHex(),
		TransportDir: transport.CampfireDir(cf.PublicKeyHex()),
		JoinProtocol: cf.JoinProtocol,
		Role:         store.PeerRoleCreator,
		JoinedAt:     store.NowNano(),
	}); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("recording membership: %v", err))
	}

	result, _ := toolResultJSON(map[string]interface{}{
		"campfire_id":            cf.PublicKeyHex(),
		"join_protocol":          cf.JoinProtocol,
		"reception_requirements": cf.ReceptionRequirements,
		"transport_dir":          transport.CampfireDir(cf.PublicKeyHex()),
	})
	return okResponse(id, result)
}

// handleCreateHTTP is the hosted HTTP mode path for campfire creation.
// It stores campfire state in the session's local filesystem, publishes an
// HTTP transport beacon, registers the campfire with the transport router,
// and sets up the HTTP transport so external peers can reach this campfire.
func (s *server) handleCreateHTTP(id interface{}, cf *campfire.Campfire, agentID *identity.Identity, description string) jsonRPCResponse {
	// Use the session's cfHome as the fs transport base for state storage.
	fsTransport := fs.New(s.cfHome)
	if err := fsTransport.Init(cf); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("initializing campfire state: %v", err))
	}

	now := time.Now().UnixNano()
	if err := fsTransport.WriteMember(cf.PublicKeyHex(), campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  now,
	}); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("writing member record: %v", err))
	}

	// Publish beacon with HTTP transport config pointing to the server URL.
	b, err := beacon.New(
		cf.PublicKey, cf.PrivateKey,
		cf.JoinProtocol, cf.ReceptionRequirements,
		beacon.TransportConfig{
			Protocol: "p2p-http",
			Config:   map[string]string{"endpoint": s.externalAddr},
		},
		description,
	)
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("creating beacon: %v", err))
	}
	if err := beacon.Publish(s.beaconDir, b); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("publishing beacon: %v", err))
	}

	// Store membership in SQLite.
	st, err := store.Open(s.storePath())
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("opening store: %v", err))
	}
	defer st.Close()

	if err := st.AddMembership(store.Membership{
		CampfireID:    cf.PublicKeyHex(),
		TransportDir:  s.externalAddr,
		TransportType: "p2p-http",
		JoinProtocol:  cf.JoinProtocol,
		Role:          store.PeerRoleCreator,
		JoinedAt:      now,
		CreatorPubkey: agentID.PublicKeyHex(),
	}); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("recording membership: %v", err))
	}

	// Configure the HTTP transport: set self info so join responses include
	// this node's identity, and register a key provider for the join handler.
	s.httpTransport.SetSelfInfo(agentID.PublicKeyHex(), s.externalAddr)
	s.httpTransport.SetKeyProvider(func(campfireID string) (privKey []byte, pubKey []byte, err error) {
		state, err := fsTransport.ReadState(campfireID)
		if err != nil {
			return nil, nil, err
		}
		return state.PrivateKey, state.PublicKey, nil
	})

	// Register self as a peer so membership checks pass for self-authored messages.
	if err := st.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   cf.PublicKeyHex(),
		MemberPubkey: agentID.PublicKeyHex(),
		Endpoint:     s.externalAddr,
		Role:         store.PeerRoleCreator,
	}); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("adding self as peer: %v", err))
	}

	// Register this campfire with the transport router so external peers
	// can reach it via the hosted server's /campfire/ routes. Use
	// RegisterForSession so UnregisterSession can clean up on reap.
	if s.transportRouter != nil {
		s.transportRouter.RegisterForSession(cf.PublicKeyHex(), s.sessionToken, s.httpTransport)
	}

	result, _ := toolResultJSON(map[string]interface{}{
		"campfire_id":            cf.PublicKeyHex(),
		"join_protocol":          cf.JoinProtocol,
		"reception_requirements": cf.ReceptionRequirements,
		"transport":              "p2p-http",
		"endpoint":               s.externalAddr,
	})
	return okResponse(id, result)
}

func (s *server) handleJoin(id interface{}, params map[string]interface{}) jsonRPCResponse {
	campfireID := getStr(params, "campfire_id")
	if campfireID == "" {
		return errResponse(id, -32602, "campfire_id is required")
	}

	agentID, err := identity.Load(s.identityPath())
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("loading identity: %v", err))
	}

	st, err := store.Open(s.storePath())
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("opening store: %v", err))
	}
	defer st.Close()

	transport := s.fsTransport()

	state, err := transport.ReadState(campfireID)
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("reading campfire state: %v", err))
	}

	members, err := transport.ListMembers(campfireID)
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("listing members: %v", err))
	}

	alreadyOnDisk := false
	var existingJoinedAt int64
	for _, m := range members {
		if fmt.Sprintf("%x", m.PublicKey) == agentID.PublicKeyHex() {
			alreadyOnDisk = true
			existingJoinedAt = m.JoinedAt
			break
		}
	}

	existingMembership, _ := st.GetMembership(campfireID)
	if existingMembership != nil {
		return errResponse(id, -32000, "already a member of this campfire")
	}

	now := time.Now().UnixNano()

	if alreadyOnDisk {
		now = existingJoinedAt
	} else {
		switch state.JoinProtocol {
		case "open":
			// immediately admitted
		case "invite-only":
			return errResponse(id, -32000, fmt.Sprintf("campfire is invite-only; ask a member to run 'cf admit %s %s'", campfireID[:12], agentID.PublicKeyHex()))
		default:
			return errResponse(id, -32000, fmt.Sprintf("unknown join protocol: %s", state.JoinProtocol))
		}

		if err := transport.WriteMember(campfireID, campfire.MemberRecord{
			PublicKey: agentID.PublicKey,
			JoinedAt:  now,
		}); err != nil {
			return errResponse(id, -32000, fmt.Sprintf("writing member record: %v", err))
		}
	}

	if !alreadyOnDisk {
		sysMsg, err := message.NewMessage(
			state.PrivateKey, state.PublicKey,
			[]byte(fmt.Sprintf(`{"member":"%s","joined_at":%d}`, agentID.PublicKeyHex(), now)),
			[]string{"campfire:member-joined"},
			nil,
		)
		if err != nil {
			return errResponse(id, -32000, fmt.Sprintf("creating system message: %v", err))
		}

		updatedMembers, _ := transport.ListMembers(campfireID)
		cf := campfireFromState(state, updatedMembers)
		if err := sysMsg.AddHop(
			state.PrivateKey, state.PublicKey,
			cf.MembershipHash(), len(updatedMembers),
			state.JoinProtocol, state.ReceptionRequirements,
		); err != nil {
			return errResponse(id, -32000, fmt.Sprintf("adding provenance hop: %v", err))
		}

		if err := transport.WriteMessage(campfireID, sysMsg); err != nil {
			return errResponse(id, -32000, fmt.Sprintf("writing system message: %v", err))
		}
	}

	if err := st.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: transport.CampfireDir(campfireID),
		JoinProtocol: state.JoinProtocol,
		Role:         campfire.RoleFull,
		JoinedAt:     now,
	}); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("recording membership: %v", err))
	}

	result, _ := toolResultJSON(map[string]string{
		"campfire_id": campfireID,
		"status":      "joined",
	})
	return okResponse(id, result)
}

func (s *server) handleSend(id interface{}, params map[string]interface{}) jsonRPCResponse {
	campfireID := getStr(params, "campfire_id")
	payload := getStr(params, "message")
	if campfireID == "" || payload == "" {
		return errResponse(id, -32602, "campfire_id and message are required")
	}

	tags := getStringSlice(params, "tags")
	// Accept both "reply_to" (canonical) and "antecedents" (backward-compat alias).
	antecedents := getStringSlice(params, "reply_to")
	if legacy := getStringSlice(params, "antecedents"); len(legacy) > 0 {
		antecedents = append(antecedents, legacy...)
	}
	future := getBool(params, "future")
	fulfills := getStr(params, "fulfills")
	instance := getStr(params, "instance")

	agentID, err := identity.Load(s.identityPath())
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("loading identity: %v", err))
	}

	st, err := store.Open(s.storePath())
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("opening store: %v", err))
	}
	defer st.Close()

	m, err := st.GetMembership(campfireID)
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("querying membership: %v", err))
	}
	if m == nil {
		return errResponse(id, -32000, fmt.Sprintf("not a member of campfire %s", campfireID[:12]))
	}

	fsT := s.fsTransport()

	members, err := fsT.ListMembers(campfireID)
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("listing members: %v", err))
	}

	isMember := false
	for _, mem := range members {
		if fmt.Sprintf("%x", mem.PublicKey) == agentID.PublicKeyHex() {
			isMember = true
			break
		}
	}
	if !isMember {
		return errResponse(id, -32000, "not recognized as a member in the transport directory")
	}

	if future {
		tags = append(tags, "future")
	}
	if fulfills != "" {
		tags = append(tags, "fulfills")
		antecedents = append(antecedents, fulfills)
	}

	msg, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, []byte(payload), tags, antecedents)
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("creating message: %v", err))
	}
	msg.Instance = instance // tainted metadata, not covered by signature

	state, err := fsT.ReadState(campfireID)
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("reading campfire state: %v", err))
	}

	cf := campfireFromState(state, members)
	if err := msg.AddHop(
		state.PrivateKey, state.PublicKey,
		cf.MembershipHash(), len(members),
		state.JoinProtocol, state.ReceptionRequirements,
	); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("adding provenance hop: %v", err))
	}

	if s.httpTransport != nil {
		// HTTP mode: store in SQLite and deliver to HTTP peers.
		if _, err := st.AddMessage(store.MessageRecordFromMessage(campfireID, msg, store.NowNano())); err != nil {
			return errResponse(id, -32000, fmt.Sprintf("storing message: %v", err))
		}
		// Wake any long-polling goroutines (hosted and external peers).
		s.httpTransport.PollBrokerNotify(campfireID)
		// Deliver to external HTTP peers.
		peers := s.httpTransport.Peers(campfireID)
		if len(peers) > 0 {
			endpoints := make([]string, len(peers))
			for i, p := range peers {
				endpoints[i] = p.Endpoint
			}
			cfhttp.DeliverToAll(endpoints, campfireID, msg, agentID)
		}
	} else {
		if err := fsT.WriteMessage(campfireID, msg); err != nil {
			return errResponse(id, -32000, fmt.Sprintf("writing message: %v", err))
		}
	}

	result, _ := toolResultJSON(map[string]interface{}{
		"id":          msg.ID,
		"campfire_id": campfireID,
		"sender":      agentID.PublicKeyHex(),
		"payload":     payload,
		"tags":        msg.Tags,
		"antecedents": msg.Antecedents,
		"timestamp":   msg.Timestamp,
		"instance":    msg.Instance,
	})
	return okResponse(id, result)
}

func (s *server) handleRead(id interface{}, params map[string]interface{}) jsonRPCResponse {
	campfireID := getStr(params, "campfire_id")
	readAll := getBool(params, "all")
	readPeek := getBool(params, "peek")

	st, err := store.Open(s.storePath())
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("opening store: %v", err))
	}
	defer st.Close()

	var campfireIDs []string
	if campfireID != "" {
		campfireIDs = []string{campfireID}
	} else {
		memberships, err := st.ListMemberships()
		if err != nil {
			return errResponse(id, -32000, fmt.Sprintf("listing memberships: %v", err))
		}
		for _, m := range memberships {
			campfireIDs = append(campfireIDs, m.CampfireID)
		}
	}

	// Sync from filesystem (skip in HTTP mode — messages are already in SQLite
	// via the HTTP transport's handleDeliver or via handleSend).
	if s.httpTransport == nil {
		fsTransport := fs.New(fs.DefaultBaseDir())
		for _, cfID := range campfireIDs {
			fsMessages, err := fsTransport.ListMessages(cfID)
			if err != nil {
				continue
			}
			for _, fsMsg := range fsMessages {
				st.AddMessage(store.MessageRecordFromMessage(cfID, &fsMsg, store.NowNano())) //nolint:errcheck
			}
		}
	}

	// Collect messages
	var allMessages []store.MessageRecord
	for _, cfID := range campfireIDs {
		var afterTS int64
		if !readAll {
			afterTS, _ = st.GetReadCursor(cfID)
		}
		msgs, err := st.ListMessages(cfID, afterTS)
		if err != nil {
			return errResponse(id, -32000, fmt.Sprintf("listing messages: %v", err))
		}
		allMessages = append(allMessages, msgs...)
	}

	type jsonMsg struct {
		ID          string                  `json:"id"`
		CampfireID  string                  `json:"campfire_id"`
		Sender      string                  `json:"sender"`
		Instance    string                  `json:"instance,omitempty"`
		Payload     string                  `json:"payload"`
		Tags        []string                `json:"tags"`
		Antecedents []string                `json:"antecedents"`
		Timestamp   int64                   `json:"timestamp"`
		Provenance  []message.ProvenanceHop `json:"provenance"`
	}
	var out []jsonMsg
	for _, m := range allMessages {
		tags := m.Tags
		if tags == nil {
			tags = []string{}
		}
		ants := m.Antecedents
		if ants == nil {
			ants = []string{}
		}
		prov := m.Provenance
		if prov == nil {
			prov = []message.ProvenanceHop{}
		}
		out = append(out, jsonMsg{
			ID:          m.ID,
			CampfireID:  m.CampfireID,
			Sender:      m.Sender,
			Instance:    m.Instance,
			Payload:     string(m.Payload),
			Tags:        tags,
			Antecedents: ants,
			Timestamp:   m.Timestamp,
			Provenance:  prov,
		})
	}
	if out == nil {
		out = []jsonMsg{}
	}

	// Advance cursors unless all/peek
	if !readAll && !readPeek && len(allMessages) > 0 {
		cursors := map[string]int64{}
		for _, m := range allMessages {
			if m.Timestamp > cursors[m.CampfireID] {
				cursors[m.CampfireID] = m.Timestamp
			}
		}
		for cfID, ts := range cursors {
			st.SetReadCursor(cfID, ts)
		}
	}

	result, _ := toolResultJSON(out)
	return okResponse(id, result)
}

func (s *server) handleAwait(id interface{}, params map[string]interface{}) jsonRPCResponse {
	campfireID := getStr(params, "campfire_id")
	targetMsgID := getStr(params, "msg_id")
	timeoutStr := getStr(params, "timeout")

	if campfireID == "" || targetMsgID == "" {
		return errResponse(id, -32602, "campfire_id and msg_id are required")
	}

	var timeout time.Duration
	if timeoutStr != "" {
		var err error
		timeout, err = time.ParseDuration(timeoutStr)
		if err != nil {
			return errResponse(id, -32602, fmt.Sprintf("invalid timeout: %v", err))
		}
	} else {
		// Default to 10 minutes for MCP (avoid indefinite blocking).
		timeout = 10 * time.Minute
	}

	// In session mode, reuse the already-open store to avoid a second SQLite
	// connection to the same file. Otherwise open a dedicated connection.
	st := s.st
	if st == nil {
		var err error
		st, err = store.Open(s.storePath())
		if err != nil {
			return errResponse(id, -32000, fmt.Sprintf("opening store: %v", err))
		}
		defer st.Close()
	}

	// In HTTP mode, use PollBroker for efficient notification instead of
	// filesystem polling. The PollBroker wakes this goroutine when a new
	// message is delivered via the HTTP transport.
	if s.httpTransport != nil {
		return s.handleAwaitHTTP(id, st, campfireID, targetMsgID, timeout)
	}

	fsTransport := fs.New(fs.DefaultBaseDir())
	interval := 2 * time.Second
	deadline := time.After(timeout)

	// Initial sync and check.
	if msgs, err := fsTransport.ListMessages(campfireID); err == nil {
		for _, m := range msgs {
			st.AddMessage(store.MessageRecordFromMessage(campfireID, &m, store.NowNano())) //nolint:errcheck
		}
	}
	if msg := findMCPFulfillment(st, campfireID, targetMsgID); msg != nil {
		result, _ := toolResultJSON(msg)
		return okResponse(id, result)
	}

	// Poll loop.
	for {
		select {
		case <-deadline:
			return errResponse(id, -32000, "timeout: no fulfillment received")
		case <-time.After(interval):
		}

		if msgs, err := fsTransport.ListMessages(campfireID); err == nil {
			for _, m := range msgs {
				st.AddMessage(store.MessageRecordFromMessage(campfireID, &m, store.NowNano())) //nolint:errcheck
			}
		}
		if msg := findMCPFulfillment(st, campfireID, targetMsgID); msg != nil {
			result, _ := toolResultJSON(msg)
			return okResponse(id, result)
		}
	}
}

// httpAwaitChunkDuration is the maximum time a single handleAwaitHTTP call
// blocks before returning a "pending" status. Capped below typical MCP gateway
// timeouts (30-120s) so agents always receive a well-typed response instead of
// an HTTP-level timeout.
const httpAwaitChunkDuration = 30 * time.Second

// handleAwaitHTTP uses the PollBroker for efficient notification-driven await
// instead of filesystem polling. Each call blocks for at most httpAwaitChunkDuration
// (30s), then returns a structured status:
//
//   - fulfilled: {"status":"fulfilled","message":{...}}
//   - pending:   {"status":"pending","elapsed":"30s","remaining":"4m30s","retry":true}
//   - timeout:   {"status":"timeout","elapsed":"5m","remaining":"0s"}
//   - error:     {"status":"error","message":"campfire not found"}
//
// The agent (or MCP gateway) retries on pending. The full timeout is enforced
// across retries by passing the decremented timeout on each call.
func (s *server) handleAwaitHTTP(id interface{}, st *store.Store, campfireID, targetMsgID string, timeout time.Duration) jsonRPCResponse {
	// Validate that the campfire is registered on this server. This catches
	// misspelled or non-existent campfire IDs before blocking.
	if s.transportRouter != nil && s.transportRouter.GetCampfireTransport(campfireID) == nil {
		result, _ := toolResultJSON(map[string]interface{}{
			"status":  "error",
			"message": fmt.Sprintf("campfire not found: %s", campfireID),
		})
		return okResponse(id, result)
	}

	// Check if already fulfilled before blocking.
	if msg := findMCPFulfillment(st, campfireID, targetMsgID); msg != nil {
		result, _ := awaitStatusFulfilled(msg)
		return okResponse(id, result)
	}

	// If the caller's remaining timeout is zero or negative, it has expired.
	if timeout <= 0 {
		result, _ := toolResultJSON(map[string]interface{}{
			"status":    "timeout",
			"elapsed":   "0s",
			"remaining": "0s",
		})
		return okResponse(id, result)
	}

	// Cap this poll chunk at httpAwaitChunkDuration.
	chunkDur := timeout
	if chunkDur > httpAwaitChunkDuration {
		chunkDur = httpAwaitChunkDuration
	}

	start := time.Now()

	// Subscribe to PollBroker for this campfire.
	ch, dereg, err := s.httpTransport.PollBrokerSubscribe(campfireID)
	if err != nil {
		result, _ := toolResultJSON(map[string]interface{}{
			"status":  "error",
			"message": fmt.Sprintf("subscribing to poll broker: %v", err),
		})
		return okResponse(id, result)
	}
	defer dereg()

	// Block until a message arrives, the chunk expires, or the full timeout expires.
	select {
	case <-time.After(chunkDur):
		// Chunk expired without fulfillment.
		elapsed := time.Since(start).Round(time.Second)
		remaining := timeout - elapsed
		if remaining < 0 {
			remaining = 0
		}
		if remaining == 0 {
			result, _ := toolResultJSON(map[string]interface{}{
				"status":    "timeout",
				"elapsed":   elapsed.String(),
				"remaining": "0s",
			})
			return okResponse(id, result)
		}
		result, _ := toolResultJSON(map[string]interface{}{
			"status":    "pending",
			"elapsed":   elapsed.String(),
			"remaining": remaining.String(),
			"retry":     true,
		})
		return okResponse(id, result)

	case <-ch:
		// PollBroker fired — check for fulfillment.
		if msg := findMCPFulfillment(st, campfireID, targetMsgID); msg != nil {
			result, _ := awaitStatusFulfilled(msg)
			return okResponse(id, result)
		}
		// Message arrived but was not the fulfilling one. Return pending so the
		// agent retries immediately with the remaining timeout.
		elapsed := time.Since(start).Round(time.Second)
		remaining := timeout - elapsed
		if remaining < 0 {
			remaining = 0
		}
		result, _ := toolResultJSON(map[string]interface{}{
			"status":    "pending",
			"elapsed":   elapsed.String(),
			"remaining": remaining.String(),
			"retry":     true,
		})
		return okResponse(id, result)
	}
}

// awaitStatusFulfilled returns a structured "fulfilled" tool result wrapping
// the fulfilling message. The message is nested under the "message" key so
// callers can distinguish fulfilled from pending without parsing "status".
func awaitStatusFulfilled(msg *map[string]interface{}) (map[string]interface{}, error) {
	return toolResultJSON(map[string]interface{}{
		"status":  "fulfilled",
		"message": msg,
	})
}

// findMCPFulfillment searches for a message with the "fulfills" tag whose
// antecedents contain the target message ID.
func findMCPFulfillment(st *store.Store, campfireID, targetMsgID string) *map[string]interface{} {
	msgs, err := st.ListMessages(campfireID, 0, store.MessageFilter{
		Tags: []string{"fulfills"},
	})
	if err != nil {
		return nil
	}
	for _, m := range msgs {
		for _, ant := range m.Antecedents {
			if ant == targetMsgID {
				tags := m.Tags
				if tags == nil {
					tags = []string{}
				}
				ants := m.Antecedents
				if ants == nil {
					ants = []string{}
				}
				result := map[string]interface{}{
					"id":          m.ID,
					"campfire_id": m.CampfireID,
					"sender":      m.Sender,
					"instance":    m.Instance,
					"payload":     string(m.Payload),
					"tags":        tags,
					"antecedents": ants,
					"timestamp":   m.Timestamp,
				}
				return &result
			}
		}
	}
	return nil
}

func (s *server) handleInspect(id interface{}, params map[string]interface{}) jsonRPCResponse {
	messageID := getStr(params, "message_id")
	if messageID == "" {
		return errResponse(id, -32602, "message_id is required")
	}

	st, err := store.Open(s.storePath())
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("opening store: %v", err))
	}
	defer st.Close()

	msg, err := st.GetMessage(messageID)
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("querying message: %v", err))
	}
	if msg == nil {
		return errResponse(id, -32000, fmt.Sprintf("message %s not found (run campfire_read first to sync)", messageID))
	}

	// Tags, Antecedents, and Provenance are typed Go values on MessageRecord
	// (JSON deserialization happens at the store boundary), so no unmarshal needed.
	provenance := msg.Provenance
	antecedents := msg.Antecedents
	if antecedents == nil {
		antecedents = []string{}
	}

	refs, _ := st.ListReferencingMessages(messageID)
	var referencedBy []string
	for _, ref := range refs {
		referencedBy = append(referencedBy, ref.ID)
	}
	if referencedBy == nil {
		referencedBy = []string{}
	}

	msgSigValid := message.VerifyMessageSignature(messageID, msg.Payload, msg.Tags, msg.Antecedents, msg.Timestamp, msg.Sender, msg.Signature)

	tags := msg.Tags
	if tags == nil {
		tags = []string{}
	}

	type hopJSON struct {
		CampfireID            string   `json:"campfire_id"`
		MembershipHash        string   `json:"membership_hash"`
		MemberCount           int      `json:"member_count"`
		JoinProtocol          string   `json:"join_protocol"`
		ReceptionRequirements []string `json:"reception_requirements"`
		Timestamp             int64    `json:"timestamp"`
		SignatureValid        bool     `json:"signature_valid"`
	}
	type inspectJSON struct {
		ID             string    `json:"id"`
		CampfireID     string    `json:"campfire_id"`
		Sender         string    `json:"sender"`
		Payload        string    `json:"payload"`
		Tags           []string  `json:"tags"`
		Antecedents    []string  `json:"antecedents"`
		ReferencedBy   []string  `json:"referenced_by"`
		Timestamp      int64     `json:"timestamp"`
		SignatureValid bool      `json:"signature_valid"`
		Provenance     []hopJSON `json:"provenance"`
	}

	out := inspectJSON{
		ID:             msg.ID,
		CampfireID:     msg.CampfireID,
		Sender:         msg.Sender,
		Payload:        string(msg.Payload),
		Tags:           tags,
		Antecedents:    antecedents,
		ReferencedBy:   referencedBy,
		Timestamp:      msg.Timestamp,
		SignatureValid: msgSigValid,
	}
	for _, hop := range provenance {
		hopValid := message.VerifyHop(messageID, hop)
		out.Provenance = append(out.Provenance, hopJSON{
			CampfireID:            fmt.Sprintf("%x", hop.CampfireID),
			MembershipHash:        fmt.Sprintf("%x", hop.MembershipHash),
			MemberCount:           hop.MemberCount,
			JoinProtocol:          hop.JoinProtocol,
			ReceptionRequirements: hop.ReceptionRequirements,
			Timestamp:             hop.Timestamp,
			SignatureValid:        hopValid,
		})
	}
	if out.Provenance == nil {
		out.Provenance = []hopJSON{}
	}

	result, _ := toolResultJSON(out)
	return okResponse(id, result)
}

func (s *server) handleDiscover(id interface{}, _ map[string]interface{}) jsonRPCResponse {
	beacons, err := beacon.Scan(s.beaconDir)
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("scanning beacons: %v", err))
	}

	type entry struct {
		CampfireID            string   `json:"campfire_id"`
		JoinProtocol          string   `json:"join_protocol"`
		ReceptionRequirements []string `json:"reception_requirements"`
		Transport             string   `json:"transport"`
		Description           string   `json:"description"`
		SignatureValid        bool     `json:"signature_valid"`
	}
	var entries []entry
	for _, b := range beacons {
		entries = append(entries, entry{
			CampfireID:            b.CampfireIDHex(),
			JoinProtocol:          b.JoinProtocol,
			ReceptionRequirements: b.ReceptionRequirements,
			Transport:             b.Transport.Protocol,
			Description:           b.Description,
			SignatureValid:        b.Verify(),
		})
	}
	if entries == nil {
		entries = []entry{}
	}

	result, _ := toolResultJSON(entries)
	return okResponse(id, result)
}

func (s *server) handleLS(id interface{}, _ map[string]interface{}) jsonRPCResponse {
	st, err := store.Open(s.storePath())
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("opening store: %v", err))
	}
	defer st.Close()

	memberships, err := st.ListMemberships()
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("listing memberships: %v", err))
	}

	transport := s.fsTransport()

	type entry struct {
		CampfireID   string `json:"campfire_id"`
		JoinProtocol string `json:"join_protocol"`
		Role         string `json:"role"`
		MemberCount  int    `json:"member_count"`
		JoinedAt     string `json:"joined_at"`
	}
	var entries []entry
	for _, m := range memberships {
		members, _ := transport.ListMembers(m.CampfireID)
		entries = append(entries, entry{
			CampfireID:   m.CampfireID,
			JoinProtocol: m.JoinProtocol,
			Role:         m.Role,
			MemberCount:  len(members),
			JoinedAt:     time.Unix(0, m.JoinedAt).Format(time.RFC3339),
		})
	}
	if entries == nil {
		entries = []entry{}
	}

	result, _ := toolResultJSON(entries)
	return okResponse(id, result)
}

func (s *server) handleMembers(id interface{}, params map[string]interface{}) jsonRPCResponse {
	campfireID := getStr(params, "campfire_id")
	if campfireID == "" {
		return errResponse(id, -32602, "campfire_id is required")
	}

	st, err := store.Open(s.storePath())
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("opening store: %v", err))
	}
	defer st.Close()

	m, err := st.GetMembership(campfireID)
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("querying membership: %v", err))
	}
	if m == nil {
		return errResponse(id, -32000, fmt.Sprintf("not a member of campfire %s", campfireID[:12]))
	}

	transport := s.fsTransport()
	members, err := transport.ListMembers(campfireID)
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("listing members: %v", err))
	}

	type entry struct {
		PublicKey string `json:"public_key"`
		JoinedAt  string `json:"joined_at"`
	}
	var entries []entry
	for _, mem := range members {
		entries = append(entries, entry{
			PublicKey: fmt.Sprintf("%x", mem.PublicKey),
			JoinedAt:  time.Unix(0, mem.JoinedAt).Format(time.RFC3339),
		})
	}
	if entries == nil {
		entries = []entry{}
	}

	result, _ := toolResultJSON(entries)
	return okResponse(id, result)
}

func (s *server) handleDM(id interface{}, params map[string]interface{}) jsonRPCResponse {
	targetHex := getStr(params, "target_key")
	payload := getStr(params, "message")
	if targetHex == "" || payload == "" {
		return errResponse(id, -32602, "target_key and message are required")
	}
	dmTags := getStringSlice(params, "tags")

	// Validate hex key
	if len(targetHex) != 64 {
		return errResponse(id, -32602, "target_key must be 64 hex characters (32 bytes)")
	}
	targetKey := make([]byte, 32)
	for i := 0; i < 32; i++ {
		var b byte
		fmt.Sscanf(targetHex[i*2:i*2+2], "%02x", &b)
		targetKey[i] = b
	}

	agentID, err := identity.Load(s.identityPath())
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("loading identity: %v", err))
	}

	if targetHex == agentID.PublicKeyHex() {
		return errResponse(id, -32000, "cannot DM yourself")
	}

	st, err := store.Open(s.storePath())
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("opening store: %v", err))
	}
	defer st.Close()

	transport := s.fsTransport()

	memberships, err := st.ListMemberships()
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("listing memberships: %v", err))
	}

	var existingCF string
	for _, mem := range memberships {
		if mem.JoinProtocol != "invite-only" {
			continue
		}
		members, err := transport.ListMembers(mem.CampfireID)
		if err != nil || len(members) != 2 {
			continue
		}
		for _, m := range members {
			if fmt.Sprintf("%x", m.PublicKey) == targetHex {
				existingCF = mem.CampfireID
				break
			}
		}
		if existingCF != "" {
			break
		}
	}

	var campfireID string

	if existingCF != "" {
		campfireID = existingCF
	} else {
		cf, err := campfire.New("invite-only", nil, 1)
		if err != nil {
			return errResponse(id, -32000, fmt.Sprintf("creating DM campfire: %v", err))
		}

		cf.AddMember(agentID.PublicKey)
		cf.AddMember(targetKey)

		if err := transport.Init(cf); err != nil {
			return errResponse(id, -32000, fmt.Sprintf("initializing transport: %v", err))
		}

		now := time.Now().UnixNano()

		if err := transport.WriteMember(cf.PublicKeyHex(), campfire.MemberRecord{
			PublicKey: agentID.PublicKey,
			JoinedAt:  now,
		}); err != nil {
			return errResponse(id, -32000, fmt.Sprintf("writing sender member record: %v", err))
		}
		if err := transport.WriteMember(cf.PublicKeyHex(), campfire.MemberRecord{
			PublicKey: targetKey,
			JoinedAt:  now,
		}); err != nil {
			return errResponse(id, -32000, fmt.Sprintf("writing target member record: %v", err))
		}

		var transportConfig beacon.TransportConfig
		if s.httpTransport != nil {
			transportConfig = beacon.TransportConfig{
				Protocol: "p2p-http",
				Config:   map[string]string{"endpoint": s.externalAddr},
			}
		} else {
			transportConfig = beacon.TransportConfig{
				Protocol: "filesystem",
				Config:   map[string]string{"dir": transport.CampfireDir(cf.PublicKeyHex())},
			}
		}
		b, err := beacon.New(
			cf.PublicKey, cf.PrivateKey,
			cf.JoinProtocol, cf.ReceptionRequirements,
			transportConfig,
			fmt.Sprintf("dm:%s:%s", agentID.PublicKeyHex()[:12], targetHex[:12]),
		)
		if err != nil {
			return errResponse(id, -32000, fmt.Sprintf("creating beacon: %v", err))
		}
		if err := beacon.Publish(s.beaconDir, b); err != nil {
			return errResponse(id, -32000, fmt.Sprintf("publishing beacon: %v", err))
		}

		transportDir := transport.CampfireDir(cf.PublicKeyHex())
		transportType := ""
		if s.httpTransport != nil {
			transportDir = s.externalAddr
			transportType = "p2p-http"
			// Register DM campfire with transport router.
			if s.transportRouter != nil {
				s.transportRouter.RegisterForSession(cf.PublicKeyHex(), s.sessionToken, s.httpTransport)
			}
		}
		if err := st.AddMembership(store.Membership{
			CampfireID:    cf.PublicKeyHex(),
			TransportDir:  transportDir,
			TransportType: transportType,
			JoinProtocol:  cf.JoinProtocol,
			Role:          store.PeerRoleCreator,
			JoinedAt:      now,
		}); err != nil {
			return errResponse(id, -32000, fmt.Sprintf("recording membership: %v", err))
		}

		campfireID = cf.PublicKeyHex()
	}

	msg, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, []byte(payload), dmTags, nil)
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("creating message: %v", err))
	}

	state, err := transport.ReadState(campfireID)
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("reading campfire state: %v", err))
	}

	members, err := transport.ListMembers(campfireID)
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("listing members: %v", err))
	}

	cf := campfireFromState(state, members)
	if err := msg.AddHop(
		state.PrivateKey, state.PublicKey,
		cf.MembershipHash(), len(members),
		state.JoinProtocol, state.ReceptionRequirements,
	); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("adding provenance hop: %v", err))
	}

	if s.httpTransport != nil {
		// HTTP mode: store in SQLite and deliver to peers.
		if _, err := st.AddMessage(store.MessageRecordFromMessage(campfireID, msg, store.NowNano())); err != nil {
			return errResponse(id, -32000, fmt.Sprintf("storing message: %v", err))
		}
		s.httpTransport.PollBrokerNotify(campfireID)
	} else {
		if err := transport.WriteMessage(campfireID, msg); err != nil {
			return errResponse(id, -32000, fmt.Sprintf("writing message: %v", err))
		}
	}

	result, _ := toolResultJSON(map[string]interface{}{
		"id":          msg.ID,
		"campfire_id": campfireID,
		"target":      targetHex,
		"payload":     payload,
		"reused":      existingCF != "",
	})
	return okResponse(id, result)
}

// handleExport tars the agent's session directory (cfHome) and returns it as
// a base64-encoded gzip tar archive. The tarball contains identity.json,
// store.db, and any campfire state files present. Dropping the contents into
// a local CF_HOME directory and running `cf id` will show the same public key.
func (s *server) handleExport(id interface{}, _ map[string]interface{}) jsonRPCResponse {
	if s.cfHome == "" {
		return errResponse(id, -32000, "no session directory (run campfire_init first)")
	}

	// Verify the identity exists — require init before export.
	if !identity.Exists(s.identityPath()) {
		return errResponse(id, -32000, "no identity found (run campfire_init first)")
	}

	// Ensure store.db exists. In session mode the store is already open, so
	// no second connection is needed. Otherwise open and close to create the file,
	// guaranteeing the tarball always contains a valid store.
	if s.st == nil {
		st, openErr := store.Open(s.storePath())
		if openErr != nil {
			return errResponse(id, -32000, fmt.Sprintf("opening store: %v", openErr))
		}
		st.Close()
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	err := filepath.Walk(s.cfHome, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(s.cfHome, path)
		if err != nil {
			return err
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		hdr := &tar.Header{
			Name:    rel,
			Mode:    int64(info.Mode()),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := io.Copy(tw, f); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("creating tarball: %v", err))
	}

	if err := tw.Close(); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("closing tar writer: %v", err))
	}
	if err := gz.Close(); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("closing gzip writer: %v", err))
	}

	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())
	result, _ := toolResultJSON(map[string]interface{}{
		"tarball":     encoded,
		"encoding":    "base64",
		"compression": "gzip",
		"instructions": "Decode the tarball and extract into a local CF_HOME directory. " +
			"Then run `cf id` to verify the same public key appears.",
	})
	return okResponse(id, result)
}

// ---------------------------------------------------------------------------
// Request dispatch
// ---------------------------------------------------------------------------

func (s *server) dispatch(req jsonRPCRequest) jsonRPCResponse {
	switch req.Method {
	case "initialize":
		return okResponse(req.ID, map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"serverInfo":      mcpServerInfo{Name: "campfire", Version: "0.2.0"},
			"capabilities":    mcpCapabilities{Tools: map[string]interface{}{}},
		})

	case "notifications/initialized":
		// No response needed for notifications, but return something benign.
		return jsonRPCResponse{} // marker: skip sending

	case "tools/list":
		return okResponse(req.ID, map[string]interface{}{"tools": tools})

	case "tools/call":
		var callParams struct {
			Name      string                 `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &callParams); err != nil {
			return errResponse(req.ID, -32602, fmt.Sprintf("invalid params: %v", err))
		}
		if callParams.Arguments == nil {
			callParams.Arguments = map[string]interface{}{}
		}

		switch callParams.Name {
		case "campfire_init":
			return s.handleInit(req.ID, callParams.Arguments)
		case "campfire_id":
			return s.handleID(req.ID, callParams.Arguments)
		case "campfire_create":
			return s.handleCreate(req.ID, callParams.Arguments)
		case "campfire_join":
			return s.handleJoin(req.ID, callParams.Arguments)
		case "campfire_send":
			return s.handleSend(req.ID, callParams.Arguments)
		case "campfire_read":
			return s.handleRead(req.ID, callParams.Arguments)
		case "campfire_inspect":
			return s.handleInspect(req.ID, callParams.Arguments)
		case "campfire_discover":
			return s.handleDiscover(req.ID, callParams.Arguments)
		case "campfire_ls":
			return s.handleLS(req.ID, callParams.Arguments)
		case "campfire_members":
			return s.handleMembers(req.ID, callParams.Arguments)
		case "campfire_dm":
			return s.handleDM(req.ID, callParams.Arguments)
		case "campfire_await":
			return s.handleAwait(req.ID, callParams.Arguments)
		case "campfire_export":
			return s.handleExport(req.ID, callParams.Arguments)
		default:
			return errResponse(req.ID, -32601, fmt.Sprintf("unknown tool: %s", callParams.Name))
		}

	default:
		return errResponse(req.ID, -32601, fmt.Sprintf("unknown method: %s", req.Method))
	}
}

// ---------------------------------------------------------------------------
// HTTP+SSE transport
// ---------------------------------------------------------------------------

// handleMCP handles POST /mcp — accepts a JSON-RPC 2.0 request body, calls
// dispatch(), and returns the JSON-RPC response. This is the HTTP equivalent
// of the stdio line-based loop.
func (s *server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024)) // 1MB max
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(errResponse(nil, -32700, fmt.Sprintf("read error: %v", err)))
		return
	}

	var req jsonRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK) // JSON-RPC errors use 200 per spec
		json.NewEncoder(w).Encode(errResponse(nil, -32700, fmt.Sprintf("parse error: %v", err)))
		return
	}

	resp := s.dispatch(req)

	// Skip empty responses (notifications).
	if resp.JSONRPC == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		fmt.Fprintf(os.Stderr, "http encode error: %v\n", err)
	}
}

// handleSSE handles GET /sse — opens a server-sent events stream. For Phase 1,
// this establishes the SSE connection and sends a heartbeat. The endpoint URL
// for POSTing messages is communicated via the initial "endpoint" event.
func (s *server) handleSSE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Send the MCP endpoint event so clients know where to POST.
	fmt.Fprintf(w, "event: endpoint\ndata: /mcp\n\n")
	flusher.Flush()

	// Keep the connection alive until the client disconnects.
	ctx := r.Context()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

// handleMCPSessioned is the session-aware MCP handler used in hosted HTTP mode.
//
// Protocol:
//   - No Authorization header + campfire_init call: generate a token, create a
//     session, dispatch using the session's server, inject "session_token" into
//     the campfire_init result text.
//   - No Authorization header + any other call: reject with -32000 (session
//     required).
//   - Authorization: Bearer <token>: look up the session and dispatch.
func (s *server) handleMCPSessioned(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(errResponse(nil, -32700, fmt.Sprintf("read error: %v", err))) //nolint:errcheck
		return
	}

	var req jsonRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(errResponse(nil, -32700, fmt.Sprintf("parse error: %v", err))) //nolint:errcheck
		return
	}

	// Extract Bearer token.
	token := ""
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		token = strings.TrimPrefix(auth, "Bearer ")
	}

	// Determine if this is a campfire_init call.
	isInit := false
	if req.Method == "tools/call" && req.Params != nil {
		var cp struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(req.Params, &cp) == nil {
			isInit = cp.Name == "campfire_init"
		}
	}

	if token == "" && !isInit {
		// Non-init request with no session token.
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(errResponse(req.ID, -32000, "session required: call campfire_init first to obtain a session token")) //nolint:errcheck
		return
	}

	if token == "" {
		// campfire_init with no token: create a new session.
		var genErr error
		token, genErr = generateToken()
		if genErr != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(errResponse(req.ID, -32000, fmt.Sprintf("generating session token: %v", genErr))) //nolint:errcheck
			return
		}
	}

	sess, err := s.sessManager.getOrCreate(token)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(errResponse(req.ID, -32000, fmt.Sprintf("creating session: %v", err))) //nolint:errcheck
		return
	}

	// Build a per-request server view pointing at the session's cfHome.
	sessSrv := sess.server(s.sessManager)
	resp := sessSrv.dispatch(req)

	// For campfire_init, inject the session token so the client knows what to
	// include in subsequent Authorization: Bearer headers.
	//
	// We round-trip through JSON to avoid fragile type assertions: Go's
	// encoding/json always produces []interface{}, not []map[string]interface{},
	// when unmarshaling into interface{}, so any prior marshal/unmarshal of
	// resp.Result would silently break a direct type assertion chain.
	if isInit && resp.Error == nil && resp.Result != nil {
		var initResult struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if b, err := json.Marshal(resp.Result); err == nil {
			if err := json.Unmarshal(b, &initResult); err == nil && len(initResult.Content) > 0 {
				initResult.Content[0].Text += "\n\nSession token: " + token + "\nInclude this in subsequent requests as: Authorization: Bearer " + token
				resp.Result = initResult
			}
		}
	}

	if resp.JSONRPC == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		fmt.Fprintf(os.Stderr, "http encode error: %v\n", err)
	}
}

// handleHealth handles GET /health. Returns 200 with a JSON body reporting
// the server status and active session count. Used by Fly.io health checks.
func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sessions := 0
	if s.sessManager != nil {
		s.sessManager.sessions.Range(func(_, _ interface{}) bool {
			sessions++
			return true
		})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status":"ok","sessions":%d}`, sessions)
}

// serveHTTP starts the HTTP+SSE MCP server on the given address.
func (s *server) serveHTTP(addr string) error {
	mux := http.NewServeMux()
	if s.sessManager != nil {
		mux.HandleFunc("/mcp", s.handleMCPSessioned)
	} else {
		mux.HandleFunc("/mcp", s.handleMCP)
	}
	mux.HandleFunc("/sse", s.handleSSE)
	mux.HandleFunc("/health", s.handleHealth)

	// Mount the HTTP transport router so external peers can reach hosted
	// campfires via /campfire/{id}/deliver, /campfire/{id}/poll, etc.
	if s.transportRouter != nil {
		mux.Handle("/campfire/", s.transportRouter)
	}

	fmt.Fprintf(os.Stderr, "cf-mcp listening on %s\n", addr)
	return http.ListenAndServe(addr, mux)
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	// Parse minimal flags.
	cfHome := ""
	beaconDir := ""
	httpAddr := ""
	sessionsDir := ""
	externalAddrFlag := ""
	cfHomeExplicit := false
	for i, arg := range os.Args[1:] {
		switch {
		case arg == "--cf-home" && i+1 < len(os.Args[1:]):
			cfHome = os.Args[i+2]
			cfHomeExplicit = true
		case strings.HasPrefix(arg, "--cf-home="):
			cfHome = strings.TrimPrefix(arg, "--cf-home=")
			cfHomeExplicit = true
		case arg == "--beacon-dir" && i+1 < len(os.Args[1:]):
			beaconDir = os.Args[i+2]
		case strings.HasPrefix(arg, "--beacon-dir="):
			beaconDir = strings.TrimPrefix(arg, "--beacon-dir=")
		case arg == "--http" && i+1 < len(os.Args[1:]):
			httpAddr = os.Args[i+2]
		case strings.HasPrefix(arg, "--http="):
			httpAddr = strings.TrimPrefix(arg, "--http=")
		case arg == "--sessions-dir" && i+1 < len(os.Args[1:]):
			sessionsDir = os.Args[i+2]
		case strings.HasPrefix(arg, "--sessions-dir="):
			sessionsDir = strings.TrimPrefix(arg, "--sessions-dir=")
		case arg == "--external-addr" && i+1 < len(os.Args[1:]):
			externalAddrFlag = os.Args[i+2]
		case strings.HasPrefix(arg, "--external-addr="):
			externalAddrFlag = strings.TrimPrefix(arg, "--external-addr=")
		}
	}

	// Resolve defaults.
	if cfHome == "" {
		if env := os.Getenv("CF_HOME"); env != "" {
			cfHome = env
			cfHomeExplicit = true
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: cannot determine home directory: %v\n", err)
				os.Exit(1)
			}
			cfHome = filepath.Join(home, ".campfire")
		}
	}
	if beaconDir == "" {
		if env := os.Getenv("CF_BEACON_DIR"); env != "" {
			beaconDir = env
		} else {
			beaconDir = filepath.Join(cfHome, "beacons")
		}
	}
	// Resolve external address: flag > env > derived from listen addr.
	externalAddr := externalAddrFlag
	if externalAddr == "" {
		externalAddr = os.Getenv("CF_EXTERNAL_URL")
	}

	srv := &server{
		cfHome:         cfHome,
		beaconDir:      beaconDir,
		cfHomeExplicit: cfHomeExplicit,
	}

	// HTTP+SSE mode when --http is set, otherwise stdio.
	if httpAddr != "" {
		// When --sessions-dir is provided, enable per-session isolation
		// with embedded HTTP transport.
		if sessionsDir != "" {
			if err := os.MkdirAll(sessionsDir, 0700); err != nil {
				fmt.Fprintf(os.Stderr, "error: creating sessions dir: %v\n", err)
				os.Exit(1)
			}
			router := NewTransportRouter()
			sm := NewSessionManager(sessionsDir)
			sm.router = router
			// If external address not provided via flag/env, derive from listen address.
			// If the address starts with ":", prepend "http://localhost".
			if externalAddr == "" {
				externalAddr = httpAddr
				if strings.HasPrefix(externalAddr, ":") {
					externalAddr = "http://localhost" + externalAddr
				} else if !strings.HasPrefix(externalAddr, "http") {
					externalAddr = "http://" + externalAddr
				}
			}
			sm.externalAddr = externalAddr
			srv.sessManager = sm
			srv.transportRouter = router
		}
		if err := srv.serveHTTP(httpAddr); err != nil {
			fmt.Fprintf(os.Stderr, "http server error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// JSON-RPC 2.0 over stdio (default).
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB max line
	enc := json.NewEncoder(os.Stdout)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(line, &req); err != nil {
			resp := errResponse(nil, -32700, fmt.Sprintf("parse error: %v", err))
			enc.Encode(resp) //nolint:errcheck
			continue
		}

		resp := srv.dispatch(req)

		// Skip sending for notifications (no ID, empty response).
		if resp.JSONRPC == "" {
			continue
		}

		if err := enc.Encode(resp); err != nil {
			fmt.Fprintf(os.Stderr, "encode error: %v\n", err)
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "stdin error: %v\n", err)
		os.Exit(1)
	}
}
