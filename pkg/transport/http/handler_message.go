package http

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
)

// handleDeliver receives a CBOR-encoded Message from a peer.
// POST /campfire/{id}/deliver
// Auth (signature + membership) is enforced by authMiddleware in route.
func (h *handler) handleDeliver(w http.ResponseWriter, r *http.Request, campfireID, senderHex string, body []byte) {
	// Decode message
	var msg message.Message
	if err := cfencoding.Unmarshal(body, &msg); err != nil {
		http.Error(w, "invalid CBOR body", http.StatusBadRequest)
		return
	}

	// Verify the inner message signature (prevents tampered message content).
	if !msg.VerifySignature() {
		log.Printf("handleDeliver: message signature invalid for campfire %s", campfireID)
		http.Error(w, "invalid message signature", http.StatusBadRequest)
		return
	}

	// Server-side role enforcement on the deliverer (the HTTP request sender).
	// Self (the local node) is always allowed — it is the creator/admitting member.
	// For peers, look up their stored role and enforce restrictions:
	//   - observer: cannot deliver any messages.
	//   - writer:   cannot deliver campfire:* system messages.
	//   - full (and backward-compat aliases "member", "creator", ""): no restrictions.
	//
	// When msg.Sender != senderHex (relay case), the deliverer acts on behalf of the
	// original author. The message signature (VerifySignature above) proves the content
	// is authentic; we only need to verify the deliverer has delivery rights.
	//
	// campfire.EffectiveRole normalizes legacy/unknown values ("member", "creator",
	// empty string) to campfire.RoleFull so the switch only needs to handle the
	// three canonical roles. Without this normalization a peer whose role was stored
	// as "member" (the pre-enforcement default) would fall through the switch without
	// restriction — correct behaviour, but relying on implicit fallthrough rather than
	// explicit semantics.
	selfPubKeyHex, _ := h.transport.SelfInfo()
	if senderHex != selfPubKeyHex {
		rawRole, err := h.store.GetPeerRole(campfireID, senderHex)
		if err != nil {
			log.Printf("handleDeliver: failed to look up role for sender %s in campfire %s: %v", senderHex, campfireID, err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		switch campfire.EffectiveRole(rawRole) {
		case campfire.RoleObserver:
			log.Printf("handleDeliver: observer %s attempted to deliver message to campfire %s", senderHex, campfireID)
			http.Error(w, "observers cannot deliver messages", http.StatusForbidden)
			return
		case campfire.RoleWriter:
			for _, tag := range msg.Tags {
				if strings.HasPrefix(tag, "campfire:") {
					log.Printf("handleDeliver: writer %s attempted to deliver system message (tag %q) to campfire %s", senderHex, tag, campfireID)
					http.Error(w, "writers cannot deliver campfire system messages", http.StatusForbidden)
					return
				}
			}
		}
		// campfire.RoleFull and any other normalized value: no restrictions.
	}

	// Sender-match check: when the HTTP deliverer differs from the message author,
	// this is a relay. Relay is permitted for members with delivery rights (RoleFull or
	// RoleWriter), which was verified above. Non-members are already rejected by
	// authMiddleware before this point.
	if msg.SenderHex() != senderHex {
		log.Printf("handleDeliver: relay for campfire %s: deliverer=%s author=%s", campfireID, senderHex, msg.SenderHex())
	}

	// Dedup check (spec §7.3): if message ID already seen, drop silently.
	// Check dedup BEFORE storing — a duplicate should not be re-stored or re-forwarded.
	// Return 200 so the sender doesn't retry.
	if h.transport != nil && h.transport.dedup != nil {
		if h.transport.dedup.See(msg.ID) {
			w.WriteHeader(http.StatusOK)
			return
		}
	}

	// Max hops check (spec §7.5): if provenance chain length >= MaxHops, drop.
	if len(msg.Provenance) >= MaxHops {
		log.Printf("handleDeliver: message %s for campfire %s exceeds max_hops (%d), dropping", msg.ID, campfireID, MaxHops)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Verify all provenance hops before storing.
	// The filesystem sync path (pkg/protocol/read.go) applies the same check.
	// A message with a forged blind-relay hop (invalid signature) must be rejected
	// to prevent corrupt IsBridged() results (spec §7.5).
	for _, hop := range msg.Provenance {
		if !message.VerifyHop(msg.ID, hop) {
			log.Printf("handleDeliver: invalid provenance hop signature for message %s in campfire %s", msg.ID, campfireID)
			http.Error(w, "invalid provenance hop signature", http.StatusBadRequest)
			return
		}
	}

	// FED-2: validate routing:beacon payload before storage to prevent beacon poisoning.
	// A malformed or unsigned beacon must be rejected before it is stored or relayed.
	for _, tag := range msg.Tags {
		if tag == "routing:beacon" {
			var decl beacon.BeaconDeclaration
			if err := json.Unmarshal(msg.Payload, &decl); err != nil {
				log.Printf("handleDeliver: routing:beacon payload is not valid JSON for campfire %s: %v", campfireID, err)
				http.Error(w, "routing:beacon payload is not valid JSON", http.StatusBadRequest)
				return
			}
			if !beacon.VerifyDeclaration(decl) {
				log.Printf("handleDeliver: routing:beacon payload failed signature verification for campfire %s", campfireID)
				http.Error(w, "routing:beacon payload failed signature verification", http.StatusBadRequest)
				return
			}
			break
		}
	}

	// Store in local SQLite
	rec := store.MessageRecordFromMessage(campfireID, &msg, store.NowNano())
	if _, err := h.store.AddMessage(rec); err != nil {
		log.Printf("handleDeliver: failed to store message for campfire %s: %v", campfireID, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Dispatch convention operations arriving via P2P deliver (T5).
	// Mirrors the dispatch hook in handleSend so convention servers receive
	// messages regardless of whether they arrived via MCP or HTTP peer delivery.
	//
	// Uses the server-lifetime context (transport.ctx) with a 30-second timeout
	// instead of r.Context() or context.Background(). The request context would
	// cancel when the HTTP response is sent, killing in-flight dispatch goroutines
	// (campfire-agent-0rl). context.Background() is unbounded and leaks goroutines
	// on shutdown (campfire-agent-n34). The server-lifetime context cancels on
	// Stop(), and the timeout bounds individual dispatch operations.
	//
	// The cancel func is passed to the hook so it can be deferred inside the
	// goroutine spawned by Dispatch, releasing the timeout timer promptly.
	if h.transport != nil && h.transport.OnMessageDelivered != nil {
		dispatchCtx, dispatchCancel := context.WithTimeout(h.transport.ctx, 30*time.Second)
		h.transport.OnMessageDelivered(dispatchCtx, dispatchCancel, campfireID, &rec)
	}

	// Process routing:beacon and routing:withdraw tags for routing table updates.
	if h.transport != nil {
		for _, tag := range msg.Tags {
			switch tag {
			case "routing:beacon":
				if err := h.transport.routingTable.HandleBeacon(msg.Payload, campfireID, senderHex); err != nil {
					log.Printf("handleDeliver: routing:beacon processing failed for campfire %s: %v", campfireID, err)
				} else {
					// Re-advertise the beacon to other peers with our node_id appended to the path (spec §7.2).
					go h.reAdvertiseBeacon(campfireID, senderHex, msg.Payload)
				}
			case "routing:withdraw":
				if err := h.transport.routingTable.HandleWithdraw(msg.Payload); err != nil {
					log.Printf("handleDeliver: routing:withdraw processing failed for campfire %s: %v", campfireID, err)
				} else {
					// Propagate the withdrawal to downstream peers (spec §7.3).
					go h.propagateWithdraw(campfireID, senderHex, msg.Payload)
				}
			}
		}
	}

	// Record that this peer delivered a message for this campfire (spec §5.3).
	// This populates the peer-needs-set used in path-vector forwarding.
	if h.transport != nil && senderHex != "" {
		h.transport.routingTable.RecordMessageDelivery(campfireID, senderHex)
	}

	// Forward the message to other peers (router forwarding, spec §7.2).
	if h.transport != nil {
		h.forwardMessage(campfireID, senderHex, &msg)
	}

	// Wake any long-polling goroutines waiting for new messages.
	if h.transport != nil && h.transport.pollBroker != nil {
		h.transport.pollBroker.Notify(campfireID)
	}

	w.WriteHeader(http.StatusOK)
}

// forwardMessage forwards a message to peers for the given campfire, excluding the sender.
// It appends a provenance hop signed by the campfire key before forwarding.
//
// Forwarding policy (spec §11.2):
//   - Default: forward only if this instance has the campfire key (locally-hosted campfire).
//   - Relay mode: forward for any campfire in the routing table (opt-in).
//
// If the keyProvider is not set, forwarding is skipped (no campfire key to sign hops).
//
// Routing control messages (routing:beacon, routing:withdraw) are NOT forwarded here;
// they are handled by reAdvertiseBeacon and propagateWithdraw respectively, which
// apply path-vector semantics (appending node_id to the path, etc.). Double-forwarding
// these messages would corrupt the path accumulation and cause test races.
func (h *handler) forwardMessage(campfireID, senderHex string, msg *message.Message) {
	// Skip routing control messages — propagated via dedicated handlers.
	for _, tag := range msg.Tags {
		if tag == "routing:beacon" || tag == "routing:withdraw" {
			return
		}
	}

	kp := h.keyProvider
	if kp == nil && h.transport != nil {
		h.transport.mu.RLock()
		kp = h.transport.keyProvider
		h.transport.mu.RUnlock()
	}
	if kp == nil {
		// No key provider: cannot sign provenance hops; skip forwarding.
		return
	}

	privKeyBytes, pubKeyBytes, err := kp(campfireID)
	if err != nil {
		// Not a locally-hosted campfire; check relay mode.
		h.transport.mu.RLock()
		relayMode := h.transport.relayMode
		h.transport.mu.RUnlock()

		if !relayMode {
			// Default policy: only forward for locally-hosted campfires.
			return
		}
		// Relay mode: no campfire key, cannot sign hops. Skip.
		log.Printf("forwardMessage: relay mode enabled for campfire %s but no key available: %v", campfireID, err)
		return
	}

	campfirePriv := ed25519.PrivateKey(privKeyBytes)
	campfirePub := ed25519.PublicKey(pubKeyBytes)

	// Get campfire membership for provenance hop metadata.
	membership, err := h.store.GetMembership(campfireID)
	if err != nil || membership == nil {
		// Log but continue — we can still forward without membership metadata.
		log.Printf("forwardMessage: GetMembership failed for campfire %s: %v", campfireID, err)
	}

	var joinProtocol string
	if membership != nil {
		joinProtocol = membership.JoinProtocol
	}

	// Make a copy of the message to add provenance hop without mutating the original.
	fwdMsg := *msg
	fwdMsg.Provenance = make([]message.ProvenanceHop, len(msg.Provenance))
	copy(fwdMsg.Provenance, msg.Provenance)

	// Add provenance hop signed by campfire key (spec §7.4).
	if err := fwdMsg.AddHop(campfirePriv, campfirePub, nil, 0, joinProtocol, nil, ""); err != nil {
		log.Printf("forwardMessage: AddHop failed for campfire %s: %v", campfireID, err)
		return
	}

	// Build the forwarder identity from campfire keys.
	// The router signs requests as the campfire, not as an individual agent.
	fwdIdentity := &identity.Identity{
		PublicKey:  campfirePub,
		PrivateKey: campfirePriv,
	}

	// Compute the forwarding set per spec §5 (path-vector amendment).
	//
	// Algorithm:
	// 1. Fetch routing table routes for campfireID.
	// 2. If any route has a non-empty Path (path-vector route):
	//    a. Collect unique next_hop node_ids from all routes.
	//    b. Union with PeerNeedsSet(campfireID).
	//    c. Remove senderHex (no echo).
	//    d. Map each node_id to an endpoint via local peers.
	// 3. If no path-vector routes exist (all empty-path/legacy beacons):
	//    Fall back to flood: all local peers except sender (v0.4.2 behavior).

	// Snapshot local peers once (endpoint lookup map).
	h.transport.mu.RLock()
	localPeers := make([]PeerInfo, len(h.transport.peers[campfireID]))
	copy(localPeers, h.transport.peers[campfireID])
	h.transport.mu.RUnlock()

	// Build node_id → endpoint map from local peers (excludes sender).
	nodeToEndpoint := make(map[string]string, len(localPeers))
	for _, peer := range localPeers {
		if peer.Endpoint != "" && peer.PubKeyHex != senderHex {
			nodeToEndpoint[peer.PubKeyHex] = peer.Endpoint
		}
	}

	// Consult the routing table.
	routes := h.transport.routingTable.Lookup(campfireID)

	// Determine if any path-vector routes exist (non-empty Path).
	hasPathVectorRoutes := false
	for _, route := range routes {
		if len(route.Path) > 0 {
			hasPathVectorRoutes = true
			break
		}
	}

	var targetEndpoints []string

	if hasPathVectorRoutes {
		// Path-vector forwarding with bloom filter pruning:
		// forwarding_set = bloom_filter((PeerNeedsSet ∪ NextHops) - sender).
		// The bloom filter check prunes peers whose downstream does not include
		// this campfire, approximating a per-campfire shortest-path tree (SPT).
		forwardingSet := make(map[string]bool)

		// Add routing next_hops, pruned by bloom filter.
		for _, route := range routes {
			if route.NextHop != "" && route.NextHop != senderHex {
				if h.transport.routingTable.PeerBloomCheck(route.NextHop, campfireID) {
					forwardingSet[route.NextHop] = true
				}
			}
		}

		// Union with peer-needs-set (also bloom-checked).
		peerNeeds := h.transport.routingTable.PeerNeedsSet(campfireID)
		for nodeID := range peerNeeds {
			if nodeID != senderHex {
				if h.transport.routingTable.PeerBloomCheck(nodeID, campfireID) {
					forwardingSet[nodeID] = true
				}
			}
		}

		// Map node_ids to endpoints. Skip if no endpoint known for a node.
		seen := make(map[string]bool)
		for nodeID := range forwardingSet {
			ep, ok := nodeToEndpoint[nodeID]
			if !ok {
				// Try routing table entries for this node's endpoint.
				for _, route := range routes {
					if route.NextHop == nodeID && route.Endpoint != "" {
						ep = route.Endpoint
						ok = true
						break
					}
				}
			}
			if ok && ep != "" && !seen[ep] {
				seen[ep] = true
				targetEndpoints = append(targetEndpoints, ep)
			}
		}
	} else {
		// Legacy flood fallback (v0.4.2 behavior): all peers except sender.
		seen := make(map[string]bool)
		// Include routing table endpoints.
		for _, route := range routes {
			if route.Endpoint != "" && !seen[route.Endpoint] {
				seen[route.Endpoint] = true
				targetEndpoints = append(targetEndpoints, route.Endpoint)
			}
		}
		// Include local peers.
		for _, peer := range localPeers {
			if peer.Endpoint == "" || peer.PubKeyHex == senderHex {
				continue
			}
			if !seen[peer.Endpoint] {
				seen[peer.Endpoint] = true
				targetEndpoints = append(targetEndpoints, peer.Endpoint)
			}
		}
	}

	if len(targetEndpoints) == 0 {
		return
	}

	// Forward in parallel (fire-and-forget, errors are logged not fatal).
	for _, ep := range targetEndpoints {
		go func(endpoint string) {
			if err := deliverMessage(endpoint, campfireID, &fwdMsg, fwdIdentity); err != nil {
				log.Printf("forwardMessage: deliver to %s for campfire %s failed: %v", endpoint, campfireID, err)
			}
		}(ep)
	}
}

// reAdvertiseBeacon re-advertises a received routing:beacon to other peers, appending
// this router's node_id to the path per spec §7.2. The beacon inner_signature is
// re-signed with the TARGET campfire private key when available (threshold=1). When the
// target campfire key is not held by this node, the path is appended advisory-only and
// the original inner_signature is kept (threshold>1 behavior per §3.2).
//
// The re-advertisement is delivered into the gateway campfire (campfireID) so peers can
// receive it via the same routing:beacon mechanism. The delivery request is signed with
// the gateway campfire key (which this node must hold to be authorised to deliver).
//
// The re-advertisement is sent to all known peers for the gateway campfire, excluding the
// peer that originally sent us the beacon (senderHex).
func (h *handler) reAdvertiseBeacon(campfireID, senderHex string, rawPayload []byte) {
	if h.transport == nil {
		return
	}

	// Own node_id: the transport's self public key hex, used as the router's node_id in path.
	selfNodeID, selfEndpoint := h.transport.SelfInfo()
	if selfNodeID == "" {
		// No self identity configured — cannot append node_id to path.
		return
	}

	// Resolve the key provider (transport-level takes precedence over handler-level).
	kp := h.keyProvider
	if kp == nil {
		h.transport.mu.RLock()
		kp = h.transport.keyProvider
		h.transport.mu.RUnlock()
	}

	// The delivery request must be signed with the GATEWAY campfire key.
	// Without it, we cannot authenticate to downstream peers.
	if kp == nil {
		return
	}
	gwPrivBytes, gwPubBytes, err := kp(campfireID)
	if err != nil {
		// This node doesn't hold the gateway campfire key — cannot deliver.
		log.Printf("reAdvertiseBeacon: no gateway key for campfire %s: %v", campfireID, err)
		return
	}
	gwPriv := ed25519.PrivateKey(gwPrivBytes)
	gwPub := ed25519.PublicKey(gwPubBytes)

	// Parse the incoming beacon payload.
	var bp beaconPayload
	if err := json.Unmarshal(rawPayload, &bp); err != nil {
		log.Printf("reAdvertiseBeacon: unmarshal failed for campfire %s: %v", campfireID, err)
		return
	}

	// Append own node_id to path (spec §7.2 step 3).
	newPath := make([]string, len(bp.Path)+1)
	copy(newPath, bp.Path)
	newPath[len(bp.Path)] = selfNodeID
	bp.Path = newPath

	// Attempt to re-sign the inner_signature with the TARGET campfire key (threshold=1,
	// spec §7.2 step 4). The inner_signature must be signed by the target campfire key
	// (whose public key is bp.CampfireID), not the gateway key. If we don't hold the
	// target campfire key, keep the original inner_signature — the path becomes advisory.
	targetPrivBytes, _, targetKeyErr := kp(bp.CampfireID)
	if targetKeyErr == nil {
		// We hold the target campfire key — re-sign with updated path.
		targetPriv := ed25519.PrivateKey(targetPrivBytes)
		decl := beacon.BeaconDeclaration{
			CampfireID:        bp.CampfireID,
			Endpoint:          bp.Endpoint,
			Transport:         bp.Transport,
			Description:       bp.Description,
			JoinProtocol:      bp.JoinProtocol,
			Timestamp:         bp.Timestamp,
			ConventionVersion: bp.ConventionVersion,
			Path:              newPath,
		}
		signBytes, marshalErr := beacon.MarshalInnerSignInput(decl)
		if marshalErr == nil {
			sig := ed25519.Sign(targetPriv, signBytes)
			bp.InnerSignature = fmt.Sprintf("%x", sig)
		} else {
			log.Printf("reAdvertiseBeacon: re-sign marshal failed for target campfire %s: %v", bp.CampfireID, marshalErr)
			// Keep original inner_signature — path is advisory.
		}
	}
	// targetKeyErr != nil: don't hold target campfire key — original inner_signature kept (advisory path).

	// Marshal the updated beacon payload.
	updatedPayload, err := json.Marshal(bp)
	if err != nil {
		log.Printf("reAdvertiseBeacon: marshal updated beacon failed for campfire %s: %v", campfireID, err)
		return
	}

	// Create a new routing:beacon message signed by the gateway campfire key.
	// This is how the re-advertisement is authenticated to downstream peers.
	beaconMsg, err := message.NewMessage(gwPriv, gwPub, updatedPayload, []string{"routing:beacon"}, nil)
	if err != nil {
		log.Printf("reAdvertiseBeacon: creating beacon message failed for campfire %s: %v", campfireID, err)
		return
	}

	fwdIdentity := &identity.Identity{
		PublicKey:  gwPub,
		PrivateKey: gwPriv,
	}

	// Collect target peers: all known peers for the gateway campfire, excluding the sender.
	var targetEndpoints []string
	h.transport.mu.RLock()
	localPeers := make([]PeerInfo, len(h.transport.peers[campfireID]))
	copy(localPeers, h.transport.peers[campfireID])
	h.transport.mu.RUnlock()

	for _, peer := range localPeers {
		if peer.Endpoint == "" {
			continue
		}
		// Exclude the peer that sent us this beacon (prevent echo).
		if peer.PubKeyHex == senderHex {
			continue
		}
		// Exclude our own endpoint (prevent self-delivery).
		if peer.Endpoint == selfEndpoint {
			continue
		}
		targetEndpoints = append(targetEndpoints, peer.Endpoint)
	}

	if len(targetEndpoints) == 0 {
		return
	}

	// Forward in parallel (fire-and-forget).
	for _, ep := range targetEndpoints {
		go func(endpoint string) {
			if err := deliverMessage(endpoint, campfireID, beaconMsg, fwdIdentity); err != nil {
				log.Printf("reAdvertiseBeacon: deliver to %s for campfire %s failed: %v", endpoint, campfireID, err)
			}
		}(ep)
	}
}

// propagateWithdraw propagates a routing:withdraw message to all peers for the campfire,
// excluding the peer that originally sent the withdrawal (spec §7.3).
// This ensures downstream peers that received beacons through this router also remove
// the stale route.
func (h *handler) propagateWithdraw(campfireID, senderHex string, rawPayload []byte) {
	if h.transport == nil {
		return
	}

	// Need a key to sign the forwarded delivery request.
	kp := h.keyProvider
	if kp == nil && h.transport != nil {
		h.transport.mu.RLock()
		kp = h.transport.keyProvider
		h.transport.mu.RUnlock()
	}
	if kp == nil {
		return
	}
	privKeyBytes, pubKeyBytes, err := kp(campfireID)
	if err != nil {
		log.Printf("propagateWithdraw: no key for gateway campfire %s: %v", campfireID, err)
		return
	}
	campfirePriv := ed25519.PrivateKey(privKeyBytes)
	campfirePub := ed25519.PublicKey(pubKeyBytes)

	fwdIdentity := &identity.Identity{
		PublicKey:  campfirePub,
		PrivateKey: campfirePriv,
	}

	// Create a new routing:withdraw message with the same payload.
	withdrawMsg, err := message.NewMessage(campfirePriv, campfirePub, rawPayload, []string{"routing:withdraw"}, nil)
	if err != nil {
		log.Printf("propagateWithdraw: creating withdraw message failed for campfire %s: %v", campfireID, err)
		return
	}

	selfNodeID, selfEndpoint := h.transport.SelfInfo()

	// Collect target peers: all known peers for this campfire, excluding the sender.
	var targetEndpoints []string
	h.transport.mu.RLock()
	localPeers := make([]PeerInfo, len(h.transport.peers[campfireID]))
	copy(localPeers, h.transport.peers[campfireID])
	h.transport.mu.RUnlock()

	for _, peer := range localPeers {
		if peer.Endpoint == "" {
			continue
		}
		if peer.PubKeyHex == senderHex {
			continue
		}
		if selfNodeID != "" && peer.PubKeyHex == selfNodeID {
			continue
		}
		if peer.Endpoint == selfEndpoint {
			continue
		}
		targetEndpoints = append(targetEndpoints, peer.Endpoint)
	}

	if len(targetEndpoints) == 0 {
		return
	}

	for _, ep := range targetEndpoints {
		go func(endpoint string) {
			if err := deliverMessage(endpoint, campfireID, withdrawMsg, fwdIdentity); err != nil {
				log.Printf("propagateWithdraw: deliver to %s for campfire %s failed: %v", endpoint, campfireID, err)
			}
		}(ep)
	}
}

// deliverMessage delivers a message to a peer endpoint, signing the request.
// This is the internal version that accepts raw ed25519 keys wrapped in identity.Identity.
func deliverMessage(endpoint, campfireID string, msg *message.Message, id *identity.Identity) error {
	body, err := cfencoding.Marshal(msg)
	if err != nil {
		return fmt.Errorf("encoding message: %w", err)
	}

	url := fmt.Sprintf("%s/campfire/%s/deliver", endpoint, campfireID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	signRequest(req, id, body)

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("posting to %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("peer returned %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// handleSync serves messages from the local store newer than the given timestamp.
// GET /campfire/{id}/sync?since={nanosecond-timestamp}
// Auth (signature + membership) is enforced by authMiddleware in route.
func (h *handler) handleSync(w http.ResponseWriter, r *http.Request, campfireID, senderHex string, body []byte) {
	sinceStr := r.URL.Query().Get("since")
	var since int64
	if sinceStr != "" {
		var err error
		since, err = strconv.ParseInt(sinceStr, 10, 64)
		if err != nil {
			http.Error(w, "invalid since parameter", http.StatusBadRequest)
			return
		}
	}

	records, err := h.store.ListMessages(campfireID, since)
	if err != nil {
		log.Printf("handleSync: failed to query messages for campfire %s: %v", campfireID, err)
		http.Error(w, "failed to query messages", http.StatusInternalServerError)
		return
	}

	// Convert store records back to message.Message for wire format
	msgs := make([]message.Message, 0, len(records))
	for _, rec := range records {
		msg, err := recordToMessage(rec)
		if err != nil {
			continue
		}
		msgs = append(msgs, msg)
	}

	data, err := cfencoding.Marshal(msgs)
	if err != nil {
		log.Printf("handleSync: failed to encode response for campfire %s: %v", campfireID, err)
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/cbor")
	w.WriteHeader(http.StatusOK)
	w.Write(data) //nolint:errcheck
}

// handlePoll implements long-polling: sync-then-block semantics.
// GET /campfire/{id}/poll?since={ns}&timeout={s}
//
// Behaviour:
//  1. Auth check (401 on failure) — enforced by authMiddleware in route.
//  2. Membership check (403 if sender not a member) — enforced by authMiddleware.
//  3. Parse query params (400 on bad since; timeout default=30, cap=50).
//  4. Subscribe to PollBroker (503 if limit exceeded).
//  5. Initial sync: if records exist → 200 with CBOR body + X-Campfire-Cursor.
//  6. Block on channel or timeout.
//  7. Post-wait sync: if records exist → 200; else → 204 + X-Campfire-Cursor=since.
func (h *handler) handlePoll(w http.ResponseWriter, r *http.Request, campfireID, senderHex string, body []byte) {
	// Null-broker guard.
	if h.transport == nil || h.transport.pollBroker == nil {
		http.Error(w, "long poll not supported", http.StatusNotImplemented)
		return
	}

	// Parse query params.
	sinceStr := r.URL.Query().Get("since")
	var since int64
	if sinceStr != "" {
		var err error
		since, err = strconv.ParseInt(sinceStr, 10, 64)
		if err != nil {
			http.Error(w, "invalid since parameter", http.StatusBadRequest)
			return
		}
	}

	timeoutSec := 30
	if timeoutStr := r.URL.Query().Get("timeout"); timeoutStr != "" {
		t, err := strconv.Atoi(timeoutStr)
		if err != nil {
			http.Error(w, "invalid timeout parameter", http.StatusBadRequest)
			return
		}
		timeoutSec = t
	}
	if timeoutSec > 50 {
		timeoutSec = 50 // cap below server WriteTimeout (60s) to avoid killed connections
	}
	if timeoutSec < 1 {
		timeoutSec = 1 // enforce minimum 1s to prevent zero-duration busy-loop DoS
	}

	// Subscribe to PollBroker.
	ch, dereg, err := h.transport.pollBroker.Subscribe(campfireID)
	if err != nil {
		http.Error(w, "too many active pollers", http.StatusServiceUnavailable)
		return
	}
	defer dereg()

	// Helper: encode and send records as CBOR 200 with cursor header.
	respondWithRecords := func(records []store.MessageRecord) {
		msgs := make([]message.Message, 0, len(records))
		for _, rec := range records {
			msg, err := recordToMessage(rec)
			if err != nil {
				continue
			}
			msgs = append(msgs, msg)
		}
		data, err := cfencoding.Marshal(msgs)
		if err != nil {
			log.Printf("handlePoll: failed to encode response for campfire %s: %v", campfireID, err)
			http.Error(w, "failed to encode response", http.StatusInternalServerError)
			return
		}
		cursor := strconv.FormatInt(records[len(records)-1].ReceivedAt, 10)
		w.Header().Set("Content-Type", "application/cbor")
		w.Header().Set("X-Campfire-Cursor", cursor)
		w.WriteHeader(http.StatusOK)
		w.Write(data) //nolint:errcheck
	}

	// The poll cursor is a received_at nanosecond timestamp. Filter by received_at
	// so cursor and filter use the same field, preventing message loss when sender
	// clocks are skewed relative to the server. (Fix for workspace-d68.)
	pollFilter := store.MessageFilter{AfterReceivedAt: since}

	// Initial sync: return immediately if messages already exist.
	records, err := h.store.ListMessages(campfireID, 0, pollFilter)
	if err != nil {
		log.Printf("handlePoll: failed to query messages for campfire %s: %v", campfireID, err)
		http.Error(w, "failed to query messages", http.StatusInternalServerError)
		return
	}
	if len(records) > 0 {
		respondWithRecords(records)
		return
	}

	// Block until notification or timeout.
	select {
	case <-ch:
	case <-time.After(time.Duration(timeoutSec) * time.Second):
	}

	// Post-wait sync.
	records, err = h.store.ListMessages(campfireID, 0, pollFilter)
	if err != nil {
		log.Printf("handlePoll: failed to query messages (post-wait) for campfire %s: %v", campfireID, err)
		http.Error(w, "failed to query messages", http.StatusInternalServerError)
		return
	}
	if len(records) > 0 {
		respondWithRecords(records)
		return
	}

	// No messages: 204 with cursor = since.
	w.Header().Set("X-Campfire-Cursor", strconv.FormatInt(since, 10))
	w.WriteHeader(http.StatusNoContent)
}

// handleMembership receives a membership change notification.
// POST /campfire/{id}/membership
// Auth (signature + membership) is enforced by authMiddleware in route.
func (h *handler) handleMembership(w http.ResponseWriter, r *http.Request, campfireID, senderHex string, body []byte) {
	var event MembershipEvent
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	// Update local peer list based on event.
	// Identity validation: the sender (verified via signature) must match
	// the member field for join/leave events to prevent identity injection.
	switch event.Event {
	case "join":
		// A node can only announce its own join.
		if event.Member != senderHex {
			http.Error(w, "join member must match sender", http.StatusBadRequest)
			return
		}
		if event.Endpoint != "" {
			if err := validateJoinerEndpoint(event.Endpoint); err != nil {
				http.Error(w, "invalid endpoint: "+err.Error(), http.StatusBadRequest)
				return
			}
			h.transport.AddPeer(campfireID, senderHex, event.Endpoint)
		}
	case "leave":
		// A node can only announce its own departure.
		if event.Member != senderHex {
			http.Error(w, "leave member must match sender", http.StatusBadRequest)
			return
		}
		h.transport.RemovePeer(campfireID, senderHex)
	case "delivery":
		// A member updates their own delivery preference (push endpoint or pull).
		// Only the member themselves may change their own delivery preference.
		if event.Member != senderHex {
			http.Error(w, "delivery member must match sender", http.StatusBadRequest)
			return
		}
		if event.Endpoint != "" {
			// Member wants push delivery. Validate endpoint and check campfire supports push.
			if err := validateJoinerEndpoint(event.Endpoint); err != nil {
				http.Error(w, "invalid endpoint: "+err.Error(), http.StatusBadRequest)
				return
			}
			// Read delivery modes from disk state or provider — same logic as handleJoin.
			var deliveryModes []string
			membership, err := h.store.GetMembership(campfireID)
			if err != nil || membership == nil {
				http.Error(w, "campfire membership not found", http.StatusNotFound)
				return
			}
			if safeDir, dirErr := sanitizeTransportDir(membership.TransportDir); dirErr == nil {
				stateFile := filepath.Join(safeDir, "campfire.cbor")
				if stateData, readErr := os.ReadFile(stateFile); readErr == nil {
					var cfState campfire.CampfireState
					if decErr := cfencoding.Unmarshal(stateData, &cfState); decErr == nil {
						deliveryModes = campfire.EffectiveDeliveryModes(cfState.DeliveryModes)
					}
				}
			}
			if len(deliveryModes) == 0 && h.transport != nil {
				h.transport.mu.RLock()
				dmp := h.transport.deliveryModesProvider
				h.transport.mu.RUnlock()
				if dmp != nil {
					if modes := dmp(campfireID); len(modes) > 0 {
						deliveryModes = modes
					}
				}
			}
			if len(deliveryModes) == 0 {
				deliveryModes = campfire.EffectiveDeliveryModes(nil)
			}
			supportsPush := false
			for _, m := range deliveryModes {
				if m == campfire.DeliveryModePush {
					supportsPush = true
					break
				}
			}
			if !supportsPush {
				log.Printf("handleMembership: delivery event: campfire %s does not support push (modes=%v), rejected from %s",
					campfireID, deliveryModes, senderHex[:min(8, len(senderHex))])
				http.Error(w, "campfire does not support push delivery", http.StatusBadRequest)
				return
			}
			// Store/update the endpoint and register the peer in the transport.
			h.store.UpsertPeerEndpoint(store.PeerEndpoint{ //nolint:errcheck
				CampfireID:   campfireID,
				MemberPubkey: senderHex,
				Endpoint:     event.Endpoint,
			})
			h.transport.AddPeer(campfireID, senderHex, event.Endpoint)
		} else {
			// Empty endpoint: member is switching to pull — remove stored endpoint.
			h.store.DeletePeerEndpoint(campfireID, senderHex) //nolint:errcheck
			h.transport.RemovePeer(campfireID, senderHex)
		}
	case "evict":
		// Eviction is issued by the creator on behalf of another member.
		// Fail-closed: if we can't verify the creator, reject the eviction.
		membership, err := h.store.GetMembership(campfireID)
		if err != nil {
			log.Printf("handleMembership: GetMembership failed for campfire %s: %v", campfireID, err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if membership == nil || membership.CreatorPubkey == "" || senderHex != membership.CreatorPubkey {
			http.Error(w, "only the campfire creator may evict members", http.StatusForbidden)
			return
		}
		h.transport.RemovePeer(campfireID, event.Member)
	default:
		http.Error(w, "unknown event type", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// verifyRequestSignature checks the Ed25519 signature header.
// The signature covers: timestamp (as ASCII decimal Unix seconds string) || newline || nonce (hex string) || newline || body.
// This construction prevents replay attacks: each request has a unique nonce and a
// bounded timestamp, so captured requests cannot be re-submitted.
func verifyRequestSignature(senderHex, sigB64, nonce, timestamp string, body []byte) error {
	pubKeyBytes, err := hex.DecodeString(senderHex)
	if err != nil {
		return fmt.Errorf("decoding sender public key: %w", err)
	}
	if len(pubKeyBytes) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid public key length: %d", len(pubKeyBytes))
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return fmt.Errorf("decoding signature: %w", err)
	}
	// Build the signed payload: timestamp || nonce || body.
	signedPayload := buildSignedPayload(timestamp, nonce, body)
	if !ed25519.Verify(ed25519.PublicKey(pubKeyBytes), signedPayload, sig) {
		return fmt.Errorf("signature verification failed")
	}
	return nil
}

// buildSignedPayload constructs the canonical bytes that are signed for a request.
// Format: timestamp (as ASCII decimal string) + "\n" + nonce + "\n" + body.
// Using ASCII strings avoids endianness ambiguity and is trivially debuggable.
func buildSignedPayload(timestamp, nonce string, body []byte) []byte {
	// pre-allocate: len(timestamp) + 1 + len(nonce) + 1 + len(body)
	n := len(timestamp) + 1 + len(nonce) + 1 + len(body)
	out := make([]byte, 0, n)
	out = append(out, []byte(timestamp)...)
	out = append(out, '\n')
	out = append(out, []byte(nonce)...)
	out = append(out, '\n')
	out = append(out, body...)
	return out
}

// recordToMessage converts a store.MessageRecord to a message.Message.
// Tags, Antecedents, and Provenance are already typed Go values on MessageRecord
// (JSON deserialization happens at the store boundary), so no unmarshaling is needed here.
func recordToMessage(rec store.MessageRecord) (message.Message, error) {
	senderBytes, err := hex.DecodeString(rec.Sender)
	if err != nil {
		return message.Message{}, fmt.Errorf("decoding sender: %w", err)
	}

	return message.Message{
		ID:          rec.ID,
		Sender:      senderBytes,
		Payload:     rec.Payload,
		Tags:        rec.Tags,
		Antecedents: rec.Antecedents,
		Timestamp:   rec.Timestamp,
		Signature:   rec.Signature,
		Provenance:  rec.Provenance,
		Instance:    rec.Instance,
	}, nil
}
