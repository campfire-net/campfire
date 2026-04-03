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
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/campfire-net/campfire/pkg/admission"
	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/campfire"
	"github.com/campfire-net/campfire/pkg/convention"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/forge"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/ratelimit"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/store/aztable"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
	"github.com/google/uuid"
)

// Version is set at build time via ldflags.
var Version = "dev"

// ssrfValidateEndpoint is the SSRF pre-flight check applied to peer endpoints
// before handleRemoteJoin makes any outbound connection. It is a package-level
// variable so tests can replace it entirely when needed (e.g. TestSSRFJoin_*
// tests that want to bypass the pre-flight check to exercise the transport-level
// TOCTOU guard). Production code must not override this.
//
// Note: cfhttp.ValidateJoinerEndpoint routes through validateJoinerEndpointFunc,
// so cfhttp.OverrideValidateJoinerEndpointForTest() is sufficient for tests that
// just need loopback endpoints to pass — no separate override of this var is needed.
var ssrfValidateEndpoint = func(endpoint string) error {
	return cfhttp.ValidateJoinerEndpoint(endpoint)
}

// cfhttpJoin is the function used to perform the remote join HTTP call.
// It is a package-level variable so tests can inject a fake JoinResult
// without making real network connections. Production code must not override
// this.
var cfhttpJoin = func(peerEndpoint, campfireID string, id *identity.Identity, myEndpoint string) (*cfhttp.JoinResult, error) {
	return cfhttp.Join(peerEndpoint, campfireID, id, myEndpoint)
}

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
	cfHome           string
	beaconDir        string
	cfHomeExplicit   bool
	exposePrimitives bool             // when true, primitive tools are included in tools/list
	lockFile         *os.File
	sessManager     *SessionManager   // non-nil only in HTTP+session mode
	httpTransport   *cfhttp.Transport // non-nil when this server has an embedded HTTP transport
	transportRouter *TransportRouter  // non-nil in hosted HTTP mode (shared across sessions)
	externalAddr    string            // public URL of the hosted server (e.g. "http://localhost:8080")
	auditWriter     *AuditWriter      // non-nil when transparency logging is enabled (§5.e)
	sessionToken    string            // non-empty in session mode; used for campfire ownership tracking in the router
	st              store.Store       // non-nil in session mode; already-open store shared from Session
	sess            *Session          // non-nil in session mode; back-reference used to persist auditWriter across requests
	conventionTools *conventionToolMap // dynamic convention-declared tools per session
	joinMu          sync.Mutex              // guards joinLock map
	joinLock        map[string]*joinEntry   // per-campfireID entry; evicted when refcount drops to zero
	forgeEmitter         *forge.ForgeEmitter          // non-nil when relay metering is enabled; async, fail-open
	forgeAccounts        *forgeAccountManager         // non-nil when FORGE_SERVICE_KEY is set; auto-provisions Forge sub-accounts
	conventionDispatcher     *convention.ConventionDispatcher    // non-nil when convention metering is enabled (M8)
	conventionDispatchStore  convention.DispatchStore            // same store as conventionDispatcher; shared by billingSweep
	billingSweep             *convention.BillingSweep            // non-nil when convention billing sweep is enabled
	conventionServerStore    aztable.ConventionServerStore       // non-nil when Azure Table Storage is available (T4)
}

func (s *server) identityPath() string {
	return filepath.Join(s.cfHome, "identity.json")
}

// joinEntry is a reference-counted per-campfireID mutex entry.
// When refs drops to zero the entry is evicted from the map.
type joinEntry struct {
	mu   sync.Mutex
	refs int
}

// acquireJoinMutex returns the per-campfireID mutex locked and ready for use.
// Concurrent joins for different campfireIDs proceed in parallel; joins for
// the same campfireID are serialized to prevent the TOCTOU cleanup race.
// Call releaseJoinMutex when done — it unlocks the mutex and evicts the map
// entry when no other goroutine holds a reference.
func (s *server) acquireJoinMutex(campfireID string) *joinEntry {
	s.joinMu.Lock()
	if s.joinLock == nil {
		s.joinLock = make(map[string]*joinEntry)
	}
	e, ok := s.joinLock[campfireID]
	if !ok {
		e = &joinEntry{}
		s.joinLock[campfireID] = e
	}
	e.refs++
	s.joinMu.Unlock()

	e.mu.Lock()
	return e
}

// releaseJoinMutex unlocks the entry and evicts it from the map when no
// other goroutine holds a reference, bounding the map to active campfires.
func (s *server) releaseJoinMutex(campfireID string, e *joinEntry) {
	e.mu.Unlock()

	s.joinMu.Lock()
	e.refs--
	if e.refs == 0 {
		delete(s.joinLock, campfireID)
	}
	s.joinMu.Unlock()
}



func (s *server) storePath() string {
	return store.StorePath(s.cfHome)
}

// newProtocolClient creates a protocol.Client wrapping the given store and identity.
// The identity may be nil for read-only operations.
func newProtocolClient(st store.Store, agentID *identity.Identity) *protocol.Client {
	return protocol.New(st, agentID)
}

