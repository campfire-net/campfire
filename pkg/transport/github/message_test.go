package github

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"github.com/3dl-dev/campfire/pkg/message"
)

func makeTestMessage(t *testing.T) *message.Message {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	msg, err := message.NewMessage(priv, pub, []byte("hello campfire"), []string{"test"}, nil)
	if err != nil {
		t.Fatalf("creating message: %v", err)
	}
	return msg
}

func TestRoundTrip(t *testing.T) {
	orig := makeTestMessage(t)

	encoded, err := EncodeComment(orig)
	if err != nil {
		t.Fatalf("EncodeComment: %v", err)
	}

	decoded, err := DecodeComment(encoded)
	if err != nil {
		t.Fatalf("DecodeComment: %v", err)
	}

	if decoded.ID != orig.ID {
		t.Errorf("ID mismatch: got %q, want %q", decoded.ID, orig.ID)
	}
	if string(decoded.Payload) != string(orig.Payload) {
		t.Errorf("Payload mismatch: got %q, want %q", decoded.Payload, orig.Payload)
	}
	if len(decoded.Tags) != len(orig.Tags) || decoded.Tags[0] != orig.Tags[0] {
		t.Errorf("Tags mismatch: got %v, want %v", decoded.Tags, orig.Tags)
	}
	if string(decoded.Sender) != string(orig.Sender) {
		t.Errorf("Sender mismatch")
	}
	if decoded.Timestamp != orig.Timestamp {
		t.Errorf("Timestamp mismatch: got %d, want %d", decoded.Timestamp, orig.Timestamp)
	}
	if string(decoded.Signature) != string(orig.Signature) {
		t.Errorf("Signature mismatch")
	}
}

func TestEncodedCommentHasPrefix(t *testing.T) {
	msg := makeTestMessage(t)
	encoded, err := EncodeComment(msg)
	if err != nil {
		t.Fatalf("EncodeComment: %v", err)
	}
	if !strings.HasPrefix(encoded, commentPrefix) {
		t.Errorf("encoded comment missing prefix %q: got %q", commentPrefix, encoded[:min(len(encoded), 40)])
	}
}

func TestDecodeNonCampfireComment(t *testing.T) {
	humanComment := "This looks good to me! Great work on the implementation."
	_, err := DecodeComment(humanComment)
	if !errors.Is(err, ErrNotCampfireMessage) {
		t.Errorf("expected ErrNotCampfireMessage, got %v", err)
	}
}

func TestDecodeTruncatedBase64(t *testing.T) {
	// Valid prefix but truncated/invalid base64
	truncated := commentPrefix + "YWJjZGVmZ2g!!!invalid!!!"
	_, err := DecodeComment(truncated)
	if err == nil {
		t.Error("expected error for truncated base64, got nil")
	}
	if errors.Is(err, ErrNotCampfireMessage) {
		t.Error("expected base64 decode error, not ErrNotCampfireMessage")
	}
}

func TestDecodeEmptyBody(t *testing.T) {
	_, err := DecodeComment("")
	if !errors.Is(err, ErrNotCampfireMessage) {
		t.Errorf("expected ErrNotCampfireMessage for empty body, got %v", err)
	}
}

func TestDecodeOnlyPrefix(t *testing.T) {
	// Prefix present but no base64 payload — valid base64 but empty CBOR
	// The prefix alone decodes to empty bytes, which should fail CBOR unmarshal
	_, err := DecodeComment(commentPrefix)
	if err == nil {
		t.Error("expected error for prefix-only comment, got nil")
	}
	// Should NOT be ErrNotCampfireMessage since prefix is present
	if errors.Is(err, ErrNotCampfireMessage) {
		t.Error("expected CBOR decode error, not ErrNotCampfireMessage")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
