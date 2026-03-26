package beacon

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
)

// makeDeclaration creates a signed BeaconDeclaration for testing.
func makeDeclaration(t *testing.T, pub ed25519.PublicKey, priv ed25519.PrivateKey) *BeaconDeclaration {
	t.Helper()
	d, err := SignDeclaration(pub, priv, "http://example.com", "p2p-http", "test campfire", "open")
	if err != nil {
		t.Fatalf("SignDeclaration: %v", err)
	}
	return d
}

// pubHex returns the hex-encoded public key.
func pubHex(pub ed25519.PublicKey) string {
	return fmt.Sprintf("%x", pub)
}

// openTestStore opens a temporary SQLite store and registers campfireID as a membership.
func openTestStore(t *testing.T, campfireID string) store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if campfireID != "" {
		if err := s.AddMembership(store.Membership{
			CampfireID:   campfireID,
			TransportDir: dir,
			JoinProtocol: "open",
			Role:         "member",
			JoinedAt:     1,
		}); err != nil {
			t.Fatalf("AddMembership: %v", err)
		}
	}
	return s
}

// addBeaconMessage posts a routing:beacon message to the store for campfireID.
func addBeaconMessage(t *testing.T, s store.Store, campfireID string, d *BeaconDeclaration, msgID string) {
	t.Helper()
	payload, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	senderPub, senderPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	sig := ed25519.Sign(senderPriv, payload)

	rec := store.MessageRecord{
		ID:          msgID,
		CampfireID:  campfireID,
		Sender:      fmt.Sprintf("%x", senderPub),
		Payload:     payload,
		Tags:        []string{"routing:beacon"},
		Antecedents: []string{},
		Timestamp:   1000,
		Signature:   sig,
		Provenance:  []message.ProvenanceHop{},
		ReceivedAt:  1000,
	}
	added, err := s.AddMessage(rec)
	if err != nil {
		t.Fatalf("AddMessage: %v", err)
	}
	if !added {
		t.Fatalf("AddMessage returned added=false for id %s", msgID)
	}
}

// --- Unit Tests ---

func TestSignAndVerifyDeclaration(t *testing.T) {
	pub, priv := testKeypair(t)
	d := makeDeclaration(t, pub, priv)

	if !VerifyDeclaration(*d) {
		t.Error("VerifyDeclaration should return true for a freshly signed declaration")
	}
}

func TestVerifyDeclaration_TamperedField(t *testing.T) {
	pub, priv := testKeypair(t)
	d := makeDeclaration(t, pub, priv)

	d.Endpoint = "http://attacker.example.com"
	if VerifyDeclaration(*d) {
		t.Error("VerifyDeclaration should return false after tampering with endpoint")
	}
}

func TestVerifyDeclaration_InvalidCampfireID(t *testing.T) {
	pub, priv := testKeypair(t)
	d := makeDeclaration(t, pub, priv)
	d.CampfireID = "not-hex"
	if VerifyDeclaration(*d) {
		t.Error("VerifyDeclaration should return false for invalid campfire_id")
	}
}

func TestVerifyDeclaration_WrongKeySize(t *testing.T) {
	pub, priv := testKeypair(t)
	d := makeDeclaration(t, pub, priv)
	d.CampfireID = "deadbeef" // too short for ed25519 public key
	if VerifyDeclaration(*d) {
		t.Error("VerifyDeclaration should return false for wrong key size")
	}
}

func TestDeclarationToBeacon_Valid(t *testing.T) {
	pub, priv := testKeypair(t)
	d := makeDeclaration(t, pub, priv)

	b, err := DeclarationToBeacon(*d)
	if err != nil {
		t.Fatalf("DeclarationToBeacon: %v", err)
	}
	if b.CampfireIDHex() != d.CampfireID {
		t.Errorf("campfire ID hex = %s, want %s", b.CampfireIDHex(), d.CampfireID)
	}
	if b.JoinProtocol != d.JoinProtocol {
		t.Errorf("JoinProtocol = %s, want %s", b.JoinProtocol, d.JoinProtocol)
	}
	if b.Transport.Protocol != d.Transport {
		t.Errorf("Transport.Protocol = %s, want %s", b.Transport.Protocol, d.Transport)
	}
	if b.Description != d.Description {
		t.Errorf("Description = %s, want %s", b.Description, d.Description)
	}
	if b.Transport.Config["endpoint"] != d.Endpoint {
		t.Errorf("endpoint = %s, want %s", b.Transport.Config["endpoint"], d.Endpoint)
	}
}

