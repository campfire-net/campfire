package main

// Tests for the readAll → IncludeCompacted fix (Wave 0, PR #147).
//
// The behavioral boundary under test is at the MCP layer: handleRead must
// honour the all=true/false flag by setting IncludeCompacted on the
// protocol.ReadRequest. When all=false (default), messages superseded by a
// campfire:compact event must be excluded from the response. When all=true
// they must be included.
//
// Bead: campfire-agent-mf3

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/store"
	"github.com/google/uuid"
)

// TestReadAll_CompactedToggle exercises the MCP read handler's all=true/false
// flag against a campfire that contains compacted messages.
//
// Steps:
//  1. Init + create campfire + send 2 messages via MCP.
//  2. Write a campfire:compact message directly into the store, superseding
//     the first message.
//  3. all=false → compacted message absent, non-compacted message present.
//  4. all=true  → both messages present (compacted + non-compacted).
func TestReadAll_CompactedToggle(t *testing.T) {
	srv, st := newTestServerWithStore(t)
	doInit(t, srv)

	// --- 1. Create campfire ---
	createResp := srv.dispatch(makeReq("tools/call", `{"name":"campfire_create","arguments":{}}`))
	createFields := extractCreateResult(t, createResp)
	campfireID, _ := createFields["campfire_id"].(string)
	if campfireID == "" {
		t.Fatal("missing campfire_id in campfire_create response")
	}

	// --- Send two messages ---
	payloadA := "compacted-message-A"
	payloadB := "live-message-B"

	for _, msg := range []string{payloadA, payloadB} {
		args, _ := json.Marshal(map[string]interface{}{
			"campfire_id": campfireID,
			"message":     msg,
		})
		resp := srv.dispatch(makeReq("tools/call",
			`{"name":"campfire_send","arguments":`+string(args)+`}`))
		if resp.Error != nil {
			t.Fatalf("campfire_send(%q): code=%d msg=%s", msg, resp.Error.Code, resp.Error.Message)
		}
		time.Sleep(time.Millisecond) // ensure distinct timestamps
	}

	// --- 2. Retrieve the two message IDs from the store ---
	allMsgs, err := st.ListMessages(campfireID, 0)
	if err != nil {
		t.Fatalf("listing messages: %v", err)
	}
	// Filter to the two user messages (exclude any audit/system messages).
	var msgAID string
	for _, m := range allMsgs {
		if string(m.Payload) == payloadA {
			msgAID = m.ID
			break
		}
	}
	if msgAID == "" {
		t.Fatalf("could not find message with payload %q in store", payloadA)
	}

	// --- Write a campfire:compact event that supersedes msgA ---
	compactionPayload, _ := json.Marshal(store.CompactionPayload{
		Supersedes:     []string{msgAID},
		Summary:        []byte("test compaction"),
		Retention:      "archive",
		CheckpointHash: "test-hash",
	})
	compactRec := store.MessageRecord{
		ID:          uuid.New().String(),
		CampfireID:  campfireID,
		Sender:      "test-compactor",
		Payload:     compactionPayload,
		Tags:        []string{"campfire:compact"},
		Antecedents: []string{msgAID},
		Timestamp:   store.NowNano(),
		Signature:   []byte("test-sig"),
		ReceivedAt:  store.NowNano(),
	}
	if _, err := st.AddMessage(compactRec); err != nil {
		t.Fatalf("inserting compaction event: %v", err)
	}

	// --- 3. Read with all=false: compacted message must be absent ---
	readFalseArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
		"all":         false,
	})
	readFalseResp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_read","arguments":`+string(readFalseArgs)+`}`))
	if readFalseResp.Error != nil {
		t.Fatalf("campfire_read all=false: code=%d msg=%s",
			readFalseResp.Error.Code, readFalseResp.Error.Message)
	}
	textFalse := unwrapEnvelopeContent(extractResultText(t, readFalseResp))

	if strings.Contains(textFalse, payloadA) {
		t.Errorf("all=false: compacted payload %q must not appear in read response, got: %s",
			payloadA, textFalse)
	}
	if !strings.Contains(textFalse, payloadB) {
		t.Errorf("all=false: live payload %q must appear in read response, got: %s",
			payloadB, textFalse)
	}

	// --- 4. Read with all=true: both messages must be present ---
	readTrueArgs, _ := json.Marshal(map[string]interface{}{
		"campfire_id": campfireID,
		"all":         true,
	})
	readTrueResp := srv.dispatch(makeReq("tools/call",
		`{"name":"campfire_read","arguments":`+string(readTrueArgs)+`}`))
	if readTrueResp.Error != nil {
		t.Fatalf("campfire_read all=true: code=%d msg=%s",
			readTrueResp.Error.Code, readTrueResp.Error.Message)
	}
	textTrue := unwrapEnvelopeContent(extractResultText(t, readTrueResp))

	if !strings.Contains(textTrue, payloadA) {
		t.Errorf("all=true: compacted payload %q must appear in read response, got: %s",
			payloadA, textTrue)
	}
	if !strings.Contains(textTrue, payloadB) {
		t.Errorf("all=true: live payload %q must appear in read response, got: %s",
			payloadB, textTrue)
	}
}
