// Package aztable provides an Azure Table Storage implementation of store.Store.
// It is intended for use in hosted cf-mcp deployments (Azure Container Apps)
// where a managed SQLite file is not practical.
//
// Table mapping:
//   - campfire_memberships → CampfireMemberships  PK=campfireID  RK="membership"
//   - messages             → CampfireMessages      PK=campfireID  RK=messageID
//   - read_cursors         → CampfireReadCursors   PK=campfireID  RK="cursor"
//   - peer_endpoints       → CampfirePeerEndpoints PK=campfireID  RK=memberPubkey
//   - threshold_shares     → CampfireThresholds    PK=campfireID  RK="share"
//   - pending_threshold_shares → CampfirePendingShares PK=campfireID RK=zero-padded-participantID
//   - campfire_epoch_secrets   → CampfireEpochs    PK=campfireID  RK=zero-padded-epoch
//   - filters              → CampfireFilters       PK=campfireID  RK=direction
//
// Table Storage property value limit is 64 KB for binary (Edm.Binary).
// Large payloads (message body, secret share blobs) are chunked into Chunk0..ChunkN
// properties each ≤ 60 KB (allowing for base64 expansion at the wire level).
// A ChunkCount property records the number of chunks.
package aztable

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
	"github.com/campfire-net/campfire/pkg/crypto"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
)

// chunkSize is the maximum raw byte size per chunk property (24 KB).
// Azure Table Storage limits property values to 64 KB. Binary data stored
// as []byte is base64-encoded by json.Marshal (4/3 expansion), and Azurite
// counts string values in UTF-16 (2 bytes/char). So effective limit is:
// raw * 4/3 (base64) * 2 (UTF-16) ≤ 64 KB → raw ≤ 24 KB.
const chunkSize = 24 * 1024

// epochPadWidth is the zero-padding width for epoch strings used as row keys.
// uint64 max is 18446744073709551615 (20 digits).
const epochPadWidth = 20

// participantPadWidth is the zero-padding width for participant IDs used as row keys.
// uint32 max is 4294967295 (10 digits).
const participantPadWidth = 10

// Compile-time assertion that *TableStore implements store.Store.
var _ store.Store = (*TableStore)(nil)

// TableStore implements store.Store against Azure Table Storage.
type TableStore struct {
	svc         *aztables.ServiceClient
	memberships *aztables.Client
	messages    *aztables.Client
	cursors     *aztables.Client
	peers       *aztables.Client
	thresholds  *aztables.Client
	pending     *aztables.Client
	epochs      *aztables.Client
	filters     *aztables.Client
	invites     *aztables.Client
	counters    *aztables.Client

	// namespace is an optional per-session prefix applied to all PartitionKeys
	// (except invites, which are global). When set, PK = encodeKey(namespace+"|"+id).
	// Empty means single-agent mode (existing behaviour).
	namespace string

	// mu protects supersededCache.
	mu              sync.RWMutex
	supersededCache map[string]supersededCacheEntry
}

// pk returns the encoded PartitionKey for campfireID, prefixed with the
// session namespace when one is configured. Invites use a separate key scheme
// and must not go through pk().
func (ts *TableStore) pk(campfireID string) string {
	if ts.namespace == "" {
		return encodeKey(campfireID)
	}
	return encodeKey(ts.namespace + "|" + campfireID)
}

// nsPKFilter returns an OData filter string that restricts a scan to entities
// belonging to the current namespace. When namespace is empty the filter is
// empty (no restriction — existing single-agent behaviour).
//
// The prefix "namespace|" encodes to "namespaceX7c" (| → x7c). The
// exclusive upper bound increments the last nibble character ('c' → 'd').
// This gives a tight half-open range scan without a STARTSWITH operator.
func (ts *TableStore) nsPKFilter() string {
	if ts.namespace == "" {
		return ""
	}
	lo := encodeKey(ts.namespace+"|") // e.g. "abc123x7c"
	// Increment the last char to get the exclusive upper bound.
	hi := lo[:len(lo)-1] + string(lo[len(lo)-1]+1)
	return fmt.Sprintf("PartitionKey ge '%s' and PartitionKey lt '%s'", lo, hi)
}

type supersededCacheEntry struct {
	maxCompactionTS int64
	superseded      map[string]bool
}

// NewTableStore connects to Azure Table Storage using the given connection string
// and ensures all required tables exist. Returns a store.Store.
func NewTableStore(connectionString string) (store.Store, error) {
	return newTableStore(connectionString)
}

// NewNamespacedTableStore creates a TableStore that prefixes all PartitionKeys
// (except invites) with namespace+"|". This gives per-session isolation within
// shared Azure Storage tables, equivalent to the per-file isolation of SQLite.
//
// namespace must be a non-empty string that contains only alphanumeric characters
// and hyphens (e.g. a UUID-based internalID). Callers typically pass the session's
// internalID.
func NewNamespacedTableStore(connectionString, namespace string) (store.Store, error) {
	ts, err := newTableStore(connectionString)
	if err != nil {
		return nil, err
	}
	ts.namespace = namespace
	return ts, nil
}

// NewRawTableStore creates a TableStore and returns the concrete *TableStore.
// This is intended for tests and callers that need access to methods beyond
// the store.Store interface (e.g., GetStorageCounter).
func NewRawTableStore(connectionString string) (*TableStore, error) {
	return newTableStore(connectionString)
}

// newTableStore is the shared constructor used by both NewTableStore and
// NewNamespacedTableStore. It creates the service client and ensures all
// required tables exist, returning the raw *TableStore (not the Store interface)
// so the callers can set additional fields before returning.
func newTableStore(connectionString string) (*TableStore, error) {
	svc, err := aztables.NewServiceClientFromConnectionString(connectionString, nil)
	if err != nil {
		return nil, fmt.Errorf("aztable: creating service client: %w", err)
	}
	ts := &TableStore{
		svc:             svc,
		supersededCache: make(map[string]supersededCacheEntry),
	}
	tables := []struct {
		name   string
		target **aztables.Client
	}{
		{"CampfireMemberships", &ts.memberships},
		{"CampfireMessages", &ts.messages},
		{"CampfireReadCursors", &ts.cursors},
		{"CampfirePeerEndpoints", &ts.peers},
		{"CampfireThresholds", &ts.thresholds},
		{"CampfirePendingShares", &ts.pending},
		{"CampfireEpochs", &ts.epochs},
		{"CampfireFilters", &ts.filters},
		{"CampfireInvites", &ts.invites},
		{storageCountersTable, &ts.counters},
	}
	ctx := context.Background()
	for _, t := range tables {
		client := svc.NewClient(t.name)
		_, createErr := client.CreateTable(ctx, nil)
		if createErr != nil {
			if !isTableExistsError(createErr) {
				return nil, fmt.Errorf("aztable: ensuring table %s: %w", t.name, createErr)
			}
		}
		*t.target = client
	}
	return ts, nil
}

// Close is a no-op for the Azure Table Storage backend.
func (ts *TableStore) Close() error { return nil }

// ---------------------------------------------------------------------------
// MembershipStore
// ---------------------------------------------------------------------------

// AddMembership inserts a new membership record.
func (ts *TableStore) AddMembership(m store.Membership) error {
	threshold := m.Threshold
	if threshold == 0 {
		threshold = 1
	}
	enc := 0
	if m.Encrypted {
		enc = 1
	}
	entity := map[string]any{
		"PartitionKey":  ts.pk(m.CampfireID),
		"RowKey":        "membership",
		"CampfireID":    m.CampfireID,
		"TransportDir":  m.TransportDir,
		"JoinProtocol": m.JoinProtocol,
		"Role":          m.Role,
		"JoinedAt":      m.JoinedAt,
		"Threshold":     int64(threshold),
		"Description":   m.Description,
		"CreatorPubkey": m.CreatorPubkey,
		"TransportType": m.TransportType,
		"Encrypted":     int64(enc),
	}
	return upsertEntity(context.Background(), ts.memberships, entity)
}

