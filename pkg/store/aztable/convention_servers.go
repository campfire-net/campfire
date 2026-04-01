// Package aztable — convention_servers.go
//
// CampfireConventionServers table: registry of convention server handlers
// per campfire. This is the provisioning/registry side — not part of
// DispatchStore. It records which convention server handles which operation
// for a given campfire, along with billing and configuration metadata.
//
// Table: CampfireConventionServers
//   PK = encodeKey(campfireID)
//   RK = encodeKey(convention + ":" + operation)
package aztable

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
)

// conventionServersTable is the Azure Table Storage table name.
const conventionServersTable = "CampfireConventionServers"

// ConventionServerRecord holds the registration for a single convention
// server handler bound to a (campfireID, convention, operation) triple.
type ConventionServerRecord struct {
	CampfireID  string
	Convention  string
	Operation   string
	ServerID    string // Ed25519 pubkey hex of the convention server
	Tier        int32  // 1 = inline Go handler, 2 = HTTP POST (Function URL)
	HandlerURL  string // Tier 2 only: Azure Function URL
	Declaration string // Full convention declaration JSON
	CustomerID  string // Customer identifier for billing
	CreatedAt   time.Time
	Enabled     bool // Allows disabling without deletion
}

// ConventionServerStore is the interface for convention server registry persistence.
type ConventionServerStore interface {
	// RegisterConventionServer inserts or replaces a convention server record.
	RegisterConventionServer(ctx context.Context, rec *ConventionServerRecord) error

	// GetConventionServer looks up the handler for a (campfireID, convention, operation).
	// Returns (nil, nil) if not found.
	GetConventionServer(ctx context.Context, campfireID, convention, operation string) (*ConventionServerRecord, error)

	// ListConventionServers lists all registered handlers for a campfire.
	ListConventionServers(ctx context.Context, campfireID string) ([]*ConventionServerRecord, error)

	// DeregisterConventionServer removes a handler record.
	DeregisterConventionServer(ctx context.Context, campfireID, convention, operation string) error

	// SetConventionServerEnabled enables or disables a handler without deleting it.
	SetConventionServerEnabled(ctx context.Context, campfireID, convention, operation string, enabled bool) error
}

// NewConventionServerStore connects to Azure Table Storage and ensures the
// CampfireConventionServers table exists.
func NewConventionServerStore(connectionString string) (ConventionServerStore, error) {
	svc, err := aztables.NewServiceClientFromConnectionString(connectionString, nil)
	if err != nil {
		return nil, fmt.Errorf("aztable: ConventionServerStore: creating service client: %w", err)
	}
	client := svc.NewClient(conventionServersTable)
	ctx := context.Background()
	_, createErr := client.CreateTable(ctx, nil)
	if createErr != nil && !isTableExistsError(createErr) {
		return nil, fmt.Errorf("aztable: ConventionServerStore: ensuring table %s: %w", conventionServersTable, createErr)
	}
	return &tableConventionServerStore{client: client}, nil
}

// tableConventionServerStore implements ConventionServerStore against Azure Table Storage.
type tableConventionServerStore struct {
	client *aztables.Client
}

// conventionServerRK returns the RowKey for a (convention, operation) pair.
func conventionServerRK(convention, operation string) string {
	return encodeKey(convention + ":" + operation)
}

// RegisterConventionServer inserts or replaces a convention server record.
func (s *tableConventionServerStore) RegisterConventionServer(ctx context.Context, rec *ConventionServerRecord) error {
	enabledInt := int64(0)
	if rec.Enabled {
		enabledInt = 1
	}
	entity := map[string]any{
		"PartitionKey": encodeKey(rec.CampfireID),
		"RowKey":       conventionServerRK(rec.Convention, rec.Operation),
		"CampfireID":   rec.CampfireID,
		"Convention":   rec.Convention,
		"Operation":    rec.Operation,
		"ServerID":     rec.ServerID,
		"Tier":         int64(rec.Tier),
		"HandlerURL":   rec.HandlerURL,
		"Declaration":  rec.Declaration,
		"CustomerID":   rec.CustomerID,
		"CreatedAt":    rec.CreatedAt.UnixNano(),
		"Enabled":      enabledInt,
	}
	if err := upsertEntity(ctx, s.client, entity); err != nil {
		return fmt.Errorf("aztable: ConventionServerStore.Register: %w", err)
	}
	return nil
}

