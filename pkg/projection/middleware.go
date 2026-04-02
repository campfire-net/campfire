// Package projection implements the ProjectionMiddleware that maintains named
// filter projection views on top of a store.Store.
//
// Two paths are supported:
//
//   - ReadView (lazy delta): on every cf view read, evaluate only the delta
//     (messages since the last high_water_mark) against the predicate and add
//     matches to the stored projection. Cost is O(delta + result_set).
//
//   - AddMessage (eager write): for refresh:on-write views with a Class 1
//     (ClassIncremental) predicate, update the projection synchronously on
//     every AddMessage call. Cost is O(views × 1) per message.
package projection

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/campfire-net/campfire/pkg/predicate"
	"github.com/campfire-net/campfire/pkg/store"
)

// defaultMaxProjectedViews is the maximum number of on-write projected views
// per campfire when CF_MAX_PROJECTED_VIEWS is not set.
const defaultMaxProjectedViews = 20

// viewDefinition mirrors cmd/cf/cmd/view.go viewDefinition to avoid import cycle.
type viewDefinition struct {
	Name      string `json:"name"`
	Predicate string `json:"predicate"`
	Refresh   string `json:"refresh,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	EntityKey string `json:"entity_key,omitempty"` // dot-separated payload path for entity-key views
}

// cachedView holds a parsed and classified view definition for use in AddMessage.
type cachedView struct {
	def           viewDefinition
	parsed        *predicate.Node
	class         Class
	predicateHash string
	entityKey     string // mirrors def.EntityKey for fast access
}

// ProjectionMiddleware wraps a store.Store to maintain named projection views.
// It implements store.Store so it can be used as a drop-in replacement.
//
// ReadView performs lazy delta evaluation: only messages since the stored
// high_water_mark are evaluated against the predicate.
//
// AddMessage intercepts each inserted message and, for on-write Class 1 views,
// evaluates the predicate and inserts matching entries.
type ProjectionMiddleware struct {
	base    store.Store
	maxViews int

	mu         sync.Mutex
	viewCache  map[string][]cachedView // campfireID → classified views
}

// New creates a ProjectionMiddleware wrapping base.
// The maxViews limit (from CF_MAX_PROJECTED_VIEWS env, default 20) caps
// the number of on-write views that are projected per campfire.
func New(base store.Store) *ProjectionMiddleware {
	maxViews := defaultMaxProjectedViews
	if v := os.Getenv("CF_MAX_PROJECTED_VIEWS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxViews = n
		}
	}
	return &ProjectionMiddleware{
		base:      base,
		maxViews:  maxViews,
		viewCache: make(map[string][]cachedView),
	}
}

// ReadView returns the messages matching the named view, using lazy delta
// evaluation for Class 1 predicates and full-scan fallback for Class 3.
//
// Algorithm:
//  1. Find latest view definition (campfire:view message with matching name).
//  2. Load projection metadata. If predicate_hash mismatches → full rebuild.
//  3. Check compaction: if last_compaction_id mismatches → full rebuild.
//  4. Query delta: messages with received_at > high_water_mark.
//  5. For each delta message: evaluate predicate, insert matches.
//  6. Class 3 fallback: full scan (identical to pre-middleware behavior).
//  7. Return projection entries joined with full message records.
func (m *ProjectionMiddleware) ReadView(campfireID, name string) ([]store.MessageRecord, error) {
	// 1. Find latest view definition.
	def, err := m.findLatestView(campfireID, name)
	if err != nil {
		return nil, fmt.Errorf("projection: find view: %w", err)
	}
	if def == nil {
		return nil, nil // view not found — caller handles
	}

	// Parse and classify the predicate.
	parsed, err := predicate.Parse(def.Predicate)
	if err != nil {
		return nil, fmt.Errorf("projection: parse predicate: %w", err)
	}
	class := Classify(parsed, def.Limit)
	predHash := hashPredicate(def.Predicate)

	// Class 3 (AlwaysScan): fall back to full scan. No projection stored.
	if class == ClassAlwaysScan {
		return m.fullScan(campfireID, def, parsed)
	}

	// 2. Load projection metadata.
	meta, err := m.base.GetProjectionMetadata(campfireID, name)
	if err != nil {
		return nil, fmt.Errorf("projection: get metadata: %w", err)
	}

	// Determine if a full rebuild is required.
	needRebuild := false
	if meta == nil {
		needRebuild = true
	} else if meta.PredicateHash != predHash {
		// Predicate changed — rebuild from scratch.
		needRebuild = true
	} else {
		// 3. Check compaction staleness.
		latestCompactionID, err := m.latestCompactionID(campfireID)
		if err != nil {
			return nil, fmt.Errorf("projection: check compaction: %w", err)
		}
		if latestCompactionID != meta.LastCompactionID {
			needRebuild = true
		}
	}

	if needRebuild {
		if err := m.base.DeleteAllProjectionEntries(campfireID, name); err != nil {
			return nil, fmt.Errorf("projection: delete entries for rebuild: %w", err)
		}
		// Reset metadata to zero for full rebuild.
		meta = &store.ProjectionMetadata{
			CampfireID:    campfireID,
			ViewName:      name,
			PredicateHash: predHash,
		}
	}

	highWaterMark := int64(0)
	if meta != nil {
		highWaterMark = meta.HighWaterMark
	}

	// 4. Query delta: messages since high_water_mark using received_at cursor.
	delta, err := m.base.ListMessages(campfireID, 0, store.MessageFilter{
		AfterReceivedAt:   highWaterMark,
		RespectCompaction: true,
	})
	if err != nil {
		return nil, fmt.Errorf("projection: list delta: %w", err)
	}

	// 5. Evaluate delta against predicate, insert matches.
	var newHighWaterMark int64 = highWaterMark
	for _, msg := range delta {
		if msg.ReceivedAt > newHighWaterMark {
			newHighWaterMark = msg.ReceivedAt
		}

		// Skip system messages (campfire:* tags).
		if isSystemMsg(msg.Tags) {
			continue
		}

		ctx := buildCtx(msg, nil) // Class 1: no fulfillment index needed
		if predicate.Eval(parsed, ctx) {
			// Extract entity key if this is an entity-key view.
			entityKey := ""
			if def.EntityKey != "" {
				var ok bool
				entityKey, ok = extractEntityKey(msg.Payload, def.EntityKey)
				if !ok {
					continue // skip messages missing the entity key field
				}
			}
			if err := m.insertOrUpsert(campfireID, name, msg.ID, entityKey, msg.ReceivedAt, msg.Timestamp); err != nil {
				return nil, fmt.Errorf("projection: insert/upsert entry: %w", err)
			}
		}
	}

	// 6. Get latest compaction ID for metadata update.
	latestCompactionID, err := m.latestCompactionID(campfireID)
	if err != nil {
		return nil, fmt.Errorf("projection: get compaction ID: %w", err)
	}

	// Persist updated metadata.
	updatedMeta := store.ProjectionMetadata{
		CampfireID:       campfireID,
		ViewName:         name,
		PredicateHash:    predHash,
		LastCompactionID: latestCompactionID,
		HighWaterMark:    newHighWaterMark,
	}
	if err := m.base.SetProjectionMetadata(campfireID, name, updatedMeta); err != nil {
		return nil, fmt.Errorf("projection: set metadata: %w", err)
	}

	// 7. Fetch all projection entries and join with full message records.
	entries, err := m.base.ListProjectionEntries(campfireID, name)
	if err != nil {
		return nil, fmt.Errorf("projection: list entries: %w", err)
	}

	result := make([]store.MessageRecord, 0, len(entries))
	for _, entry := range entries {
		msg, err := m.base.GetMessage(entry.MessageID)
		if err != nil {
			return nil, fmt.Errorf("projection: get message %s: %w", entry.MessageID, err)
		}
		if msg == nil {
			// Message was compacted away — skip.
			continue
		}
		result = append(result, *msg)
	}

	// Re-sort by timestamp (projection entries are ordered by indexed_at, not timestamp).
	sort.Slice(result, func(i, j int) bool {
		return result[i].Timestamp < result[j].Timestamp
	})

	return result, nil
}

// AddMessage implements store.Store.AddMessage with eager write-path projection.
// For on-write Class 1 views, projection entries are inserted synchronously
// after the base message is written. Projection failures are non-fatal —
// the lazy delta path will recover them on next ReadView.
func (m *ProjectionMiddleware) AddMessage(msg store.MessageRecord) (bool, error) {
	// 1. Write to base store first.
	inserted, err := m.base.AddMessage(msg)
	if err != nil {
		return false, err
	}

	// 2. Duplicate — nothing to project.
	if !inserted {
		return false, nil
	}

	// 3. System message with campfire:view tag → invalidate view cache.
	if isSystemMsg(msg.Tags) {
		if hasTagPrefix(msg.Tags, "campfire:view") {
			m.invalidateViewCache(msg.CampfireID)
		}
		if hasTagPrefix(msg.Tags, "campfire:compact") {
			// Handle compaction: delete projection entries for superseded messages.
			m.handleCompaction(msg)
		}
		// Never evaluate system messages against view predicates.
		return true, nil
	}

	// 4. Evaluate message against all on-write Class 1 views.
	views, err := m.getOnWriteViews(msg.CampfireID)
	if err != nil {
		// Non-fatal: lazy delta will recover on next ReadView.
		return true, nil
	}

	ctx := buildCtx(msg, nil)
	for i, cv := range views {
		if i >= m.maxViews {
			break // view cap
		}
		if cv.class != ClassIncremental {
			continue // Class 3 downgraded to on-read
		}
		if predicate.Eval(cv.parsed, ctx) {
			entityKey := ""
			if cv.def.EntityKey != "" {
				var ok bool
				entityKey, ok = extractEntityKey(msg.Payload, cv.def.EntityKey)
				if !ok {
					continue // skip messages missing the entity key field
				}
			}
			// Non-fatal if insert/upsert fails — lazy delta recovers.
			_ = m.insertOrUpsert(msg.CampfireID, cv.def.Name, msg.ID, entityKey, msg.ReceivedAt, msg.Timestamp)
		}
	}

	return true, nil
}

// handleCompaction parses a campfire:compact message and deletes projection
// entries for all superseded message IDs across all active views.
func (m *ProjectionMiddleware) handleCompaction(msg store.MessageRecord) {
	var payload struct {
		Supersedes []string `json:"supersedes"`
	}
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return
	}
	if len(payload.Supersedes) == 0 {
		return
	}

	// Find all view names for this campfire.
	views, err := m.getOnWriteViews(msg.CampfireID)
	if err != nil {
		return
	}

	// Also handle on-read views by finding their names from stored metadata.
	// For simplicity, delete from all projection entries we know about.
	viewNames := make(map[string]bool)
	for _, cv := range views {
		viewNames[cv.def.Name] = true
	}

	for viewName := range viewNames {
		_ = m.base.DeleteProjectionEntries(msg.CampfireID, viewName, payload.Supersedes)
	}
}

// invalidateViewCache removes the cached view definitions for a campfire.
func (m *ProjectionMiddleware) invalidateViewCache(campfireID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.viewCache, campfireID)
}

// getOnWriteViews returns the classified on-write view definitions for a campfire.
// Results are cached and invalidated when campfire:view messages arrive.
func (m *ProjectionMiddleware) getOnWriteViews(campfireID string) ([]cachedView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if cached, ok := m.viewCache[campfireID]; ok {
		return cached, nil
	}

	// Load all view definitions.
	msgs, err := m.base.ListMessages(campfireID, 0, store.MessageFilter{
		Tags: []string{"campfire:view"},
	})
	if err != nil {
		return nil, err
	}

	// Latest definition per name.
	seen := make(map[string]viewDefinition)
	for _, msg := range msgs {
		var def viewDefinition
		if json.Unmarshal(msg.Payload, &def) != nil {
			continue
		}
		if def.Name != "" {
			seen[def.Name] = def
		}
	}

	var views []cachedView
	for _, def := range seen {
		if def.Refresh != "on-write" {
			continue
		}
		parsed, err := predicate.Parse(def.Predicate)
		if err != nil {
			continue
		}
		class := Classify(parsed, def.Limit)
		views = append(views, cachedView{
			def:           def,
			parsed:        parsed,
			class:         class,
			predicateHash: hashPredicate(def.Predicate),
			entityKey:     def.EntityKey,
		})
	}

	m.viewCache[campfireID] = views
	return views, nil
}

// findLatestView finds the most recent campfire:view message with the given name.
func (m *ProjectionMiddleware) findLatestView(campfireID, name string) (*viewDefinition, error) {
	msgs, err := m.base.ListMessages(campfireID, 0, store.MessageFilter{
		Tags: []string{"campfire:view"},
	})
	if err != nil {
		return nil, err
	}

	// Scan backwards — latest wins.
	for i := len(msgs) - 1; i >= 0; i-- {
		var def viewDefinition
		if json.Unmarshal(msgs[i].Payload, &def) != nil {
			continue
		}
		if def.Name == name {
			return &def, nil
		}
	}
	return nil, nil
}

// latestCompactionID returns the ID of the most recent campfire:compact message.
// Returns "" if no compaction events exist.
func (m *ProjectionMiddleware) latestCompactionID(campfireID string) (string, error) {
	events, err := m.base.ListCompactionEvents(campfireID)
	if err != nil {
		return "", err
	}
	if len(events) == 0 {
		return "", nil
	}
	return events[len(events)-1].ID, nil
}

// fullScan performs a full message scan (used for Class 3 / AlwaysScan predicates).
// This replicates the pre-middleware runViewRead behavior exactly.
func (m *ProjectionMiddleware) fullScan(campfireID string, def *viewDefinition, parsed *predicate.Node) ([]store.MessageRecord, error) {
	allMsgs, err := m.base.ListMessages(campfireID, 0, store.MessageFilter{RespectCompaction: true})
	if err != nil {
		return nil, fmt.Errorf("projection: full scan list: %w", err)
	}

	fulfillmentIndex := buildFulfillmentIndex(allMsgs)

	var matched []store.MessageRecord
	for _, msg := range allMsgs {
		if isSystemMsg(msg.Tags) {
			continue
		}
		ctx := buildCtx(msg, fulfillmentIndex)
		if predicate.Eval(parsed, ctx) {
			matched = append(matched, msg)
		}
	}

	// Re-sort by timestamp (default: ascending).
	sort.Slice(matched, func(i, j int) bool {
		return matched[i].Timestamp < matched[j].Timestamp
	})

	return matched, nil
}

// buildFulfillmentIndex builds a map of message IDs that have been fulfilled
// (i.e., appear as antecedents of a "fulfills"-tagged message).
func buildFulfillmentIndex(msgs []store.MessageRecord) map[string]bool {
	idx := make(map[string]bool)
	for _, m := range msgs {
		for _, tag := range m.Tags {
			if tag == "fulfills" {
				for _, ant := range m.Antecedents {
					idx[ant] = true
				}
				break
			}
		}
	}
	return idx
}

// buildCtx creates a predicate.MessageContext from a store.MessageRecord.
func buildCtx(m store.MessageRecord, fulfillmentIndex map[string]bool) *predicate.MessageContext {
	var payload map[string]any
	json.Unmarshal(m.Payload, &payload) //nolint:errcheck

	senderIdentity := m.Sender
	if m.SenderCampfireID != "" {
		senderIdentity = m.SenderCampfireID
	}

	return &predicate.MessageContext{
		MessageID:        m.ID,
		Tags:             m.Tags,
		Sender:           senderIdentity,
		Timestamp:        m.Timestamp,
		Payload:          payload,
		RawPayload:       m.Payload, // for payload-size predicate
		FulfillmentIndex: fulfillmentIndex,
	}
}

// isSystemMsg returns true if any tag has the "campfire:" prefix.
func isSystemMsg(tags []string) bool {
	for _, t := range tags {
		if strings.HasPrefix(t, "campfire:") {
			return true
		}
	}
	return false
}

// hasTagPrefix returns true if any tag exactly equals the given value.
func hasTagPrefix(tags []string, exact string) bool {
	for _, t := range tags {
		if t == exact {
			return true
		}
	}
	return false
}

// hashPredicate returns a short stable hash of a predicate expression string.
func hashPredicate(expr string) string {
	h := sha256.Sum256([]byte(expr))
	return hex.EncodeToString(h[:8])
}

// extractEntityKey extracts a string value from a message payload using a
// dot-separated path (e.g. "payload.bead_id"). Returns ("", false) if the
// field is missing or not a string-representable value.
func extractEntityKey(payload []byte, path string) (string, bool) {
	if path == "" || len(payload) == 0 {
		return "", false
	}
	var root any
	if err := json.Unmarshal(payload, &root); err != nil {
		return "", false
	}

	// Strip leading "payload." prefix if present (mirrors predicate field syntax).
	parts := strings.Split(path, ".")
	if len(parts) > 0 && parts[0] == "payload" {
		parts = parts[1:]
	}

	current := root
	for _, part := range parts {
		m, ok := current.(map[string]any)
		if !ok {
			return "", false
		}
		current, ok = m[part]
		if !ok {
			return "", false
		}
	}

	switch v := current.(type) {
	case string:
		return v, true
	case float64:
		return fmt.Sprintf("%g", v), true
	case bool:
		if v {
			return "true", true
		}
		return "false", true
	default:
		return "", false
	}
}

// insertOrUpsert inserts or upserts a projection entry depending on whether
// an entity key is configured for this view.
func (m *ProjectionMiddleware) insertOrUpsert(campfireID, viewName, messageID, entityKey string, indexedAt, timestamp int64) error {
	if entityKey != "" {
		return m.base.UpsertProjectionEntry(campfireID, viewName, messageID, entityKey, indexedAt, timestamp)
	}
	return m.base.InsertProjectionEntry(campfireID, viewName, messageID, indexedAt)
}

// --- store.Store delegation methods ---
// ProjectionMiddleware implements store.Store by delegating all methods to base
// except AddMessage (intercepted above).

func (m *ProjectionMiddleware) AddMembership(mem store.Membership) error {
	return m.base.AddMembership(mem)
}
func (m *ProjectionMiddleware) UpdateMembershipRole(campfireID, role string) error {
	return m.base.UpdateMembershipRole(campfireID, role)
}
func (m *ProjectionMiddleware) RemoveMembership(campfireID string) error {
	return m.base.RemoveMembership(campfireID)
}
func (m *ProjectionMiddleware) GetMembership(campfireID string) (*store.Membership, error) {
	return m.base.GetMembership(campfireID)
}
func (m *ProjectionMiddleware) ListMemberships() ([]store.Membership, error) {
	return m.base.ListMemberships()
}
func (m *ProjectionMiddleware) HasMessage(id string) (bool, error) {
	return m.base.HasMessage(id)
}
func (m *ProjectionMiddleware) GetMessage(id string) (*store.MessageRecord, error) {
	return m.base.GetMessage(id)
}
func (m *ProjectionMiddleware) GetMessageByPrefix(prefix string) (*store.MessageRecord, error) {
	return m.base.GetMessageByPrefix(prefix)
}
func (m *ProjectionMiddleware) ListMessages(campfireID string, afterTimestamp int64, filter ...store.MessageFilter) ([]store.MessageRecord, error) {
	return m.base.ListMessages(campfireID, afterTimestamp, filter...)
}
func (m *ProjectionMiddleware) MaxMessageTimestamp(campfireID string, afterTS int64) (int64, error) {
	return m.base.MaxMessageTimestamp(campfireID, afterTS)
}
func (m *ProjectionMiddleware) ListReferencingMessages(messageID string) ([]store.MessageRecord, error) {
	return m.base.ListReferencingMessages(messageID)
}
func (m *ProjectionMiddleware) ListCompactionEvents(campfireID string) ([]store.MessageRecord, error) {
	return m.base.ListCompactionEvents(campfireID)
}
func (m *ProjectionMiddleware) GetReadCursor(campfireID string) (int64, error) {
	return m.base.GetReadCursor(campfireID)
}
func (m *ProjectionMiddleware) SetReadCursor(campfireID string, timestamp int64) error {
	return m.base.SetReadCursor(campfireID, timestamp)
}
func (m *ProjectionMiddleware) UpsertPeerEndpoint(e store.PeerEndpoint) error {
	return m.base.UpsertPeerEndpoint(e)
}
func (m *ProjectionMiddleware) DeletePeerEndpoint(campfireID, memberPubkey string) error {
	return m.base.DeletePeerEndpoint(campfireID, memberPubkey)
}
func (m *ProjectionMiddleware) ListPeerEndpoints(campfireID string) ([]store.PeerEndpoint, error) {
	return m.base.ListPeerEndpoints(campfireID)
}
func (m *ProjectionMiddleware) GetPeerRole(campfireID, memberPubkey string) (string, error) {
	return m.base.GetPeerRole(campfireID, memberPubkey)
}
func (m *ProjectionMiddleware) UpsertThresholdShare(share store.ThresholdShare) error {
	return m.base.UpsertThresholdShare(share)
}
func (m *ProjectionMiddleware) GetThresholdShare(campfireID string) (*store.ThresholdShare, error) {
	return m.base.GetThresholdShare(campfireID)
}
func (m *ProjectionMiddleware) StorePendingThresholdShare(campfireID string, participantID uint32, shareData []byte) error {
	return m.base.StorePendingThresholdShare(campfireID, participantID, shareData)
}
func (m *ProjectionMiddleware) ClaimPendingThresholdShare(campfireID string) (uint32, []byte, error) {
	return m.base.ClaimPendingThresholdShare(campfireID)
}
func (m *ProjectionMiddleware) UpsertEpochSecret(secret store.EpochSecret) error {
	return m.base.UpsertEpochSecret(secret)
}
func (m *ProjectionMiddleware) GetEpochSecret(campfireID string, epoch uint64) (*store.EpochSecret, error) {
	return m.base.GetEpochSecret(campfireID, epoch)
}
func (m *ProjectionMiddleware) GetLatestEpochSecret(campfireID string) (*store.EpochSecret, error) {
	return m.base.GetLatestEpochSecret(campfireID)
}
func (m *ProjectionMiddleware) SetMembershipEncrypted(campfireID string, encrypted bool) error {
	return m.base.SetMembershipEncrypted(campfireID, encrypted)
}
func (m *ProjectionMiddleware) ApplyMembershipCommitAtomically(campfireID string, newMember *store.Membership, secret store.EpochSecret) error {
	return m.base.ApplyMembershipCommitAtomically(campfireID, newMember, secret)
}
func (m *ProjectionMiddleware) CreateInvite(inv store.InviteRecord) error {
	return m.base.CreateInvite(inv)
}
func (m *ProjectionMiddleware) ValidateInvite(campfireID, inviteCode string) (*store.InviteRecord, error) {
	return m.base.ValidateInvite(campfireID, inviteCode)
}
func (m *ProjectionMiddleware) RevokeInvite(campfireID, inviteCode string) error {
	return m.base.RevokeInvite(campfireID, inviteCode)
}
func (m *ProjectionMiddleware) ListInvites(campfireID string) ([]store.InviteRecord, error) {
	return m.base.ListInvites(campfireID)
}
func (m *ProjectionMiddleware) LookupInvite(inviteCode string) (*store.InviteRecord, error) {
	return m.base.LookupInvite(inviteCode)
}
func (m *ProjectionMiddleware) HasAnyInvites(campfireID string) (bool, error) {
	return m.base.HasAnyInvites(campfireID)
}
func (m *ProjectionMiddleware) IncrementInviteUse(inviteCode string) error {
	return m.base.IncrementInviteUse(inviteCode)
}
func (m *ProjectionMiddleware) ValidateAndUseInvite(campfireID, inviteCode string) (*store.InviteRecord, error) {
	return m.base.ValidateAndUseInvite(campfireID, inviteCode)
}
func (m *ProjectionMiddleware) InsertProjectionEntry(campfireID, viewName, messageID string, indexedAt int64) error {
	return m.base.InsertProjectionEntry(campfireID, viewName, messageID, indexedAt)
}
func (m *ProjectionMiddleware) UpsertProjectionEntry(campfireID, viewName, messageID, entityKey string, indexedAt, timestamp int64) error {
	return m.base.UpsertProjectionEntry(campfireID, viewName, messageID, entityKey, indexedAt, timestamp)
}
func (m *ProjectionMiddleware) DeleteProjectionEntries(campfireID, viewName string, messageIDs []string) error {
	return m.base.DeleteProjectionEntries(campfireID, viewName, messageIDs)
}
func (m *ProjectionMiddleware) DeleteAllProjectionEntries(campfireID, viewName string) error {
	return m.base.DeleteAllProjectionEntries(campfireID, viewName)
}
func (m *ProjectionMiddleware) ListProjectionEntries(campfireID, viewName string) ([]store.ProjectionEntry, error) {
	return m.base.ListProjectionEntries(campfireID, viewName)
}
func (m *ProjectionMiddleware) GetProjectionMetadata(campfireID, viewName string) (*store.ProjectionMetadata, error) {
	return m.base.GetProjectionMetadata(campfireID, viewName)
}
func (m *ProjectionMiddleware) SetProjectionMetadata(campfireID, viewName string, meta store.ProjectionMetadata) error {
	return m.base.SetProjectionMetadata(campfireID, viewName, meta)
}
func (m *ProjectionMiddleware) UpdateCampfireID(oldID, newID string) error {
	return m.base.UpdateCampfireID(oldID, newID)
}
func (m *ProjectionMiddleware) Close() error {
	return m.base.Close()
}