// UpdateMembershipRole updates the role of an existing membership.
func (ts *TableStore) UpdateMembershipRole(campfireID, role string) error {
	m, err := ts.GetMembership(campfireID)
	if err != nil {
		return fmt.Errorf("aztable: UpdateMembershipRole get: %w", err)
	}
	if m == nil {
		return fmt.Errorf("membership not found: %s", campfireID)
	}
	m.Role = role
	return ts.AddMembership(*m)
}

// RemoveMembership deletes a campfire membership.
func (ts *TableStore) RemoveMembership(campfireID string) error {
	return deleteEntity(context.Background(), ts.memberships, ts.pk(campfireID), "membership")
}

// GetMembership retrieves a single membership by campfire ID.
func (ts *TableStore) GetMembership(campfireID string) (*store.Membership, error) {
	raw, err := getEntity(context.Background(), ts.memberships, ts.pk(campfireID), "membership")
	if err != nil {
		return nil, fmt.Errorf("aztable: GetMembership: %w", err)
	}
	if raw == nil {
		return nil, nil
	}
	return membershipFromEntity(raw)
}

// ListMemberships returns all memberships, ordered by JoinedAt.
func (ts *TableStore) ListMemberships() ([]store.Membership, error) {
	opts := &aztables.ListEntitiesOptions{}
	if f := ts.nsPKFilter(); f != "" {
		opts.Filter = strPtr(f)
	}
	pager := ts.memberships.NewListEntitiesPager(opts)
	var memberships []store.Membership
	ctx := context.Background()
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("aztable: ListMemberships: %w", err)
		}
		for _, raw := range page.Entities {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				return nil, fmt.Errorf("aztable: ListMemberships unmarshal: %w", err)
			}
			mem, err := membershipFromEntity(m)
			if err != nil {
				return nil, err
			}
			memberships = append(memberships, *mem)
		}
	}
	sort.Slice(memberships, func(i, j int) bool {
		return memberships[i].JoinedAt < memberships[j].JoinedAt
	})
	return memberships, nil
}

// ---------------------------------------------------------------------------
// MessageStore
// ---------------------------------------------------------------------------

// AddMessage inserts a message if not already present. Returns true if inserted.
// Enforces downgrade prevention: rejects plaintext messages in encrypted campfires.
func (ts *TableStore) AddMessage(m store.MessageRecord) (bool, error) {
	if m.Tags == nil {
		m.Tags = []string{}
	}
	if m.Antecedents == nil {
		m.Antecedents = []string{}
	}
	if m.Provenance == nil {
		m.Provenance = []message.ProvenanceHop{}
	}

	// Downgrade prevention.
	if !isSystemMessage(m.Tags) {
		mem, err := ts.GetMembership(m.CampfireID)
		if err != nil {
			return false, fmt.Errorf("aztable: AddMessage downgrade check: %w", err)
		}
		if mem != nil && mem.Encrypted {
			if _, unmarshalErr := unmarshalEncryptedPayload(m.Payload); unmarshalErr != nil {
				return false, fmt.Errorf("%w: campfire %s requires encrypted payload",
					store.ErrPlaintextInEncryptedCampfire, m.CampfireID)
			}
		}
	}

	// Validate compaction BytesSuperseded consistency before persisting.
	if isCompactionEvent(m) {
		var cp compactionPayload
		if err := json.Unmarshal(m.Payload, &cp); err == nil {
			if err := store.ValidateCompactionBytes(cp.Supersedes, cp.BytesSuperseded, func(id string) ([]byte, error) {
				msg, err := ts.GetMessage(id)
				if err != nil {
					return nil, err
				}
				if msg == nil {
					return nil, nil
				}
				return msg.Payload, nil
			}); err != nil {
				return false, err
			}
		}
	}

	// Check if already exists.
	existing, err := getEntity(context.Background(), ts.messages, ts.pk(m.CampfireID), encodeKey(m.ID))
	if err != nil {
		return false, fmt.Errorf("aztable: AddMessage check existing: %w", err)
	}
	if existing != nil {
		return false, nil
	}

	tagsJSON, _ := json.Marshal(m.Tags)
	anteJSON, _ := json.Marshal(m.Antecedents)
	provJSON, _ := json.Marshal(m.Provenance)

	entity := map[string]any{
		"PartitionKey":     ts.pk(m.CampfireID),
		"RowKey":           encodeKey(m.ID),
		"MessageID":        m.ID,
		"CampfireID":       m.CampfireID,
		"Sender":           m.Sender,
		"Tags":             string(tagsJSON),
		"Antecedents":      string(anteJSON),
		"Timestamp":        m.Timestamp,
		"Provenance":       string(provJSON),
		"ReceivedAt":       m.ReceivedAt,
		"Instance":         m.Instance,
		"SenderCampfireID": m.SenderCampfireID,
	}
	// Chunk large payload and signature.
	setChunked(entity, "Payload", m.Payload)
	setChunked(entity, "Signature", m.Signature)

	if err := insertEntity(context.Background(), ts.messages, entity); err != nil {
		return false, fmt.Errorf("aztable: AddMessage insert: %w", err)
	}

	// Increment per-campfire storage counter. This is best-effort: a counter
	// update failure does not roll back the message insert (fail-open for metering).
	if ts.counters != nil {
		if counterErr := ts.incrementStorageCounter(context.Background(), m.CampfireID, int64(len(m.Payload))); counterErr != nil {
			// Log but do not fail the write — metering is never a blocker.
			_ = counterErr
		}
	}

	// Invalidate superseded cache for compaction events and decrement storage counter.
	if isCompactionEvent(m) {
		ts.mu.Lock()
		delete(ts.supersededCache, ts.pk(m.CampfireID))
		ts.mu.Unlock()

		// Decrement the storage counter by the bytes and messages superseded (if provided).
		if ts.counters != nil && len(m.Payload) > 0 {
			var cp compactionPayload
			if err := json.Unmarshal(m.Payload, &cp); err == nil && (cp.BytesSuperseded > 0 || len(cp.Supersedes) > 0) {
				if counterErr := ts.decrementStorageCounter(context.Background(), m.CampfireID, cp.BytesSuperseded, int64(len(cp.Supersedes))); counterErr != nil {
					// Best-effort: do not fail the write for a counter update failure.
					_ = counterErr
				}
			}
		}
	}
	return true, nil
}

// HasMessage checks whether a message ID exists.
func (ts *TableStore) HasMessage(id string) (bool, error) {
	// We must search across all campfires since we don't know the campfire.
	// Use ListMessages-style scan with a row key filter, restricted to namespace.
	rkFilter := fmt.Sprintf("RowKey eq '%s'", encodeKey(id))
	filterStr := rkFilter
	if nsF := ts.nsPKFilter(); nsF != "" {
		filterStr = nsF + " and " + rkFilter
	}
	opts := &aztables.ListEntitiesOptions{
		Filter: strPtr(filterStr),
	}
	pager := ts.messages.NewListEntitiesPager(opts)
	ctx := context.Background()
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return false, fmt.Errorf("aztable: HasMessage: %w", err)
		}
		if len(page.Entities) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// GetMessage retrieves a message by ID (searches across all campfires in the namespace).
func (ts *TableStore) GetMessage(id string) (*store.MessageRecord, error) {
	rkFilter := fmt.Sprintf("RowKey eq '%s'", encodeKey(id))
	filterStr := rkFilter
	if nsF := ts.nsPKFilter(); nsF != "" {
		filterStr = nsF + " and " + rkFilter
	}
	opts := &aztables.ListEntitiesOptions{
		Filter: strPtr(filterStr),
	}
	pager := ts.messages.NewListEntitiesPager(opts)
	ctx := context.Background()
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("aztable: GetMessage: %w", err)
		}
		for _, raw := range page.Entities {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				return nil, fmt.Errorf("aztable: GetMessage unmarshal: %w", err)
			}
			rec, err := messageFromEntity(m)
			if err != nil {
				return nil, err
			}
			return rec, nil
		}
	}
	return nil, nil
}

