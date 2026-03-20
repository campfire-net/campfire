package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/predicate"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/transport"
	"github.com/spf13/cobra"
)

// viewDefinition is the JSON payload stored in a campfire:view message.
type viewDefinition struct {
	Name       string   `json:"name"`
	Predicate  string   `json:"predicate"`
	Projection []string `json:"projection,omitempty"` // field names to include in output
	Ordering   string   `json:"ordering,omitempty"`   // "timestamp asc" (default) or "timestamp desc"
	Limit      int      `json:"limit,omitempty"`      // 0 = no limit
	Refresh    string   `json:"refresh,omitempty"`     // "on-read" (default), "on-write", "periodic"
}

var viewCmd = &cobra.Command{
	Use:   "view",
	Short: "Manage named views (predicate-filtered message queries)",
}

var viewCreateCmd = &cobra.Command{
	Use:   "create <campfire-id> <name>",
	Short: "Create a named view in a campfire",
	Long: `Create a named view by storing a campfire:view message.

The view defines a predicate filter, optional projection, ordering, and limit.
Requires "full" membership role (campfire:* system messages).

Example predicates (S-expression syntax):
  (tag "memory:standing")
  (and (tag "memory:standing") (gt (field "payload.confidence") (literal 0.5)))
  (or (tag "memory:standing") (tag "memory:anchor"))`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		viewPredicate, _ := cmd.Flags().GetString("predicate")
		viewProjection, _ := cmd.Flags().GetString("projection")
		viewOrdering, _ := cmd.Flags().GetString("ordering")
		viewLimit, _ := cmd.Flags().GetInt("limit")
		viewRefresh, _ := cmd.Flags().GetString("refresh")
		return runViewCreate(args[0], args[1], viewPredicate, viewProjection, viewOrdering, viewRefresh, viewLimit)
	},
}

var viewReadCmd = &cobra.Command{
	Use:   "read <campfire-id> <name>",
	Short: "Materialize a named view",
	Long:  `Finds the latest campfire:view definition with the given name, applies the predicate to all messages, and returns filtered/projected/ordered results.`,
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runViewRead(args[0], args[1])
	},
}

var viewListCmd = &cobra.Command{
	Use:   "list <campfire-id>",
	Short: "List defined views in a campfire",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runViewList(args[0])
	},
}

func init() {
	viewCreateCmd.Flags().String("predicate", "", "S-expression predicate (required)")
	viewCreateCmd.Flags().String("projection", "", "comma-separated field names for output projection")
	viewCreateCmd.Flags().String("ordering", "timestamp asc", "ordering: 'timestamp asc' or 'timestamp desc'")
	viewCreateCmd.Flags().Int("limit", 0, "maximum number of results (0 = no limit)")
	viewCreateCmd.Flags().String("refresh", "on-read", "refresh strategy: on-read (only supported in P1)")
	viewCreateCmd.MarkFlagRequired("predicate") //nolint:errcheck

	viewCmd.AddCommand(viewCreateCmd)
	viewCmd.AddCommand(viewReadCmd)
	viewCmd.AddCommand(viewListCmd)
	rootCmd.AddCommand(viewCmd)
}

