package cmd

import (
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/threshold"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	ghtr "github.com/campfire-net/campfire/pkg/transport/github"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
	"github.com/spf13/cobra"
)

var joinCmd = &cobra.Command{
	Use:   "join <campfire-id>",
	Short: "Join a campfire",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		joinVia, _ := cmd.Flags().GetString("via")
		joinListen, _ := cmd.Flags().GetString("listen")
		joinTLSCert, _ := cmd.Flags().GetString("tls-cert")
		joinTLSKey, _ := cmd.Flags().GetString("tls-key")
		joinGitHubRepo, _ := cmd.Flags().GetString("github-repo")
		joinGitHubTokenEnv, _ := cmd.Flags().GetString("github-token-env")
		joinGitHubBaseURL, _ := cmd.Flags().GetString("github-base-url")
		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		campfireID, err := resolveCampfireID(args[0], s)
		if err != nil {
			return err
		}

		// Check if already a member
		existingMembership, _ := s.GetMembership(campfireID)
		if existingMembership != nil {
			return fmt.Errorf("already a member of campfire %s", campfireID[:shortIDLen])
		}

		// Route based on --via flag (p2p-http), GitHub Issue URL, or filesystem (default).
		if joinVia != "" {
			return joinP2PHTTP(campfireID, agentID, s, joinVia, joinListen, joinTLSCert, joinTLSKey)
		}
		if strings.HasPrefix(campfireID, "https://github.com/") {
			return joinGitHub(campfireID, agentID, s, joinGitHubTokenEnv, joinGitHubBaseURL, joinGitHubRepo)
		}
		return joinFilesystem(campfireID, agentID, s)
	},
}

// resolveFSTransportDir returns the filesystem transport directory for campfireID.
// It checks the beacon (global and project) for a "dir" key in the transport config.
// If no beacon is found or it carries no dir, falls back to the default base dir.
func resolveFSTransportDir(campfireID string) string {
	for _, dir := range []string{BeaconDir(), projectBeaconDir()} {
		if dir == "" {
			continue
		}
		beacons, err := beacon.Scan(dir)
		if err != nil {
			continue
		}
		for _, b := range beacons {
			if b.CampfireIDHex() == campfireID {
				if d, ok := b.Transport.Config["dir"]; ok && d != "" {
					return d
				}
			}
		}
	}
	return filepath.Join(fs.DefaultBaseDir(), campfireID)
}

