package forge_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/forge"
)

// newTestClient creates a Client pointed at the given test server, with
// zero retry delays to keep tests fast.
func newTestClient(server *httptest.Server) *forge.Client {
	return &forge.Client{
		BaseURL:     server.URL,
		ServiceKey:  "forge-sk-testkey",
		HTTPClient:  server.Client(),
		RetryDelays: []time.Duration{0, 0, 0},
	}
}

// writeJSON writes a JSON response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ---- CreateAccount ----------------------------------------------------------

func TestCreateAccount_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/accounts" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "Bearer ") {
			t.Errorf("missing/invalid Authorization header: %q", auth)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["name"] != "test-org" {
			t.Errorf("unexpected name: %v", body["name"])
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"account_id":        "acct-abc123",
			"name":              "test-org",
			"sovereignty_floor": "open",
			"admin_key":         "forge-ak-oneshot",
			"created_at":        "2026-01-01T00:00:00Z",
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	acct, err := c.CreateAccount(context.Background(), "test-org", "ignored@example.com")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if acct.AccountID != "acct-abc123" {
		t.Errorf("AccountID = %q, want %q", acct.AccountID, "acct-abc123")
	}
	if acct.Name != "test-org" {
		t.Errorf("Name = %q, want %q", acct.Name, "test-org")
	}
}

func TestCreateAccount_4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.CreateAccount(context.Background(), "test-org", "")
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention 401, got: %v", err)
	}
}

func TestCreateAccount_5xxThenSuccess(t *testing.T) {
	attempt := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"account_id": "acct-retry",
			"name":       "retry-org",
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	acct, err := c.CreateAccount(context.Background(), "retry-org", "")
	if err != nil {
		t.Fatalf("CreateAccount after retry: %v", err)
	}
	if acct.AccountID != "acct-retry" {
		t.Errorf("AccountID = %q, want %q", acct.AccountID, "acct-retry")
	}
	if attempt != 2 {
		t.Errorf("expected 2 attempts, got %d", attempt)
	}
}

// ---- GetAccount -------------------------------------------------------------

func TestGetAccount_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/billing/accounts/acct-abc" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		bal := int64(5_000_000)
		writeJSON(w, http.StatusOK, map[string]any{
			"account_id":        "acct-abc",
			"sovereignty_floor": "open",
			"balance_micro":     bal,
			"created_at":        "2026-01-01T00:00:00Z",
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	acct, err := c.GetAccount(context.Background(), "acct-abc")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if acct.AccountID != "acct-abc" {
		t.Errorf("AccountID = %q, want %q", acct.AccountID, "acct-abc")
	}
	if acct.BalanceMicro == nil || *acct.BalanceMicro != 5_000_000 {
		t.Errorf("BalanceMicro = %v, want 5000000", acct.BalanceMicro)
	}
}

func TestGetAccount_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.GetAccount(context.Background(), "acct-missing")
	if err == nil {
		t.Fatal("expected error on 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention 404, got: %v", err)
	}
}

// ---- Balance ----------------------------------------------------------------

func TestBalance_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/accounts/acct-xyz/balance" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"account_id":    "acct-xyz",
			"balance_micro": int64(1_000_000),
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	bal, err := c.Balance(context.Background(), "acct-xyz")
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal != 1_000_000 {
		t.Errorf("Balance = %d, want 1000000", bal)
	}
}

func TestBalance_RetryOn500(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"balance_micro": int64(42)})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	bal, err := c.Balance(context.Background(), "acct-retry")
	if err != nil {
		t.Fatalf("Balance after retry: %v", err)
	}
	if bal != 42 {
		t.Errorf("Balance = %d, want 42", bal)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestBalance_ExhaustsRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.Balance(context.Background(), "acct-fail")
	if err == nil {
		t.Fatal("expected error after exhausted retries")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error should mention 503, got: %v", err)
	}
}

func TestBalance_401NoRetry(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.Balance(context.Background(), "acct-unauth")
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if attempts != 1 {
		t.Errorf("expected 1 attempt (no retry on 4xx), got %d", attempts)
	}
}

// ---- CreateKey --------------------------------------------------------------

func TestCreateKey_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/keys" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["role"] != "service" {
			t.Errorf("unexpected role: %v", body["role"])
		}
		if body["target_account_id"] != "acct-abc" {
			t.Errorf("unexpected target_account_id: %v", body["target_account_id"])
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"token_hash": "sha256-abc",
			"key":        "forge-sk-newkey",
			"account_id": "acct-abc",
			"role":       "service",
			"created_at": "2026-01-01T00:00:00Z",
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	key, err := c.CreateKey(context.Background(), "acct-abc", "service")
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	if key.KeyPlaintext != "forge-sk-newkey" {
		t.Errorf("KeyPlaintext = %q, want %q", key.KeyPlaintext, "forge-sk-newkey")
	}
	if key.AccountID != "acct-abc" {
		t.Errorf("AccountID = %q, want %q", key.AccountID, "acct-abc")
	}
}

