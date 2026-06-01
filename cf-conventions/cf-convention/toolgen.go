package convention

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/campfire-net/campfire/cf-protocol/store"
)

// maxToolDescriptionLen is the maximum number of characters included in the
// MCP tool description field. Longer descriptions are truncated.
// The limit is set high enough to accommodate the response-semantics suffix
// appended by GenerateTool (up to ~32 chars).
const maxToolDescriptionLen = 120

// MCPToolInfo describes a tool for the MCP protocol.
type MCPToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// StoreReader provides message reading for declaration discovery.
type StoreReader interface {
	ListMessages(campfireID string, afterTimestamp int64, filter ...store.MessageFilter) ([]store.MessageRecord, error)
}

// GenerateTool produces an MCP tool descriptor from a parsed declaration.
// campfireID is pre-filled into the campfire_id property of the input schema.
func GenerateTool(decl *Declaration, campfireID string) (*MCPToolInfo, error) {
	schema, err := buildInputSchema(decl, campfireID)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	// Append response semantics suffix based on declaration response mode.
	// Only for explicitly declared response modes (ResponseExplicit=true).
	var suffix string
	if decl.ResponseExplicit {
		switch decl.Response {
		case "sync":
			suffix = " Returns response directly."
		case "async":
			suffix = " Returns message ID."
		}
	}

	desc := decl.Description
	// Truncate base description to make room for suffix, then append.
	maxBase := maxToolDescriptionLen - len(suffix)
	if maxBase < 0 {
		maxBase = 0
	}
	if len(desc) > maxBase {
		desc = desc[:maxBase]
	}
	desc = desc + suffix

	return &MCPToolInfo{
		Name:        decl.Operation,
		Description: desc,
		InputSchema: json.RawMessage(raw),
	}, nil
}

// GenerateToolName produces a tool name, handling collisions.
// Primary: operation name. On collision: conventionslug_operation.
func GenerateToolName(decl *Declaration, existing map[string]bool) string {
	name := decl.Operation
	if !existing[name] {
		return name
	}
	return NamespacedToolName(decl)
}

// NamespacedToolName returns the convention-namespaced tool name:
// conventionslug_operation (hyphens in convention name become underscores).
func NamespacedToolName(decl *Declaration) string {
	slug := strings.ReplaceAll(decl.Convention, "-", "_")
	return slug + "_" + decl.Operation
}

// ListOperations reads convention:operation tagged messages from a campfire store.
// Parse errors are skipped; only valid declarations are returned.
// campfireKey is passed to Parse for authority verification (use "" to skip).
//
// Supersede semantics: if a declaration carries a non-empty Supersedes field, the
// declaration with that message ID is replaced by the newer one. Only the newest
// version in a supersede chain is returned. When multiple declarations claim to
// supersede the same target, the one with the highest timestamp wins; all others
// are also excluded.
//
// Revoke semantics: convention:revoke tagged messages (produced by the convention-
// extension "revoke" operation) permanently remove a declaration from the list.
// A revoked declaration disappears entirely. Revoking a superseded declaration
// also removes the superseding declaration (chain invalidation).
func ListOperations(ctx context.Context, s StoreReader, campfireID, campfireKey string) ([]*Declaration, error) {
	return listOperations(ctx, s, campfireID, campfireKey, "")
}

// ListOperationsWithRegistry reads declarations from campfireID (inline) and, when
// registryCampfireID is non-empty, also from the convention registry campfire.
// Messages from both sources are merged before supersede and revoke filtering,
// so registry declarations can supersede inline ones via the Supersedes field.
// When registryCampfireID is empty, this is identical to ListOperations.
func ListOperationsWithRegistry(ctx context.Context, s StoreReader, campfireID, campfireKey, registryCampfireID string) ([]*Declaration, error) {
	return listOperations(ctx, s, campfireID, campfireKey, registryCampfireID)
}

