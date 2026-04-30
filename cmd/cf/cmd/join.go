package cmd

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/admission"
	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/cf-protocol/campfire"
	cfencoding "github.com/campfire-net/campfire/cf-protocol/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/cf-protocol/message"
	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/threshold"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
	cfhttp "github.com/campfire-net/campfire/cf-protocol/transport/http"
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
		joinInviteCode, _ := cmd.Flags().GetString("invite-code")
		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		// Detect beacon URI before general resolution so we can extract the
		// transport hint for join routing.
		// SECURITY: transport hint from beacon is ONLY used here for join, never
		// for send/read operations on existing memberships (SSRF prevention).
		if naming.IsCampfireURI(args[0]) {
			parsed, parseErr := naming.ParseURI(args[0])
			if parseErr != nil {
				return fmt.Errorf("parsing URI: %w", parseErr)
			}
			if parsed.Kind == naming.URIKindBeacon {
				existingMembership, _ := s.GetMembership(parsed.CampfireID)
				if existingMembership != nil {
					return fmt.Errorf("already a member of campfire %s", parsed.CampfireID[:shortIDLen])
				}
				return joinFromBeacon(parsed, agentID, s, joinListen, joinTLSCert, joinTLSKey)
			}
		}

		campfireID, err := resolveCampfireID(args[0], s)
		if err != nil {
			return err
		}

		// Check if already a member
		existingMembership, _ := s.GetMembership(campfireID)
		if existingMembership != nil {
			return fmt.Errorf("already a member of campfire %s", campfireID[:shortIDLen])
		}

		// Route based on --via flag (p2p-http), or filesystem (default).
		// Note: GitHub transport was removed in v0.30.0.
		if joinVia != "" {
			return joinP2PHTTP(campfireID, agentID, s, joinVia, joinListen, joinTLSCert, joinTLSKey, joinInviteCode)
		}
		if strings.HasPrefix(campfireID, "https://github.com/") {
			return fmt.Errorf("GitHub transport was removed in v0.30.0; GitHub Issue URLs are no longer supported")
		}
		return joinFilesystem(campfireID, agentID, s)
	},
}

// joinFromBeacon joins a campfire using the transport hint from a verified beacon URI.
// SECURITY: the transport hint is only used here (join path), never for send/read.
func joinFromBeacon(parsed *naming.URI, agentID *identity.Identity, s store.Store, listen, tlsCert, tlsKey string) error {
	campfireID := parsed.CampfireID

	// Decode the beacon to read the transport hint.
	beaconBytes, err := decodeBeaconBase64(parsed.BeaconData)
	if err != nil {
		return fmt.Errorf("decoding beacon: %w", err)
	}
	var b beacon.Beacon
	if err := cfencoding.Unmarshal(beaconBytes, &b); err != nil {
		return fmt.Errorf("unmarshalling beacon: %w", err)
	}
	// Re-verify after decode (defence-in-depth — ParseURI already verified).
	if !b.Verify() {
		return fmt.Errorf("beacon signature invalid")
	}

	switch b.Transport.Protocol {
	case "p2p-http":
		via, ok := b.Transport.Config["endpoint"]
		if !ok || via == "" {
			return fmt.Errorf("beacon p2p-http transport missing 'endpoint' config key")
		}
		return joinP2PHTTP(campfireID, agentID, s, via, listen, tlsCert, tlsKey, "")
	case "github":
		// GitHub transport was removed in v0.30.0. Beacons with github protocol
		// can no longer be used to join campfires.
		return fmt.Errorf("GitHub transport was removed in v0.30.0; cannot join campfire via a github beacon")
	default:
		// Filesystem or unknown protocol: fall back to filesystem join.
		return joinFilesystem(campfireID, agentID, s)
	}
}

