package http

// SignRoundRequest is the body for POST /campfire/{id}/sign.
// It carries FROST signing round messages from the initiator to a co-signer.
// The protocol runs in two rounds:
//   - Round 1: initiator sends its commitment + context; co-signer responds with its commitment.
//   - Round 2: initiator sends the aggregated round-2 message; co-signer responds with its share.
//
// Sessions are ephemeral: the server stores signing state only for the duration
// of an active signing protocol (typically a few seconds). After signing completes
// or times out, the state is discarded.
type SignRoundRequest struct {
	// SessionID is a unique identifier for this signing session. The initiator
	// generates it; co-signers index their state by it.
	SessionID string `json:"session_id"`
	// SignerIDs lists all participant IDs taking part in this signing session.
	// Provided in round 1; ignored (may be empty) in round 2.
	SignerIDs []uint32 `json:"signer_ids,omitempty"`
	// MessageToSign is the bytes being threshold-signed. Provided in round 1 only.
	MessageToSign []byte `json:"message_to_sign,omitempty"`
	// Round is 1 or 2.
	Round int `json:"round"`
	// Messages contains the FROST protocol messages addressed to or broadcast to
	// this co-signer. Each element is a binary-marshaled messages.Message.
	Messages [][]byte `json:"messages"`
}

// SignRoundResponse is the response to a SignRoundRequest.
// It carries the co-signer's FROST output messages for the requested round.
type SignRoundResponse struct {
	// Messages contains the FROST protocol messages produced by this co-signer.
	// Each element is a binary-marshaled messages.Message.
	Messages [][]byte `json:"messages"`
}
