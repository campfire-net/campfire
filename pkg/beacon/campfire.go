package beacon

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/campfire-net/campfire/pkg/store"
)

// BeaconDeclaration is the JSON payload format for routing:beacon messages.
// Fields match the declaration schema in declarations/routing-beacon.json.
// TAINTED fields: endpoint, transport, description, join_protocol.
// VERIFIED fields: campfire_id, timestamp, convention_version, inner_signature, path (threshold=1).
// ADVISORY fields: path (threshold>1) — path is not covered by inner_signature for threshold>1 campfires.
// Missing path is treated as empty (backward compatibility per §3.3).
type BeaconDeclaration struct {
	CampfireID        string   `json:"campfire_id"`
	Endpoint          string   `json:"endpoint"`
	Transport         string   `json:"transport"`
	Description       string   `json:"description"`
	JoinProtocol      string   `json:"join_protocol"`
	Timestamp         int64    `json:"timestamp"`
	ConventionVersion string   `json:"convention_version"`
	InnerSignature    string   `json:"inner_signature"`
	Path              []string `json:"path,omitempty"`
}

// BeaconInnerSignInput is the canonical byte representation signed by the
// campfire key for inner_signature. It is JSON-encoded deterministically —
// callers MUST use MarshalInnerSignInput to produce the signing bytes.
//
// For threshold=1 campfires, Path is included in the signing input (§3.2).
// For threshold>1 campfires, Path is omitted — use MarshalInnerSignInputNoPath.
type BeaconInnerSignInput struct {
	CampfireID        string   `json:"campfire_id"`
	Endpoint          string   `json:"endpoint"`
	Transport         string   `json:"transport"`
	Description       string   `json:"description"`
	JoinProtocol      string   `json:"join_protocol"`
	Timestamp         int64    `json:"timestamp"`
	ConventionVersion string   `json:"convention_version"`
	Path              []string `json:"path,omitempty"`
}

// MarshalInnerSignInput returns the canonical JSON bytes for signing,
// including path in the signing input (threshold=1 behavior per §3.2).
// Fields are in declaration order to ensure deterministic encoding.
// A nil or empty Path is omitted from the output (backward compatible with
// legacy beacons that have no path field).
func MarshalInnerSignInput(d BeaconDeclaration) ([]byte, error) {
	inp := BeaconInnerSignInput{
		CampfireID:        d.CampfireID,
		Endpoint:          d.Endpoint,
		Transport:         d.Transport,
		Description:       d.Description,
		JoinProtocol:      d.JoinProtocol,
		Timestamp:         d.Timestamp,
		ConventionVersion: d.ConventionVersion,
		Path:              d.Path,
	}
	return json.Marshal(inp)
}

// MarshalInnerSignInputNoPath returns the canonical JSON bytes for signing
// without including the path field. Use this for threshold>1 campfires where
// path is advisory and not cryptographically bound (§3.2).
func MarshalInnerSignInputNoPath(d BeaconDeclaration) ([]byte, error) {
	inp := BeaconInnerSignInput{
		CampfireID:        d.CampfireID,
		Endpoint:          d.Endpoint,
		Transport:         d.Transport,
		Description:       d.Description,
		JoinProtocol:      d.JoinProtocol,
		Timestamp:         d.Timestamp,
		ConventionVersion: d.ConventionVersion,
		// Path intentionally omitted for threshold>1 campfires
	}
	return json.Marshal(inp)
}

