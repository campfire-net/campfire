package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/3dl-dev/campfire/pkg/campfire"
	cfencoding "github.com/3dl-dev/campfire/pkg/encoding"
	"github.com/3dl-dev/campfire/pkg/identity"
	"github.com/3dl-dev/campfire/pkg/message"
	"github.com/3dl-dev/campfire/pkg/store"
	"github.com/3dl-dev/campfire/pkg/threshold"
	"github.com/3dl-dev/campfire/pkg/transport/fs"
	cfhttp "github.com/3dl-dev/campfire/pkg/transport/http"
	"github.com/spf13/cobra"
)

var (
	joinVia      string
	joinListen   string
)

var joinCmd = &cobra.Command{
	Use:   "join <campfire-id>",
	Short: "Join a campfire",
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

		// Check if already a member
		existingMembership, _ := s.GetMembership(campfireID)
		if existingMembership != nil {
			return fmt.Errorf("already a member of campfire %s", campfireID[:12])
		}

		// Route based on --via flag (p2p-http) or filesystem (default)
		if joinVia != "" {
			return joinP2PHTTP(campfireID, agentID, s)
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

func init() {
	joinCmd.Flags().StringVar(&joinVia, "via", "", "peer HTTP endpoint to join through (enables p2p-http transport)")
	joinCmd.Flags().StringVar(&joinListen, "listen", "", "HTTP listen address for p2p-http transport (e.g. :9002)")
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