// GetMessageByPrefix resolves a message ID prefix to a single message.
// Returns an error if the prefix is ambiguous.
func (ts *TableStore) GetMessageByPrefix(prefix string) (*store.MessageRecord, error) {
	// Table Storage row keys are exact; we must scan and filter client-side.
	var opts *aztables.ListEntitiesOptions
	if nsF := ts.nsPKFilter(); nsF != "" {
		opts = &aztables.ListEntitiesOptions{Filter: strPtr(nsF)}
	}
	pager := ts.messages.NewListEntitiesPager(opts)
	ctx := context.Background()
	var matches []*store.MessageRecord
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("aztable: GetMessageByPrefix: %w", err)
		}
		for _, raw := range page.Entities {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				return nil, fmt.Errorf("aztable: GetMessageByPrefix unmarshal: %w", err)
			}
			msgID, _ := m["MessageID"].(string)
			if !strings.HasPrefix(msgID, prefix) {
				continue
			}
			rec, err := messageFromEntity(m)
			if err != nil {
				return nil, err
			}
			matches = append(matches, rec)
			if len(matches) > 1 {
				return nil, fmt.Errorf("ambiguous message ID prefix %s, matches multiple messages", prefix)
			}
		}
	}
	if len(matches) == 0 {
		return nil, nil
	}
	return matches[0], nil
}

// ListMessages returns messages for a campfire, ordered by timestamp.
// If campfireID is empty, returns messages across all campfires.
func (ts *TableStore) ListMessages(campfireID string, afterTimestamp int64, filter ...store.MessageFilter) ([]store.MessageRecord, error) {
	var f store.MessageFilter
	if len(filter) > 0 {
		f = filter[0]
	}

	var filterStr string
	if campfireID != "" {
		filterStr = fmt.Sprintf("PartitionKey eq '%s'", ts.pk(campfireID))
	} else if nsF := ts.nsPKFilter(); nsF != "" {
		filterStr = nsF
	}

	opts := &aztables.ListEntitiesOptions{}
	if filterStr != "" {
		opts.Filter = strPtr(filterStr)
	}

	pager := ts.messages.NewListEntitiesPager(opts)
	ctx := context.Background()
	var msgs []store.MessageRecord
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("aztable: ListMessages: %w", err)
		}
		for _, raw := range page.Entities {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				return nil, fmt.Errorf("aztable: ListMessages unmarshal: %w", err)
			}
			rec, err := messageFromEntity(m)
			if err != nil {
				return nil, err
			}

			// Apply timestamp filter.
			if f.AfterReceivedAt > 0 {
				if rec.ReceivedAt <= f.AfterReceivedAt {
					continue
				}
			} else if rec.Timestamp <= afterTimestamp {
				continue
			}

			// Apply tag include filter (OR across exact + prefix).
			if (len(f.Tags) > 0 || len(f.TagPrefixes) > 0) && !hasAnyTagOrPrefix(rec.Tags, f.Tags, f.TagPrefixes) {
				continue
			}

			// Apply tag exclude filter.
			if len(f.ExcludeTags) > 0 && hasAnyTag(rec.Tags, f.ExcludeTags) {
				continue
			}
			if len(f.ExcludeTagPrefixes) > 0 && hasAnyTagPrefix(rec.Tags, f.ExcludeTagPrefixes) {
				continue
			}

			// Apply sender prefix filter.
			if f.Sender != "" && !strings.HasPrefix(strings.ToLower(rec.Sender), strings.ToLower(f.Sender)) {
				continue
			}

			msgs = append(msgs, *rec)
		}
	}

	// Sort by timestamp.
	sort.Slice(msgs, func(i, j int) bool {
		return msgs[i].Timestamp < msgs[j].Timestamp
	})

	if !f.RespectCompaction {
		return msgs, nil
	}

	superseded, err := ts.collectSupersededIDs(campfireID)
	if err != nil {
		return nil, fmt.Errorf("aztable: ListMessages compaction filter: %w", err)
	}
	if len(superseded) == 0 {
		return msgs, nil
	}
	filtered := msgs[:0]
	for _, m := range msgs {
		if superseded[m.ID] && !isCompactionEvent(m) {
			continue
		}
		filtered = append(filtered, m)
	}
	return filtered, nil
}

// MaxMessageTimestamp returns the maximum timestamp among all messages for a campfire.
func (ts *TableStore) MaxMessageTimestamp(campfireID string, afterTS int64) (int64, error) {
	msgs, err := ts.ListMessages(campfireID, afterTS)
	if err != nil {
		return 0, err
	}
	var maxTS int64
	for _, m := range msgs {
		if m.Timestamp > maxTS {
			maxTS = m.Timestamp
		}
	}
	return maxTS, nil
}

// ListReferencingMessages finds messages whose antecedents contain the given message ID.
func (ts *TableStore) ListReferencingMessages(messageID string) ([]store.MessageRecord, error) {
	pager := ts.messages.NewListEntitiesPager(nil)
	ctx := context.Background()
	var result []store.MessageRecord
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("aztable: ListReferencingMessages: %w", err)
		}
		for _, raw := range page.Entities {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				return nil, fmt.Errorf("aztable: ListReferencingMessages unmarshal: %w", err)
			}
			rec, err := messageFromEntity(m)
			if err != nil {
				return nil, err
			}
			for _, a := range rec.Antecedents {
				if a == messageID {
					result = append(result, *rec)
					break
				}
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Timestamp < result[j].Timestamp
	})
	return result, nil
}

// ListCompactionEvents returns all campfire:compact messages for a campfire.
func (ts *TableStore) ListCompactionEvents(campfireID string) ([]store.MessageRecord, error) {
	all, err := ts.ListMessages(campfireID, 0)
	if err != nil {
		return nil, err
	}
	var events []store.MessageRecord
	for _, m := range all {
		if isCompactionEvent(m) {
			events = append(events, m)
		}
	}
	return events, nil
}

// GetReadCursor returns the last-read timestamp for a campfire. Returns 0 if absent.
func (ts *TableStore) GetReadCursor(campfireID string) (int64, error) {
	raw, err := getEntity(context.Background(), ts.cursors, ts.pk(campfireID), "cursor")
	if err != nil {
		return 0, fmt.Errorf("aztable: GetReadCursor: %w", err)
	}
	if raw == nil {
		return 0, nil
	}
	// Stored as string to avoid float64 precision loss for nanosecond timestamps.
	if s, ok := raw["LastReadAt"].(string); ok {
		v, _ := strconv.ParseInt(s, 10, 64)
		return v, nil
	}
	// Legacy: handle numeric values from older writes.
	ts64, _ := raw["LastReadAt"].(float64)
	return int64(ts64), nil
}

// SetReadCursor updates the read cursor for a campfire.
func (ts *TableStore) SetReadCursor(campfireID string, timestamp int64) error {
	entity := map[string]any{
		"PartitionKey": ts.pk(campfireID),
		"RowKey":       "cursor",
		"CampfireID":   campfireID,
		"LastReadAt":   strconv.FormatInt(timestamp, 10),
	}
	return upsertEntity(context.Background(), ts.cursors, entity)
}

// ---------------------------------------------------------------------------
// PeerStore
// ---------------------------------------------------------------------------

// UpsertPeerEndpoint inserts or updates a peer endpoint.
func (ts *TableStore) UpsertPeerEndpoint(e store.PeerEndpoint) error {
	role := e.Role
	if role == "" {
		role = store.PeerRoleMember
	}
	entity := map[string]any{
		"PartitionKey":  ts.pk(e.CampfireID),
		"RowKey":        encodeKey(e.MemberPubkey),
		"CampfireID":    e.CampfireID,
		"MemberPubkey":  e.MemberPubkey,
		"Endpoint":      e.Endpoint,
		"ParticipantID": int64(e.ParticipantID),
		"Role":          role,
	}
	return upsertEntity(context.Background(), ts.peers, entity)
}

