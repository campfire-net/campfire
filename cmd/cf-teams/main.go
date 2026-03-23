// cf-teams is the campfire-to-Teams bidirectional bridge.
//
// It is a campfire protocol member that translates messages between
// campfire and Microsoft Teams, preserving threading, tags, and provenance.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/campfire-net/campfire/bridge"
	"github.com/campfire-net/campfire/bridge/teams"
	"github.com/campfire-net/campfire/bridge/teams/botframework"
	"github.com/campfire-net/campfire/bridge/enrichment"
	"github.com/campfire-net/campfire/bridge/poller"
	"github.com/campfire-net/campfire/bridge/state"
	"github.com/campfire-net/campfire/pkg/identity"
	cfstore "github.com/campfire-net/campfire/pkg/store"
)

func main() {
	configPath := flag.String("config", "", "path to bridge config YAML")
	flag.Parse()

	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "usage: cf-teams --config <path>")
		os.Exit(1)
	}

	// Load config.
	cfg, err := bridge.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	log.Printf("config loaded: %d campfire route(s), listen=%s", len(cfg.Campfire), cfg.Listen)

	// Load bridge Ed25519 identity.
	id, err := identity.Load(cfg.Identity)
	if err != nil {
		log.Fatalf("identity: %v", err)
	}
	log.Printf("identity loaded: %s", id.PublicKeyHex())

	// Open campfire protocol store.
	storePath := cfstore.StorePath(cfg.CFHome)
	cs, err := cfstore.Open(storePath)
	if err != nil {
		log.Fatalf("campfire store: %v", err)
	}
	defer cs.Close()
	log.Printf("campfire store opened: %s", storePath)

	// Open bridge state database.
	bdb, err := state.Open(cfg.BridgeDB)
	if err != nil {
		log.Fatalf("bridge db: %v", err)
	}
	defer bdb.Close()
	log.Printf("bridge state db opened: %s", cfg.BridgeDB)

	// Seed identity registry from config.
	for _, ident := range cfg.Idents {
		if err := bdb.SeedIdentity(ident.Pubkey, ident.DisplayName, ident.Role, ident.Color); err != nil {
			log.Printf("warning: seed identity %s: %v", ident.Pubkey, err)
		}
	}

	// Seed ACL from config.
	for _, acl := range cfg.ACL {
		for _, cfID := range acl.Campfires {
			if err := bdb.SeedACL(acl.TeamsUserID, cfID, acl.DisplayName); err != nil {
				log.Printf("warning: seed acl %s: %v", acl.TeamsUserID, err)
			}
		}
	}

	log.Printf("bridge ready — campfires: %v", campfireIDs(cfg))

	// Build poller configs for campfires that have a webhook URL.
	var pollerCfgs []poller.Config
	for _, route := range cfg.Campfire {
		if route.WebhookURL == "" {
			log.Printf("skipping campfire %s: no webhook_url configured", route.ID)
			continue
		}
		pollerCfgs = append(pollerCfgs, poller.Config{
			CampfireID:         route.ID,
			PollInterval:       route.PollInterval,
			UrgentPollInterval: route.UrgentPollInterval,
			UrgentTags:         route.UrgentTags,
		})
	}

	// Optionally build a Bot Framework client when Azure credentials are present.
	var bfClient *botframework.Client
	if cfg.Azure.AppID != "" && cfg.Azure.AppPassword != "" {
		tenantID := cfg.Azure.TenantID
		if tenantID == "" {
			tenantID = "botframework.com"
		}
		tc := botframework.NewTokenClient(cfg.Azure.AppID, cfg.Azure.AppPassword, tenantID)
		bfClient = botframework.NewClient(tc)
		log.Printf("Bot Framework client created (app_id=%s, tenant=%s)", cfg.Azure.AppID, tenantID)
	}

	// Build per-campfire handlers.
	// When BF credentials are available, use BotHandler for rich Adaptive Card delivery.
	// Fall back to WebhookHandler (Phase 1 plain text) when webhook_url is set.
	enrichOpts := enrichment.EnrichOptions{
		UrgentCampfires: cfg.Urgent,
		DB:              bdb,
	}
	handlerByID := make(map[string]poller.MessageHandler, len(cfg.Campfire))
	for _, route := range cfg.Campfire {
		if bfClient != nil {
			h := teams.NewBotHandler(route.ID, enrichOpts, bfClient, bdb)
			handlerByID[route.ID] = h.Handle
			log.Printf("registered BotHandler for campfire %s", route.ID)
		} else if route.WebhookURL != "" {
			handlerByID[route.ID] = teams.WebhookHandler(route.WebhookURL, nil)
			log.Printf("registered WebhookHandler for campfire %s", route.ID)
		}
	}

	// Dispatch handler routes by campfire ID.
	dispatch := func(campfireID string) poller.MessageHandler {
		if h, ok := handlerByID[campfireID]; ok {
			return h
		}
		return func(_ cfstore.MessageRecord) error { return nil }
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Start poller goroutines.
	errCh := make(chan error, len(pollerCfgs))
	for _, pcfg := range pollerCfgs {
		p := poller.New(cs, pcfg, dispatch(pcfg.CampfireID))
		go func() {
			errCh <- p.Run(ctx)
		}()
		log.Printf("poller started for campfire %s (interval=%s)", pcfg.CampfireID, pcfg.PollInterval)
	}

	// Block until signal.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	_ = id // used by Teams→campfire flow in later phases

	select {
	case <-sig:
		log.Println("shutting down")
	case err := <-errCh:
		log.Printf("poller error: %v", err)
	}
	cancel()
}

func campfireIDs(cfg *bridge.Config) []string {
	ids := make([]string, len(cfg.Campfire))
	for i, c := range cfg.Campfire {
		ids[i] = c.ID
	}
	return ids
}
