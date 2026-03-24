package x402_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/pkg/x402"
)

// ---------------------------------------------------------------------------
// PaymentChallenge / DefaultChallenge
// ---------------------------------------------------------------------------

func TestDefaultChallenge(t *testing.T) {
	c := x402.DefaultChallenge("https://example.com/api/payment")
	if c.Amount == "" {
		t.Error("Amount must not be empty")
	}
	if c.Currency != "USDC" {
		t.Errorf("Currency: want USDC, got %q", c.Currency)
	}
	if c.Chain != "base" {
		t.Errorf("Chain: want base, got %q", c.Chain)
	}
	if c.RecipientAddress == "" {
		t.Error("RecipientAddress must not be empty")
	}
	if c.PaymentURL != "https://example.com/api/payment" {
		t.Errorf("PaymentURL mismatch: %q", c.PaymentURL)
	}
}

func TestPaymentChallenge_JSONRoundTrip(t *testing.T) {
	orig := x402.PaymentChallenge{
		Amount:           "2.50",
		Currency:         "USDC",
		RecipientAddress: "0xabcdef1234567890abcdef1234567890abcdef12",
		Chain:            "base",
		PaymentURL:       "https://host/api/payment",
	}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got x402.PaymentChallenge
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != orig {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// ChallengeFromError
// ---------------------------------------------------------------------------

// fake402Error implements the StatusCode() int interface with 402.
type fake402Error struct{}

func (fake402Error) Error() string   { return "monthly cap exceeded" }
func (fake402Error) StatusCode() int { return http.StatusPaymentRequired }

// fakeNon402Error implements StatusCode() with 429.
type fakeNon402Error struct{}

func (fakeNon402Error) Error() string   { return "rate limited" }
func (fakeNon402Error) StatusCode() int { return http.StatusTooManyRequests }

func TestChallengeFromError_402(t *testing.T) {
	w := httptest.NewRecorder()
	handled := x402.ChallengeFromError(w, fake402Error{}, "https://host/api/payment")
	if !handled {
		t.Fatal("expected ChallengeFromError to handle 402 error")
	}
	res := w.Result()
	if res.StatusCode != http.StatusPaymentRequired {
		t.Errorf("status: want 402, got %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type: want application/json, got %q", ct)
	}
	var challenge x402.PaymentChallenge
	if err := json.NewDecoder(res.Body).Decode(&challenge); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if challenge.Currency != "USDC" {
		t.Errorf("Currency: want USDC, got %q", challenge.Currency)
	}
	if challenge.PaymentURL != "https://host/api/payment" {
		t.Errorf("PaymentURL: %q", challenge.PaymentURL)
	}
}

func TestChallengeFromError_Non402(t *testing.T) {
	w := httptest.NewRecorder()
	handled := x402.ChallengeFromError(w, fakeNon402Error{}, "https://host/api/payment")
	if handled {
		t.Fatal("ChallengeFromError should not handle non-402 error")
	}
	if w.Code != http.StatusOK {
		t.Errorf("response should not have been written, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// StubVerifier
// ---------------------------------------------------------------------------

var validProof = x402.PaymentProof{
	TxHash:       "0x" + strings.Repeat("a", 64),
	Chain:        "base",
	PayerAddress: "0x" + strings.Repeat("b", 40),
}

func TestStubVerifier_ValidProof(t *testing.T) {
	v := x402.StubVerifier{}
	if err := v.Verify(validProof); err != nil {
		t.Errorf("expected valid proof to pass, got: %v", err)
	}
}

func TestStubVerifier_InvalidTxHash(t *testing.T) {
	v := x402.StubVerifier{}
	proof := validProof
	proof.TxHash = "not-a-hash"
	if err := v.Verify(proof); err == nil {
		t.Error("expected error for invalid tx_hash")
	}
}

func TestStubVerifier_EmptyChain(t *testing.T) {
	v := x402.StubVerifier{}
	proof := validProof
	proof.Chain = ""
	if err := v.Verify(proof); err == nil {
		t.Error("expected error for empty chain")
	}
}

func TestStubVerifier_InvalidPayerAddress(t *testing.T) {
	v := x402.StubVerifier{}
	proof := validProof
	proof.PayerAddress = "not-an-address"
	if err := v.Verify(proof); err == nil {
		t.Error("expected error for invalid payer_address")
	}
}

func TestStubVerifier_TxHashWrongLength(t *testing.T) {
	v := x402.StubVerifier{}
	proof := validProof
	proof.TxHash = "0x" + strings.Repeat("a", 63) // one short
	if err := v.Verify(proof); err == nil {
		t.Error("expected error for tx_hash with wrong length")
	}
}

func TestPaymentProof_JSONRoundTrip(t *testing.T) {
	b, err := json.Marshal(validProof)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got x402.PaymentProof
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != validProof {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// NewPaymentHandler
// ---------------------------------------------------------------------------

// fakeCapSetter implements CapSetter for testing.
type fakeCapSetter struct {
	counts map[string]int
}

func newFakeCapSetter() *fakeCapSetter {
	return &fakeCapSetter{counts: make(map[string]int)}
}

func (f *fakeCapSetter) SetMonthlyCount(campfireID string, count int) {
	f.counts[campfireID] = count
}

func (f *fakeCapSetter) MonthlyCount(campfireID string) int {
	return f.counts[campfireID]
}

func paymentBody(campfireID string, proof x402.PaymentProof) *bytes.Buffer {
	req := x402.PaymentRequest{CampfireID: campfireID, PaymentProof: proof}
	b, _ := json.Marshal(req)
	return bytes.NewBuffer(b)
}

func TestPaymentHandler_Success(t *testing.T) {
	caps := newFakeCapSetter()
	caps.SetMonthlyCount("cf1", 1000) // at cap

	h := x402.NewPaymentHandler(x402.StubVerifier{}, caps)
	req := httptest.NewRequest(http.MethodPost, "/api/payment", paymentBody("cf1", validProof))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: want 200, got %d — body: %s", w.Code, w.Body.String())
	}
	var resp x402.PaymentResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK {
		t.Errorf("expected ok:true, got %+v", resp)
	}
	// Count should be reduced by CapIncrease (1000 - 1000 = 0).
	if got := caps.MonthlyCount("cf1"); got != 0 {
		t.Errorf("monthly count: want 0, got %d", got)
	}
}

func TestPaymentHandler_CapFloorAtZero(t *testing.T) {
	caps := newFakeCapSetter()
	caps.SetMonthlyCount("cf1", 200) // less than CapIncrease

	h := x402.NewPaymentHandler(x402.StubVerifier{}, caps)
	req := httptest.NewRequest(http.MethodPost, "/api/payment", paymentBody("cf1", validProof))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: want 200, got %d", w.Code)
	}
	// Floor at 0 — not negative.
	if got := caps.MonthlyCount("cf1"); got != 0 {
		t.Errorf("monthly count: want 0 (floored), got %d", got)
	}
}

func TestPaymentHandler_InvalidProof(t *testing.T) {
	caps := newFakeCapSetter()
	h := x402.NewPaymentHandler(x402.StubVerifier{}, caps)

	badProof := x402.PaymentProof{TxHash: "bad", Chain: "base", PayerAddress: "bad"}
	req := httptest.NewRequest(http.MethodPost, "/api/payment", paymentBody("cf1", badProof))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: want 400, got %d", w.Code)
	}
	var resp x402.PaymentResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.OK {
		t.Error("expected ok:false for invalid proof")
	}
}

func TestPaymentHandler_MissingCampfireID(t *testing.T) {
	caps := newFakeCapSetter()
	h := x402.NewPaymentHandler(x402.StubVerifier{}, caps)

	req := httptest.NewRequest(http.MethodPost, "/api/payment", paymentBody("", validProof))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: want 400, got %d", w.Code)
	}
}

func TestPaymentHandler_MethodNotAllowed(t *testing.T) {
	caps := newFakeCapSetter()
	h := x402.NewPaymentHandler(x402.StubVerifier{}, caps)

	req := httptest.NewRequest(http.MethodGet, "/api/payment", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: want 405, got %d", w.Code)
	}
}

func TestPaymentHandler_BadJSON(t *testing.T) {
	caps := newFakeCapSetter()
	h := x402.NewPaymentHandler(x402.StubVerifier{}, caps)

	req := httptest.NewRequest(http.MethodPost, "/api/payment", strings.NewReader("{not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: want 400, got %d", w.Code)
	}
}
