// Package cfsession implements the cf-session L3 convention (design v2 §2.9).
//
// cf-session is the replacement for the shared-key session token mechanism
// (pkg/protocol/session.go, removed in cf 0.30). The old mechanism shared a
// single ephemeral private key with all participants, destroying per-worker
// attribution. cf-session provides per-worker keys with parent-bounded grants.
//
// # Architecture (three mechanisms per §2.9)
//
// ## 1. Lazy-mint issuance
//
// At session open, the orchestrator emits a session:open system event with a
// CapabilityTemplate that describes what workers will be granted. No grants are
// pre-minted. When a real worker presents itself (via identity:introduce into
// the session campfire), the orchestrator's session handler responds with a
// delegation:grant scoped as parent_grant ⊓ capability_template. Each worker
// gets exactly one grant tied to its own freshly-generated key.
//
// ## 2. Jail-write (default) and signing-proxy (opt-in)
//
// jail: orchestrator generates worker key, writes it into a 0700 jail directory,
// forks the worker. The key never crosses a network boundary.
//
// signing_proxy: orchestrator keeps the worker key and exposes a Unix socket.
// The worker never holds the private key. The proxy enforces the grant's where
// and op_glob constraints on every signing request.
//
// ## 3. Disposable session log, canonical grant log
//
// On session:close, the session campfire's coordination messages are eligible
// for compaction (operator policy; default 24h retention after close). Issued
// grants live in the OWNER'S IDENTITY CAMPFIRE, not in the session campfire.
// Compacting a closed session does not break re-walk to the human root.
//
// # Layer
//
// L3 — cf-conventions/cf-session/.
// Depends on:
//   - cf-conventions/cf-convention (L2): Declaration, GateEvaluator
//   - cf-conventions/cf-authority/trust (L3): DefaultGateEvaluator
//   - cf-protocol/protocol (L1): Session, EmitSessionOpen, EmitSessionClose
//
// Does NOT define the Convention dispatch server — callers wire that via
// cf-convention's Executor / ConventionDispatcher. The session handler registers
// its handlers with the dispatcher's dispatch table.
//
// Tracked bead: campfireagent-c3a.
package cfsession

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	cfprotocol "github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"
	convention "github.com/campfire-net/campfire/cf-conventions/cf-convention"
)

// ── Convention constants ──────────────────────────────────────────────────────

// SessionConvention is the convention name for cf-session operations.
const SessionConvention = "cf-session"

// sessionVersion is the convention version.
const sessionVersion = "0.1"

// ── Session lifecycle tags (re-exported from cf-protocol) ────────────────────

// TagSessionOpen is the session:open L1 system event tag.
// Wire value: "session:open" — frozen at cf-protocol 1.0.
const TagSessionOpen = cfprotocol.TagSessionOpen

// TagSessionClose is the session:close L1 system event tag.
// Wire value: "session:close" — frozen at cf-protocol 1.0.
const TagSessionClose = cfprotocol.TagSessionClose

// ── KeyHandling — backend selector ───────────────────────────────────────────

// KeyHandling selects the worker key receipt backend for this session.
// Declared in the cf-session convention declaration's key_handling field.
type KeyHandling string

const (
	// KeyHandlingJail is the default backend: orchestrator generates the worker
	// key and writes it into a 0700 jail directory. Key never crosses the network.
	// Trust boundary: parent process (equivalent to sudo, legion-779 pattern).
	KeyHandlingJail KeyHandling = "jail"

	// KeyHandlingSigningProxy is the hardened opt-in: orchestrator keeps the
	// worker key and exposes a Unix socket the worker calls for each signature.
	// The worker process never holds the private key.
	// Requires a resident daemon; fails on stateless deployments (§OPEN-026).
	KeyHandlingSigningProxy KeyHandling = "signing_proxy"
)

// ── CapabilityTemplate ───────────────────────────────────────────────────────

