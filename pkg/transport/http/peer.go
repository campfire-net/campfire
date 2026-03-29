package http

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	frostmessages "github.com/taurusgroup/frost-ed25519/pkg/messages"
)

// randRead is the function used to generate random nonce bytes.
// Overridable in tests to inject deterministic nonces.
var randRead = rand.Read


// httpClient uses newSSRFSafeTransport() to re-validate resolved IPs at
// connection time, closing the DNS-rebinding TOCTOU window.
var httpClient = &http.Client{
	Timeout:   30 * time.Second,
	Transport: newSSRFSafeTransport(),
}

// OverrideHTTPClientForTest replaces the package-level HTTP client.
// Call from TestMain when tests use loopback servers.
func OverrideHTTPClientForTest(c *http.Client) {
	httpClient = c
}

// pollTransport is the http.RoundTripper used by Poll(). It defaults to an
// SSRF-safe transport and can be overridden in tests via OverridePollTransportForTest.
var pollTransport http.RoundTripper = newSSRFSafeTransport()

// OverridePollTransportForTest replaces the transport used by Poll() so that
// test servers on loopback (127.0.0.1) are reachable. Call from TestMain.
func OverridePollTransportForTest(t http.RoundTripper) {
	pollTransport = t
}

// Deliver POSTs a CBOR-encoded message to a peer endpoint.
// Signs the request body with senderIdentity.
func Deliver(endpoint string, campfireID string, msg *message.Message, id *identity.Identity) error {
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

// Sync GETs messages from a peer since the given nanosecond timestamp.
func Sync(endpoint string, campfireID string, since int64, id *identity.Identity) ([]message.Message, error) {
	url := fmt.Sprintf("%s/campfire/%s/sync?since=%s", endpoint, campfireID, strconv.FormatInt(since, 10))
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	// GET has no body; sign empty bytes
	signRequest(req, id, []byte{})

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("getting from %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("peer returned %d: %s", resp.StatusCode, string(b))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	var msgs []message.Message
	if err := cfencoding.Unmarshal(body, &msgs); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return msgs, nil
}

// DeliverToAll delivers a message to all given endpoints in parallel.
// Returns one error per endpoint (nil if successful).
func DeliverToAll(endpoints []string, campfireID string, msg *message.Message, id *identity.Identity) []error {
	errs := make([]error, len(endpoints))
	var wg sync.WaitGroup
	for i, ep := range endpoints {
		wg.Add(1)
		go func(i int, ep string) {
			defer wg.Done()
			errs[i] = Deliver(ep, campfireID, msg, id)
		}(i, ep)
	}
	wg.Wait()
	return errs
}

// NotifyMembership sends a membership change notification to a peer.
func NotifyMembership(endpoint string, campfireID string, event MembershipEvent, id *identity.Identity) error {
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encoding event: %w", err)
	}

	url := fmt.Sprintf("%s/campfire/%s/membership", endpoint, campfireID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
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

// JoinResult holds the outcome of a successful join request.
type JoinResult struct {
	// CampfirePrivKey is the decrypted campfire Ed25519 private key (threshold=1).
	// Nil if threshold>1 (use ThresholdShareData instead).
	CampfirePrivKey []byte
	// CampfirePubKey is the campfire Ed25519 public key bytes.
	CampfirePubKey []byte
	// JoinProtocol is the campfire's join protocol.
	JoinProtocol string
	// ReceptionRequirements lists required message tags.
	ReceptionRequirements []string
	// Threshold is the campfire's signing threshold.
	Threshold uint
	// Peers is the list of known peer endpoints returned by the admitting member.
	Peers []PeerEntry
	// ThresholdShareData is the decrypted FROST DKG share for this node (threshold>1).
	// Serialized with threshold.MarshalResult.
	ThresholdShareData []byte
	// MyParticipantID is the FROST participant ID assigned to this joiner (threshold>1).
	MyParticipantID uint32
	// DeliveryModes is the campfire's supported delivery modes from the join response.
	// Defaults to ["pull"] when not set (backward compat: pre-DeliveryModes servers).
	DeliveryModes []string
	// Declarations carries convention:operation messages from the admitting node.
	// The joiner stores these locally so readDeclarations can discover them.
	Declarations []DeclarationMessage
}

// Join sends a join request to the given peer endpoint and returns the
// campfire state (including the decrypted private key for threshold=1).
func Join(peerEndpoint, campfireID string, id *identity.Identity, myEndpoint string) (*JoinResult, error) {
	// Generate ephemeral X25519 keypair for key exchange.
	ephemPriv, err := generateX25519Key()
	if err != nil {
		return nil, fmt.Errorf("generating ephemeral X25519 key: %w", err)
	}
	ephemPub := ephemPriv.PublicKey()

	joinReq := JoinRequest{
		JoinerPubkey:       id.PublicKeyHex(),
		JoinerEndpoint:     myEndpoint,
		EphemeralX25519Pub: fmt.Sprintf("%x", ephemPub.Bytes()),
	}
	bodyBytes, err := json.Marshal(joinReq)
	if err != nil {
		return nil, fmt.Errorf("encoding join request: %w", err)
	}

	url := fmt.Sprintf("%s/campfire/%s/join", peerEndpoint, campfireID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("building join request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	signRequest(req, id, bodyBytes)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("posting to %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("peer returned %d: %s", resp.StatusCode, string(b))
	}

	var joinResp JoinResponse
	if err := json.NewDecoder(resp.Body).Decode(&joinResp); err != nil {
		return nil, fmt.Errorf("decoding join response: %w", err)
	}

	result := &JoinResult{
		JoinProtocol:          joinResp.JoinProtocol,
		ReceptionRequirements: joinResp.ReceptionRequirements,
		Threshold:             joinResp.Threshold,
		Peers:                 joinResp.Peers,
		MyParticipantID:       joinResp.JoinerParticipantID,
		DeliveryModes:         campfire.EffectiveDeliveryModes(joinResp.DeliveryModes),
		Declarations:          joinResp.Declarations,
	}

	// Decode campfire public key.
	if joinResp.CampfirePubKey != "" {
		pubBytes, err := hex.DecodeString(joinResp.CampfirePubKey)
		if err != nil {
			return nil, fmt.Errorf("decoding campfire public key: %w", err)
		}
		result.CampfirePubKey = pubBytes
	}

	// Derive shared secret if the responder provided their ephemeral X25519 key.
	var sharedSecret []byte
	if joinResp.ResponderX25519Pub != "" {
		respPubBytes, err := hex.DecodeString(joinResp.ResponderX25519Pub)
		if err != nil {
			return nil, fmt.Errorf("decoding responder X25519 key: %w", err)
		}
		respPub, err := ecdh.X25519().NewPublicKey(respPubBytes)
		if err != nil {
			return nil, fmt.Errorf("parsing responder X25519 key: %w", err)
		}
		rawShared, err := ephemPriv.ECDH(respPub)
		if err != nil {
			return nil, fmt.Errorf("ECDH: %w", err)
		}
		derivedKey, err := HkdfSHA256(rawShared, "campfire-join-v1")
		if err != nil {
			return nil, fmt.Errorf("deriving join key: %w", err)
		}
		sharedSecret = derivedKey
	}

	// Decrypt campfire private key (threshold=1).
	if len(joinResp.EncryptedPrivKey) > 0 && sharedSecret != nil {
		privKey, err := aesGCMDecrypt(sharedSecret, joinResp.EncryptedPrivKey)
		if err != nil {
			return nil, fmt.Errorf("decrypting campfire private key: %w", err)
		}
		result.CampfirePrivKey = privKey
	}

	// Decrypt threshold DKG share (threshold>1).
	if len(joinResp.ThresholdShareData) > 0 && sharedSecret != nil {
		shareData, err := aesGCMDecrypt(sharedSecret, joinResp.ThresholdShareData)
		if err != nil {
			return nil, fmt.Errorf("decrypting threshold share data: %w", err)
		}
		result.ThresholdShareData = shareData
	}

	return result, nil
}

// SendRekeyPhase1 sends a phase-1 rekey request to a peer and returns the peer's
// ephemeral X25519 public key hex. The caller uses this to derive the shared secret
// for encrypting key material in phase 2.
// Returns ("", nil) if the peer responds 200 with no ephemeral key (nothing to do).
func SendRekeyPhase1(endpoint, oldCampfireID string, req RekeyRequest, id *identity.Identity) (string, error) {
	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("encoding rekey phase-1 request: %w", err)
	}

	url := fmt.Sprintf("%s/campfire/%s/rekey", endpoint, oldCampfireID)
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("building rekey phase-1 request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	signRequest(httpReq, id, bodyBytes)

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("posting to %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("peer returned %d: %s", resp.StatusCode, string(b))
	}

	// Try to decode RekeyResponse.
	var rekeyResp RekeyResponse
	if err := json.NewDecoder(resp.Body).Decode(&rekeyResp); err != nil {
		// Peer responded 200 with no JSON body — phase 1 processed as complete.
		return "", nil
	}
	return rekeyResp.EphemeralX25519Pub, nil
}

// SendRekey sends a phase-2 rekey request (with encrypted key material) to a peer.
func SendRekey(endpoint, oldCampfireID string, req RekeyRequest, id *identity.Identity) error {
	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encoding rekey request: %w", err)
	}

	url := fmt.Sprintf("%s/campfire/%s/rekey", endpoint, oldCampfireID)
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("building rekey request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	signRequest(httpReq, id, bodyBytes)

	resp, err := httpClient.Do(httpReq)
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

// SendSignRound sends FROST signing round messages to a peer co-signer and
// returns the peer's outbound messages for that round.
// round is 1 (commitments) or 2 (shares).
// On round 1, signerIDs and messageToSign must be provided.
func SendSignRound(endpoint, campfireID, sessionID string, round int, signerIDs []uint32, messageToSign []byte, msgs []*frostmessages.Message, id *identity.Identity) ([]*frostmessages.Message, error) {
	// Serialize outbound messages.
	rawMsgs := make([][]byte, 0, len(msgs))
	for _, m := range msgs {
		b, err := m.MarshalBinary()
		if err != nil {
			return nil, fmt.Errorf("serializing FROST message: %w", err)
		}
		rawMsgs = append(rawMsgs, b)
	}

	req := SignRoundRequest{
		SessionID:     sessionID,
		Round:         round,
		Messages:      rawMsgs,
		SignerIDs:     signerIDs,
		MessageToSign: messageToSign,
	}
	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encoding sign request: %w", err)
	}

	url := fmt.Sprintf("%s/campfire/%s/sign", endpoint, campfireID)
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("building sign request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	signRequest(httpReq, id, bodyBytes)

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("posting to %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("peer returned %d: %s", resp.StatusCode, string(b))
	}

	var signResp SignRoundResponse
	if err := json.NewDecoder(resp.Body).Decode(&signResp); err != nil {
		return nil, fmt.Errorf("decoding sign response: %w", err)
	}

	// Deserialize response messages.
	outMsgs := make([]*frostmessages.Message, 0, len(signResp.Messages))
	for _, raw := range signResp.Messages {
		var m frostmessages.Message
		if err := m.UnmarshalBinary(raw); err != nil {
			return nil, fmt.Errorf("deserializing peer FROST message: %w", err)
		}
		outMsgs = append(outMsgs, &m)
	}
	return outMsgs, nil
}

// Poll makes a long-poll request to endpoint and returns when messages are
// available or the server-side timeout fires. Returns (nil, cursor, nil) on
// timeout (204 response). cursor is the ReceivedAt nanosecond timestamp to
// resume from; pass 0 for full history.
//
// On success, the returned cursor is the ReceivedAt of the newest returned
// message. Pass it as cursor on the next call.
//
// The caller is responsible for the reconnect loop. No reconnect is done here.
func Poll(endpoint, campfireID string, cursor int64, timeoutSecs int, id *identity.Identity) ([]message.Message, int64, error) {
	url := fmt.Sprintf("%s/campfire/%s/poll?since=%d&timeout=%d", endpoint, campfireID, cursor, timeoutSecs)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, cursor, fmt.Errorf("building poll request: %w", err)
	}
	// Sign with empty body — same convention as handleSync and handlePoll.
	signRequest(req, id, []byte{})

	// Use a per-request client with timeout = timeoutSecs + 5s so the OS does
	// not cut the connection before the server responds. Do NOT use httpClient
	// (which has a fixed 30s timeout, too short for long polls).
	// Use pollTransport (SSRF-safe by default) to close the DNS-rebinding
	// TOCTOU window — endpoint validation at join time is not sufficient; the
	// transport re-validates the resolved IP at connection time on every dial.
	// pollTransport can be overridden in tests via OverridePollTransportForTest.
	pollClient := &http.Client{
		Timeout:   time.Duration(timeoutSecs+5) * time.Second,
		Transport: pollTransport,
	}
	resp, err := pollClient.Do(req)
	if err != nil {
		return nil, cursor, fmt.Errorf("poll request to %s: %w", url, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNoContent: // 204: server timed out, no messages.
		// Return cursor unchanged so the caller can reconnect from the same position.
		return nil, cursor, nil

	case http.StatusOK: // 200: messages available.
		cursorStr := resp.Header.Get("X-Campfire-Cursor")
		newCursor, err := strconv.ParseInt(cursorStr, 10, 64)
		if err != nil {
			return nil, cursor, fmt.Errorf("parsing X-Campfire-Cursor %q: %w", cursorStr, err)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, cursor, fmt.Errorf("reading poll response body: %w", err)
		}
		var msgs []message.Message
		if err := cfencoding.Unmarshal(body, &msgs); err != nil {
			return nil, cursor, fmt.Errorf("decoding poll response: %w", err)
		}
		return msgs, newCursor, nil

	default:
		b, _ := io.ReadAll(resp.Body)
		return nil, cursor, fmt.Errorf("poll: peer returned %d: %s", resp.StatusCode, string(b))
	}
}

// signRequest adds Ed25519 signature headers to an HTTP request.
// Headers set:
//
//	X-Campfire-Sender:    hex-encoded Ed25519 public key
//	X-Campfire-Nonce:     hex-encoded 16-byte random value (per-request uniqueness)
//	X-Campfire-Timestamp: Unix timestamp in seconds (for freshness checks)
//	X-Campfire-Signature: base64 signature over timestamp+nonce+body
//
// The signed payload format mirrors buildSignedPayload in handler_message.go.
func signRequest(req *http.Request, id *identity.Identity, body []byte) {
	// Generate a random 16-byte nonce.
	nonceBytes := make([]byte, 16)
	if _, err := randRead(nonceBytes); err != nil {
		// rand.Read only fails on catastrophic OS errors; panic to fail fast.
		panic("signRequest: rand.Read failed: " + err.Error())
	}
	nonce := hex.EncodeToString(nonceBytes)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)

	signedPayload := buildSignedPayload(timestamp, nonce, body)
	sig := id.Sign(signedPayload)

	req.Header.Set("X-Campfire-Sender", id.PublicKeyHex())
	req.Header.Set("X-Campfire-Nonce", nonce)
	req.Header.Set("X-Campfire-Timestamp", timestamp)
	req.Header.Set("X-Campfire-Signature", base64.StdEncoding.EncodeToString(sig))
}
