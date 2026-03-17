package cmd

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	frostmessages "github.com/taurusgroup/frost-ed25519/pkg/messages"
)

var (
	sendTags        []string
	sendAntecedents []string
	sendFuture      bool
	sendFulfills    string
)

var sendCmd = &cobra.Command{
	Use:   "send <campfire-id> <message>",
	Short: "Send a message to a campfire",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		campfireID := args[0]
		payload := args[1]

		// Merge deprecated --antecedent alias into --reply-to.
		if legacyAnts, err := cmd.Flags().GetStringSlice("antecedent"); err == nil && len(legacyAnts) > 0 {
			sendAntecedents = append(sendAntecedents, legacyAnts...)
		}

		agentID, err := identity.Load(IdentityPath())
		if err != nil {
			return fmt.Errorf("loading identity: %w", err)
		}

		s, err := store.Open(store.StorePath(CFHome()))
		if err != nil {
			return fmt.Errorf("opening store: %w", err)
		}
		defer s.Close()

		m, err := s.GetMembership(campfireID)
		if err != nil {
			return fmt.Errorf("querying membership: %w", err)
		}
		if m == nil {
			return fmt.Errorf("not a member of campfire %s", campfireID[:12])
		}

		// Build tags
		tags := sendTags
		if sendFuture {
			tags = append(tags, "future")
		}
		if sendFulfills != "" {
			tags = append(tags, "fulfills")
		}

		// Build antecedents
		antecedents := sendAntecedents
		if sendFulfills != "" {
			antecedents = append(antecedents, sendFulfills)
		}

		// Route based on transport type.
		var msg *message.Message
		if isGitHubCampfire(m.TransportDir) {
			msg, err = sendGitHub(campfireID, payload, tags, antecedents, agentID, s, m)
		} else if isPeerHTTPCampfire(m.TransportDir, campfireID) {
			msg, err = sendP2PHTTP(campfireID, payload, tags, antecedents, agentID, s, m)
		} else {
			msg, err = sendFilesystem(campfireID, payload, tags, antecedents, agentID, m.TransportDir)
		}
		if err != nil {
			return err
		}

		if jsonOutput {
			out := map[string]interface{}{
				"id":          msg.ID,
				"campfire_id": campfireID,
				"sender":      agentID.PublicKeyHex(),
				"payload":     payload,
				"tags":        msg.Tags,
				"antecedents": msg.Antecedents,
				"timestamp":   msg.Timestamp,
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		fmt.Println(msg.ID)
		return nil
	},
}

// isPeerHTTPCampfire detects a p2p-http campfire by checking for the CBOR state file.
func isPeerHTTPCampfire(transportDir, campfireID string) bool {
	statePath := filepath.Join(transportDir, campfireID+".cbor")
	_, err := os.Stat(statePath)
	return err == nil
}

// sendFilesystem sends a message via the filesystem transport.
// transportDir is the campfire-specific directory from the membership record
// (e.g. /tmp/campfire/<campfire-id>). Falls back to fs.DefaultBaseDir() when empty.
func sendFilesystem(campfireID, payload string, tags, antecedents []string, agentID *identity.Identity, transportDir string) (*message.Message, error) {
	baseDir := fs.DefaultBaseDir()
	if transportDir != "" {
		baseDir = filepath.Dir(transportDir)
	}
	transport := fs.New(baseDir)

	// Verify sender is a member in the transport directory.
	members, err := transport.ListMembers(campfireID)
	if err != nil {
		return nil, fmt.Errorf("listing members: %w", err)
	}
	isMember := false
	for _, mem := range members {
		if fmt.Sprintf("%x", mem.PublicKey) == agentID.PublicKeyHex() {
			isMember = true
			break
		}
	}
	if !isMember {
		return nil, fmt.Errorf("not recognized as a member in the transport directory")
	}

	// Create and sign message.
	msg, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, []byte(payload), tags, antecedents)
	if err != nil {
		return nil, fmt.Errorf("creating message: %w", err)
	}

	// Read campfire state for provenance hop.
	state, err := transport.ReadState(campfireID)
	if err != nil {
		return nil, fmt.Errorf("reading campfire state: %w", err)
	}

	// Add provenance hop.
	cf := campfireFromState(state, members)
	if err := msg.AddHop(
		state.PrivateKey, state.PublicKey,
		cf.MembershipHash(), len(members),
		state.JoinProtocol, state.ReceptionRequirements,
	); err != nil {
		return nil, fmt.Errorf("adding provenance hop: %w", err)
	}

	// Write to transport.
	if err := transport.WriteMessage(campfireID, msg); err != nil {
		return nil, fmt.Errorf("writing message: %w", err)
	}

	return msg, nil
}