// CapabilityTemplate is the intersection template encoded in the session:open
// system event. Issued worker grants are scoped as parent_grant ⊓ template.
// Workers can never exceed the template's scope.
type CapabilityTemplate struct {
	// Convention is the convention the session grants access to
	// (e.g. "swarm-coordination").
	Convention string `json:"convention"`

	// OpGlob is the op pattern the grant covers (e.g. "*" or "claim-item|complete").
	OpGlob string `json:"op_glob"`

	// Until is the absolute expiry (UTC) for all worker grants. Must be ≤ session
	// expiry. Worker grants inherit this as their maximum TTL.
	Until time.Time `json:"until"`

	// Where is an optional campfire ID constraint. Empty = no restriction.
	// Worker grants are further bounded to this campfire.
	Where string `json:"where,omitempty"`
}

// ── SessionOpenPayload ────────────────────────────────────────────────────────

// SessionOpenPayload is the JSON body of the session:open L1 system event.
// Posted by the orchestrator when it opens a session campfire.
type SessionOpenPayload struct {
	// SessionID is the session campfire ID (hex).
	SessionID string `json:"session_id"`

	// ParentGrantChainRoot is the hex-encoded public key of the trust anchor
	// (the human root per §2.3 re-anchor). Worker grant chains re-walk to this.
	ParentGrantChainRoot string `json:"parent_grant_chain_root"`

	// CapabilityTemplate is the intersection template that bounds worker grants.
	CapabilityTemplate CapabilityTemplate `json:"capability_template"`

	// Until is the session expiry (UTC nanoseconds). Worker grants must expire ≤ Until.
	Until int64 `json:"until"`

	// KeyHandling is the backend used for worker key receipt ("jail" or "signing_proxy").
	KeyHandling KeyHandling `json:"key_handling"`
}

// ── SessionClosePayload ───────────────────────────────────────────────────────

// SessionClosePayload is the JSON body of the session:close L1 system event.
// Posted by the orchestrator (campfire-key-signed) when the session ends.
type SessionClosePayload struct {
	// SessionID is the session campfire ID (hex).
	SessionID string `json:"session_id"`

	// Reason is the close reason: "normal", "ttl_expired", "orchestrator_end".
	Reason string `json:"reason"`

	// ClosedAt is the close timestamp (UTC nanoseconds since epoch).
	ClosedAt int64 `json:"closed_at"`
}

// ── WorkerIdentity ────────────────────────────────────────────────────────────

// WorkerIdentity holds the Ed25519 keypair for a session worker.
// In jail mode, this is written to <jail>/campfire/identity.json with mode 0600.
// In signing-proxy mode, the PrivateKey is kept by the orchestrator.
type WorkerIdentity struct {
	// PublicKey is the worker's Ed25519 public key (32 bytes).
	PublicKey ed25519.PublicKey `json:"public_key"`

	// PrivateKey is the worker's Ed25519 private key (64 bytes).
	// NEVER transmit over the network. NEVER pass as a process argument.
	// NEVER store in an environment variable.
	PrivateKey ed25519.PrivateKey `json:"private_key"`
}

// identityFile is the JSON structure written to <jail>/campfire/identity.json.
type identityFile struct {
	PublicKeyHex  string `json:"public_key_hex"`
	PrivateKeyHex string `json:"private_key_hex"`
}

// ── Keymat — jail-write backend ───────────────────────────────────────────────

// GenerateWorkerIdentity generates a fresh Ed25519 keypair for a session worker.
// Each worker MUST get a unique keypair; sessions MUST NOT reuse keys (§2.9.2).
func GenerateWorkerIdentity() (*WorkerIdentity, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("cfsession: generating worker identity: %w", err)
	}
	return &WorkerIdentity{PublicKey: pub, PrivateKey: priv}, nil
}