func TestCreateKey_403(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.CreateKey(context.Background(), "acct-abc", "admin")
	if err == nil {
		t.Fatal("expected error on 403")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should mention 403, got: %v", err)
	}
}

// ---- ResolveKey -------------------------------------------------------------

func TestResolveKey_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/keys" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		// The request should use the operator key, not the service key.
		if r.Header.Get("Authorization") != "Bearer operator-key-123" {
			t.Errorf("expected operator key in Authorization, got: %q", r.Header.Get("Authorization"))
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"object": "list",
			"data": []map[string]any{
				{
					"token_hash_prefix": "abcdef012345",
					"account_id":        "acct-operator",
					"role":              "agent",
					"created_at":        "2026-01-01T00:00:00Z",
					"revoked":           false,
				},
			},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	rec, err := c.ResolveKey(context.Background(), "operator-key-123")
	if err != nil {
		t.Fatalf("ResolveKey: %v", err)
	}
	if rec.AccountID != "acct-operator" {
		t.Errorf("AccountID = %q, want %q", rec.AccountID, "acct-operator")
	}
}

func TestResolveKey_401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid key"})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.ResolveKey(context.Background(), "bad-key")
	if err == nil {
		t.Fatal("expected error on 401")
	}
}

func TestResolveKey_EmptyList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"object": "list",
			"data":   []any{},
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.ResolveKey(context.Background(), "forge-sk-empty")
	if err == nil {
		t.Fatal("expected error for empty key list")
	}
	if !strings.Contains(err.Error(), "no keys found") {
		t.Errorf("unexpected error: %v", err)
	}
}

// ---- Ingest -----------------------------------------------------------------

func TestIngest_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/usage/ingest" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var event forge.UsageEvent
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Errorf("decode event: %v", err)
		}
		if event.AccountID != "acct-123" {
			t.Errorf("AccountID = %q, want %q", event.AccountID, "acct-123")
		}
		if event.ServiceID != "campfire-hosting" {
			t.Errorf("ServiceID = %q, want %q", event.ServiceID, "campfire-hosting")
		}
		if event.IdempotencyKey == "" {
			t.Error("IdempotencyKey should not be empty")
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"status":          "created",
			"idempotency_key": event.IdempotencyKey,
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	err := c.Ingest(context.Background(), forge.UsageEvent{
		AccountID:      "acct-123",
		ServiceID:      "campfire-hosting",
		IdempotencyKey: "evt-unique-001",
		UnitType:       "message",
		Quantity:       1.0,
		Timestamp:      time.Now(),
	})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
}

func TestIngest_RetryOn500(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"status": "created"})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	err := c.Ingest(context.Background(), forge.UsageEvent{
		AccountID:      "acct-retry",
		ServiceID:      "campfire-hosting",
		IdempotencyKey: "evt-retry-001",
	})
	if err != nil {
		t.Fatalf("Ingest after retry: %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestIngest_429NoRetry(t *testing.T) {
	// 429 is a 4xx — should not retry.
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	err := c.Ingest(context.Background(), forge.UsageEvent{
		AccountID:      "acct-ratelimited",
		ServiceID:      "campfire-hosting",
		IdempotencyKey: "evt-rl-001",
	})
	if err == nil {
		t.Fatal("expected error on 429")
	}
	if attempts != 1 {
		t.Errorf("expected 1 attempt (no retry on 4xx), got %d", attempts)
	}
}

func TestIngest_ExhaustsRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	err := c.Ingest(context.Background(), forge.UsageEvent{
		AccountID:      "acct-down",
		ServiceID:      "campfire-hosting",
		IdempotencyKey: "evt-down-001",
	})
	if err == nil {
		t.Fatal("expected error after exhausted retries")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("error should mention 502, got: %v", err)
	}
}

// ---- CreateSubAccount -------------------------------------------------------

func TestCreateSubAccount_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/accounts" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["name"] != "tenant-org" {
			t.Errorf("unexpected name: %v", body["name"])
		}
		if body["parent_account_id"] != "acct-root" {
			t.Errorf("unexpected parent_account_id: %v", body["parent_account_id"])
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"account_id": "acct-child123",
			"name":       "tenant-org",
			"created_at": "2026-01-01T00:00:00Z",
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	acct, err := c.CreateSubAccount(context.Background(), "tenant-org", "acct-root")
	if err != nil {
		t.Fatalf("CreateSubAccount: %v", err)
	}
	if acct.AccountID != "acct-child123" {
		t.Errorf("AccountID = %q, want %q", acct.AccountID, "acct-child123")
	}
}

