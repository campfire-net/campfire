package hosting_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/forge"
	"github.com/campfire-net/campfire/pkg/hosting"
)

// mockForgeState holds the per-account state managed by the mock Forge server.
type mockForgeState struct {
	mu sync.Mutex

	// accounts maps account_id → account name
	accounts map[string]string
	// keys maps plaintext key → KeyRecord
	keys map[string]forge.KeyRecord
	// balances maps account_id → balance_micro
	balances map[string]int64
	// ingestEvents is the ordered list of UsageEvents received
	ingestEvents []forge.UsageEvent

	// nextAccountID is the account ID to return for the next CreateAccount call
	nextAccountID string
	// nextKey is the plaintext key to return for the next CreateKey call
	nextKey string
}

func newMockForgeState() *mockForgeState {
	return &mockForgeState{
		accounts:      make(map[string]string),
		keys:          make(map[string]forge.KeyRecord),
		balances:      make(map[string]int64),
		nextAccountID: "acc-e2e-001",
		nextKey:       "forge-tk-e2e-plaintext",
	}
}

// buildMockForgeServer returns an httptest.Server implementing the Forge API
// endpoints used by pkg/forge.Client.
//
// Endpoints handled:
//   POST /v1/accounts       → create account
//   GET  /v1/keys           → resolve key (uses Bearer token as the key)
//   POST /v1/keys           → create key
//   GET  /v1/accounts/{id}/balance → return balance_micro
//   POST /v1/usage/ingest   → record usage event
func buildMockForgeServer(t *testing.T, state *mockForgeState) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// POST /v1/accounts — create account, return account_id + admin_key
	mux.HandleFunc("/v1/accounts", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		state.mu.Lock()
		id := state.nextAccountID
		state.accounts[id] = req.Name
		state.balances[id] = 1_000_000 // default positive balance (1 USD in micro-USD)
		state.mu.Unlock()

		writeJSON(t, w, http.StatusCreated, map[string]any{
			"account_id": id,
			"name":       req.Name,
			"admin_key":  "forge-sk-admin-once",
			"created_at": time.Now().UTC().Format(time.RFC3339),
		})
	})

	// GET /v1/keys — resolve key using Bearer token
	// POST /v1/keys — create key for a target account
	mux.HandleFunc("/v1/keys", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// The forge.Client.ResolveKey probes GET /v1/keys using the operator key
			// as the bearer token.
			auth := r.Header.Get("Authorization")
			apiKey := strings.TrimPrefix(auth, "Bearer ")

			state.mu.Lock()
			rec, ok := state.keys[apiKey]
			state.mu.Unlock()

			if !ok {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			writeJSON(t, w, http.StatusOK, map[string]any{
				"object": "list",
				"data":   []forge.KeyRecord{rec},
			})

		case http.MethodPost:
			var req struct {
				Role            string `json:"role"`
				TargetAccountID string `json:"target_account_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			state.mu.Lock()
			plaintext := state.nextKey
			rec := forge.KeyRecord{
				TokenHashPrefix: "e2e-hash-prefix",
				AccountID:       req.TargetAccountID,
				Role:            req.Role,
			}
			state.keys[plaintext] = rec
			state.mu.Unlock()

			writeJSON(t, w, http.StatusCreated, forge.Key{
				TokenHash:    "e2e-hash-prefix-full",
				KeyPlaintext: plaintext,
				AccountID:    req.TargetAccountID,
				Role:         req.Role,
				CreatedAt:    time.Now().UTC().Format(time.RFC3339),
			})

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// GET /v1/accounts/{id}/balance — return balance_micro for the account
	mux.HandleFunc("/v1/accounts/", func(w http.ResponseWriter, r *http.Request) {
		// Path: /v1/accounts/{id}/balance
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/accounts/"), "/")
		if len(parts) != 2 || parts[1] != "balance" {
			http.NotFound(w, r)
			return
		}
		accountID := parts[0]

		state.mu.Lock()
		bal, ok := state.balances[accountID]
		state.mu.Unlock()

		if !ok {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		writeJSON(t, w, http.StatusOK, map[string]any{
			"balance_micro": bal,
		})
	})

	// POST /v1/usage/ingest — accept usage event
	mux.HandleFunc("/v1/usage/ingest", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var event forge.UsageEvent
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		state.mu.Lock()
		state.ingestEvents = append(state.ingestEvents, event)
		state.mu.Unlock()

		w.WriteHeader(http.StatusNoContent)
	})

	return httptest.NewServer(mux)
}

// writeJSON writes a JSON-encoded body with the given HTTP status.
func writeJSON(t *testing.T, w http.ResponseWriter, status int, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("writeJSON: %v", err)
	}
}

// TestE2E exercises the full hosting flow:
//
//  1. Operator signup via SignupService.CreateOperator
//  2. Key resolution via ForgeIdentityResolver.ResolveKey
//  3. Identity gate passes for authenticated operator
//  4. Billing gate passes (positive balance)
//  5. Billing gate rejects zero-balance account
//  6. Anonymous identity gate rejection
//  7. Usage recording and emission verified against mock Forge
func TestE2E(t *testing.T) {
	ctx := context.Background()
	state := newMockForgeState()
	srv := buildMockForgeServer(t, state)
	defer srv.Close()

	// Build the forge.Client pointed at the mock server. Zero retry delays for
	// fast tests.
	client := &forge.Client{
		BaseURL:     srv.URL,
		ServiceKey:  "forge-sk-service-key",
		HTTPClient:  srv.Client(),
		RetryDelays: []time.Duration{0, 0, 0},
	}

	// ── Step 1: Operator signup ───────────────────────────────────────────────
	t.Log("Step 1: operator signup")
	signupSvc := hosting.NewSignupService(client)
	identity, apiKey, err := signupSvc.CreateOperator(ctx, "TestCorp")
	if err != nil {
		t.Fatalf("CreateOperator: %v", err)
	}
	if identity.AccountID == "" {
		t.Fatal("CreateOperator: got empty AccountID")
	}
	if identity.Name != "TestCorp" {
		t.Errorf("CreateOperator: Name = %q, want %q", identity.Name, "TestCorp")
	}
	if identity.Role != hosting.RoleTenant {
		t.Errorf("CreateOperator: Role = %q, want %q", identity.Role, hosting.RoleTenant)
	}
	if apiKey == "" {
		t.Fatal("CreateOperator: returned empty apiKey")
	}
	t.Logf("  account_id=%s api_key=%s", identity.AccountID, apiKey)

	// ── Step 2: Key resolution ────────────────────────────────────────────────
	t.Log("Step 2: key resolution")
	resolver := hosting.NewForgeIdentityResolver(client)
	// Set a short TTL so we can bypass cache in step 5 if needed.
	resolver.TTL = time.Millisecond

	resolved, err := resolver.ResolveKey(ctx, apiKey)
	if err != nil {
		t.Fatalf("ResolveKey: %v", err)
	}
	if resolved.AccountID != identity.AccountID {
		t.Errorf("ResolveKey: AccountID = %q, want %q", resolved.AccountID, identity.AccountID)
	}
	if resolved.Role != hosting.RoleTenant {
		t.Errorf("ResolveKey: Role = %q, want %q", resolved.Role, hosting.RoleTenant)
	}
	t.Logf("  resolved account_id=%s role=%s", resolved.AccountID, resolved.Role)

	// ── Step 3: Identity gate passes for authenticated operator ───────────────
	t.Log("Step 3: identity gate — authenticated operator")
	gate := &hosting.IdentityGate{}
	if err := gate.RequireIdentity(ctx, &resolved); err != nil {
		t.Fatalf("RequireIdentity: unexpected error for valid identity: %v", err)
	}

	// ── Step 4: Billing gate passes (positive balance) ────────────────────────
	t.Log("Step 4: billing gate — positive balance")
	// Mock server initialised the account with 1_000_000 micro-USD.
	billingGate := hosting.NewBillingGate(client)
	billingGate.CacheTTL = time.Millisecond // allow cache bypass in step 5

	if err := billingGate.AllowDurableWrite(ctx, resolved.AccountID); err != nil {
		t.Fatalf("AllowDurableWrite (positive balance): %v", err)
	}

	// ── Step 5: Zero balance is rejected ─────────────────────────────────────
	t.Log("Step 5: billing gate — zero balance rejected")
	// Update the mock server state to zero balance.
	state.mu.Lock()
	state.balances[resolved.AccountID] = 0
	state.mu.Unlock()

	// Sleep past CacheTTL so the gate re-queries Forge.
	time.Sleep(5 * time.Millisecond)

	err = billingGate.AllowDurableWrite(ctx, resolved.AccountID)
	if !errors.Is(err, hosting.ErrInsufficientBalance) {
		t.Fatalf("AllowDurableWrite (zero balance): expected ErrInsufficientBalance, got %v", err)
	}

	// ── Step 6: Anonymous client rejected by identity gate ────────────────────
	t.Log("Step 6: identity gate — anonymous client")
	err = gate.RequireIdentity(ctx, nil)
	if !errors.Is(err, hosting.ErrAnonymousDurableStorage) {
		t.Fatalf("RequireIdentity(nil): expected ErrAnonymousDurableStorage, got %v", err)
	}

	// Also verify empty AccountID is treated as anonymous.
	emptyIdentity := &hosting.OperatorIdentity{}
	err = gate.RequireIdentity(ctx, emptyIdentity)
	if !errors.Is(err, hosting.ErrAnonymousDurableStorage) {
		t.Fatalf("RequireIdentity(emptyAccountID): expected ErrAnonymousDurableStorage, got %v", err)
	}

	// ── Step 7: Usage recording and emission ─────────────────────────────────
	t.Log("Step 7: usage recording and emission")
	emitter := hosting.NewUsageEmitter(client, 50*time.Millisecond)

	// Record some messages.
	emitter.RecordMessage("campfire-abc", resolved.AccountID)
	emitter.RecordMessage("campfire-abc", resolved.AccountID)
	emitter.RecordMessage("campfire-xyz", resolved.AccountID)

	// Run Start in a goroutine — it will flush on stop.
	emitterCtx, emitterCancel := context.WithTimeout(ctx, 5*time.Second)
	defer emitterCancel()

	started := make(chan struct{})
	go func() {
		close(started)
		emitter.Start(emitterCtx)
	}()
	<-started

	// Stop the emitter, which triggers a flush of the buffered counts.
	emitter.Stop()

	// Verify the mock Forge server received exactly one UsageEvent for the operator.
	state.mu.Lock()
	events := state.ingestEvents
	state.mu.Unlock()

	if len(events) == 0 {
		t.Fatal("expected at least one UsageEvent to be ingested, got none")
	}

	// Find the event for our operator.
	var operatorEvent *forge.UsageEvent
	for i := range events {
		if events[i].AccountID == resolved.AccountID {
			operatorEvent = &events[i]
			break
		}
	}
	if operatorEvent == nil {
		t.Fatalf("no UsageEvent found for account %q in %d events", resolved.AccountID, len(events))
	}
	if operatorEvent.ServiceID != "campfire-hosting" {
		t.Errorf("UsageEvent.ServiceID = %q, want %q", operatorEvent.ServiceID, "campfire-hosting")
	}
	if operatorEvent.UnitType != "message" {
		t.Errorf("UsageEvent.UnitType = %q, want %q", operatorEvent.UnitType, "message")
	}
	if operatorEvent.Quantity != 3 {
		t.Errorf("UsageEvent.Quantity = %v, want 3", operatorEvent.Quantity)
	}
	if operatorEvent.IdempotencyKey == "" {
		t.Error("UsageEvent.IdempotencyKey is empty")
	}

	t.Logf("  ingest event: account=%s quantity=%.0f idempotency_key=%s",
		operatorEvent.AccountID, operatorEvent.Quantity, operatorEvent.IdempotencyKey)
	t.Log("TestE2E: all 7 steps passed")
}