// DeletePeerEndpoint removes a peer endpoint.
func (ts *TableStore) DeletePeerEndpoint(campfireID, memberPubkey string) error {
	return deleteEntity(context.Background(), ts.peers, ts.pk(campfireID), encodeKey(memberPubkey))
}

// ListPeerEndpoints returns all known peer endpoints for a campfire.
func (ts *TableStore) ListPeerEndpoints(campfireID string) ([]store.PeerEndpoint, error) {
	opts := &aztables.ListEntitiesOptions{
		Filter: strPtr(fmt.Sprintf("PartitionKey eq '%s'", ts.pk(campfireID))),
	}
	pager := ts.peers.NewListEntitiesPager(opts)
	ctx := context.Background()
	var endpoints []store.PeerEndpoint
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("aztable: ListPeerEndpoints: %w", err)
		}
		for _, raw := range page.Entities {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				return nil, fmt.Errorf("aztable: ListPeerEndpoints unmarshal: %w", err)
			}
			e := peerEndpointFromEntity(m)
			endpoints = append(endpoints, e)
		}
	}
	return endpoints, nil
}

// GetPeerRole returns the role of a specific member.
func (ts *TableStore) GetPeerRole(campfireID, memberPubkey string) (string, error) {
	raw, err := getEntity(context.Background(), ts.peers, ts.pk(campfireID), encodeKey(memberPubkey))
	if err != nil {
		return "", fmt.Errorf("aztable: GetPeerRole: %w", err)
	}
	if raw == nil {
		return store.PeerRoleMember, nil
	}
	role, _ := raw["Role"].(string)
	if role == "" {
		role = store.PeerRoleMember
	}
	return role, nil
}

// ---------------------------------------------------------------------------
// Metering helpers
// ---------------------------------------------------------------------------

// CampfirePeerCount holds the peer endpoint count for a single campfire.
// Used by the daily peer-endpoint metering timer.
type CampfirePeerCount struct {
	CampfireID string
	Count      int
}

// ListCampfirePeerCounts scans the CampfirePeerEndpoints table and returns
// the number of peer endpoint rows per campfire. Only campfires with at least
// one peer endpoint are included.
func (ts *TableStore) ListCampfirePeerCounts(ctx context.Context) ([]CampfirePeerCount, error) {
	pager := ts.peers.NewListEntitiesPager(nil)
	counts := make(map[string]int) // campfireID → count
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("aztable: ListCampfirePeerCounts: %w", err)
		}
		for _, raw := range page.Entities {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				return nil, fmt.Errorf("aztable: ListCampfirePeerCounts unmarshal: %w", err)
			}
			cid, _ := m["CampfireID"].(string)
			if cid != "" {
				counts[cid]++
			}
		}
	}
	results := make([]CampfirePeerCount, 0, len(counts))
	for cid, n := range counts {
		results = append(results, CampfirePeerCount{CampfireID: cid, Count: n})
	}
	return results, nil
}

// DeleteMessage removes a single message entity from the messages table.
// campfireID and messageID identify the entity (PK + RK). Ignores not-found.
func (ts *TableStore) DeleteMessage(ctx context.Context, campfireID, messageID string) error {
	return deleteEntity(ctx, ts.messages, ts.pk(campfireID), encodeKey(messageID))
}

// ListMessagesOlderThan returns all messages whose ReceivedAt timestamp is
// before cutoff (nanoseconds). If campfireID is non-empty, only that campfire
// is scanned; otherwise all campfires are scanned.
// Note: this can be expensive on large deployments — use only from timer jobs.
func (ts *TableStore) ListMessagesOlderThan(ctx context.Context, campfireID string, cutoff int64) ([]store.MessageRecord, error) {
	var filterStr string
	if campfireID != "" {
		filterStr = fmt.Sprintf("PartitionKey eq '%s'", ts.pk(campfireID))
	}
	opts := &aztables.ListEntitiesOptions{}
	if filterStr != "" {
		opts.Filter = strPtr(filterStr)
	}
	pager := ts.messages.NewListEntitiesPager(opts)
	var results []store.MessageRecord
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("aztable: ListMessagesOlderThan: %w", err)
		}
		for _, raw := range page.Entities {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				return nil, fmt.Errorf("aztable: ListMessagesOlderThan unmarshal: %w", err)
			}
			rec, err := messageFromEntity(m)
			if err != nil {
				return nil, err
			}
			if rec.ReceivedAt > 0 && rec.ReceivedAt < cutoff {
				results = append(results, *rec)
			} else if rec.ReceivedAt == 0 && rec.Timestamp < cutoff {
				// Legacy: use Timestamp if ReceivedAt is absent.
				results = append(results, *rec)
			}
		}
	}
	return results, nil
}

// ---------------------------------------------------------------------------
// ThresholdStore
// ---------------------------------------------------------------------------

// UpsertThresholdShare stores or replaces FROST DKG share data.
func (ts *TableStore) UpsertThresholdShare(share store.ThresholdShare) error {
	entity := map[string]any{
		"PartitionKey":  ts.pk(share.CampfireID),
		"RowKey":        "share",
		"CampfireID":    share.CampfireID,
		"ParticipantID": int64(share.ParticipantID),
	}
	setChunked(entity, "SecretShare", share.SecretShare)
	setChunked(entity, "PublicData", share.PublicData)
	return upsertEntity(context.Background(), ts.thresholds, entity)
}

// GetThresholdShare retrieves FROST DKG share data. Returns nil if absent.
func (ts *TableStore) GetThresholdShare(campfireID string) (*store.ThresholdShare, error) {
	raw, err := getEntity(context.Background(), ts.thresholds, ts.pk(campfireID), "share")
	if err != nil {
		return nil, fmt.Errorf("aztable: GetThresholdShare: %w", err)
	}
	if raw == nil {
		return nil, nil
	}
	pid, _ := raw["ParticipantID"].(float64)
	secretShare := getChunked(raw, "SecretShare")
	publicData := getChunked(raw, "PublicData")
	return &store.ThresholdShare{
		CampfireID:    campfireID,
		ParticipantID: uint32(pid),
		SecretShare:   secretShare,
		PublicData:    publicData,
	}, nil
}

// StorePendingThresholdShare stores a DKG share for a future joiner.
func (ts *TableStore) StorePendingThresholdShare(campfireID string, participantID uint32, shareData []byte) error {
	rk := fmt.Sprintf("%0*d", participantPadWidth, participantID)
	entity := map[string]any{
		"PartitionKey":  ts.pk(campfireID),
		"RowKey":        rk,
		"CampfireID":    campfireID,
		"ParticipantID": int64(participantID),
	}
	setChunked(entity, "ShareData", shareData)
	return upsertEntity(context.Background(), ts.pending, entity)
}

// ClaimPendingThresholdShare retrieves and removes the next available pending share.
// Returns (0, nil, nil) if none available.
func (ts *TableStore) ClaimPendingThresholdShare(campfireID string) (uint32, []byte, error) {
	opts := &aztables.ListEntitiesOptions{
		Filter: strPtr(fmt.Sprintf("PartitionKey eq '%s'", ts.pk(campfireID))),
		Top:    int32Ptr(1),
	}
	pager := ts.pending.NewListEntitiesPager(opts)
	ctx := context.Background()
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return 0, nil, fmt.Errorf("aztable: ClaimPendingThresholdShare list: %w", err)
		}
		for _, raw := range page.Entities {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				return 0, nil, fmt.Errorf("aztable: ClaimPendingThresholdShare unmarshal: %w", err)
			}
			rk, _ := m["RowKey"].(string)
			pid, _ := m["ParticipantID"].(float64)
			shareData := getChunked(m, "ShareData")

			// Delete the claimed row.
			if delErr := deleteEntity(ctx, ts.pending, ts.pk(campfireID), rk); delErr != nil {
				return 0, nil, fmt.Errorf("aztable: ClaimPendingThresholdShare delete: %w", delErr)
			}
			return uint32(pid), shareData, nil
		}
	}
	return 0, nil, nil
}

// ---------------------------------------------------------------------------
// EpochSecretStore
// ---------------------------------------------------------------------------

