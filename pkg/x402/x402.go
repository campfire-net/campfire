// Package x402 implements HTTP 402 Payment Required challenge/proof types
// for agent-autonomous billing via stablecoins.
//
// The flow:
//  1. A rate-limited operation returns ErrMonthlyCapExceeded (HTTP 402).
//  2. The server converts it to a PaymentChallenge and writes it as JSON
//     in the 402 response body.
//  3. The agent submits a PaymentProof to POST /api/payment.
//  4. The server verifies the proof (stub: format check only) and increases
//     the monthly cap on the rate limiter.
//
// Real on-chain verification is deferred; the StubVerifier accepts any
// well-formatted proof so the integration path can be exercised end-to-end.
package x402

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// CapIncrease is the number of additional messages granted per payment.
const CapIncrease = 1000

// PaymentChallenge is the body of an HTTP 402 response. It tells the payer
// how much to send, to which address, and on which chain.
type PaymentChallenge struct {
	// Amount is the required payment amount as a decimal string (e.g. "1.00").
	Amount string `json:"amount"`

	// Currency is the stablecoin symbol (e.g. "USDC").
	Currency string `json:"currency"`

	// RecipientAddress is the on-chain address to send payment to.
	RecipientAddress string `json:"recipient_address"`

	// Chain is the blockchain network (e.g. "base").
	Chain string `json:"chain"`

	// PaymentURL is the endpoint to submit a PaymentProof to after paying.
	PaymentURL string `json:"payment_url"`
}

// PaymentProof is submitted by the payer to prove an on-chain payment was made.
type PaymentProof struct {
	// TxHash is the transaction hash on the target chain.
	TxHash string `json:"tx_hash"`

	// Chain identifies the blockchain network (must match the challenge).
	Chain string `json:"chain"`

	// PayerAddress is the on-chain address that sent the payment.
	PayerAddress string `json:"payer_address"`
}

// DefaultChallenge returns a PaymentChallenge with production defaults.
// paymentURL should be the absolute URL of the payment submission endpoint
// (e.g. "https://example.com/api/payment").
func DefaultChallenge(paymentURL string) PaymentChallenge {
	return PaymentChallenge{
		Amount:           "1.00",
		Currency:         "USDC",
		RecipientAddress: "0x0000000000000000000000000000000000000000",
		Chain:            "base",
		PaymentURL:       paymentURL,
	}
}

// ChallengeFromError converts an error that carries HTTP status 402 into a
// PaymentChallenge response body written to w. If err does not indicate a 402
// status, ChallengeFromError writes nothing and returns false.
//
// paymentURL is included in the challenge so the client knows where to submit
// the PaymentProof.
func ChallengeFromError(w http.ResponseWriter, err error, paymentURL string) bool {
	type statuser interface{ StatusCode() int }
	var s statuser
	if !errors.As(err, &s) || s.StatusCode() != http.StatusPaymentRequired {
		return false
	}
	challenge := DefaultChallenge(paymentURL)
	body, _ := json.Marshal(challenge)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusPaymentRequired)
	_, _ = w.Write(body)
	return true
}

// Verifier validates a PaymentProof.
type Verifier interface {
	// Verify returns nil if the proof is acceptable, or an error describing why not.
	Verify(proof PaymentProof) error
}

// txHashRE matches a 0x-prefixed hex string of 64 hex digits (EVM tx hash).
var txHashRE = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)

// addrRE matches a 0x-prefixed hex string of 40 hex digits (EVM address).
var addrRE = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

// StubVerifier accepts any well-formatted PaymentProof without querying the chain.
// Real on-chain verification is deferred to a future implementation.
type StubVerifier struct{}

// Verify checks that all fields are present and syntactically valid.
// It does NOT verify on-chain state.
func (StubVerifier) Verify(proof PaymentProof) error {
	if !txHashRE.MatchString(proof.TxHash) {
		return fmt.Errorf("x402: invalid tx_hash: must be 0x + 64 hex digits, got %q", proof.TxHash)
	}
	if strings.TrimSpace(proof.Chain) == "" {
		return errors.New("x402: chain must not be empty")
	}
	if !addrRE.MatchString(proof.PayerAddress) {
		return fmt.Errorf("x402: invalid payer_address: must be 0x + 40 hex digits, got %q", proof.PayerAddress)
	}
	return nil
}
