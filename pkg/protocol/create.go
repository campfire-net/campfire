package protocol

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/threshold"
	"github.com/campfire-net/campfire/pkg/transport/fs"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// CreateRequest holds all parameters for Client.Create().
type CreateRequest struct {
	// Description is an optional human-readable description of the campfire.
	// Stored in the membership metadata.
	Description string

	// JoinProtocol is "open" (default) or "invite-only".
	JoinProtocol string

	// ReceptionRequirements is the list of required tags for messages.
	// Nil is treated as empty.
	ReceptionRequirements []string

	// Threshold is the FROST signing threshold. 0 or 1 means single-signer mode.
	// Values > 1 require the P2P HTTP transport and trigger DKG.
	Threshold uint

	// Transport selects and configures the transport. Use FilesystemTransport,
	// P2PHTTPTransport, or GitHubTransport. Required.
	Transport Transport

	// BeaconDir is the directory to publish the beacon file.
	// If empty, beacon.DefaultBeaconDir() is used.
	BeaconDir string
}

// CreateResult holds the outcome of a successful Create() call.
type CreateResult struct {
	// CampfireID is the hex-encoded campfire public key.
	CampfireID string

	// BeaconID is the hex-encoded beacon identity (same as CampfireID), provided
	// as a convenience so callers can use it directly without parsing Beacon.
	BeaconID string

	// Beacon is the published beacon for this campfire.
	Beacon *beacon.Beacon

	// BeaconPath is the absolute path of the published beacon file.
	BeaconPath string
}

// Create generates a new campfire keypair, initializes the transport, admits
// the caller as the first member (role=full), and publishes a beacon.
//
// Supported transports: filesystem, P2P HTTP (threshold=1 and threshold>1), GitHub.
//
// The caller must already have an identity (Init returns one). If c.identity is
// nil, Create returns an error.
func (c *Client) Create(req CreateRequest) (*CreateResult, error) {
	if c.identity == nil {
		return nil, fmt.Errorf("identity required to create a campfire")
	}

	if req.JoinProtocol == "" {
		req.JoinProtocol = "open"
	}
	if req.Threshold == 0 {
		req.Threshold = 1
	}

	// Generate campfire keypair.
	cf, err := campfire.New(req.JoinProtocol, req.ReceptionRequirements, req.Threshold)
	if err != nil {
		return nil, fmt.Errorf("generating campfire: %w", err)
	}
	campfireID := cf.PublicKeyHex()

	var transportType string
	var transportDir string

	switch t := req.Transport.(type) {
	case *FilesystemTransport:
		transportType = "filesystem"
		transportDir, err = c.createFilesystemCampfire(cf, t)
	case FilesystemTransport:
		transportType = "filesystem"
		transportDir, err = c.createFilesystemCampfire(cf, &t)
	case *P2PHTTPTransport:
		transportType = "p2p-http"
		transportDir, err = c.createP2PHTTPCampfire(cf, t)
	case P2PHTTPTransport:
		transportType = "p2p-http"
		transportDir, err = c.createP2PHTTPCampfire(cf, &t)
	case *GitHubTransport:
		transportType = "github"
		transportDir = githubTransportDirFromConfig(t)
	case GitHubTransport:
		transportType = "github"
		transportDir = githubTransportDirFromConfig(&t)
	case nil:
		return nil, fmt.Errorf("Transport is required for Create (use FilesystemTransport, P2PHTTPTransport, or GitHubTransport)")
	default:
		return nil, fmt.Errorf("unsupported transport type: %T", req.Transport)
	}
	if err != nil {
		return nil, err
	}

	// Admit self as first member (role=full) in the local store.
	membership := store.Membership{
		CampfireID:    campfireID,
		TransportDir:  transportDir,
		JoinProtocol:  req.JoinProtocol,
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     req.Threshold,
		TransportType: transportType,
		CreatorPubkey: c.identity.PublicKeyHex(),
		Description:   req.Description,
	}
	if err := c.store.AddMembership(membership); err != nil {
		return nil, fmt.Errorf("recording membership: %w", err)
	}

	// Publish beacon.
	beaconDir := req.BeaconDir
	if beaconDir == "" {
		beaconDir = beacon.DefaultBeaconDir()
	}

	transportConfig := buildBeaconTransportFromTyped(req.Transport, transportType, transportDir)
	b, err := beacon.New(
		cf.PublicKey, cf.PrivateKey,
		req.JoinProtocol,
		req.ReceptionRequirements,
		transportConfig,
		"",
	)
	if err != nil {
		return nil, fmt.Errorf("creating beacon: %w", err)
	}

	if err := beacon.Publish(beaconDir, b); err != nil {
		return nil, fmt.Errorf("publishing beacon: %w", err)
	}

	beaconPath := filepath.Join(beaconDir, fmt.Sprintf("%x.beacon", cf.PublicKey))

	return &CreateResult{
		CampfireID: campfireID,
		BeaconID:   campfireID,
		Beacon:     b,
		BeaconPath: beaconPath,
	}, nil
}