// GetConventionServer looks up the handler for a (campfireID, convention, operation).
func (s *tableConventionServerStore) GetConventionServer(ctx context.Context, campfireID, convention, operation string) (*ConventionServerRecord, error) {
	pk := encodeKey(campfireID)
	rk := conventionServerRK(convention, operation)
	raw, err := getEntity(ctx, s.client, pk, rk)
	if err != nil {
		return nil, fmt.Errorf("aztable: ConventionServerStore.Get: %w", err)
	}
	if raw == nil {
		return nil, nil
	}
	return conventionServerFromEntity(raw), nil
}

// ListConventionServers lists all registered handlers for a campfire.
func (s *tableConventionServerStore) ListConventionServers(ctx context.Context, campfireID string) ([]*ConventionServerRecord, error) {
	pk := encodeKey(campfireID)
	filter := fmt.Sprintf("PartitionKey eq '%s'", pk)
	opts := &aztables.ListEntitiesOptions{Filter: strPtr(filter)}
	pager := s.client.NewListEntitiesPager(opts)

	var result []*ConventionServerRecord
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("aztable: ConventionServerStore.List: %w", err)
		}
		for _, rawBytes := range page.Entities {
			var m map[string]any
			if err := json.Unmarshal(rawBytes, &m); err != nil {
				continue
			}
			result = append(result, conventionServerFromEntity(m))
		}
	}
	return result, nil
}

// DeregisterConventionServer removes a handler record.
func (s *tableConventionServerStore) DeregisterConventionServer(ctx context.Context, campfireID, convention, operation string) error {
	pk := encodeKey(campfireID)
	rk := conventionServerRK(convention, operation)
	if err := deleteEntity(ctx, s.client, pk, rk); err != nil {
		return fmt.Errorf("aztable: ConventionServerStore.Deregister: %w", err)
	}
	return nil
}

// SetConventionServerEnabled enables or disables a handler without deleting it.
func (s *tableConventionServerStore) SetConventionServerEnabled(ctx context.Context, campfireID, convention, operation string, enabled bool) error {
	pk := encodeKey(campfireID)
	rk := conventionServerRK(convention, operation)
	raw, err := getEntity(ctx, s.client, pk, rk)
	if err != nil {
		return fmt.Errorf("aztable: ConventionServerStore.SetEnabled: get: %w", err)
	}
	if raw == nil {
		return fmt.Errorf("aztable: ConventionServerStore.SetEnabled: record not found for %s/%s/%s", campfireID, convention, operation)
	}
	enabledInt := int64(0)
	if enabled {
		enabledInt = 1
	}
	raw["Enabled"] = enabledInt
	if err := upsertEntity(ctx, s.client, raw); err != nil {
		return fmt.Errorf("aztable: ConventionServerStore.SetEnabled: upsert: %w", err)
	}
	return nil
}

// conventionServerFromEntity converts a raw Table Storage entity map to a ConventionServerRecord.
func conventionServerFromEntity(m map[string]any) *ConventionServerRecord {
	tier := int32(toInt64(m["Tier"]))
	enabledInt := toInt64(m["Enabled"])
	createdAtNs := toInt64(m["CreatedAt"])
	return &ConventionServerRecord{
		CampfireID:  str(m, "CampfireID"),
		Convention:  str(m, "Convention"),
		Operation:   str(m, "Operation"),
		ServerID:    str(m, "ServerID"),
		Tier:        tier,
		HandlerURL:  str(m, "HandlerURL"),
		Declaration: str(m, "Declaration"),
		CustomerID:  str(m, "CustomerID"),
		CreatedAt:   time.Unix(0, createdAtNs),
		Enabled:     enabledInt != 0,
	}
}