// listOperations is the shared implementation used by ListOperations and
// ListOperationsWithRegistry.
func listOperations(ctx context.Context, s StoreReader, campfireID, campfireKey, registryCampfireID string) ([]*Declaration, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Collect operation declarations.
	opMsgs, err := s.ListMessages(campfireID, 0, store.MessageFilter{
		Tags: []string{ConventionOperationTag},
	})
	if err != nil {
		return nil, fmt.Errorf("listing operation declarations: %w", err)
	}

	// Collect revoke messages.
	revokeMsgs, err := s.ListMessages(campfireID, 0, store.MessageFilter{
		Tags: []string{conventionRevokeTag},
	})
	if err != nil {
		return nil, fmt.Errorf("listing revoke messages: %w", err)
	}

	// When a registry campfire is provided, merge its declarations and revokes
	// with the inline ones. Registry messages are appended after inline messages
	// so that the timestamp-based supersede winner logic applies uniformly.
	if registryCampfireID != "" && registryCampfireID != campfireID {
		regOpMsgs, regErr := s.ListMessages(registryCampfireID, 0, store.MessageFilter{
			Tags: []string{ConventionOperationTag},
		})
		if regErr == nil {
			opMsgs = append(opMsgs, regOpMsgs...)
		}
		regRevokeMsgs, regErr := s.ListMessages(registryCampfireID, 0, store.MessageFilter{
			Tags: []string{conventionRevokeTag},
		})
		if regErr == nil {
			revokeMsgs = append(revokeMsgs, regRevokeMsgs...)
		}
	}

	// Build a sender index for all operation messages so that offline-mode revoke
	// validation can check whether the revoker matches the original declaration's signer.
	opSenderByMsgID := make(map[string]string, len(opMsgs))
	for _, m := range opMsgs {
		opSenderByMsgID[m.ID] = m.Sender
	}

	// Build revoked set: target_id values from revoke message payloads.
	// Authorization rules (in priority order):
	//   1. campfireKey non-empty: only revoke messages sent by the campfire key are honoured.
	//   2. campfireKey empty (offline mode): only the original declaration's signer may revoke
	//      their own declaration. Revoke messages from any other sender are ignored.
	revoked := make(map[string]bool)
	for _, msg := range revokeMsgs {
		var revokePayload struct {
			TargetID string `json:"target_id"`
		}
		if jsonErr := json.Unmarshal(msg.Payload, &revokePayload); jsonErr != nil {
			continue
		}
		if campfireKey != "" {
			// Online mode: campfire key has full revoke authority.
			if msg.Sender != campfireKey {
				continue // revoke not signed by campfire key — ignore
			}
		} else {
			// Offline mode: only the original signer may revoke their own declaration.
			// Empty sender is never a valid revoker — reject immediately.
			if msg.Sender == "" {
				continue // empty sender cannot authorize a revoke
			}
			originalSigner, known := opSenderByMsgID[revokePayload.TargetID]
			if !known || originalSigner == "" || msg.Sender != originalSigner {
				continue // revoker does not match original signer — ignore
			}
		}
		if revokePayload.TargetID != "" {
			revoked[revokePayload.TargetID] = true
		}
	}

	// Parse all operation declarations.
	type opEntry struct {
		decl      *Declaration
		messageID string
		timestamp int64
		sender    string
	}
	var all []opEntry
	for _, msg := range opMsgs {
		decl, _, parseErr := Parse(msg.Tags, msg.Payload, msg.Sender, campfireKey, DefaultDeniedTagPrefixes)
		if parseErr != nil {
			continue // skip malformed
		}
		decl.MessageID = msg.ID
		all = append(all, opEntry{decl: decl, messageID: msg.ID, timestamp: msg.Timestamp, sender: msg.Sender})
	}

	// Build supersede winner map: for each target, find the superseding entry with
	// the highest timestamp. All other candidates claiming to supersede the same
	// target are treated as superseded themselves.
	winnerByTarget := make(map[string]opEntry) // target msgID -> winning entry
	for _, e := range all {
		if e.decl.Supersedes == "" {
			continue
		}
		// Authorization (campfire-f5c): a declaration may only supersede a target
		// signed by the same identity (self-upgrade) or, in online mode, by the
		// campfire key (owner override). This mirrors the revoke authorization rule
		// above and prevents any writer from replacing another signer's declaration
		// via a crafted Supersedes link. An unauthorized superseder does not
		// supersede the target; it falls through to the (also-gated) version-dedup
		// pass below, where it cannot win the slot of a different signer.
		if !precedenceAuthorized(e.sender, opSenderByMsgID[e.decl.Supersedes], campfireKey) {
			continue
		}
		prev, exists := winnerByTarget[e.decl.Supersedes]
		if !exists || e.timestamp > prev.timestamp {
			winnerByTarget[e.decl.Supersedes] = e
		}
	}

	// Collect all message IDs that are effectively superseded:
	// - The direct targets (they have a newer replacement).
	// - Losing superseder candidates (earlier-timestamp declarations that also
	//   claimed to supersede the same target, but lost to the winner).
	supersededIDs := make(map[string]bool)
	for targetID := range winnerByTarget {
		supersededIDs[targetID] = true
	}
	for _, e := range all {
		if e.decl.Supersedes == "" {
			continue
		}
		target := e.decl.Supersedes
		if winner, ok := winnerByTarget[target]; ok && winner.messageID != e.messageID {
			supersededIDs[e.messageID] = true
		}
	}

	// Transitively expand the revoked set through the supersede chain.
	// If msg1 is revoked and msg2.supersedes == msg1, then msg2 is also revoked.
	// If msg3.supersedes == msg2, then msg3 is also revoked. Repeat until stable.
	// Build a lookup from messageID to the entry that supersedes it (winner only).
	supersedesBy := make(map[string]string) // target msgID -> winner msgID that supersedes it
	for targetID, winner := range winnerByTarget {
		supersedesBy[targetID] = winner.messageID
	}
	for {
		added := false
		for targetID, supersederID := range supersedesBy {
			if revoked[targetID] && !revoked[supersederID] {
				revoked[supersederID] = true
				added = true
			}
		}
		if !added {
			break
		}
	}

	// Build final list: include only declarations that are not superseded and not revoked.
	var decls []*Declaration
	for _, e := range all {
		msgID := e.messageID
		// Skip if superseded.
		if supersededIDs[msgID] {
			continue
		}
		// Skip if directly or transitively revoked.
		if revoked[msgID] {
			continue
		}
		decls = append(decls, e.decl)
	}

	// Dedup by (convention, operation): when multiple declarations share the same
	// (convention, operation) tuple without an explicit Supersedes link, keep only
	// the one with the highest version. This handles the case where an operator
	// reinstalls a convention at a new version without setting Supersedes (campfire-03e).
	//
	// Version comparison is lexicographic on dot-separated numeric segments
	// (e.g. "0.1" < "0.2" < "0.10" < "1.0"). Non-numeric segments fall back to
	// string comparison within the segment. When versions are equal, the entry
	// with the higher message timestamp wins; ties are broken by message ID
	// for determinism.
	//
	// Authorization (campfire-f5c): each (convention, operation) slot is owned, on a
	// trust-on-first-use basis, by the signer of its earliest-timestamp declaration.
	// Only that signer (self-upgrade) or, in online mode, the campfire key (owner
	// override) may occupy the slot. An unauthorized writer cannot displace the slot
	// owner regardless of how high a version number it posts — its declaration is
	// dropped from resolution (it remains in the event log for audit). Without this
	// gate any writer could hijack an operation by posting (conv, op)@HUGE_VERSION.
	type convOpKey struct{ convention, operation string }
	type winner struct {
		decl      *Declaration
		timestamp int64
	}

	// Per-message timestamp lookup (avoids an O(n) scan per declaration).
	tsByMsgID := make(map[string]int64, len(all))
	for _, e := range all {
		tsByMsgID[e.messageID] = e.timestamp
	}

	// Determine the trust-on-first-use owner signer for each slot: the signer of
	// the earliest-timestamp declaration (ties broken by message ID for determinism).
	slotOwner := make(map[convOpKey]string)
	slotOwnerTS := make(map[convOpKey]int64)
	slotOwnerMsgID := make(map[convOpKey]string)
	for _, d := range decls {
		key := convOpKey{d.Convention, d.Operation}
		ts := tsByMsgID[d.MessageID]
		cur, exists := slotOwnerTS[key]
		if !exists || ts < cur || (ts == cur && d.MessageID < slotOwnerMsgID[key]) {
			slotOwner[key] = opSenderByMsgID[d.MessageID]
			slotOwnerTS[key] = ts
			slotOwnerMsgID[key] = d.MessageID
		}
	}

	byConvOp := make(map[convOpKey]winner, len(decls))
	for _, d := range decls {
		key := convOpKey{d.Convention, d.Operation}
		ts := tsByMsgID[d.MessageID]
		// The slot owner (earliest declaration) always occupies its own slot.
		// Any later contender must be authorized to take precedence over it.
		if d.MessageID != slotOwnerMsgID[key] &&
			!precedenceAuthorized(opSenderByMsgID[d.MessageID], slotOwner[key], campfireKey) {
			continue
		}
		prev, exists := byConvOp[key]
		if !exists {
			byConvOp[key] = winner{d, ts}
			continue
		}
		cmp := compareVersions(d.Version, prev.decl.Version)
		if cmp > 0 || (cmp == 0 && ts > prev.timestamp) ||
			(cmp == 0 && ts == prev.timestamp && d.MessageID > prev.decl.MessageID) {
			byConvOp[key] = winner{d, ts}
		}
	}
	// Reconstruct decls in original order, keeping only the winners.
	var deduped []*Declaration
	for _, d := range decls {
		key := convOpKey{d.Convention, d.Operation}
		if w, ok := byConvOp[key]; ok && w.decl.MessageID == d.MessageID {
			deduped = append(deduped, d)
			// Consume the winner entry so it only appears once even if MessageID
			// collides (shouldn't happen in practice, but defensive).
			delete(byConvOp, key)
		}
	}
	return deduped, nil
}