// decodeBeaconBase64 decodes a base64-encoded beacon payload string.
// Tries RawURL, URL, RawStd, and Std encodings in order.
func decodeBeaconBase64(data string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	} {
		if raw, err := enc.DecodeString(data); err == nil {
			return raw, nil
		}
	}
	return nil, fmt.Errorf("invalid base64 beacon data")
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
	// Pre-admitted members bypass this check.
	existingMembers, err := tr.ListMembers(campfireID)
	if err != nil {
		return fmt.Errorf("listing members: %w", err)
	}
	alreadyOnDisk := false
	existingRole := campfire.RoleFull
	for _, m := range existingMembers {
		if fmt.Sprintf("%x", m.PublicKey) == agentID.PublicKeyHex() {
			alreadyOnDisk = true
			if m.Role != "" {
				existingRole = m.Role
			}
			break
		}
	}
	if !alreadyOnDisk {
		switch state.JoinProtocol {
		case "open":
			// Immediately admitted via AdmitMember below.
		case "invite-only":
			return fmt.Errorf("campfire %s is invite-only; ask a member to run 'cf admit %s %s'",
				campfireID[:shortIDLen], campfireID[:shortIDLen], agentID.PublicKeyHex())
		default:
			return fmt.Errorf("unknown join protocol: %s", state.JoinProtocol)
		}
	}

	// Look up description from beacon (best-effort).
	description := lookupBeaconDescription(campfireID)

	// Determine effective role: preserve pre-admitted role if already on disk.
	effectiveRole := existingRole // campfire.RoleFull for new joins, or pre-admitted role

	// Admit member via shared admission package (writes member file + records in store).
	// Skip writing to FSTransport if already on disk (member file already exists).
	now := time.Now().UnixNano()
	var fstr admission.FSTransport
	if !alreadyOnDisk {
		fstr = tr
	}
	_, err = admission.AdmitMember(context.Background(), admission.AdmitterDeps{
		FSTransport: fstr,
		Store:       s,
	}, admission.AdmissionRequest{
		CampfireID:      campfireID,
		MemberPubKeyHex: agentID.PublicKeyHex(),
		Role:            effectiveRole,
		Encrypted:       state.Encrypted,
		JoinProtocol:    state.JoinProtocol,
		TransportDir:    tr.CampfireDir(campfireID),
		TransportType:   "filesystem",
		Description:     description,
	})
	if err != nil {
		return err
	}

	// Write campfire:member-joined system message to transport (idempotent with sync).
	if !alreadyOnDisk {
		cfSigner, msgErr := message.NewEd25519Signer(
			ed25519.PrivateKey(state.PrivateKey),
			ed25519.PublicKey(state.PublicKey),
		)
		var sysMsg *message.Message
		if msgErr == nil {
			sysMsg, msgErr = message.NewMessage(
				cfSigner,
				[]byte(fmt.Sprintf(`{"member":"%s","joined_at":%d}`, agentID.PublicKeyHex(), now)),
				[]string{campfire.TagMemberJoined},
				nil,
			)
		}
		if msgErr == nil {
			updatedMembers, _ := tr.ListMembers(campfireID)
			cf := campfireFromState(state, updatedMembers)
			if hopErr := sysMsg.AddHop(
				state.PrivateKey, state.PublicKey,
				cf.MembershipHash(), len(updatedMembers),
				state.JoinProtocol, state.ReceptionRequirements,
				campfire.RoleFull,
			); hopErr == nil {
				tr.WriteMessage(campfireID, sysMsg) //nolint:errcheck
			}
		}
	}

	m, err := s.GetMembership(campfireID)
	if err != nil || m == nil {
		return fmt.Errorf("membership not found after admission")
	}

	// Sync messages immediately so convention declarations are available
	// without requiring a separate cf read. Errors are non-fatal here —
	// a failed sync at join time is not a reason to abort the join.
	syncCampfire(campfireID, m, agentID, s) //nolint:errcheck

	// Auto-send identity:profile if the agent has a display name (best-effort).
	maybeSendProfileMessage(campfireID, agentID, s)

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

