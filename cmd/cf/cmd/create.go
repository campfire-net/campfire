package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/3dl-dev/campfire/pkg/beacon"
	"github.com/3dl-dev/campfire/pkg/campfire"
	cfencoding "github.com/3dl-dev/campfire/pkg/encoding"
	"github.com/3dl-dev/campfire/pkg/identity"
	"github.com/3dl-dev/campfire/pkg/store"
	"github.com/3dl-dev/campfire/pkg/threshold"
	"github.com/3dl-dev/campfire/pkg/transport/fs"
	cfhttp "github.com/3dl-dev/campfire/pkg/transport/http"
	ghtr "github.com/3dl-dev/campfire/pkg/transport/github"
	"github.com/spf13/cobra"
)

var (
	createProtocol     string
	createRequire      []string
	createDescription  string
	createThreshold    uint
	createTransport    string
	createListen       string
	createParticipants uint

	// GitHub transport flags.
	createGitHubRepo         string
	createGitHubTokenEnv     string
	createGitHubBaseURL      string
)

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new campfire",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Load agent identity
		agentID, err := identity.Load(IdentityPath())
		if err != nil {
			return fmt.Errorf("loading identity (run 'cf init' first): %w", err)
		}

		// Create campfire
		cf, err := campfire.New(createProtocol, createRequire, createThreshold)
		if err != nil {
			return fmt.Errorf("creating campfire: %w", err)
		}

		// Add creator as first member
		cf.AddMember(agentID.PublicKey)

		s, err := store.Open(store.StorePath(CFHome()))
		if err != nil {
			return fmt.Errorf("opening store: %w", err)
		}
		defer s.Close()

		switch createTransport {
		case "github":
			return createGitHub(cf, agentID, s, createDescription)
		case "p2p-http":
			return createP2PHTTP(cf, agentID, s)
		default:
			return createFilesystem(cf, agentID, s)
		}
	},
}

