package cmd

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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

var (
	joinVia             string
	joinListen          string
	joinGitHubRepo      string
	joinGitHubTokenEnv  string
	joinGitHubBaseURL   string
)

var joinCmd = &cobra.Command{
	Use:   "join <campfire-id>",
	Short: "Join a campfire",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		agentID, err := identity.Load(IdentityPath())
		if err != nil {
			return fmt.Errorf("loading identity (run 'cf init' first): %w", err)
		}

		s, err := store.Open(store.StorePath(CFHome()))
		if err != nil {
			return fmt.Errorf("opening store: %w", err)
		}
		defer s.Close()

		campfireID, err := resolveCampfireID(args[0], s)
		if err != nil {
			return err
		}

		// Check if already a member
		existingMembership, _ := s.GetMembership(campfireID)
		if existingMembership != nil {
			return fmt.Errorf("already a member of campfire %s", campfireID[:12])
		}

		// Route based on --via flag (p2p-http), GitHub Issue URL, or filesystem (default).
		if joinVia != "" {
			return joinP2PHTTP(campfireID, agentID, s)
		}
		if strings.HasPrefix(campfireID, "https://github.com/") {
			return joinGitHub(campfireID, agentID, s)
		}
		return joinFilesystem(campfireID, agentID, s)
	},
}

