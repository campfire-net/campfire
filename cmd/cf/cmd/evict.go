package cmd

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/threshold"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var (
	evictReason    string
	evictListen    string
	evictTLSCert   string
	evictTLSKey    string
)

var evictCmd = &cobra.Command{
	Use:   "evict <campfire-id> <member-pubkey-hex>",
	Short: "Evict a member from a campfire (creator only). Always rekeys the campfire.",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		evictedPubkeyHex := args[1]

		agentID, err := identity.Load(IdentityPath())
		if err != nil {
			return fmt.Errorf("loading identity: %w", err)
		}

		s, err := store.Open(store.StorePath(CFHome()))
		if err != nil {
			return fmt.Errorf("opening store: %w", err)
		}
		defer s.Close()

		oldCampfireID, err := resolveCampfireID(args[0], s)
		if err != nil {
			return err
		}

		// Verify caller is a member and has creator role.
		m, err := s.GetMembership(oldCampfireID)
		if err != nil {
			return fmt.Errorf("querying membership: %w", err)
		}
		if m == nil {
			return fmt.Errorf("not a member of campfire %s", oldCampfireID[:12])
		}
		if m.Role != "creator" {
			return fmt.Errorf("only the creator can evict members (your role: %s)", m.Role)
		}

		// Load old campfire state.
		stateDir := m.TransportDir
		oldStateFile := filepath.Join(stateDir, oldCampfireID+".cbor")
		oldStateData, err := os.ReadFile(oldStateFile)
		if err != nil {
			return fmt.Errorf("reading campfire state: %w", err)
		}
		var oldCFState campfire.CampfireState
		if err := cfencoding.Unmarshal(oldStateData, &oldCFState); err != nil {
			return fmt.Errorf("decoding campfire state: %w", err)
		}

		// Validate evicted member pubkey.
		evictedPubKeyBytes, err := hex.DecodeString(evictedPubkeyHex)
		if err != nil {
			return fmt.Errorf("invalid member pubkey hex: %w", err)
		}
		if len(evictedPubKeyBytes) != ed25519.PublicKeySize {
			return fmt.Errorf("invalid member pubkey length: %d", len(evictedPubKeyBytes))
		}

		// Load peer endpoints.
		peers, err := s.ListPeerEndpoints(oldCampfireID)
		if err != nil {
			return fmt.Errorf("listing peer endpoints: %w", err)
		}

		// Separate remaining peers from evicted peer.
		// A member may have joined without an HTTP endpoint (no listener); that's fine —
		// we simply won't be able to notify them, but eviction still proceeds.
		var remainingPeers []store.PeerEndpoint
		for _, p := range peers {
			if p.MemberPubkey != evictedPubkeyHex {
				remainingPeers = append(remainingPeers, p)
			}
		}

		reason := evictReason
		if reason == "" {
			reason = "eviction"
		}

		// Generate new campfire identity for threshold=1 and as placeholder for threshold>1.
		newCFIdentity, err := identity.Generate()
		if err != nil {
			return fmt.Errorf("generating new campfire identity: %w", err)
		}
		newCampfireID := newCFIdentity.PublicKeyHex()

		// Route eviction by threshold.
		var actualNewCampfireID string
		if m.Threshold <= 1 {
			actualNewCampfireID = newCampfireID
			if err := evictThreshold1(
				agentID, s, stateDir,
				oldCampfireID, newCampfireID,
				&oldCFState, newCFIdentity,
				evictedPubkeyHex, remainingPeers,
				reason,
			); err != nil {
				return err
			}
		} else {
			var err error
			actualNewCampfireID, err = evictThresholdN(
				agentID, s, stateDir,
				oldCampfireID,
				&oldCFState,
				evictedPubkeyHex, remainingPeers,
				reason,
				m.Threshold,
			)
			if err != nil {
				return err
			}
		}

		if jsonOutput {
			out := map[string]string{
				"old_campfire_id": oldCampfireID,
				"new_campfire_id": actualNewCampfireID,
				"evicted_member":  evictedPubkeyHex,
				"reason":          reason,
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		fmt.Printf("Evicted %s from campfire %s\n", evictedPubkeyHex[:16], oldCampfireID[:12])
		fmt.Printf("New campfire ID: %s\n", actualNewCampfireID[:12])
		return nil
	},
}

// evictThreshold1 handles eviction when threshold=1 (shared private key).
func evictThreshold1(
	agentID *identity.Identity,
	s *store.Store,
	stateDir string,
	oldCampfireID, newCampfireID string,
	oldCFState *campfire.CampfireState,
	newCFIdentity *identity.Identity,
	evictedPubkeyHex string,
	remainingPeers []store.PeerEndpoint,
	reason string,
) error {
	newPrivKey := []byte(newCFIdentity.PrivateKey)
	newPubKey := []byte(newCFIdentity.PublicKey)

	// Build campfire:rekey message signed by OLD campfire key.
	rekeyPayload := buildRekeyPayload(oldCampfireID, newCampfireID, reason)
	rekeyMsg, err := message.NewMessage(
		ed25519.PrivateKey(oldCFState.PrivateKey),
		ed25519.PublicKey(oldCFState.PublicKey),
		rekeyPayload,
		[]string{"campfire:rekey"},
		nil,
	)
	if err != nil {
		return fmt.Errorf("creating rekey message: %w", err)
	}
	rekeyMsgCBOR, err := cfencoding.Marshal(rekeyMsg)
	if err != nil {
		return fmt.Errorf("encoding rekey message: %w", err)
	}

	// Send rekey notification to each remaining peer.
	for _, peer := range remainingPeers {
		if peer.MemberPubkey == agentID.PublicKeyHex() || peer.Endpoint == "" {
			continue
		}
		if err := deliverRekey(peer.Endpoint, oldCampfireID, newCampfireID,
			newPrivKey, nil, 0, evictedPubkeyHex, rekeyMsgCBOR, agentID); err != nil {
			fmt.Fprintf(os.Stderr, "warning: rekey delivery to peer %s failed: %v\n", peer.Endpoint, err)
		}
	}

	// Update local campfire state file.
	newCFState := campfire.CampfireState{
		PublicKey:             newPubKey,
		PrivateKey:            newPrivKey,
		JoinProtocol:          oldCFState.JoinProtocol,
		ReceptionRequirements: oldCFState.ReceptionRequirements,
		CreatedAt:             oldCFState.CreatedAt,
		Threshold:             oldCFState.Threshold,
	}
	newStateData, err := cfencoding.Marshal(newCFState)
	if err != nil {
		return fmt.Errorf("encoding new campfire state: %w", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, newCampfireID+".cbor"), newStateData, 0600); err != nil {
		return fmt.Errorf("writing new campfire state: %w", err)
	}

	// Update store FIRST: rename campfire_id in all tables.
	// If this fails, leave the old state file intact so the campfire remains recoverable.
	if err := s.UpdateCampfireID(oldCampfireID, newCampfireID); err != nil {
		return fmt.Errorf("updating campfire ID in store: %w", err)
	}

	// Only remove old state file after DB is committed — prevents inconsistency
	// if the DB update fails (matches the fix applied to handleRekey).
	os.Remove(filepath.Join(stateDir, oldCampfireID+".cbor")) //nolint:errcheck

	// Remove evicted member from peer endpoints.
	s.DeletePeerEndpoint(newCampfireID, evictedPubkeyHex) //nolint:errcheck

	// Store rekey system message locally under the new campfire ID.
	storeRekeyMessage(s, newCampfireID, rekeyMsgCBOR) //nolint:errcheck

	// Update beacon: remove old, publish new.
	updateBeacon(oldCampfireID, newCFIdentity.PublicKey, newCFIdentity.PrivateKey,
		oldCFState.JoinProtocol, oldCFState.ReceptionRequirements)

	return nil
}

// evictThresholdN handles eviction when threshold>1 (FROST multi-party signing).
// Returns the new campfire ID (the FROST group public key hex).
func evictThresholdN(
	agentID *identity.Identity,
	s *store.Store,
	stateDir string,
	oldCampfireID string,
	oldCFState *campfire.CampfireState,
	evictedPubkeyHex string,
	remainingPeers []store.PeerEndpoint,
	reason string,
	thresh uint,
) (string, error) {
	// Count remaining members (including self) for new DKG.
	// remainingPeers comes from the peer_endpoints table. The creator may not have
	// stored their own entry there (they are the initiator, not a joiner), so we
	// check explicitly and add 1 for self if not already counted.
	selfInPeers := false
	for _, p := range remainingPeers {
		if p.MemberPubkey == agentID.PublicKeyHex() {
			selfInPeers = true
			break
		}
	}
	n := uint(len(remainingPeers))
	if !selfInPeers {
		n++ // include self in the DKG participant count
	}
	if n == 0 {
		n = 1
	}

	// New threshold: keep same or reduce if remaining count < threshold.
	newThresh := thresh
	if newThresh > n {
		newThresh = n
	}
	if newThresh < 1 {
		newThresh = 1
	}

	// Run new DKG in-process for all remaining participants.
	participantIDs := make([]uint32, n)
	for i := uint(0); i < n; i++ {
		participantIDs[i] = uint32(i + 1)
	}
	dkgResults, err := threshold.RunDKG(participantIDs, int(newThresh))
	if err != nil {
		return "", fmt.Errorf("running new DKG: %w", err)
	}

	// New campfire ID = FROST group public key.
	creatorResult := dkgResults[1]
	newGroupPubKey := creatorResult.GroupPublicKey()
	actualNewCampfireID := fmt.Sprintf("%x", newGroupPubKey)

	// Build campfire:rekey message.
	// For threshold>1, try FROST signing with old shares; fall back to best-effort.
	rekeyPayload := buildRekeyPayload(oldCampfireID, actualNewCampfireID, reason)
	rekeyMsgCBOR, err := buildAndSignRekeyMessage(agentID, s, oldCampfireID, oldCFState,
		rekeyPayload, remainingPeers, thresh)
	if err != nil {
		// FROST quorum signing failed — no single private key exists for threshold>1.
		// Send the eviction without a rekey audit message rather than sending an unsigned
		// placeholder that would be rejected by peers (handler_rekey.go enforces signature
		// verification for all thresholds). The eviction and key rotation still proceed;
		// only the audit record is missing for this rekey event.
		fmt.Fprintf(os.Stderr, "warning: threshold signing for rekey failed (%v); eviction proceeds without signed audit record\n", err)
		rekeyMsgCBOR = nil
	}

	// Assign participant IDs to remaining peers.
	// Creator is always participant 1.
	peerToParticipantID := make(map[string]uint32)
	peerToParticipantID[agentID.PublicKeyHex()] = 1
	nextPID := uint32(2)
	for _, peer := range remainingPeers {
		if peer.MemberPubkey == agentID.PublicKeyHex() {
			continue
		}
		peerToParticipantID[peer.MemberPubkey] = nextPID
		nextPID++
	}

	// Send rekey + new DKG shares to each remaining peer.
	for _, peer := range remainingPeers {
		if peer.MemberPubkey == agentID.PublicKeyHex() || peer.Endpoint == "" {
			continue
		}
		pid := peerToParticipantID[peer.MemberPubkey]
		if pid == 0 {
			continue
		}
		r, ok := dkgResults[pid]
		if !ok {
			fmt.Fprintf(os.Stderr, "warning: no DKG result for participant %d (peer %s)\n", pid, peer.Endpoint)
			continue
		}
		shareData, err := threshold.MarshalResult(pid, r)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: marshaling DKG share for participant %d: %v\n", pid, err)
			continue
		}
		if err := deliverRekey(peer.Endpoint, oldCampfireID, actualNewCampfireID,
			nil, shareData, pid, evictedPubkeyHex, rekeyMsgCBOR, agentID); err != nil {
			fmt.Fprintf(os.Stderr, "warning: rekey delivery to peer %s failed: %v\n", peer.Endpoint, err)
		}
	}

	// Update local campfire state file with new group key.
	newCFState := campfire.CampfireState{
		PublicKey:             newGroupPubKey,
		PrivateKey:            nil, // threshold>1 has no single private key
		JoinProtocol:          oldCFState.JoinProtocol,
		ReceptionRequirements: oldCFState.ReceptionRequirements,
		CreatedAt:             oldCFState.CreatedAt,
		Threshold:             newThresh,
	}
	newStateData, err := cfencoding.Marshal(newCFState)
	if err != nil {
		return "", fmt.Errorf("encoding new campfire state: %w", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, actualNewCampfireID+".cbor"), newStateData, 0600); err != nil {
		return "", fmt.Errorf("writing new campfire state: %w", err)
	}

	// Update store FIRST: rename campfire_id in all tables before inserting the new
	// threshold share (avoids UNIQUE constraint conflicts on threshold_shares).
	// If this fails, leave the old state file intact so the campfire remains recoverable.
	if err := s.UpdateCampfireID(oldCampfireID, actualNewCampfireID); err != nil {
		return "", fmt.Errorf("updating campfire ID in store: %w", err)
	}

	// Only remove old state file after DB is committed — prevents inconsistency
	// if the DB update fails (matches the fix applied to handleRekey).
	os.Remove(filepath.Join(stateDir, oldCampfireID+".cbor")) //nolint:errcheck

	// Store creator's new DKG share (participant 1).
	// Called after UpdateCampfireID so UpsertThresholdShare's ON CONFLICT UPDATE applies.
	creatorShareData, err := threshold.MarshalResult(1, creatorResult)
	if err != nil {
		return "", fmt.Errorf("serializing creator DKG share: %w", err)
	}
	if err := s.UpsertThresholdShare(store.ThresholdShare{
		CampfireID:    actualNewCampfireID,
		ParticipantID: 1,
		SecretShare:   creatorShareData,
	}); err != nil {
		return "", fmt.Errorf("storing creator threshold share: %w", err)
	}

	// Remove evicted member from peer endpoints.
	s.DeletePeerEndpoint(actualNewCampfireID, evictedPubkeyHex) //nolint:errcheck

	// Update remaining peer participant IDs in store.
	for _, peer := range remainingPeers {
		pid := peerToParticipantID[peer.MemberPubkey]
		if pid == 0 {
			continue
		}
		s.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
			CampfireID:    actualNewCampfireID,
			MemberPubkey:  peer.MemberPubkey,
			Endpoint:      peer.Endpoint,
			ParticipantID: pid,
		})
	}

	// Store rekey system message locally.
	storeRekeyMessage(s, actualNewCampfireID, rekeyMsgCBOR) //nolint:errcheck

	// Remove old beacon (can't publish new one for threshold>1 without single private key).
	oldPubKeyBytes, _ := hex.DecodeString(oldCampfireID)
	beacon.Remove(BeaconDir(), oldPubKeyBytes) //nolint:errcheck
	fmt.Fprintf(os.Stderr, "note: new beacon not published (threshold>1 campfire has no single private key for beacon signing)\n")

	return actualNewCampfireID, nil
}

