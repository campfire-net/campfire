// admin_campfire.go — Admin campfire convention for operator API key provisioning.
//
// When CF_ADMIN_CAMPFIRE and CF_ADMIN_ALLOWLIST are set, cf-mcp starts a
// convention.Server on the designated admin campfire. The server exposes one
// operation: admin:create-key. Only senders whose Ed25519 public keys appear
// in the allowlist may invoke it. On success, the handler calls
// Forge.CreateKey and returns the plaintext key in the convention response.
//
// Environment variables:
//
//	CF_ADMIN_CAMPFIRE   — hex campfire ID to serve on (required to enable)
//	CF_ADMIN_ALLOWLIST  — comma-separated Ed25519 pubkeys (hex) allowed to call
//	                      admin:create-key; empty list rejects all senders
//
// Security invariants:
//   - Allowlist is checked before any Forge call — fail closed.
//   - Key plaintext is returned in convention response only; never logged.
//   - If CF_ADMIN_CAMPFIRE is unset, setup is skipped (feature disabled).
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/forge"
	"github.com/campfire-net/campfire/pkg/protocol"
)

// adminCampfireConfig holds resolved configuration for the admin convention.
type adminCampfireConfig struct {
	campfireID string
	allowlist  []string // hex-encoded Ed25519 pubkeys
}

// loadAdminCampfireConfig reads CF_ADMIN_CAMPFIRE and CF_ADMIN_ALLOWLIST from
// the environment. Returns nil when CF_ADMIN_CAMPFIRE is not set (disabled).
func loadAdminCampfireConfig() *adminCampfireConfig {
	campfireID := os.Getenv("CF_ADMIN_CAMPFIRE")
	if campfireID == "" {
		return nil
	}
	raw := os.Getenv("CF_ADMIN_ALLOWLIST")
	var allowlist []string
	for _, k := range strings.Split(raw, ",") {
		k = strings.TrimSpace(k)
		if k != "" {
			allowlist = append(allowlist, k)
		}
	}
	return &adminCampfireConfig{
		campfireID: campfireID,
		allowlist:  allowlist,
	}
}

// adminConventionDecl is the Declaration for the admin:create-key operation.
var adminConventionDecl = &convention.Declaration{
	Convention: "admin",
	Version:    "0.1",
	Operation:  "create-key",
	Signing:    "member_key",
	Antecedents: "none",
	Args: []convention.ArgDescriptor{
		{Name: "account_id", Type: "string", Required: true},
		{Name: "name", Type: "string", Required: false},
	},
	ProducesTags: []convention.TagRule{
		{Tag: "admin:create-key", Cardinality: "exactly_one"},
	},
}

// forgeKeyCreator is the interface satisfied by *forge.Client that the admin
// handler depends on. Abstracted for testing.
type forgeKeyCreator interface {
	CreateKey(ctx context.Context, accountID, role string) (forge.Key, error)
}

// buildAdminHandler returns a HandlerFunc that validates the sender against the
// allowlist and, if allowed, calls forge.CreateKey and returns the plaintext key.
// The forge parameter must not be nil — callers must check before wiring.
func buildAdminHandler(allowlist []string, fc forgeKeyCreator) convention.HandlerFunc {
	// Build a quick lookup set.
	allowed := make(map[string]bool, len(allowlist))
	for _, k := range allowlist {
		allowed[strings.ToLower(k)] = true
	}

	return func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		// 1. Allowlist check — fail closed.
		if !allowed[strings.ToLower(req.Sender)] {
			return &convention.Response{
				Payload: map[string]string{"error": "sender not in allowlist"},
			}, nil
		}

		// 2. Extract account_id.
		accountID, _ := req.Args["account_id"].(string)
		if accountID == "" {
			return &convention.Response{
				Payload: map[string]string{"error": "account_id is required"},
			}, nil
		}

		// 3. Call Forge — key plaintext is returned and must not be logged.
		key, err := fc.CreateKey(ctx, accountID, "tenant")
		if err != nil {
			return &convention.Response{
				Payload: map[string]string{"error": fmt.Sprintf("forge error: %v", err)},
			}, nil
		}

		// 4. Return key plaintext in the convention response. Never log it.
		return &convention.Response{
			Payload: map[string]string{"key": key.KeyPlaintext},
		}, nil
	}
}

// startAdminCampfire starts the admin convention server in a goroutine.
// It is called at startup (after session manager setup) when CF_ADMIN_CAMPFIRE
// is set. ctx governs the lifetime — cancelling it stops the server.
//
// The function creates its own protocol.Client from cfHome (same identity as
// the main server). The Client is NOT shared with any other goroutine.
//
// Returns an error if forge is nil (key creation not possible) or if protocol
// init fails. Returns nil immediately when cfg is nil (feature disabled).
func startAdminCampfire(ctx context.Context, cfg *adminCampfireConfig, cfHome string, fc forgeKeyCreator) error {
	if cfg == nil {
		return nil
	}
	if fc == nil {
		return fmt.Errorf("admin campfire: forge client is required but nil")
	}

	client, err := protocol.Init(cfHome)
	if err != nil {
		return fmt.Errorf("admin campfire: protocol init: %w", err)
	}

	srv := convention.NewServer(client, adminConventionDecl)
	srv.WithErrorHandler(func(err error) {
		log.Printf("admin campfire: convention server error: %v", err)
	})
	srv.RegisterHandler("create-key", buildAdminHandler(cfg.allowlist, fc))

	go func() {
		defer client.Close()
		if err := srv.Serve(ctx, cfg.campfireID); err != nil && ctx.Err() == nil {
			log.Printf("admin campfire: convention server stopped unexpectedly: %v", err)
		}
	}()

	log.Printf("admin campfire: serving admin:create-key on campfire %s (allowlist: %d key(s))",
		cfg.campfireID, len(cfg.allowlist))
	return nil
}

// wireAdminCampfire is called at startup in serveHTTP when the admin campfire
// feature is configured. It reads configuration from environment variables and
// starts the convention server background goroutine.
//
// A non-nil forgeAccountManager is required — without it we cannot call
// CreateKey and the feature is disabled. Logs a warning and returns if not set.
func (s *server) wireAdminCampfire(ctx context.Context) {
	cfg := loadAdminCampfireConfig()
	if cfg == nil {
		return // CF_ADMIN_CAMPFIRE not set — feature disabled
	}

	// Resolve the forge client. In production, forgeAccounts is wired by main()
	// when FORGE_SERVICE_KEY is set. In development it may be nil.
	var fc forgeKeyCreator
	if s.sessManager != nil && s.sessManager.forgeAccounts != nil {
		fc = s.sessManager.forgeAccounts.forge
	}
	if fc == nil {
		log.Printf("admin campfire: CF_ADMIN_CAMPFIRE is set but forge client is nil (FORGE_SERVICE_KEY missing?) — admin convention disabled")
		return
	}

	if err := startAdminCampfire(ctx, cfg, s.cfHome, fc); err != nil {
		log.Printf("admin campfire: startup error: %v", err)
	}
}
