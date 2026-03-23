// cf-teams is the campfire-to-Teams bidirectional bridge.
//
// It is a campfire protocol member that translates messages between
// campfire and Microsoft Teams, preserving threading, tags, and provenance.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
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

	// Build poller configs for all configured campfires.
	var pollerCfgs []poller.Config
	for _, route := range cfg.Campfire {
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

	// Build campfire channel→ID lookup for conversation ref bootstrap.
	channelToCampfire := make(map[string]string)
	for _, route := range cfg.Campfire {
		if route.TeamsChannel != "" {
			channelToCampfire[route.TeamsChannel] = route.ID
		}
	}

	// Set up inbound (Teams→campfire) handler.
	validator := botframework.NewValidator(cfg.Azure.AppID, false)
	inbound := teams.NewInboundHandler(id, cs, bdb, nil, validator)
	if bfClient != nil {
		inbound = inbound.WithBFClient(bfClient)
	}

	// HTTP handler for Bot Framework activities.
	http.HandleFunc("/api/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			log.Printf("inbound: read body: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		// Parse the activity to extract conversation ref.
		activity, parseErr := botframework.ParseActivity(body)
		if parseErr != nil {
			log.Printf("inbound: parse activity: %v", parseErr)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		log.Printf("inbound: activity type=%s from=%s conv=%s", activity.Type, activity.From.ID, activity.Conversation.ID)

		// Bootstrap conversation ref from any inbound activity.
		// Strip ";messageid=..." suffix — we want the channel-level conversation ID
		// so outbound messages land as top-level posts, not thread replies.
		convID := activity.Conversation.ID
		if idx := strings.Index(convID, ";messageid="); idx != -1 {
			convID = convID[:idx]
		}
		for channel, cfID := range channelToCampfire {
			if containsChannel(convID, channel) {
				ref := state.ConversationRef{
					CampfireID:  cfID,
					TeamsConvID: convID,
					ServiceURL:  activity.ServiceURL,
					TenantID:    activity.Conversation.TenantID,
					ChannelID:   channel,
					BotID:       activity.Recipient.ID,
				}
				if err := bdb.UpsertConversationRef(ref); err != nil {
					log.Printf("inbound: upsert conv ref: %v", err)
				} else {
					log.Printf("inbound: conversation ref stored for campfire %s (conv=%s)", cfID, convID)
				}
				break
			}
		}

		// Handle message and invoke activities.
		authHeader := r.Header.Get("Authorization")
		switch activity.Type {
		case botframework.ActivityTypeMessage, botframework.ActivityTypeInvoke:
			msgID, err := inbound.HandleActivity(r.Context(), authHeader, body)
			if err != nil {
				log.Printf("inbound: handle activity: %v", err)
				// Return 200 anyway — Bot Framework retries on non-2xx.
				w.WriteHeader(http.StatusOK)
				return
			}
			log.Printf("inbound: created campfire message %s", msgID)
			w.WriteHeader(http.StatusOK)
		default:
			// conversationUpdate, typing, etc. — acknowledge.
			log.Printf("inbound: ignoring activity type %s", activity.Type)
			w.WriteHeader(http.StatusOK)
		}
	})

	// Start HTTP server.
	go func() {
		log.Printf("HTTP server listening on %s", cfg.Listen)
		if err := http.ListenAndServe(cfg.Listen, nil); err != nil {
			log.Fatalf("http server: %v", err)
		}
	}()

	select {
	case <-sig:
		log.Println("shutting down")
	case err := <-errCh:
		log.Printf("poller error: %v", err)
	}
	cancel()
}

// containsChannel checks if a Teams conversation ID contains the channel identifier.
func containsChannel(convID, channel string) bool {
	return len(convID) >= len(channel) && (convID == channel || len(convID) > len(channel) && convID[:len(channel)] == channel)
}

func campfireIDs(cfg *bridge.Config) []string {
	ids := make([]string, len(cfg.Campfire))
	for i, c := range cfg.Campfire {
		ids[i] = c.ID
	}
	return ids
}