// createFilesystemCampfire initializes the filesystem transport for a new campfire
// and returns the TransportDir (the campfire-specific subdirectory).
func (c *Client) createFilesystemCampfire(cf *campfire.Campfire, t *FilesystemTransport) (string, error) {
	if t.Dir == "" {
		return "", fmt.Errorf("FilesystemTransport.Dir is required for filesystem transport")
	}

	tr := fs.New(t.Dir)

	// Create directory structure and write campfire state.
	if err := tr.Init(cf); err != nil {
		return "", fmt.Errorf("initializing filesystem transport: %w", err)
	}

	// Write creator as first member record.
	if err := tr.WriteMember(cf.PublicKeyHex(), campfire.MemberRecord{
		PublicKey: c.identity.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		return "", fmt.Errorf("writing creator member record: %w", err)
	}

	return tr.CampfireDir(cf.PublicKeyHex()), nil
}

// createP2PHTTPCampfire initializes the P2P HTTP transport for a new campfire.
// For threshold>1, runs a local DKG and stores the creator's share plus pending
// shares for future members.
// Returns the TransportDir (the directory where the state CBOR is written).
func (c *Client) createP2PHTTPCampfire(cf *campfire.Campfire, t *P2PHTTPTransport) (string, error) {
	if t.Transport == nil {
		return "", fmt.Errorf("P2PHTTPTransport.Transport is required for p2p-http transport")
	}

	campfireID := cf.PublicKeyHex()

	// P2P HTTP campfires support both pull and push delivery.
	cf.DeliveryModes = []string{campfire.DeliveryModePull, campfire.DeliveryModePush}
	state := cf.State()

	// Write campfire state CBOR to stateDir/{campfireID}.cbor.
	// Use Dir from the transport config if set, otherwise create a temp dir.
	stateDir := t.Dir
	if stateDir == "" {
		var err error
		stateDir, err = os.MkdirTemp("", "campfire-p2p-")
		if err != nil {
			return "", fmt.Errorf("creating temp transport dir: %w", err)
		}
	}
	stateData, err := cfencoding.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("encoding campfire state: %w", err)
	}
	statePath := fmt.Sprintf("%s/%s.cbor", stateDir, campfireID)
	if err := atomicWriteFile(statePath, stateData); err != nil {
		return "", fmt.Errorf("writing campfire state: %w", err)
	}

	// Capture keys for the key provider closure.
	cfPrivKey := make([]byte, len(cf.PrivateKey))
	cfPubKey := make([]byte, len(cf.PublicKey))
	copy(cfPrivKey, cf.PrivateKey)
	copy(cfPubKey, cf.PublicKey)
	cfID := campfireID

	t.Transport.SetKeyProvider(func(id string) ([]byte, []byte, error) {
		if id == cfID {
			return cfPrivKey, cfPubKey, nil
		}
		return nil, nil, fmt.Errorf("campfire %s not hosted on this node", shortID(id))
	})

	// Register delivery modes provider.
	deliveryModes := cf.DeliveryModes
	t.Transport.SetDeliveryModesProvider(func(id string) []string {
		if id == cfID {
			return deliveryModes
		}
		return nil
	})

	// Register self info on the transport and in the store.
	if t.MyEndpoint != "" {
		t.Transport.SetSelfInfo(c.identity.PublicKeyHex(), t.MyEndpoint)
		if err := c.store.UpsertPeerEndpoint(store.PeerEndpoint{
			CampfireID:    campfireID,
			MemberPubkey:  c.identity.PublicKeyHex(),
			Endpoint:      t.MyEndpoint,
			ParticipantID: 1,
		}); err != nil {
			return "", fmt.Errorf("registering peer endpoint: %w", err)
		}
	}

	// For threshold>1: run DKG and store creator's share + pending shares.
	if cf.Threshold > 1 {
		if err := c.initThresholdDKG(cf, campfireID, t.Transport); err != nil {
			return "", fmt.Errorf("initializing threshold DKG: %w", err)
		}
	}

	return stateDir, nil
}