// sendGitHub sends a message via the GitHub Issues transport.
// The campfire state (repo + issue number) is read from m.TransportDir.
// The agent signs the message and POSTs it as a campfire-msg-v1: comment.
func sendGitHub(campfireID, payload string, tags, antecedents []string, agentID *identity.Identity, s *store.Store, m *store.Membership) (*message.Message, error) {
	meta, ok := parseGitHubTransportDir(m.TransportDir)
	if !ok {
		return nil, fmt.Errorf("invalid GitHub transport dir: %s", m.TransportDir)
	}

	token, err := resolveGitHubToken("", CFHome())
	if err != nil {
		return nil, fmt.Errorf("resolving GitHub token: %w", err)
	}

	cfg := ghtr.Config{
		Repo:        meta.Repo,
		IssueNumber: meta.IssueNumber,
		Token:       token,
		BaseURL:     meta.BaseURL,
	}
	tr, err := ghtr.New(cfg, s)
	if err != nil {
		return nil, fmt.Errorf("creating GitHub transport: %w", err)
	}
	tr.RegisterCampfire(campfireID, meta.IssueNumber)

	// Create and sign message with agent key.
	msg, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, []byte(payload), tags, antecedents)
	if err != nil {
		return nil, fmt.Errorf("creating message: %w", err)
	}

	// Send via GitHub transport (posts as Issue comment).
	if err := tr.Send(campfireID, msg); err != nil {
		return nil, fmt.Errorf("sending via GitHub transport: %w", err)
	}

	return msg, nil
}

