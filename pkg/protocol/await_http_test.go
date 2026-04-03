package protocol_test

// await_http_test.go — regression test for campfire-agent-5sc:
// Client.Await works in HTTP/PollBroker mode.
//
// The gap: syncIfFilesystem is a no-op for HTTP transports, so without
// PollBroker wiring, Await only polls every interval instead of waking
// immediately on message delivery.
//
// The fix: Client.WithHTTPTransport() attaches a cfhttp.Transport to the
// client. Await subscribes to the PollBroker and wakes on notification.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/campfire-net/campfire/pkg/store"
	cfhttp "github.com/campfire-net/campfire/pkg/transport/http"
)

// portBaseAwaitHTTP returns a reproducible base port for the HTTP Await test.
// Uses a high-numbered port range unlikely to collide with other test suites.
func portBaseAwaitHTTP() int {
	return 42800
}

// TestClientAwait_HTTPPollBroker verifies that Client.Await resolves correctly
// in HTTP/PollBroker mode (campfire-agent-5sc).
//
// Setup: two nodes (A and B) on real HTTP transports. A creates the campfire,
// B joins. A sends a future message. B calls Client.Await with its HTTP
// transport attached (enabling PollBroker). A then delivers the fulfillment.
// Await on B must wake immediately on the PollBroker notification and return
// the fulfilling message — not block for the poll interval.
//
// This test covers the gap: syncIfFilesystem is a no-op for HTTP transports,
// so without PollBroker wiring, Await would only check on the poll interval
// rather than being woken by message delivery.
func TestClientAwait_HTTPPollBroker(t *testing.T) {
	base := portBaseAwaitHTTP()
	addrA := fmt.Sprintf("127.0.0.1:%d", base+0)
	addrB := fmt.Sprintf("127.0.0.1:%d", base+1)
	endpointA := fmt.Sprintf("http://%s", addrA)
	endpointB := fmt.Sprintf("http://%s", addrB)

	// Node A: creator.
	clientA := newAwaitHTTPClient(t)
	sA := clientA.ClientStore()
	trA := startAwaitHTTPTransport(t, addrA, sA)

	transportDirA := t.TempDir()
	createResult, err := clientA.Create(protocol.CreateRequest{
		Transport:    &protocol.P2PHTTPTransport{Transport: trA, MyEndpoint: endpointA, Dir: transportDirA},
		JoinProtocol: "open",
	})
	if err != nil {
		t.Fatalf("A.Create: %v", err)
	}
	campfireID := createResult.CampfireID

	// Node B: joiner.
	clientB := newAwaitHTTPClient(t)
	sB := clientB.ClientStore()
	trB := startAwaitHTTPTransport(t, addrB, sB)

	transportDirB := t.TempDir()
	_, err = clientB.Join(protocol.JoinRequest{
		Transport:  &protocol.P2PHTTPTransport{Transport: trB, MyEndpoint: endpointB, PeerEndpoint: endpointA, Dir: transportDirB},
		CampfireID: campfireID,
	})
	if err != nil {
		t.Fatalf("B.Join: %v", err)
	}

	// A sends a "future" message that B will wait on.
	futureMsg, err := clientA.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte("need ruling from B"),
		Tags:       []string{"future"},
	})
	if err != nil {
		t.Fatalf("A.Send(future): %v", err)
	}

	// Attach B's HTTP transport so Await can use PollBroker.
	// This is the fix for campfire-agent-5sc: without this, Await only polls
	// every interval and never uses PollBroker notifications.
	clientB.WithHTTPTransport(trB)

	// B starts Await with a long interval — correctness must come from PollBroker,
	// not periodic polling. If PollBroker is not wired, this test would take ~10s.
	type awaitResult struct {
		msg *protocol.Message
		err error
	}
	resultCh := make(chan awaitResult, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		msg, err := clientB.Await(ctx, protocol.AwaitRequest{
			CampfireID:   campfireID,
			TargetMsgID:  futureMsg.ID,
			Timeout:      30 * time.Second,
			PollInterval: 10 * time.Second, // long interval: must wake via PollBroker, not polling
		})
		resultCh <- awaitResult{msg, err}
	}()

	// Give B's Await goroutine time to subscribe to the PollBroker.
	time.Sleep(100 * time.Millisecond)

	// A sends the fulfillment. This wakes the PollBroker on B's transport.
	fulfillMsg, err := clientA.Send(protocol.SendRequest{
		CampfireID:  campfireID,
		Payload:     []byte("ruling: proceed"),
		Tags:        []string{"fulfills"},
		Antecedents: []string{futureMsg.ID},
	})
	if err != nil {
		t.Fatalf("A.Send(fulfills): %v", err)
	}

	// B's Await must resolve quickly (well before the 10s poll interval)
	// because PollBroker woke it on message delivery.
	select {
	case r := <-resultCh:
		if r.err != nil {
			t.Fatalf("Await: unexpected error: %v", r.err)
		}
		if r.msg == nil {
			t.Fatal("Await: expected fulfilling message, got nil")
		}
		if r.msg.ID != fulfillMsg.ID {
			t.Errorf("Await: expected fulfillment ID %q, got %q", fulfillMsg.ID, r.msg.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Await did not resolve within 5s — PollBroker wiring may be missing or broken")
	}
}

// newAwaitHTTPClient creates a protocol.Client with a fresh identity and store.
func newAwaitHTTPClient(t *testing.T) *protocol.Client {
	t.Helper()
	agentID, s, _ := setupTestEnv(t)
	c := protocol.New(s, agentID)
	return c
}

// startAwaitHTTPTransport starts a cfhttp.Transport and registers it for cleanup.
func startAwaitHTTPTransport(t *testing.T, addr string, s store.Store) *cfhttp.Transport {
	t.Helper()
	tr := cfhttp.New(addr, s)
	if err := tr.Start(); err != nil {
		t.Fatalf("start transport %s: %v", addr, err)
	}
	t.Cleanup(func() { tr.Stop() }) //nolint:errcheck
	time.Sleep(20 * time.Millisecond) // let listener bind
	return tr
}