// MaterializeWorkerIdentity writes a WorkerIdentity to a jail directory
// at <jailPath>/campfire/identity.json with mode 0600 inside a 0700 directory.
// This is the jail-write key receipt backend (§2.9.2, default).
//
// The calling process (orchestrator) generates and writes; the worker process
// reads. The key MUST NOT be passed via process arguments or environment variables.
//
// SECURITY: Trust boundary is the parent process (equivalent to sudo).
// The inode is exclusive to this worker's jail; GC'd at session close.
func MaterializeWorkerIdentity(jailPath string, wid *WorkerIdentity) error {
	campfireDir := filepath.Join(jailPath, "campfire")
	if err := os.MkdirAll(campfireDir, 0700); err != nil {
		return fmt.Errorf("cfsession: creating jail campfire dir %q: %w", campfireDir, err)
	}

	file := identityFile{
		PublicKeyHex:  fmt.Sprintf("%x", wid.PublicKey),
		PrivateKeyHex: fmt.Sprintf("%x", wid.PrivateKey),
	}
	data, err := json.Marshal(file)
	if err != nil {
		return fmt.Errorf("cfsession: encoding worker identity: %w", err)
	}

	identPath := filepath.Join(campfireDir, "identity.json")
	if err := os.WriteFile(identPath, data, 0600); err != nil {
		return fmt.Errorf("cfsession: writing worker identity to %q: %w", identPath, err)
	}
	return nil
}

// LoadWorkerIdentity reads a WorkerIdentity from a jail directory
// at <jailPath>/campfire/identity.json. Called by the worker process to load
// its key after the orchestrator has materialized it.
func LoadWorkerIdentity(jailPath string) (*WorkerIdentity, error) {
	identPath := filepath.Join(jailPath, "campfire", "identity.json")
	data, err := os.ReadFile(identPath)
	if err != nil {
		return nil, fmt.Errorf("cfsession: reading worker identity from %q: %w", identPath, err)
	}

	var file identityFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("cfsession: decoding worker identity: %w", err)
	}

	// Decode hex keys.
	pubBytes, err := hexDecode(file.PublicKeyHex)
	if err != nil {
		return nil, fmt.Errorf("cfsession: decoding public key hex: %w", err)
	}
	privBytes, err := hexDecode(file.PrivateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("cfsession: decoding private key hex: %w", err)
	}

	if len(pubBytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("cfsession: public key has wrong length %d (want %d)", len(pubBytes), ed25519.PublicKeySize)
	}
	if len(privBytes) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("cfsession: private key has wrong length %d (want %d)", len(privBytes), ed25519.PrivateKeySize)
	}

	return &WorkerIdentity{
		PublicKey:  ed25519.PublicKey(pubBytes),
		PrivateKey: ed25519.PrivateKey(privBytes),
	}, nil
}

// hexDecode decodes a hex string to bytes.
func hexDecode(s string) ([]byte, error) {
	if len(s)%2 != 0 {
		return nil, fmt.Errorf("odd-length hex string")
	}
	buf := make([]byte, len(s)/2)
	for i := 0; i < len(s); i += 2 {
		hi, ok1 := fromHexNibble(s[i])
		lo, ok2 := fromHexNibble(s[i+1])
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("invalid hex character at position %d", i)
		}
		buf[i/2] = hi<<4 | lo
	}
	return buf, nil
}

func fromHexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// ── Session open / close helpers ──────────────────────────────────────────────

// OpenSession emits a session:open system event on the given campfire session.
// The payload includes the capability template and no pre-minted grants (lazy-mint).
//
// sess is a *cfprotocol.Session backed by the session campfire (created via
// cfprotocol.Client.NewSession or equivalent). The session:open event is
// signed by the orchestrator's identity key via sess.
//
// Returns the posted wire message on success.
func OpenSession(sess *cfprotocol.Session, payload *SessionOpenPayload) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("cfsession.OpenSession: encoding payload: %w", err)
	}
	if _, err := cfprotocol.EmitSessionOpen(sess, data); err != nil {
		return fmt.Errorf("cfsession.OpenSession: emitting session:open: %w", err)
	}
	return nil
}

// CloseSession emits a session:close system event on the given campfire session.
// After this, the session campfire's coordination messages are eligible for
// compaction. The grant log in the owner's identity campfire is unaffected.
//
// Returns an error if the event cannot be posted.
func CloseSession(sess *cfprotocol.Session, payload *SessionClosePayload) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("cfsession.CloseSession: encoding payload: %w", err)
	}
	if _, err := cfprotocol.EmitSessionClose(sess, data); err != nil {
		return fmt.Errorf("cfsession.CloseSession: emitting session:close: %w", err)
	}
	return nil
}