// syncFSVerified syncs messages from the filesystem transport into the store,
// verifying signatures and provenance hops on every message before storing.
// Messages with invalid signatures, empty provenance, or invalid hops are silently
// skipped — they are not stored and will not appear in subsequent queries.
//
// This is the verified-sync primitive used by handleViewTool and the FS-polling
// path of handleAwait to fix the bypass identified in campfire-agent-ltj: those
// handlers previously called fs.Transport.ListMessages directly, bypassing the
// signature and hop verification that protocol.Client.syncIfFilesystem performs.
//
// Callers in HTTP mode should skip this function entirely (messages arrive via
// push and are already verified at ingestion). In FS mode, this must be called
// before querying the store to ensure tampered or unsigned messages are rejected.
func syncFSVerified(st store.Store, fsT *fs.Transport, campfireID string) {
	fsMessages, err := fsT.ListMessages(campfireID)
	if err != nil {
		return
	}
	for _, fsMsg := range fsMessages {
		// Reject messages with invalid Ed25519 signature.
		if !fsMsg.VerifySignature() {
			continue
		}
		// Reject messages with empty provenance — every legitimate message must
		// have at least one hop establishing the originating sender.
		if len(fsMsg.Provenance) == 0 {
			continue
		}
		// Reject messages with any invalid provenance hop.
		hopOK := true
		for _, hop := range fsMsg.Provenance {
			if !message.VerifyHop(fsMsg.ID, hop) {
				hopOK = false
				break
			}
		}
		if !hopOK {
			continue
		}
		st.AddMessage(store.MessageRecordFromMessage(campfireID, &fsMsg, store.NowNano())) //nolint:errcheck
	}
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

// shortID returns the first n characters of s, or s itself if shorter.
func shortID(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
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

// getSlice extracts a []interface{} param (returns nil if missing).
func getSlice(params map[string]interface{}, key string) []interface{} {
	if v, ok := params[key]; ok {
		if s, ok := v.([]interface{}); ok {
			return s
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

// baseTools are always registered regardless of --expose-primitives.
// They cover identity, lifecycle, discovery, and session management.
var baseTools []mcpToolInfo

// primitiveTools are only registered when --expose-primitives is set.
// They expose raw data-plane operations: create, send, read, inspect, dm, await, export.
// By default these are hidden so agents use convention tools (the campfire's typed API) instead.
var primitiveTools []mcpToolInfo

// primitiveToolNames lists the tool names that are considered low-level primitives.
// These are hidden by default and only registered when --expose-primitives is set.
var primitiveToolNames = map[string]bool{
	"campfire_create":     true,
	"campfire_send":       true,
	"campfire_commitment": true,
	"campfire_read":       true,
	"campfire_inspect":    true,
	"campfire_dm":         true,
	"campfire_await":      true,
	"campfire_export":     true,
}

func init() {
	// Build the full tool list then partition into base and primitive slices.
	allToolDefs := []mcpToolInfo{
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

After init:
  campfire_init → campfire_join → NEW TOOLS APPEAR (call tools/list to see them)

When you join a campfire, its convention declarations auto-register as typed MCP tools with validated arguments, proper tags, and correct signing. Use those tools — do NOT compose raw campfire_send calls. The convention tools are the API.`,
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
					"delivery_modes": map[string]interface{}{
						"type":  "array",
						"items": map[string]interface{}{"type": "string", "enum": []string{"pull", "push"}},
						"description": `Delivery modes for this campfire. Controls how the server delivers messages to members.
Valid values: "pull" (members poll for messages, default), "push" (server pushes to members).
Default: ["pull"]. Example: ["pull","push"] to enable both modes.`,
					},
					"declarations": map[string]interface{}{
						"type":  "array",
						"items": map[string]interface{}{},
						"description": `Convention declarations to publish at creation time. Each entry is signed with the campfire key and published as a convention:operation message, so convention tools are available immediately after creation. Joiners get them automatically.

Each entry can be:
- A URL string: fetched and published (e.g. "https://aietf.getcampfire.dev/.well-known/campfire/declarations/social-post.json")
- A well-known name string: resolved via https://aietf.getcampfire.dev/.well-known/campfire/declarations/<name>.json
- An inline declaration object: published directly

Example: ["social-post", "peering"] seeds the campfire with social-post and peering convention tools.`,
					},
					"views": map[string]interface{}{
						"type": "array",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"name":        map[string]interface{}{"type": "string", "description": "View name — becomes a callable MCP tool."},
								"predicate":   map[string]interface{}{"type": "string", "description": "S-expression filter, e.g. (tag \"exchange:match\") or (and (tag \"exchange:put\") (not (tag \"exchange:revoke\")))"},
								"description": map[string]interface{}{"type": "string", "description": "Human-readable description of what this view returns."},
							},
							"required": []string{"name", "predicate"},
						},
						"description": `Named views to create at campfire birth. Each view becomes a callable MCP tool that returns filtered messages matching the predicate. Joiners discover views automatically.

Example: [{"name": "inventory", "predicate": "(tag \"exchange:phase:put-accept\")", "description": "Active listings on the exchange"}]

Predicate operators: (tag "x"), (sender "hex"), (and ...), (or ...), (not ...), (field "path"), (eq/gt/lt/gte/lte ...).`,
					},
				},
				"required": []string{},
			}),
		},
		{
			Name: "campfire_join",
			Description: `Join a campfire and discover its convention tools. THIS IS THE KEY STEP — after joining, call tools/list to see the new tools that were auto-registered from the campfire's convention declarations.

Convention tools are the campfire's typed API. They appear as new MCP tools with validated arguments, proper tag composition, and correct signing. Use them instead of campfire_send.

You may provide campfire_id, invite_code, or both. If you only have an invite code, pass just invite_code — the server resolves the campfire. For remote instances, provide peer_endpoint or let the server resolve it from a beacon.`,
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"campfire_id": map[string]interface{}{
						"type":        "string",
						"description": "Campfire ID to join (64-char hex string). Optional when invite_code is provided — the server will resolve the campfire from the invite code.",
					},
					"invite_code": map[string]interface{}{
						"type":        "string",
						"description": "Invite code for the campfire. When provided without campfire_id, the server resolves the campfire automatically. Required for campfires that have invite-code enforcement enabled.",
					},
					"peer_endpoint": map[string]interface{}{
						"type":        "string",
						"description": "HTTP endpoint of a remote campfire server (e.g. https://mcp.getcampfire.dev). When provided, the server joins via cfhttp.Join against this endpoint, bypassing local and beacon resolution. Useful for cross-instance joins before beacon discovery is fully wired.",
					},
				},
				"required": []string{},
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
			Description: `PRIMITIVE — send a raw, untyped message. You almost certainly want a convention tool instead.

After campfire_join, call tools/list. Convention tools (social_post, beacon_register, etc.) handle validation, tags, and signing automatically. Only use campfire_send when no convention tool covers your use case — e.g. free-form coordination, status updates, or chat.

Basic: campfire_send({campfire_id: "...", message: "hello"})

Coordination primitives (these are valid campfire_send use cases):
  - tags: ["status", "blocker", "finding"] — categorize for filtering
  - reply_to: [msg_id] — thread replies (builds a causal DAG)
  - future: true — mark as a promise; another agent blocks on campfire_await until you fulfill it
  - fulfills: msg_id — respond to a future; unblocks the waiting agent
  - instance: "reviewer" — self-declared role label for filtering`,
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
			Description: `Read messages from a campfire. Returns a Trust v0.2 envelope with trust_status, operator_provenance, and the messages in tainted.content.

By default returns only unread messages and advances your read cursor.

Patterns:
  - campfire_read({campfire_id: "..."}) — new messages since last read
  - campfire_read({campfire_id: "...", all: true}) — full history
  - campfire_read({campfire_id: "...", peek: true}) — check without advancing cursor
  - campfire_read({}) — unread from ALL joined campfires

Each message includes: id, sender, timestamp, payload, tags, provenance, threading. The envelope wraps messages with verified.campfire_id and runtime_computed.trust_status.`,
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
		// ---------------------------------------------------------------
		// TRANSPARENCY LOG — per-agent audit campfire (security model §5.e)
		// ---------------------------------------------------------------
		{
			Name: "campfire_audit",
			Description: `Show a summary of your agent's transparency log.

Every action this server takes on your behalf (send, join, create, export, invite, revoke) is
recorded in a dedicated per-agent audit campfire. The log can be read by you or any party you
share the audit_campfire_id with, providing an independent record of server activity for
detecting operator abuse.

Returns: total actions, actions by type, latest Merkle root (if computed), and audit_campfire_id.`,
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"since": map[string]interface{}{
						"type":        "string",
						"description": "ISO-8601 timestamp (e.g. 2026-03-24T00:00:00Z). Only audit entries at or after this time are included in the summary.",
					},
				},
				"required": []string{},
			}),
		},
		// ---------------------------------------------------------------
		// PEERING — add, remove, and list P2P HTTP peers
		// ---------------------------------------------------------------
		{
			Name: "campfire_add_peer",
			Description: `Add a peer endpoint to a P2P HTTP campfire.

Registers a remote peer's HTTP endpoint so this node can exchange messages with it directly
(peer-to-peer, no relay). Only works on campfires using HTTP transport.

After adding a peer, messages sent to this campfire will be delivered to the peer's endpoint.`,
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"campfire_id": map[string]interface{}{
						"type":        "string",
						"description": "The campfire ID to add the peer to.",
					},
					"endpoint": map[string]interface{}{
						"type":        "string",
						"description": "HTTP endpoint URL of the peer (e.g. 'https://peer.example.com/campfire').",
					},
					"public_key_hex": map[string]interface{}{
						"type":        "string",
						"description": "The peer's Ed25519 public key (hex-encoded). Required for message verification.",
					},
				},
				"required": []string{"campfire_id", "endpoint", "public_key_hex"},
			}),
		},
		{
			Name: "campfire_remove_peer",
			Description: `Remove a peer from a P2P HTTP campfire by their public key.

The peer's endpoint is removed so messages are no longer delivered to it. Only works on
campfires using HTTP transport.`,
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"campfire_id": map[string]interface{}{
						"type":        "string",
						"description": "The campfire ID to remove the peer from.",
					},
					"public_key_hex": map[string]interface{}{
						"type":        "string",
						"description": "The peer's Ed25519 public key (hex-encoded).",
					},
				},
				"required": []string{"campfire_id", "public_key_hex"},
			}),
		},
		{
			Name:        "campfire_peers",
			Description: "List all registered peers for a P2P HTTP campfire. Returns each peer's endpoint URL, public key, and participant ID. Only works on campfires using HTTP transport.",
			InputSchema: mustJSON(map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"campfire_id": map[string]interface{}{
						"type":        "string",
						"description": "The campfire ID to list peers for.",
					},
				},
				"required": []string{"campfire_id"},
			}),
		},
	}

	// Partition the full list into base tools and primitive tools.
	for _, t := range allToolDefs {
		if primitiveToolNames[t.Name] {
			primitiveTools = append(primitiveTools, t)
		} else {
			baseTools = append(baseTools, t)
		}
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
		// Persist the named home into the Session so subsequent per-request
		// servers (built via Session.server()) inherit the correct directory.
		// beaconDir lives inside cfHome so update it too.
		if s.sess != nil {
			s.sess.mu.Lock()
			s.sess.cfHome = namedHome
			s.sess.beaconDir = filepath.Join(namedHome, "beacons")
			s.sess.mu.Unlock()
			s.beaconDir = s.sess.beaconDir
		}
	} else if !s.cfHomeExplicit {
		tmpDir, err := os.MkdirTemp("", "campfire-session-*")
		if err != nil {
			return errResponse(id, -32000, fmt.Sprintf("creating session temp dir: %v", err))
		}
		s.cfHome = tmpDir
		// Persist the temp home into the Session so subsequent per-request
		// servers inherit the correct directory.
		if s.sess != nil {
			s.sess.mu.Lock()
			s.sess.cfHome = tmpDir
			s.sess.mu.Unlock()
		}
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
		// Persist identity to cloud so it survives instance hops.
		if s.sessManager != nil && s.sess != nil {
			if data, readErr := os.ReadFile(path); readErr == nil {
				s.sessManager.SaveIdentityToCloud(s.sess.internalID, data)
			}
		}
	}

	identityType := "session (disposable)"
	if name != "" {
		identityType = fmt.Sprintf("persistent agent '%s'", name)
	}

	// Auto-provision Forge sub-account for this operator (best-effort).
	// Runs asynchronously to avoid blocking campfire_init on Forge availability.
	// On success, enables Forge balance enforcement on the session's rate limiter.
	if s.forgeAccounts != nil {
		pubkeyHex := agentID.PublicKeyHex()
		sess := s.sess // capture for goroutine
		forgeAccounts := s.forgeAccounts
		go func() {
			ctx := context.Background()
			accountID, err := forgeAccounts.EnsureOperatorAccount(ctx, pubkeyHex)
			if err != nil {
				log.Printf("campfire_init: forge account provisioning failed for %s: %v", pubkeyHex, err)
				return
			}
			// Wire Forge balance enforcement into the session's rate limiter.
			// sess may be nil in stdio/non-session mode; rateLimiter is always
			// set when a session exists.
			if sess != nil && sess.rateLimiter != nil {
				sess.rateLimiter.SetForgeAccount(accountID, forgeAccounts.forge)
			}
		}()
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

Call tools/list now — if this campfire has convention declarations, they are already registered as typed MCP tools with validated arguments, proper tags, and signing. Use those tools to interact with this campfire. Do NOT fall back to campfire_send unless no convention tool covers your use case.`,
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

Next steps:
1. campfire_join({campfire_id: "..."}) — join a campfire
2. Call tools/list — convention tools appear automatically after join
3. Use those tools. They are the campfire's API.

Convention tools handle argument validation, tag composition, rate limiting, and signing. Do NOT use campfire_send to replicate what a convention tool already does.

campfire_send exists for free-form messaging when no convention covers your use case (coordination, chat, status updates). campfire_read returns messages with trust envelopes.

Other commands: campfire_discover (find campfires), campfire_create (start one).`,
		status, agentID.PublicKeyHex(), identityType, s.cfHome)

	// Transparency log: create audit campfire for this session (§5.e).
	// Best-effort — audit failure does not block init.
	//
	// AuditWriter is persisted in the Session (s.sess.auditWriter) rather than
	// on this per-request server struct. Without this, repeated campfire_init
	// calls on the same session each get a fresh *server with nil auditWriter,
	// causing a new AuditWriter (and its background goroutine) to be created
	// every call while the previous one is abandoned without Close().
	auditCampfireID := ""
	auditStatus := "ok"
	auditError := ""
	if s.auditWriter == nil {
		if aw, awErr := NewAuditWriter(s); awErr == nil {
			s.auditWriter = aw
			// Persist into the session so subsequent requests on this session
			// reuse the same AuditWriter instead of creating a new one.
			if s.sess != nil {
				s.sess.mu.Lock()
				s.sess.auditWriter = aw
				s.sess.mu.Unlock()
			}
			auditCampfireID = aw.CampfireID()
		} else {
			auditStatus = "disabled"
			auditError = awErr.Error()
		}
	} else {
		auditCampfireID = s.auditWriter.CampfireID()
	}

	if auditCampfireID != "" {
		result, _ := toolResultJSON(map[string]interface{}{
			"public_key":        agentID.PublicKeyHex(),
			"audit_campfire_id": auditCampfireID,
			"audit_status":      auditStatus,
			"guide":             guide,
		})
		return okResponse(id, result)
	}

	// Audit writer unavailable: include warning so the agent knows transparency
	// logging is not active for this session (§5.e).
	result, _ := toolResultJSON(map[string]interface{}{
		"public_key":   agentID.PublicKeyHex(),
		"audit_status": auditStatus,
		"audit_error":  auditError,
		"guide":        guide,
	})
	return okResponse(id, result)
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

	// In hosted HTTP mode, register transport and set self info.
	transportDir := fsTransport.CampfireDir(cf.PublicKeyHex())
	transportType := "filesystem"
	var endpoint string
	if s.httpTransport != nil {
		s.httpTransport.SetSelfInfo(agentID.PublicKeyHex(), s.externalAddr)
		transportDir = s.externalAddr
		transportType = "p2p-http"
		endpoint = s.externalAddr
	}

	admitDeps := admission.AdmitterDeps{
		FSTransport: fsTransport,
		Store:       st,
	}
	if s.httpTransport != nil {
		admitDeps.HTTPTransport = s.httpTransport
	}
	if _, admitErr := admission.AdmitMember(context.Background(), admitDeps, admission.AdmissionRequest{
		CampfireID:      cf.PublicKeyHex(),
		MemberPubKeyHex: agentID.PublicKeyHex(),
		Role:            campfire.RoleFull,
		JoinProtocol:    "open",
		TransportDir:    transportDir,
		TransportType:   transportType,
		Endpoint:        endpoint,
		Description:     fmt.Sprintf("auto-provisioned from campfire_init (requested: %s)", campfireID),
		CreatorPubkey:   agentID.PublicKeyHex(),
	}); admitErr != nil {
		resp := errResponse(id, -32000, fmt.Sprintf("admitting member: %v", admitErr))
		return nil, &resp
	}

	if s.httpTransport != nil {
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
	deliveryModes := getStringSlice(params, "delivery_modes")
	declarations := getSlice(params, "declarations")
	views := getSlice(params, "views")

	// Validate delivery_modes: only "pull" and "push" are valid.
	for _, mode := range deliveryModes {
		if !campfire.ValidDeliveryMode(mode) {
			return errResponse(id, -32602, fmt.Sprintf("invalid delivery_mode %q: must be \"pull\" or \"push\"", mode))
		}
	}
	// Default to ["pull","push"] when an external addr is available (HTTP transport
	// can push); otherwise default to ["pull"] only.
	if len(deliveryModes) == 0 {
		if s.externalAddr != "" {
			deliveryModes = []string{campfire.DeliveryModePull, campfire.DeliveryModePush}
		} else {
			deliveryModes = []string{campfire.DeliveryModePull}
		}
	}

	agentID, err := identity.Load(s.identityPath())
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("loading identity (run campfire_init first): %v", err))
	}

	cf, err := campfire.New(protocol, require, 1)
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("creating campfire: %v", err))
	}

	cf.Encrypted = encrypted
	cf.DeliveryModes = deliveryModes

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
		return s.handleCreateHTTP(id, cf, agentID, description, serviceRole, declarations, views)
	}

	transport := s.fsTransport()
	if err := transport.Init(cf); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("initializing transport: %v", err))
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

	fsDeps := admission.AdmitterDeps{
		FSTransport: transport,
		Store:       st,
	}
	if _, admitErr := admission.AdmitMember(context.Background(), fsDeps, admission.AdmissionRequest{
		CampfireID:      cf.PublicKeyHex(),
		MemberPubKeyHex: agentID.PublicKeyHex(),
		Role:            serviceRole,
		Encrypted:       encrypted,
		JoinProtocol:    cf.JoinProtocol,
		TransportDir:    transport.CampfireDir(cf.PublicKeyHex()),
		TransportType:   "filesystem",
		CreatorPubkey:   agentID.PublicKeyHex(),
		Description:     description,
	}); admitErr != nil {
		return errResponse(id, -32000, fmt.Sprintf("admitting member: %v", admitErr))
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

	// Audit: record create action (§5.e).
	if s.auditWriter != nil {
		paramBytes, _ := json.Marshal(params)
		s.auditWriter.Log(AuditEntry{
			Timestamp:   time.Now().UnixNano(),
			Action:      "create",
			AgentKey:    agentID.PublicKeyHex(),
			CampfireID:  cf.PublicKeyHex(),
			RequestHash: requestHash(paramBytes),
		})
	}

	// Publish convention declarations if provided. These are signed with the
	// campfire key so they pass the trust gate on readDeclarations.
	resultMap := map[string]interface{}{
		"campfire_id":            cf.PublicKeyHex(),
		"join_protocol":          cf.JoinProtocol,
		"reception_requirements": cf.ReceptionRequirements,
		"delivery_modes":         campfire.EffectiveDeliveryModes(cf.DeliveryModes),
		"transport_dir":          transport.CampfireDir(cf.PublicKeyHex()),
		"invite_code":            inviteCode,
	}

	if len(declarations) > 0 {
		count, toolNames := s.publishDeclarations(st, cf.PublicKeyHex(), declarations)
		if count > 0 {
			resultMap["convention_tools_registered"] = count
			resultMap["convention_tools"] = toolNames
		}
	}

	if len(views) > 0 {
		vCount, vNames := s.publishViews(st, cf.PublicKeyHex(), views)
		if vCount > 0 {
			resultMap["convention_views_registered"] = vCount
			resultMap["convention_views"] = vNames
		}
	}

	toolCount, _ := resultMap["convention_tools_registered"].(int)
	viewCount, _ := resultMap["convention_views_registered"].(int)
	total := toolCount + viewCount
	if total > 0 {
		resultMap["guide"] = fmt.Sprintf("%d convention tools + %d views registered. Call tools/list to see them.", toolCount, viewCount)
	}

	result, _ := toolResultJSON(resultMap)
	return okResponse(id, result)
}

