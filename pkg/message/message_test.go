package message

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

func testKeypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error: %v", err)
	}
	return pub, priv
}

func TestNewMessageAndVerify(t *testing.T) {
	pub, priv := testKeypair(t)
	msg, err := NewMessage(priv, pub, []byte("hello"), []string{"test"}, nil)
	if err != nil {
		t.Fatalf("NewMessage() error: %v", err)
	}
	if !msg.VerifySignature() {
		t.Error("message signature should verify")
	}
	if msg.ID == "" {
		t.Error("message ID should not be empty")
	}
	if msg.SenderHex() == "" {
		t.Error("sender hex should not be empty")
	}
	if msg.Antecedents == nil {
		t.Error("antecedents should not be nil")
	}
}

func TestVerifyTamperedMessage(t *testing.T) {
	pub, priv := testKeypair(t)
	msg, _ := NewMessage(priv, pub, []byte("hello"), []string{"test"}, nil)

	msg.Payload = []byte("tampered")
	if msg.VerifySignature() {
		t.Error("tampered message should not verify")
	}
}

func TestAntecedentsInSignature(t *testing.T) {
	pub, priv := testKeypair(t)

	// Message with antecedents
	msg, _ := NewMessage(priv, pub, []byte("hello"), []string{"test"}, []string{"msg-1", "msg-2"})
	if !msg.VerifySignature() {
		t.Error("message with antecedents should verify")
	}
	if len(msg.Antecedents) != 2 {
		t.Errorf("antecedents length = %d, want 2", len(msg.Antecedents))
	}

	// Tamper with antecedents
	msg.Antecedents = []string{"msg-1", "msg-3"}
	if msg.VerifySignature() {
		t.Error("tampered antecedents should not verify")
	}
}

func TestAddHopAndVerify(t *testing.T) {
	senderPub, senderPriv := testKeypair(t)
	cfPub, cfPriv := testKeypair(t)

	msg, _ := NewMessage(senderPriv, senderPub, []byte("hello"), []string{"test"}, nil)
	err := msg.AddHop(cfPriv, cfPub, []byte("fakehash"), 2, "open", []string{"test"})
	if err != nil {
		t.Fatalf("AddHop() error: %v", err)
	}
	if len(msg.Provenance) != 1 {
		t.Fatalf("provenance length = %d, want 1", len(msg.Provenance))
	}
	if !VerifyHop(msg.ID, msg.Provenance[0]) {
		t.Error("hop signature should verify")
	}
}

func TestVerifyTamperedHop(t *testing.T) {
	senderPub, senderPriv := testKeypair(t)
	cfPub, cfPriv := testKeypair(t)

	msg, _ := NewMessage(senderPriv, senderPub, []byte("hello"), nil, nil)
	msg.AddHop(cfPriv, cfPub, []byte("hash"), 1, "open", nil)

	msg.Provenance[0].MemberCount = 999
	if VerifyHop(msg.ID, msg.Provenance[0]) {
		t.Error("tampered hop should not verify")
	}
}

func TestNilTags(t *testing.T) {
	pub, priv := testKeypair(t)
	msg, _ := NewMessage(priv, pub, []byte("hello"), nil, nil)
	if msg.Tags == nil {
		t.Error("tags should not be nil")
	}
	if !msg.VerifySignature() {
		t.Error("nil-tags message should verify")
	}
}

func TestVerifyMessageSignatureFromStored(t *testing.T) {
	pub, priv := testKeypair(t)
	msg, _ := NewMessage(priv, pub, []byte("hello"), []string{"test", "foo"}, []string{"ant-1"})

	senderHex := msg.SenderHex()
	tagsJSON := `["test","foo"]`
	antJSON := `["ant-1"]`

	if !VerifyMessageSignature(msg.ID, msg.Payload, tagsJSON, antJSON, msg.Timestamp, senderHex, msg.Signature) {
		t.Error("stored-form signature should verify")
	}

	// Tamper payload
	if VerifyMessageSignature(msg.ID, []byte("wrong"), tagsJSON, antJSON, msg.Timestamp, senderHex, msg.Signature) {
		t.Error("tampered stored-form should not verify")
	}

	// Tamper antecedents
	if VerifyMessageSignature(msg.ID, msg.Payload, tagsJSON, `["ant-2"]`, msg.Timestamp, senderHex, msg.Signature) {
		t.Error("tampered antecedents stored-form should not verify")
	}
}
