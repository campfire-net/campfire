package cmd

import (
	"context"
	"fmt"

	"github.com/campfire-net/campfire/cf-conventions/cf-convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/pkg/trust"
	"github.com/spf13/cobra"
)

var conventionAdoptFromCampfire string

var conventionAdoptCmd = &cobra.Command{
	Use:   "adopt [file|dir]",
	Short: "Register convention declarations in the local trust policy via ~home",
	Long: `Register convention declarations in the local trust policy.

Declarations are posted to the ~home campfire as convention:operation messages.
The local trust engine (loadLocalPolicyEngine) reads ~home on each boot, so
adopted conventions automatically appear in 'cf trust show' as source "seed".

Usage:
  cf convention adopt <file.json>          adopt a declaration from a file
  cf convention adopt <dir/>               adopt all .json declarations in a directory
  cf convention adopt --from <campfire-id> copy all declarations from a source campfire

Duplicate detection: adopting the same convention+operation+version twice is a no-op.`,
	RunE: runConventionAdopt,
}

func init() {
	conventionAdoptCmd.Flags().StringVar(&conventionAdoptFromCampfire, "from", "", "source campfire ID — copy all its declarations to ~home")
	conventionCmd.AddCommand(conventionAdoptCmd)
}

func runConventionAdopt(cmd *cobra.Command, args []string) error {
	// Validate args: either --from OR a positional file/dir, not both, not neither.
	if conventionAdoptFromCampfire != "" && len(args) > 0 {
		return fmt.Errorf("--from and a file/dir argument are mutually exclusive")
	}
	if conventionAdoptFromCampfire == "" && len(args) == 0 {
		return fmt.Errorf("provide a file/dir argument or --from <campfire-id>")
	}

	agentID, s, err := requireAgentAndStore()
	if err != nil {
		return err
	}
	defer s.Close()

	// Resolve ~home.
	homeID, err := resolveCampfireID("~home", s)
	if err != nil {
		return fmt.Errorf("no ~home campfire — run cf init first")
	}

	// Collect source payloads.
	var sources []declSource

	if conventionAdoptFromCampfire != "" {
		// --from mode: read all declarations from the source campfire.
		sourceID, err := resolveCampfireID(conventionAdoptFromCampfire, s)
		if err != nil {
			return fmt.Errorf("resolving source campfire %q: %w", conventionAdoptFromCampfire, err)
		}
		decls, err := listConventionOperations(context.Background(), s, sourceID)
		if err != nil {
			return fmt.Errorf("reading declarations from campfire: %w", err)
		}
		if len(decls) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No convention declarations found in source campfire.")
			return nil
		}
		// Re-read raw payloads from the store so we can post them verbatim.
		rawPayloads, err := loadRawDeclarationPayloads(s, sourceID)
		if err != nil {
			return fmt.Errorf("loading raw declaration payloads: %w", err)
		}
		for _, decl := range decls {
			key := decl.Convention + ":" + decl.Operation + "@" + decl.Version
			payload, ok := rawPayloads[key]
			if !ok {
				continue
			}
			sources = append(sources, declSource{
				name:    decl.Convention + "/" + decl.Operation,
				payload: payload,
			})
		}
	} else {
		// File/dir mode.
		sources, err = readDeclarationsFromPath(args[0])
		if err != nil {
			return err
		}
		if len(sources) == 0 {
			return fmt.Errorf("no .json declaration files found in %q", args[0])
		}
	}

	// Load existing declarations from ~home for duplicate detection.
	existing, err := loadExistingDeclarations(s, homeID)
	if err != nil {
		return fmt.Errorf("loading existing ~home declarations: %w", err)
	}

	adopted := 0
	skipped := 0

	for _, src := range sources {
		ok, fp, opName, adoptErr := adoptSingle(src, homeID, agentID, s, existing)
		if adoptErr != nil {
			// Print lint or parse errors to stdout and fail immediately.
			fmt.Fprintf(cmd.OutOrStdout(), "error: %s: %s\n", src.name, adoptErr)
			return adoptErr
		}
		if !ok {
			fmt.Fprintf(cmd.OutOrStdout(), "  skip  %s (already adopted)\n", opName)
			skipped++
			continue
		}
		// Register in duplicate map so subsequent entries in this batch are detected.
		decl, _, _ := convention.Parse(
			[]string{convention.ConventionOperationTag},
			src.payload,
			agentID.PublicKeyHex(),
			agentID.PublicKeyHex(),
			convention.DefaultDeniedTagPrefixes,
		)
		if decl != nil {
			conflictKey := decl.Convention + ":" + decl.Operation + "@" + decl.Version
			existing[conflictKey] = decl
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  ok    %s  %s\n", opName, shortFingerprint(fp))
		adopted++
	}

	fmt.Fprintf(cmd.OutOrStdout(), "\nAdopted %d convention(s)", adopted)
	if skipped > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), " (%d skipped — already adopted)", skipped)
	}
	fmt.Fprintln(cmd.OutOrStdout(), ". Run 'cf trust show' to verify.")
	return nil
}

