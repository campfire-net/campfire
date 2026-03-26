package convention

import (
	"encoding/json"
	"strings"

	"github.com/campfire-net/campfire/pkg/store"
)

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
	desc := decl.Description
	if len(desc) > 80 {
		desc = desc[:80]
	}
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
	// Collision: prefix with convention slug (hyphens → underscores).
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
func ListOperations(s StoreReader, campfireID, campfireKey string) ([]*Declaration, error) {
	return listOperations(s, campfireID, campfireKey, "")
}

// ListOperationsWithRegistry reads declarations from campfireID (inline) and, when
// registryCampfireID is non-empty, also from the convention registry campfire.
// Messages from both sources are merged before supersede and revoke filtering,
// so registry declarations can supersede inline ones via the Supersedes field.
// When registryCampfireID is empty, this is identical to ListOperations.
func ListOperationsWithRegistry(s StoreReader, campfireID, campfireKey, registryCampfireID string) ([]*Declaration, error) {
	return listOperations(s, campfireID, campfireKey, registryCampfireID)
}

// listOperations is the shared implementation used by ListOperations and
// ListOperationsWithRegistry.
func listOperations(s StoreReader, campfireID, campfireKey, registryCampfireID string) ([]*Declaration, error) {
	// Collect operation declarations.
	opMsgs, err := s.ListMessages(campfireID, 0, store.MessageFilter{
		Tags: []string{ConventionOperationTag},
	})
	if err != nil {
		return nil, err
	}

	// Collect revoke messages.
	revokeMsgs, err := s.ListMessages(campfireID, 0, store.MessageFilter{
		Tags: []string{conventionRevokeTag},
	})
	if err != nil {
		return nil, err
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
			originalSigner, known := opSenderByMsgID[revokePayload.TargetID]
			if !known || msg.Sender != originalSigner {
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
	}
	var all []opEntry
	for _, msg := range opMsgs {
		decl, _, parseErr := Parse(msg.Tags, msg.Payload, msg.Sender, campfireKey)
		if parseErr != nil {
			continue // skip malformed
		}
		decl.MessageID = msg.ID
		all = append(all, opEntry{decl: decl, messageID: msg.ID, timestamp: msg.Timestamp})
	}

	// Build supersede winner map: for each target, find the superseding entry with
	// the highest timestamp. All other candidates claiming to supersede the same
	// target are treated as superseded themselves.
	winnerByTarget := make(map[string]opEntry) // target msgID -> winning entry
	for _, e := range all {
		if e.decl.Supersedes == "" {
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
	return decls, nil
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
		if arg.Min != 0 {
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