// UpsertEpochSecret stores or updates the root secret and CEK for (campfire, epoch).
func (ts *TableStore) UpsertEpochSecret(secret store.EpochSecret) error {
	rk := fmt.Sprintf("%0*d", epochPadWidth, secret.Epoch)
	entity := map[string]any{
		"PartitionKey": ts.pk(secret.CampfireID),
		"RowKey":       rk,
		"CampfireID":   secret.CampfireID,
		"Epoch":        fmt.Sprintf("%d", secret.Epoch),
		"CreatedAt":    secret.CreatedAt,
	}
	setChunked(entity, "RootSecret", secret.RootSecret)
	setChunked(entity, "CEK", secret.CEK)
	return upsertEntity(context.Background(), ts.epochs, entity)
}

// GetEpochSecret retrieves the epoch secret for (campfireID, epoch). Returns nil if absent.
func (ts *TableStore) GetEpochSecret(campfireID string, epoch uint64) (*store.EpochSecret, error) {
	rk := fmt.Sprintf("%0*d", epochPadWidth, epoch)
	raw, err := getEntity(context.Background(), ts.epochs, ts.pk(campfireID), rk)
	if err != nil {
		return nil, fmt.Errorf("aztable: GetEpochSecret: %w", err)
	}
	if raw == nil {
		return nil, nil
	}
	return epochSecretFromEntity(raw, campfireID)
}

// GetLatestEpochSecret returns the highest-epoch secret for campfireID. Returns nil if absent.
func (ts *TableStore) GetLatestEpochSecret(campfireID string) (*store.EpochSecret, error) {
	opts := &aztables.ListEntitiesOptions{
		Filter: strPtr(fmt.Sprintf("PartitionKey eq '%s'", ts.pk(campfireID))),
	}
	pager := ts.epochs.NewListEntitiesPager(opts)
	ctx := context.Background()
	var latest *store.EpochSecret
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("aztable: GetLatestEpochSecret: %w", err)
		}
		for _, raw := range page.Entities {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				return nil, fmt.Errorf("aztable: GetLatestEpochSecret unmarshal: %w", err)
			}
			es, err := epochSecretFromEntity(m, campfireID)
			if err != nil {
				return nil, err
			}
			if latest == nil || es.Epoch > latest.Epoch {
				latest = es
			}
		}
	}
	return latest, nil
}

// SetMembershipEncrypted sets the encrypted flag for a campfire membership.
func (ts *TableStore) SetMembershipEncrypted(campfireID string, encrypted bool) error {
	m, err := ts.GetMembership(campfireID)
	if err != nil {
		return fmt.Errorf("aztable: SetMembershipEncrypted get: %w", err)
	}
	if m == nil {
		return fmt.Errorf("membership not found: %s", campfireID)
	}
	m.Encrypted = encrypted
	return ts.AddMembership(*m)
}

// ApplyMembershipCommitAtomically installs an epoch secret and optionally upserts
// a membership record. Table Storage has no cross-entity transactions across
// partitions; we do a best-effort two-step with a compensating rollback on failure.
// Within the same partition, Table Storage batch transactions could be used, but
// memberships and epochs live in different tables, so we accept the limitation:
// if the epoch upsert succeeds and the membership upsert fails, the epoch may be
// orphaned. Callers should treat partial failures as transient and retry.
func (ts *TableStore) ApplyMembershipCommitAtomically(campfireID string, newMember *store.Membership, secret store.EpochSecret) error {
	if err := ts.UpsertEpochSecret(secret); err != nil {
		return fmt.Errorf("aztable: ApplyMembershipCommitAtomically upsert epoch: %w", err)
	}
	if newMember != nil {
		if err := ts.AddMembership(*newMember); err != nil {
			return fmt.Errorf("aztable: ApplyMembershipCommitAtomically upsert membership: %w", err)
		}
	}
	return nil
}

// UpdateCampfireID renames all records from oldID to newID.
// Table Storage does not support row key updates — we must copy+delete.
func (ts *TableStore) UpdateCampfireID(oldID, newID string) error {
	ctx := context.Background()

	// 1. Memberships
	if err := ts.renameMembership(ctx, oldID, newID); err != nil {
		return err
	}
	// 2. Messages
	if err := ts.renameMessages(ctx, oldID, newID); err != nil {
		return err
	}
	// 3. Read cursors
	if err := ts.renameCursor(ctx, oldID, newID); err != nil {
		return err
	}
	// 4. Peers
	if err := ts.renamePeers(ctx, oldID, newID); err != nil {
		return err
	}
	// 5. Thresholds
	if err := ts.renameThreshold(ctx, oldID, newID); err != nil {
		return err
	}
	// 6. Pending shares
	if err := ts.renamePendingShares(ctx, oldID, newID); err != nil {
		return err
	}
	// 7. Epoch secrets
	if err := ts.renameEpochs(ctx, oldID, newID); err != nil {
		return err
	}
	// 8. Filters
	if err := ts.renameFilters(ctx, oldID, newID); err != nil {
		return err
	}

	// Evict superseded cache.
	ts.mu.Lock()
	delete(ts.supersededCache, ts.pk(oldID))
	delete(ts.supersededCache, ts.pk(newID))
	ts.mu.Unlock()

	return nil
}

// ---------------------------------------------------------------------------
// Internal rename helpers
// ---------------------------------------------------------------------------

func (ts *TableStore) renameMembership(ctx context.Context, oldID, newID string) error {
	raw, err := getEntity(ctx, ts.memberships, ts.pk(oldID), "membership")
	if err != nil || raw == nil {
		return err
	}
	raw["PartitionKey"] = ts.pk(newID)
	raw["CampfireID"] = newID
	if err := upsertEntity(ctx, ts.memberships, raw); err != nil {
		return fmt.Errorf("aztable: UpdateCampfireID copy membership: %w", err)
	}
	return deleteEntity(ctx, ts.memberships, ts.pk(oldID), "membership")
}

func (ts *TableStore) renameMessages(ctx context.Context, oldID, newID string) error {
	opts := &aztables.ListEntitiesOptions{
		Filter: strPtr(fmt.Sprintf("PartitionKey eq '%s'", ts.pk(oldID))),
	}
	pager := ts.messages.NewListEntitiesPager(opts)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("aztable: UpdateCampfireID list messages: %w", err)
		}
		for _, raw := range page.Entities {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				return err
			}
			rk, _ := m["RowKey"].(string)
			m["PartitionKey"] = ts.pk(newID)
			m["CampfireID"] = newID
			if err := upsertEntity(ctx, ts.messages, m); err != nil {
				return fmt.Errorf("aztable: UpdateCampfireID copy message: %w", err)
			}
			if err := deleteEntity(ctx, ts.messages, ts.pk(oldID), rk); err != nil {
				return fmt.Errorf("aztable: UpdateCampfireID delete old message: %w", err)
			}
		}
	}
	return nil
}

func (ts *TableStore) renameCursor(ctx context.Context, oldID, newID string) error {
	raw, err := getEntity(ctx, ts.cursors, ts.pk(oldID), "cursor")
	if err != nil || raw == nil {
		return err
	}
	raw["PartitionKey"] = ts.pk(newID)
	raw["CampfireID"] = newID
	if err := upsertEntity(ctx, ts.cursors, raw); err != nil {
		return fmt.Errorf("aztable: UpdateCampfireID copy cursor: %w", err)
	}
	return deleteEntity(ctx, ts.cursors, ts.pk(oldID), "cursor")
}

