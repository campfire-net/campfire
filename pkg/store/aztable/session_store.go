// Package aztable — session_store.go
//
// SessionStore provides Azure Table Storage persistence for hosted cf-mcp
// session state that must survive Azure Functions instance boundaries:
//
//   - Token registry entries (token → internalID mapping)
//   - Session identity key material (per-session Ed25519 keypair JSON)
//   - Audit campfire ID (per-operator, shared across instances)
//
// Tables:
//
//	CampfireSessionTokens     PK="tokens"      RK=encodeKey(token)
//	CampfireSessionIdentities PK="identities"  RK=encodeKey(internalID)
//	                          PK="audit"       RK=encodeKey(agentKey) — audit campfire IDs
//
// These tables are created on first use; callers do not need to provision them
// separately from the main campfire tables.
package aztable

import (
	"context"
	"fmt"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

// SessionStore persists session metadata (token registry + identity key material
// + attestation state) to Azure Table Storage. It is shared across all cf-mcp
// instances that connect to the same storage account, giving every instance
// access to tokens, identities, and attestations created on any other instance.
type SessionStore struct {
	tokens       *aztables.Client // CampfireSessionTokens
	identities   *aztables.Client // CampfireSessionIdentities
	attestations *aztables.Client // CampfireSessionAttestations
}

// TokenEntryRecord is the on-disk representation of a token registry entry.
// Mirroring session.go tokenEntry / tokenEntryJSON without importing that package.
type TokenEntryRecord struct {
	Token            string    `json:"token"`
	InternalID       string    `json:"internal_id"`
	IssuedAt         time.Time `json:"issued_at"`
	Revoked          bool      `json:"revoked"`
	GracePeriodUntil time.Time `json:"grace_period_until,omitempty"`
	// Operator, when true, marks this as a forge-sk-/forge-tk- issued operator
	// session token that has TTL=0 (no expiry). Persisted so cold-start validation
	// can skip the TTL check without consulting the in-memory operatorSessionIndex.
	Operator bool `json:"operator,omitempty"`
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
		{"CampfireSessionAttestations", &ss.attestations},
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
	operator := int64(0)
	if entry.Operator {
		operator = 1
	}
	entity := map[string]any{
		"PartitionKey":       "tokens",
		"RowKey":             encodeKey(token),
		"Token":              token,
		"InternalID":         entry.InternalID,
		"IssuedAtNs":         entry.IssuedAt.UnixNano(),
		"Revoked":            revoked,
		"GracePeriodUntilNs": gracePeriodNs,
		"Operator":           operator,
	}
	// Annotate nanosecond timestamp fields as Edm.Int64 so Azure Table Storage
	// does not silently coerce them to Edm.Double (53-bit mantissa), which
	// loses precision for large nanosecond values. See annotateInt64 for rationale.
	annotateInt64(entity, "IssuedAtNs", "GracePeriodUntilNs")
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
			if err := unmarshalEntity(raw, &m); err != nil {
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
				Operator:         toInt64(m["Operator"]) != 0,
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

// ---------------------------------------------------------------------------
// Attestation persistence
// ---------------------------------------------------------------------------

// SaveAttestations persists the serialized attestation store JSON for a session.
// The data is the raw JSON bytes of the provenance persistedState (attestations.json).
// Dual-write: callers also write to the local file; this provides a cloud fallback
// for cold-start recovery when the local filesystem is reset (e.g. instance hop).
func (ss *SessionStore) SaveAttestations(internalID string, data []byte) error {
	entity := map[string]any{
		"PartitionKey": "attestations",
		"RowKey":       encodeKey(internalID),
		"InternalID":   internalID,
	}
	setChunked(entity, "Data", data)
	return upsertEntity(context.Background(), ss.attestations, entity)
}

// LoadAttestations returns the attestation store JSON for a session, or
// (nil, false, nil) if no attestations have been stored for that internalID.
func (ss *SessionStore) LoadAttestations(internalID string) ([]byte, bool, error) {
	raw, err := getEntity(context.Background(), ss.attestations, "attestations", encodeKey(internalID))
	if err != nil {
		return nil, false, fmt.Errorf("aztable: LoadAttestations: %w", err)
	}
	if raw == nil {
		return nil, false, nil
	}
	data := getChunked(raw, "Data")
	return data, true, nil
}

// ---------------------------------------------------------------------------
// Audit campfire ID persistence
// ---------------------------------------------------------------------------

// SaveAuditCampfireID persists the audit campfire ID to Azure Table Storage so
// that it survives cold starts and is shared across all instances that use the
// same storage account. The agentKey is the hex-encoded Ed25519 public key of
// the operator identity, used as a scoping key so multiple operators can share
// one storage account without conflicts.
//
// Uses insert-if-not-exists semantics: if another instance has already written
// an ID for this agentKey, the existing ID is left untouched and no error is
// returned. Callers should LoadAuditCampfireID after Save to determine which
// ID actually won (compare-and-swap pattern for concurrent cold starts).
func (ss *SessionStore) SaveAuditCampfireID(agentKey, campfireID string) error {
	entity := map[string]any{
		"PartitionKey": "audit",
		"RowKey":       encodeKey(agentKey),
		"AgentKey":     agentKey,
		"CampfireID":   campfireID,
	}
	return insertEntity(context.Background(), ss.identities, entity)
}

// LoadAuditCampfireID returns the audit campfire ID previously saved for the
// given agent key, or ("", false, nil) if none has been stored yet.
func (ss *SessionStore) LoadAuditCampfireID(agentKey string) (string, bool, error) {
	m, err := getEntity(context.Background(), ss.identities, "audit", encodeKey(agentKey))
	if err != nil {
		return "", false, fmt.Errorf("aztable: LoadAuditCampfireID: %w", err)
	}
	if m == nil {
		return "", false, nil
	}
	id := str(m, "CampfireID")
	if id == "" {
		return "", false, nil
	}
	return id, true, nil
}
