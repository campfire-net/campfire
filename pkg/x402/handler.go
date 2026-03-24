package x402

import (
	"encoding/json"
	"net/http"
)

// CapSetter is implemented by ratelimit.Wrapper (and any compatible type) to
// increase the monthly message cap for a campfire after a verified payment.
type CapSetter interface {
	// SetMonthlyCount sets the monthly message count for the given campfire.
	SetMonthlyCount(campfireID string, count int)
	// MonthlyCount returns the current monthly message count for the given campfire.
	MonthlyCount(campfireID string) int
}

// PaymentRequest is the body of POST /api/payment.
type PaymentRequest struct {
	// CampfireID identifies which campfire's cap to increase.
	CampfireID string `json:"campfire_id"`
	PaymentProof
}

// PaymentResponse is returned on success or failure.
type PaymentResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

// NewPaymentHandler returns an http.Handler for POST /api/payment.
//
// On success it:
//   1. Decodes the PaymentRequest body.
//   2. Validates the PaymentProof using v.
//   3. Increases the monthly cap for the identified campfire by CapIncrease.
//   4. Returns 200 {"ok":true}.
//
// On failure it returns 400 or 500 with {"ok":false,"message":"..."}.
func NewPaymentHandler(v Verifier, caps CapSetter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, PaymentResponse{OK: false, Message: "method not allowed"})
			return
		}

		var req PaymentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, PaymentResponse{OK: false, Message: "invalid JSON: " + err.Error()})
			return
		}

		if req.CampfireID == "" {
			writeJSON(w, http.StatusBadRequest, PaymentResponse{OK: false, Message: "campfire_id is required"})
			return
		}

		if err := v.Verify(req.PaymentProof); err != nil {
			writeJSON(w, http.StatusBadRequest, PaymentResponse{OK: false, Message: err.Error()})
			return
		}

		// Increase the cap by CapIncrease by resetting the monthly count to
		// (current - CapIncrease), floored at 0. This gives the campfire
		// CapIncrease additional messages before the cap triggers again.
		current := caps.MonthlyCount(req.CampfireID)
		newCount := current - CapIncrease
		if newCount < 0 {
			newCount = 0
		}
		caps.SetMonthlyCount(req.CampfireID, newCount)

		writeJSON(w, http.StatusOK, PaymentResponse{OK: true})
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