// buildRekeyPayload constructs the JSON payload for a campfire:rekey message.
func buildRekeyPayload(oldKey, newKey, reason string) []byte {
	p := map[string]string{
		"old_key": oldKey,
		"new_key": newKey,
		"reason":  reason,
	}
	b, _ := json.Marshal(p)
	return b
}

// buildAndSignRekeyMessage produces a FROST-signed campfire:rekey message using
// the old campfire key shares held by remaining peers.
// Returns the CBOR-encoded signed message.
func buildAndSignRekeyMessage(
	agentID *identity.Identity,
	s *store.Store,
	oldCampfireID string,
	oldCFState *campfire.CampfireState,
	payload []byte,
	remainingPeers []store.PeerEndpoint,
	thresh uint,
) ([]byte, error) {
	// Load this node's OLD DKG share.
	share, err := s.GetThresholdShare(oldCampfireID)
	if err != nil || share == nil {
		return nil, fmt.Errorf("no threshold share for old campfire")
	}
	myParticipantID, myDKGResult, err := threshold.UnmarshalResult(share.SecretShare)
	if err != nil {
		return nil, fmt.Errorf("deserializing old threshold share: %w", err)
	}

	// Build the canonical sign bytes for the message.
	msgID := uuid.New().String()
	ts := time.Now().UnixNano()
	tags := []string{"campfire:rekey"}
	antecedents := []string{}
	signInput := message.MessageSignInput{
		ID:          msgID,
		Payload:     payload,
		Tags:        tags,
		Antecedents: antecedents,
		Timestamp:   ts,
	}
	signBytes, err := cfencoding.Marshal(signInput)
	if err != nil {
		return nil, fmt.Errorf("encoding sign input: %w", err)
	}

	// Pick co-signers (remaining peers with known participant IDs and endpoints).
	needed := int(thresh) - 1
	type coSigner struct {
		endpoint      string
		participantID uint32
	}
	var coSigners []coSigner
	for _, p := range remainingPeers {
		if p.MemberPubkey == agentID.PublicKeyHex() {
			continue
		}
		if p.ParticipantID > 0 && p.Endpoint != "" {
			coSigners = append(coSigners, coSigner{endpoint: p.Endpoint, participantID: p.ParticipantID})
		}
		if len(coSigners) >= needed {
			break
		}
	}
	if len(coSigners) < needed {
		return nil, fmt.Errorf("need %d co-signers, only %d available", needed, len(coSigners))
	}

	// Build co-signer list for RunFROSTSign.
	frostCoSigners := make([]cfhttp.CoSigner, len(coSigners))
	for i, cs := range coSigners {
		frostCoSigners[i] = cfhttp.CoSigner{
			Endpoint:      cs.endpoint,
			ParticipantID: cs.participantID,
		}
	}

	sessionID := uuid.New().String()

	sig, err := cfhttp.RunFROSTSign(myDKGResult, myParticipantID, signBytes, frostCoSigners, oldCampfireID, sessionID, agentID)
	if err != nil {
		return nil, fmt.Errorf("FROST signing: %w", err)
	}

	// Assemble signed message.
	rekeyMsg := &message.Message{
		ID:          msgID,
		Sender:      ed25519.PublicKey(oldCFState.PublicKey),
		Payload:     payload,
		Tags:        tags,
		Antecedents: antecedents,
		Timestamp:   ts,
		Signature:   sig,
		Provenance:  []message.ProvenanceHop{},
	}
	return cfencoding.Marshal(rekeyMsg)
}

