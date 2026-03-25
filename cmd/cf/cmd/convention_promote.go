package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/message"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/spf13/cobra"
)

// promoteResult is the output for a single promoted declaration.
type promoteResult struct {
	File      string `json:"file"`
	Operation string `json:"operation,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	Skipped   bool   `json:"skipped,omitempty"`
	Error     string `json:"error,omitempty"`
}

var conventionPromoteCmd = &cobra.Command{
	Use:   "promote <file|dir> --registry <campfire-id>",
	Short: "Publish validated declarations to a live convention registry",
	Long: `Publish validated convention declarations to a live convention registry campfire.

Safety:
  - Lint runs automatically; promotion is refused if lint fails.
  - Existing declarations with the same convention+operation+version require --force.
  - The caller must be a member of the registry campfire.`,
	Args: cobra.ExactArgs(1),
	RunE: runConventionPromote,
}

var (
	conventionPromoteRegistry string
	conventionPromoteForce    bool
)

func init() {
	conventionPromoteCmd.Flags().StringVar(&conventionPromoteRegistry, "registry", "", "convention registry campfire ID (required)")
	conventionPromoteCmd.Flags().BoolVar(&conventionPromoteForce, "force", false, "overwrite existing declaration with same convention+version")
	_ = conventionPromoteCmd.MarkFlagRequired("registry")
	conventionCmd.AddCommand(conventionPromoteCmd)
}

func runConventionPromote(_ *cobra.Command, args []string) error {
	sources, err := readDeclarationsFromPath(args[0])
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		return fmt.Errorf("no .json declaration files found in %q", args[0])
	}

	// Load agent identity and open store.
	agentID, s, err := requireAgentAndStore()
	if err != nil {
		return err
	}
	defer s.Close()

	// Resolve registry campfire ID.
	registryID, err := resolveCampfireID(conventionPromoteRegistry, s)
	if err != nil {
		return fmt.Errorf("resolving registry campfire %q: %w", conventionPromoteRegistry, err)
	}

	// Verify membership.
	m, err := s.GetMembership(registryID)
	if err != nil {
		return fmt.Errorf("querying membership for registry %s: %w", registryID[:12], err)
	}
	if m == nil {
		return fmt.Errorf("not a member of registry campfire %s — join first", registryID[:12])
	}

	// Load existing declarations to detect conflicts.
	existing, err := loadExistingDeclarations(s, registryID)
	if err != nil {
		return fmt.Errorf("loading existing declarations: %w", err)
	}

	var results []promoteResult
	allOK := true

	for _, src := range sources {
		result := promoteSingle(src, registryID, agentID, s, existing)
		results = append(results, result)
		if result.Error != "" {
			allOK = false
		}
	}

	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"ok":      allOK,
			"results": results,
		})
	}

	for _, r := range results {
		if r.Skipped {
			fmt.Fprintf(os.Stdout, "  skip  %s (already published; use --force to overwrite)\n", r.Operation)
		} else if r.Error != "" {
			fmt.Fprintf(os.Stdout, "  FAIL  %s: %s\n", r.File, r.Error)
		} else {
			fmt.Fprintf(os.Stdout, "  ok    %s → %s\n", r.Operation, r.MessageID)
		}
	}

	if !allOK {
		return fmt.Errorf("one or more declarations failed to promote")
	}
	return nil
}

// promoteSingle lints, checks for conflicts, and publishes one declaration.
func promoteSingle(
	src declSource,
	registryID string,
	agentID *identity.Identity,
	s store.Store,
	existing map[string]*convention.Declaration,
) promoteResult {
	result := promoteResult{File: src.name}

	// Lint first — refuse if errors.
	lintResult := convention.Lint(src.payload)
	if len(lintResult.Errors) > 0 {
		result.Error = fmt.Sprintf("lint failed: %s", lintResult.Errors[0].Message)
		return result
	}

	// Parse to get convention+operation+version for conflict detection.
	decl, _, err := convention.Parse(
		[]string{"convention:operation"},
		src.payload,
		agentID.PublicKeyHex(),
		agentID.PublicKeyHex(),
	)
	if err != nil {
		result.Error = fmt.Sprintf("parse failed: %s", err)
		return result
	}
	result.Operation = decl.Operation

	// Conflict check.
	conflictKey := decl.Convention + ":" + decl.Operation + "@" + decl.Version
	if _, conflict := existing[conflictKey]; conflict && !conventionPromoteForce {
		result.Skipped = true
		return result
	}

	// Sign and store the message in the local registry campfire.
	msgID, err := signAndStoreDeclaration(src.payload, registryID, agentID, s)
	if err != nil {
		result.Error = fmt.Sprintf("send failed: %s", err)
		return result
	}
	result.MessageID = msgID
	return result
}

// loadExistingDeclarations reads all convention:operation messages from the registry.
// Returns a map keyed by "convention:operation@version".
func loadExistingDeclarations(s store.Store, registryID string) (map[string]*convention.Declaration, error) {
	msgs, err := s.ListMessages(registryID, 0, store.MessageFilter{
		Tags: []string{"convention:operation"},
	})
	if err != nil {
		return nil, err
	}
	result := make(map[string]*convention.Declaration)
	for _, msg := range msgs {
		decl, _, err := convention.Parse(msg.Tags, msg.Payload, msg.Sender, "")
		if err != nil {
			continue
		}
		key := decl.Convention + ":" + decl.Operation + "@" + decl.Version
		result[key] = decl
	}
	return result, nil
}

// signAndStoreDeclaration creates a signed message and stores it in the local store.
// This uses the local SQLite store directly — suitable for local convention registry
// campfires. For remote registries the caller would need to use the campfire transport.
func signAndStoreDeclaration(payload []byte, campfireID string, agentID *identity.Identity, s store.Store) (string, error) {
	tags := []string{"convention:operation"}
	msg, err := message.NewMessage(agentID.PrivateKey, agentID.PublicKey, payload, tags, nil)
	if err != nil {
		return "", fmt.Errorf("creating signed message: %w", err)
	}
	rec := store.MessageRecord{
		ID:         msg.ID,
		CampfireID: campfireID,
		Sender:     agentID.PublicKeyHex(),
		Payload:    payload,
		Tags:       tags,
		Timestamp:  msg.Timestamp,
		Signature:  msg.Signature,
		ReceivedAt: store.NowNano(),
	}
	if _, err := s.AddMessage(rec); err != nil {
		return "", fmt.Errorf("storing message: %w", err)
	}
	return msg.ID, nil
}