func TestCreateSubAccount_4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	_, err := c.CreateSubAccount(context.Background(), "tenant-org", "acct-root")
	if err == nil {
		t.Fatal("expected error on 403")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should mention 403, got: %v", err)
	}
}

func TestCreateSubAccount_5xxThenSuccess(t *testing.T) {
	attempt := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"account_id": "acct-child-retry",
			"name":       "retry-tenant",
		})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	acct, err := c.CreateSubAccount(context.Background(), "retry-tenant", "acct-root")
	if err != nil {
		t.Fatalf("CreateSubAccount after retry: %v", err)
	}
	if acct.AccountID != "acct-child-retry" {
		t.Errorf("AccountID = %q, want %q", acct.AccountID, "acct-child-retry")
	}
	if attempt != 2 {
		t.Errorf("expected 2 attempts, got %d", attempt)
	}
}

// ---- CreditAccount ----------------------------------------------------------

func TestCreditAccount_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/accounts/acct-abc/credit" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "Bearer ") {
			t.Errorf("missing/invalid Authorization header: %q", auth)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["amount_micro"] != float64(10_000_000) {
			t.Errorf("unexpected amount_micro: %v", body["amount_micro"])
		}
		if body["product"] != "campfire-signup-credit" {
			t.Errorf("unexpected product: %v", body["product"])
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	err := c.CreditAccount(context.Background(), "acct-abc", 10_000_000, "campfire-signup-credit")
	if err != nil {
		t.Fatalf("CreditAccount: %v", err)
	}
}

func TestCreditAccount_4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "account not found"})
	}))
	defer srv.Close()

	c := newTestClient(srv)
	err := c.CreditAccount(context.Background(), "acct-missing", 5_000_000, "campfire-signup-credit")
	if err == nil {
		t.Fatal("expected error on 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention 404, got: %v", err)
	}
}

func TestCreditAccount_5xxThenSuccess(t *testing.T) {
	attempt := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		if attempt == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	err := c.CreditAccount(context.Background(), "acct-abc", 1_000_000, "campfire-signup-credit")
	if err != nil {
		t.Fatalf("CreditAccount after retry: %v", err)
	}
	if attempt != 2 {
		t.Errorf("expected 2 attempts, got %d", attempt)
	}
}

func TestCreditAccount_ExhaustsRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	c := newTestClient(srv)
	err := c.CreditAccount(context.Background(), "acct-abc", 1_000_000, "campfire-signup-credit")
	if err == nil {
		t.Fatal("expected error after exhausted retries")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("error should mention 502, got: %v", err)
	}
}

// ---- Context cancellation ---------------------------------------------------

func TestIngest_ContextCancelled(t *testing.T) {
	// Server that always returns 500 so retries kick in.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	// Use real retry delays (non-zero) to exercise context cancellation.
	c := &forge.Client{
		BaseURL:     srv.URL,
		ServiceKey:  "forge-sk-test",
		HTTPClient:  srv.Client(),
		RetryDelays: []time.Duration{100 * time.Millisecond, 200 * time.Millisecond},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := c.Ingest(ctx, forge.UsageEvent{
		AccountID:      "acct-cancel",
		ServiceID:      "campfire-hosting",
		IdempotencyKey: "evt-cancel-001",
	})
	if err == nil {
		t.Fatal("expected error on context cancellation")
	}
}

// ---- Exponential backoff timing ---------------------------------------------

func TestExponentialBackoffTiming(t *testing.T) {
	// Verify that the client waits for the configured delays between retries.
	delays := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond}
	attempts := 0
	var lastAttemptTime time.Time
	var gaps []time.Duration

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		if !lastAttemptTime.IsZero() {
			gaps = append(gaps, now.Sub(lastAttemptTime))
		}
		lastAttemptTime = now
		attempts++
		if attempts <= len(delays) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"balance_micro": int64(0)})
	}))
	defer srv.Close()

	c := &forge.Client{
		BaseURL:     srv.URL,
		ServiceKey:  "forge-sk-timing",
		HTTPClient:  srv.Client(),
		RetryDelays: delays,
	}

	_, err := c.Balance(context.Background(), "acct-timing")
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
	if len(gaps) != 2 {
		t.Fatalf("expected 2 gaps, got %d", len(gaps))
	}

	// Each gap should be at least the configured delay (with 5ms tolerance).
	for i, gap := range gaps {
		if gap < delays[i]-5*time.Millisecond {
			t.Errorf("gap[%d] = %v, expected >= %v", i, gap, delays[i])
		}
	}
	// Second gap should be larger than first (exponential).
	if gaps[1] < gaps[0] {
		t.Errorf("gap[1]=%v should be >= gap[0]=%v (exponential backoff)", gaps[1], gaps[0])
	}
}