// sendP2PHTTP sends a message via the P2P HTTP transport.
// For threshold=1: signs provenance hop with campfire key, fans out to peers.
// For threshold>1: runs FROST signing rounds with co-signers, then fans out.
func sendP2PHTTP(campfireID, payload string, tags, antecedents []string, agentID *identity.Identity, s *store.Store, m *store.Membership) (*message.Message, error) {
	// Load campfire state from local CBOR file.
	statePath := filepath.Join(m.TransportDir, campfireID+".cbor")
	stateData, err := os.ReadFile(statePath)
	if err != nil {
		return nil, fmt.Errorf("reading campfire state: %w", err)
	}
	var cfState campfire.CampfireState
	if err := cfencoding.Unmarshal(stateData, &cfState); err != nil {
		return nil, fmt.Errorf("decoding campfire state: %w", err)
	}

	// Create and sign message with agent key.
	msg, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, []byte(payload), tags, antecedents)
	if err != nil {
		return nil, fmt.Errorf("creating message: %w", err)
	}

	// Load peer endpoints.
	peers, err := s.ListPeerEndpoints(campfireID)
	if err != nil {
		return nil, fmt.Errorf("listing peer endpoints: %w", err)
	}

	// Collect peer endpoints (excluding self).
	var otherPeers []peerEntry
	for _, p := range peers {
		if p.MemberPubkey != agentID.PublicKeyHex() && p.Endpoint != "" {
			otherPeers = append(otherPeers, peerEntry{endpoint: p.Endpoint, participantID: p.ParticipantID})
		}
	}

	memberCount := len(peers)
	if memberCount == 0 {
		memberCount = 1
	}

	reqs := cfState.ReceptionRequirements
	if reqs == nil {
		reqs = []string{}
	}

	// Build and sign the provenance hop.
	if m.Threshold <= 1 {
		// threshold=1: sign with campfire private key directly.
		if err := msg.AddHop(
			ed25519.PrivateKey(cfState.PrivateKey),
			ed25519.PublicKey(cfState.PublicKey),
			cfState.PublicKey, // use pub key as simple membership hash
			memberCount,
			cfState.JoinProtocol,
			reqs,
		); err != nil {
			return nil, fmt.Errorf("adding provenance hop: %w", err)
		}
	} else {
		// threshold>1: FROST threshold signing.
		sig, hopTimestamp, err := thresholdSignHop(msg, &cfState, memberCount, campfireID, agentID, s, otherPeers, m.Threshold)
		if err != nil {
			return nil, fmt.Errorf("threshold signing: %w", err)
		}
		// Attach the threshold-signed provenance hop.
		hop := message.ProvenanceHop{
			CampfireID:            cfState.PublicKey,
			MembershipHash:        cfState.PublicKey, // use pub key as simple membership hash
			MemberCount:           memberCount,
			JoinProtocol:          cfState.JoinProtocol,
			ReceptionRequirements: reqs,
			Timestamp:             hopTimestamp,
			Signature:             sig,
		}
		msg.Provenance = append(msg.Provenance, hop)
	}

	// Extract peer endpoints for delivery.
	var peerEndpoints []string
	for _, p := range otherPeers {
		peerEndpoints = append(peerEndpoints, p.endpoint)
	}


	// Fan-out to all peers via HTTP.
	if len(peerEndpoints) > 0 {
		errs := cfhttp.DeliverToAll(peerEndpoints, campfireID, msg, agentID)
		for i, e := range errs {
			if e != nil {
				fmt.Fprintf(os.Stderr, "warning: delivery to peer %d failed: %v\n", i, e)
			}
		}
	}

	// Store message locally.
	tagsJSON, _ := json.Marshal(msg.Tags)
	anteJSON, _ := json.Marshal(msg.Antecedents)
	provJSON, _ := json.Marshal(msg.Provenance)
	s.AddMessage(store.MessageRecord{ //nolint:errcheck
		ID:          msg.ID,
		CampfireID:  campfireID,
		Sender:      agentID.PublicKeyHex(),
		Payload:     msg.Payload,
		Tags:        string(tagsJSON),
		Antecedents: string(anteJSON),
		Timestamp:   msg.Timestamp,
		Signature:   msg.Signature,
		Provenance:  string(provJSON),
		ReceivedAt:  store.NowNano(),
	})

	return msg, nil
}

// peerEntry holds endpoint info for a peer in the signing context.
type peerEntry struct {
	endpoint      string
	participantID uint32
}