// deliverRekey sends new key material to one remaining peer using ECDH encryption.
// Uses a two-phase protocol matching the join handshake:
//   Phase 1: GET peer's ephemeral X25519 public key via POST with no key material.
//   Phase 2: Send encrypted key material using derived shared secret.
func deliverRekey(
	endpoint, oldCampfireID, newCampfireID string,
	newPrivKey []byte,
	newShareData []byte,
	newParticipantID uint32,
	evictedPubkeyHex string,
	rekeyMsgCBOR []byte,
	agentID *identity.Identity,
) error {
	// Generate sender's ephemeral X25519 key.
	senderPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generating ephemeral X25519 key: %w", err)
	}
	senderPubHex := fmt.Sprintf("%x", senderPriv.PublicKey().Bytes())

	// Phase 1: send request with no encrypted payload, get receiver's ephemeral pub.
	phase1Req := cfhttp.RekeyRequest{
		NewCampfireID:       newCampfireID,
		SenderX25519Pub:     senderPubHex,
		EvictedMemberPubkey: evictedPubkeyHex,
		RekeyMessageCBOR:    rekeyMsgCBOR,
		// No EncryptedPrivKey or EncryptedShareData yet.
	}

	receiverEphemeralPubHex, err := cfhttp.SendRekeyPhase1(endpoint, oldCampfireID, phase1Req, agentID)
	if err != nil {
		return fmt.Errorf("rekey phase 1: %w", err)
	}

	if receiverEphemeralPubHex == "" {
		// Peer processed phase 1 as complete (no key material needed, e.g., already updated).
		return nil
	}

	// Derive shared secret: ECDH(sender_priv, receiver_pub).
	receiverPubBytes, err := hex.DecodeString(receiverEphemeralPubHex)
	if err != nil {
		return fmt.Errorf("decoding receiver ephemeral pub: %w", err)
	}
	receiverPub, err := ecdh.X25519().NewPublicKey(receiverPubBytes)
	if err != nil {
		return fmt.Errorf("parsing receiver ephemeral pub: %w", err)
	}
	rawShared, err := senderPriv.ECDH(receiverPub)
	if err != nil {
		return fmt.Errorf("ECDH: %w", err)
	}
	// Apply HKDF-SHA256 to the raw ECDH output for domain separation
	// and proper key derivation. Info string must match the handler side.
	sharedSecret, err := cfhttp.HkdfSHA256(rawShared, "campfire-rekey-v1")
	if err != nil {
		return fmt.Errorf("HKDF: %w", err)
	}

	// Phase 2: encrypt and send key material.
	phase2Req := cfhttp.RekeyRequest{
		NewCampfireID:       newCampfireID,
		SenderX25519Pub:     senderPubHex,
		EvictedMemberPubkey: evictedPubkeyHex,
		RekeyMessageCBOR:    rekeyMsgCBOR,
		NewParticipantID:    newParticipantID,
	}

	if len(newPrivKey) > 0 {
		enc, err := cfhttp.AESGCMEncrypt(sharedSecret, newPrivKey)
		if err != nil {
			return fmt.Errorf("encrypting new private key: %w", err)
		}
		phase2Req.EncryptedPrivKey = enc
	}
	if len(newShareData) > 0 {
		enc, err := cfhttp.AESGCMEncrypt(sharedSecret, newShareData)
		if err != nil {
			return fmt.Errorf("encrypting new share data: %w", err)
		}
		phase2Req.EncryptedShareData = enc
	}

	return cfhttp.SendRekey(endpoint, oldCampfireID, phase2Req, agentID)
}