// ── Convention declarations ───────────────────────────────────────────────────

// SessionDeclarations returns all cf-session convention operation declarations.
// Register these with a ConventionDispatcher to handle cf-session operations.
func SessionDeclarations() []*convention.Declaration {
	return []*convention.Declaration{
		OpenDeclaration(),
		CloseDeclaration(),
		IntroduceWorkerDeclaration(),
		GrantWorkerDeclaration(),
	}
}

// OpenDeclaration returns the declaration for the cf-session "open" operation.
// The orchestrator posts this to create a new session.
// Signing: campfire_key (the orchestrator's campfire key).
// Produces: session:open tag (L1 system event).
func OpenDeclaration() *convention.Declaration {
	return &convention.Declaration{
		Convention:  SessionConvention,
		Version:     sessionVersion,
		Operation:   "open",
		Description: "Open a new session campfire with lazy-mint grant issuance",
		Signing:     "campfire_key",
		SignerType:  convention.SignerCampfireKey,
		ProducesTags: []convention.TagRule{
			{Tag: TagSessionOpen, Cardinality: "exactly_one"},
		},
		Args: []convention.ArgDescriptor{
			{
				Name:        "session_id",
				Type:        "string",
				Required:    true,
				Description: "Session campfire ID (hex)",
			},
			{
				Name:        "parent_grant_chain_root",
				Type:        "string",
				Required:    true,
				Description: "Trust anchor key (human root, §2.3 re-anchor)",
			},
			{
				Name:        "capability_template",
				Type:        "object",
				Required:    true,
				Description: "Capability template — worker grants are bounded by this",
			},
			{
				Name:        "until",
				Type:        "int",
				Required:    true,
				Description: "Session expiry (nanoseconds since Unix epoch UTC)",
			},
			{
				Name:        "key_handling",
				Type:        "string",
				Required:    false,
				Default:     string(KeyHandlingJail),
				Values:      []string{"jail", "signing_proxy"},
				Description: "Worker key receipt backend (jail or signing_proxy)",
			},
		},
	}
}

// CloseDeclaration returns the declaration for the cf-session "close" operation.
// The orchestrator posts this to close a session.
// Signing: campfire_key.
// Produces: session:close tag (L1 system event).
func CloseDeclaration() *convention.Declaration {
	return &convention.Declaration{
		Convention:  SessionConvention,
		Version:     sessionVersion,
		Operation:   "close",
		Description: "Close a session campfire; session messages become eligible for compaction",
		Signing:     "campfire_key",
		SignerType:  convention.SignerCampfireKey,
		ProducesTags: []convention.TagRule{
			{Tag: TagSessionClose, Cardinality: "exactly_one"},
		},
		Args: []convention.ArgDescriptor{
			{
				Name:        "session_id",
				Type:        "string",
				Required:    true,
				Description: "Session campfire ID (hex)",
			},
			{
				Name:        "reason",
				Type:        "string",
				Required:    false,
				Default:     "normal",
				Values:      []string{"normal", "ttl_expired", "orchestrator_end"},
				Description: "Close reason",
			},
		},
	}
}

// IntroduceWorkerDeclaration returns the declaration for the cf-session
// "introduce-worker" operation. Workers post this with their freshly-generated
// public key on first contact with the session campfire. The orchestrator's
// session handler observes this and replies with a delegation:grant.
//
// Signing: member_key (the worker's freshly-generated key).
// Produces: identity:introduction tag (re-using the cf-identity tag).
func IntroduceWorkerDeclaration() *convention.Declaration {
	return &convention.Declaration{
		Convention:  SessionConvention,
		Version:     sessionVersion,
		Operation:   "introduce-worker",
		Description: "Worker self-introduction: post freshly-generated key for lazy-mint grant issuance",
		Signing:     "member_key",
		SignerType:  convention.SignerMemberKey,
		ProducesTags: []convention.TagRule{
			{Tag: "session:worker-introduced", Cardinality: "exactly_one"},
		},
		Args: []convention.ArgDescriptor{
			{
				Name:        "worker_pubkey_hex",
				Type:        "string",
				Required:    true,
				Description: "Worker's Ed25519 public key (hex)",
			},
			{
				Name:        "worker_id",
				Type:        "string",
				Required:    false,
				Description: "Optional worker display name for debugging",
			},
		},
	}
}