// thresholdSignHop runs FROST signing rounds with co-signers to produce a threshold
// signature for the provenance hop. Returns the 64-byte Ed25519 signature and the hop timestamp.
func thresholdSignHop(msg *message.Message, cfState *campfire.CampfireState, memberCount int, campfireID string, agentID *identity.Identity, s *store.Store, peers []peerEntry, thresh uint) ([]byte, int64, error) {
	// Load this node's DKG share.
	share, err := s.GetThresholdShare(campfireID)
	if err != nil {
		return nil, 0, fmt.Errorf("loading threshold share: %w", err)
	}
	if share == nil {
		return nil, 0, fmt.Errorf("no threshold share — DKG not completed for campfire %s", campfireID[:minInt(12, len(campfireID))])
	}
	myParticipantID, myDKGResult, err := threshold.UnmarshalResult(share.SecretShare)
	if err != nil {
		return nil, 0, fmt.Errorf("deserializing threshold share: %w", err)
	}

	// Pick co-signers (peers with known participant IDs).
	needed := int(thresh) - 1
	var coSigners []peerEntry
	for _, p := range peers {
		if p.participantID > 0 {
			coSigners = append(coSigners, p)
		}
		if len(coSigners) >= needed {
			break
		}
	}
	if len(coSigners) < needed {
		return nil, 0, fmt.Errorf("need %d co-signers with known participant IDs, only %d available", needed, len(coSigners))
	}

	// Build the hop data to sign.
	reqs := cfState.ReceptionRequirements
	if reqs == nil {
		reqs = []string{}
	}
	hopTimestamp := time.Now().UnixNano()
	unsignedHop := message.ProvenanceHop{
		CampfireID:            cfState.PublicKey,
		MembershipHash:        cfState.PublicKey,
		MemberCount:           memberCount,
		JoinProtocol:          cfState.JoinProtocol,
		ReceptionRequirements: reqs,
		Timestamp:             hopTimestamp,
	}
	signInput := message.HopSignInput{
		MessageID:             msg.ID,
		CampfireID:            unsignedHop.CampfireID,
		MembershipHash:        unsignedHop.MembershipHash,
		MemberCount:           unsignedHop.MemberCount,
		JoinProtocol:          unsignedHop.JoinProtocol,
		ReceptionRequirements: unsignedHop.ReceptionRequirements,
		Timestamp:             unsignedHop.Timestamp,
	}
	signBytes, err := cfencoding.Marshal(signInput)
	if err != nil {
		return nil, 0, fmt.Errorf("computing hop sign bytes: %w", err)
	}

	// Build signer ID list: self + co-signers.
	signerIDs := []uint32{myParticipantID}
	for _, cs := range coSigners {
		signerIDs = append(signerIDs, cs.participantID)
	}

	sessionID := uuid.New().String()

	// Initialize our signing session.
	mySS, err := threshold.NewSigningSession(myDKGResult.SecretShare, myDKGResult.Public, signBytes, signerIDs)
	if err != nil {
		return nil, 0, fmt.Errorf("creating signing session: %w", err)
	}

	// Round 1: get our commitment messages.
	myRound1Msgs := mySS.Start()

	// Round 1: send to all co-signers and collect their commitments.
	var allPeerRound1Msgs []*frostmessages.Message
	for _, cs := range coSigners {
		peerMsgs, err := cfhttp.SendSignRound(cs.endpoint, campfireID, sessionID, 1, signerIDs, signBytes, myRound1Msgs, agentID)
		if err != nil {
			return nil, 0, fmt.Errorf("sign round 1 to %s: %w", cs.endpoint, err)
		}
		allPeerRound1Msgs = append(allPeerRound1Msgs, peerMsgs...)
	}

	// Deliver all peer round-1 messages to our session.
	for _, m := range allPeerRound1Msgs {
		mySS.Deliver(m) //nolint:errcheck
	}

	// Process to generate round-2 messages.
	myRound2Msgs := mySS.ProcessAll()

	// Round 2: send to all co-signers and collect their shares.
	var allPeerRound2Msgs []*frostmessages.Message
	for _, cs := range coSigners {
		peerMsgs, err := cfhttp.SendSignRound(cs.endpoint, campfireID, sessionID, 2, nil, nil, myRound2Msgs, agentID)
		if err != nil {
			return nil, 0, fmt.Errorf("sign round 2 to %s: %w", cs.endpoint, err)
		}
		allPeerRound2Msgs = append(allPeerRound2Msgs, peerMsgs...)
	}

	// Deliver all peer round-2 messages to our session.
	for _, peerMsg := range allPeerRound2Msgs {
		mySS.Deliver(peerMsg) //nolint:errcheck
	}
	mySS.ProcessAll()

	// Wait for signing to complete.
	select {
	case <-mySS.Done():
	case <-time.After(10 * time.Second):
		return nil, 0, fmt.Errorf("threshold signing timed out")
	}

	sig, err := mySS.Signature()
	if err != nil {
		return nil, 0, fmt.Errorf("extracting signature: %w", err)
	}
	return sig, hopTimestamp, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func init() {
	sendCmd.Flags().StringSliceVar(&sendTags, "tag", nil, "message tags")
	sendCmd.Flags().StringSliceVar(&sendAntecedents, "reply-to", nil, "message IDs this message replies to (causal dependencies)")
	// --antecedent is a hidden backward-compatibility alias for --reply-to.
	sendCmd.Flags().StringSlice("antecedent", nil, "alias for --reply-to (deprecated)")
	sendCmd.Flags().MarkHidden("antecedent") //nolint:errcheck
	sendCmd.Flags().BoolVar(&sendFuture, "future", false, "tag this message as a future")
	sendCmd.Flags().StringVar(&sendFulfills, "fulfills", "", "message ID this fulfills (adds 'fulfills' tag + reply-to in one step)")
	rootCmd.AddCommand(sendCmd)
}