func joinFilesystem(campfireID string, agentID *identity.Identity, s *store.Store) error {
	transport := fs.New(fs.DefaultBaseDir())

	// Read campfire state to check join protocol
	state, err := transport.ReadState(campfireID)
	if err != nil {
		return fmt.Errorf("reading campfire state: %w", err)
	}

	// Check if already a member
	members, err := transport.ListMembers(campfireID)
	if err != nil {
		return fmt.Errorf("listing members: %w", err)
	}
	alreadyOnDisk := false
	var existingJoinedAt int64
	for _, m := range members {
		if fmt.Sprintf("%x", m.PublicKey) == agentID.PublicKeyHex() {
			alreadyOnDisk = true
			existingJoinedAt = m.JoinedAt
			break
		}
	}

	now := time.Now().UnixNano()

	if alreadyOnDisk {
		// Pre-admitted (e.g., via DM or cf admit). Just register locally.
		now = existingJoinedAt
	} else {
		// Need to be admitted first
		switch state.JoinProtocol {
		case "open":
			// Immediately admitted
		case "invite-only":
			return fmt.Errorf("campfire %s is invite-only; ask a member to run 'cf admit %s %s'",
				campfireID[:12], campfireID[:12], agentID.PublicKeyHex())
		default:
			return fmt.Errorf("unknown join protocol: %s", state.JoinProtocol)
		}

		// Write member record to transport directory
		if err := transport.WriteMember(campfireID, campfire.MemberRecord{
			PublicKey: agentID.PublicKey,
			JoinedAt:  now,
		}); err != nil {
			return fmt.Errorf("writing member record: %w", err)
		}
	}

	// Write campfire:member-joined system message (only if newly admitted)
	if !alreadyOnDisk {
		sysMsg, err := message.NewMessage(
			state.PrivateKey, state.PublicKey,
			[]byte(fmt.Sprintf(`{"member":"%s","joined_at":%d}`, agentID.PublicKeyHex(), now)),
			[]string{"campfire:member-joined"},
			nil,
		)
		if err != nil {
			return fmt.Errorf("creating system message: %w", err)
		}

		updatedMembers, _ := transport.ListMembers(campfireID)
		cf := campfireFromState(state, updatedMembers)
		if err := sysMsg.AddHop(
			state.PrivateKey, state.PublicKey,
			cf.MembershipHash(), len(updatedMembers),
			state.JoinProtocol, state.ReceptionRequirements,
		); err != nil {
			return fmt.Errorf("adding provenance hop: %w", err)
		}

		if err := transport.WriteMessage(campfireID, sysMsg); err != nil {
			return fmt.Errorf("writing system message: %w", err)
		}
	}

	// Record membership in local store
	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: transport.CampfireDir(campfireID),
		JoinProtocol: state.JoinProtocol,
		Role:         "member",
		JoinedAt:     now,
	}); err != nil {
		return fmt.Errorf("recording membership: %w", err)
	}

	if jsonOutput {
		out := map[string]string{
			"campfire_id": campfireID,
			"status":      "joined",
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Printf("Joined campfire %s\n", campfireID[:12])
	return nil
}

func joinP2PHTTP(campfireID string, agentID *identity.Identity, s *store.Store) error {
	// Resolve joiner's own endpoint if --listen is provided.
	myEndpoint := ""
	if joinListen != "" {
		myEndpoint = resolveEndpoint(joinListen)
	}

	// Send join request to the via endpoint.
	result, err := cfhttp.Join(joinVia, campfireID, agentID, myEndpoint)
	if err != nil {
		return fmt.Errorf("joining campfire via %s: %w", joinVia, err)
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

	// Record membership in local store.
	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: stateDir,
		JoinProtocol: result.JoinProtocol,
		Role:         "member",
		JoinedAt:     store.NowNano(),
		Threshold:    result.Threshold,
	}); err != nil {
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
		tr := cfhttp.New(joinListen, s)
		tr.SetSelfInfo(agentID.PublicKeyHex(), myEndpoint)
		tr.SetKeyProvider(buildKeyProvider(CFHome()))
		tr.SetThresholdShareProvider(buildThresholdShareProvider(s))
		if err := tr.Start(); err != nil {
			return fmt.Errorf("starting HTTP listener on %s: %w", joinListen, err)
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

	if jsonOutput {
		out := map[string]interface{}{
			"campfire_id":  campfireID,
			"status":       "joined",
			"transport":    "p2p-http",
			"peers":        len(result.Peers),
			"has_priv_key": len(result.CampfirePrivKey) > 0,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Printf("Joined campfire %s\n", campfireID[:12])
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
func joinGitHub(campfireArg string, agentID *identity.Identity, s *store.Store) error {
	token, err := resolveGitHubToken(joinGitHubTokenEnv, CFHome())
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
		client := ghtr.NewClient(joinGitHubBaseURL, token)
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
		if joinGitHubRepo == "" {
			return fmt.Errorf("--github-repo required when joining by campfire ID (not URL)")
		}
		repo = joinGitHubRepo
		client := ghtr.NewClient(joinGitHubBaseURL, token)
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
			return fmt.Errorf("campfire %s not found in %s beacons", campfireID[:min16(campfireID)], repo)
		}
	}

	// Check if already a member.
	if existing, _ := s.GetMembership(campfireID); existing != nil {
		return fmt.Errorf("already a member of campfire %s", campfireID[:min16(campfireID)])
	}

	cfg := ghtr.Config{
		Repo:        repo,
		IssueNumber: issueNumber,
		Token:       token,
		BaseURL:     joinGitHubBaseURL,
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
		BaseURL:     joinGitHubBaseURL,
	})
	if err != nil {
		return fmt.Errorf("encoding transport dir: %w", err)
	}

	// Record membership in local store.
	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: transportDir,
		JoinProtocol: "open", // populated from beacon in production; simplified here
		Role:         "member",
		JoinedAt:     store.NowNano(),
		Threshold:    1,
	}); err != nil {
		return fmt.Errorf("recording membership: %w", err)
	}

	if jsonOutput {
		out := map[string]string{
			"campfire_id": campfireID,
			"status":      "joined",
			"transport":   "github",
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Printf("Joined campfire %s\n", campfireID[:min16(campfireID)])
	return nil
}

// pollForKeyDelivery polls the GitHub Issue for a campfire:key-delivery comment
// addressed to us. Returns the decrypted campfire private key bytes.
// Cap at 1000 comments per the design doc.
func pollForKeyDelivery(tr *ghtr.Transport, campfireID string, agentID *identity.Identity) ([]byte, error) {
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
			// The payload is hex-encoded encrypted key material.
			ciphertext, err := hex.DecodeString(string(msg.Payload))
			if err != nil {
				continue
			}
			plaintext, err := identity.DecryptWithEd25519Key(agentID.PrivateKey, ciphertext)
			if err != nil {
				continue
			}
			return plaintext, nil
		}
		// Key not yet delivered; wait a moment (tests do not reach this path).
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("key delivery not received after %d poll attempts", maxAttempts)
}

func min16(s string) int {
	if len(s) < 16 {
		return len(s)
	}
	return 16
}

func init() {
	joinCmd.Flags().StringVar(&joinVia, "via", "", "peer HTTP endpoint to join through (enables p2p-http transport)")
	joinCmd.Flags().StringVar(&joinListen, "listen", "", "HTTP listen address for p2p-http transport (e.g. :9002)")
	// GitHub transport flags.
	joinCmd.Flags().StringVar(&joinGitHubRepo, "github-repo", "", "coordination repository for GitHub beacon discovery (owner/repo)")
	joinCmd.Flags().StringVar(&joinGitHubTokenEnv, "github-token-env", "", "name of env var containing GitHub token (default: GITHUB_TOKEN)")
	joinCmd.Flags().StringVar(&joinGitHubBaseURL, "github-base-url", "", "GitHub API base URL (for GitHub Enterprise; default: https://api.github.com)")
	rootCmd.AddCommand(joinCmd)
}

// campfireFromState reconstructs a Campfire for membership hash computation.
func campfireFromState(state *campfire.CampfireState, members []campfire.MemberRecord) *campfire.Campfire {
	cf := &campfire.Campfire{
		JoinProtocol:          state.JoinProtocol,
		ReceptionRequirements: state.ReceptionRequirements,
		CreatedAt:             state.CreatedAt,
	}
	for _, m := range members {
		cf.Members = append(cf.Members, campfire.Member{
			PublicKey: m.PublicKey,
			JoinedAt:  m.JoinedAt,
		})
	}
	return cf
}