// GrantWorkerDeclaration returns the declaration for the cf-session
// "grant-worker" operation. The orchestrator posts this in response to
// introduce-worker. It is a delegation:grant scoped as parent_grant ⊓ template.
//
// Signing: campfire_key (the orchestrator's campfire key — grants are signed
// by the issuer).
// Produces: delegation:grant tag.
func GrantWorkerDeclaration() *convention.Declaration {
	return &convention.Declaration{
		Convention:  SessionConvention,
		Version:     sessionVersion,
		Operation:   "grant-worker",
		Description: "Orchestrator issues a scoped delegation:grant to a worker (lazy-mint)",
		Signing:     "campfire_key",
		SignerType:  convention.SignerCampfireKey,
		ProducesTags: []convention.TagRule{
			{Tag: "delegation:grant", Cardinality: "exactly_one"},
		},
		Args: []convention.ArgDescriptor{
			{
				Name:        "worker_pubkey_hex",
				Type:        "string",
				Required:    true,
				Description: "The worker's Ed25519 public key (hex) — must match introduce-worker",
			},
			{
				Name:        "parent_grant_id",
				Type:        "string",
				Required:    true,
				Description: "ID of the orchestrator's own grant in the identity campfire",
			},
			{
				Name:        "grant_payload_cbor",
				Type:        "string",
				Required:    true,
				Description: "CBOR-encoded GrantPayload (trust.GrantPayload), base64url",
			},
			{
				Name:        "until",
				Type:        "int",
				Required:    true,
				Description: "Grant expiry (nanoseconds since epoch UTC); must be ≤ session until",
			},
		},
	}
}

// ── Grant issuance — lazy-mint handler ────────────────────────────────────────

// GrantRequest carries the inputs to IssueWorkerGrant.
type GrantRequest struct {
	// WorkerPubkey is the worker's freshly-generated Ed25519 public key.
	// Provided by the worker via introduce-worker.
	WorkerPubkey ed25519.PublicKey

	// ParentGrantID is the message ID of the orchestrator's own grant in the
	// identity campfire. Enables re-walk from worker → orchestrator → human root.
	ParentGrantID []byte

	// CapabilityTemplate is the session's intersection template. The issued grant
	// is scoped as parent_grant ⊓ capability_template.
	CapabilityTemplate CapabilityTemplate

	// SessionUntil is the session expiry. The worker grant's Until must be ≤ SessionUntil.
	SessionUntil time.Time

	// Nonce is a fresh random nonce for the Capability. If nil, 16 bytes are generated.
	// Callers may provide a nonce for test reproducibility.
	Nonce []byte

	// GranterPubKey is the 32-byte Ed25519 public key of the orchestrator issuing
	// this grant. When set, it is embedded as GrantPayload.GranterPubKey (CBOR field 5)
	// so the evaluator can verify the trust anchor chain termination.
	// If empty, the field is omitted (fail-closed: evaluator returns Unresolvable
	// when RootPrincipal is set and GranterPubKey is absent).
	GranterPubKey ed25519.PublicKey
}

// GrantResult is the output of IssueWorkerGrant.
type GrantResult struct {
	// Payload is the CBOR-encoded GrantPayload (trust.GrantPayload).
	Payload []byte

	// GrantID is the SHA-256 of the canonical CBOR of the first Capability.
	// Used for Unresolvable reporting and audit.
	GrantID []byte
}

