package protocol

import (
	"crypto/rand"
	"fmt"
	"os"
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
	// JoinProtocol is "open" (default) or "invite-only".
	JoinProtocol string

	// ReceptionRequirements is the list of required tags for messages.
	// Nil is treated as empty.
	ReceptionRequirements []string

	// Threshold is the FROST signing threshold. 0 or 1 means single-signer mode.
	// Values > 1 require the P2P HTTP transport and trigger DKG.
	Threshold uint

	// TransportDir is the base directory for the filesystem transport, or the
	// directory where P2P HTTP campfire state files are stored.
	// Required for "filesystem" and "p2p-http" transports.
	// For GitHub transport, set this to the github: JSON metadata string.
	TransportDir string

	// TransportType selects the transport: "filesystem" (default), "p2p-http", or "github".
	// Empty string defaults to "filesystem".
	TransportType string

	// BeaconDir is the directory to publish the beacon file.
	// If empty, beacon.DefaultBeaconDir() is used.
	BeaconDir string

	// HTTPTransport is the P2P HTTP Transport instance. Required when
	// TransportType is "p2p-http". The transport must already be started
	// (or the caller starts it after Create returns).
	HTTPTransport *cfhttp.Transport

	// MyHTTPEndpoint is this node's HTTP endpoint (e.g. "http://host:port").
	// Used when TransportType is "p2p-http".
	MyHTTPEndpoint string
}

// CreateResult holds the outcome of a successful Create() call.
type CreateResult struct {
	// CampfireID is the hex-encoded campfire public key.
	CampfireID string

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

	transportType := req.TransportType
	if transportType == "" {
		transportType = "filesystem"
	}

	var transportDir string

	switch transportType {
	case "filesystem":
		transportDir, err = c.createFilesystemCampfire(cf, req)
	case "p2p-http":
		transportDir, err = c.createP2PHTTPCampfire(cf, req)
	case "github":
		transportDir = req.TransportDir
	default:
		return nil, fmt.Errorf("unsupported transport type: %q", transportType)
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
	}
	if err := c.store.AddMembership(membership); err != nil {
		return nil, fmt.Errorf("recording membership: %w", err)
	}

	// Publish beacon.
	beaconDir := req.BeaconDir
	if beaconDir == "" {
		beaconDir = beacon.DefaultBeaconDir()
	}

	transportConfig := buildBeaconTransport(transportType, transportDir, req)
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

	beaconPath := fmt.Sprintf("%s/%x.beacon", beaconDir, cf.PublicKey)

	return &CreateResult{
		CampfireID: campfireID,
		Beacon:     b,
		BeaconPath: beaconPath,
	}, nil
}

// createFilesystemCampfire initializes the filesystem transport for a new campfire
// and returns the TransportDir (the campfire-specific subdirectory).
func (c *Client) createFilesystemCampfire(cf *campfire.Campfire, req CreateRequest) (string, error) {
	if req.TransportDir == "" {
		return "", fmt.Errorf("TransportDir is required for filesystem transport")
	}

	tr := fs.New(req.TransportDir)

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
func (c *Client) createP2PHTTPCampfire(cf *campfire.Campfire, req CreateRequest) (string, error) {
	if req.TransportDir == "" {
		return "", fmt.Errorf("TransportDir is required for p2p-http transport")
	}
	if req.HTTPTransport == nil {
		return "", fmt.Errorf("HTTPTransport is required for p2p-http transport")
	}

	campfireID := cf.PublicKeyHex()

	// P2P HTTP campfires support both pull and push delivery.
	// Without push, joiners providing an endpoint will be rejected by handleJoin.
	cf.DeliveryModes = []string{campfire.DeliveryModePull, campfire.DeliveryModePush}
	state := cf.State()

	// Write campfire state CBOR file to TransportDir/{campfireID}.cbor
	stateData, err := cfencoding.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("encoding campfire state: %w", err)
	}
	statePath := fmt.Sprintf("%s/%s.cbor", req.TransportDir, campfireID)
	if err := atomicWriteFile(statePath, stateData); err != nil {
		return "", fmt.Errorf("writing campfire state: %w", err)
	}

	// Capture keys for the key provider closure.
	cfPrivKey := make([]byte, len(cf.PrivateKey))
	cfPubKey := make([]byte, len(cf.PublicKey))
	copy(cfPrivKey, cf.PrivateKey)
	copy(cfPubKey, cf.PublicKey)
	cfID := campfireID

	req.HTTPTransport.SetKeyProvider(func(id string) ([]byte, []byte, error) {
		if id == cfID {
			return cfPrivKey, cfPubKey, nil
		}
		return nil, nil, fmt.Errorf("campfire %s not hosted on this node", shortID(id))
	})

	// Register delivery modes provider so the join handler can determine
	// the campfire's supported delivery modes without reading a campfire.cbor
	// at the root of TransportDir (the state file lives at {campfireID}.cbor, not
	// campfire.cbor, in P2P HTTP mode).
	deliveryModes := cf.DeliveryModes
	req.HTTPTransport.SetDeliveryModesProvider(func(id string) []string {
		if id == cfID {
			return deliveryModes
		}
		return nil
	})

	// Register self info on the transport and in the store.
	if req.MyHTTPEndpoint != "" {
		req.HTTPTransport.SetSelfInfo(c.identity.PublicKeyHex(), req.MyHTTPEndpoint)
		if err := c.store.UpsertPeerEndpoint(store.PeerEndpoint{
			CampfireID:    campfireID,
			MemberPubkey:  c.identity.PublicKeyHex(),
			Endpoint:      req.MyHTTPEndpoint,
			ParticipantID: 1,
		}); err != nil {
			return "", fmt.Errorf("registering peer endpoint: %w", err)
		}
	}

	// For threshold>1: run DKG and store creator's share + pending shares.
	if cf.Threshold > 1 {
		if err := c.initThresholdDKG(cf, campfireID, req.HTTPTransport); err != nil {
			return "", fmt.Errorf("initializing threshold DKG: %w", err)
		}
	}

	return req.TransportDir, nil
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

// buildBeaconTransport constructs the beacon.TransportConfig for the given transport type.
func buildBeaconTransport(transportType, transportDir string, req CreateRequest) beacon.TransportConfig {
	switch transportType {
	case "p2p-http":
		config := map[string]string{
			"protocol": "p2p-http",
		}
		if req.MyHTTPEndpoint != "" {
			config["endpoint"] = req.MyHTTPEndpoint
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