// storeRekeyMessage decodes and stores a CBOR-encoded rekey message in the local store.
func storeRekeyMessage(s *store.Store, campfireID string, rekeyMsgCBOR []byte) error {
	if len(rekeyMsgCBOR) == 0 {
		return nil
	}
	var rekeyMsg message.Message
	if err := cfencoding.Unmarshal(rekeyMsgCBOR, &rekeyMsg); err != nil {
		return fmt.Errorf("decoding rekey message: %w", err)
	}
	_, err := s.AddMessage(store.MessageRecordFromMessage(campfireID, &rekeyMsg, store.NowNano()))
	return err
}

// updateBeacon removes the old beacon and publishes a new one for the rekeyed campfire.
func updateBeacon(oldCampfireID string, newPubKey ed25519.PublicKey, newPrivKey ed25519.PrivateKey,
	joinProtocol string, receptionReqs []string) {
	beaconDir := BeaconDir()

	// Remove old beacon.
	oldPubKeyBytes, err := hex.DecodeString(oldCampfireID)
	if err == nil {
		beacon.Remove(beaconDir, oldPubKeyBytes) //nolint:errcheck
	}

	// Find self endpoint for new beacon.
	selfEndpoint := ""
	if evictListen != "" {
		useTLS := evictTLSCert != ""
		selfEndpoint = resolveEndpoint(evictListen, useTLS)
	}

	var transportConfig beacon.TransportConfig
	if selfEndpoint != "" {
		transportConfig = beacon.TransportConfig{
			Protocol: "p2p-http",
			Config:   map[string]string{"endpoints": selfEndpoint},
		}
	} else {
		transportConfig = beacon.TransportConfig{
			Protocol: "p2p-http",
			Config:   map[string]string{},
		}
	}

	b, err := beacon.New(
		newPubKey, newPrivKey,
		joinProtocol, receptionReqs,
		transportConfig, "",
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: creating new beacon: %v\n", err)
		return
	}
	if err := beacon.Publish(beaconDir, b); err != nil {
		fmt.Fprintf(os.Stderr, "warning: publishing new beacon: %v\n", err)
	}
}

func init() {
	evictCmd.Flags().StringVar(&evictReason, "reason", "", "reason for eviction")
	evictCmd.Flags().StringVar(&evictListen, "listen", "", "HTTP listen address for beacon update (optional)")
	evictCmd.Flags().StringVar(&evictTLSCert, "tls-cert", "", "TLS certificate file (PEM); enables https:// endpoint in updated beacon")
	evictCmd.Flags().StringVar(&evictTLSKey, "tls-key", "", "TLS private key file (PEM); must be paired with --tls-cert")
	rootCmd.AddCommand(evictCmd)
}