func joinFilesystem(campfireID string, agentID *identity.Identity, s store.Store) error {
	transportDir := resolveFSTransportDir(campfireID)
	tr := fs.ForDir(transportDir)

	// Read campfire state to check join protocol.
	state, err := tr.ReadState(campfireID)
	if err != nil {
		return fmt.Errorf("reading campfire state: %w", err)
	}

	// Enforce invite-only before admission attempt.
	// admitFSMemberIfNew handles open protocol; pre-admitted members bypass this check.
	existingMembers, err := tr.ListMembers(campfireID)
	if err != nil {
		return fmt.Errorf("listing members: %w", err)
	}
	alreadyOnDisk := false
	for _, m := range existingMembers {
		if fmt.Sprintf("%x", m.PublicKey) == agentID.PublicKeyHex() {
			alreadyOnDisk = true
			break
		}
	}
	if !alreadyOnDisk {
		switch state.JoinProtocol {
		case "open":
			// Immediately admitted via admitFSMemberIfNew below.
		case "invite-only":
			return fmt.Errorf("campfire %s is invite-only; ask a member to run 'cf admit %s %s'",
				campfireID[:shortIDLen], campfireID[:shortIDLen], agentID.PublicKeyHex())
		default:
			return fmt.Errorf("unknown join protocol: %s", state.JoinProtocol)
		}
	}

	now, role, _, err := admitFSMemberIfNew(tr, campfireID, agentID, state)
	if err != nil {
		return err
	}

	// Look up description from beacon (best-effort).
	description := lookupBeaconDescription(campfireID)

	// Record membership in local store.
	// Role is preserved from the admission record (workspace-4s4).
	m := store.Membership{
		CampfireID:   campfireID,
		TransportDir: tr.CampfireDir(campfireID),
		JoinProtocol: state.JoinProtocol,
		Role:         role,
		JoinedAt:     now,
		Description:  description,
	}
	if err := s.AddMembership(m); err != nil {
		return fmt.Errorf("recording membership: %w", err)
	}

	// Sync messages immediately so convention declarations are available
	// without requiring a separate cf read.
	syncCampfire(campfireID, &m, agentID, s)

	// Compare fingerprints against local policy (Trust v0.2 §5.3).
	report := compareJoinedCampfire(s, campfireID)

	if jsonOutput {
		out := map[string]interface{}{
			"campfire_id":       campfireID,
			"status":            "joined",
			"trust_status":      string(report.OverallStatus),
			"fingerprint_match": report.FingerprintMatch,
			"conventions":       report.Conventions,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Printf("Joined campfire %s\n", campfireID[:shortIDLen])
	printCompatibilityReport(report)
	return nil
}

func joinP2PHTTP(campfireID string, agentID *identity.Identity, s store.Store, via, listen, tlsCert, tlsKey string) error {
	if (tlsCert == "") != (tlsKey == "") {
		return fmt.Errorf("--tls-cert and --tls-key must both be provided or both omitted")
	}
	useTLS := tlsCert != ""

	// Resolve joiner's own endpoint if --listen is provided.
	myEndpoint := ""
	if listen != "" {
		myEndpoint = resolveEndpoint(listen, useTLS)
	}

	// Send join request to the via endpoint.
	result, err := cfhttp.Join(via, campfireID, agentID, myEndpoint, false)
	if err != nil {
		return fmt.Errorf("joining campfire via %s: %w", via, err)
	}

	// Persist campfire state locally.
	stateDir := filepath.Join(CFHome(), "campfires")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return fmt.Errorf("creating campfire state directory: %w", err)
	}

	cfState := campfire.CampfireState{
		PublicKey:             result.CampfirePubKey,
		PrivateKey:            result.CampfirePrivKey,
		JoinProtocol:          result.JoinProtocol,
		ReceptionRequirements: result.ReceptionRequirements,
		Threshold:             result.Threshold,
	}
	stateData, err := cfencoding.Marshal(cfState)
	if err != nil {
		return fmt.Errorf("encoding campfire state: %w", err)
	}
	stateFile := filepath.Join(stateDir, campfireID+".cbor")
	if err := os.WriteFile(stateFile, stateData, 0600); err != nil {
		return fmt.Errorf("writing campfire state: %w", err)
	}

	// Look up description from beacon (best-effort).
	p2pDescription := lookupBeaconDescription(campfireID)

	// Record membership in local store.
	p2pMembership := store.Membership{
		CampfireID:   campfireID,
		TransportDir: stateDir,
		JoinProtocol: result.JoinProtocol,
		Role:         campfire.RoleFull,
		JoinedAt:     store.NowNano(),
		Threshold:    result.Threshold,
		Description:  p2pDescription,
	}
	if err := s.AddMembership(p2pMembership); err != nil {
		return fmt.Errorf("recording membership: %w", err)
	}

	// Store peer endpoints received from admitting member (includes participant IDs).
	for _, peer := range result.Peers {
		if peer.PubKeyHex != "" && peer.Endpoint != "" {
			s.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
				CampfireID:    campfireID,
				MemberPubkey:  peer.PubKeyHex,
				Endpoint:      peer.Endpoint,
				ParticipantID: peer.ParticipantID,
			})
		}
	}

	// Store received DKG share (threshold>1).
	if len(result.ThresholdShareData) > 0 {
		// Decode the share to extract the participant ID.
		participantID, _, err := threshold.UnmarshalResult(result.ThresholdShareData)
		if err != nil {
			return fmt.Errorf("decoding threshold share: %w", err)
		}
		if err := s.UpsertThresholdShare(store.ThresholdShare{
			CampfireID:    campfireID,
			ParticipantID: participantID,
			SecretShare:   result.ThresholdShareData, // full MarshalResult output
			PublicData:    nil,
		}); err != nil {
			return fmt.Errorf("storing threshold share: %w", err)
		}
	}

	// If joiner has an endpoint, start the HTTP listener and notify peers.
	if myEndpoint != "" {
		tr := cfhttp.New(listen, s)
		if useTLS {
			tr.SetTLSConfig(&cfhttp.TLSConfig{CertFile: tlsCert, KeyFile: tlsKey})
		}
		tr.SetSelfInfo(agentID.PublicKeyHex(), myEndpoint)
		tr.SetKeyProvider(buildKeyProvider(CFHome()))
		tr.SetThresholdShareProvider(buildThresholdShareProvider(s))
		if err := tr.Start(); err != nil {
			return fmt.Errorf("starting HTTP listener on %s: %w", listen, err)
		}

		// Record self endpoint with participant ID.
		s.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
			CampfireID:    campfireID,
			MemberPubkey:  agentID.PublicKeyHex(),
			Endpoint:      myEndpoint,
			ParticipantID: result.MyParticipantID,
		})

		// Notify all known peers of our join.
		joinEvent := cfhttp.MembershipEvent{
			Event:    "join",
			Member:   agentID.PublicKeyHex(),
			Endpoint: myEndpoint,
		}
		for _, peer := range result.Peers {
			if peer.Endpoint != "" {
				cfhttp.NotifyMembership(peer.Endpoint, campfireID, joinEvent, agentID) //nolint:errcheck
			}
		}
	}

	// Sync messages immediately so convention declarations are available
	// without requiring a separate cf read.
	syncCampfire(campfireID, &p2pMembership, agentID, s)

	// Compare fingerprints against local policy (Trust v0.2 §5.3).
	p2pReport := compareJoinedCampfire(s, campfireID)

	if jsonOutput {
		out := map[string]interface{}{
			"campfire_id":       campfireID,
			"status":            "joined",
			"transport":         "p2p-http",
			"peers":             len(result.Peers),
			"has_priv_key":      len(result.CampfirePrivKey) > 0,
			"trust_status":      string(p2pReport.OverallStatus),
			"fingerprint_match": p2pReport.FingerprintMatch,
			"conventions":       p2pReport.Conventions,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Printf("Joined campfire %s\n", campfireID[:shortIDLen])
	printCompatibilityReport(p2pReport)
	return nil
}

// joinGitHub joins a campfire via the GitHub transport.
//
// The argument can be either:
//   - A GitHub Issue URL: https://github.com/org/repo/issues/N
//   - An Ed25519 public key hex (requires --github-repo to discover the beacon)
//
// For open campfires (threshold=1), the admitting member (typically the creator)
// will observe the campfire:join-request comment in their poll loop and post a
// campfire:key-delivery comment encrypting the campfire private key to the joiner's
// public key. This function polls until the key delivery comment arrives.
func joinGitHub(campfireArg string, agentID *identity.Identity, s store.Store, tokenEnv, baseURL, ghRepo string) error {
	token, err := resolveGitHubToken(tokenEnv, CFHome())
	if err != nil {
		return fmt.Errorf("resolving GitHub token: %w", err)
	}

	// Parse the campfire argument: GitHub Issue URL or hex pubkey.
	var repo string
	var issueNumber int
	var campfireID string

	if strings.HasPrefix(campfireArg, "https://github.com/") {
		// Parse GitHub Issue URL: https://github.com/org/repo/issues/N
		parsed, err := url.Parse(campfireArg)
		if err != nil {
			return fmt.Errorf("parsing GitHub Issue URL: %w", err)
		}
		parts := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
		if len(parts) < 4 || parts[2] != "issues" {
			return fmt.Errorf("invalid GitHub Issue URL: expected https://github.com/owner/repo/issues/N, got %s", campfireArg)
		}
		repo = parts[0] + "/" + parts[1]
		n, err := strconv.Atoi(parts[3])
		if err != nil {
			return fmt.Errorf("invalid issue number in URL: %w", err)
		}
		issueNumber = n

		// Fetch the issue to get the campfire ID from the beacon body.
		// We use DiscoverBeacons and find the one with the matching issue number.
		client := ghtr.NewClient(baseURL, token)
		beacons, err := ghtr.DiscoverBeacons(client, repo)
		if err != nil {
			return fmt.Errorf("discovering beacons from %s: %w", repo, err)
		}
		found := false
		for _, b := range beacons {
			if b.Transport.Config.IssueNumber == issueNumber && b.Transport.Config.Repo == repo {
				campfireID = b.CampfireID
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("no beacon found in %s for issue #%d (beacon may not have been published)", repo, issueNumber)
		}
	} else {
		// Ed25519 hex pubkey: use --github-repo to discover.
		campfireID = campfireArg
		if ghRepo == "" {
			return fmt.Errorf("--github-repo required when joining by campfire ID (not URL)")
		}
		repo = ghRepo
		client := ghtr.NewClient(baseURL, token)
		beacons, err := ghtr.DiscoverBeacons(client, repo)
		if err != nil {
			return fmt.Errorf("discovering beacons: %w", err)
		}
		found := false
		for _, b := range beacons {
			if b.CampfireID == campfireID {
				issueNumber = b.Transport.Config.IssueNumber
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("campfire %s not found in %s beacons", campfireID[:min(len(campfireID), 16)], repo)
		}
	}

	// Check if already a member.
	if existing, _ := s.GetMembership(campfireID); existing != nil {
		return fmt.Errorf("already a member of campfire %s", campfireID[:min(len(campfireID), 16)])
	}

	cfg := ghtr.Config{
		Repo:        repo,
		IssueNumber: issueNumber,
		Token:       token,
		BaseURL:     baseURL,
	}
	tr, err := ghtr.New(cfg, s)
	if err != nil {
		return fmt.Errorf("creating GitHub transport: %w", err)
	}
	tr.RegisterCampfire(campfireID, issueNumber)

	// Post a campfire:join-request signed message so the creator can observe it.
	joinReqMsg, err := message.NewMessage(
		agentID.PrivateKey,
		agentID.PublicKey,
		[]byte(fmt.Sprintf(`{"joiner":"%s"}`, agentID.PublicKeyHex())),
		[]string{"campfire:join-request"},
		nil,
	)
	if err != nil {
		return fmt.Errorf("creating join-request message: %w", err)
	}
	if err := tr.Send(campfireID, joinReqMsg); err != nil {
		return fmt.Errorf("posting join-request: %w", err)
	}

	// Poll for campfire:key-delivery comment addressed to us.
	// For open campfires in production, the creator's poll loop handles this.
	// In tests the test harness posts the key delivery directly.
	// Cap at 1000 comments (design doc constraint, §11 open question #6).
	campfirePrivKey, err := pollForKeyDelivery(tr, campfireID, agentID)
	if err != nil {
		return fmt.Errorf("waiting for key delivery: %w", err)
	}

	// Build the campfire public key from the private key (last 32 bytes of Ed25519 private key).
	campfirePubKey := make([]byte, 32)
	if len(campfirePrivKey) >= 64 {
		copy(campfirePubKey, campfirePrivKey[32:])
	}

	// Encode transport metadata into TransportDir.
	transportDir, err := encodeGitHubTransportDir(githubTransportMeta{
		Repo:        repo,
		IssueNumber: issueNumber,
		BaseURL:     baseURL,
	})
	if err != nil {
		return fmt.Errorf("encoding transport dir: %w", err)
	}

	// Look up description from beacon (best-effort).
	ghDescription := lookupBeaconDescription(campfireID)

	// Record membership in local store.
	ghMembership := store.Membership{
		CampfireID:   campfireID,
		TransportDir: transportDir,
		JoinProtocol: "open", // populated from beacon in production; simplified here
		Role:         campfire.RoleFull,
		JoinedAt:     store.NowNano(),
		Threshold:    1,
		Description:  ghDescription,
	}
	if err := s.AddMembership(ghMembership); err != nil {
		return fmt.Errorf("recording membership: %w", err)
	}

	// Sync messages immediately so convention declarations are available
	// without requiring a separate cf read.
	syncCampfire(campfireID, &ghMembership, agentID, s)

	// Compare fingerprints against local policy (Trust v0.2 §5.3).
	ghReport := compareJoinedCampfire(s, campfireID)

	if jsonOutput {
		out := map[string]interface{}{
			"campfire_id":       campfireID,
			"status":            "joined",
			"transport":         "github",
			"trust_status":      string(ghReport.OverallStatus),
			"fingerprint_match": ghReport.FingerprintMatch,
			"conventions":       ghReport.Conventions,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Printf("Joined campfire %s\n", campfireID[:min(len(campfireID), 16)])
	printCompatibilityReport(ghReport)
	return nil
}

// pollForKeyDelivery polls the GitHub Issue for a campfire:key-delivery comment
// addressed to us. Returns the decrypted campfire private key bytes.
// Cap at 1000 comments per the design doc.
//
// Security: only messages whose Sender public key matches the campfireID are
// accepted. campfireID is the hex-encoded Ed25519 public key of the campfire
// (and its creator). Any other sender is silently skipped to prevent an
// attacker with issue write access from injecting a malicious private key.
func pollForKeyDelivery(tr *ghtr.Transport, campfireID string, agentID *identity.Identity) ([]byte, error) {
	// Decode the campfire public key from the hex campfireID so we can compare
	// it against msg.Sender for each key-delivery candidate.
	campfirePubKeyBytes, err := hex.DecodeString(campfireID)
	if err != nil {
		return nil, fmt.Errorf("invalid campfire ID (not hex): %w", err)
	}

	const maxAttempts = 20
	for i := 0; i < maxAttempts; i++ {
		msgs, err := tr.Poll(campfireID)
		if err != nil {
			return nil, fmt.Errorf("poll attempt %d: %w", i+1, err)
		}
		for _, msg := range msgs {
			var tags []string
			for _, t := range msg.Tags {
				tags = append(tags, t)
			}
			isKeyDelivery := false
			for _, t := range tags {
				if t == "campfire:key-delivery" {
					isKeyDelivery = true
					break
				}
			}
			if !isKeyDelivery {
				continue
			}

			// Verify sender is the campfire creator. msg.Sender holds the raw
			// Ed25519 public key bytes of the message author. Only the holder of
			// the campfire private key (the creator) is authorised to deliver it.
			if subtle.ConstantTimeCompare(msg.Sender, campfirePubKeyBytes) != 1 {
				continue
			}

			// The payload is hex-encoded encrypted key material.
			ciphertext, err := hex.DecodeString(string(msg.Payload))
			if err != nil {
				continue
			}
			plaintext, err := identity.DecryptWithEd25519Key(agentID.PrivateKey, ciphertext)
			if err != nil {
				continue
			}
			// Validate the decrypted key is exactly 64 bytes (Ed25519 private key).
			// A valid Ed25519 private key is 64 bytes: 32-byte seed + 32-byte public key.
			// Reject malformed plaintext to prevent silent acceptance of truncated or
			// garbage keys that would produce an unusable campfire identity.
			if len(plaintext) != 64 {
				return nil, fmt.Errorf("delivered key has invalid length: got %d bytes, want 64", len(plaintext))
			}
			return plaintext, nil
		}
		// Key not yet delivered; wait a moment (tests do not reach this path).
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("key delivery not received after %d poll attempts", maxAttempts)
}


// lookupBeaconDescription scans global and project beacon directories for a
// beacon matching campfireID and returns its description. Returns "" on miss.
func lookupBeaconDescription(campfireID string) string {
	for _, dir := range []string{BeaconDir(), projectBeaconDir()} {
		if dir == "" {
			continue
		}
		beacons, err := beacon.Scan(dir)
		if err != nil {
			continue
		}
		for _, b := range beacons {
			if b.CampfireIDHex() == campfireID {
				return b.Description
			}
		}
	}
	return ""
}

// projectBeaconDir returns the .campfire/beacons dir for the current project, or "".
func projectBeaconDir() string {
	if _, projectDir, ok := ProjectRoot(); ok {
		return filepath.Join(projectDir, ".campfire", "beacons")
	}
	return ""
}

func init() {
	joinCmd.Flags().String("via", "", "peer HTTP endpoint to join through (enables p2p-http transport)")
	joinCmd.Flags().String("listen", "", "HTTP listen address for p2p-http transport (e.g. :9002)")
	joinCmd.Flags().String("tls-cert", "", "TLS certificate file (PEM); enables https:// endpoint advertisement")
	joinCmd.Flags().String("tls-key", "", "TLS private key file (PEM); must be paired with --tls-cert")
	// GitHub transport flags.
	joinCmd.Flags().String("github-repo", "", "coordination repository for GitHub beacon discovery (owner/repo)")
	joinCmd.Flags().String("github-token-env", "", "name of env var containing GitHub token (default: GITHUB_TOKEN)")
	joinCmd.Flags().String("github-base-url", "", "GitHub API base URL (for GitHub Enterprise; default: https://api.github.com)")
	rootCmd.AddCommand(joinCmd)
}

// campfireFromState reconstructs a Campfire for membership hash computation.
func campfireFromState(state *campfire.CampfireState, members []campfire.MemberRecord) *campfire.Campfire {
	return state.ToCampfire(members)
}
