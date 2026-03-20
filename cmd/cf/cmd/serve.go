package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
	"github.com/spf13/cobra"
)

var (
	serveListen  string
	serveTLSCert string
	serveTLSKey  string
)

var serveCmd = &cobra.Command{
	Use:   "serve <campfire-id>",
	Short: "Start HTTP listener for a p2p-http campfire and block until interrupted",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		campfireID := args[0]

		agentID, err := identity.Load(IdentityPath())
		if err != nil {
			return fmt.Errorf("loading identity (run 'cf init' first): %w", err)
		}

		s, err := store.Open(store.StorePath(CFHome()))
		if err != nil {
			return fmt.Errorf("opening store: %w", err)
		}
		defer s.Close()

		campfireID, err = resolveCampfireID(campfireID, s)
		if err != nil {
			return err
		}

		// Verify membership.
		m, err := s.GetMembership(campfireID)
		if err != nil {
			return fmt.Errorf("querying membership: %w", err)
		}
		if m == nil {
			return fmt.Errorf("not a member of campfire %s", campfireID[:12])
		}

		listenAddr := serveListen
		if listenAddr == "" {
			return fmt.Errorf("--listen is required (e.g. --listen :9001)")
		}
		if (serveTLSCert == "") != (serveTLSKey == "") {
			return fmt.Errorf("--tls-cert and --tls-key must both be provided or both omitted")
		}
		useTLS := serveTLSCert != ""

		endpoint := resolveEndpoint(listenAddr, useTLS)

		// Record/update self endpoint.
		s.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
			CampfireID:   campfireID,
			MemberPubkey: agentID.PublicKeyHex(),
			Endpoint:     endpoint,
		})

		tr := cfhttp.New(listenAddr, s)
		if useTLS {
			tr.SetTLSConfig(&cfhttp.TLSConfig{CertFile: serveTLSCert, KeyFile: serveTLSKey})
		}
		tr.SetSelfInfo(agentID.PublicKeyHex(), endpoint)
		tr.SetKeyProvider(buildKeyProvider(CFHome()))
		tr.SetThresholdShareProvider(buildThresholdShareProvider(s))
		if err := tr.Start(); err != nil {
			return fmt.Errorf("starting HTTP listener on %s: %w", listenAddr, err)
		}

		fmt.Fprintf(os.Stderr, "serving campfire %s on %s\n", campfireID[:12], endpoint)

		// Block until SIGINT or SIGTERM.
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh

		return tr.Stop()
	},
}

func init() {
	serveCmd.Flags().StringVar(&serveListen, "listen", "", "HTTP listen address (e.g. :9001)")
	serveCmd.Flags().StringVar(&serveTLSCert, "tls-cert", "", "TLS certificate file (PEM); enables https:// endpoint advertisement")
	serveCmd.Flags().StringVar(&serveTLSKey, "tls-key", "", "TLS private key file (PEM); must be paired with --tls-cert")
	rootCmd.AddCommand(serveCmd)
}
