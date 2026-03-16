// cmd/cf-mcp/main.go — Campfire MCP server
//
// Implements the Model Context Protocol (MCP) over stdio so that any
// MCP-compatible AI model can use Campfire as a coordination layer.
// Protocol: JSON-RPC 2.0, stdio transport, stateless between tool calls
// (identity is loaded from CF_HOME on each call).
//
// Usage:
//
//	cf-mcp [--cf-home <path>] [--beacon-dir <path>]
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/3dl-dev/campfire/pkg/beacon"
	"github.com/3dl-dev/campfire/pkg/campfire"
	"github.com/3dl-dev/campfire/pkg/identity"
	"github.com/3dl-dev/campfire/pkg/message"
	"github.com/3dl-dev/campfire/pkg/store"
	"github.com/3dl-dev/campfire/pkg/transport/fs"
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
	cfHome    string
	beaconDir string
}

func (s *server) identityPath() string {
	return filepath.Join(s.cfHome, "identity.json")
}

func (s *server) storePath() string {
	return store.StorePath(s.cfHome)
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
	cf := &campfire.Campfire{
		JoinProtocol:          state.JoinProtocol,
		ReceptionRequirements: state.ReceptionRequirements,
		CreatedAt:             state.CreatedAt,
	}
	for _, m := range members {
		cf.Members = append(cf.Members, campfire.Member{
			PublicKey: m.PublicKey,
			JoinedAt:  m.JoinedAt,
		})
	}
	return cf
}

// ---------------------------------------------------------------------------
// Tool definitions
// ---------------------------------------------------------------------------

var tools []mcpToolInfo