func runViewCreate(campfireIDArg, name, viewPredicate, viewProjection, viewOrdering, viewRefresh string, viewLimit int) error {
	agentID, err := identity.Load(IdentityPath())
	if err != nil {
		return fmt.Errorf("loading identity: %w", err)
	}

	s, err := store.Open(store.StorePath(CFHome()))
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer s.Close()

	campfireID, err := resolveCampfireID(campfireIDArg, s)
	if err != nil {
		return err
	}

	// Verify membership and role.
	m, err := s.GetMembership(campfireID)
	if err != nil {
		return fmt.Errorf("querying membership: %w", err)
	}
	if m == nil {
		return fmt.Errorf("not a member of campfire %s", campfireID[:min(12, len(campfireID))])
	}

	// View creation uses campfire:view tag — requires full role.
	tags := []string{"campfire:view"}
	if err := checkRoleCanSend(m.Role, tags); err != nil {
		return err
	}

	// Validate predicate syntax.
	if _, err := predicate.Parse(viewPredicate); err != nil {
		return fmt.Errorf("invalid predicate: %w", err)
	}

	// Validate ordering.
	ordering := strings.TrimSpace(viewOrdering)
	if ordering != "" && ordering != "timestamp asc" && ordering != "timestamp desc" {
		return fmt.Errorf("invalid ordering %q: must be 'timestamp asc' or 'timestamp desc'", ordering)
	}

	// Validate refresh strategy.
	refresh := strings.TrimSpace(viewRefresh)
	if refresh != "on-read" {
		return fmt.Errorf("unsupported refresh strategy %q: only 'on-read' is supported in P1", refresh)
	}

	// Build projection list.
	var projection []string
	if viewProjection != "" {
		for _, f := range strings.Split(viewProjection, ",") {
			f = strings.TrimSpace(f)
			if f != "" {
				projection = append(projection, f)
			}
		}
	}

	// Build view definition payload.
	def := viewDefinition{
		Name:       name,
		Predicate:  viewPredicate,
		Projection: projection,
		Ordering:   ordering,
		Limit:      viewLimit,
		Refresh:    refresh,
	}
	payloadBytes, err := json.Marshal(def)
	if err != nil {
		return fmt.Errorf("encoding view definition: %w", err)
	}

	// Route to transport — same path as cf send.
	// sendP2PHTTP stores locally itself; sendFilesystem and sendGitHub do not.
	var msg *message.Message
	transportType := transport.ResolveType(*m)
	switch transportType {
	case transport.TypeGitHub:
		msg, err = sendGitHub(campfireID, string(payloadBytes), tags, []string{}, "", agentID, s, m)
	case transport.TypePeerHTTP:
		msg, err = sendP2PHTTP(campfireID, string(payloadBytes), tags, []string{}, "", agentID, s, m)
	default:
		msg, err = sendFilesystem(campfireID, string(payloadBytes), tags, []string{}, "", agentID, m.TransportDir)
	}
	if err != nil {
		return fmt.Errorf("sending view message: %w", err)
	}

	// Store locally for filesystem and GitHub transports (P2P HTTP stores in sendP2PHTTP).
	if transportType != transport.TypePeerHTTP {
		if _, err := s.AddMessage(store.MessageRecordFromMessage(campfireID, msg, store.NowNano())); err != nil {
			return fmt.Errorf("storing view message: %w", err)
		}
	}

	if jsonOutput {
		out := map[string]any{
			"id":          msg.ID,
			"campfire_id": campfireID,
			"name":        name,
			"predicate":   viewPredicate,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Println(msg.ID)
	return nil
}

func runViewRead(campfireIDArg, name string) error {
	s, err := store.Open(store.StorePath(CFHome()))
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer s.Close()

	campfireID, err := resolveCampfireID(campfireIDArg, s)
	if err != nil {
		return err
	}

	// Find the latest campfire:view message with this name.
	def, err := findLatestView(s, campfireID, name)
	if err != nil {
		return err
	}
	if def == nil {
		return fmt.Errorf("view %q not found in campfire %s", name, campfireID[:min(12, len(campfireID))])
	}

	// Parse the predicate.
	pred, err := predicate.Parse(def.Predicate)
	if err != nil {
		return fmt.Errorf("invalid predicate in view definition: %w", err)
	}

	// Load all messages (not just unread — views see everything).
	// RespectCompaction: true so superseded messages are excluded from view results.
	allMsgs, err := s.ListMessages(campfireID, 0, store.MessageFilter{RespectCompaction: true})
	if err != nil {
		return fmt.Errorf("listing messages: %w", err)
	}

	// Evaluate predicate against each message, skipping campfire:* system messages.
	// System messages (e.g. campfire:view definitions) must not appear in view
	// results. This is especially important for negation predicates like
	// (not (tag "foo")) which would otherwise match system messages that lack
	// the negated tag.
	var matched []store.MessageRecord
	for _, m := range allMsgs {
		ctx := buildMessageContext(m)
		isSystem := false
		for _, tag := range ctx.Tags {
			if strings.HasPrefix(tag, "campfire:") {
				isSystem = true
				break
			}
		}
		if isSystem {
			continue
		}
		if predicate.Eval(pred, ctx) {
			matched = append(matched, m)
		}
	}

	// Apply ordering.
	ordering := strings.TrimSpace(def.Ordering)
	if ordering == "timestamp desc" {
		sort.Slice(matched, func(i, j int) bool {
			return matched[i].Timestamp > matched[j].Timestamp
		})
	}
	// Default "timestamp asc" is already the natural order from ListMessages.

	// Apply limit.
	if def.Limit > 0 && len(matched) > def.Limit {
		matched = matched[:def.Limit]
	}

	// Output.
	if jsonOutput {
		return outputViewJSON(matched, def.Projection)
	}

	if len(matched) == 0 {
		fmt.Println("No messages match view predicate.")
		return nil
	}

	if len(def.Projection) > 0 {
		return outputViewProjected(matched, def.Projection)
	}

	printMessages(matched, s)
	return nil
}

func runViewList(campfireIDArg string) error {
	s, err := store.Open(store.StorePath(CFHome()))
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer s.Close()

	campfireID, err := resolveCampfireID(campfireIDArg, s)
	if err != nil {
		return err
	}

	// Find all campfire:view messages.
	views, err := findAllViews(s, campfireID)
	if err != nil {
		return err
	}

	if jsonOutput {
		type viewEntry struct {
			Name      string `json:"name"`
			Predicate string `json:"predicate"`
			MessageID string `json:"message_id"`
		}
		var out []viewEntry
		for _, v := range views {
			out = append(out, viewEntry{
				Name:      v.def.Name,
				Predicate: v.def.Predicate,
				MessageID: v.msgID,
			})
		}
		if out == nil {
			out = []viewEntry{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	if len(views) == 0 {
		fmt.Println("No views defined.")
		return nil
	}

	for _, v := range views {
		fmt.Printf("  %s — %s\n", v.def.Name, v.def.Predicate)
	}
	return nil
}

// findLatestView finds the most recent campfire:view message with the given name.
func findLatestView(s *store.Store, campfireID, name string) (*viewDefinition, error) {
	// Use tag-filtered query to get campfire:view messages efficiently.
	msgs, err := s.ListMessages(campfireID, 0, store.MessageFilter{Tags: []string{"campfire:view"}})
	if err != nil {
		return nil, fmt.Errorf("listing view messages: %w", err)
	}

	// Find the latest one with matching name (messages are ordered by timestamp asc).
	var latest *viewDefinition
	for i := len(msgs) - 1; i >= 0; i-- {
		var def viewDefinition
		if err := json.Unmarshal(msgs[i].Payload, &def); err != nil {
			continue
		}
		if def.Name == name {
			latest = &def
			break
		}
	}
	return latest, nil
}

type viewInfo struct {
	def   viewDefinition
	msgID string
}

// findAllViews returns the latest definition for each unique view name.
func findAllViews(s *store.Store, campfireID string) ([]viewInfo, error) {
	msgs, err := s.ListMessages(campfireID, 0, store.MessageFilter{Tags: []string{"campfire:view"}})
	if err != nil {
		return nil, fmt.Errorf("listing view messages: %w", err)
	}

	// Latest definition per name (later messages override earlier ones).
	seen := map[string]viewInfo{}
	for _, m := range msgs {
		var def viewDefinition
		if err := json.Unmarshal(m.Payload, &def); err != nil {
			continue
		}
		seen[def.Name] = viewInfo{def: def, msgID: m.ID}
	}

	// Convert to sorted slice.
	var result []viewInfo
	for _, v := range seen {
		result = append(result, v)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].def.Name < result[j].def.Name
	})
	return result, nil
}

// buildMessageContext creates a predicate.MessageContext from a store.MessageRecord.
func buildMessageContext(m store.MessageRecord) *predicate.MessageContext {
	var tags []string
	json.Unmarshal([]byte(m.Tags), &tags) //nolint:errcheck

	// Try to parse payload as JSON for field access.
	var payload map[string]any
	json.Unmarshal(m.Payload, &payload) //nolint:errcheck

	return &predicate.MessageContext{
		Tags:      tags,
		Sender:    m.Sender,
		Timestamp: m.Timestamp,
		Payload:   payload,
	}
}

// outputViewJSON outputs matched messages as JSON, with optional projection.
func outputViewJSON(msgs []store.MessageRecord, projection []string) error {
	if len(projection) == 0 {
		// Full message output.
		type jsonMsg struct {
			ID          string          `json:"id"`
			CampfireID  string          `json:"campfire_id"`
			Sender      string          `json:"sender"`
			Instance    string          `json:"instance,omitempty"`
			Payload     string          `json:"payload"`
			Tags        []string        `json:"tags"`
			Antecedents []string        `json:"antecedents"`
			Timestamp   int64           `json:"timestamp"`
			Provenance  json.RawMessage `json:"provenance"`
		}
		var out []jsonMsg
		for _, m := range msgs {
			var tags []string
			json.Unmarshal([]byte(m.Tags), &tags) //nolint:errcheck
			var antecedents []string
			json.Unmarshal([]byte(m.Antecedents), &antecedents) //nolint:errcheck
			if antecedents == nil {
				antecedents = []string{}
			}
			out = append(out, jsonMsg{
				ID:          m.ID,
				CampfireID:  m.CampfireID,
				Sender:      m.Sender,
				Instance:    m.Instance,
				Payload:     string(m.Payload),
				Tags:        tags,
				Antecedents: antecedents,
				Timestamp:   m.Timestamp,
				Provenance:  json.RawMessage(m.Provenance),
			})
		}
		if out == nil {
			out = []jsonMsg{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	// Projected output: only include specified fields.
	projSet := make(map[string]bool, len(projection))
	for _, f := range projection {
		projSet[f] = true
	}

	var out []map[string]any
	for _, m := range msgs {
		entry := map[string]any{}
		if projSet["id"] {
			entry["id"] = m.ID
		}
		if projSet["campfire_id"] {
			entry["campfire_id"] = m.CampfireID
		}
		if projSet["sender"] {
			entry["sender"] = m.Sender
		}
		if projSet["instance"] {
			entry["instance"] = m.Instance
		}
		if projSet["payload"] {
			entry["payload"] = string(m.Payload)
		}
		if projSet["tags"] {
			var tags []string
			json.Unmarshal([]byte(m.Tags), &tags) //nolint:errcheck
			entry["tags"] = tags
		}
		if projSet["antecedents"] {
			var antecedents []string
			json.Unmarshal([]byte(m.Antecedents), &antecedents) //nolint:errcheck
			entry["antecedents"] = antecedents
		}
		if projSet["timestamp"] {
			entry["timestamp"] = m.Timestamp
		}
		if projSet["provenance"] {
			entry["provenance"] = json.RawMessage(m.Provenance)
		}
		out = append(out, entry)
	}
	if out == nil {
		out = []map[string]any{}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// outputViewProjected prints projected fields in a compact human-readable format.
func outputViewProjected(msgs []store.MessageRecord, projection []string) error {
	for _, m := range msgs {
		parts := []string{}
		for _, field := range projection {
			switch field {
			case "id":
				parts = append(parts, fmt.Sprintf("id=%s", m.ID))
			case "sender":
				short := m.Sender
				if len(short) > 12 {
					short = short[:12]
				}
				parts = append(parts, fmt.Sprintf("sender=%s", short))
			case "tags":
				parts = append(parts, fmt.Sprintf("tags=%s", m.Tags))
			case "payload":
				parts = append(parts, fmt.Sprintf("payload=%s", string(m.Payload)))
			case "timestamp":
				parts = append(parts, fmt.Sprintf("timestamp=%d", m.Timestamp))
			case "instance":
				if m.Instance != "" {
					parts = append(parts, fmt.Sprintf("instance=%s", m.Instance))
				}
			}
		}
		fmt.Println(strings.Join(parts, " "))
	}
	return nil
}