func (ts *TableStore) renamePeers(ctx context.Context, oldID, newID string) error {
	opts := &aztables.ListEntitiesOptions{
		Filter: strPtr(fmt.Sprintf("PartitionKey eq '%s'", ts.pk(oldID))),
	}
	pager := ts.peers.NewListEntitiesPager(opts)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("aztable: UpdateCampfireID list peers: %w", err)
		}
		for _, raw := range page.Entities {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				return err
			}
			rk, _ := m["RowKey"].(string)
			m["PartitionKey"] = ts.pk(newID)
			m["CampfireID"] = newID
			if err := upsertEntity(ctx, ts.peers, m); err != nil {
				return fmt.Errorf("aztable: UpdateCampfireID copy peer: %w", err)
			}
			if err := deleteEntity(ctx, ts.peers, ts.pk(oldID), rk); err != nil {
				return fmt.Errorf("aztable: UpdateCampfireID delete old peer: %w", err)
			}
		}
	}
	return nil
}

func (ts *TableStore) renameThreshold(ctx context.Context, oldID, newID string) error {
	raw, err := getEntity(ctx, ts.thresholds, ts.pk(oldID), "share")
	if err != nil || raw == nil {
		return err
	}
	raw["PartitionKey"] = ts.pk(newID)
	raw["CampfireID"] = newID
	if err := upsertEntity(ctx, ts.thresholds, raw); err != nil {
		return fmt.Errorf("aztable: UpdateCampfireID copy threshold: %w", err)
	}
	return deleteEntity(ctx, ts.thresholds, ts.pk(oldID), "share")
}

func (ts *TableStore) renamePendingShares(ctx context.Context, oldID, newID string) error {
	opts := &aztables.ListEntitiesOptions{
		Filter: strPtr(fmt.Sprintf("PartitionKey eq '%s'", ts.pk(oldID))),
	}
	pager := ts.pending.NewListEntitiesPager(opts)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("aztable: UpdateCampfireID list pending shares: %w", err)
		}
		for _, raw := range page.Entities {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				return err
			}
			rk, _ := m["RowKey"].(string)
			m["PartitionKey"] = ts.pk(newID)
			m["CampfireID"] = newID
			if err := upsertEntity(ctx, ts.pending, m); err != nil {
				return fmt.Errorf("aztable: UpdateCampfireID copy pending share: %w", err)
			}
			if err := deleteEntity(ctx, ts.pending, ts.pk(oldID), rk); err != nil {
				return fmt.Errorf("aztable: UpdateCampfireID delete old pending share: %w", err)
			}
		}
	}
	return nil
}

func (ts *TableStore) renameEpochs(ctx context.Context, oldID, newID string) error {
	opts := &aztables.ListEntitiesOptions{
		Filter: strPtr(fmt.Sprintf("PartitionKey eq '%s'", ts.pk(oldID))),
	}
	pager := ts.epochs.NewListEntitiesPager(opts)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("aztable: UpdateCampfireID list epochs: %w", err)
		}
		for _, raw := range page.Entities {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				return err
			}
			rk, _ := m["RowKey"].(string)
			m["PartitionKey"] = ts.pk(newID)
			m["CampfireID"] = newID
			if err := upsertEntity(ctx, ts.epochs, m); err != nil {
				return fmt.Errorf("aztable: UpdateCampfireID copy epoch: %w", err)
			}
			if err := deleteEntity(ctx, ts.epochs, ts.pk(oldID), rk); err != nil {
				return fmt.Errorf("aztable: UpdateCampfireID delete old epoch: %w", err)
			}
		}
	}
	return nil
}

func (ts *TableStore) renameFilters(ctx context.Context, oldID, newID string) error {
	opts := &aztables.ListEntitiesOptions{
		Filter: strPtr(fmt.Sprintf("PartitionKey eq '%s'", ts.pk(oldID))),
	}
	pager := ts.filters.NewListEntitiesPager(opts)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("aztable: UpdateCampfireID list filters: %w", err)
		}
		for _, raw := range page.Entities {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				return err
			}
			rk, _ := m["RowKey"].(string)
			m["PartitionKey"] = ts.pk(newID)
			m["CampfireID"] = newID
			if err := upsertEntity(ctx, ts.filters, m); err != nil {
				return fmt.Errorf("aztable: UpdateCampfireID copy filter: %w", err)
			}
			if err := deleteEntity(ctx, ts.filters, ts.pk(oldID), rk); err != nil {
				return fmt.Errorf("aztable: UpdateCampfireID delete old filter: %w", err)
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Internal: compaction cache
// ---------------------------------------------------------------------------

func (ts *TableStore) maxCompactionTimestamp(campfireID string) (int64, error) {
	events, err := ts.ListCompactionEvents(campfireID)
	if err != nil {
		return 0, err
	}
	var max int64
	for _, e := range events {
		if e.Timestamp > max {
			max = e.Timestamp
		}
	}
	return max, nil
}

func (ts *TableStore) collectSupersededIDs(campfireID string) (map[string]bool, error) {
	if campfireID != "" {
		maxTS, err := ts.maxCompactionTimestamp(campfireID)
		if err != nil {
			return nil, err
		}
		if maxTS == 0 {
			return nil, nil
		}
		cacheKey := ts.pk(campfireID)
		ts.mu.RLock()
		entry, ok := ts.supersededCache[cacheKey]
		ts.mu.RUnlock()
		if ok && entry.maxCompactionTS == maxTS {
			cp := make(map[string]bool, len(entry.superseded))
			for k, v := range entry.superseded {
				cp[k] = v
			}
			return cp, nil
		}
		events, err := ts.ListCompactionEvents(campfireID)
		if err != nil {
			return nil, err
		}
		superseded := make(map[string]bool)
		for _, ev := range events {
			var payload compactionPayload
			if err := json.Unmarshal(ev.Payload, &payload); err != nil {
				continue
			}
			for _, id := range payload.Supersedes {
				superseded[id] = true
			}
		}
		ts.mu.Lock()
		if _, exists := ts.supersededCache[cacheKey]; !exists {
			ts.supersededCache[cacheKey] = supersededCacheEntry{
				maxCompactionTS: maxTS,
				superseded:      superseded,
			}
		}
		ts.mu.Unlock()
		cp := make(map[string]bool, len(superseded))
		for k, v := range superseded {
			cp[k] = v
		}
		return cp, nil
	}

	// Cross-campfire: no caching.
	events, err := ts.ListCompactionEvents("")
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, nil
	}
	superseded := make(map[string]bool)
	for _, ev := range events {
		var payload compactionPayload
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			continue
		}
		for _, id := range payload.Supersedes {
			superseded[id] = true
		}
	}
	return superseded, nil
}

type compactionPayload struct {
	Supersedes      []string `json:"supersedes"`
	BytesSuperseded int64    `json:"bytes_superseded,omitempty"`
}

// ---------------------------------------------------------------------------
// Table Storage helpers
// ---------------------------------------------------------------------------

// encodeKey replaces characters not allowed in Azure Table Storage partition/row keys.
// Forbidden characters: / \ # ? and control characters.
// We hex-encode the input to ensure safety.
func encodeKey(s string) string {
	// Simple approach: use the string as-is if safe, else hex-encode.
	// For simplicity we always hex-encode to avoid issues.
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			sb.WriteByte(c)
		} else {
			sb.WriteString(fmt.Sprintf("x%02x", c))
		}
	}
	return sb.String()
}