func createFilesystem(cf *campfire.Campfire, agentID *identity.Identity, s *store.Store) error {
	// Set up filesystem transport
	transport := fs.New(fs.DefaultBaseDir())
	if err := transport.Init(cf); err != nil {
		return fmt.Errorf("initializing transport: %w", err)
	}

	// Write creator's member record
	if err := transport.WriteMember(cf.PublicKeyHex(), campfire.MemberRecord{
		PublicKey: agentID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
	}); err != nil {
		return fmt.Errorf("writing member record: %w", err)
	}

	// Publish beacon
	beaconDir := BeaconDir()
	b, err := beacon.New(
		cf.Identity.PublicKey,
		cf.Identity.PrivateKey,
		cf.JoinProtocol,
		cf.ReceptionRequirements,
		beacon.TransportConfig{
			Protocol: "filesystem",
			Config:   map[string]string{"dir": transport.CampfireDir(cf.PublicKeyHex())},
		},
		createDescription,
	)
	if err != nil {
		return fmt.Errorf("creating beacon: %w", err)
	}
	if err := beacon.Publish(beaconDir, b); err != nil {
		return fmt.Errorf("publishing beacon: %w", err)
	}

	// Record membership in local store
	if err := s.AddMembership(store.Membership{
		CampfireID:   cf.PublicKeyHex(),
		TransportDir: transport.CampfireDir(cf.PublicKeyHex()),
		JoinProtocol: cf.JoinProtocol,
		Role:         "creator",
		JoinedAt:     store.NowNano(),
		Threshold:    cf.Threshold,
	}); err != nil {
		return fmt.Errorf("recording membership: %w", err)
	}

	if jsonOutput {
		out := map[string]interface{}{
			"campfire_id":            cf.PublicKeyHex(),
			"join_protocol":          cf.JoinProtocol,
			"reception_requirements": cf.ReceptionRequirements,
			"threshold":              cf.Threshold,
			"transport_dir":          transport.CampfireDir(cf.PublicKeyHex()),
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Println(cf.PublicKeyHex())
	return nil
}

func createP2PHTTP(cf *campfire.Campfire, agentID *identity.Identity, s *store.Store) error {
	listenAddr := createListen
	if listenAddr == "" {
		return fmt.Errorf("--listen is required for p2p-http transport (e.g. --listen :9001)")
	}

	campfireID := cf.PublicKeyHex()

	// Persist campfire state locally so the key provider can serve join requests.
	stateDir := filepath.Join(CFHome(), "campfires")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return fmt.Errorf("creating campfire state directory: %w", err)
	}
	stateFile := filepath.Join(stateDir, campfireID+".cbor")
	stateData, err := cfencoding.Marshal(cf.State())
	if err != nil {
		return fmt.Errorf("encoding campfire state: %w", err)
	}
	if err := os.WriteFile(stateFile, stateData, 0600); err != nil {
		return fmt.Errorf("writing campfire state: %w", err)
	}

	// For threshold>1: run DKG for all participants and store shares.
	// The creator gets participant 1; joiners receive participants 2..N in order.
	if cf.Threshold > 1 {
		n := createParticipants
		if n < cf.Threshold {
			n = cf.Threshold // default: N=threshold (threshold-of-threshold)
		}
		participantIDs := make([]uint32, n)
		for i := uint(0); i < n; i++ {
			participantIDs[i] = uint32(i + 1)
		}
		dkgResults, err := threshold.RunDKG(participantIDs, int(cf.Threshold))
		if err != nil {
			return fmt.Errorf("running DKG: %w", err)
		}

		// Store creator's share (participant 1) in local DB.
		creatorShareData, err := threshold.MarshalResult(1, dkgResults[1])
		if err != nil {
			return fmt.Errorf("serializing creator DKG share: %w", err)
		}
		if err := s.UpsertThresholdShare(store.ThresholdShare{
			CampfireID:    campfireID,
			ParticipantID: 1,
			SecretShare:   creatorShareData,
			PublicData:    nil, // stored within creatorShareData via MarshalResult
		}); err != nil {
			return fmt.Errorf("storing creator threshold share: %w", err)
		}

		// Store pending shares for future joiners (participants 2..N).
		for i := uint32(2); i <= uint32(n); i++ {
			r, ok := dkgResults[i]
			if !ok {
				continue
			}
			shareData, err := threshold.MarshalResult(i, r)
			if err != nil {
				return fmt.Errorf("serializing participant %d DKG share: %w", i, err)
			}
			if err := s.StorePendingThresholdShare(campfireID, i, shareData); err != nil {
				return fmt.Errorf("storing pending share for participant %d: %w", i, err)
			}
		}
	}

	// Resolve endpoint URL from listen address.
	endpoint := resolveEndpoint(listenAddr)

	// Record membership in local store.
	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: stateDir, // reuse field to store state directory
		JoinProtocol: cf.JoinProtocol,
		Role:         "creator",
		JoinedAt:     store.NowNano(),
		Threshold:    cf.Threshold,
	}); err != nil {
		return fmt.Errorf("recording membership: %w", err)
	}

	// Record self as a peer endpoint (participant 1 for threshold>1).
	selfParticipantID := uint32(0)
	if cf.Threshold > 1 {
		selfParticipantID = 1
	}
	if err := s.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:    campfireID,
		MemberPubkey:  agentID.PublicKeyHex(),
		Endpoint:      endpoint,
		ParticipantID: selfParticipantID,
	}); err != nil {
		return fmt.Errorf("recording self endpoint: %w", err)
	}

	// Start HTTP listener.
	tr := cfhttp.New(listenAddr, s)
	tr.SetSelfInfo(agentID.PublicKeyHex(), endpoint)
	tr.SetKeyProvider(buildKeyProvider(CFHome()))
	tr.SetThresholdShareProvider(buildThresholdShareProvider(s))
	if err := tr.Start(); err != nil {
		return fmt.Errorf("starting HTTP listener on %s: %w", listenAddr, err)
	}

	// Publish beacon with p2p-http transport config.
	beaconDir := BeaconDir()
	b, err := beacon.New(
		cf.Identity.PublicKey,
		cf.Identity.PrivateKey,
		cf.JoinProtocol,
		cf.ReceptionRequirements,
		beacon.TransportConfig{
			Protocol: "p2p-http",
			Config:   map[string]string{"endpoints": endpoint},
		},
		createDescription,
	)
	if err != nil {
		return fmt.Errorf("creating beacon: %w", err)
	}
	if err := beacon.Publish(beaconDir, b); err != nil {
		return fmt.Errorf("publishing beacon: %w", err)
	}

	if jsonOutput {
		out := map[string]interface{}{
			"campfire_id":            cf.PublicKeyHex(),
			"join_protocol":          cf.JoinProtocol,
			"reception_requirements": cf.ReceptionRequirements,
			"threshold":              cf.Threshold,
			"transport":              "p2p-http",
			"endpoint":               endpoint,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Printf("%s\n", cf.PublicKeyHex())
	fmt.Fprintf(os.Stderr, "Listening on %s\n", endpoint)
	return nil
}

// createGitHub creates a campfire with the GitHub Issues transport.
// It creates a GitHub Issue, publishes a beacon to the coordination repo,
// and records the membership in the local store.
func createGitHub(cf *campfire.Campfire, agentID *identity.Identity, s *store.Store, description string) error {
	if createGitHubRepo == "" {
		return fmt.Errorf("--github-repo is required for GitHub transport (e.g. org/campfire-relay)")
	}

	token, err := resolveGitHubToken(createGitHubTokenEnv, CFHome())
	if err != nil {
		return fmt.Errorf("resolving GitHub token: %w", err)
	}

	cfg := ghtr.Config{
		Repo:    createGitHubRepo,
		Token:   token,
		BaseURL: createGitHubBaseURL,
	}
	tr, err := ghtr.New(cfg, s)
	if err != nil {
		return fmt.Errorf("creating GitHub transport: %w", err)
	}

	// Create the GitHub Issue for this campfire.
	issueNumber, err := tr.CreateCampfire(cf, description)
	if err != nil {
		return fmt.Errorf("creating campfire issue: %w", err)
	}

	// Build and publish beacon to .campfire/beacons/ in the coordination repo.
	campfireID := cf.PublicKeyHex()
	b := ghtr.Beacon{
		CampfireID:            campfireID,
		JoinProtocol:          cf.JoinProtocol,
		ReceptionRequirements: cf.ReceptionRequirements,
		Transport: ghtr.BeaconTransport{
			Protocol: "github",
			Config: ghtr.BeaconTransportConfig{
				Repo:        createGitHubRepo,
				IssueNumber: issueNumber,
				IssueURL:    fmt.Sprintf("https://github.com/%s/issues/%d", createGitHubRepo, issueNumber),
			},
		},
		Description: description,
	}
	// Sign the beacon with the campfire's private key.
	sig, err := ghtr.SignBeacon(b, cf.Identity.PrivateKey)
	if err != nil {
		return fmt.Errorf("signing beacon: %w", err)
	}
	b.Signature = sig

	client := ghtr.NewClient(createGitHubBaseURL, token)
	if err := ghtr.PublishBeacon(client, createGitHubRepo, b); err != nil {
		// Non-fatal: may lack Contents write permission. Warn and continue.
		fmt.Fprintf(os.Stderr, "warning: could not publish beacon to repo (Contents write required): %v\n", err)
	}

	// Encode transport metadata into TransportDir.
	transportDir, err := encodeGitHubTransportDir(githubTransportMeta{
		Repo:        createGitHubRepo,
		IssueNumber: issueNumber,
		BaseURL:     createGitHubBaseURL,
	})
	if err != nil {
		return fmt.Errorf("encoding transport dir: %w", err)
	}

	// Record membership in local store.
	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: transportDir,
		JoinProtocol: cf.JoinProtocol,
		Role:         "creator",
		JoinedAt:     store.NowNano(),
		Threshold:    cf.Threshold,
	}); err != nil {
		return fmt.Errorf("recording membership: %w", err)
	}

	if jsonOutput {
		out := map[string]interface{}{
			"campfire_id":   campfireID,
			"join_protocol": cf.JoinProtocol,
			"transport":     "github",
			"repo":          createGitHubRepo,
			"issue_number":  issueNumber,
			"issue_url":     fmt.Sprintf("https://github.com/%s/issues/%d", createGitHubRepo, issueNumber),
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Println(campfireID)
	fmt.Fprintf(os.Stderr, "GitHub Issue: https://github.com/%s/issues/%d\n", createGitHubRepo, issueNumber)
	return nil
}

// resolveEndpoint turns a listen address like ":9001" into "http://localhost:9001".
func resolveEndpoint(listenAddr string) string {
	if len(listenAddr) > 0 && listenAddr[0] == ':' {
		return "http://localhost" + listenAddr
	}
	return "http://" + listenAddr
}

// buildKeyProvider returns a CampfireKeyProvider that reads campfire state
// from CBOR files in $CF_HOME/campfires/.
func buildKeyProvider(cfHome string) cfhttp.CampfireKeyProvider {
	stateDir := filepath.Join(cfHome, "campfires")
	return func(campfireID string) (privKey []byte, pubKey []byte, err error) {
		stateFile := filepath.Join(stateDir, campfireID+".cbor")
		data, err := os.ReadFile(stateFile)
		if err != nil {
			return nil, nil, fmt.Errorf("reading campfire state: %w", err)
		}
		var state campfire.CampfireState
		if err := cfencoding.Unmarshal(data, &state); err != nil {
			return nil, nil, fmt.Errorf("decoding campfire state: %w", err)
		}
		return state.PrivateKey, state.PublicKey, nil
	}
}

// buildThresholdShareProvider returns a ThresholdShareProvider that reads FROST DKG
// shares from the local store.
func buildThresholdShareProvider(s *store.Store) cfhttp.ThresholdShareProvider {
	return func(campfireID string) (uint32, []byte, error) {
		share, err := s.GetThresholdShare(campfireID)
		if err != nil {
			return 0, nil, fmt.Errorf("querying threshold share: %w", err)
		}
		if share == nil {
			shortID := campfireID
		if len(shortID) > 12 {
			shortID = shortID[:12]
		}
		return 0, nil, fmt.Errorf("no threshold share found for campfire %s", shortID)
		}
		return share.ParticipantID, share.SecretShare, nil
	}
}


func init() {
	createCmd.Flags().StringVar(&createProtocol, "protocol", "open", "join protocol: open, invite-only")
	createCmd.Flags().StringSliceVar(&createRequire, "require", nil, "reception requirements (tags)")
	createCmd.Flags().StringVar(&createDescription, "description", "", "campfire description")
	createCmd.Flags().UintVar(&createThreshold, "threshold", 1, "signature threshold (1=any member, >1=FROST multi-party, Phase 2)")
	createCmd.Flags().StringVar(&createTransport, "transport", "filesystem", "transport type: filesystem, p2p-http, github")
	createCmd.Flags().StringVar(&createListen, "listen", "", "HTTP listen address for p2p-http transport (e.g. :9001)")
	createCmd.Flags().UintVar(&createParticipants, "participants", 0, "total number of DKG participants for threshold>1 (default: equals threshold)")
	// GitHub transport flags.
	createCmd.Flags().StringVar(&createGitHubRepo, "github-repo", "", "coordination repository for GitHub transport (owner/repo)")
	createCmd.Flags().StringVar(&createGitHubTokenEnv, "github-token-env", "", "name of env var containing GitHub token (default: GITHUB_TOKEN)")
	createCmd.Flags().StringVar(&createGitHubBaseURL, "github-base-url", "", "GitHub API base URL (for GitHub Enterprise; default: https://api.github.com)")
	rootCmd.AddCommand(createCmd)
}