// handleCreateHTTP is the hosted HTTP mode path for campfire creation.
// It stores campfire state in the session's local filesystem, publishes an
// HTTP transport beacon, registers the campfire with the transport router,
// and sets up the HTTP transport so external peers can reach this campfire.
//
// serviceRole is the campfire membership role the hosted service should use.
// For encrypted campfires, this is campfire.RoleBlindRelay (spec §5.c).
func (s *server) handleCreateHTTP(id interface{}, cf *campfire.Campfire, agentID *identity.Identity, description string, serviceRole string, declarations []interface{}, views []interface{}) jsonRPCResponse {
	// Use the session's cfHome as the fs transport base for state storage.
	fsTransport := fs.New(s.cfHome)
	if err := fsTransport.Init(cf); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("initializing campfire state: %v", err))
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

	// Configure the HTTP transport: set self info so join responses include
	// this node's identity. SetKeyProvider is set once at session init
	// (see session.go getOrCreate) so we do not overwrite it here.
	s.httpTransport.SetSelfInfo(agentID.PublicKeyHex(), s.externalAddr)

	httpDeps := admission.AdmitterDeps{
		FSTransport:   fsTransport,
		Store:         st,
		HTTPTransport: s.httpTransport,
	}
	if _, admitErr := admission.AdmitMember(context.Background(), httpDeps, admission.AdmissionRequest{
		CampfireID:      cf.PublicKeyHex(),
		MemberPubKeyHex: agentID.PublicKeyHex(),
		Role:            serviceRole,
		Encrypted:       cf.Encrypted,
		Endpoint:        s.externalAddr,
		JoinProtocol:    cf.JoinProtocol,
		TransportDir:    s.externalAddr,
		TransportType:   "p2p-http",
		CreatorPubkey:   agentID.PublicKeyHex(),
		Description:     description,
	}); admitErr != nil {
		return errResponse(id, -32000, fmt.Sprintf("admitting member: %v", admitErr))
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

	// Audit: record create action for HTTP mode (§5.e).
	if s.auditWriter != nil {
		s.auditWriter.Log(AuditEntry{
			Timestamp:  time.Now().UnixNano(),
			Action:     "create",
			AgentKey:   agentID.PublicKeyHex(),
			CampfireID: cf.PublicKeyHex(),
		})
	}

	// Publish convention declarations if provided.
	resultMap := map[string]interface{}{
		"campfire_id":            cf.PublicKeyHex(),
		"join_protocol":          cf.JoinProtocol,
		"reception_requirements": cf.ReceptionRequirements,
		"delivery_modes":         campfire.EffectiveDeliveryModes(cf.DeliveryModes),
		"transport":              "p2p-http",
		"endpoint":               s.externalAddr,
		"invite_code":            inviteCode,
	}

	if len(declarations) > 0 {
		count, toolNames := s.publishDeclarations(st, cf.PublicKeyHex(), declarations)
		if count > 0 {
			resultMap["convention_tools_registered"] = count
			resultMap["convention_tools"] = toolNames
		}
	}

	if len(views) > 0 {
		vCount, vNames := s.publishViews(st, cf.PublicKeyHex(), views)
		if vCount > 0 {
			resultMap["convention_views_registered"] = vCount
			resultMap["convention_views"] = vNames
		}
	}

	toolCount, _ := resultMap["convention_tools_registered"].(int)
	viewCount, _ := resultMap["convention_views_registered"].(int)
	total := toolCount + viewCount
	if total > 0 {
		resultMap["guide"] = fmt.Sprintf("%d convention tools + %d views registered. Call tools/list to see them.", toolCount, viewCount)
	}

	result, _ := toolResultJSON(resultMap)
	return okResponse(id, result)
}