// getEntity retrieves a single entity by PK/RK. Returns nil if not found.
func getEntity(ctx context.Context, client *aztables.Client, pk, rk string) (map[string]any, error) {
	resp, err := client.GetEntity(ctx, pk, rk, nil)
	if err != nil {
		if isNotFoundError(err) {
			return nil, nil
		}
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(resp.Value, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// upsertEntity writes an entity using merge-or-insert semantics.
func upsertEntity(ctx context.Context, client *aztables.Client, entity map[string]any) error {
	data, err := json.Marshal(entity)
	if err != nil {
		return err
	}
	_, err = client.UpsertEntity(ctx, data, &aztables.UpsertEntityOptions{
		UpdateMode: aztables.UpdateModeReplace,
	})
	return err
}

// insertEntity writes an entity only if it doesn't exist (INSERT OR IGNORE semantics).
func insertEntity(ctx context.Context, client *aztables.Client, entity map[string]any) error {
	data, err := json.Marshal(entity)
	if err != nil {
		return err
	}
	_, err = client.AddEntity(ctx, data, nil)
	if err != nil {
		if isConflictError(err) {
			// Already exists — treat as success (idempotent insert).
			return nil
		}
		return err
	}
	return nil
}

// deleteEntity removes a single entity. Ignores not-found.
func deleteEntity(ctx context.Context, client *aztables.Client, pk, rk string) error {
	_, err := client.DeleteEntity(ctx, pk, rk, nil)
	if err != nil && !isNotFoundError(err) {
		return err
	}
	return nil
}

// setChunked stores a byte slice in an entity map as chunked properties.
// Properties are named <prefix>0, <prefix>1, ... with a ChunkCount<prefix> counter.
func setChunked(entity map[string]any, prefix string, data []byte) {
	if len(data) == 0 {
		entity[prefix+"ChunkCount"] = int64(0)
		return
	}
	count := 0
	for i := 0; i < len(data); i += chunkSize {
		end := i + chunkSize
		if end > len(data) {
			end = len(data)
		}
		entity[fmt.Sprintf("%s%d", prefix, count)] = data[i:end]
		count++
	}
	entity[prefix+"ChunkCount"] = int64(count)
}

// getChunked reassembles a byte slice from chunked entity properties.
func getChunked(entity map[string]any, prefix string) []byte {
	countRaw, ok := entity[prefix+"ChunkCount"]
	if !ok {
		return nil
	}
	count := int(toInt64(countRaw))
	if count == 0 {
		return nil
	}
	var result []byte
	for i := 0; i < count; i++ {
		key := fmt.Sprintf("%s%d", prefix, i)
		val, ok := entity[key]
		if !ok {
			break
		}
		switch v := val.(type) {
		case []byte:
			result = append(result, v...)
		case string:
			// Azure Table SDK returns binary properties as base64 strings.
			if decoded, err := base64.StdEncoding.DecodeString(v); err == nil {
				result = append(result, decoded...)
			} else {
				result = append(result, []byte(v)...)
			}
		}
	}
	return result
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	case string:
		var n int64
		fmt.Sscan(x, &n)
		return n
	}
	return 0
}

// membershipFromEntity converts a map from Table Storage to a store.Membership.
func membershipFromEntity(m map[string]any) (*store.Membership, error) {
	enc, _ := m["Encrypted"].(float64)
	threshold := toInt64(m["Threshold"])
	if threshold == 0 {
		threshold = 1
	}
	return &store.Membership{
		CampfireID:    str(m, "CampfireID"),
		TransportDir:  str(m, "TransportDir"),
		JoinProtocol:  str(m, "JoinProtocol"),
		Role:          str(m, "Role"),
		JoinedAt:      toInt64(m["JoinedAt"]),
		Threshold:     uint(threshold),
		Description:   str(m, "Description"),
		CreatorPubkey: str(m, "CreatorPubkey"),
		TransportType: str(m, "TransportType"),
		Encrypted:     enc != 0,
	}, nil
}

// messageFromEntity converts a map from Table Storage to a store.MessageRecord.
func messageFromEntity(m map[string]any) (*store.MessageRecord, error) {
	var tags []string
	if err := json.Unmarshal([]byte(str(m, "Tags")), &tags); err != nil || tags == nil {
		tags = []string{}
	}
	var antecedents []string
	if err := json.Unmarshal([]byte(str(m, "Antecedents")), &antecedents); err != nil || antecedents == nil {
		antecedents = []string{}
	}
	var provenance []message.ProvenanceHop
	if err := json.Unmarshal([]byte(str(m, "Provenance")), &provenance); err != nil || provenance == nil {
		provenance = []message.ProvenanceHop{}
	}
	payload := getChunked(m, "Payload")
	signature := getChunked(m, "Signature")
	return &store.MessageRecord{
		ID:               str(m, "MessageID"),
		CampfireID:       str(m, "CampfireID"),
		Sender:           str(m, "Sender"),
		Payload:          payload,
		Tags:             tags,
		Antecedents:      antecedents,
		Timestamp:        toInt64(m["Timestamp"]),
		Signature:        signature,
		Provenance:       provenance,
		ReceivedAt:       toInt64(m["ReceivedAt"]),
		Instance:         str(m, "Instance"),
		SenderCampfireID: str(m, "SenderCampfireID"),
	}, nil
}

// peerEndpointFromEntity converts a map to a store.PeerEndpoint.
func peerEndpointFromEntity(m map[string]any) store.PeerEndpoint {
	role := str(m, "Role")
	if role == "" {
		role = store.PeerRoleMember
	}
	pid := toInt64(m["ParticipantID"])
	return store.PeerEndpoint{
		CampfireID:    str(m, "CampfireID"),
		MemberPubkey:  str(m, "MemberPubkey"),
		Endpoint:      str(m, "Endpoint"),
		ParticipantID: uint32(pid),
		Role:          role,
	}
}

// epochSecretFromEntity converts a map to a store.EpochSecret.
func epochSecretFromEntity(m map[string]any, campfireID string) (*store.EpochSecret, error) {
	epochStr := str(m, "Epoch")
	var epoch uint64
	fmt.Sscan(epochStr, &epoch)
	return &store.EpochSecret{
		CampfireID: campfireID,
		Epoch:      epoch,
		RootSecret: getChunked(m, "RootSecret"),
		CEK:        getChunked(m, "CEK"),
		CreatedAt:  toInt64(m["CreatedAt"]),
	}, nil
}

func str(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func strPtr(s string) *string { return &s }

func int32Ptr(n int32) *int32 { return &n }

// isSystemMessage returns true if any tag has the "campfire:" prefix.
func isSystemMessage(tags []string) bool {
	for _, t := range tags {
		if strings.HasPrefix(t, "campfire:") {
			return true
		}
	}
	return false
}

// isCompactionEvent returns true if the record has the "campfire:compact" tag.
func isCompactionEvent(rec store.MessageRecord) bool {
	return store.HasTag(rec.Tags, "campfire:compact")
}

// hasAnyTag returns true if rec tags contain any of the filter tags (case-insensitive).
func hasAnyTag(recTags []string, filterTags []string) bool {
	for _, rt := range recTags {
		for _, ft := range filterTags {
			if strings.EqualFold(rt, ft) {
				return true
			}
		}
	}
	return false
}

// hasAnyTagPrefix returns true if any rec tag starts with any prefix (case-insensitive).
func hasAnyTagPrefix(recTags []string, prefixes []string) bool {
	for _, rt := range recTags {
		rtl := strings.ToLower(rt)
		for _, p := range prefixes {
			if strings.HasPrefix(rtl, strings.ToLower(p)) {
				return true
			}
		}
	}
	return false
}

// hasAnyTagOrPrefix returns true if any rec tag matches an exact tag OR starts with a prefix.
func hasAnyTagOrPrefix(recTags []string, exactTags []string, prefixes []string) bool {
	return hasAnyTag(recTags, exactTags) || hasAnyTagPrefix(recTags, prefixes)
}

// unmarshalEncryptedPayload delegates to pkg/crypto for downgrade prevention.
func unmarshalEncryptedPayload(payload []byte) (any, error) {
	return crypto.UnmarshalEncryptedPayload(payload)
}

// isTableExistsError returns true if err is an Azure "table already exists" error.
func isTableExistsError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "TableAlreadyExists") || strings.Contains(s, "409")
}

// isNotFoundError returns true if err is an Azure "entity not found" (404) error.
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "ResourceNotFound") || strings.Contains(s, "404") ||
		strings.Contains(s, "TableNotFound")
}

// isConflictError returns true if err is an Azure "entity already exists" (409) error.
func isConflictError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "EntityAlreadyExists") || strings.Contains(s, "409")
}

// isPreconditionFailedError returns true if err is an Azure "precondition failed" (412)
// error, which occurs when UpdateEntity is called with an ETag that no longer matches
// (i.e. the entity was modified by a concurrent writer).
func isPreconditionFailedError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "UpdateConditionNotSatisfied") || strings.Contains(s, "412")
}