// adoptSingle lints, checks for duplicates, and posts one declaration to ~home.
// Returns (adopted bool, fingerprint string, operationName string, error).
func adoptSingle(
	src declSource,
	homeID string,
	agentID *identity.Identity,
	s store.Store,
	existing map[string]*convention.Declaration,
) (bool, string, string, error) {
	// Lint — refuse if errors.
	lintResult := convention.Lint(src.payload)
	if len(lintResult.Errors) > 0 {
		return false, "", "", fmt.Errorf("lint failed: %s", lintResult.Errors[0].Message)
	}

	// Parse to extract key.
	decl, _, err := convention.Parse(
		[]string{convention.ConventionOperationTag},
		src.payload,
		agentID.PublicKeyHex(),
		agentID.PublicKeyHex(),
		convention.DefaultDeniedTagPrefixes,
	)
	if err != nil {
		return false, "", "", fmt.Errorf("parse failed: %s", err)
	}

	// Duplicate detection.
	conflictKey := decl.Convention + ":" + decl.Operation + "@" + decl.Version
	if _, dup := existing[conflictKey]; dup {
		return false, "", decl.Convention+"/"+decl.Operation, nil
	}

	// Compute semantic fingerprint for display.
	fp, fpErr := trust.SemanticFingerprint(decl)
	if fpErr != nil {
		fp = "(unknown)"
	}

	// Post to ~home via transport.
	client := protocol.New(s, agentID)
	_, sendErr := client.Send(protocol.SendRequest{
		CampfireID: homeID,
		Payload:    src.payload,
		Tags:       []string{convention.ConventionOperationTag},
	})
	if sendErr != nil {
		return false, fp, decl.Convention + "/" + decl.Operation, fmt.Errorf("send failed: %s", sendErr)
	}
	return true, fp, decl.Convention + "/" + decl.Operation, nil
}

// loadRawDeclarationPayloads reads the raw JSON payload for each convention:operation message
// from a campfire. Returns a map keyed by "convention:operation@version".
func loadRawDeclarationPayloads(s store.Store, sourceID string) (map[string][]byte, error) {
	msgs, err := s.ListMessages(sourceID, 0, store.MessageFilter{
		Tags: []string{convention.ConventionOperationTag},
	})
	if err != nil {
		return nil, err
	}
	result := make(map[string][]byte, len(msgs))
	for _, msg := range msgs {
		decl, _, err := convention.Parse(msg.Tags, msg.Payload, msg.Sender, "", convention.DefaultDeniedTagPrefixes)
		if err != nil {
			continue
		}
		key := decl.Convention + ":" + decl.Operation + "@" + decl.Version
		result[key] = msg.Payload
	}
	return result, nil
}
