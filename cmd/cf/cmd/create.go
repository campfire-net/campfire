package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/threshold"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
	ghtr "github.com/campfire-net/campfire/pkg/transport/github"
	"github.com/spf13/cobra"
)

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new campfire",
	RunE: func(cmd *cobra.Command, args []string) error {
		createProtocol, _ := cmd.Flags().GetString("protocol")
		createRequire, _ := cmd.Flags().GetStringSlice("require")
		createDescription, _ := cmd.Flags().GetString("description")
		createThreshold, _ := cmd.Flags().GetUint("threshold")
		createTransport, _ := cmd.Flags().GetString("transport")
		createListen, _ := cmd.Flags().GetString("listen")
		createTLSCert, _ := cmd.Flags().GetString("tls-cert")
		createTLSKey, _ := cmd.Flags().GetString("tls-key")
		createParticipants, _ := cmd.Flags().GetUint("participants")
		createGitHubRepo, _ := cmd.Flags().GetString("github-repo")
		createGitHubTokenEnv, _ := cmd.Flags().GetString("github-token-env")
		createGitHubBaseURL, _ := cmd.Flags().GetString("github-base-url")

		// Load agent identity
		agentID, err := identity.Load(IdentityPath())
		if err != nil {
			return fmt.Errorf("loading identity (run 'cf init' first): %w", err)
		}

		// Resolve join protocol: if not explicitly set (empty string), inherit from
		// the parent campfire. If there is no parent in scope, default to "open".
		if createProtocol == "" {
			createProtocol = "open" // fallback if no parent
			if rootCampfireID, _, ok := ProjectRoot(); ok {
				ps, serr := openStore()
				if serr == nil {
					if m, merr := ps.GetMembership(rootCampfireID); merr == nil && m != nil && m.JoinProtocol != "" {
						createProtocol = m.JoinProtocol
					}
					ps.Close()
				}
			}
		}

		// Create campfire
		cf, err := campfire.New(createProtocol, createRequire, createThreshold)
		if err != nil {
			return fmt.Errorf("creating campfire: %w", err)
		}

		// Add creator as first member
		cf.AddMember(agentID.PublicKey)

		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()

		switch createTransport {
		case "github":
			return createGitHub(cf, agentID, s, createDescription, createGitHubRepo, createGitHubTokenEnv, createGitHubBaseURL)
		case "p2p-http":
			return createP2PHTTP(cf, agentID, s, createDescription, createListen, createTLSCert, createTLSKey, createParticipants)
		default:
			return createFilesystem(cf, agentID, s, createDescription)
		}
	},
}

func createFilesystem(cf *campfire.Campfire, agentID *identity.Identity, s store.Store, description string) error {
	return createFilesystemWithDesc(cf, agentID, s, fs.DefaultBaseDir(), description)
}

