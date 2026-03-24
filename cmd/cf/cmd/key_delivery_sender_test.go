package cmd

// Tests for workspace-17qu.7: key delivery sender verification.
// pollForKeyDelivery must reject campfire:key-delivery messages from senders
// who are not the campfire creator (i.e., whose public key != campfireID).

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	ghtr "github.com/campfire-net/campfire/pkg/transport/github"
)

// ---- minimal fake GitHub server for key-delivery tests ----

type kdFakeServer struct {
	mu       sync.Mutex
	comments []kdComment
	nextID   int
	etag     string
}

type kdComment struct {
	ID        int       `json:"id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

func newKDFakeServer(t *testing.T) (*kdFakeServer, *httptest.Server) {
	t.Helper()
	fs := &kdFakeServer{nextID: 1, etag: `"initial"`}

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) {
		if !hasSuffix(r.URL.Path, "/comments") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			var req struct {
				Body string `json:"body"`
			}
			json.Unmarshal(body, &req) //nolint:errcheck

			fs.mu.Lock()
			id := fs.nextID
			fs.nextID++
			c := kdComment{ID: id, Body: req.Body, CreatedAt: time.Now().UTC()}
			fs.comments = append(fs.comments, c)
			fs.etag = fmt.Sprintf(`"etag-%d"`, id)
			fs.mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(c) //nolint:errcheck
		case http.MethodGet:
			fs.mu.Lock()
			etag := fs.etag
			comments := make([]kdComment, len(fs.comments))
			copy(comments, fs.comments)
			fs.mu.Unlock()

			w.Header().Set("ETag", etag)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(comments) //nolint:errcheck
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	srv := httptest.NewServer(mux)
	return fs, srv
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

func openKDStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "kd.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func newKDTransport(t *testing.T, srv *httptest.Server, s store.Store, campfireID string, issueNum int) *ghtr.Transport {
	t.Helper()
	cfg := ghtr.Config{
		Repo:        "org/relay",
		IssueNumber: issueNum,
		Token:       "tok",
		BaseURL:     srv.URL,
	}
	tr, err := ghtr.New(cfg, s)
	if err != nil {
		t.Fatalf("ghtr.New: %v", err)
	}
	tr.RegisterCampfire(campfireID, issueNum)
	return tr
}

// postKeyDelivery posts a campfire:key-delivery comment signed by senderID
// with a hex-encoded ciphertext payload. Returns the transport used to send.
func postKeyDelivery(t *testing.T, tr *ghtr.Transport, senderID *identity.Identity, campfireID string, payload string) {
	t.Helper()
	msg, err := message.NewMessage(
		senderID.PrivateKey,
		senderID.PublicKey,
		[]byte(payload),
		[]string{"campfire:key-delivery"},
		nil,
	)
	if err != nil {
		t.Fatalf("NewMessage: %v", err)
	}
	if err := tr.Send(campfireID, msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
}

// TestPollForKeyDelivery_RejectsUnauthorizedSender verifies that a
// campfire:key-delivery message from a sender whose public key does not match
// the campfireID is silently ignored by pollForKeyDelivery, even when the
// attacker correctly encrypts a payload to the joiner's public key.
//
// This is the real attack vector: an attacker with issue write access who
// knows the joiner's public key can encrypt a malicious campfire key to the
// joiner and post it. Without sender verification, pollForKeyDelivery would
// return the attacker's key instead of the creator's key.
func TestPollForKeyDelivery_RejectsUnauthorizedSender(t *testing.T) {
	_, srv := newKDFakeServer(t)
	defer srv.Close()

	// Creator: the campfire identity (campfireID == creator.PublicKeyHex()).
	creatorID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generate creator: %v", err)
	}
	campfireID := creatorID.PublicKeyHex()

	// Attacker: a different identity with issue write access.
	attackerID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generate attacker: %v", err)
	}

	// Joiner: the agent requesting to join.
	joinerID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generate joiner: %v", err)
	}

	storeAttacker := openKDStore(t)
	storeJoiner := openKDStore(t)

	const issueNum = 1

	trAttacker := newKDTransport(t, srv, storeAttacker, campfireID, issueNum)
	trJoiner := newKDTransport(t, srv, storeJoiner, campfireID, issueNum)

	// Attacker encrypts a malicious key to the joiner's public key — this is
	// a properly formed payload that the joiner CAN decrypt.
	maliciousKey := []byte("malicious-campfire-private-key!!")
	attackerCiphertext, err := identity.EncryptToEd25519Key(joinerID.PublicKey, maliciousKey)
	if err != nil {
		t.Fatalf("attacker EncryptToEd25519Key: %v", err)
	}
	postKeyDelivery(t, trAttacker, attackerID, campfireID, hex.EncodeToString(attackerCiphertext))

	// pollForKeyDelivery must reject the attacker's message because the sender
	// (attacker) does not match the campfireID (creator).
	// Without the fix this would return maliciousKey, which is wrong.
	_, err = pollForKeyDelivery(trJoiner, campfireID, joinerID)
	if err == nil {
		t.Fatal("pollForKeyDelivery accepted key-delivery from unauthorized sender; expected rejection")
	}
}

// TestPollForKeyDelivery_AcceptsCreatorDelivery verifies that a key-delivery
// message from the campfire creator (sender == campfireID) is accepted.
func TestPollForKeyDelivery_AcceptsCreatorDelivery(t *testing.T) {
	_, srv := newKDFakeServer(t)
	defer srv.Close()

	// Creator: the campfire identity (campfireID == creator.PublicKeyHex()).
	creatorID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generate creator: %v", err)
	}
	campfireID := creatorID.PublicKeyHex()

	// Joiner: the agent requesting to join.
	joinerID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generate joiner: %v", err)
	}

	storeCreator := openKDStore(t)
	storeJoiner := openKDStore(t)

	const issueNum = 1

	trCreator := newKDTransport(t, srv, storeCreator, campfireID, issueNum)
	trJoiner := newKDTransport(t, srv, storeJoiner, campfireID, issueNum)

	// Creator encrypts a fake campfire key to the joiner's public key.
	fakeCampfireKey := []byte("fake-campfire-private-key-bytes!") // 32 bytes
	ciphertext, err := identity.EncryptToEd25519Key(joinerID.PublicKey, fakeCampfireKey)
	if err != nil {
		t.Fatalf("EncryptToEd25519Key: %v", err)
	}
	payload := hex.EncodeToString(ciphertext)

	// Creator posts the key-delivery comment.
	postKeyDelivery(t, trCreator, creatorID, campfireID, payload)

	// pollForKeyDelivery should accept the creator's delivery and return the decrypted key.
	gotKey, err := pollForKeyDelivery(trJoiner, campfireID, joinerID)
	if err != nil {
		t.Fatalf("pollForKeyDelivery rejected creator delivery: %v", err)
	}
	if string(gotKey) != string(fakeCampfireKey) {
		t.Errorf("decrypted key mismatch: got %x, want %x", gotKey, fakeCampfireKey)
	}
}
