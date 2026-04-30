package cmd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/campfire-net/campfire/cf-protocol/admission"
	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/cf-protocol/campfire"
	cfencoding "github.com/campfire-net/campfire/cf-protocol/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/threshold"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
	cfhttp "github.com/campfire-net/campfire/cf-protocol/transport/http"
	"github.com/google/uuid"
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
		createNoConfig, _ := cmd.Flags().GetBool("no-config")
		createRelay, _ := cmd.Flags().GetString("relay")

		// Resolve relay URL: flag wins over config.
		if createRelay == "" {
			cfHome := CFHome()
			cwd, cwderr := os.Getwd()
			if cwderr != nil {
				cwd = cfHome
			}
			if cfg, _, _, err2 := protocol.LoadConfig(cfHome, cwd); err2 == nil && cfg != nil {
				createRelay = cfg.Transport.Relay
			}
		}

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

		// If --relay is set (or resolved from config), register on the relay.
		if createRelay != "" {
			return createAndRegisterOnRelay(cf, agentID, s, createDescription, createRelay)
		}

		switch createTransport {
		case "p2p-http":
			return createP2PHTTP(cf, agentID, s, createDescription, createListen, createTLSCert, createTLSKey, createParticipants)
		default:
			return createFilesystemWithNoConfig(cf, agentID, s, createDescription, createNoConfig)
		}
	},
}

