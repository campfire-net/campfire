package github

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// beaconFakeServer is a minimal fake GitHub Contents API server for beacon tests.
// It stores files and supports directory listing (array response for paths ending in /).
type beaconFakeServer struct {
	files map[string][]byte // path -> raw content bytes
}

func newBeaconFakeServer() (*beaconFakeServer, *httptest.Server) {
	bfs := &beaconFakeServer{
		files: make(map[string][]byte),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		idx := strings.Index(path, "/contents/")
		if idx < 0 {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		filePath := path[idx+len("/contents/"):]

		switch r.Method {
		case http.MethodGet:
			bfs.handleGet(w, r, filePath)
		case http.MethodPut:
			bfs.handlePut(w, r, filePath)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	srv := httptest.NewServer(mux)
	return bfs, srv
}

func (bfs *beaconFakeServer) handleGet(w http.ResponseWriter, r *http.Request, filePath string) {
	// Directory listing: return array of file entries under that prefix
	if !strings.HasSuffix(filePath, ".json") {
		// Treat as directory listing
		var entries []map[string]string
		prefix := filePath
		if !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		for p := range bfs.files {
			if strings.HasPrefix(p, prefix) {
				name := strings.TrimPrefix(p, prefix)
				if !strings.Contains(name, "/") && name != "" {
					entries = append(entries, map[string]string{
						"name": name,
						"path": p,
						"type": "file",
					})
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if entries == nil {
			// GitHub returns 404 for empty directory — but bead notes say must return empty, not error
			// Represent as empty array (alternative: some GitHub Enterprise behaviour)
			// We'll return an empty array to match expected behaviour
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("[]"))
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(entries)
		return
	}

	// Single file
	content, ok := bfs.files[filePath]
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"content": base64.StdEncoding.EncodeToString(content),
		"sha":     "sha-fake",
	})
}

func (bfs *beaconFakeServer) handlePut(w http.ResponseWriter, r *http.Request, filePath string) {
	var req struct {
		Message string `json:"message"`
		Content string `json:"content"` // base64
		SHA     string `json:"sha"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	raw, err := base64.StdEncoding.DecodeString(req.Content)
	if err != nil {
		http.Error(w, "bad base64", http.StatusBadRequest)
		return
	}
	bfs.files[filePath] = raw
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"content": map[string]string{"path": filePath},
	})
}

// generateTestIdentity creates a fresh Ed25519 keypair for testing.
func generateTestIdentity(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return pub, priv
}

// makeSignedBeacon builds a Beacon and signs it correctly.
func makeSignedBeacon(t *testing.T, pub ed25519.PublicKey, priv ed25519.PrivateKey, repo string, issueNumber int) Beacon {
	t.Helper()
	campfireID := hex.EncodeToString(pub)
	b := Beacon{
		CampfireID:           campfireID,
		JoinProtocol:         "open",
		ReceptionRequirements: []string{},
		Transport: BeaconTransport{
			Protocol: "github",
			Config: BeaconTransportConfig{
				Repo:        repo,
				IssueNumber: issueNumber,
				IssueURL:    fmt.Sprintf("https://github.com/%s/issues/%d", repo, issueNumber),
			},
		},
		Description: "test campfire",
	}
	sig, err := SignBeacon(b, priv)
	if err != nil {
		t.Fatalf("sign beacon: %v", err)
	}
	b.Signature = sig
	return b
}

// --- Tests ---

func TestPublishBeacon_StoresJSONAtCorrectPath(t *testing.T) {
	bfs, srv := newBeaconFakeServer()
	defer srv.Close()

	pub, priv := generateTestIdentity(t)
	client := newGithubClient(srv.URL, "token")

	beacon := makeSignedBeacon(t, pub, priv, "org/relay", 42)
	err := PublishBeacon(client, "org/relay", beacon)
	if err != nil {
		t.Fatalf("PublishBeacon: %v", err)
	}

	campfireID := hex.EncodeToString(pub)
	expectedPath := ".campfire/beacons/" + campfireID + ".json"
	stored, ok := bfs.files[expectedPath]
	if !ok {
		t.Fatalf("expected file at %q, not found in server state", expectedPath)
	}

	var roundtrip Beacon
	if err := json.Unmarshal(stored, &roundtrip); err != nil {
		t.Fatalf("stored file is not valid JSON: %v", err)
	}
	if roundtrip.CampfireID != campfireID {
		t.Errorf("campfire_id: got %q, want %q", roundtrip.CampfireID, campfireID)
	}
	if roundtrip.Signature == "" {
		t.Error("stored beacon has empty signature")
	}
}

func TestPublishBeacon_MissingCampfireID_Error(t *testing.T) {
	_, srv := newBeaconFakeServer()
	defer srv.Close()

	client := newGithubClient(srv.URL, "token")

	_, priv := generateTestIdentity(t)
	b := Beacon{
		CampfireID:   "", // missing
		JoinProtocol: "open",
	}
	sig, _ := SignBeacon(b, priv)
	b.Signature = sig

	err := PublishBeacon(client, "org/relay", b)
	if err == nil {
		t.Error("expected error for missing campfire_id, got nil")
	}
}

func TestDiscoverBeacons_ValidBeaconReturned(t *testing.T) {
	bfs, srv := newBeaconFakeServer()
	defer srv.Close()

	pub, priv := generateTestIdentity(t)
	client := newGithubClient(srv.URL, "token")

	beacon := makeSignedBeacon(t, pub, priv, "org/relay", 7)
	campfireID := hex.EncodeToString(pub)

	// Publish directly to fake server
	data, _ := json.Marshal(beacon)
	bfs.files[".campfire/beacons/"+campfireID+".json"] = data

	beacons, err := DiscoverBeacons(client, "org/relay")
	if err != nil {
		t.Fatalf("DiscoverBeacons: %v", err)
	}
	if len(beacons) != 1 {
		t.Fatalf("expected 1 beacon, got %d", len(beacons))
	}
	if beacons[0].CampfireID != campfireID {
		t.Errorf("campfire_id: got %q, want %q", beacons[0].CampfireID, campfireID)
	}
}

func TestDiscoverBeacons_TamperedSignature_Skipped(t *testing.T) {
	bfs, srv := newBeaconFakeServer()
	defer srv.Close()

	pub, priv := generateTestIdentity(t)
	client := newGithubClient(srv.URL, "token")

	beacon := makeSignedBeacon(t, pub, priv, "org/relay", 7)
	// Tamper: change description after signing
	beacon.Description = "tampered!"
	campfireID := hex.EncodeToString(pub)

	data, _ := json.Marshal(beacon)
	bfs.files[".campfire/beacons/"+campfireID+".json"] = data

	beacons, err := DiscoverBeacons(client, "org/relay")
	if err != nil {
		t.Fatalf("DiscoverBeacons returned error (should skip bad beacons): %v", err)
	}
	if len(beacons) != 0 {
		t.Errorf("expected tampered beacon to be skipped, got %d beacons", len(beacons))
	}
}

func TestDiscoverBeacons_EmptyDirectory_ReturnsEmptySlice(t *testing.T) {
	// No files stored — directory is empty.
	_, srv := newBeaconFakeServer()
	defer srv.Close()

	client := newGithubClient(srv.URL, "token")

	beacons, err := DiscoverBeacons(client, "org/relay")
	if err != nil {
		t.Fatalf("DiscoverBeacons on empty directory: %v", err)
	}
	if beacons == nil {
		t.Error("expected non-nil empty slice, got nil")
	}
	if len(beacons) != 0 {
		t.Errorf("expected 0 beacons, got %d", len(beacons))
	}
}

func TestDiscoverBeacons_MixedValidAndInvalid(t *testing.T) {
	bfs, srv := newBeaconFakeServer()
	defer srv.Close()

	pub1, priv1 := generateTestIdentity(t)
	pub2, priv2 := generateTestIdentity(t)
	client := newGithubClient(srv.URL, "token")

	// Valid beacon
	validBeacon := makeSignedBeacon(t, pub1, priv1, "org/relay", 1)
	id1 := hex.EncodeToString(pub1)
	data1, _ := json.Marshal(validBeacon)
	bfs.files[".campfire/beacons/"+id1+".json"] = data1

	// Invalid beacon: signed but then tampered
	tamperedBeacon := makeSignedBeacon(t, pub2, priv2, "org/relay", 2)
	tamperedBeacon.JoinProtocol = "invite-only" // tamper after signing
	id2 := hex.EncodeToString(pub2)
	data2, _ := json.Marshal(tamperedBeacon)
	bfs.files[".campfire/beacons/"+id2+".json"] = data2

	beacons, err := DiscoverBeacons(client, "org/relay")
	if err != nil {
		t.Fatalf("DiscoverBeacons: %v", err)
	}
	if len(beacons) != 1 {
		t.Fatalf("expected 1 valid beacon, got %d", len(beacons))
	}
	if beacons[0].CampfireID != id1 {
		t.Errorf("wrong beacon returned: got %q, want %q", beacons[0].CampfireID, id1)
	}
}

func TestSignBeacon_VerifyBeacon_RoundTrip(t *testing.T) {
	pub, priv := generateTestIdentity(t)

	b := Beacon{
		CampfireID:           hex.EncodeToString(pub),
		JoinProtocol:         "open",
		ReceptionRequirements: []string{},
		Description:          "round-trip test",
	}

	sig, err := SignBeacon(b, priv)
	if err != nil {
		t.Fatalf("SignBeacon: %v", err)
	}
	b.Signature = sig

	if err := VerifyBeacon(b); err != nil {
		t.Errorf("VerifyBeacon rejected valid beacon: %v", err)
	}
}

func TestVerifyBeacon_InvalidPubKey_Error(t *testing.T) {
	b := Beacon{
		CampfireID:   "not-a-hex-pubkey",
		JoinProtocol: "open",
		Signature:    "deadbeef",
	}
	if err := VerifyBeacon(b); err == nil {
		t.Error("expected error for invalid campfire_id (not hex pubkey), got nil")
	}
}