// IssueWorkerGrant constructs a CBOR-encoded GrantPayload for a worker,
// scoped as parent_grant ⊓ capability_template.
//
// This implements the lazy-mint issuance flow (§2.9.1): called by the
// orchestrator's session handler when it observes a worker's introduce-worker
// message. The resulting payload is posted as a grant-worker convention op.
//
// The evaluator constraint D7 (no forever grants) is enforced: Until is clamped
// to min(SessionUntil, now+24h). The depth is set to 1 (orchestrator-granted;
// the orchestrator's depth-0 grant lives in the identity campfire).
func IssueWorkerGrant(req GrantRequest) (*GrantResult, error) {
	if len(req.WorkerPubkey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("cfsession.IssueWorkerGrant: worker pubkey must be %d bytes, got %d",
			ed25519.PublicKeySize, len(req.WorkerPubkey))
	}
	if req.CapabilityTemplate.Convention == "" {
		return nil, fmt.Errorf("cfsession.IssueWorkerGrant: capability template convention is required")
	}
	if req.SessionUntil.IsZero() {
		return nil, fmt.Errorf("cfsession.IssueWorkerGrant: session expiry (SessionUntil) is required")
	}

	// Nonce: generate if not provided.
	nonce := req.Nonce
	if len(nonce) == 0 {
		nonce = make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			return nil, fmt.Errorf("cfsession.IssueWorkerGrant: generating nonce: %w", err)
		}
	}

	// Compute grant expiry: min(CapabilityTemplate.Until, SessionUntil).
	grantUntil := req.CapabilityTemplate.Until
	if grantUntil.IsZero() || grantUntil.After(req.SessionUntil) {
		grantUntil = req.SessionUntil
	}

	// Build the capability.
	cap := trust.Capability{
		Convention: req.CapabilityTemplate.Convention,
		OpPattern:  req.CapabilityTemplate.OpGlob,
		Bounds:     map[string]any{},
		Until:      grantUntil.UnixNano(),
		Nonce:      nonce,
	}
	if cap.OpPattern == "" {
		cap.OpPattern = "*"
	}

	// Build the GrantPayload.
	// Depth=1: this is a one-hop grant from the orchestrator (who holds a depth-0
	// owner grant in their identity campfire). The worker's chain is depth 2 at
	// evaluation time (human→orchestrator→worker), which satisfies MaxChainDepth=2.
	gp := trust.GrantPayload{
		ParentGrantID: req.ParentGrantID,
		ChildPubkey:   req.WorkerPubkey,
		Capabilities:  []trust.Capability{cap},
		Depth:         1,
		GranterPubKey: req.GranterPubKey, // CBOR field 5 — required for trust anchor check (D3)
	}

	payload, err := trust.MarshalGrantPayloadCBOR(gp)
	if err != nil {
		return nil, fmt.Errorf("cfsession.IssueWorkerGrant: marshaling grant payload: %w", err)
	}

	grantID, err := trust.CapabilityGrantID(cap)
	if err != nil {
		return nil, fmt.Errorf("cfsession.IssueWorkerGrant: computing grant ID: %w", err)
	}

	return &GrantResult{
		Payload: payload,
		GrantID: grantID,
	}, nil
}

// ── Signing-proxy adapter ─────────────────────────────────────────────────────

// SigningProxy is the opt-in hardening backend for worker key handling (§2.9.2).
// The orchestrator keeps the worker's private key; the worker calls a Unix socket
// for every signature. The proxy enforces grant constraints on each signing request.
//
// This is the cf-session generalization of legion's internal/automaton/signing.go
// pattern. The campfire allowlist matches the grant's `where` constraint; the
// tag allowlist matches the grant's `op_glob` via the convention declaration.
type SigningProxy struct {
	// privateKey is the worker's private key, kept by the orchestrator.
	privateKey ed25519.PrivateKey
	// publicKey is the corresponding public key.
	publicKey ed25519.PublicKey

	// allowedCampfires is the set of campfire IDs the proxy will sign for.
	// Derived from the grant's where constraint. Empty = session campfire only.
	allowedCampfires map[string]bool

	// socketPath is the Unix socket path the worker connects to.
	socketPath string
}