// precedenceAuthorized reports whether a declaration signed by candidateSigner is
// permitted to take precedence over a declaration owned by ownerSigner.
//
//   - Self-upgrade: candidateSigner == ownerSigner is always allowed.
//   - Owner override: in online mode (campfireKey non-empty), the campfire key may
//     take precedence over any signer.
//   - An empty candidateSigner is never authorized.
//
// This mirrors the revoke authorization rule in listOperations and is the single
// gate guarding both declaration-precedence paths (Supersedes and version-dedup).
// See campfire-f5c.
func precedenceAuthorized(candidateSigner, ownerSigner, campfireKey string) bool {
	if candidateSigner == "" {
		return false
	}
	if campfireKey != "" && candidateSigner == campfireKey {
		return true // owner override (online mode)
	}
	return ownerSigner != "" && candidateSigner == ownerSigner
}

// compareVersions compares two dot-separated version strings numerically.
// Returns -1, 0, or 1.
// Examples: "0.1" < "0.2", "0.9" < "0.10", "1.0" > "0.9".
func compareVersions(a, b string) int {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")
	// Pad to equal length.
	for len(aParts) < len(bParts) {
		aParts = append(aParts, "0")
	}
	for len(bParts) < len(aParts) {
		bParts = append(bParts, "0")
	}
	for i := range aParts {
		an, aerr := strconv.Atoi(aParts[i])
		bn, berr := strconv.Atoi(bParts[i])
		if aerr == nil && berr == nil {
			if an < bn {
				return -1
			}
			if an > bn {
				return 1
			}
			continue
		}
		// Non-numeric: string compare.
		if aParts[i] < bParts[i] {
			return -1
		}
		if aParts[i] > bParts[i] {
			return 1
		}
	}
	return 0
}