func init() {
	tools = []mcpToolInfo{
		{
			Name:        "campfire_init",
			Description: "Generate a new agent identity (Ed25519 keypair). Only creates if one does not exist, unless force=true.",
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
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
	force := getBool(params, "force")
	path := s.identityPath()

	if identity.Exists(path) && !force {
		// Load and return existing
		agentID, err := identity.Load(path)
		if err != nil {
			return errResponse(id, -32000, fmt.Sprintf("loading identity: %v", err))
		}
		result, _ := toolResultJSON(map[string]string{
			"status":     "exists",
			"public_key": agentID.PublicKeyHex(),
		})
		return okResponse(id, result)
	}

	agentID, err := identity.Generate()
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("generating identity: %v", err))
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("creating directory: %v", err))
	}

	if err := agentID.Save(path); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("saving identity: %v", err))
	}

	result, _ := toolResultJSON(map[string]string{
		"status":     "created",
		"public_key": agentID.PublicKeyHex(),
	})
	return okResponse(id, result)
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

	transport := fs.New(fs.DefaultBaseDir())
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
		cf.Identity.PublicKey, cf.Identity.PrivateKey,
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
		Role:         "creator",
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

	transport := fs.New(fs.DefaultBaseDir())

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
		Role:         "member",
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

	transport := fs.New(fs.DefaultBaseDir())

	members, err := transport.ListMembers(campfireID)
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

	state, err := transport.ReadState(campfireID)
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

	if err := transport.WriteMessage(campfireID, msg); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("writing message: %v", err))
	}

	result, _ := toolResultJSON(map[string]interface{}{
		"id":          msg.ID,
		"campfire_id": campfireID,
		"sender":      agentID.PublicKeyHex(),
		"payload":     payload,
		"tags":        msg.Tags,
		"antecedents": msg.Antecedents,
		"timestamp":   msg.Timestamp,
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

	transport := fs.New(fs.DefaultBaseDir())

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

	// Sync from filesystem
	for _, cfID := range campfireIDs {
		fsMessages, err := transport.ListMessages(cfID)
		if err != nil {
			continue
		}
		for _, fsMsg := range fsMessages {
			provJSON, _ := json.Marshal(fsMsg.Provenance)
			tagsJSON, _ := json.Marshal(fsMsg.Tags)
			antJSON, _ := json.Marshal(fsMsg.Antecedents)
			st.AddMessage(store.MessageRecord{
				ID:          fsMsg.ID,
				CampfireID:  cfID,
				Sender:      fmt.Sprintf("%x", fsMsg.Sender),
				Payload:     fsMsg.Payload,
				Tags:        string(tagsJSON),
				Antecedents: string(antJSON),
				Timestamp:   fsMsg.Timestamp,
				Signature:   fsMsg.Signature,
				Provenance:  string(provJSON),
				ReceivedAt:  store.NowNano(),
			})
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
		ID          string          `json:"id"`
		CampfireID  string          `json:"campfire_id"`
		Sender      string          `json:"sender"`
		Payload     string          `json:"payload"`
		Tags        []string        `json:"tags"`
		Antecedents []string        `json:"antecedents"`
		Timestamp   int64           `json:"timestamp"`
		Provenance  json.RawMessage `json:"provenance"`
	}
	var out []jsonMsg
	for _, m := range allMessages {
		var tags []string
		json.Unmarshal([]byte(m.Tags), &tags)
		var ants []string
		json.Unmarshal([]byte(m.Antecedents), &ants)
		if ants == nil {
			ants = []string{}
		}
		out = append(out, jsonMsg{
			ID:          m.ID,
			CampfireID:  m.CampfireID,
			Sender:      m.Sender,
			Payload:     string(m.Payload),
			Tags:        tags,
			Antecedents: ants,
			Timestamp:   m.Timestamp,
			Provenance:  json.RawMessage(m.Provenance),
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

	var provenance []message.ProvenanceHop
	json.Unmarshal([]byte(msg.Provenance), &provenance)

	var antecedents []string
	json.Unmarshal([]byte(msg.Antecedents), &antecedents)
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

	var tags []string
	json.Unmarshal([]byte(msg.Tags), &tags)

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

	transport := fs.New(fs.DefaultBaseDir())

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

	transport := fs.New(fs.DefaultBaseDir())
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

	transport := fs.New(fs.DefaultBaseDir())

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

		b, err := beacon.New(
			cf.Identity.PublicKey, cf.Identity.PrivateKey,
			cf.JoinProtocol, cf.ReceptionRequirements,
			beacon.TransportConfig{
				Protocol: "filesystem",
				Config:   map[string]string{"dir": transport.CampfireDir(cf.PublicKeyHex())},
			},
			fmt.Sprintf("dm:%s:%s", agentID.PublicKeyHex()[:12], targetHex[:12]),
		)
		if err != nil {
			return errResponse(id, -32000, fmt.Sprintf("creating beacon: %v", err))
		}
		if err := beacon.Publish(s.beaconDir, b); err != nil {
			return errResponse(id, -32000, fmt.Sprintf("publishing beacon: %v", err))
		}

		if err := st.AddMembership(store.Membership{
			CampfireID:   cf.PublicKeyHex(),
			TransportDir: transport.CampfireDir(cf.PublicKeyHex()),
			JoinProtocol: cf.JoinProtocol,
			Role:         "creator",
			JoinedAt:     now,
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

	if err := transport.WriteMessage(campfireID, msg); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("writing message: %v", err))
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

// ---------------------------------------------------------------------------
// Request dispatch
// ---------------------------------------------------------------------------

func (s *server) dispatch(req jsonRPCRequest) jsonRPCResponse {
	switch req.Method {
	case "initialize":
		return okResponse(req.ID, map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"serverInfo":      mcpServerInfo{Name: "campfire", Version: "1.0.0"},
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
		default:
			return errResponse(req.ID, -32601, fmt.Sprintf("unknown tool: %s", callParams.Name))
		}

	default:
		return errResponse(req.ID, -32601, fmt.Sprintf("unknown method: %s", req.Method))
	}
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	// Parse minimal flags.
	cfHome := ""
	beaconDir := ""
	for i, arg := range os.Args[1:] {
		switch {
		case arg == "--cf-home" && i+1 < len(os.Args[1:]):
			cfHome = os.Args[i+2]
		case strings.HasPrefix(arg, "--cf-home="):
			cfHome = strings.TrimPrefix(arg, "--cf-home=")
		case arg == "--beacon-dir" && i+1 < len(os.Args[1:]):
			beaconDir = os.Args[i+2]
		case strings.HasPrefix(arg, "--beacon-dir="):
			beaconDir = strings.TrimPrefix(arg, "--beacon-dir=")
		}
	}

	// Resolve defaults.
	if cfHome == "" {
		if env := os.Getenv("CF_HOME"); env != "" {
			cfHome = env
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

	srv := &server{
		cfHome:    cfHome,
		beaconDir: beaconDir,
	}

	// JSON-RPC 2.0 over stdio.
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