func (s *server) handleJoin(id interface{}, params map[string]interface{}) jsonRPCResponse {
	campfireID := getStr(params, "campfire_id")
	inviteCodeForResolution := getStr(params, "invite_code")

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

	// Design-mcp-security.md §5.a: campfire_id is optional when invite_code is
	// provided. The server resolves the campfire from the invite code alone.
	// If both campfire_id and invite_code are provided, ValidateInvite below
	// will enforce that the code belongs to the given campfire_id.
	if campfireID == "" {
		if inviteCodeForResolution == "" {
			return errResponse(id, -32602, "campfire_id or invite_code is required")
		}
		// Resolve campfire_id from invite_code.
		// In hosted mode, search all session stores via the transport router.
		// In CLI/single-store mode, search the already-open store.
		if s.transportRouter != nil {
			_, resolved := s.transportRouter.LookupInviteAcrossAllStores(inviteCodeForResolution)
			if resolved == "" {
				return errResponse(id, -32000, "invite code not found on this server")
			}
			campfireID = resolved
		} else {
			inv, lookupErr := st.LookupInvite(inviteCodeForResolution)
			if lookupErr != nil || inv == nil {
				return errResponse(id, -32000, "invite code not found")
			}
			campfireID = inv.CampfireID
		}
	}

	transport := s.fsTransport()

	state, err := transport.ReadState(campfireID)
	if err != nil {
		// Campfire not found locally — try remote join via peer_endpoint or beacon.
		peerEndpoint := getStr(params, "peer_endpoint")
		if peerEndpoint == "" {
			// Beacon resolution: scan local beacon dir for a p2p-http beacon.
			peerEndpoint = s.resolveBeaconEndpoint(campfireID)
		}
		if peerEndpoint == "" {
			return errResponse(id, -32000, "campfire not found locally or via beacon discovery")
		}
		return s.handleRemoteJoin(id, params, campfireID, peerEndpoint, agentID, st)
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
		// Already on disk: skip WriteMember, record membership in store only.
		if _, admitErr := admission.AdmitMember(context.Background(), admission.AdmitterDeps{
			Store: st,
		}, admission.AdmissionRequest{
			CampfireID:      campfireID,
			MemberPubKeyHex: agentID.PublicKeyHex(),
			Role:            serviceRole,
			Encrypted:       state.Encrypted,
			JoinProtocol:    state.JoinProtocol,
			TransportDir:    transport.CampfireDir(campfireID),
			TransportType:   "filesystem",
		}); admitErr != nil {
			return errResponse(id, -32000, fmt.Sprintf("admitting member: %v", admitErr))
		}
	} else {
		// Invite code enforcement (security model §5.a).
		// Grace period: if the campfire has NO registered invite codes (e.g. old campfires),
		// we allow the join without a code. If at least one code exists, a valid code is required.
		//
		// IMPORTANT: invite records live in the campfire creator's session store, not the
		// joining session's store. In hosted (multi-session) mode, HasAnyInvites on the
		// local session store always returns false for campfires created by other sessions,
		// which would allow bypass. We must query the campfire owner's store via the
		// transport router. In CLI/single-store mode there is only one store, so the
		// local store is always correct.
		inviteCode := getStr(params, "invite_code")
		// inviteSt is the store to consult for invite records.
		// Default: the session's own store (correct for CLI mode, single-store deployments).
		var inviteSt store.InviteStore = st
		if s.transportRouter != nil {
			if ownerTransport := s.transportRouter.GetCampfireTransport(campfireID); ownerTransport != nil {
				inviteSt = ownerTransport.Store()
			}
			// If no owner transport is found, the campfire was not created via this hosted
			// service in this process (e.g. pre-existing campfire on disk). The grace period
			// applies: HasAnyInvites on the local store returns false → join is allowed.
			// This is safe because pre-existing campfires have no invite records anywhere.
		}
		hasInvites, inviteCheckErr := inviteSt.HasAnyInvites(campfireID)
		if inviteCheckErr != nil {
			return errResponse(id, -32000, fmt.Sprintf("checking invite codes: %v", inviteCheckErr))
		}
		if hasInvites {
			if inviteCode == "" {
				return errResponse(id, -32000, "invite code required to join this campfire")
			}
			// ValidateAndUseInvite atomically validates and increments the use count,
			// eliminating the TOCTOU race between validation and increment.
			if _, validateErr := inviteSt.ValidateAndUseInvite(campfireID, inviteCode); validateErr != nil {
				return errResponse(id, -32000, fmt.Sprintf("invalid invite code: %v", validateErr))
			}
			// Re-check revocation after the atomic use to close the race window
			// between ValidateAndUseInvite and WriteMember. If a concurrent
			// RevokeInvite fired in that gap, the invite was revoked mid-join:
			// deny the join. The use_count increment is harmless — the code is
			// revoked and cannot be used by any future joiner regardless.
			if postUseInv, recheckErr := inviteSt.LookupInvite(inviteCode); recheckErr == nil && postUseInv != nil && postUseInv.Revoked {
				return errResponse(id, -32000, "invite code was revoked")
			}
		}

		switch state.JoinProtocol {
		case "open":
			// immediately admitted
		case "invite-only":
			return errResponse(id, -32000, fmt.Sprintf("campfire is invite-only; ask a member to run 'cf admit %s %s'", shortID(campfireID, 12), agentID.PublicKeyHex()))
		default:
			return errResponse(id, -32000, fmt.Sprintf("unknown join protocol: %s", state.JoinProtocol))
		}

		// Fresh join: write member file + record membership via AdmitMember.
		if _, admitErr := admission.AdmitMember(context.Background(), admission.AdmitterDeps{
			FSTransport: transport,
			Store:       st,
		}, admission.AdmissionRequest{
			CampfireID:      campfireID,
			MemberPubKeyHex: agentID.PublicKeyHex(),
			Role:            serviceRole,
			Encrypted:       state.Encrypted,
			JoinProtocol:    state.JoinProtocol,
			TransportDir:    transport.CampfireDir(campfireID),
			TransportType:   "filesystem",
		}); admitErr != nil {
			return errResponse(id, -32000, fmt.Sprintf("admitting member: %v", admitErr))
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
			serviceRole,
		); err != nil {
			return errResponse(id, -32000, fmt.Sprintf("adding provenance hop: %v", err))
		}

		if err := transport.WriteMessage(campfireID, sysMsg); err != nil {
			return errResponse(id, -32000, fmt.Sprintf("writing system message: %v", err))
		}
	}

	// Audit: record join action (§5.e).
	if s.auditWriter != nil {
		paramBytes, _ := json.Marshal(params)
		s.auditWriter.Log(AuditEntry{
			Timestamp:   time.Now().UnixNano(),
			Action:      "join",
			AgentKey:    agentID.PublicKeyHex(),
			CampfireID:  campfireID,
			RequestHash: requestHash(paramBytes),
		})
	}

	// Post-join: discover convention:operation declarations and register as tools.
	if s.conventionTools == nil {
		s.conventionTools = newConventionToolMap()
	}
	var fsToolNames []string
	decls, declErr := readDeclarations(st, campfireID, campfireID)
	if declErr != nil {
		log.Printf("convention: reading declarations for %s: %v", campfireID, declErr)
	} else if len(decls) > 0 {
		fsToolNames = registerConventionTools(s.conventionTools, campfireID, decls)
		log.Printf("convention: registered %d tools for campfire %s", len(decls), campfireID)
	}
	if s.sess != nil {
		s.sess.conventionTools = s.conventionTools
	}

	// Post-join: discover convention views and register as read tools.
	viewCount, viewNames := s.readAndRegisterViews(st, campfireID)

	// Build response with convention tool + view discovery results.
	joinResult := map[string]interface{}{
		"campfire_id": campfireID,
		"status":      "joined",
	}
	if len(fsToolNames) > 0 {
		joinResult["convention_tools_registered"] = len(fsToolNames)
		joinResult["convention_tools"] = fsToolNames
	}
	if viewCount > 0 {
		joinResult["convention_views_registered"] = viewCount
		joinResult["convention_views"] = viewNames
	}
	total := len(fsToolNames) + viewCount
	if total > 0 {
		allNames := make([]string, 0, total)
		allNames = append(allNames, fsToolNames...)
		allNames = append(allNames, viewNames...)
		joinResult["guide"] = fmt.Sprintf(
			"%d tools + %d views are now available: %s. "+
				"Call these directly — tools handle writes, views handle reads. "+
				"Run tools/list to see their full schemas.",
			len(fsToolNames), viewCount, strings.Join(allNames, ", "))
	}

	result, _ := toolResultJSON(joinResult)
	return okResponse(id, result)
}

// resolveBeaconEndpoint scans the server's beacon directory for a p2p-http
// beacon matching campfireID and returns the endpoint URL. Returns "" if not
// found or the beacon has no p2p-http transport.
func (s *server) resolveBeaconEndpoint(campfireID string) string {
	beacons, err := beacon.Scan(s.beaconDir)
	if err != nil {
		return ""
	}
	// Use first p2p-http beacon matching the campfire ID.
	for _, b := range beacons {
		if b.CampfireIDHex() != campfireID {
			continue
		}
		if b.Transport.Protocol != "p2p-http" {
			continue
		}
		if ep := b.Transport.Config["endpoint"]; ep != "" {
			return ep
		}
	}
	return ""
}