// githubTransportDirFromConfig encodes a GitHubTransport as a transport dir string.
// The GitHub transport stores metadata as a JSON string in TransportDir.
// This is a legacy format maintained for store compatibility.
func githubTransportDirFromConfig(t *GitHubTransport) string {
	if t.Owner != "" && t.Repo != "" {
		return fmt.Sprintf("github:%s/%s", t.Owner, t.Repo)
	}
	return ""
}

// initThresholdDKG runs an in-process DKG for a new threshold campfire.
// The creator is assigned participant ID 1 and its share is stored in the local
// store. Additional shares (IDs 2..threshold) are stored as pending so future
// joiners can claim them via ClaimPendingThresholdShare.
func (c *Client) initThresholdDKG(cf *campfire.Campfire, campfireID string, tr *cfhttp.Transport) error {
	numParticipants := int(cf.Threshold)
	participantIDs := make([]uint32, numParticipants)
	for i := range participantIDs {
		participantIDs[i] = uint32(i + 1)
	}

	results, err := threshold.RunDKG(participantIDs, numParticipants)
	if err != nil {
		return fmt.Errorf("running DKG: %w", err)
	}

	// Store creator's share (participant ID 1).
	creatorResult := results[1]
	shareData, err := threshold.MarshalResult(1, creatorResult)
	if err != nil {
		return fmt.Errorf("serializing creator threshold share: %w", err)
	}
	if err := c.store.UpsertThresholdShare(store.ThresholdShare{
		CampfireID:    campfireID,
		ParticipantID: 1,
		SecretShare:   shareData,
	}); err != nil {
		return fmt.Errorf("storing creator threshold share: %w", err)
	}

	// Register threshold share provider on the transport so the sign handler
	// can retrieve this node's share during FROST signing rounds.
	s := c.store
	tr.SetThresholdShareProvider(func(id string) (uint32, []byte, error) {
		share, err := s.GetThresholdShare(id)
		if err != nil {
			return 0, nil, err
		}
		if share == nil {
			return 0, nil, fmt.Errorf("no threshold share for campfire %s", shortID(id))
		}
		return share.ParticipantID, share.SecretShare, nil
	})

	// Store additional participant shares as pending (for future joiners).
	for pid := uint32(2); pid <= uint32(numParticipants); pid++ {
		r := results[pid]
		data, err := threshold.MarshalResult(pid, r)
		if err != nil {
			return fmt.Errorf("serializing threshold share for participant %d: %w", pid, err)
		}
		if err := c.store.StorePendingThresholdShare(campfireID, pid, data); err != nil {
			return fmt.Errorf("storing pending threshold share for participant %d: %w", pid, err)
		}
	}

	return nil
}

// buildBeaconTransportFromTyped constructs the beacon.TransportConfig from a typed Transport.
func buildBeaconTransportFromTyped(t Transport, transportType, transportDir string) beacon.TransportConfig {
	switch transportType {
	case "p2p-http":
		config := map[string]string{
			"protocol": "p2p-http",
		}
		if p, ok := t.(*P2PHTTPTransport); ok && p.MyEndpoint != "" {
			config["endpoint"] = p.MyEndpoint
		} else if p, ok := t.(P2PHTTPTransport); ok && p.MyEndpoint != "" {
			config["endpoint"] = p.MyEndpoint
		}
		return beacon.TransportConfig{
			Protocol: "p2p-http",
			Config:   config,
		}
	case "github":
		return beacon.TransportConfig{
			Protocol: "github",
			Config:   map[string]string{"transport_dir": transportDir},
		}
	default: // filesystem
		return beacon.TransportConfig{
			Protocol: "filesystem",
			Config:   map[string]string{"dir": transportDir},
		}
	}
}

// atomicWriteFile writes data to path atomically via temp file + rename.
func atomicWriteFile(path string, data []byte) error {
	var randBytes [8]byte
	if _, err := rand.Read(randBytes[:]); err != nil {
		ns := uint64(time.Now().UnixNano())
		for i := range randBytes {
			randBytes[i] = byte(ns >> (uint(7-i) * 8))
		}
	}
	tmp := fmt.Sprintf("%s.tmp.%x", path, randBytes)
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("renaming file: %w", err)
	}
	return nil
}
