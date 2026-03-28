// Package forge provides a Go HTTP client for Forge's service-to-service API.
//
// Campfire-hosting uses this package to manage operator identities, check
// balances, and post usage events. All calls require a forge-sk-* service key
// with RoleService or higher.
package forge

import "time"

// Account is the JSON representation of a Forge account, as returned by
// POST /v1/accounts (creation) or GET /v1/billing/accounts/{id}.
type Account struct {
	AccountID        string  `json:"account_id"`
	Name             string  `json:"name"`
	SovereigntyFloor string  `json:"sovereignty_floor,omitempty"`
	ParentAccountID  string  `json:"parent_account_id,omitempty"`
	BalanceMicro     *int64  `json:"balance_micro,omitempty"`
	CreatedAt        string  `json:"created_at,omitempty"`
	UpdatedAt        string  `json:"updated_at,omitempty"`
}

// Key is the response from POST /v1/keys — includes the plaintext key shown once.
type Key struct {
	TokenHash        string `json:"token_hash"`
	KeyPlaintext     string `json:"key"` // shown once at creation
	AccountID        string `json:"account_id"`
	Role             string `json:"role"`
	SovereigntyFloor string `json:"sovereignty_floor,omitempty"`
	CreatedAt        string `json:"created_at,omitempty"`
}

// KeyRecord is a sanitized key record as returned by GET /v1/keys.
// The plaintext key is never included; only the hash prefix for identification.
type KeyRecord struct {
	TokenHashPrefix  string `json:"token_hash_prefix"`
	AccountID        string `json:"account_id"`
	Role             string `json:"role"`
	SovereigntyFloor string `json:"sovereignty_floor,omitempty"`
	RPMLimit         int    `json:"rpm_limit,omitempty"`
	TPMLimit         int    `json:"tpm_limit,omitempty"`
	DailyBudget      int    `json:"daily_budget,omitempty"`
	MonthlyBudget    int    `json:"monthly_budget,omitempty"`
	CreatedAt        string `json:"created_at,omitempty"`
	Revoked          bool   `json:"revoked"`
}

// UsageEvent is a single metered event to post via POST /v1/usage/ingest.
// Field names and JSON tags match Forge's meter.UsageEvent exactly so the
// server accepts the payload without transformation.
type UsageEvent struct {
	// Identity
	AccountID        string `json:"account_id"`
	KeyHashPrefix    string `json:"key_hash_prefix,omitempty"`
	BillingAccountID string `json:"billing_account_id,omitempty"`

	// Model / routing
	ModelID     string `json:"model_id,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Sovereignty string `json:"sovereignty,omitempty"`

	// Outcome
	Status string `json:"status,omitempty"`

	// Token counts
	InputTokens      int `json:"input_tokens,omitempty"`
	OutputTokens     int `json:"output_tokens,omitempty"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`

	// Cost
	CostUSD float64 `json:"cost_usd,omitempty"`

	// Timing / tracing
	LatencyMS        int64  `json:"latency_ms,omitempty"`
	BedrockRequestID string `json:"bedrock_request_id,omitempty"`

	// RPT attribution
	BeadID    string `json:"bead_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	AgentType string `json:"agent_type,omitempty"`
	Project   string `json:"project,omitempty"`

	// Meta
	TokenCountMethod string `json:"token_count_method,omitempty"`
	ServiceID        string `json:"service_id,omitempty"`

	// Non-token billing
	UnitType string  `json:"unit_type,omitempty"`
	Quantity float64 `json:"quantity,omitempty"`

	// Deduplication
	IdempotencyKey string `json:"idempotency_key,omitempty"`

	// Timestamp when the event occurred.
	Timestamp time.Time `json:"timestamp"`
}
