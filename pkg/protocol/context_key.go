package protocol

// context_key.go — context key delegation for protocol.Init().
//
// When Init() is called in a directory whose .campfire/center sentinel names a
// center campfire, a context key (Ed25519 keypair) is generated for the current
// directory context and a delegation cert is issued.
//
// Delegation cert format:
//   - Plaintext signed: "delegate:" + hex(contextPubKey)  (UTF-8, no newline)
//   - Signed by: center campfire's Ed25519 private key
//   - Stored as: hex-encoded 64-byte Ed25519 signature, no newline
//   - File: configDir/.campfire/delegation.cert
//
// Context key files:
//   - configDir/.campfire/context-key.pub  — raw 32-byte Ed25519 public key
//   - configDir/.campfire/context-key.json — full identity.json for private key
//
// Delegation message posted to center campfire:
//   - Payload: "context-key-delegation:" + hex(contextPubKey)
//   - Tag: "context-key-delegation"

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/transport"
	"github.com/campfire-net/campfire/pkg/transport/fs"
)

const (
	campfireSubdir       = ".campfire"
	contextKeyPubFile    = "context-key.pub"
	contextKeyIdentFile  = "context-key.json"
	delegationCertFile   = "delegation.cert"
	centerSentinelFile   = "center"
	delegationTagPrefix  = "context-key-delegation"
	delegationMsgPrefix  = "context-key-delegation:"
	certSignPrefix       = "delegate:"
)

// maybeIssueContextKeyDelegation is called from Init() after identity and store
// are set up. It walks up from configDir looking for a center campfire sentinel
// (naming.ResolveContext), generates a context Ed25519 key if needed, signs a
// delegation cert with the center campfire's private key, persists the files, and
// posts a delegation message to the center campfire.
//
// It is idempotent: if context-key.pub already exists at the expected path, it
// returns without creating a new key or cert.
//
// If no center campfire is found in the walk-up path, it returns nil — no key,
// no cert, no error.
//
// c must already be fully initialised (store and identity present).
func (c *Client) maybeIssueContextKeyDelegation(configDir string) error {
	if !c.opts.walkUp {
		return nil
	}

	centerCampfireID := walkUpForCenter(configDir)
	if centerCampfireID == "" {
		return nil // no center — nothing to do
	}

	// Derive the .campfire directory path within configDir.
	campfireDir := filepath.Join(configDir, campfireSubdir)
	if err := os.MkdirAll(campfireDir, 0700); err != nil {
		return fmt.Errorf("context key delegation: creating .campfire dir: %w", err)
	}

	contextKeyPubPath := filepath.Join(campfireDir, contextKeyPubFile)
	contextKeyIdentPath := filepath.Join(campfireDir, contextKeyIdentFile)
	delegationCertPath := filepath.Join(campfireDir, delegationCertFile)

	// Idempotency: if context key already exists, skip.
	if _, err := os.Stat(contextKeyPubPath); err == nil {
		return nil
	}

	// Generate context Ed25519 keypair.
	ctxID, err := identity.Generate()
	if err != nil {
		return fmt.Errorf("context key delegation: generating context key: %w", err)
	}

	// Retrieve center campfire state to obtain its private key.
	centerPrivKey, err := c.centerCampfirePrivateKey(centerCampfireID)
	if err != nil {
		return fmt.Errorf("context key delegation: reading center key: %w", err)
	}

	// Sign delegation cert: Ed25519 over "delegate:" + hex(contextPubKey).
	contextPubHex := hex.EncodeToString(ctxID.PublicKey)
	signInput := []byte(certSignPrefix + contextPubHex)
	sig := ed25519.Sign(centerPrivKey, signInput)

	// Persist context key private identity.
	if err := ctxID.Save(contextKeyIdentPath); err != nil {
		return fmt.Errorf("context key delegation: saving context key identity: %w", err)
	}

	// Persist context key public key (raw 32 bytes).
	if err := os.WriteFile(contextKeyPubPath, []byte(ctxID.PublicKey), 0644); err != nil {
		// Attempt cleanup of the identity file.
		os.Remove(contextKeyIdentPath)
		return fmt.Errorf("context key delegation: writing context-key.pub: %w", err)
	}

	// Persist delegation cert (hex-encoded 64-byte signature).
	certHex := hex.EncodeToString(sig)
	if err := os.WriteFile(delegationCertPath, []byte(certHex), 0600); err != nil {
		os.Remove(contextKeyIdentPath)
		os.Remove(contextKeyPubPath)
		return fmt.Errorf("context key delegation: writing delegation.cert: %w", err)
	}

	// Post delegation message to the center campfire (best-effort: non-fatal on error).
	msgPayload := delegationMsgPrefix + contextPubHex
	c.Send(SendRequest{ //nolint:errcheck
		CampfireID: centerCampfireID,
		Payload:    []byte(msgPayload),
		Tags:       []string{delegationTagPrefix},
	})

	return nil
}

// centerCampfirePrivateKey retrieves the Ed25519 private key of the center
// campfire from the local store membership record and the campfire state file.
// Only filesystem-transport campfires are supported; others return an error.
func (c *Client) centerCampfirePrivateKey(centerCampfireID string) (ed25519.PrivateKey, error) {
	m, err := c.store.GetMembership(centerCampfireID)
	if err != nil {
		return nil, fmt.Errorf("querying membership for center campfire: %w", err)
	}
	if m == nil {
		return nil, fmt.Errorf("not a member of center campfire %s", shortID(centerCampfireID))
	}

	tt := transport.ResolveType(*m)
	if tt != transport.TypeFilesystem {
		return nil, fmt.Errorf("center campfire uses non-filesystem transport (%v); delegation not supported", tt)
	}

	tr := fs.ForDir(m.TransportDir)
	state, err := tr.ReadState(centerCampfireID)
	if err != nil {
		return nil, fmt.Errorf("reading center campfire state: %w", err)
	}

	return ed25519.PrivateKey(state.PrivateKey), nil
}

// walkUpForCenter is defined in walk_up.go (shared sentinel walk helper).
