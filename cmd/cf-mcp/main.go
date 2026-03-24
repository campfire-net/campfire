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
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"github.com/campfire-net/campfire/pkg/ratelimit"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
	"github.com/google/uuid"
)

// Version is set at build time via ldflags.
var Version = "dev"

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
	st              store.Store      // non-nil in session mode; already-open store shared from Session
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
		// ---------------------------------------------------------------
		// LIFECYCLE — call campfire_init first, then create or join
		// ---------------------------------------------------------------
		{
			Name: "campfire_init",
			Description: `Initialize your campfire identity. Call this FIRST before any other tool.

Three modes:
  1. campfire_init() — disposable session identity (anonymous, forgotten after disconnect)
  2. campfire_init({name: "worker-1"}) — persistent named identity (other agents recognize you across sessions)
  3. campfire_init({campfire_id: "abc123..."}) — initialize AND auto-provision a campfire (creates it if new, joins if existing)

After init, your typical workflow is:
  campfire_init → campfire_create or campfire_join → campfire_send / campfire_read

The response includes your public key (your identity) and a session token for subsequent requests.`,
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Persistent agent name (e.g. 'worker-1'). Makes your identity stable across sessions so other agents can recognize you. Omit for a disposable session identity.",
					},
					"force": map[string]interface{}{
						"type":        "boolean",
						"description": "Overwrite existing identity if one already exists for this session.",
					},
					"campfire_id": map[string]interface{}{
						"type":        "string",
						"description": "Campfire ID to auto-provision. Creates the campfire if it doesn't exist (threshold=1, you as first member with role='full'), or joins it if it does. Free-tier rate limiting (1000 msg/month) is applied automatically. Idempotent — safe to call repeatedly with the same ID.",
					},
				},
				"required": []string{},
			}),
		},
		{
			Name:        "campfire_id",
			Description: "Return your agent's public key (Ed25519, hex-encoded). This is your identity — other agents use it to verify your messages, send you DMs, or add you to campfires. Share it when asked 'who are you?'",
			InputSchema: mustJSON(map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
				"required":   []string{},
			}),
		},
		// ---------------------------------------------------------------
		// CAMPFIRE MANAGEMENT — create, join, discover, list
		// ---------------------------------------------------------------
		{
			Name: "campfire_create",
			Description: `Create a new campfire (shared communication channel). Returns a campfire_id that other agents use to join.

A campfire is a signed, authenticated message channel. All messages are cryptographically signed by the sender. Use campfires for:
  - Team coordination (multiple agents working on a shared task)
  - Status broadcasting (one agent posts, many read)
  - Escalation channels (ask a question with future/await pattern)

Use 'require' to enforce that only messages with specific tags are accepted — useful for filtering (e.g. require: ["status", "blocker"] means only those tagged messages get through).

Use 'encrypted: true' to create an E2E encrypted campfire. When encrypted, the hosted service joins as a blind relay and cannot decrypt message payloads. Full confidentiality requires at least one self-hosted member (see design-mcp-security.md §5.c).`,
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"protocol": map[string]interface{}{
						"type":        "string",
						"description": "Join protocol: 'open' (anyone can join, default) or 'invite-only' (members must be explicitly admitted).",
						"enum":        []string{"open", "invite-only"},
					},
					"require": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Reception requirements — only messages carrying at least one of these tags will be accepted into the campfire. Use to create focused channels (e.g. ['blocker', 'gate-human'] for an escalation channel).",
					},
					"description": map[string]interface{}{
						"type":        "string",
						"description": "Human-readable description of the campfire's purpose. Shown when other agents discover or list it.",
					},
					"encrypted": map[string]interface{}{
						"type":        "boolean",
						"description": "Enable E2E payload encryption (spec-encryption.md v0.2). When true, the hosted service joins as a blind relay and cannot decrypt message payloads. Requires self-hosted members for full confidentiality (design-mcp-security.md §5.c). Default: false.",
					},
				},
				"required": []string{},
			}),
		},
		{
			Name:        "campfire_join",
			Description: "Join an existing campfire by its ID. After joining, you can send and read messages. You must know the campfire_id — get it from another agent, from campfire_discover, or from your task instructions.",
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"campfire_id": map[string]interface{}{
						"type":        "string",
						"description": "Campfire ID to join (64-char hex string). You can also use a unique prefix (e.g. first 8-12 chars) and the server will resolve it.",
					},
				},
				"required": []string{"campfire_id"},
			}),
		},
		{
			Name:        "campfire_discover",
			Description: "Search for campfires advertising themselves via beacons. Returns a list of campfire IDs with descriptions. Use this to find campfires to join when you don't already have an ID. After discovering, call campfire_join with the desired campfire_id.",
			InputSchema: mustJSON(map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
				"required":   []string{},
			}),
		},
		{
			Name:        "campfire_ls",
			Description: "List all campfires you are currently a member of, with their descriptions and your role in each. Use this to see what channels are available to you before reading or sending.",
			InputSchema: mustJSON(map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
				"required":   []string{},
			}),
		},
		{
			Name:        "campfire_members",
			Description: "List all members of a campfire with their public keys and roles. Use this to see who else is in the channel, find a specific agent's public key for DMs, or verify group composition.",
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"campfire_id": map[string]interface{}{
						"type":        "string",
						"description": "Campfire ID to list members of.",
					},
				},
				"required": []string{"campfire_id"},
			}),
		},
		// ---------------------------------------------------------------
		// MESSAGING — send, read, inspect, DM
		// ---------------------------------------------------------------
		{
			Name: "campfire_send",
			Description: `Send a message to a campfire. All messages are cryptographically signed with your identity.

Basic usage: campfire_send({campfire_id: "...", message: "hello"})

Advanced coordination features:
  - tags: Categorize messages for filtering. Readers can filter by tag. Common tags: "status", "blocker", "finding", "schema-change", "decision". Use tags that match the campfire's reception requirements.
  - instance: Your role in this context (e.g. "implementer", "reviewer", "architect"). Not verified — it's a label for readers to filter by sender role.
  - reply_to: Reference prior messages by ID to build a conversation thread (DAG). Readers see the causal chain.
  - future: Mark this message as a promise you'll fulfill later. Another agent can call campfire_await on this message's ID to block until you respond. Use for async questions: "I need a decision on X" → other agent awaits → you fulfill with the answer.
  - fulfills: Respond to a future message. Automatically adds the reply_to link and a 'fulfills' tag. The agent waiting via campfire_await receives your response immediately.

The future/fulfills/await pattern is how agents coordinate without polling:
  1. Agent A sends: campfire_send({..., message: "Need ruling on X", future: true}) → returns msg_id
  2. Agent A blocks: campfire_await({campfire_id: "...", msg_id: "<msg_id>"})
  3. Agent B reads the future, decides, responds: campfire_send({..., message: "Do Y", fulfills: "<msg_id>"})
  4. Agent A's await returns with Agent B's response`,
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"campfire_id": map[string]interface{}{
						"type":        "string",
						"description": "Campfire ID to send to.",
					},
					"message": map[string]interface{}{
						"type":        "string",
						"description": "Message text. Can be any length up to 64KB.",
					},
					"tags": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Message tags for categorization and filtering. Common: 'status', 'blocker', 'finding', 'decision', 'schema-change'. Campfires with reception requirements only accept messages carrying matching tags.",
					},
					"reply_to": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Message IDs this message replies to. Builds a causal DAG — readers see the thread structure. Use when responding to a specific prior message.",
					},
					"future": map[string]interface{}{
						"type":        "boolean",
						"description": "Mark this as a future — a promise that will be fulfilled later. Other agents can call campfire_await on this message's ID to block until someone fulfills it. Use for questions, escalations, or async requests.",
					},
					"fulfills": map[string]interface{}{
						"type":        "string",
						"description": "Message ID of a future this message fulfills. Automatically adds reply_to + 'fulfills' tag. The agent blocked on campfire_await for that future receives this response immediately.",
					},
					"instance": map[string]interface{}{
						"type":        "string",
						"description": "Your role/instance name in this context (e.g. 'implementer', 'reviewer', 'architect'). Not cryptographically verified — it's a self-declared label. Readers can filter messages by instance to see only messages from a specific role.",
					},
				"commitment": map[string]interface{}{
					"type":        "string",
					"description": "Optional SHA256(message + commitment_nonce) hex commitment. Binds the server to your intended payload \u2014 recipients can verify the commitment on read. Use campfire_commitment to compute this if your client cannot do crypto.",
				},
				"commitment_nonce": map[string]interface{}{
					"type":        "string",
					"description": "Nonce paired with commitment. Must be the same nonce used when computing the commitment. Typically a random hex string.",
				},
				},
				"required": []string{"campfire_id", "message"},
			}),
		},
		{
			Name: "campfire_commitment",
			Description: `Compute a blind commit for a message payload. Returns a {commitment, nonce} pair where commitment = SHA256(payload + nonce).

Use this before calling campfire_send when you want payload integrity guarantees:
  1. campfire_commitment({payload: "your message"}) → {commitment: "...", nonce: "..."}
  2. campfire_send({..., message: "your message", commitment: "...", commitment_nonce: "..."})
  3. campfire_read will return commitment_verified: true if the payload was not substituted.

The nonce prevents the server from pre-computing commitments for guessed payloads.
This is a server-side helper for clients that cannot perform crypto operations.`,
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"payload": map[string]interface{}{
						"type":        "string",
						"description": "The message payload to commit to. Must be identical to the message you will pass to campfire_send.",
					},
				},
				"required": []string{"payload"},
			}),
		},
		{
			Name: "campfire_read",
			Description: `Read messages from a campfire. By default returns only unread messages and advances your read cursor (so the next call returns only newer messages).

Typical patterns:
  - campfire_read({campfire_id: "..."}) — get new messages since your last read
  - campfire_read({campfire_id: "...", all: true}) — get the full message history
  - campfire_read({campfire_id: "...", peek: true}) — check for new messages without marking them as read (non-destructive)
  - campfire_read({}) — read unread messages from ALL campfires you belong to

Each message includes: id, sender (public key), timestamp, payload, tags, instance, and threading info. Use message IDs with reply_to, fulfills, or campfire_inspect.`,
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"campfire_id": map[string]interface{}{
						"type":        "string",
						"description": "Campfire ID to read from. Omit to read from all campfires you belong to.",
					},
					"all": map[string]interface{}{
						"type":        "boolean",
						"description": "Return all messages (full history), not just unread. Useful for catching up or reviewing past decisions.",
					},
					"peek": map[string]interface{}{
						"type":        "boolean",
						"description": "Return unread messages WITHOUT advancing the read cursor. The same messages will appear again on the next read. Use when you want to check for messages without committing to having processed them.",
					},
				},
				"required": []string{},
			}),
		},
		{
			Name:        "campfire_inspect",
			Description: "Deep-inspect a single message by ID. Shows the full provenance chain (which relays handled it), DAG context (what it replies to and what replies to it), and cryptographic signature verification. Use when you need to verify a message's authenticity or trace its delivery path.",
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"message_id": map[string]interface{}{
						"type":        "string",
						"description": "Message ID to inspect (from campfire_read output).",
					},
				},
				"required": []string{"message_id"},
			}),
		},
		{
			Name: "campfire_dm",
			Description: `Send a private direct message to another agent by their public key. Automatically creates (or reuses) a private 2-member invite-only campfire between you and the target.

You need the target agent's public key — get it from campfire_members, from a message they sent (the sender field), or from out-of-band instructions.

DM campfires are persistent. Subsequent DMs to the same agent reuse the same private channel, preserving message history.`,
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"target_key": map[string]interface{}{
						"type":        "string",
						"description": "Target agent's public key (64-char hex). Get this from campfire_members output or from a message's sender field.",
					},
					"message": map[string]interface{}{
						"type":        "string",
						"description": "Message text to send privately.",
					},
					"tags": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Message tags (same semantics as campfire_send).",
					},
				},
				"required": []string{"target_key", "message"},
			}),
		},
		// ---------------------------------------------------------------
		// COORDINATION — await, trust
		// ---------------------------------------------------------------
		{
			Name: "campfire_await",
			Description: `Block until a specific future message is fulfilled. This is the receiving side of the future/fulfills pattern.

Pattern:
  1. You send a question with future: true → get back a msg_id
  2. You call campfire_await({campfire_id, msg_id}) → blocks your execution
  3. Another agent reads your future, decides, and sends a response with fulfills: "<your msg_id>"
  4. campfire_await returns immediately with the fulfilling message

This keeps your full context alive while waiting — no polling loops, no lost state. Use for:
  - Asking an architect for a design decision
  - Requesting human approval (gate-human pattern)
  - Waiting for another agent to complete a prerequisite

If the future is already fulfilled when you call await, it returns immediately.`,
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"campfire_id": map[string]interface{}{
						"type":        "string",
						"description": "Campfire ID where the future message lives.",
					},
					"msg_id": map[string]interface{}{
						"type":        "string",
						"description": "Message ID of the future you're waiting on (returned when you sent the original message with future: true).",
					},
					"timeout": map[string]interface{}{
						"type":        "string",
						"description": "Maximum time to wait before giving up. Examples: '30s', '5m', '1h'. Omit to wait indefinitely. Returns an error if the timeout expires without fulfillment.",
					},
				},
				"required": []string{"campfire_id", "msg_id"},
			}),
		},
		{
			Name:        "campfire_trust",
			Description: "Assign a human-readable pet name to an agent's public key (e.g. 'architect', 'baron'). Once set, that name appears in message output instead of the raw hex key. Call without a label to look up the current display name for a key. Use this to make campfire_read output more readable when working with multiple agents.",
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"public_key": map[string]interface{}{
						"type":        "string",
						"description": "Agent public key (64-char hex) to assign a name to.",
					},
					"label": map[string]interface{}{
						"type":        "string",
						"description": "Pet name to assign (e.g. 'architect', 'worker-1'). Omit to just look up the current name.",
					},
				},
				"required": []string{"public_key"},
			}),
		},
		// ---------------------------------------------------------------
		// PORTABILITY — export your identity and data
		// ---------------------------------------------------------------
		{
			Name: "campfire_export",
			Description: `Export your complete session (identity + message history + campfire memberships) as a base64-encoded tar.gz.

Use this to migrate from hosted to self-hosted: download the export, decode it, and drop the contents into a local CF_HOME directory. Your identity, message history, and campfire memberships transfer with you. The hosted service is infrastructure — you can leave at any time with zero data loss.`,
			InputSchema: mustJSON(map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
				"required":   []string{},
			}),
		},
		// ---------------------------------------------------------------
		// INVITE CODES — campfire access control (security model §5.a)
		// ---------------------------------------------------------------
		{
			Name: "campfire_invite",
			Description: `Create an additional invite code for a campfire you own.

Returns a new invite_code that can be shared with agents you want to admit. Codes can have optional
max_uses limits and human-readable labels for tracking.

Note: campfire_create automatically generates a default invite code. Use campfire_invite to
create additional codes (e.g. per-agent codes, time-limited codes with max_uses).`,
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"campfire_id": map[string]interface{}{
						"type":        "string",
						"description": "The campfire ID to create an invite code for.",
					},
					"max_uses": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of times this code can be used (0 = unlimited).",
					},
					"label": map[string]interface{}{
						"type":        "string",
						"description": "Human-readable label for this code (e.g. 'team-access', 'agent-7').",
					},
				},
				"required": []string{"campfire_id"},
			}),
		},
		{
			Name: "campfire_revoke_invite",
			Description: `Revoke an invite code, preventing further use.

After revocation the code is permanently invalid. Agents who joined with this code retain their
membership — revocation only prevents new joins using this code.`,
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"campfire_id": map[string]interface{}{
						"type":        "string",
						"description": "The campfire ID.",
					},
					"invite_code": map[string]interface{}{
						"type":        "string",
						"description": "The invite code to revoke.",
					},
				},
				"required": []string{"campfire_id", "invite_code"},
			}),
		},
		// ---------------------------------------------------------------
		// SESSION MANAGEMENT — revoke and rotate session tokens
		// ---------------------------------------------------------------
		{
			Name:        "campfire_revoke_session",
			Description: "Revoke your current session token immediately. The session is closed and all state is cleared. You must call campfire_init to get a new token. Use this if you believe your token has been compromised.",
			InputSchema: mustJSON(map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
				"required":   []string{},
			}),
		},
		{
			Name:        "campfire_rotate_token",
			Description: "Rotate your session token. Returns a new token that maps to the same session identity and state. The old token remains valid for 30 seconds to allow in-flight requests to complete, then it is invalidated. Use this to limit the blast radius of a leaked token.",
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

// validateAgentName rejects names that would escape the agents/ directory via
// path traversal. A valid name is a single path component with no separators
// and is not a dot-only component ("." or "..").
func validateAgentName(name string) error {
	if strings.ContainsAny(name, `/\`) || name == ".." || name == "." || filepath.Base(name) != name {
		return fmt.Errorf("invalid agent name %q: must be a single path component with no separators", name)
	}
	return nil
}

func (s *server) handleInit(id interface{}, params map[string]interface{}) jsonRPCResponse {
	name := getStr(params, "name")

	// Named identity: persistent agent across sessions
	// No name: session-scoped identity (disposable)
	if name != "" {
		if err := validateAgentName(name); err != nil {
			return errResponse(id, -32602, err.Error())
		}
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

	// Auto-provision campfire if campfire_id is provided.
	// If the campfire does not exist in the session store it is created.
	// If it already exists the call is idempotent (existing state is returned).
	// Free-tier rate limiting (1000 msg/month) is applied via the session store wrapper.
	campfireIDParam := getStr(params, "campfire_id")
	if campfireIDParam != "" {
		provResult, provErr := s.autoProvisionCampfire(id, campfireIDParam, agentID)
		if provErr != nil {
			return *provErr
		}
		guide := fmt.Sprintf(`Identity %s: %s
Type: %s
Location: %s
Campfire: %s (status: %s, free tier: 1000 msg/month)

You are ready to communicate. Use campfire_send / campfire_read with the campfire_id above.`,
			status, agentID.PublicKeyHex(), identityType, s.cfHome,
			provResult.campfireID, provResult.campfireStatus)
		result, _ := toolResultJSON(map[string]interface{}{
			"public_key":   agentID.PublicKeyHex(),
			"campfire_id":  provResult.campfireID,
			"campfire_status": provResult.campfireStatus,
			"threshold":    provResult.threshold,
			"role":         campfire.RoleFull,
			"free_tier":    true,
			"monthly_cap":  ratelimit.DefaultMonthlyMessageCap,
			"guide":        guide,
		})
		return okResponse(id, result)
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

// autoProvisionResult holds the result of autoProvisionCampfire.
type autoProvisionResult struct {
	campfireID     string
	campfireStatus string // "created" or "exists"
	threshold      uint
}

// autoProvisionCampfire creates or retrieves a campfire for the given campfireID.
// If campfireID is not in the session store, a new campfire is created with
// default settings (threshold=1, open protocol) and the agent is registered as
// the first member with role="full". If campfireID already exists in the store,
// the call is idempotent and returns the existing membership.
//
// Free-tier rate limiting (1000 msg/month) is enforced via the session store
// wrapper (applied at session creation time in session.go).
//
// Returns a non-nil *jsonRPCResponse pointer on error (ready to return to caller).
func (s *server) autoProvisionCampfire(id interface{}, campfireID string, agentID *identity.Identity) (*autoProvisionResult, *jsonRPCResponse) {
	// Resolve the store: prefer the already-open session store, otherwise open one.
	st := s.st
	if st == nil {
		var openErr error
		st, openErr = store.Open(s.storePath())
		if openErr != nil {
			resp := errResponse(id, -32000, fmt.Sprintf("opening store: %v", openErr))
			return nil, &resp
		}
		defer st.Close()
	}

	// Idempotency: if the campfire already exists in the store, return existing state.
	existing, err := st.GetMembership(campfireID)
	if err != nil {
		resp := errResponse(id, -32000, fmt.Sprintf("checking campfire membership: %v", err))
		return nil, &resp
	}
	if existing != nil {
		return &autoProvisionResult{
			campfireID:     existing.CampfireID,
			campfireStatus: "exists",
			threshold:      existing.Threshold,
		}, nil
	}

	// New campfire: create it with default parameters.
	cf, err := campfire.New("open", nil, 1)
	if err != nil {
		resp := errResponse(id, -32000, fmt.Sprintf("creating campfire: %v", err))
		return nil, &resp
	}
	cf.AddMember(agentID.PublicKey)

	// Persist campfire state to the session's local filesystem.
	fsTransport := fs.New(s.cfHome)
	if err := fsTransport.Init(cf); err != nil {
		resp := errResponse(id, -32000, fmt.Sprintf("initializing campfire state: %v", err))
		return nil, &resp
	}

	now := time.Now().UnixNano()
	if err := fsTransport.WriteMember(cf.PublicKeyHex(), campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  now,
	}); err != nil {
		resp := errResponse(id, -32000, fmt.Sprintf("writing member record: %v", err))
		return nil, &resp
	}

	// Record membership in the store under the real (system-generated) campfire ID.
	// In hosted HTTP mode, use the HTTP transport type; otherwise filesystem.
	transportDir := fsTransport.CampfireDir(cf.PublicKeyHex())
	transportType := "filesystem"
	if s.httpTransport != nil {
		transportDir = s.externalAddr
		transportType = "p2p-http"
	}

	if err := st.AddMembership(store.Membership{
		CampfireID:    cf.PublicKeyHex(),
		TransportDir:  transportDir,
		TransportType: transportType,
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      now,
		Threshold:     1,
		Description:   fmt.Sprintf("auto-provisioned from campfire_init (requested: %s)", campfireID),
		CreatorPubkey: agentID.PublicKeyHex(),
	}); err != nil {
		resp := errResponse(id, -32000, fmt.Sprintf("recording membership: %v", err))
		return nil, &resp
	}

	// In hosted HTTP mode, register with the transport router so external
	// peers can reach this campfire, and set self info on the HTTP transport.
	if s.httpTransport != nil {
		s.httpTransport.SetSelfInfo(agentID.PublicKeyHex(), s.externalAddr)
		if err := st.UpsertPeerEndpoint(store.PeerEndpoint{
			CampfireID:   cf.PublicKeyHex(),
			MemberPubkey: agentID.PublicKeyHex(),
			Endpoint:     s.externalAddr,
			Role:         store.PeerRoleCreator,
		}); err != nil {
			resp := errResponse(id, -32000, fmt.Sprintf("registering self as peer: %v", err))
			return nil, &resp
		}
		if s.transportRouter != nil {
			s.transportRouter.RegisterForSession(cf.PublicKeyHex(), s.sessionToken, s.httpTransport)
		}
	}

	return &autoProvisionResult{
		campfireID:     cf.PublicKeyHex(),
		campfireStatus: "created",
		threshold:      1,
	}, nil
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
	encrypted := getBool(params, "encrypted")

	agentID, err := identity.Load(s.identityPath())
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("loading identity (run campfire_init first): %v", err))
	}

	cf, err := campfire.New(protocol, require, 1)
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("creating campfire: %v", err))
	}

	cf.Encrypted = encrypted

	// When the campfire is encrypted, the hosted service joins as a blind relay
	// (spec-encryption.md v0.2 §2.5, design-mcp-security.md §5.c). The service
	// stores/forwards ciphertext but does not hold the epoch key.
	serviceRole := ""
	if encrypted {
		serviceRole = campfire.RoleBlindRelay
	}

	cf.AddMember(agentID.PublicKey)

	// In hosted HTTP mode, use the session's local fs for campfire state and
	// the HTTP transport for message delivery. Beacons point to the server URL.
	if s.httpTransport != nil {
		return s.handleCreateHTTP(id, cf, agentID, description, serviceRole)
	}

	transport := s.fsTransport()
	if err := transport.Init(cf); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("initializing transport: %v", err))
	}

	if err := transport.WriteMember(cf.PublicKeyHex(), campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      serviceRole,
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

	// In session mode, reuse the already-open store to avoid a second SQLite
	// connection to the same file. Otherwise open a dedicated connection.
	st := s.st
	if st == nil {
		var openErr error
		st, openErr = store.Open(s.storePath())
		if openErr != nil {
			return errResponse(id, -32000, fmt.Sprintf("opening store: %v", openErr))
		}
		defer st.Close()
	}

	if err := st.AddMembership(store.Membership{
		CampfireID:   cf.PublicKeyHex(),
		TransportDir: transport.CampfireDir(cf.PublicKeyHex()),
		JoinProtocol: cf.JoinProtocol,
		Role:         serviceRole,
		JoinedAt:     store.NowNano(),
		Encrypted:    encrypted,
	}); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("recording membership: %v", err))
	}

	// Generate a default invite code for this campfire (security model §5.a).
	inviteCode := uuid.New().String()
	if err := st.CreateInvite(store.InviteRecord{
		CampfireID: cf.PublicKeyHex(),
		InviteCode: inviteCode,
		CreatedBy:  agentID.PublicKeyHex(),
		CreatedAt:  store.NowNano(),
		Label:      "default",
	}); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("creating invite code: %v", err))
	}

	result, _ := toolResultJSON(map[string]interface{}{
		"campfire_id":            cf.PublicKeyHex(),
		"join_protocol":          cf.JoinProtocol,
		"reception_requirements": cf.ReceptionRequirements,
		"transport_dir":          transport.CampfireDir(cf.PublicKeyHex()),
		"invite_code":            inviteCode,
	})
	return okResponse(id, result)
}

// handleCreateHTTP is the hosted HTTP mode path for campfire creation.
// It stores campfire state in the session's local filesystem, publishes an
// HTTP transport beacon, registers the campfire with the transport router,
// and sets up the HTTP transport so external peers can reach this campfire.
//
// serviceRole is the campfire membership role the hosted service should use.
// For encrypted campfires, this is campfire.RoleBlindRelay (spec §5.c).
func (s *server) handleCreateHTTP(id interface{}, cf *campfire.Campfire, agentID *identity.Identity, description string, serviceRole string) jsonRPCResponse {
	// Use the session's cfHome as the fs transport base for state storage.
	fsTransport := fs.New(s.cfHome)
	if err := fsTransport.Init(cf); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("initializing campfire state: %v", err))
	}

	now := time.Now().UnixNano()
	if err := fsTransport.WriteMember(cf.PublicKeyHex(), campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  now,
		Role:      serviceRole,
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

	// Store membership in SQLite. In session mode, reuse the already-open
	// store to avoid a second SQLite connection to the same file.
	st := s.st
	if st == nil {
		var openErr error
		st, openErr = store.Open(s.storePath())
		if openErr != nil {
			return errResponse(id, -32000, fmt.Sprintf("opening store: %v", openErr))
		}
		defer st.Close()
	}

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
	// this node's identity. SetKeyProvider is set once at session init
	// (see session.go getOrCreate) so we do not overwrite it here.
	s.httpTransport.SetSelfInfo(agentID.PublicKeyHex(), s.externalAddr)

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

	// Generate a default invite code for this campfire (security model §5.a).
	inviteCode := uuid.New().String()
	if err := st.CreateInvite(store.InviteRecord{
		CampfireID: cf.PublicKeyHex(),
		InviteCode: inviteCode,
		CreatedBy:  agentID.PublicKeyHex(),
		CreatedAt:  store.NowNano(),
		Label:      "default",
	}); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("creating invite code: %v", err))
	}

	result, _ := toolResultJSON(map[string]interface{}{
		"campfire_id":            cf.PublicKeyHex(),
		"join_protocol":          cf.JoinProtocol,
		"reception_requirements": cf.ReceptionRequirements,
		"transport":              "p2p-http",
		"endpoint":               s.externalAddr,
		"invite_code":            inviteCode,
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

	// In session mode, reuse the already-open store to avoid a second SQLite
	// connection to the same file. Otherwise open a dedicated connection.
	st := s.st
	if st == nil {
		var openErr error
		st, openErr = store.Open(s.storePath())
		if openErr != nil {
			return errResponse(id, -32000, fmt.Sprintf("opening store: %v", openErr))
		}
		defer st.Close()
	}

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

	// When the campfire is encrypted, the hosted service joins as a blind relay
	// (spec-encryption.md v0.2 §2.5, design-mcp-security.md §5.c). The service
	// stores/forwards ciphertext but is excluded from epoch key deliveries and
	// cannot decrypt message payloads.
	serviceRole := ""
	if state.Encrypted {
		serviceRole = campfire.RoleBlindRelay
	}

	if alreadyOnDisk {
		now = existingJoinedAt
	} else {
		// Invite code enforcement (security model §5.a).
		// Grace period: if the campfire has NO registered invite codes (e.g. old campfires),
		// we allow the join without a code. If at least one code exists, a valid code is required.
		inviteCode := getStr(params, "invite_code")
		hasInvites, inviteCheckErr := st.HasAnyInvites(campfireID)
		if inviteCheckErr != nil {
			return errResponse(id, -32000, fmt.Sprintf("checking invite codes: %v", inviteCheckErr))
		}
		if hasInvites {
			if inviteCode == "" {
				return errResponse(id, -32000, "invite code required to join this campfire")
			}
			inv, validateErr := st.ValidateInvite(campfireID, inviteCode)
			if validateErr != nil {
				return errResponse(id, -32000, fmt.Sprintf("invalid invite code: %v", validateErr))
			}
			// Increment use count after successful validation.
			if incErr := st.IncrementInviteUse(inv.InviteCode); incErr != nil {
				return errResponse(id, -32000, fmt.Sprintf("recording invite use: %v", incErr))
			}
		}

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
			Role:      serviceRole,
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
		Role:         serviceRole,
		JoinedAt:     now,
		Encrypted:    state.Encrypted,
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
	commitment := getStr(params, "commitment")
	commitmentNonce := getStr(params, "commitment_nonce")

	agentID, err := identity.Load(s.identityPath())
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("loading identity: %v", err))
	}

	// In session mode, reuse the already-open store to avoid a second SQLite
	// connection to the same file. Otherwise open a dedicated connection.
	st := s.st
	if st == nil {
		var openErr error
		st, openErr = store.Open(s.storePath())
		if openErr != nil {
			return errResponse(id, -32000, fmt.Sprintf("opening store: %v", openErr))
		}
		defer st.Close()
	}

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
	// Blind commit: include commitment and nonce in signed tags so recipients
	// can verify SHA256(payload + nonce) == commitment after delivery.
	if commitment != "" && commitmentNonce != "" {
		tags = append(tags, "commitment:"+commitment)
		tags = append(tags, "commitment-nonce:"+commitmentNonce)
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

// handleCommitment implements the campfire_commitment helper tool.
// It generates a random 16-byte nonce and returns {commitment, nonce} where
// commitment = SHA256(payload + nonce). This is a server-side convenience for
// MCP clients that cannot perform crypto operations.
func (s *server) handleCommitment(id interface{}, params map[string]interface{}) jsonRPCResponse {
	payload := getStr(params, "payload")
	if payload == "" {
		return errResponse(id, -32602, "payload is required")
	}

	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("generating nonce: %v", err))
	}
	nonce := hex.EncodeToString(nonceBytes)

	h := sha256.New()
	h.Write([]byte(payload))
	h.Write([]byte(nonce))
	commitment := hex.EncodeToString(h.Sum(nil))

	result, _ := toolResultJSON(map[string]interface{}{
		"commitment": commitment,
		"nonce":      nonce,
	})
	return okResponse(id, result)
}

func (s *server) handleRead(id interface{}, params map[string]interface{}) jsonRPCResponse {
	campfireID := getStr(params, "campfire_id")
	readAll := getBool(params, "all")
	readPeek := getBool(params, "peek")

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
		ID                  string                  `json:"id"`
		CampfireID          string                  `json:"campfire_id"`
		Sender              string                  `json:"sender"`
		Instance            string                  `json:"instance,omitempty"`
		Payload             string                  `json:"payload"`
		Tags                []string                `json:"tags"`
		Antecedents         []string                `json:"antecedents"`
		Timestamp           int64                   `json:"timestamp"`
		Provenance          []message.ProvenanceHop `json:"provenance"`
		CommitmentVerified  *bool                   `json:"commitment_verified,omitempty"`
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

		// Blind commit verification: if the message carries commitment tags,
		// verify SHA256(payload + nonce) == commitment and include the result.
		var commitmentVerified *bool
		var foundCommitment, foundNonce string
		for _, tag := range tags {
			if strings.HasPrefix(tag, "commitment:") {
				foundCommitment = strings.TrimPrefix(tag, "commitment:")
			} else if strings.HasPrefix(tag, "commitment-nonce:") {
				foundNonce = strings.TrimPrefix(tag, "commitment-nonce:")
			}
		}
		if foundCommitment != "" && foundNonce != "" {
			h := sha256.New()
			h.Write(m.Payload)
			h.Write([]byte(foundNonce))
			computed := hex.EncodeToString(h.Sum(nil))
			verified := computed == foundCommitment
			commitmentVerified = &verified
		}

		out = append(out, jsonMsg{
			ID:                 m.ID,
			CampfireID:         m.CampfireID,
			Sender:             m.Sender,
			Instance:           m.Instance,
			Payload:            string(m.Payload),
			Tags:               tags,
			Antecedents:        ants,
			Timestamp:          m.Timestamp,
			Provenance:         prov,
			CommitmentVerified: commitmentVerified,
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
	deadline := time.After(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

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
		case <-ticker.C:
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
func (s *server) handleAwaitHTTP(id interface{}, st store.Store, campfireID, targetMsgID string, timeout time.Duration) jsonRPCResponse {
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
func findMCPFulfillment(st store.Store, campfireID, targetMsgID string) *map[string]interface{} {
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
	targetKey, err := hex.DecodeString(targetHex)
	if err != nil {
		return errResponse(id, -32602, "target_key must be valid hex")
	}

	agentID, err := identity.Load(s.identityPath())
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("loading identity: %v", err))
	}

	if targetHex == agentID.PublicKeyHex() {
		return errResponse(id, -32000, "cannot DM yourself")
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

// trustFilePath returns the path to the trust.json file in cfHome.
func (s *server) trustFilePath() string {
	return filepath.Join(s.cfHome, "trust.json")
}

// loadTrust reads the trust map from trust.json. Returns an empty map if the
// file does not exist yet.
func (s *server) loadTrust() (map[string]string, error) {
	data, err := os.ReadFile(s.trustFilePath())
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing trust.json: %w", err)
	}
	return m, nil
}

// saveTrust writes the trust map back to trust.json atomically.
func (s *server) saveTrust(m map[string]string) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.trustFilePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.trustFilePath())
}

// handleTrust sets or resolves a human-readable pet name for an agent public
// key. With label: stores the mapping. Without label: returns the current label
// or "(unlabeled)" if no pet name has been set.
func (s *server) handleTrust(id interface{}, params map[string]interface{}) jsonRPCResponse {
	if s.cfHome == "" {
		return errResponse(id, -32000, "no session directory (run campfire_init first)")
	}
	publicKey := getStr(params, "public_key")
	if publicKey == "" {
		return errResponse(id, -32602, "public_key is required")
	}
	label := getStr(params, "label")

	trust, err := s.loadTrust()
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("loading trust store: %v", err))
	}

	if label != "" {
		// Set mode: store the pet name.
		trust[publicKey] = label
		if err := s.saveTrust(trust); err != nil {
			return errResponse(id, -32000, fmt.Sprintf("saving trust store: %v", err))
		}
		return okResponse(id, toolResult(fmt.Sprintf("Labeled %s as %q", publicKey, label)))
	}

	// Resolve mode: look up the current label.
	if name, ok := trust[publicKey]; ok {
		return okResponse(id, toolResult(fmt.Sprintf("%s \u2192 %s", publicKey, name)))
	}
	return okResponse(id, toolResult(fmt.Sprintf("%s \u2192 (unlabeled)", publicKey)))
}

// maxExportSize is the maximum in-memory tarball size (uncompressed bytes
// written to the tar stream) that handleExport will produce. Exports that
// exceed this limit return a -32000 error so no single session can exhaust
// server memory.
const maxExportSize = 50 * 1024 * 1024 // 50 MB

// errExportTooLarge is a sentinel returned from the WalkDir callback when the
// accumulated tar write size exceeds maxExportSize.
var errExportTooLarge = fmt.Errorf("export too large")

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

	var totalWritten int64

	err := filepath.WalkDir(s.cfHome, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		// Skip directories — they have no content to archive.
		if d.IsDir() {
			return nil
		}
		// Skip symlinks. filepath.WalkDir does not follow symlinks for
		// directories, but does resolve them for files (the DirEntry.Type()
		// check catches symlinks before os.Open resolves them).
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
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

		n, err := io.Copy(tw, f)
		if err != nil {
			return err
		}
		totalWritten += n
		if totalWritten > maxExportSize {
			return errExportTooLarge
		}
		return nil
	})
	if err != nil {
		if err == errExportTooLarge {
			return errResponse(id, -32000, "export too large: session data exceeds 50 MB limit")
		}
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
// Invite code handlers (security model §5.a)
// ---------------------------------------------------------------------------

// handleCreateInvite creates a new invite code for the given campfire.
func (s *server) handleCreateInvite(id interface{}, params map[string]interface{}) jsonRPCResponse {
	campfireID := getStr(params, "campfire_id")
	if campfireID == "" {
		return errResponse(id, -32602, "campfire_id is required")
	}

	agentID, err := identity.Load(s.identityPath())
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("loading identity (run campfire_init first): %v", err))
	}

	maxUsesFloat, _ := params["max_uses"].(float64)
	maxUses := int(maxUsesFloat)
	label := getStr(params, "label")

	st := s.st
	if st == nil {
		var openErr error
		st, openErr = store.Open(s.storePath())
		if openErr != nil {
			return errResponse(id, -32000, fmt.Sprintf("opening store: %v", openErr))
		}
		defer st.Close()
	}

	inviteCode := uuid.New().String()
	if err := st.CreateInvite(store.InviteRecord{
		CampfireID: campfireID,
		InviteCode: inviteCode,
		CreatedBy:  agentID.PublicKeyHex(),
		CreatedAt:  store.NowNano(),
		MaxUses:    maxUses,
		Label:      label,
	}); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("creating invite code: %v", err))
	}

	result, _ := toolResultJSON(map[string]interface{}{
		"campfire_id": campfireID,
		"invite_code": inviteCode,
		"max_uses":    maxUses,
		"label":       label,
	})
	return okResponse(id, result)
}

// handleRevokeInvite revokes an invite code for the given campfire.
func (s *server) handleRevokeInvite(id interface{}, params map[string]interface{}) jsonRPCResponse {
	campfireID := getStr(params, "campfire_id")
	inviteCode := getStr(params, "invite_code")
	if campfireID == "" || inviteCode == "" {
		return errResponse(id, -32602, "campfire_id and invite_code are required")
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

	if err := st.RevokeInvite(campfireID, inviteCode); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("revoking invite code: %v", err))
	}

	result, _ := toolResultJSON(map[string]interface{}{
		"campfire_id": campfireID,
		"invite_code": inviteCode,
		"status":      "revoked",
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
			"serverInfo":      mcpServerInfo{Name: "campfire", Version: Version},
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
		case "campfire_commitment":
			return s.handleCommitment(req.ID, callParams.Arguments)
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
		case "campfire_trust":
			return s.handleTrust(req.ID, callParams.Arguments)
		case "campfire_export":
			return s.handleExport(req.ID, callParams.Arguments)
		case "campfire_invite":
			return s.handleCreateInvite(req.ID, callParams.Arguments)
		case "campfire_revoke_invite":
			return s.handleRevokeInvite(req.ID, callParams.Arguments)
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

	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024) // 1MB max; closes conn on overflow
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			json.NewEncoder(w).Encode(errResponse(nil, -32700, "request body too large"))
		} else {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(errResponse(nil, -32700, fmt.Sprintf("read error: %v", err)))
		}
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
// Auth dispatch:
//   - No Authorization header + campfire_init call: issue a new token via the
//     registry, create a session, dispatch using the session's server, inject
//     "session_token" into the campfire_init result text.
//   - No Authorization header + any other call: reject with -32000 (session
//     required).
//   - Authorization: Bearer <token>: validate token against registry (rejects
//     unregistered, revoked, and expired tokens), look up session, dispatch.
//   - Authorization: Signed <pubkey>:<sig>: dispatch point reserved for future
//     client-side crypto (P1). Currently returns 401 "unsupported auth scheme".
func (s *server) handleMCPSessioned(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024) // 1MB max; closes conn on overflow
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			json.NewEncoder(w).Encode(errResponse(nil, -32700, "request body too large")) //nolint:errcheck
		} else {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(errResponse(nil, -32700, fmt.Sprintf("read error: %v", err))) //nolint:errcheck
		}
		return
	}

	var req jsonRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(errResponse(nil, -32700, fmt.Sprintf("parse error: %v", err))) //nolint:errcheck
		return
	}

	// Auth middleware: parse Authorization header and dispatch by scheme.
	// Supported: "Bearer <token>" — validated against issuance registry.
	// Prepared: "Signed <pubkey>:<sig>" — reserved for future P1 (client-side crypto).
	token := ""
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		token = strings.TrimPrefix(authHeader, "Bearer ")
	} else if strings.HasPrefix(authHeader, "Signed ") {
		// Future: client-side Ed25519 auth. Not yet implemented.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(errResponse(req.ID, -32000, "auth scheme 'Signed' not yet supported; use Bearer")) //nolint:errcheck
		return
	}

	// Determine if this is a campfire_init call.
	toolName := ""
	if req.Method == "tools/call" && req.Params != nil {
		var cp struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(req.Params, &cp) == nil {
			toolName = cp.Name
		}
	}
	isInit := toolName == "campfire_init"

	if token == "" && !isInit {
		// Non-init request with no session token.
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(errResponse(req.ID, -32000, "session required: call campfire_init first to obtain a session token")) //nolint:errcheck
		return
	}

	if token == "" {
		// campfire_init with no token: issue a new registered token.
		var issueErr error
		token, issueErr = s.sessManager.issueToken()
		if issueErr != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(errResponse(req.ID, -32000, fmt.Sprintf("issuing session token: %v", issueErr))) //nolint:errcheck
			return
		}
	} else {
		// Validate token against registry before doing anything else.
		if _, err := s.sessManager.validateToken(token); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			var expErr *tokenExpiredError
			if errors.As(err, &expErr) {
				json.NewEncoder(w).Encode(errResponse(req.ID, -32000, "session token has expired: call campfire_init to get a new token")) //nolint:errcheck
			} else {
				json.NewEncoder(w).Encode(errResponse(req.ID, -32000, "invalid or revoked session token: call campfire_init to get a new token")) //nolint:errcheck
			}
			return
		}
	}

	// Handle session management tools before getOrCreate (they operate on the
	// registry directly and don't need a live session object for revocation).
	if toolName == "campfire_revoke_session" {
		s.sessManager.revokeSession(token)
		w.Header().Set("Content-Type", "application/json")
		resp := okResponse(req.ID, map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": "Session revoked. Call campfire_init to start a new session."},
			},
		})
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
		return
	}

	if toolName == "campfire_rotate_token" {
		newToken, rotErr := s.sessManager.rotateToken(token)
		if rotErr != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(errResponse(req.ID, -32000, fmt.Sprintf("rotating token: %v", rotErr))) //nolint:errcheck
			return
		}
		graceSec := int(s.sessManager.gracePeriod().Seconds())
		w.Header().Set("Content-Type", "application/json")
		resp := okResponse(req.ID, map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": fmt.Sprintf("New session token: %s\nOld token valid for %d more seconds. Update your Authorization header immediately.", newToken, graceSec)},
			},
		})
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
		return
	}

	sess, err := s.sessManager.getOrCreate(token)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		var limitErr *sessionLimitError
		if errors.As(err, &limitErr) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
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
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      65 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return srv.ListenAndServe()
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