// isMergeNotFoundError returns true if err is the Azurite-specific 400
// InvalidOperation error returned when UpdateEntity (PATCH/merge) is called
// on a non-existent entity. The real Azure Table Storage returns 404 in this
// case (caught by isNotFoundError); Azurite returns 400 InvalidOperation.
func isMergeNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "InvalidOperation") && strings.Contains(s, "400")
}

// NowNano returns the current time in nanoseconds (mirrors store.NowNano).
func NowNano() int64 {
	return time.Now().UnixNano()
}

// ---------------------------------------------------------------------------
// InviteStore — Azure Table Storage implementation
//
// Table: CampfireInvites  PK=invite_code  RK=campfire_id
// Using invite_code as PK enables global lookup without knowing campfire_id.
// ---------------------------------------------------------------------------

// CreateInvite inserts a new invite record.
func (ts *TableStore) CreateInvite(inv store.InviteRecord) error {
	revoked := 0
	if inv.Revoked {
		revoked = 1
	}
	entity := map[string]any{
		"PartitionKey": encodeKey(inv.InviteCode),
		"RowKey":       encodeKey(inv.CampfireID),
		"CampfireID":   inv.CampfireID,
		"InviteCode":   inv.InviteCode,
		"CreatedBy":    inv.CreatedBy,
		"CreatedAt":    inv.CreatedAt,
		"Revoked":      int64(revoked),
		"MaxUses":      int64(inv.MaxUses),
		"UseCount":     int64(inv.UseCount),
		"Label":        inv.Label,
	}
	return upsertEntity(context.Background(), ts.invites, entity)
}

// LookupInvite returns a single invite by code or nil if not found.
func (ts *TableStore) LookupInvite(inviteCode string) (*store.InviteRecord, error) {
	// List all rows with PK=inviteCode (should be exactly one).
	filter := fmt.Sprintf("PartitionKey eq '%s'", encodeKey(inviteCode))
	opts := &aztables.ListEntitiesOptions{Filter: &filter}
	pager := ts.invites.NewListEntitiesPager(opts)
	ctx := context.Background()
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("aztable: LookupInvite: %w", err)
		}
		for _, raw := range page.Entities {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				return nil, fmt.Errorf("aztable: LookupInvite unmarshal: %w", err)
			}
			return inviteFromEntity(m), nil
		}
	}
	return nil, nil
}

// ValidateInvite checks that the code belongs to campfireID and is usable.
func (ts *TableStore) ValidateInvite(campfireID, inviteCode string) (*store.InviteRecord, error) {
	inv, err := ts.LookupInvite(inviteCode)
	if err != nil {
		return nil, err
	}
	if inv == nil {
		return nil, fmt.Errorf("invite code not found")
	}
	if inv.CampfireID != campfireID {
		return nil, fmt.Errorf("invite code not valid for this campfire")
	}
	if inv.Revoked {
		return nil, fmt.Errorf("invite code has been revoked")
	}
	if inv.MaxUses > 0 && inv.UseCount >= inv.MaxUses {
		return nil, fmt.Errorf("invite code has reached its maximum uses")
	}
	return inv, nil
}

// RevokeInvite marks a code as revoked.
func (ts *TableStore) RevokeInvite(campfireID, inviteCode string) error {
	inv, err := ts.LookupInvite(inviteCode)
	if err != nil {
		return err
	}
	if inv == nil {
		return fmt.Errorf("invite code not found: %s", inviteCode)
	}
	inv.Revoked = true
	return ts.CreateInvite(*inv)
}

// ListInvites returns all invite records for a campfire.
func (ts *TableStore) ListInvites(campfireID string) ([]store.InviteRecord, error) {
	// Scan all rows and filter by CampfireID (no secondary index in Table Storage).
	opts := &aztables.ListEntitiesOptions{}
	pager := ts.invites.NewListEntitiesPager(opts)
	var result []store.InviteRecord
	ctx := context.Background()
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("aztable: ListInvites: %w", err)
		}
		for _, raw := range page.Entities {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				return nil, fmt.Errorf("aztable: ListInvites unmarshal: %w", err)
			}
			inv := inviteFromEntity(m)
			if inv.CampfireID == campfireID {
				result = append(result, *inv)
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt < result[j].CreatedAt
	})
	return result, nil
}

// HasAnyInvites returns true if the campfire has at least one registered invite code.
func (ts *TableStore) HasAnyInvites(campfireID string) (bool, error) {
	invites, err := ts.ListInvites(campfireID)
	if err != nil {
		return false, err
	}
	return len(invites) > 0, nil
}

// IncrementInviteUse increments the use_count for the given invite code.
func (ts *TableStore) IncrementInviteUse(inviteCode string) error {
	inv, err := ts.LookupInvite(inviteCode)
	if err != nil {
		return err
	}
	if inv == nil {
		return fmt.Errorf("invite code not found: %s", inviteCode)
	}
	inv.UseCount++
	return ts.CreateInvite(*inv)
}

// ValidateAndUseInvite atomically validates and increments the invite code using
// ETag-based optimistic concurrency. It reads the entity (capturing the ETag),
// validates it, then updates with IfMatch to ensure no concurrent writer has
// modified the record between the read and the write. On a 412 Precondition
// Failed, it retries (up to maxInviteRetries attempts) before giving up.
func (ts *TableStore) ValidateAndUseInvite(campfireID, inviteCode string) (*store.InviteRecord, error) {
	const maxRetries = 5
	ctx := context.Background()
	pk := encodeKey(inviteCode)
	rk := encodeKey(campfireID)

	for attempt := 0; attempt < maxRetries; attempt++ {
		// Fetch the entity with its current ETag.
		resp, err := ts.invites.GetEntity(ctx, pk, rk, nil)
		if err != nil {
			if isNotFoundError(err) {
				return nil, fmt.Errorf("invite code not found")
			}
			return nil, fmt.Errorf("ValidateAndUseInvite: %w", err)
		}
		var m map[string]any
		if err := json.Unmarshal(resp.Value, &m); err != nil {
			return nil, fmt.Errorf("ValidateAndUseInvite unmarshal: %w", err)
		}
		inv := inviteFromEntity(m)

		// Validate.
		if inv.CampfireID != campfireID {
			return nil, fmt.Errorf("invite code not valid for this campfire")
		}
		if inv.Revoked {
			return nil, fmt.Errorf("invite code has been revoked")
		}
		if inv.MaxUses > 0 && inv.UseCount >= inv.MaxUses {
			return nil, store.ErrInviteExhausted
		}

		// Increment and write back with ETag guard.
		inv.UseCount++
		m["UseCount"] = int64(inv.UseCount)
		data, err := json.Marshal(m)
		if err != nil {
			return nil, fmt.Errorf("ValidateAndUseInvite marshal: %w", err)
		}
		etag := resp.ETag
		_, err = ts.invites.UpdateEntity(ctx, data, &aztables.UpdateEntityOptions{
			UpdateMode: aztables.UpdateModeReplace,
			IfMatch:    &etag,
		})
		if err == nil {
			return inv, nil
		}
		if isPreconditionFailedError(err) {
			// Another writer modified the entity concurrently — retry.
			continue
		}
		return nil, fmt.Errorf("ValidateAndUseInvite update: %w", err)
	}
	// All retries exhausted — treat as contention-driven exhaustion.
	return nil, store.ErrInviteExhausted
}

// inviteFromEntity converts an Azure Table Storage entity map to a store.InviteRecord.
func inviteFromEntity(m map[string]any) *store.InviteRecord {
	revoked := toInt64(m["Revoked"]) != 0
	return &store.InviteRecord{
		CampfireID: str(m, "CampfireID"),
		InviteCode: str(m, "InviteCode"),
		CreatedBy:  str(m, "CreatedBy"),
		CreatedAt:  toInt64(m["CreatedAt"]),
		Revoked:    revoked,
		MaxUses:    int(toInt64(m["MaxUses"])),
		UseCount:   int(toInt64(m["UseCount"])),
		Label:      str(m, "Label"),
	}
}
