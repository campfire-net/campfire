package trust

// adapter.go — bridge between cf-authority/trust (L3) and cf-convention (L2)
// GateEvaluator interface.
//
// This file provides NewConventionAdapter() which wraps DefaultGateEvaluator
// and satisfies the convention.GateEvaluator interface from L2. The adapter
// translates between the L2 types (convention.EvaluateRequest) and the L3
// types (trust.EvaluateRequest).
//
// Layer separation: this file IMPORTS cf-conventions/cf-convention (L2 is imported
// by L3). L2 MUST NOT import cf-authority (L3). The depguard rule enforces this.
//
// Wiring: callers in production code (dispatcher.go, server.go) instantiate a
// ConventionAdapter and pass it as the GateEvaluator to WithGateEvaluator().

import (
	"context"
	"crypto/ed25519"
	"time"

	convention "github.com/campfire-net/campfire/cf-conventions/cf-convention"
)

// ConventionAdapter wraps DefaultGateEvaluator and satisfies convention.GateEvaluator.
// It translates convention.EvaluateRequest → trust.EvaluateRequest, evaluates, and
// translates the result back.
//
// Usage:
//
//	adapter := trust.NewConventionAdapter()
//	dispatcher := convention.NewConventionDispatcher(store, logger)
//	dispatcher.SetGateEvaluator(adapter)
type ConventionAdapter struct {
	eval DefaultGateEvaluator
}

// NewConventionAdapter returns a ConventionAdapter that wraps DefaultGateEvaluator.
func NewConventionAdapter() *ConventionAdapter {
	return &ConventionAdapter{}
}

// Evaluate implements convention.GateEvaluator by translating the L2 request
// to L3 and returning the translated result.
func (a *ConventionAdapter) Evaluate(ctx context.Context, req convention.EvaluateRequest) convention.EvaluateResult {
	l3req := translateRequest(req)
	l3result := a.eval.Evaluate(ctx, l3req)
	return translateResult(l3result)
}

// translateRequest converts a convention.EvaluateRequest (L2 types) to a
// trust.EvaluateRequest (L3 types).
func translateRequest(req convention.EvaluateRequest) EvaluateRequest {
	// Translate GateOpRequest.
	opReq := OpRequest{
		Convention: req.Request.Convention,
		Operation:  req.Request.Operation,
		CampfireID: req.Request.CampfireID,
		Tags:       req.Request.Tags,
		Sender:     ed25519.PublicKey(req.Request.Sender),
	}

	// Decode SerializedPredicate (CBOR-encoded AST) into PredicateAST.
	// If serialized predicate is empty, use zero-value predicate (allow-all).
	if len(req.Request.SerializedPredicate) > 0 {
		// Attempt to decode as a map — the predicate AST is stored as CBOR
		// in L2 to avoid importing L3 types. Decode here.
		var raw map[string]any
		if err := cborDecMode.Unmarshal(req.Request.SerializedPredicate, &raw); err == nil {
			if pred, err := ParsePredicateAST(raw); err == nil {
				opReq.Predicate = pred
			}
			// If parse fails, use zero predicate (allow-all; convention-author error).
		}
	}

	// Translate ChainMessages.
	chain := make([]ChainMessage, 0, len(req.ChainMessages))
	for _, cm := range req.ChainMessages {
		chain = append(chain, ChainMessage{
			ID:      cm.ID,
			Payload: cm.Payload,
		})
	}

	// Translate RevocationView.
	revView := make([]RevocationViewEntry, 0, len(req.RevocationView))
	for _, rv := range req.RevocationView {
		revView = append(revView, RevocationViewEntry{
			CampfireID:          rv.CampfireID,
			LatestObservedMsgID: rv.LatestObservedMsgID,
			ObservedAt:          rv.ObservedAt,
		})
	}

	// Translate OwnerPolicy.
	ownerPolicy := OwnerPolicy{
		MaxRevocationStaleness: req.OwnerPolicy.MaxRevocationStaleness,
		MinLevelOverride:       req.OwnerPolicy.MinLevelOverride,
		BlanketDeny:            req.OwnerPolicy.BlanketDeny,
	}

	return EvaluateRequest{
		Request:        opReq,
		ChainMessages:  chain,
		RootPrincipal:  ed25519.PublicKey(req.RootPrincipal),
		CurrentTime:    req.CurrentTime,
		RevocationView: revView,
		OwnerPolicy:    ownerPolicy,
	}
}

// translateResult converts a trust.EvaluateResult (L3 types) to a
// convention.EvaluateResult (L2 types).
func translateResult(r EvaluateResult) convention.EvaluateResult {
	return convention.EvaluateResult{
		Decision:         translateDecision(r.Decision),
		Reason:           convention.DenyReason(r.Reason),
		MissingMessageID: r.MissingMessageID,
	}
}

// translateDecision maps trust.Decision to convention.Decision.
func translateDecision(d Decision) convention.Decision {
	switch d {
	case Allow:
		return convention.GateAllow
	case Deny:
		return convention.GateDeny
	case Unresolvable:
		return convention.GateUnresolvable
	default:
		// Unknown decision → fail closed.
		return convention.GateDeny
	}
}

// Ensure ConventionAdapter satisfies the convention.GateEvaluator interface
// at compile time.
var _ convention.GateEvaluator = (*ConventionAdapter)(nil)

// --- time.Time zero value sentinel ---

// zeroTime is used to check if CurrentTime was provided.
var zeroTime = time.Time{}