// handleRemoteJoin performs a cross-instance join via cfhttp.Join.
// Called from handleJoin when the campfire is not found locally.
//
// Flow:
//  1. Validate peerEndpoint against SSRF blocklist (blocks private/internal addresses)
//  2. Call cfhttp.Join(peerEndpoint, campfireID, agentID, myEndpoint)
//  3. Write campfire state CBOR to local fs transport dir
//  4. Write self as a member in the local fs transport
//  5. Call st.AddMembership so handleRead/handleSend can find the campfire
//  6. Register peer endpoints from the join result
//  7. If HTTP transport is running, register the campfire and add server A as peer
func (s *server) handleRemoteJoin(id interface{}, params map[string]interface{}, campfireID, peerEndpoint string, agentID *identity.Identity, st store.Store) jsonRPCResponse {
	// SSRF pre-flight: reject private/internal addresses before making any
	// outbound request. Covers both user-supplied peer_endpoint and
	// beacon-resolved endpoints (both paths converge here).
	if err := ssrfValidateEndpoint(peerEndpoint); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("SSRF blocked: %v", err))
	}

	// Advertise our own endpoint so the remote host can deliver messages back.
	myEndpoint := ""
	if s.externalAddr != "" {
		myEndpoint = s.externalAddr
	}

	result, err := cfhttpJoin(peerEndpoint, campfireID, agentID, myEndpoint)
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("joining remote campfire via %s: %v", peerEndpoint, err))
	}

	// If we advertised an endpoint but the campfire doesn't support push delivery,
	// warn and clear the endpoint so we don't register for push. The server-side
	// join handler should have rejected the request already; this is defense-in-depth.
	supportsPush := false
	for _, m := range result.DeliveryModes {
		if m == campfire.DeliveryModePush {
			supportsPush = true
			break
		}
	}
	if myEndpoint != "" && !supportsPush {
		log.Printf("handleRemoteJoin: campfire %s does not support push delivery (modes=%v); endpoint not registered",
			campfireID[:min(8, len(campfireID))], result.DeliveryModes)
		myEndpoint = ""
	}

	// Serialize concurrent joins for the same campfireID to prevent a TOCTOU
	// cleanup race: without this lock, two concurrent calls can both observe
	// dirExistedBefore=false, then the failing call's defer runs RemoveAll on
	// the directory that the succeeding call already populated.
	je := s.acquireJoinMutex(campfireID)
	defer s.releaseJoinMutex(campfireID, je)

	// Persist campfire state to local fs transport so handleSend/handleRead can use it.
	transport := s.fsTransport()
	campfireDir := transport.CampfireDir(campfireID)

	// If the campfire directory did not exist before this call, clean it up on
	// any failure after MkdirAll so partial writes don't leave orphaned state.
	dirExistedBefore := false
	if _, statErr := os.Stat(campfireDir); statErr == nil {
		dirExistedBefore = true
	}

	success := false
	defer func() {
		if !success && !dirExistedBefore {
			os.RemoveAll(campfireDir) //nolint:errcheck
		}
	}()

	for _, sub := range []string{"members", "messages"} {
		if mkErr := os.MkdirAll(filepath.Join(campfireDir, sub), 0755); mkErr != nil {
			return errResponse(id, -32000, fmt.Sprintf("creating campfire directory: %v", mkErr))
		}
	}

	cfState := campfire.CampfireState{
		PublicKey:             result.CampfirePubKey,
		PrivateKey:            result.CampfirePrivKey,
		JoinProtocol:          result.JoinProtocol,
		ReceptionRequirements: result.ReceptionRequirements,
		Threshold:             result.Threshold,
		DeliveryModes:         result.DeliveryModes,
		Encrypted:             result.Encrypted,
	}
	stateData, marshalErr := cfencoding.Marshal(cfState)
	if marshalErr != nil {
		return errResponse(id, -32000, fmt.Sprintf("encoding campfire state: %v", marshalErr))
	}
	statePath := filepath.Join(campfireDir, "campfire.cbor")
	if writeErr := os.WriteFile(statePath, stateData, 0600); writeErr != nil {
		return errResponse(id, -32000, fmt.Sprintf("writing campfire state: %v", writeErr))
	}

	// Write self as a member and record membership via AdmitMember.
	// Role is derived from cfState.Encrypted: encrypted → RoleBlindRelay, plain → RoleFull.
	remoteJoinTransportType := "filesystem"
	if s.httpTransport != nil {
		remoteJoinTransportType = "p2p-http"
	}
	if _, admitErr := admission.AdmitMember(context.Background(), admission.AdmitterDeps{
		FSTransport: transport,
		Store:       st,
	}, admission.AdmissionRequest{
		CampfireID:      campfireID,
		MemberPubKeyHex: agentID.PublicKeyHex(),
		Encrypted:       cfState.Encrypted,
		Endpoint:        myEndpoint,
		JoinProtocol:    result.JoinProtocol,
		TransportDir:    campfireDir,
		TransportType:   remoteJoinTransportType,
	}); admitErr != nil {
		return errResponse(id, -32000, fmt.Sprintf("admitting member: %v", admitErr))
	}

	// Store peer endpoints from the join result (includes the admitting member).
	// Apply SSRF pre-validation to each peer endpoint before storing it: a
	// malicious remote server could inject private-range addresses into the
	// Peers list, causing this node to later attempt outbound connections to
	// internal hosts (via DeliverToAll). Skip any peer whose endpoint fails
	// validation — do not abort the whole join, as other peers may be valid.
	for _, peer := range result.Peers {
		if peer.PubKeyHex == "" {
			continue
		}
		if peer.Endpoint != "" {
			if err := ssrfValidateEndpoint(peer.Endpoint); err != nil {
				log.Printf("handleRemoteJoin: skipping peer %s: SSRF blocked: %v", shortID(peer.PubKeyHex, 8), err)
				continue
			}
		}
		st.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
			CampfireID:    campfireID,
			MemberPubkey:  peer.PubKeyHex,
			Endpoint:      peer.Endpoint,
			ParticipantID: peer.ParticipantID,
		})
		// Register peer in HTTP transport so handleSend can deliver to them.
		if s.httpTransport != nil && peer.Endpoint != "" {
			s.httpTransport.AddPeer(campfireID, peer.PubKeyHex, peer.Endpoint)
		}
	}

	// If this server has an HTTP transport running, register the remote campfire
	// with the transport router so incoming deliveries from peers are routed to
	// this session's transport handler. Also register self as a peer in the store
	// so membership checks pass for self-authored messages.
	if s.httpTransport != nil && s.sessionToken != "" && s.transportRouter != nil {
		s.transportRouter.RegisterForSession(campfireID, s.sessionToken, s.httpTransport)
		// Add self as peer so sender authentication on incoming messages works.
		st.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
			CampfireID:   campfireID,
			MemberPubkey: agentID.PublicKeyHex(),
			Endpoint:     s.externalAddr,
			Role:         store.PeerRoleMember,
		})
	}

	// Audit: record join action.
	if s.auditWriter != nil {
		paramBytes, _ := json.Marshal(params)
		s.auditWriter.Log(AuditEntry{
			Timestamp:   time.Now().UnixNano(),
			Action:      "join",
			AgentKey:    agentID.PublicKeyHex(),
			CampfireID:  campfireID,
			RequestHash: requestHash(paramBytes),
		})
	}

	// Store convention declaration messages received from the admitting node.
	// Also parse them directly with campfire-key authority — declarations in
	// the join response were published by the campfire key on the admitting
	// node, so they carry operational authority. Re-reading through
	// readDeclarations would lose this: Parse sees the stored Sender (which
	// may differ from campfireID) and demotes signing="campfire_key" to
	// member_key, while signing="member_key" declarations are always
	// classified as untrusted by ResolveAuthority. This matches how
	// publishDeclarations grants SignerCampfireKey at publish time.
	var joinDecls []*convention.Declaration
	for _, dm := range result.Declarations {
		st.AddMessage(store.MessageRecord{ //nolint:errcheck
			ID:         dm.ID,
			CampfireID: campfireID,
			Sender:     dm.Sender,
			Payload:    dm.Payload,
			Tags:       dm.Tags,
			Timestamp:  dm.Timestamp,
			Signature:  dm.Signature,
			ReceivedAt: store.NowNano(),
		})
		// Parse directly and grant campfire-key authority.
		decl, parseResult, parseErr := convention.Parse(dm.Tags, dm.Payload, campfireID, campfireID)
		if parseErr != nil {
			log.Printf("convention: parsing join declaration %s: %v", dm.ID, parseErr)
			continue
		}
		if !parseResult.Valid {
			log.Printf("convention: join declaration %s invalid: %v", dm.ID, parseResult.Warnings)
			continue
		}
		decl.SignerType = convention.SignerCampfireKey
		decl.MessageID = dm.ID
		joinDecls = append(joinDecls, decl)
	}

	// Post-join: register convention tools from parsed declarations.
	if s.conventionTools == nil {
		s.conventionTools = newConventionToolMap()
	}
	var httpToolNames []string
	if len(joinDecls) > 0 {
		httpToolNames = registerConventionTools(s.conventionTools, campfireID, joinDecls)
		log.Printf("convention: registered %d tools for campfire %s", len(joinDecls), campfireID)
	}
	if s.sess != nil {
		s.sess.conventionTools = s.conventionTools
	}

	// Post-join: discover convention views and register as read tools.
	httpViewCount, httpViewNames := s.readAndRegisterViews(st, campfireID)

	httpJoinResult := map[string]interface{}{
		"campfire_id": campfireID,
		"status":      "joined",
		"transport":   "p2p-http",
		"via":         peerEndpoint,
	}
	if len(httpToolNames) > 0 {
		httpJoinResult["convention_tools_registered"] = len(httpToolNames)
		httpJoinResult["convention_tools"] = httpToolNames
	}
	if httpViewCount > 0 {
		httpJoinResult["convention_views_registered"] = httpViewCount
		httpJoinResult["convention_views"] = httpViewNames
	}
	httpTotal := len(httpToolNames) + httpViewCount
	if httpTotal > 0 {
		allNames := make([]string, 0, httpTotal)
		allNames = append(allNames, httpToolNames...)
		allNames = append(allNames, httpViewNames...)
		httpJoinResult["guide"] = fmt.Sprintf(
			"%d tools + %d views are now available: %s. "+
				"Call these directly — tools handle writes, views handle reads. "+
				"Run tools/list to see their full schemas.",
			len(httpToolNames), httpViewCount, strings.Join(allNames, ", "))
	}

	joinResult, _ := toolResultJSON(httpJoinResult)
	success = true
	return okResponse(id, joinResult)
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

	// §5.d: commitment and commitment_nonce must be provided together.
	// Providing only one silently drops the commitment, misleading the sender
	// into thinking the message is committed when it is not.
	if (commitment != "") != (commitmentNonce != "") {
		return errResponse(id, -32602, "commitment and commitment_nonce must both be provided, or neither")
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

	// FED-2: validate routing:beacon payload before storage to prevent beacon poisoning.
	// Reject messages tagged routing:beacon if the payload is not valid JSON or fails
	// inner_signature verification. This runs before NewMessage/client.Send so that
	// malformed beacons are never written to the store or relayed.
	for _, tag := range tags {
		if tag == "routing:beacon" {
			var decl beacon.BeaconDeclaration
			if err := json.Unmarshal([]byte(payload), &decl); err != nil {
				return errResponse(id, -32000, fmt.Sprintf("routing:beacon payload is not valid JSON: %v", err))
			}
			if !beacon.VerifyDeclaration(decl) {
				return errResponse(id, -32000, "routing:beacon payload failed signature verification")
			}
			break
		}
	}

	var msg *message.Message

	{
		// Delegate to protocol.Client for message creation, membership verification,
		// role enforcement, hop signing, peer delivery, and store write.
		//
		// In HTTP mode, the campfire state (.cbor) lives under cfHome rather than in
		// m.TransportDir (which holds the external HTTP address). We pass StateDir so
		// sendP2PHTTP reads state from the correct location without duplicating the
		// protocol pipeline here (campfire-agent-nzk).
		sendReq := protocol.SendRequest{
			CampfireID:  campfireID,
			Payload:     []byte(payload),
			Tags:        tags,
			Antecedents: antecedents,
			Instance:    instance,
		}
		if s.httpTransport != nil {
			sendReq.StateDir = s.cfHome
		}
		client := newProtocolClient(st, agentID)
		msg, err = client.Send(sendReq)
		if err != nil {
			var roleErr *protocol.RoleError
			if protocol.IsRoleError(err, &roleErr) {
				return errResponse(id, -32000, roleErr.Error())
			}
			return errResponse(id, -32000, err.Error())
		}

		if s.httpTransport != nil {
			// Wake any long-polling goroutines (hosted and external peers).
			// protocol.Client.Send() delivers to external HTTP peers via the store's
			// peer list. PollBrokerNotify wakes long-poll subscribers — this is
			// MCP-specific and cannot live in protocol.Client.
			s.httpTransport.PollBrokerNotify(campfireID)
			// M7: relay metering — emit relay-bytes usage event after delivery.
			// The emitter is async and fail-open; this never blocks the send path.
			if s.forgeEmitter != nil {
				// TODO(M5): replace placeholder AccountID with real Forge account lookup
				// once the operator account store (M5) is wired into the server. For
				// now we use the campfireID as a stable placeholder so the idempotency
				// key is still unique per message.
				s.forgeEmitter.Emit(forge.UsageEvent{
					AccountID:      campfireID, // TODO(M5): replace with real Forge account ID
					ServiceID:      "campfire-hosting",
					UnitType:       "relay-bytes",
					Quantity:       float64(len(msg.Payload)),
					IdempotencyKey: campfireID + ":" + msg.ID + ":relay",
				})
			}
		}
	}

	// Convention dispatch (T4): after successful write, dispatch to registered
	// convention server handlers. Non-blocking — Dispatch spawns goroutines internally.
	//
	// The dispatch context is derived from the transport's server-lifetime context
	// (cancelled when Stop() is called) with a 30s timeout, matching the handleDeliver
	// path (handler_message.go). This ensures goroutines spawned by Dispatch are
	// cancelled on shutdown rather than running unbounded (campfire-agent-gxc).
	if s.conventionDispatcher != nil {
		var parentCtx context.Context
		if s.httpTransport != nil {
			parentCtx = s.httpTransport.Context()
		} else {
			parentCtx = context.Background()
		}
		dispatchCtx, dispatchCancel := context.WithTimeout(parentCtx, 30*time.Second)
		// Lazy-load convention server registrations for this campfire on first send.
		s.loadConventionServersForCampfire(dispatchCtx, campfireID)
		msgRec := store.MessageRecordFromMessage(campfireID, msg, store.NowNano())
		if !s.conventionDispatcher.DispatchWithCancel(dispatchCtx, dispatchCancel, campfireID, &msgRec) {
			dispatchCancel()
		}
	}

	// Audit: record send action (§5.e).
	if s.auditWriter != nil {
		paramBytes, _ := json.Marshal(params)
		s.auditWriter.Log(AuditEntry{
			Timestamp:   time.Now().UnixNano(),
			Action:      "send",
			AgentKey:    agentID.PublicKeyHex(),
			CampfireID:  campfireID,
			RequestHash: requestHash(paramBytes),
			Commitment:  commitment,
		})
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

	// Use protocol.Client for sync-before-query. For filesystem campfires, the
	// client verifies message signatures during sync (fixes campfire-agent-bxh:
	// missing sig verification). For HTTP campfires, sync is skipped automatically
	// (messages arrive via push and are already in SQLite).
	client := newProtocolClient(st, nil) // nil identity: read-only

	// Collect messages per campfire.
	var allMessages []protocol.Message
	preFilterCursors := map[string]int64{} // campfireID → MaxTimestamp from protocol result
	for _, cfID := range campfireIDs {
		var afterTS int64
		if !readAll {
			afterTS, _ = st.GetReadCursor(cfID)
		}
		result, readErr := client.Read(protocol.ReadRequest{
			CampfireID:       cfID,
			AfterTimestamp:   afterTS,
			IncludeCompacted: readAll,
		})
		if readErr != nil {
			return errResponse(id, -32000, fmt.Sprintf("listing messages: %v", readErr))
		}
		allMessages = append(allMessages, result.Messages...)
		// Track the pre-filter MaxTimestamp so the cursor advances past all
		// messages the server returned, not just the ones that survive tag/sender
		// filtering. Without this, filtered-out messages re-appear on the next read.
		if result.MaxTimestamp > preFilterCursors[cfID] {
			preFilterCursors[cfID] = result.MaxTimestamp
		}
	}

	type jsonMsg struct {
		ID                  string                  `json:"id"`
		CampfireID          string                  `json:"campfire_id"`
		Sender              string                  `json:"sender"`
		// Instance is tainted (sender-asserted, not verified). Display only —
		// callers must not use this field for access control or trust decisions.
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
			computed := strings.ToLower(hex.EncodeToString(h.Sum(nil)))
			verified := computed == strings.ToLower(foundCommitment)
			commitmentVerified = &verified
		}

		out = append(out, jsonMsg{
			ID:                 m.ID,
			CampfireID:         m.CampfireID,
			Sender:             m.SenderIdentity(),
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

	// Advance cursors unless all/peek.
	// Use preFilterCursors (MaxTimestamp from the protocol result) rather than
	// scanning allMessages. allMessages is the post-filter set, so scanning it
	// would leave filtered-out messages behind the cursor and re-deliver them
	// on the next read.
	if !readAll && !readPeek {
		for cfID, ts := range preFilterCursors {
			if ts > 0 {
				st.SetReadCursor(cfID, ts)
			}
		}
	}

	// Wrap in trust envelope (Trust v0.2 §6). Use the requested campfire_id
	// or, for multi-campfire reads, the first campfire in the result set.
	envelopeCfID := campfireID
	if envelopeCfID == "" && len(out) > 0 {
		envelopeCfID = out[0].CampfireID
	}
	return s.envelopedResponse(id, envelopeCfID, out)
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

	// Filesystem mode: poll the FS transport directory directly. We use the
	// global default transport dir (CF_TRANSPORT_DIR or /tmp/campfire) so that
	// campfires without a membership record in the store (e.g. project campfires
	// or test setups) are still polled correctly.
	//
	// syncFSVerified is used instead of fsTransport.ListMessages to ensure that
	// signature and provenance-hop verification happen on every synced message
	// (campfire-agent-ltj: raw ListMessages bypassed verification).
	fsTransport := fs.New(fs.DefaultBaseDir())
	deadline := time.After(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Initial sync and check.
	syncFSVerified(st, fsTransport, campfireID)
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

		syncFSVerified(st, fsTransport, campfireID)
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
				senderAddr := m.Sender
				if m.SenderCampfireID != "" {
					senderAddr = m.SenderCampfireID
				}
				result := map[string]interface{}{
					"id":          m.ID,
					"campfire_id": m.CampfireID,
					"sender":      senderAddr,
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
		return errResponse(id, -32000, fmt.Sprintf("not a member of campfire %s", shortID(campfireID, 12)))
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

		transportDir := transport.CampfireDir(cf.PublicKeyHex())
		transportType := "filesystem"
		if s.httpTransport != nil {
			transportDir = s.externalAddr
			transportType = "p2p-http"
		}

		dmAdmitDeps := admission.AdmitterDeps{
			FSTransport: transport,
			Store:       st,
		}
		if s.httpTransport != nil {
			dmAdmitDeps.HTTPTransport = s.httpTransport
		}

		// Admit self member (writes member file + records store membership).
		if _, admitErr := admission.AdmitMember(context.Background(), dmAdmitDeps, admission.AdmissionRequest{
			CampfireID:      cf.PublicKeyHex(),
			MemberPubKeyHex: agentID.PublicKeyHex(),
			Role:            store.PeerRoleCreator,
			JoinProtocol:    cf.JoinProtocol,
			TransportDir:    transportDir,
			TransportType:   transportType,
		}); admitErr != nil {
			return errResponse(id, -32000, fmt.Sprintf("admitting sender member: %v", admitErr))
		}

		// Write target member file to filesystem transport (remote peer — no store membership record).
		if err := transport.WriteMember(cf.PublicKeyHex(), campfire.MemberRecord{
			PublicKey: targetKey,
			JoinedAt:  time.Now().UnixNano(),
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

		if s.httpTransport != nil {
			// Register DM campfire with transport router.
			if s.transportRouter != nil {
				s.transportRouter.RegisterForSession(cf.PublicKeyHex(), s.sessionToken, s.httpTransport)
			}
		}

		campfireID = cf.PublicKeyHex()
	}

	var msg *message.Message

	if s.httpTransport != nil {
		// HTTP mode: build message inline so we can call PollBrokerNotify after
		// storing. The DM campfire state lives in cfHome (not in the membership's
		// TransportDir, which is the external HTTP address), so protocol.Client
		// cannot resolve the campfire key via its sendP2PHTTP path.
		var buildErr error
		msg, buildErr = message.NewMessage(agentID.PrivateKey, agentID.PublicKey, []byte(payload), dmTags, nil)
		if buildErr != nil {
			return errResponse(id, -32000, fmt.Sprintf("creating message: %v", buildErr))
		}

		state, stateErr := transport.ReadState(campfireID)
		if stateErr != nil {
			return errResponse(id, -32000, fmt.Sprintf("reading campfire state: %v", stateErr))
		}

		dmMembers, listErr := transport.ListMembers(campfireID)
		if listErr != nil {
			return errResponse(id, -32000, fmt.Sprintf("listing members: %v", listErr))
		}

		dmCF := campfireFromState(state, dmMembers)
		if hopErr := msg.AddHop(
			state.PrivateKey, state.PublicKey,
			dmCF.MembershipHash(), len(dmMembers),
			state.JoinProtocol, state.ReceptionRequirements,
			campfire.RoleFull,
		); hopErr != nil {
			return errResponse(id, -32000, fmt.Sprintf("adding provenance hop: %v", hopErr))
		}

		if _, storeErr := st.AddMessage(store.MessageRecordFromMessage(campfireID, msg, store.NowNano())); storeErr != nil {
			return errResponse(id, -32000, fmt.Sprintf("storing message: %v", storeErr))
		}
		s.httpTransport.PollBrokerNotify(campfireID)
	} else {
		// Filesystem mode: delegate to protocol.Client which handles member
		// verification, message creation, hop signing, and transport write.
		client := newProtocolClient(st, agentID)
		var sendErr error
		msg, sendErr = client.Send(protocol.SendRequest{
			CampfireID: campfireID,
			Payload:    []byte(payload),
			Tags:       dmTags,
		})
		if sendErr != nil {
			return errResponse(id, -32000, sendErr.Error())
		}
	}

	// Audit: record dm action (§5.e).
	if s.auditWriter != nil {
		paramBytes, _ := json.Marshal(params)
		s.auditWriter.Log(AuditEntry{
			Timestamp:   time.Now().UnixNano(),
			Action:      "dm",
			AgentKey:    agentID.PublicKeyHex(),
			CampfireID:  campfireID,
			RequestHash: requestHash(paramBytes),
		})
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

	// Audit: record export action (§5.e).
	if s.auditWriter != nil {
		agentID, _ := identity.Load(s.identityPath())
		agentKey := ""
		if agentID != nil {
			agentKey = agentID.PublicKeyHex()
		}
		s.auditWriter.Log(AuditEntry{
			Timestamp: time.Now().UnixNano(),
			Action:    "export",
			AgentKey:  agentKey,
		})
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

	// Membership authorization check: only members may create invite codes.
	membership, err := st.GetMembership(campfireID)
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("querying membership: %v", err))
	}
	if membership == nil {
		return errResponse(id, -32000, fmt.Sprintf("not a member of campfire %s", shortID(campfireID, 12)))
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

	// Audit: record invite action (§5.e).
	if s.auditWriter != nil {
		paramBytes, _ := json.Marshal(params)
		s.auditWriter.Log(AuditEntry{
			Timestamp:   time.Now().UnixNano(),
			Action:      "invite",
			AgentKey:    agentID.PublicKeyHex(),
			CampfireID:  campfireID,
			RequestHash: requestHash(paramBytes),
		})
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

	agentID, err := identity.Load(s.identityPath())
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("loading identity (run campfire_init first): %v", err))
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

	// Membership authorization check: only members may revoke invite codes.
	membership, memberErr := st.GetMembership(campfireID)
	if memberErr != nil {
		return errResponse(id, -32000, fmt.Sprintf("querying membership: %v", memberErr))
	}
	if membership == nil {
		return errResponse(id, -32000, fmt.Sprintf("not a member of campfire %s", shortID(campfireID, 12)))
	}

	if err := st.RevokeInvite(campfireID, inviteCode); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("revoking invite code: %v", err))
	}

	// Audit: record revoke action (§5.e).
	if s.auditWriter != nil {
		paramBytes, _ := json.Marshal(params)
		s.auditWriter.Log(AuditEntry{
			Timestamp:   time.Now().UnixNano(),
			Action:      "revoke",
			AgentKey:    agentID.PublicKeyHex(),
			CampfireID:  campfireID,
			RequestHash: requestHash(paramBytes),
		})
	}

	result, _ := toolResultJSON(map[string]interface{}{
		"campfire_id": campfireID,
		"invite_code": inviteCode,
		"status":      "revoked",
	})
	return okResponse(id, result)
}

// ---------------------------------------------------------------------------
// Audit handler (transparency log §5.e)
// ---------------------------------------------------------------------------

// handleAudit implements the campfire_audit tool. It reads all messages from
// the agent's audit campfire, counts actions by type, and returns a summary
// including the latest Merkle root if one has been published.
//
// Optional parameter:
//
//	since (string) — ISO-8601 timestamp. When provided, only audit entries
//	    whose timestamp is at or after this time are included in the summary.
func (s *server) handleAudit(id interface{}, params map[string]interface{}) jsonRPCResponse {
	// Parse optional 'since' filter. Accept both RFC3339 (second precision) and
	// RFC3339Nano (sub-second precision) as both are valid ISO-8601 forms.
	var sinceNano int64
	if sinceStr := getStr(params, "since"); sinceStr != "" {
		var sinceTime time.Time
		var parseErr error
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
			sinceTime, parseErr = time.Parse(layout, sinceStr)
			if parseErr == nil {
				break
			}
		}
		if parseErr != nil {
			return errResponse(id, -32602, fmt.Sprintf("invalid 'since' timestamp %q: must be ISO-8601 (e.g. 2026-03-24T00:00:00Z)", sinceStr))
		}
		sinceNano = sinceTime.UnixNano()
	}

	if s.auditWriter == nil {
		result, _ := toolResultJSON(map[string]interface{}{
			"audit_campfire_id": "",
			"total_actions":     0,
			"actions_by_type":   map[string]int{},
			"latest_root":       "",
			"anomalies":         []string{},
			"note":              "Transparency logging is not enabled for this session.",
		})
		return okResponse(id, result)
	}

	auditID := s.auditWriter.CampfireID()

	// Read all audit campfire messages.
	st := s.st
	var ownStore bool
	if st == nil {
		var err error
		st, err = store.Open(s.storePath())
		if err != nil {
			return errResponse(id, -32000, fmt.Sprintf("opening store: %v", err))
		}
		defer st.Close()
		ownStore = true
	}
	_ = ownStore

	fsT := s.fsTransport()
	messages, err := fsT.ListMessages(auditID)
	if err != nil {
		// Audit campfire might not exist yet (no actions logged). Return empty summary.
		result, _ := toolResultJSON(map[string]interface{}{
			"audit_campfire_id": auditID,
			"total_actions":     0,
			"actions_by_type":   map[string]int{},
			"latest_root":       "",
			"anomalies":         []string{},
		})
		return okResponse(id, result)
	}

	totalActions := 0
	actionsByType := map[string]int{}
	latestRoot := ""
	var sequences []uint64

	for _, msg := range messages {
		// Apply 'since' filter using the message envelope timestamp (UnixNano).
		if sinceNano > 0 && msg.Timestamp < sinceNano {
			continue
		}

		// Parse the payload.
		var entry map[string]interface{}
		if err := json.Unmarshal(msg.Payload, &entry); err != nil {
			continue
		}
		// Check for audit-root messages.
		for _, tag := range msg.Tags {
			if tag == "campfire:audit-root" {
				if root, ok := entry["merkle_root"].(string); ok {
					latestRoot = root
				}
			}
			if tag == "campfire:audit" {
				if action, ok := entry["action"].(string); ok {
					totalActions++
					actionsByType[action]++
				}
				// Collect sequence numbers for gap detection.
				if seqRaw, ok := entry["sequence"]; ok {
					switch v := seqRaw.(type) {
					case float64:
						if v > 0 {
							sequences = append(sequences, uint64(v))
						}
					case json.Number:
						if n, err2 := v.Int64(); err2 == nil && n > 0 {
							sequences = append(sequences, uint64(n))
						}
					}
				}
			}
		}
	}

	// Detect sequence gaps — gaps indicate potential tampering or dropped entries.
	anomalies := detectSequenceGaps(sequences)

	result, _ := toolResultJSON(map[string]interface{}{
		"audit_campfire_id": auditID,
		"total_actions":     totalActions,
		"actions_by_type":   actionsByType,
		"latest_root":       latestRoot,
		"dropped_entries":   s.auditWriter.Dropped(),
		"anomalies":         anomalies,
	})
	return okResponse(id, result)
}

// ---------------------------------------------------------------------------
// Peer handlers
// ---------------------------------------------------------------------------

func (s *server) handleAddPeer(id interface{}, params map[string]interface{}) jsonRPCResponse {
	campfireID := getStr(params, "campfire_id")
	if campfireID == "" {
		return errResponse(id, -32602, "campfire_id is required")
	}
	endpoint := getStr(params, "endpoint")
	if endpoint == "" {
		return errResponse(id, -32602, "endpoint is required")
	}
	publicKeyHex := getStr(params, "public_key_hex")
	if publicKeyHex == "" {
		return errResponse(id, -32602, "public_key_hex is required")
	}

	// SSRF pre-flight: reject private/internal addresses before storing.
	if err := ssrfValidateEndpoint(endpoint); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("SSRF blocked: %v", err))
	}

	st := s.st
	if st == nil {
		var err error
		st, err = store.Open(s.storePath())
		if err != nil {
			return errResponse(id, -32000, fmt.Sprintf("opening store: %v", err))
		}
		defer st.Close()
	}

	client := newProtocolClient(st, nil)
	if err := client.AddPeer(campfireID, protocol.PeerInfo{
		Endpoint:     endpoint,
		PublicKeyHex: publicKeyHex,
	}); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("adding peer: %v", err))
	}

	result, _ := toolResultJSON(map[string]interface{}{
		"status":         "ok",
		"campfire_id":    campfireID,
		"endpoint":       endpoint,
		"public_key_hex": publicKeyHex,
	})
	return okResponse(id, result)
}