// createFilesystemWithDesc is the testable core of createFilesystem.
// It accepts an explicit baseDir (for tests) and description.
// In project mode (.campfire/root exists) it also:
//   - publishes a beacon to .campfire/beacons/ in the project dir
//   - sends a campfire:sub-created announcement to the root campfire
func createFilesystemWithDesc(cf *campfire.Campfire, agentID *identity.Identity, s store.Store, baseDir string, description string) error {
	// Set up filesystem transport
	transport := fs.New(baseDir)
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

	// Seed the campfire: post embedded promote declaration + any seed beacon declarations.
	// projectDir is discovered here so that project-local seeds take priority.
	seedProjectDir := ""
	if _, pd, ok := ProjectRoot(); ok {
		seedProjectDir = pd
	}
	seedCampfireFilesystem(cf.PublicKeyHex(), transport.CampfireDir(cf.PublicKeyHex()), agentID, cf, seedProjectDir)

	// Build beacon
	b, err := beacon.New(
		cf.PublicKey,
		cf.PrivateKey,
		cf.JoinProtocol,
		cf.ReceptionRequirements,
		beacon.TransportConfig{
			Protocol: "filesystem",
			Config:   map[string]string{"dir": transport.CampfireDir(cf.PublicKeyHex())},
		},
		description,
	)
	if err != nil {
		return fmt.Errorf("creating beacon: %w", err)
	}

	// Publish beacon to standard beacon dir
	beaconDir := BeaconDir()
	if err := beacon.Publish(beaconDir, b); err != nil {
		return fmt.Errorf("publishing beacon: %w", err)
	}

	// Project mode: also publish beacon to .campfire/beacons/ and announce to root campfire
	if rootCampfireID, projectDir, ok := ProjectRoot(); ok {
		// Publish beacon to .campfire/beacons/ in project dir
		projectBeaconsDir := filepath.Join(projectDir, ".campfire", "beacons")
		if err := beacon.Publish(projectBeaconsDir, b); err != nil {
			// Non-fatal: warn and continue
			fmt.Fprintf(os.Stderr, "warning: could not publish beacon to project beacons dir: %v\n", err)
		}

		// Send announcement to root campfire (best-effort, non-fatal)
		subShortID := cf.PublicKeyHex()
		if len(subShortID) > shortIDLen {
			subShortID = subShortID[:shortIDLen]
		}
		announcePayload := fmt.Sprintf("sub-campfire created: %s (%s)", description, subShortID)
		rootMembership, merr := s.GetMembership(rootCampfireID)
		if merr == nil && rootMembership != nil {
			if _, serr := sendFilesystem(rootCampfireID, announcePayload, []string{"campfire:sub-created"}, nil, "", agentID, rootMembership.TransportDir); serr != nil {
				fmt.Fprintf(os.Stderr, "warning: could not announce sub-campfire to root campfire: %v\n", serr)
			}
		}
	}

	// Record membership in local store
	if err := s.AddMembership(store.Membership{
		CampfireID:   cf.PublicKeyHex(),
		TransportDir: transport.CampfireDir(cf.PublicKeyHex()),
		JoinProtocol: cf.JoinProtocol,
		Role:         store.PeerRoleCreator,
		JoinedAt:     store.NowNano(),
		Threshold:    cf.Threshold,
		Description:  description,
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

func createP2PHTTP(cf *campfire.Campfire, agentID *identity.Identity, s store.Store, description, listenAddr, tlsCert, tlsKey string, participants uint) error {
	if listenAddr == "" {
		return fmt.Errorf("--listen is required for p2p-http transport (e.g. --listen :9001)")
	}
	if (tlsCert == "") != (tlsKey == "") {
		return fmt.Errorf("--tls-cert and --tls-key must both be provided or both omitted")
	}
	useTLS := tlsCert != ""

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
		n := participants
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
	endpoint := resolveEndpoint(listenAddr, useTLS)

	// Record membership in local store.
	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: stateDir, // reuse field to store state directory
		JoinProtocol: cf.JoinProtocol,
		Role:         store.PeerRoleCreator,
		JoinedAt:     store.NowNano(),
		Threshold:    cf.Threshold,
		Description:  description,
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
	if useTLS {
		tr.SetTLSConfig(&cfhttp.TLSConfig{CertFile: tlsCert, KeyFile: tlsKey})
	}
	tr.SetSelfInfo(agentID.PublicKeyHex(), endpoint)
	tr.SetKeyProvider(buildKeyProvider(CFHome()))
	tr.SetThresholdShareProvider(buildThresholdShareProvider(s))
	if err := tr.Start(); err != nil {
		return fmt.Errorf("starting HTTP listener on %s: %w", listenAddr, err)
	}

	// Publish beacon with p2p-http transport config.
	beaconDir := BeaconDir()
	b, err := beacon.New(
		cf.PublicKey,
		cf.PrivateKey,
		cf.JoinProtocol,
		cf.ReceptionRequirements,
		beacon.TransportConfig{
			Protocol: "p2p-http",
			Config:   map[string]string{"endpoints": endpoint},
		},
		description,
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
func createGitHub(cf *campfire.Campfire, agentID *identity.Identity, s store.Store, description, ghRepo, tokenEnv, baseURL string) error {
	if ghRepo == "" {
		return fmt.Errorf("--github-repo is required for GitHub transport (e.g. org/campfire-relay)")
	}

	token, err := resolveGitHubToken(tokenEnv, CFHome())
	if err != nil {
		return fmt.Errorf("resolving GitHub token: %w", err)
	}

	cfg := ghtr.Config{
		Repo:    ghRepo,
		Token:   token,
		BaseURL: baseURL,
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
				Repo:        ghRepo,
				IssueNumber: issueNumber,
				IssueURL:    fmt.Sprintf("https://github.com/%s/issues/%d", ghRepo, issueNumber),
			},
		},
		Description: description,
	}
	// Sign the beacon with the campfire's private key.
	sig, err := ghtr.SignBeacon(b, cf.PrivateKey)
	if err != nil {
		return fmt.Errorf("signing beacon: %w", err)
	}
	b.Signature = sig

	client := ghtr.NewClient(baseURL, token)
	if err := ghtr.PublishBeacon(client, ghRepo, b); err != nil {
		// Non-fatal: may lack Contents write permission. Warn and continue.
		fmt.Fprintf(os.Stderr, "warning: could not publish beacon to repo (Contents write required): %v\n", err)
	}

	// Encode transport metadata into TransportDir.
	transportDir, err := encodeGitHubTransportDir(githubTransportMeta{
		Repo:        ghRepo,
		IssueNumber: issueNumber,
		BaseURL:     baseURL,
	})
	if err != nil {
		return fmt.Errorf("encoding transport dir: %w", err)
	}

	// Record membership in local store.
	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: transportDir,
		JoinProtocol: cf.JoinProtocol,
		Role:         store.PeerRoleCreator,
		JoinedAt:     store.NowNano(),
		Threshold:    cf.Threshold,
		Description:  description,
	}); err != nil {
		return fmt.Errorf("recording membership: %w", err)
	}

	if jsonOutput {
		out := map[string]interface{}{
			"campfire_id":   campfireID,
			"join_protocol": cf.JoinProtocol,
			"transport":     "github",
			"repo":          ghRepo,
			"issue_number":  issueNumber,
			"issue_url":     fmt.Sprintf("https://github.com/%s/issues/%d", ghRepo, issueNumber),
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Println(campfireID)
	fmt.Fprintf(os.Stderr, "GitHub Issue: https://github.com/%s/issues/%d\n", ghRepo, issueNumber)
	return nil
}

// resolveEndpoint turns a listen address like ":9001" into an HTTP or HTTPS URL.
// When useTLS is true, the scheme is "https"; otherwise "http".
func resolveEndpoint(listenAddr string, useTLS bool) string {
	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	if len(listenAddr) > 0 && listenAddr[0] == ':' {
		return scheme + "://localhost" + listenAddr
	}
	return scheme + "://" + listenAddr
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
func buildThresholdShareProvider(s store.Store) cfhttp.ThresholdShareProvider {
	return func(campfireID string) (uint32, []byte, error) {
		share, err := s.GetThresholdShare(campfireID)
		if err != nil {
			return 0, nil, fmt.Errorf("querying threshold share: %w", err)
		}
		if share == nil {
			shortID := campfireID
		if len(shortID) > shortIDLen {
			shortID = shortID[:shortIDLen]
		}
		return 0, nil, fmt.Errorf("no threshold share found for campfire %s", shortID)
		}
		return share.ParticipantID, share.SecretShare, nil
	}
}


func init() {
	createCmd.Flags().String("protocol", "", "join protocol: open, invite-only (default: inherit parent campfire, or open if none)")
	createCmd.Flags().StringSlice("require", nil, "reception requirements (tags)")
	createCmd.Flags().String("description", "", "campfire description")
	createCmd.Flags().Uint("threshold", 1, "signature threshold (1=any member, >1=FROST multi-party, Phase 2)")
	createCmd.Flags().String("transport", "filesystem", "transport type: filesystem, p2p-http, github")
	createCmd.Flags().String("listen", "", "HTTP listen address for p2p-http transport (e.g. :9001)")
	createCmd.Flags().String("tls-cert", "", "TLS certificate file (PEM) for p2p-http transport; enables https:// endpoint")
	createCmd.Flags().String("tls-key", "", "TLS private key file (PEM) for p2p-http transport; must be paired with --tls-cert")
	createCmd.Flags().Uint("participants", 0, "total number of DKG participants for threshold>1 (default: equals threshold)")
	// GitHub transport flags.
	createCmd.Flags().String("github-repo", "", "coordination repository for GitHub transport (owner/repo)")
	createCmd.Flags().String("github-token-env", "", "name of env var containing GitHub token (default: GITHUB_TOKEN)")
	createCmd.Flags().String("github-base-url", "", "GitHub API base URL (for GitHub Enterprise; default: https://api.github.com)")
	rootCmd.AddCommand(createCmd)
}
