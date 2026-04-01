// Package aztable — session_store.go
//
// SessionStore provides Azure Table Storage persistence for hosted cf-mcp
// session state that must survive Azure Functions instance boundaries:
//
//   - Token registry entries (token → internalID mapping)
//   - Session identity key material (per-session Ed25519 keypair JSON)
//
// Tables:
//
//	CampfireSessionTokens     PK="tokens"      RK=encodeKey(token)
//	CampfireSessionIdentities PK="identities"  RK=encodeKey(internalID)
//
// These tables are created on first use; callers do not need to provision them
// separately from the main campfire tables.
package aztable

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

// SessionStore persists session metadata (token registry + identity key material)
// to Azure Table Storage. It is shared across all cf-mcp instances that connect
// to the same storage account, giving every instance access to tokens and
// identities created on any other instance.
type SessionStore struct {
	tokens     *aztables.Client // CampfireSessionTokens
	identities *aztables.Client // CampfireSessionIdentities
}

// TokenEntryRecord is the on-disk representation of a token registry entry.
// Mirroring session.go tokenEntry / tokenEntryJSON without importing that package.
type TokenEntryRecord struct {
	Token            string    `json:"token"`
	InternalID       string    `json:"internal_id"`
	IssuedAt         time.Time `json:"issued_at"`
	Revoked          bool      `json:"revoked"`
	GracePeriodUntil time.Time `json:"grace_period_until,omitempty"`
}

// NewSessionStore connects to Azure Table Storage using the given connection
// string and ensures the two session-specific tables exist. Returns a
// *SessionStore ready for use.
func NewSessionStore(connectionString string) (*SessionStore, error) {
	svc, err := aztables.NewServiceClientFromConnectionString(connectionString, nil)
	if err != nil {
		return nil, fmt.Errorf("aztable: SessionStore: creating service client: %w", err)
	}
	ss := &SessionStore{}
	tables := []struct {
		name   string
		target **aztables.Client
	}{
		{"CampfireSessionTokens", &ss.tokens},
		{"CampfireSessionIdentities", &ss.identities},
	}
	ctx := context.Background()
	for _, t := range tables {
		client := svc.NewClient(t.name)
		_, createErr := client.CreateTable(ctx, nil)
		if createErr != nil && !isTableExistsError(createErr) {
			return nil, fmt.Errorf("aztable: SessionStore: ensuring table %s: %w", t.name, createErr)
		}
		*t.target = client
	}
	return ss, nil
}

// ---------------------------------------------------------------------------
// Token registry persistence
// ---------------------------------------------------------------------------

// SaveTokenEntry persists (or overwrites) a token registry entry.
func (ss *SessionStore) SaveTokenEntry(token string, entry TokenEntryRecord) error {
	revoked := int64(0)
	if entry.Revoked {
		revoked = 1
	}
	gracePeriodNs := int64(0)
	if !entry.GracePeriodUntil.IsZero() {
		gracePeriodNs = entry.GracePeriodUntil.UnixNano()
	}
	entity := map[string]any{
		"PartitionKey":       "tokens",
		"RowKey":             encodeKey(token),
		"Token":              token,
		"InternalID":         entry.InternalID,
		"IssuedAtNs":         entry.IssuedAt.UnixNano(),
		"Revoked":            revoked,
		"GracePeriodUntilNs": gracePeriodNs,
	}
	return upsertEntity(context.Background(), ss.tokens, entity)
}

// DeleteTokenEntry removes a token entry. Used when a session is revoked and
// we want to clean up cloud state.
func (ss *SessionStore) DeleteTokenEntry(token string) error {
	return deleteEntity(context.Background(), ss.tokens, "tokens", encodeKey(token))
}

// LoadAllTokenEntries returns all token entries stored in Table Storage. Called
// at startup so the in-memory registry can be pre-populated from durable state.
func (ss *SessionStore) LoadAllTokenEntries() ([]TokenEntryRecord, error) {
	opts := &aztables.ListEntitiesOptions{
		Filter: strPtr("PartitionKey eq 'tokens'"),
	}
	pager := ss.tokens.NewListEntitiesPager(opts)
	var entries []TokenEntryRecord
	ctx := context.Background()
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("aztable: LoadAllTokenEntries: %w", err)
		}
		for _, raw := range page.Entities {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				return nil, fmt.Errorf("aztable: LoadAllTokenEntries unmarshal: %w", err)
			}
			revoked := toInt64(m["Revoked"]) != 0
			var gracePeriodUntil time.Time
			if ns := toInt64(m["GracePeriodUntilNs"]); ns != 0 {
				gracePeriodUntil = time.Unix(0, ns)
			}
			entries = append(entries, TokenEntryRecord{
				Token:            str(m, "Token"),
				InternalID:       str(m, "InternalID"),
				IssuedAt:         time.Unix(0, toInt64(m["IssuedAtNs"])),
				Revoked:          revoked,
				GracePeriodUntil: gracePeriodUntil,
			})
		}
	}
	return entries, nil
}

// ---------------------------------------------------------------------------
// Identity persistence
// ---------------------------------------------------------------------------

// SaveIdentity persists the serialised identity JSON for a session. The data
// is the raw bytes of the identity.json file produced by identity.Save().
func (ss *SessionStore) SaveIdentity(internalID string, data []byte) error {
	entity := map[string]any{
		"PartitionKey": "identities",
		"RowKey":       encodeKey(internalID),
		"InternalID":   internalID,
	}
	setChunked(entity, "Data", data)
	return upsertEntity(context.Background(), ss.identities, entity)
}

// LoadIdentity returns the identity JSON for a session, or (nil, false, nil)
// if no identity has been stored for that internalID.
func (ss *SessionStore) LoadIdentity(internalID string) ([]byte, bool, error) {
	raw, err := getEntity(context.Background(), ss.identities, "identities", encodeKey(internalID))
	if err != nil {
		return nil, false, fmt.Errorf("aztable: LoadIdentity: %w", err)
	}
	if raw == nil {
		return nil, false, nil
	}
	data := getChunked(raw, "Data")
	return data, true, nil
}

// DeleteIdentity removes identity data for a session. Called when a session is
// revoked and cloud state cleanup is desired.
func (ss *SessionStore) DeleteIdentity(internalID string) error {
	return deleteEntity(context.Background(), ss.identities, "identities", encodeKey(internalID))
}
