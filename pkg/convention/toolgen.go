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
func ListOperations(s StoreReader, campfireID, campfireKey string) ([]*Declaration, error) {
	msgs, err := s.ListMessages(campfireID, 0, store.MessageFilter{
		Tags: []string{"convention:operation"},
	})
	if err != nil {
		return nil, err
	}
	var decls []*Declaration
	for _, msg := range msgs {
		decl, _, err := Parse(msg.Tags, msg.Payload, msg.Sender, campfireKey)
		if err != nil {
			continue // skip malformed
		}
		decl.MessageID = msg.ID
		decls = append(decls, decl)
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