// NewSigningProxy creates a SigningProxy for a worker.
// privateKey is the worker key kept by the orchestrator.
// allowedCampfires is the set of campfire IDs for which the proxy will sign.
// socketPath is the Unix socket the worker calls for signing requests.
func NewSigningProxy(privateKey ed25519.PrivateKey, allowedCampfires []string, socketPath string) *SigningProxy {
	pub := privateKey.Public().(ed25519.PublicKey)
	campfires := make(map[string]bool, len(allowedCampfires))
	for _, cf := range allowedCampfires {
		campfires[cf] = true
	}
	return &SigningProxy{
		privateKey:       privateKey,
		publicKey:        pub,
		allowedCampfires: campfires,
		socketPath:       socketPath,
	}
}

// PublicKey returns the worker's public key.
func (sp *SigningProxy) PublicKey() ed25519.PublicKey {
	return sp.publicKey
}

// SocketPath returns the Unix socket path for this proxy.
func (sp *SigningProxy) SocketPath() string {
	return sp.socketPath
}

// CampfireAllowed returns true if the proxy will sign requests for the given campfire ID.
func (sp *SigningProxy) CampfireAllowed(campfireID string) bool {
	if len(sp.allowedCampfires) == 0 {
		return true // no restriction
	}
	return sp.allowedCampfires[campfireID]
}

// Sign signs a message payload for the given campfire, enforcing the allowlist.
// Returns an error if campfireID is not in the allowlist.
func (sp *SigningProxy) Sign(campfireID string, payload []byte) ([]byte, error) {
	if !sp.CampfireAllowed(campfireID) {
		return nil, fmt.Errorf("cfsession.SigningProxy: campfire %q not in allowlist", campfireID)
	}
	return ed25519.Sign(sp.privateKey, payload), nil
}

// ── GateEvaluator wiring ──────────────────────────────────────────────────────

// NewGateEvaluator returns a GateEvaluator backed by DefaultGateEvaluator from
// cf-authority/trust. Used to gate cf-session convention operations.
//
// For production use, wire this into the ConventionDispatcher via SetGateEvaluator.
func NewGateEvaluator() convention.GateEvaluator {
	return trust.NewConventionAdapter()
}

// ── Jail-write policy invariants ──────────────────────────────────────────────

// JailPolicy enumerates the jail-write backend invariants.
// These are checked by tests; violations are security defects.
const (
	// JailDirMode is the required mode for the jail directory.
	// 0700 — only the owning process (orchestrator) can enter.
	JailDirMode = os.FileMode(0700)

	// IdentityFileMode is the required mode for identity.json inside the jail.
	// 0600 — readable only by the owning process; not group- or world-readable.
	IdentityFileMode = os.FileMode(0600)
)

// VerifyJailPermissions checks that the jail directory and identity file have
// the required permissions. Returns an error if any permission is wrong.
// Used by tests and potentially by the orchestrator's pre-flight checks.
func VerifyJailPermissions(jailPath string) error {
	dirInfo, err := os.Stat(jailPath)
	if err != nil {
		return fmt.Errorf("cfsession.VerifyJailPermissions: stat jail dir %q: %w", jailPath, err)
	}
	if dirInfo.Mode().Perm() != JailDirMode {
		return fmt.Errorf("cfsession.VerifyJailPermissions: jail dir %q has mode %o, want %o",
			jailPath, dirInfo.Mode().Perm(), JailDirMode)
	}

	identPath := filepath.Join(jailPath, "campfire", "identity.json")
	fileInfo, err := os.Stat(identPath)
	if err != nil {
		return fmt.Errorf("cfsession.VerifyJailPermissions: stat identity file %q: %w", identPath, err)
	}
	if fileInfo.Mode().Perm() != IdentityFileMode {
		return fmt.Errorf("cfsession.VerifyJailPermissions: identity file %q has mode %o, want %o",
			identPath, fileInfo.Mode().Perm(), IdentityFileMode)
	}
	return nil
}