func TestDeclarationToBeacon_InvalidSignature(t *testing.T) {
	pub, priv := testKeypair(t)
	d := makeDeclaration(t, pub, priv)

	// Tamper with description to invalidate inner_signature
	d.Description = "tampered"
	_, err := DeclarationToBeacon(*d)
	if err == nil {
		t.Error("DeclarationToBeacon should fail with invalid inner_signature")
	}
}

func TestBeaconToDeclarationRoundtrip(t *testing.T) {
	pub, priv := testKeypair(t)
	b, err := New(pub, priv, "open", []string{}, TransportConfig{
		Protocol: "p2p-http",
		Config:   map[string]string{},
	}, "roundtrip test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	endpoint := "http://relay.example.com"
	d, err := BeaconToDeclaration(b, priv, endpoint)
	if err != nil {
		t.Fatalf("BeaconToDeclaration: %v", err)
	}

	if !VerifyDeclaration(*d) {
		t.Error("declaration should verify after BeaconToDeclaration")
	}
	if d.CampfireID != b.CampfireIDHex() {
		t.Errorf("campfire_id = %s, want %s", d.CampfireID, b.CampfireIDHex())
	}
	if d.Endpoint != endpoint {
		t.Errorf("endpoint = %s, want %s", d.Endpoint, endpoint)
	}
	if d.Transport != b.Transport.Protocol {
		t.Errorf("transport = %s, want %s", d.Transport, b.Transport.Protocol)
	}
	if d.Description != b.Description {
		t.Errorf("description = %s, want %s", d.Description, b.Description)
	}
	if d.ConventionVersion == "" {
		t.Error("convention_version should not be empty")
	}
}

// --- ScanCampfire Unit Tests ---

func TestScanCampfire_Empty(t *testing.T) {
	gwPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	campfireIDHex := pubHex(gwPub)
	s := openTestStore(t, campfireIDHex)

	beacons, err := ScanCampfire(s, campfireIDHex)
	if err != nil {
		t.Fatalf("ScanCampfire: %v", err)
	}
	if len(beacons) != 0 {
		t.Errorf("got %d beacons, want 0", len(beacons))
	}
}

func TestScanCampfire_FindsValidBeacon(t *testing.T) {
	// gateway campfire that hosts the beacon message
	gwPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	campfireIDHex := pubHex(gwPub)
	s := openTestStore(t, campfireIDHex)

	// advertised campfire posts its beacon
	advPub, advPriv := testKeypair(t)
	d := makeDeclaration(t, advPub, advPriv)
	addBeaconMessage(t, s, campfireIDHex, d, "msg-"+campfireIDHex[:8])

	beacons, err := ScanCampfire(s, campfireIDHex)
	if err != nil {
		t.Fatalf("ScanCampfire: %v", err)
	}
	if len(beacons) != 1 {
		t.Fatalf("got %d beacons, want 1", len(beacons))
	}
	if beacons[0].CampfireIDHex() != d.CampfireID {
		t.Errorf("campfire ID = %s, want %s", beacons[0].CampfireIDHex(), d.CampfireID)
	}
}

func TestScanCampfire_RejectsInvalidSignature(t *testing.T) {
	gwPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	campfireIDHex := pubHex(gwPub)
	s := openTestStore(t, campfireIDHex)

	advPub, advPriv := testKeypair(t)
	d := makeDeclaration(t, advPub, advPriv)
	// Tamper after signing — inner_signature no longer valid
	d.Endpoint = "http://attacker.example.com"
	addBeaconMessage(t, s, campfireIDHex, d, "msg-tampered")

	beacons, err := ScanCampfire(s, campfireIDHex)
	if err != nil {
		t.Fatalf("ScanCampfire: %v", err)
	}
	if len(beacons) != 0 {
		t.Errorf("got %d beacons, want 0 (tampered beacon should be rejected)", len(beacons))
	}
}

func TestScanCampfire_SkipsNonJSONPayload(t *testing.T) {
	gwPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	campfireIDHex := pubHex(gwPub)
	s := openTestStore(t, campfireIDHex)

	// Post a routing:beacon-tagged message with non-JSON payload
	senderPub, senderPriv, _ := ed25519.GenerateKey(rand.Reader)
	payload := []byte("not json")
	sig := ed25519.Sign(senderPriv, payload)
	rec := store.MessageRecord{
		ID:          "test-bad-json",
		CampfireID:  campfireIDHex,
		Sender:      pubHex(senderPub),
		Payload:     payload,
		Tags:        []string{"routing:beacon"},
		Antecedents: []string{},
		Timestamp:   2000,
		Signature:   sig,
		Provenance:  []message.ProvenanceHop{},
		ReceivedAt:  2000,
	}
	if _, err := s.AddMessage(rec); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	beacons, err := ScanCampfire(s, campfireIDHex)
	if err != nil {
		t.Fatalf("ScanCampfire: %v", err)
	}
	if len(beacons) != 0 {
		t.Errorf("got %d beacons, want 0 (bad JSON should be skipped)", len(beacons))
	}
}

func TestScanAllMemberships_FindsAcrossMultiple(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Two gateway campfires
	gw1Pub, _, _ := ed25519.GenerateKey(rand.Reader)
	gw2Pub, _, _ := ed25519.GenerateKey(rand.Reader)
	gw1ID := pubHex(gw1Pub)
	gw2ID := pubHex(gw2Pub)

	for _, id := range []string{gw1ID, gw2ID} {
		if err := s.AddMembership(store.Membership{
			CampfireID:   id,
			TransportDir: dir,
			JoinProtocol: "open",
			Role:         "member",
			JoinedAt:     1,
		}); err != nil {
			t.Fatalf("AddMembership: %v", err)
		}
	}

	// Post a different advertised campfire beacon in each gateway
	adv1Pub, adv1Priv := testKeypair(t)
	adv2Pub, adv2Priv := testKeypair(t)
	d1 := makeDeclaration(t, adv1Pub, adv1Priv)
	d2 := makeDeclaration(t, adv2Pub, adv2Priv)
	addBeaconMessage(t, s, gw1ID, d1, "msg-gw1")
	addBeaconMessage(t, s, gw2ID, d2, "msg-gw2")

	beacons, err := ScanAllMemberships(s)
	if err != nil {
		t.Fatalf("ScanAllMemberships: %v", err)
	}
	if len(beacons) != 2 {
		t.Errorf("got %d beacons, want 2", len(beacons))
	}
}

func TestScanAllMemberships_DeduplicatesSameCampfire(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Two gateway campfires
	gw1Pub, _, _ := ed25519.GenerateKey(rand.Reader)
	gw2Pub, _, _ := ed25519.GenerateKey(rand.Reader)
	gw1ID := pubHex(gw1Pub)
	gw2ID := pubHex(gw2Pub)

	for _, id := range []string{gw1ID, gw2ID} {
		if err := s.AddMembership(store.Membership{
			CampfireID:   id,
			TransportDir: dir,
			JoinProtocol: "open",
			Role:         "member",
			JoinedAt:     1,
		}); err != nil {
			t.Fatalf("AddMembership: %v", err)
		}
	}

	// Same advertised campfire, posted in both gateways (different message IDs/endpoints)
	advPub, advPriv := testKeypair(t)
	d1 := makeDeclaration(t, advPub, advPriv)
	d2, err := SignDeclaration(advPub, advPriv, "http://other.example.com", "p2p-http", "test campfire", "open")
	if err != nil {
		t.Fatalf("SignDeclaration: %v", err)
	}

	addBeaconMessage(t, s, gw1ID, d1, "msg-dup-gw1")
	addBeaconMessage(t, s, gw2ID, d2, "msg-dup-gw2")

	beacons, err := ScanAllMemberships(s)
	if err != nil {
		t.Fatalf("ScanAllMemberships: %v", err)
	}
	// Same campfire_id in both gateways → deduplicated to 1
	if len(beacons) != 1 {
		t.Errorf("got %d beacons after dedup, want 1", len(beacons))
	}
}

// --- Path Vector Tests (§3 of peering-pathvector-amendment) ---

// TestBeaconDeclaration_PathSerializesDeserializes verifies that the Path field
// round-trips through JSON correctly.
func TestBeaconDeclaration_PathSerializesDeserializes(t *testing.T) {
	pub, priv := testKeypair(t)
	path := []string{"node-aaa", "node-bbb", "node-ccc"}
	d, err := SignDeclaration(pub, priv, "http://example.com", "p2p-http", "test", "open", path)
	if err != nil {
		t.Fatalf("SignDeclaration with path: %v", err)
	}
	if len(d.Path) != 3 {
		t.Fatalf("Path length = %d, want 3", len(d.Path))
	}
	if d.Path[0] != "node-aaa" || d.Path[1] != "node-bbb" || d.Path[2] != "node-ccc" {
		t.Errorf("Path = %v, want [node-aaa node-bbb node-ccc]", d.Path)
	}

	// Round-trip through JSON.
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var d2 BeaconDeclaration
	if err := json.Unmarshal(raw, &d2); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if len(d2.Path) != 3 {
		t.Fatalf("after round-trip, Path length = %d, want 3", len(d2.Path))
	}
	for i, want := range path {
		if d2.Path[i] != want {
			t.Errorf("Path[%d] = %s, want %s", i, d2.Path[i], want)
		}
	}
}

// TestMarshalInnerSignInput_IncludesPath verifies that MarshalInnerSignInput
// includes the path field in the signing input for threshold=1 campfires (§3.2).
func TestMarshalInnerSignInput_IncludesPath(t *testing.T) {
	pub, priv := testKeypair(t)
	path := []string{"node-aaa", "node-bbb"}
	d, err := SignDeclaration(pub, priv, "http://example.com", "p2p-http", "test", "open", path)
	if err != nil {
		t.Fatalf("SignDeclaration: %v", err)
	}

	// Verify that the signature is valid (path included in signing input).
	if !VerifyDeclaration(*d) {
		t.Error("VerifyDeclaration should return true for a declaration with path (threshold=1)")
	}

	// Verify that tampering with the path invalidates the signature.
	d.Path = []string{"node-aaa", "node-bbb", "node-evil"}
	if VerifyDeclaration(*d) {
		t.Error("VerifyDeclaration should return false after path is tampered (threshold=1)")
	}
}

// TestMarshalInnerSignInputNoPath_ExcludesPath verifies that threshold>1
// campfires sign without path, and that path can be changed freely (§3.2).
func TestMarshalInnerSignInputNoPath_ExcludesPath(t *testing.T) {
	pub, priv := testKeypair(t)
	path := []string{"node-aaa", "node-bbb"}
	d, err := SignDeclarationThreshold(pub, priv, "http://example.com", "p2p-http", "test", "open", path)
	if err != nil {
		t.Fatalf("SignDeclarationThreshold: %v", err)
	}

	// Verify that the signature is valid despite the path being present.
	if !VerifyDeclaration(*d) {
		t.Error("VerifyDeclaration should return true for threshold>1 declaration")
	}

	// Verify that the path can be updated without invalidating the signature
	// (path is advisory for threshold>1).
	d.Path = append(d.Path, "node-ccc")
	if !VerifyDeclaration(*d) {
		t.Error("VerifyDeclaration should still return true after path update (threshold>1, path is advisory)")
	}
}

// TestMissingPath_TreatedAsEmpty verifies that a beacon without a path field
// is valid and that Path is nil/empty (backward compatibility §3.3).
func TestMissingPath_TreatedAsEmpty(t *testing.T) {
	pub, priv := testKeypair(t)
	// Sign without a path (legacy beacon).
	d, err := SignDeclaration(pub, priv, "http://example.com", "p2p-http", "test", "open")
	if err != nil {
		t.Fatalf("SignDeclaration (no path): %v", err)
	}
	if len(d.Path) != 0 {
		t.Errorf("Path should be empty for a legacy beacon, got %v", d.Path)
	}

	// Marshal to JSON — path should be omitted (omitempty).
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if _, ok := m["path"]; ok {
		t.Error("path field should be omitted from JSON when empty (omitempty)")
	}

	// Parse a beacon JSON without a path field — Path should be nil.
	noPathJSON := []byte(`{"campfire_id":"` + d.CampfireID + `","endpoint":"http://example.com","transport":"p2p-http","description":"test","join_protocol":"open","timestamp":` + fmt.Sprintf("%d", d.Timestamp) + `,"convention_version":"0.5.0","inner_signature":"` + d.InnerSignature + `"}`)
	var d2 BeaconDeclaration
	if err := json.Unmarshal(noPathJSON, &d2); err != nil {
		t.Fatalf("json.Unmarshal no-path JSON: %v", err)
	}
	if d2.Path != nil {
		t.Errorf("Path should be nil when missing from JSON, got %v", d2.Path)
	}

	// Verify that a no-path beacon still verifies correctly.
	if !VerifyDeclaration(d2) {
		t.Error("VerifyDeclaration should return true for a no-path (legacy) beacon")
	}
}