func (s *server) handleRemovePeer(id interface{}, params map[string]interface{}) jsonRPCResponse {
	campfireID := getStr(params, "campfire_id")
	if campfireID == "" {
		return errResponse(id, -32602, "campfire_id is required")
	}
	publicKeyHex := getStr(params, "public_key_hex")
	if publicKeyHex == "" {
		return errResponse(id, -32602, "public_key_hex is required")
	}

	st := s.st
	if st == nil {
		var err error
		st, err = store.Open(s.storePath())
		if err != nil {
			return errResponse(id, -32000, fmt.Sprintf("opening store: %v", err))
		}
		defer st.Close()
	}

	client := newProtocolClient(st, nil)
	if err := client.RemovePeer(campfireID, publicKeyHex); err != nil {
		return errResponse(id, -32000, fmt.Sprintf("removing peer: %v", err))
	}

	result, _ := toolResultJSON(map[string]interface{}{
		"status":         "ok",
		"campfire_id":    campfireID,
		"public_key_hex": publicKeyHex,
	})
	return okResponse(id, result)
}

func (s *server) handlePeers(id interface{}, params map[string]interface{}) jsonRPCResponse {
	campfireID := getStr(params, "campfire_id")
	if campfireID == "" {
		return errResponse(id, -32602, "campfire_id is required")
	}

	st := s.st
	if st == nil {
		var err error
		st, err = store.Open(s.storePath())
		if err != nil {
			return errResponse(id, -32000, fmt.Sprintf("opening store: %v", err))
		}
		defer st.Close()
	}

	client := newProtocolClient(st, nil)
	peers, err := client.Peers(campfireID)
	if err != nil {
		return errResponse(id, -32000, fmt.Sprintf("listing peers: %v", err))
	}

	type peerEntry struct {
		Endpoint      string `json:"endpoint"`
		PublicKeyHex  string `json:"public_key_hex"`
		ParticipantID string `json:"participant_id,omitempty"`
	}
	entries := make([]peerEntry, len(peers))
	for i, p := range peers {
		entries[i] = peerEntry{
			Endpoint:      p.Endpoint,
			PublicKeyHex:  p.PublicKeyHex,
			ParticipantID: p.ParticipantID,
		}
	}

	result, _ := toolResultJSON(map[string]interface{}{
		"campfire_id": campfireID,
		"peers":       entries,
		"count":       len(entries),
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
		allTools := make([]mcpToolInfo, len(baseTools))
		copy(allTools, baseTools)
		if s.exposePrimitives {
			allTools = append(allTools, primitiveTools...)
		}
		if s.conventionTools != nil {
			for _, ct := range s.conventionTools.list() {
				allTools = append(allTools, mcpToolInfo{
					Name:        ct.Name,
					Description: ct.Description,
					InputSchema: ct.InputSchema,
				})
			}
		}
		return okResponse(req.ID, map[string]interface{}{"tools": allTools})

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
		case "campfire_audit":
			return s.handleAudit(req.ID, callParams.Arguments)
		case "campfire_add_peer":
			return s.handleAddPeer(req.ID, callParams.Arguments)
		case "campfire_remove_peer":
			return s.handleRemovePeer(req.ID, callParams.Arguments)
		case "campfire_peers":
			return s.handlePeers(req.ID, callParams.Arguments)
		default:
			if s.conventionTools != nil {
				if entry, ok := s.conventionTools.get(callParams.Name); ok {
					return s.handleConventionTool(req.ID, entry, callParams.Arguments)
				}
				if viewEntry, ok := s.conventionTools.getView(callParams.Name); ok {
					return s.handleViewTool(req.ID, viewEntry, callParams.Arguments)
				}
			}
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
		// campfire_init with no token: enforce per-IP rate limit before issuing.
		clientIP := clientIPFromRequest(r)
		if err := s.sessManager.checkInitRateLimit(clientIP); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(errResponse(req.ID, -32000, fmt.Sprintf("rate limit exceeded: too many new sessions from this IP; retry after 1 minute"))) //nolint:errcheck
			return
		}
		// Issue a new registered token.
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
		// Audit: record revoke_session before closing the session so the
		// auditWriter is still open when Log is called (§5.e).
		if sess := s.sessManager.getSession(token); sess != nil {
			sess.mu.Lock()
			aw := sess.auditWriter
			cfHome := sess.cfHome
			sess.mu.Unlock()
			if aw != nil {
				agentKey := ""
				if aid, err := identity.Load(filepath.Join(cfHome, "identity.json")); err == nil {
					agentKey = aid.PublicKeyHex()
				}
				aw.Log(AuditEntry{
					Timestamp: time.Now().UnixNano(),
					Action:    "revoke_session",
					AgentKey:  agentKey,
				})
			}
		}
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
		// Audit: record rotate_token after successful rotation. Use the new
		// token to look up the session (rotateToken updates sess.token) (§5.e).
		if sess := s.sessManager.getSession(newToken); sess != nil {
			sess.mu.Lock()
			aw := sess.auditWriter
			cfHome := sess.cfHome
			sess.mu.Unlock()
			if aw != nil {
				agentKey := ""
				if aid, err := identity.Load(filepath.Join(cfHome, "identity.json")); err == nil {
					agentKey = aid.PublicKeyHex()
				}
				aw.Log(AuditEntry{
					Timestamp: time.Now().UnixNano(),
					Action:    "rotate_token",
					AgentKey:  agentKey,
				})
			}
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

// clientIPFromRequest extracts the client IP address from an HTTP request.
// It prefers the leftmost address in X-Forwarded-For (set by reverse proxies
// like Fly.io's edge) and falls back to the bare host from RemoteAddr.
// The returned string is a bare IP with no port.
func clientIPFromRequest(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For may contain a comma-separated list; take the first.
		if idx := strings.IndexByte(xff, ','); idx >= 0 {
			xff = xff[:idx]
		}
		ip := strings.TrimSpace(xff)
		if ip != "" {
			return ip
		}
	}
	// Fall back to RemoteAddr (host:port or bare host).
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr // already bare IP (unusual but safe)
	}
	return host
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

	// Start the convention billing sweep background loop if wired.
	// The loop runs both the fallback Sweeper and BillingSweep on a shared
	// 10-minute interval. It is a no-op if billingSweep is nil.
	if s.billingSweep != nil {
		go s.startBillingSweepLoop(context.Background())
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

// startBillingSweepLoop runs BillingSweep on a 10-minute ticker until ctx is
// cancelled. Runs one pass immediately on startup, then every 10 minutes.
// Logs results at each interval; per-record errors are already logged by BillingSweep.
func (s *server) startBillingSweepLoop(ctx context.Context) {
	const sweepInterval = 10 * time.Minute

	runOnce := func() {
		billed, err := s.billingSweep.Run(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cf-mcp: convention billing sweep error: %v\n", err)
		} else if billed > 0 {
			fmt.Fprintf(os.Stderr, "cf-mcp: convention billing sweep: billed %d record(s)\n", billed)
		}
	}

	// Run once immediately so the first billing pass doesn't wait 10 minutes.
	runOnce()

	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce()
		}
	}
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
	tokenTTLFlag := ""
	maxSessionsFlag := ""
	rotationGracePeriodFlag := ""
	exposePrimitives := false
	for i, arg := range os.Args[1:] {
		switch {
		case arg == "--expose-primitives":
			exposePrimitives = true
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
		case arg == "--token-ttl" && i+1 < len(os.Args[1:]):
			tokenTTLFlag = os.Args[i+2]
		case strings.HasPrefix(arg, "--token-ttl="):
			tokenTTLFlag = strings.TrimPrefix(arg, "--token-ttl=")
		case arg == "--max-sessions" && i+1 < len(os.Args[1:]):
			maxSessionsFlag = os.Args[i+2]
		case strings.HasPrefix(arg, "--max-sessions="):
			maxSessionsFlag = strings.TrimPrefix(arg, "--max-sessions=")
		case arg == "--rotation-grace-period" && i+1 < len(os.Args[1:]):
			rotationGracePeriodFlag = os.Args[i+2]
		case strings.HasPrefix(arg, "--rotation-grace-period="):
			rotationGracePeriodFlag = strings.TrimPrefix(arg, "--rotation-grace-period=")
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

	// Resolve session tuning: flag > env > built-in default (zero = use package default).
	if tokenTTLFlag == "" {
		tokenTTLFlag = os.Getenv("CF_TOKEN_TTL")
	}
	if maxSessionsFlag == "" {
		maxSessionsFlag = os.Getenv("CF_MAX_SESSIONS")
	}
	if rotationGracePeriodFlag == "" {
		rotationGracePeriodFlag = os.Getenv("CF_ROTATION_GRACE_PERIOD")
	}
	var tokenTTL time.Duration
	if tokenTTLFlag != "" {
		d, err := time.ParseDuration(tokenTTLFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: invalid --token-ttl value %q: %v\n", tokenTTLFlag, err)
			os.Exit(1)
		}
		tokenTTL = d
	}
	var maxSessions int
	if maxSessionsFlag != "" {
		n, err := fmt.Sscanf(maxSessionsFlag, "%d", &maxSessions)
		if err != nil || n != 1 || maxSessions <= 0 {
			fmt.Fprintf(os.Stderr, "error: invalid --max-sessions value %q: must be a positive integer\n", maxSessionsFlag)
			os.Exit(1)
		}
	}
	var rotationGracePeriod time.Duration
	if rotationGracePeriodFlag != "" {
		d, err := time.ParseDuration(rotationGracePeriodFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: invalid --rotation-grace-period value %q: %v\n", rotationGracePeriodFlag, err)
			os.Exit(1)
		}
		rotationGracePeriod = d
	}

	srv := &server{
		cfHome:           cfHome,
		beaconDir:        beaconDir,
		cfHomeExplicit:   cfHomeExplicit,
		exposePrimitives: exposePrimitives,
	}
	// M8: Wire convention metering hook on the ConventionDispatcher.
	// wireConventionMetering is a no-op when forgeEmitter is nil (development / stdio mode).
	// In hosted mode (Azure Functions), forgeEmitter is set before this runs.
	// Also saves the DispatchStore on srv.conventionDispatchStore for BillingSweep.
	srv.wireConventionMetering(srv.forgeEmitter)

	// Wire the BillingSweep using the same DispatchStore as the ConventionDispatcher.
	// wireBillingSweep is a no-op when forgeEmitter or conventionDispatchStore is nil.
	// The sweep loop is started in serveHTTP.
	srv.wireBillingSweep(srv.forgeEmitter)

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
			// Apply operator-configurable session tuning (zero values keep package defaults).
			if tokenTTL > 0 {
				sm.tokenTTL = tokenTTL
			}
			if maxSessions > 0 {
				sm.maxSessions = maxSessions
			}
			if rotationGracePeriod > 0 {
				sm.rotationGracePeriod = rotationGracePeriod
			}
			sm.router = router
			sm.exposePrimitives = exposePrimitives
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

			// Wire Azure Table Storage backend when the connection string is
			// set. This makes sessions durable across Azure Functions instances:
			// tokens, identities, and campfire state all survive instance hops.
			if azConnStr := os.Getenv("AZURE_STORAGE_CONNECTION_STRING"); azConnStr != "" {
				ss, ssErr := aztable.NewSessionStore(azConnStr)
				if ssErr != nil {
					fmt.Fprintf(os.Stderr, "warning: azure session store init failed (%v); falling back to local-only persistence\n", ssErr)
				} else {
					if useErr := sm.UseSessionStore(ss); useErr != nil {
						fmt.Fprintf(os.Stderr, "warning: azure session store load failed (%v); continuing with empty registry\n", useErr)
					}
					// Wire per-session namespaced aztable store as the campfire
					// data backend. Each session gets its own namespace within the
					// shared Azure Storage tables, equivalent to SQLite isolation.
					connStr := azConnStr // capture for closure
					sm.storeFactory = func(internalID string) (store.Store, error) {
						return aztable.NewNamespacedTableStore(connStr, internalID)
					}
				}
			}

			srv.sessManager = sm
			srv.transportRouter = router

			// T5: Propagate the ConventionDispatcher to the SessionManager so that
			// per-session transports can wire the OnMessageDelivered hook for P2P delivery.
			if srv.conventionDispatcher != nil {
				sm.conventionDispatcher = srv.conventionDispatcher
			}

			// Wire Forge account auto-provisioning when the operator account store
			// and Forge service key are both available.
			if azConnStr := os.Getenv("AZURE_STORAGE_CONNECTION_STRING"); azConnStr != "" {
				if opStore, opErr := aztable.NewOperatorAccountStore(azConnStr); opErr != nil {
					fmt.Fprintf(os.Stderr, "warning: operator account store init failed (%v); Forge auto-provisioning disabled\n", opErr)
				} else {
					if mgr := newForgeAccountManager(opStore); mgr != nil {
						sm.forgeAccounts = mgr
					}
				}
			}

			// T4: Wire convention server registry when Azure Table Storage is available.
			// This enables the ConventionDispatcher to load registered handlers per campfire.
			if azConnStr := os.Getenv("AZURE_STORAGE_CONNECTION_STRING"); azConnStr != "" {
				if csStore, csErr := aztable.NewConventionServerStore(azConnStr); csErr != nil {
					fmt.Fprintf(os.Stderr, "warning: convention server store init failed (%v); convention dispatch disabled\n", csErr)
				} else {
					srv.conventionServerStore = csStore
				}
			}
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

	// Flush and close the audit writer so all buffered entries are written
	// before the process exits. Without this, entries queued in the channel
	// after the last dispatch would be silently dropped.
	if srv.auditWriter != nil {
		srv.auditWriter.Close()
	}
}