// SignDeclaration produces a BeaconDeclaration with inner_signature signed by
// the campfire private key. The timestamp is set to the current time.
// path is the ordered list of node_ids this beacon has traversed (§3.1).
// For threshold=1 campfires, path is included in the signing input (§3.2).
// Pass nil or empty path for legacy beacons without path-vector support.
func SignDeclaration(
	campfirePub ed25519.PublicKey,
	campfirePriv ed25519.PrivateKey,
	endpoint string,
	transport string,
	description string,
	joinProtocol string,
	path ...[]string,
) (*BeaconDeclaration, error) {
	d := BeaconDeclaration{
		CampfireID:        fmt.Sprintf("%x", campfirePub),
		Endpoint:          endpoint,
		Transport:         transport,
		Description:       description,
		JoinProtocol:      joinProtocol,
		Timestamp:         time.Now().Unix(),
		ConventionVersion: "0.5.0",
	}
	if len(path) > 0 && len(path[0]) > 0 {
		d.Path = path[0]
	}
	signBytes, err := MarshalInnerSignInput(d)
	if err != nil {
		return nil, fmt.Errorf("marshaling inner sign input: %w", err)
	}
	sig := ed25519.Sign(campfirePriv, signBytes)
	d.InnerSignature = fmt.Sprintf("%x", sig)
	return &d, nil
}

// SignDeclarationThreshold produces a BeaconDeclaration for a threshold>1
// campfire. The path is included in the declaration but NOT in the signing
// input — path is advisory only for threshold>1 campfires (§3.2).
func SignDeclarationThreshold(
	campfirePub ed25519.PublicKey,
	campfirePriv ed25519.PrivateKey,
	endpoint string,
	transport string,
	description string,
	joinProtocol string,
	path []string,
) (*BeaconDeclaration, error) {
	d := BeaconDeclaration{
		CampfireID:        fmt.Sprintf("%x", campfirePub),
		Endpoint:          endpoint,
		Transport:         transport,
		Description:       description,
		JoinProtocol:      joinProtocol,
		Timestamp:         time.Now().Unix(),
		ConventionVersion: "0.5.0",
		Path:              path,
	}
	// Path excluded from signing input for threshold>1 (§3.2).
	signBytes, err := MarshalInnerSignInputNoPath(d)
	if err != nil {
		return nil, fmt.Errorf("marshaling inner sign input: %w", err)
	}
	sig := ed25519.Sign(campfirePriv, signBytes)
	d.InnerSignature = fmt.Sprintf("%x", sig)
	return &d, nil
}

// VerifyDeclaration verifies the inner_signature of a BeaconDeclaration.
// Returns false if the signature is invalid, the campfire_id is malformed,
// or the inner_signature is not a valid hex-encoded ed25519 signature.
//
// For threshold=1 campfires, the path is included in the signing input.
// For threshold>1 campfires, the path is advisory (not in the signature) —
// VerifyDeclaration tries with path first, then without if path is present
// and the first attempt fails (§3.2 backward compatibility).
func VerifyDeclaration(d BeaconDeclaration) bool {
	pubBytes, err := hex.DecodeString(d.CampfireID)
	if err != nil || len(pubBytes) != ed25519.PublicKeySize {
		return false
	}
	sigBytes, err := hex.DecodeString(d.InnerSignature)
	if err != nil {
		return false
	}
	// Try with path included (threshold=1 or no-path beacon).
	signBytes, err := MarshalInnerSignInput(d)
	if err != nil {
		return false
	}
	if ed25519.Verify(pubBytes, signBytes, sigBytes) {
		return true
	}
	// If path is present and first attempt failed, try without path.
	// This handles threshold>1 campfires where path is advisory (§3.2).
	if len(d.Path) > 0 {
		signBytesNoPath, err := MarshalInnerSignInputNoPath(d)
		if err != nil {
			return false
		}
		return ed25519.Verify(pubBytes, signBytesNoPath, sigBytes)
	}
	return false
}

