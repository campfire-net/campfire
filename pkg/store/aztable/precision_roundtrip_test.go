//go:build azurite

package aztable_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/cf-protocol/message"
	"github.com/campfire-net/campfire/cf-protocol/store"
)

// TestNanoTimestampRoundtrip is a regression test for campfireagent-ea1.
//
// Bug: Azure Table Storage entities written without an explicit
// "@odata.type": "Edm.Int64" annotation are stored as Edm.Int32 (when small)
// or Edm.Double (when too large to fit). Nanosecond timestamps (≥ 2^31) thus
// round-trip through float64, losing the lowest 1-3 decimal digits because
// float64 has a 53-bit mantissa.
//
// Manifestation: a message Timestamp signed at sender as 1778034594549990354
// is stored on the relay and retrieved by a peer as 1778034594549990400. The
// MessageSignInput timestamp field then disagrees by 46 ns and the Ed25519
// signature verification fails. Cross-relay reads return empty.
//
// Fix: AddMessage emits "MsgTimestamp@odata.type": "Edm.Int64" (and the same
// for ReceivedAt) so Azure preserves the value exactly. This test asserts
// that a sub-microsecond-precision int64 timestamp survives a write+read.
func TestNanoTimestampRoundtrip(t *testing.T) {
	s := newTestStore(t)
	cfID := fmt.Sprintf("cf-precision-%d", time.Now().UnixNano())

	mem := store.Membership{
		CampfireID:   cfID,
		TransportDir: "/tmp",
		JoinProtocol: "direct",
		Role:         "full",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}
	if err := s.AddMembership(mem); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	// A nanosecond timestamp that is NOT representable exactly as float64.
	// 1778034594549990354 has lowest bit-pattern that float64 cannot hold;
	// converting to float64 and back yields 1778034594549990400 (delta +46).
	const wantTS int64 = 1778034594549990354

	rec := store.MessageRecord{
		ID:          fmt.Sprintf("nano-%d", wantTS),
		CampfireID:  cfID,
		Sender:      "aabbccdd",
		Payload:     []byte(`{"hello":"world"}`),
		Tags:        []string{"status"},
		Timestamp:   wantTS,
		ReceivedAt:  wantTS,
		Signature:   []byte("sig"),
		Antecedents: []string{},
		Provenance:  []message.ProvenanceHop{},
	}
	if _, err := s.AddMessage(rec); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	// GetMessage path
	got, err := s.GetMessage(rec.ID)
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if got == nil {
		t.Fatal("GetMessage returned nil")
	}
	if got.Timestamp != wantTS {
		t.Errorf("GetMessage Timestamp: got %d, want %d (delta=%d) — float64 precision loss",
			got.Timestamp, wantTS, got.Timestamp-wantTS)
	}
	if got.ReceivedAt != wantTS {
		t.Errorf("GetMessage ReceivedAt: got %d, want %d (delta=%d) — float64 precision loss",
			got.ReceivedAt, wantTS, got.ReceivedAt-wantTS)
	}

	// ListMessages path (the path the relay uses for /sync responses)
	msgs, err := s.ListMessages(cfID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	var found *store.MessageRecord
	for i := range msgs {
		if msgs[i].ID == rec.ID {
			found = &msgs[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("ListMessages: did not return %s", rec.ID)
	}
	if found.Timestamp != wantTS {
		t.Errorf("ListMessages Timestamp: got %d, want %d (delta=%d) — float64 precision loss",
			found.Timestamp, wantTS, found.Timestamp-wantTS)
	}
}

// TestSignedMessageRoundtripVerifies is the cross-relay regression test for
// campfireagent-ea1: Agent A signs a message, the relay persists it via aztable,
// Agent B reads it back via ListMessages, and the signature must verify.
//
// Before the fix (pkg/store/aztable annotateInt64), the timestamp drifted
// during the Azure Tables JSON round trip (Edm.Double truncation), so the
// MessageSignInput timestamp on read disagreed with what was signed and
// VerifySignature returned false. The synced message was then silently
// dropped by the daemon's read path (cf-protocol/protocol/read.go
// readFromHTTPPeers VerifySignature gate), making cross-relay reads return
// empty.
func TestSignedMessageRoundtripVerifies(t *testing.T) {
	s := newTestStore(t)
	cfID := fmt.Sprintf("cf-sign-%d", time.Now().UnixNano())

	mem := store.Membership{
		CampfireID:   cfID,
		TransportDir: "/tmp",
		JoinProtocol: "direct",
		Role:         "full",
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}
	if err := s.AddMembership(mem); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}

	// Sender identity (Agent A).
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}
	signer := message.MustNewEd25519Signer(id.PrivateKey, id.PublicKey)

	// Build, sign, and persist many messages with rapid-fire monotonic
	// nanosecond timestamps. The probabilistic "is the lowest decimal digit
	// preserved" test only catches the bug when at least one timestamp
	// happens to be unrepresentable as float64. Loop a few hundred times so
	// the test fails on the buggy path with overwhelming probability.
	const N = 500
	type signedMsg struct {
		ID  string
		Msg *message.Message
	}
	signed := make([]signedMsg, 0, N)
	for i := 0; i < N; i++ {
		msg, err := message.NewMessage(signer, []byte(fmt.Sprintf("payload-%d", i)),
			[]string{"request"}, nil)
		if err != nil {
			t.Fatalf("NewMessage[%d]: %v", i, err)
		}
		// Sanity: signature must verify in-memory before storage.
		if !msg.VerifySignature() {
			t.Fatalf("[%d] in-memory VerifySignature failed", i)
		}
		rec := store.MessageRecordFromMessage(cfID, msg, time.Now().UnixNano())
		if _, err := s.AddMessage(rec); err != nil {
			t.Fatalf("AddMessage[%d]: %v", i, err)
		}
		signed = append(signed, signedMsg{ID: msg.ID, Msg: msg})
	}

	// Read back via ListMessages — the path the relay's GET /sync uses.
	got, err := s.ListMessages(cfID, 0)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	gotByID := make(map[string]store.MessageRecord, len(got))
	for _, r := range got {
		gotByID[r.ID] = r
	}

	verifyFails := 0
	tsDrifts := 0
	for _, s := range signed {
		rec, ok := gotByID[s.ID]
		if !ok {
			t.Fatalf("ListMessages did not return %s", s.ID)
		}
		if rec.Timestamp != s.Msg.Timestamp {
			tsDrifts++
		}
		// Reconstruct the wire-form Message and verify its signature.
		wire := message.Message{
			ID:          rec.ID,
			Sender:      s.Msg.Sender, // hex→bytes happens in handler_message.recordToMessage; here we reuse the original to focus on Timestamp drift
			Payload:     rec.Payload,
			Tags:        rec.Tags,
			Antecedents: rec.Antecedents,
			Timestamp:   rec.Timestamp,
			Signature:   rec.Signature,
		}
		if !wire.VerifySignature() {
			verifyFails++
		}
	}
	if tsDrifts > 0 {
		t.Errorf("Timestamp drift on %d/%d messages — Edm.Int64 annotation missing or ineffective", tsDrifts, N)
	}
	if verifyFails > 0 {
		t.Errorf("VerifySignature failed on %d/%d messages — cross-relay reads would silently drop these", verifyFails, N)
	}
}
