package http

import (
	"fmt"
	"time"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/threshold"
	frostmessages "github.com/taurusgroup/frost-ed25519/pkg/messages"
)

// CoSigner holds the endpoint and FROST participant ID for a co-signer.
type CoSigner struct {
	Endpoint      string
	ParticipantID uint32
}

// RunFROSTSign runs the two-round FROST signing protocol with the given co-signers.
// It initializes the caller's signing session, exchanges round-1 commitment messages,
// then exchanges round-2 share messages, and returns the 64-byte Ed25519 signature.
//
// Parameters:
//   - myDKGResult: the caller's DKG share (from threshold.UnmarshalResult)
//   - myParticipantID: the caller's FROST participant ID
//   - signBytes: the canonical bytes to sign
//   - coSigners: list of co-signers with their endpoints and participant IDs
//   - campfireID: campfire ID used for the /sign HTTP endpoint
//   - sessionID: unique identifier for this signing session (UUID recommended)
//   - agentID: the caller's Ed25519 identity (used to sign HTTP requests)
//
// Returns the 64-byte Ed25519 signature and an error if the protocol fails.
func RunFROSTSign(
	myDKGResult *threshold.DKGResult,
	myParticipantID uint32,
	signBytes []byte,
	coSigners []CoSigner,
	campfireID string,
	sessionID string,
	agentID *identity.Identity,
) ([]byte, error) {
	// Build signer ID list: self + co-signers.
	signerIDs := []uint32{myParticipantID}
	for _, cs := range coSigners {
		signerIDs = append(signerIDs, cs.ParticipantID)
	}

	// Initialize our signing session.
	mySS, err := threshold.NewSigningSession(myDKGResult.SecretShare, myDKGResult.Public, signBytes, signerIDs)
	if err != nil {
		return nil, fmt.Errorf("creating signing session: %w", err)
	}

	// Round 1: get our commitment messages.
	myRound1Msgs := mySS.Start()

	// Round 1: send to all co-signers and collect their commitments.
	var allPeerRound1Msgs []*frostmessages.Message
	for _, cs := range coSigners {
		peerMsgs, err := SendSignRound(cs.Endpoint, campfireID, sessionID, 1, signerIDs, signBytes, myRound1Msgs, agentID)
		if err != nil {
			return nil, fmt.Errorf("sign round 1 to %s: %w", cs.Endpoint, err)
		}
		allPeerRound1Msgs = append(allPeerRound1Msgs, peerMsgs...)
	}

	// Deliver all peer round-1 messages to our session.
	for _, m := range allPeerRound1Msgs {
		mySS.Deliver(m) //nolint:errcheck
	}

	// Process to generate round-2 messages.
	myRound2Msgs := mySS.ProcessAll()

	// Round 2: send to all co-signers and collect their shares.
	var allPeerRound2Msgs []*frostmessages.Message
	for _, cs := range coSigners {
		peerMsgs, err := SendSignRound(cs.Endpoint, campfireID, sessionID, 2, nil, nil, myRound2Msgs, agentID)
		if err != nil {
			return nil, fmt.Errorf("sign round 2 to %s: %w", cs.Endpoint, err)
		}
		allPeerRound2Msgs = append(allPeerRound2Msgs, peerMsgs...)
	}

	// Deliver all peer round-2 messages to our session.
	for _, peerMsg := range allPeerRound2Msgs {
		mySS.Deliver(peerMsg) //nolint:errcheck
	}
	mySS.ProcessAll()

	// Wait for signing to complete.
	select {
	case <-mySS.Done():
	case <-time.After(10 * time.Second):
		return nil, fmt.Errorf("threshold signing timed out")
	}

	sig, err := mySS.Signature()
	if err != nil {
		return nil, fmt.Errorf("extracting signature: %w", err)
	}
	return sig, nil
}
