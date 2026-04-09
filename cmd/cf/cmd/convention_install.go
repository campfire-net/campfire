package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/protocol"
	"github.com/spf13/cobra"
)

// installResult is the output for a single installed declaration.
type installResult struct {
	File      string `json:"file"`
	Operation string `json:"operation,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	Skipped   bool   `json:"skipped,omitempty"`
	Error     string `json:"error,omitempty"`
}

var conventionInstallCmd = &cobra.Command{
	Use:   "install <campfire-id> [file|dir]",
	Short: "Post validated declarations to an existing campfire",
	Long: `Post validated convention declarations to an existing campfire.

  cf convention install <campfire-id> <file.json>      install from a single file
  cf convention install <campfire-id> <dir/>           install all .json files in directory
  cf convention install <campfire-id> --from <source>  copy all declarations from source campfire

Safety:
  - Lint runs automatically; installation is refused if lint fails.
  - Duplicate detection: same convention+operation+version is skipped.
  - The caller must be a member of the target campfire.`,
	RunE: runConventionInstall,
}

var conventionInstallFrom string

func init() {
	conventionInstallCmd.Flags().StringVar(&conventionInstallFrom, "from", "", "source campfire ID to copy all declarations from")
	conventionCmd.AddCommand(conventionInstallCmd)
}

func runConventionInstall(_ *cobra.Command, args []string) error {
	// Require campfire-id as first positional arg.
	if len(args) < 1 {
		return fmt.Errorf("campfire-id is required")
	}
	targetName := args[0]

	// Validate: must have either a file/dir arg or --from, not both.
	if conventionInstallFrom != "" && len(args) > 1 {
		return fmt.Errorf("cannot use both a file/dir argument and --from")
	}
	if conventionInstallFrom == "" && len(args) < 2 {
		return fmt.Errorf("a file/dir argument or --from is required")
	}

	agentID, s, err := requireAgentAndStore()
	if err != nil {
		return err
	}
	defer s.Close()

	// Resolve target campfire ID.
	targetID, err := resolveCampfireID(targetName, s)
	if err != nil {
		return fmt.Errorf("resolving target campfire %q: %w", targetName, err)
	}

	// Verify membership in target.
	m, err := s.GetMembership(targetID)
	if err != nil {
		return fmt.Errorf("querying membership for campfire %s: %w", targetID[:12], err)
	}
	if m == nil {
		return fmt.Errorf("not a member of campfire %s — join first", targetID[:12])
	}

	// Load existing declarations on target for duplicate detection.
	existing, err := loadExistingDeclarations(s, targetID)
	if err != nil {
		return fmt.Errorf("loading existing declarations from target: %w", err)
	}

	// Gather sources: either from --from campfire or from file/dir.
	var sources []declSource
	if conventionInstallFrom != "" {
		// Copy declarations from source campfire.
		sourceID, err := resolveCampfireID(conventionInstallFrom, s)
		if err != nil {
			return fmt.Errorf("resolving source campfire %q: %w", conventionInstallFrom, err)
		}
		decls, err := listConventionOperations(context.Background(), s, sourceID)
		if err != nil {
			return fmt.Errorf("reading declarations from source campfire %s: %w", sourceID[:12], err)
		}
		if len(decls) == 0 {
			return fmt.Errorf("no convention declarations found in source campfire %s", sourceID[:12])
		}
		for _, d := range decls {
			payload, err := json.Marshal(d)
			if err != nil {
				return fmt.Errorf("serializing declaration %q: %w", d.Operation, err)
			}
			sources = append(sources, declSource{name: "campfire:" + d.Operation, payload: payload})
		}
	} else {
		sources, err = readDeclarationsFromPath(args[1])
		if err != nil {
			return err
		}
		if len(sources) == 0 {
			return fmt.Errorf("no .json declaration files found in %q", args[1])
		}
	}

	client := protocol.New(s, agentID)
	var results []installResult
	allOK := true

	for _, src := range sources {
		result := installSingle(src, targetID, agentID, client, existing)
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

	installed, skipped, failed := 0, 0, 0
	for _, r := range results {
		switch {
		case r.Skipped:
			fmt.Fprintf(os.Stdout, "  skip  %s (duplicate — same convention+operation+version)\n", r.Operation)
			skipped++
		case r.Error != "":
			fmt.Fprintf(os.Stdout, "  FAIL  %s: %s\n", r.File, r.Error)
			failed++
		default:
			fmt.Fprintf(os.Stdout, "  ok    %s → %s\n", r.Operation, r.MessageID)
			installed++
		}
	}
	fmt.Fprintf(os.Stdout, "\n%d installed, %d skipped (duplicate), %d failed\n", installed, skipped, failed)

	if !allOK {
		return fmt.Errorf("one or more declarations failed to install")
	}
	return nil
}

// installSingle lints, checks for duplicates, and posts one declaration to the campfire.
// existing is updated in-place so subsequent calls in the same batch detect duplicates.
func installSingle(
	src declSource,
	targetID string,
	agentID *identity.Identity,
	client *protocol.Client,
	existing map[string]*convention.Declaration,
) installResult {
	result := installResult{File: src.name}

	// Lint — refuse if errors.
	lintResult := convention.Lint(src.payload)
	if len(lintResult.Errors) > 0 {
		result.Error = fmt.Sprintf("lint failed: %s", lintResult.Errors[0].Message)
		return result
	}

	// Parse to extract convention+operation+version for duplicate detection.
	decl, _, err := convention.Parse(
		[]string{convention.ConventionOperationTag},
		src.payload,
		agentID.PublicKeyHex(),
		agentID.PublicKeyHex(),
	)
	if err != nil {
		result.Error = fmt.Sprintf("parse failed: %s", err)
		return result
	}
	result.Operation = decl.Operation

	// Duplicate detection.
	conflictKey := decl.Convention + ":" + decl.Operation + "@" + decl.Version
	if _, dup := existing[conflictKey]; dup {
		result.Skipped = true
		return result
	}

	// Post the declaration via the protocol client.
	msg, err := client.Send(protocol.SendRequest{
		CampfireID: targetID,
		Payload:    src.payload,
		Tags:       []string{convention.ConventionOperationTag},
	})
	if err != nil {
		result.Error = fmt.Sprintf("send failed: %s", err)
		return result
	}
	result.MessageID = msg.ID

	// Update existing so subsequent installs in the same batch detect this duplicate.
	existing[conflictKey] = decl
	return result
}