func joinP2PHTTP(campfireID string, agentID *identity.Identity, s store.Store, via, listen, tlsCert, tlsKey, inviteCode string) error {
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
	var joinOpts []cfhttp.JoinOptions
	if inviteCode != "" {
		joinOpts = append(joinOpts, cfhttp.JoinOptions{InviteCode: inviteCode})
	}
	result, err := cfhttp.Join(via, campfireID, agentID, myEndpoint, joinOpts...)
	if err != nil {
		return fmt.Errorf("joining campfire via %s: %w", via, err)
	}

	// Persist campfire state locally using fs.Transport layout. This stores
	// the state at {baseDir}/{campfireID}/campfire.cbor, consistent with the
	// creator path (registerOnRelay) and filesystem campfires.
	baseDir := filepath.Join(CFHome(), "campfires")
	transport := fs.New(baseDir)
	cf := &campfire.Campfire{
		PublicKey:             result.CampfirePubKey,
		PrivateKey:            result.CampfirePrivKey,
		JoinProtocol:          result.JoinProtocol,
		ReceptionRequirements: result.ReceptionRequirements,
		Threshold:             result.Threshold,
	}
	if err := transport.Init(cf); err != nil {
		return fmt.Errorf("storing campfire state: %w", err)
	}

	// Look up description from beacon (best-effort).
	p2pDescription := lookupBeaconDescription(campfireID)

	// Record membership in local store via shared admission package.
	if _, err := admission.AdmitMember(context.Background(), admission.AdmitterDeps{
		FSTransport: transport,
		Store:       s,
	}, admission.AdmissionRequest{
		CampfireID:      campfireID,
		MemberPubKeyHex: agentID.PublicKeyHex(),
		Role:            campfire.RoleFull,
		JoinProtocol:    result.JoinProtocol,
		TransportDir:    transport.CampfireDir(campfireID),
		TransportType:   "p2p-http",
		Description:     p2pDescription,
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

	// Always store the relay (--via endpoint) as a peer so syncFromHTTPPeers
	// can pull messages from it. Serverless relays don't return themselves in
	// the peer list (no SelfInfo), so the joiner would have zero sync targets
	// without this. Uses the campfire pubkey as the member identifier.
	s.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
		CampfireID:   campfireID,
		MemberPubkey: fmt.Sprintf("%x", result.CampfirePubKey),
		Endpoint:     via,
	})

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
	// without requiring a separate cf read. Errors are non-fatal here —
	// a failed sync at join time is not a reason to abort the join.
	p2pMembership, _ := s.GetMembership(campfireID)
	syncCampfire(campfireID, p2pMembership, agentID, s) //nolint:errcheck

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
	joinCmd.Flags().String("invite-code", "", "invite code for joining invite-only campfires via p2p-http")
	joinCmd.Flags().String("listen", "", "HTTP listen address for p2p-http transport (e.g. :9002)")
	joinCmd.Flags().String("tls-cert", "", "TLS certificate file (PEM); enables https:// endpoint advertisement")
	joinCmd.Flags().String("tls-key", "", "TLS private key file (PEM); must be paired with --tls-cert")
	// Note: --github-repo, --github-token-env, --github-base-url were removed in v0.30.0
	// when the GitHub transport was cut.
	rootCmd.AddCommand(joinCmd)
}

// campfireFromState reconstructs a Campfire for membership hash computation.
func campfireFromState(state *campfire.CampfireState, members []campfire.MemberRecord) *campfire.Campfire {
	return state.ToCampfire(members)
}

// autoJoinRootCampfire joins an open-protocol filesystem campfire automatically.
// It is only called when the campfire ID came from ProjectRoot() and the agent
// is not yet a member. Returns nil if successfully joined or if the campfire is
// invite-only (skips silently). Returns an error only on unexpected failures.
func autoJoinRootCampfire(campfireID string, agentID *identity.Identity, s store.Store) error {
	transportDir := resolveFSTransportDir(campfireID)
	tr := fs.ForDir(transportDir)

	// Read campfire state to check join protocol.
	state, err := tr.ReadState(campfireID)
	if err != nil {
		// Transport state not found — can't auto-join, skip silently.
		return nil
	}

	// Only auto-join open campfires.
	if state.JoinProtocol != "open" {
		return nil
	}

	// Check if already a member in the transport (idempotency).
	members, err := tr.ListMembers(campfireID)
	if err != nil {
		return fmt.Errorf("listing members: %w", err)
	}
	alreadyOnDisk := false
	for _, m := range members {
		if fmt.Sprintf("%x", m.PublicKey) == agentID.PublicKeyHex() {
			// Already on disk — still record in store if not there.
			if existing, _ := s.GetMembership(campfireID); existing != nil {
				return nil
			}
			alreadyOnDisk = true
			break
		}
	}

	// Admit member via shared admission package.
	// When already on disk, skip FSTransport to avoid clobbering JoinedAt in the
	// existing member file — only record membership in the store.
	admitDeps := admission.AdmitterDeps{Store: s}
	if !alreadyOnDisk {
		admitDeps.FSTransport = tr
	}
	if _, err := admission.AdmitMember(context.Background(), admitDeps, admission.AdmissionRequest{
		CampfireID:      campfireID,
		MemberPubKeyHex: agentID.PublicKeyHex(),
		Role:            campfire.RoleFull,
		JoinProtocol:    state.JoinProtocol,
		TransportDir:    tr.CampfireDir(campfireID),
		TransportType:   "filesystem",
	}); err != nil {
		return fmt.Errorf("admitting member: %w", err)
	}

	fmt.Printf("Auto-joined campfire %s\n", campfireID[:12])
	return nil
}