// DeclarationToBeacon converts a BeaconDeclaration to a Beacon.
// The declaration's inner_signature is verified before conversion.
// Returns an error if verification fails.
// The resulting Beacon has:
//   - CampfireID set from the hex-decoded campfire_id
//   - JoinProtocol from join_protocol
//   - Transport.Protocol from transport (no Config — endpoint is in the declaration)
//   - Description from description
//   - Signature set to the hex-decoded inner_signature (not a Beacon CBOR signature)
//   - ReceptionRequirements is empty (not carried in the declaration format)
func DeclarationToBeacon(d BeaconDeclaration) (*Beacon, error) {
	if !VerifyDeclaration(d) {
		return nil, fmt.Errorf("inner_signature verification failed for campfire_id %s", d.CampfireID)
	}
	pubBytes, err := hex.DecodeString(d.CampfireID)
	if err != nil {
		return nil, fmt.Errorf("decoding campfire_id: %w", err)
	}
	sigBytes, err := hex.DecodeString(d.InnerSignature)
	if err != nil {
		return nil, fmt.Errorf("decoding inner_signature: %w", err)
	}
	return &Beacon{
		CampfireID:            pubBytes,
		JoinProtocol:          d.JoinProtocol,
		ReceptionRequirements: []string{},
		Transport: TransportConfig{
			Protocol: d.Transport,
			Config:   map[string]string{"endpoint": d.Endpoint},
		},
		Description: d.Description,
		Signature:   sigBytes,
	}, nil
}

// BeaconToDeclaration converts a Beacon to a BeaconDeclaration suitable for
// posting as a routing:beacon message. The inner_signature field is populated
// by signing with the provided campfire private key. The caller must supply
// the endpoint and transport (from TransportConfig.Config["endpoint"] or
// explicitly) because the Beacon struct stores them differently.
func BeaconToDeclaration(b *Beacon, campfirePriv ed25519.PrivateKey, endpoint string) (*BeaconDeclaration, error) {
	if len(campfirePriv) == 0 {
		return nil, fmt.Errorf("campfire private key required for signing")
	}
	return SignDeclaration(
		ed25519.PublicKey(b.CampfireID),
		campfirePriv,
		endpoint,
		b.Transport.Protocol,
		b.Description,
		b.JoinProtocol,
	)
}

// ScanCampfire reads routing:beacon messages from the store for the given
// campfire ID, verifies each beacon's inner_signature, and returns the
// converted Beacon structs. Messages whose inner_signature fails verification
// are silently skipped. Messages whose payload cannot be parsed are skipped.
//
// This enables in-band beacon discovery: any member of a gateway campfire can
// call ScanCampfire to discover all advertised campfires, without any
// out-of-band file exchange.
func ScanCampfire(s store.MessageStore, campfireID string) ([]Beacon, error) {
	msgs, err := s.ListMessages(campfireID, 0, store.MessageFilter{
		Tags: []string{"routing:beacon"},
	})
	if err != nil {
		return nil, fmt.Errorf("listing routing:beacon messages from campfire %s: %w", campfireID, err)
	}

	var beacons []Beacon
	for _, msg := range msgs {
		var d BeaconDeclaration
		if err := json.Unmarshal(msg.Payload, &d); err != nil {
			continue // skip non-JSON or malformed payloads
		}
		b, err := DeclarationToBeacon(d)
		if err != nil {
			continue // skip beacons with invalid inner_signature
		}
		beacons = append(beacons, *b)
	}
	return beacons, nil
}

// ScanAllMemberships scans routing:beacon messages from every campfire in the
// store. It returns all valid beacons, deduplicated by campfire_id hex.
// This is the bulk form of ScanCampfire used by cf discover.
func ScanAllMemberships(s interface {
	store.MembershipStore
	store.MessageStore
}) ([]Beacon, error) {
	memberships, err := s.ListMemberships()
	if err != nil {
		return nil, fmt.Errorf("listing memberships: %w", err)
	}

	seen := map[string]bool{}
	var beacons []Beacon
	for _, m := range memberships {
		bs, err := ScanCampfire(s, m.CampfireID)
		if err != nil {
			continue // skip campfires we can't read
		}
		for _, b := range bs {
			id := b.CampfireIDHex()
			if !seen[id] {
				seen[id] = true
				beacons = append(beacons, b)
			}
		}
	}
	return beacons, nil
}