// createAndRegisterOnRelay creates a campfire locally and registers it on the
// given HTTP relay. It outputs the beacon string from the relay response.
func createAndRegisterOnRelay(cf *campfire.Campfire, agentID *identity.Identity, s store.Store, description, relayURL string) error {
	beaconStr, relayEndpoint, inviteCode, err := registerOnRelay(cf, agentID, s, fs.DefaultBaseDir(), relayURL, description)
	if err != nil {
		return err
	}

	// Publish beacon locally so cf share works.
	b, err := beacon.New(
		cf.PublicKey,
		cf.PrivateKey,
		cf.JoinProtocol,
		cf.ReceptionRequirements,
		beacon.TransportConfig{
			Protocol: "p2p-http",
			Config:   map[string]string{"endpoint": relayEndpoint},
		},
		description,
	)
	if err != nil {
		return fmt.Errorf("creating beacon: %w", err)
	}
	if err := beacon.Publish(BeaconDir(), b); err != nil {
		return fmt.Errorf("publishing beacon: %w", err)
	}

	if jsonOutput {
		out := map[string]interface{}{
			"campfire_id":   cf.PublicKeyHex(),
			"join_protocol": cf.JoinProtocol,
			"transport":     "p2p-http",
			"relay":         relayEndpoint,
		}
		if beaconStr != "" {
			out["beacon"] = beaconStr
		}
		if inviteCode != "" {
			out["invite_code"] = inviteCode
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	if beaconStr != "" {
		fmt.Println(beaconStr)
	} else {
		fmt.Println(cf.PublicKeyHex())
	}
	fmt.Fprintf(os.Stderr, "Registered on relay: %s\n", relayEndpoint)
	return nil
}

func createFilesystemWithNoConfig(cf *campfire.Campfire, agentID *identity.Identity, s store.Store, description string, noConfig bool) error {
	return createFilesystemWithDescAndConfig(cf, agentID, s, fs.DefaultBaseDir(), description, noConfig)
}

// createFilesystemWithDesc is the testable core of createFilesystemWithNoConfig.
// It accepts an explicit baseDir (for tests) and description.
// In project mode (.campfire/root exists) it also:
//   - publishes a beacon to .campfire/beacons/ in the project dir
//   - sends a campfire:sub-created announcement to the root campfire
func createFilesystemWithDesc(cf *campfire.Campfire, agentID *identity.Identity, s store.Store, baseDir string, description string) error {
	return createFilesystemWithDescAndConfig(cf, agentID, s, baseDir, description, false)
}

// createFilesystemWithDescAndConfig is the core implementation for filesystem campfire creation.
// noConfig=true skips writing the beacon to .cf/config.toml.
func createFilesystemWithDescAndConfig(cf *campfire.Campfire, agentID *identity.Identity, s store.Store, baseDir string, description string, noConfig bool) error {
	// Set up filesystem transport.
	// Sub-campfires created in a project context still use the base-dir model
	// (they are not rooted in the project directory — only the swarm root is).
	transport := fs.New(baseDir)
	if err := transport.Init(cf); err != nil {
		return fmt.Errorf("initializing transport: %w", err)
	}

	// Write creator's member record via shared admission package.
	if _, err := admission.AdmitMember(context.Background(), admission.AdmitterDeps{
		FSTransport: transport,
		Store:       s,
	}, admission.AdmissionRequest{
		CampfireID:      cf.PublicKeyHex(),
		MemberPubKeyHex: agentID.PublicKeyHex(),
		Role:            store.PeerRoleCreator,
		JoinProtocol:    cf.JoinProtocol,
		TransportDir:    transport.CampfireDir(cf.PublicKeyHex()),
		TransportType:   "filesystem",
		Description:     description,
	}); err != nil {
		return fmt.Errorf("admitting creator: %w", err)
	}

	// Seed the campfire: post embedded promote declaration + any seed beacon declarations.
	// projectDir is discovered here so that project-local seeds take priority.
	seedProjectDir := ""
	if _, pd, ok := ProjectRoot(); ok {
		seedProjectDir = pd
	}
	seedCampfireFilesystem(cf.PublicKeyHex(), transport.CampfireDir(cf.PublicKeyHex()), agentID, cf, seedProjectDir, s)

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

	// Config-in-repo: write beacon to .cf/config.toml in the git root (best-effort).
	if !noConfig {
		if gitRoot, ok := detectGitRoot(); ok {
			beaconStr := beaconString(b)
			configPath := filepath.Join(gitRoot, ".cf", "config.toml")
			if err := appendAutoJoin(configPath, beaconStr); err != nil {
				// Non-fatal: warn and continue
				fmt.Fprintf(os.Stderr, "warning: could not write beacon to .cf/config.toml: %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "Wrote beacon to .cf/config.toml (behavior.auto_join). Share this repo — teammates auto-join on first cf command.\n")
			}
		} else {
			fmt.Fprintf(os.Stderr, "note: not in a git repo — pass --config-dir to write auto-join config\n")
		}
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
			announceClient := protocol.New(s, agentID)
			if _, serr := announceClient.Send(protocol.SendRequest{
				CampfireID: rootCampfireID,
				Payload:    []byte(announcePayload),
				Tags:       []string{campfire.TagSubCreated},
			}); serr != nil {
				fmt.Fprintf(os.Stderr, "warning: could not announce sub-campfire to root campfire: %v\n", serr)
			}
		}
	}

	// Auto-send identity:profile if the agent has a display name (best-effort).
	maybeSendProfileMessage(cf.PublicKeyHex(), agentID, s)

	// Generate a default invite code so the creator can share it immediately.
	inviteCode := uuid.New().String()
	if err := s.CreateInvite(store.InviteRecord{
		CampfireID: cf.PublicKeyHex(),
		InviteCode: inviteCode,
		CreatedBy:  agentID.PublicKeyHex(),
		CreatedAt:  store.NowNano(),
		Label:      "default",
	}); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not create invite code: %v\n", err)
		inviteCode = ""
	}

	if jsonOutput {
		out := map[string]interface{}{
			"campfire_id":            cf.PublicKeyHex(),
			"join_protocol":          cf.JoinProtocol,
			"reception_requirements": cf.ReceptionRequirements,
			"threshold":              cf.Threshold,
			"transport_dir":          transport.CampfireDir(cf.PublicKeyHex()),
			"invite_code":            inviteCode,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Println(cf.PublicKeyHex())
	if inviteCode != "" {
		fmt.Fprintf(os.Stderr, "invite code: %s\n", inviteCode)
	}
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

	// Record membership and self peer endpoint via shared admission package.
	selfParticipantID := uint32(0)
	if cf.Threshold > 1 {
		selfParticipantID = 1
	}
	if _, err := admission.AdmitMember(context.Background(), admission.AdmitterDeps{
		Store: s,
	}, admission.AdmissionRequest{
		CampfireID:      campfireID,
		MemberPubKeyHex: agentID.PublicKeyHex(),
		Endpoint:        endpoint,
		Role:            store.PeerRoleCreator,
		JoinProtocol:    cf.JoinProtocol,
		TransportDir:    stateDir,
		TransportType:   "p2p-http",
		ParticipantID:   selfParticipantID,
		Description:     description,
	}); err != nil {
		return fmt.Errorf("admitting creator: %w", err)
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
			Config:   map[string]string{"endpoint": endpoint},
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

// detectGitRoot returns the git root directory of the current working directory.
// Returns ("", false) if not in a git repository.
func detectGitRoot() (string, bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}
	// Walk up looking for a .git directory or file (worktrees use a file).
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", false
}

// beaconString encodes a Beacon as a portable "beacon:BASE64" string suitable
// for behavior.auto_join in config.toml so teammates can resolve the campfire.
func beaconString(b *beacon.Beacon) string {
	data, err := cfencoding.Marshal(b)
	if err != nil {
		// Fallback: campfire ID hex (join won't resolve transport but records intent).
		return fmt.Sprintf("%x", b.CampfireID)
	}
	return "beacon:" + base64.StdEncoding.EncodeToString(data)
}

// appendAutoJoin reads behavior.auto_join from the TOML config at configPath,
// appends beaconStr if not already present, and writes back.
// Creates the file and parent directories if needed.
func appendAutoJoin(configPath, beaconStr string) error {
	// Read existing list (empty if file absent or field missing).
	existing := readAutoJoinList(configPath)

	// Deduplicate: don't add if already present.
	for _, entry := range existing {
		if entry == beaconStr {
			return nil // already in list
		}
	}
	existing = append(existing, beaconStr)

	// Serialise as JSON array for configSetValue.
	data, err := json.Marshal(existing)
	if err != nil {
		return fmt.Errorf("serialising auto_join list: %w", err)
	}
	return configSetValue(configPath, "behavior.auto_join", string(data))
}

// readAutoJoinList returns the current behavior.auto_join list from a TOML file.
// Returns nil (empty) on any error.
func readAutoJoinList(configPath string) []string {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil
	}
	var raw struct {
		Behavior struct {
			AutoJoin []string `toml:"auto_join"`
		} `toml:"behavior"`
	}
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return nil
	}
	return raw.Behavior.AutoJoin
}

func init() {
	createCmd.Flags().String("protocol", "", "join protocol: open, invite-only (default: inherit parent campfire, or open if none)")
	createCmd.Flags().StringSlice("require", nil, "reception requirements (tags)")
	createCmd.Flags().String("description", "", "campfire description")
	createCmd.Flags().Uint("threshold", 1, "signature threshold (1=any member, >1=FROST multi-party, Phase 2)")
	createCmd.Flags().String("transport", "filesystem", "transport type: filesystem, p2p-http")
	createCmd.Flags().String("listen", "", "HTTP listen address for p2p-http transport (e.g. :9001)")
	createCmd.Flags().String("tls-cert", "", "TLS certificate file (PEM) for p2p-http transport; enables https:// endpoint")
	createCmd.Flags().String("tls-key", "", "TLS private key file (PEM) for p2p-http transport; must be paired with --tls-cert")
	createCmd.Flags().Uint("participants", 0, "total number of DKG participants for threshold>1 (default: equals threshold)")
	createCmd.Flags().Bool("no-config", false, "skip writing beacon to .cf/config.toml in git root")
	createCmd.Flags().String("relay", "", "register on relay: URL of HTTP relay (e.g. https://mcp.getcampfire.dev); overrides transport.relay config")
	rootCmd.AddCommand(createCmd)
}