// buildInputSchema constructs a JSON Schema object for the declaration's args.
func buildInputSchema(decl *Declaration, campfireID string) (map[string]any, error) {
	properties := map[string]any{
		"campfire_id": map[string]any{
			"type":        "string",
			"description": "Campfire ID or name",
			"default":     campfireID,
		},
	}
	var required []string

	for _, arg := range decl.Args {
		prop := argToProperty(arg)
		properties[arg.Name] = prop
		if arg.Required {
			required = append(required, arg.Name)
		}
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema, nil
}

// argToProperty converts an ArgDescriptor to a JSON Schema property map.
// If the arg is repeated, it wraps the base type in an array schema.
func argToProperty(arg ArgDescriptor) map[string]any {
	base := baseProperty(arg)
	if arg.Repeated {
		arr := map[string]any{
			"type":  "array",
			"items": base,
		}
		if arg.MaxCount > 0 {
			arr["maxItems"] = arg.MaxCount
		}
		if arg.Description != "" {
			arr["description"] = arg.Description
		}
		return arr
	}
	if arg.Description != "" {
		base["description"] = arg.Description
	}
	return base
}

// baseProperty returns the core JSON Schema property for an arg type.
func baseProperty(arg ArgDescriptor) map[string]any {
	switch arg.Type {
	case "string":
		p := map[string]any{"type": "string"}
		if arg.MaxLength > 0 {
			p["maxLength"] = arg.MaxLength
		}
		if arg.Pattern != "" {
			p["pattern"] = arg.Pattern
		}
		return p

	case "integer":
		p := map[string]any{"type": "integer"}
		if arg.MinSet {
			p["minimum"] = arg.Min
		}
		if arg.Max != 0 {
			p["maximum"] = arg.Max
		}
		return p

	case "duration":
		return map[string]any{
			"type":    "string",
			"pattern": "^[0-9]+[smhd]$",
		}

	case "boolean":
		return map[string]any{"type": "boolean"}

	case "key":
		return map[string]any{
			"type":    "string",
			"pattern": "^[0-9a-f]{64}$",
		}

	case "campfire":
		return map[string]any{
			"type":        "string",
			"description": "Campfire ID or name",
		}

	case "message_id":
		return map[string]any{
			"type":        "string",
			"description": "Message ID",
		}

	case "json":
		return map[string]any{"type": "object"}

	case "tag_set":
		return map[string]any{
			"type":  "array",
			"items": map[string]any{"type": "string"},
		}

	case "enum":
		p := map[string]any{"type": "string"}
		if len(arg.Values) > 0 {
			p["enum"] = arg.Values
		}
		return p

	default:
		return map[string]any{"type": "string"}
	}
}
