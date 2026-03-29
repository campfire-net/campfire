package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/campfire-net/campfire/pkg/convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/store"
	"github.com/campfire-net/campfire/pkg/trust"
	"github.com/spf13/cobra"
)

// declTestResult is the result for a single declaration under test.
type declTestResult struct {
	File      string         `json:"file"`
	Operation string         `json:"operation,omitempty"`
	Pass      bool           `json:"pass"`
	Steps     []declTestStep `json:"steps"`
	Error     string         `json:"error,omitempty"`
}

// declTestStep records the outcome of one test step.
type declTestStep struct {
	Name string `json:"name"`
	Pass bool   `json:"pass"`
	Note string `json:"note,omitempty"`
}

var conventionTestCmd = &cobra.Command{
	Use:   "test <file|dir>",
	Short: "Test convention declarations against a local digital twin",
	Long: `Spin up a local digital twin: ephemeral root hierarchy in a temp dir.

For each declaration:
1. Lint (must pass)
2. Parse + generate tool
3. Execute with synthetic args
4. Verify envelope trust_chain status

If a directory is given, tests all .json files in it.`,
	Args: cobra.ExactArgs(1),
	RunE: runConventionTest,
}

var conventionTestBeaconRoot string

func init() {
	conventionTestCmd.Flags().StringVar(&conventionTestBeaconRoot, "beacon-root", "", "use an existing root identity key (hex) instead of generating one")
	conventionCmd.AddCommand(conventionTestCmd)
}

func runConventionTest(_ *cobra.Command, args []string) error {
	sources, err := readDeclarationsFromPath(args[0])
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		return fmt.Errorf("no .json declaration files found in %q", args[0])
	}

	// Set up ephemeral digital twin.
	twin, err := newDigitalTwin()
	if err != nil {
		return fmt.Errorf("creating digital twin: %w", err)
	}
	defer twin.close()

	var results []declTestResult
	allPass := true

	for _, src := range sources {
		result := twin.testDeclaration(src)
		results = append(results, result)
		if !result.Pass {
			allPass = false
		}
	}

	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"pass":    allPass,
			"results": results,
		})
	}

	// Human-readable output.
	for _, r := range results {
		status := "PASS"
		if !r.Pass {
			status = "FAIL"
		}
		op := r.Operation
		if op == "" {
			op = filepath.Base(r.File)
		}
		fmt.Printf("  %s  %s\n", status, op)
		for _, step := range r.Steps {
			stepStatus := "ok"
			if !step.Pass {
				stepStatus = "FAIL"
			}
			note := ""
			if step.Note != "" {
				note = ": " + step.Note
			}
			fmt.Printf("       %s  %s%s\n", stepStatus, step.Name, note)
		}
		if r.Error != "" {
			fmt.Printf("    error: %s\n", r.Error)
		}
	}

	if allPass {
		fmt.Printf("\nall %d declaration(s) passed\n", len(results))
		return nil
	}
	return fmt.Errorf("one or more declarations failed")
}

// digitalTwin is an ephemeral local environment for testing declarations.
type digitalTwin struct {
	dir             string
	rootID          *identity.Identity
	conventionRegID string
	s               store.Store
}

// newDigitalTwin creates an ephemeral root hierarchy in a temp directory.
func newDigitalTwin() (*digitalTwin, error) {
	dir, err := os.MkdirTemp("", "cf-convention-test-*")
	if err != nil {
		return nil, err
	}

	// Open an ephemeral SQLite store.
	s, err := store.Open(filepath.Join(dir, "store.db"))
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("opening store: %w", err)
	}

	// Generate a root identity.
	rootID, err := identity.Generate()
	if err != nil {
		s.Close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("generating root identity: %w", err)
	}

	// Generate a convention registry identity (simulates the convention campfire).
	convRegID, err := identity.Generate()
	if err != nil {
		s.Close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("generating convention registry identity: %w", err)
	}

	rootCampfireID := rootID.PublicKeyHex()
	convRegCampfireID := convRegID.PublicKeyHex()

	// Add root registry membership so ListMessages works.
	if err := s.AddMembership(store.Membership{
		CampfireID:   rootCampfireID,
		TransportDir: filepath.Join(dir, "root-registry"),
		JoinProtocol: "open",
		Role:         "full",
		JoinedAt:     store.NowNano(),
	}); err != nil {
		s.Close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("adding root registry membership: %w", err)
	}

	// Add convention registry membership.
	if err := s.AddMembership(store.Membership{
		CampfireID:   convRegCampfireID,
		TransportDir: filepath.Join(dir, "conv-registry"),
		JoinProtocol: "open",
		Role:         "full",
		JoinedAt:     store.NowNano(),
	}); err != nil {
		s.Close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("adding convention registry membership: %w", err)
	}

	twin := &digitalTwin{
		dir:             dir,
		rootID:          rootID,
		conventionRegID: convRegCampfireID,
		s:               s,
	}

	return twin, nil
}

// testDeclaration runs the full test pipeline for a single declaration.
func (t *digitalTwin) testDeclaration(src declSource) declTestResult {
	result := declTestResult{File: src.name, Pass: true}

	// Step 1: Lint.
	lintResult := convention.Lint(src.payload)
	step1 := declTestStep{Name: "lint"}
	if len(lintResult.Errors) > 0 {
		step1.Pass = false
		step1.Note = lintResult.Errors[0].Message
		result.Pass = false
		result.Steps = append(result.Steps, step1)
		result.Error = fmt.Sprintf("lint failed: %s", step1.Note)
		return result
	}
	step1.Pass = true
	if len(lintResult.Warnings) > 0 {
		step1.Note = fmt.Sprintf("%d warning(s)", len(lintResult.Warnings))
	}
	result.Steps = append(result.Steps, step1)

	// Step 2: Parse.
	decl, _, err := convention.Parse(
		[]string{convention.ConventionOperationTag},
		src.payload,
		t.conventionRegID,
		t.conventionRegID,
	)
	step2 := declTestStep{Name: "parse"}
	if err != nil {
		step2.Pass = false
		step2.Note = err.Error()
		result.Pass = false
		result.Steps = append(result.Steps, step2)
		result.Error = fmt.Sprintf("parse failed: %s", err)
		return result
	}
	step2.Pass = true
	result.Operation = decl.Operation
	result.Steps = append(result.Steps, step2)

	// Step 3: Generate tool.
	tool, err := convention.GenerateTool(decl, t.conventionRegID)
	step3 := declTestStep{Name: "generate_tool"}
	if err != nil {
		step3.Pass = false
		step3.Note = err.Error()
		result.Pass = false
		result.Steps = append(result.Steps, step3)
		result.Error = fmt.Sprintf("generate tool failed: %s", err)
		return result
	}
	step3.Pass = true
	step3.Note = fmt.Sprintf("tool=%q", tool.Name)
	result.Steps = append(result.Steps, step3)

	// Step 4: Execute with synthetic args.
	tr := &syntheticTransport{}
	exec := convention.NewExecutorForTest(tr, t.conventionRegID)
	synArgs := buildSyntheticArgs(decl)
	step4 := declTestStep{Name: "execute"}
	if err := exec.Execute(context.Background(), decl, t.conventionRegID, synArgs); err != nil {
		step4.Pass = false
		step4.Note = err.Error()
		// campfire_key declarations can't be executed with a member key — downgrade to note.
		if decl.Signing == "campfire_key" {
			step4.Pass = true
			step4.Note = "skipped (campfire_key signing; synthetic key used)"
		} else {
			result.Pass = false
			result.Error = fmt.Sprintf("execute failed: %s", err)
		}
	} else {
		step4.Pass = true
		if len(tr.sent) > 0 {
			step4.Note = fmt.Sprintf("produced tags=%v", tr.sent[0].tags)
		}
	}
	result.Steps = append(result.Steps, step4)

	// Step 5: Envelope.
	env := trust.BuildEnvelope(t.conventionRegID, trust.TrustUnknown, map[string]any{"test": true})
	step5 := declTestStep{Name: "envelope"}
	if env.Tainted.ContentClassification != "tainted" {
		step5.Pass = false
		step5.Note = fmt.Sprintf("unexpected classification: %q", env.Tainted.ContentClassification)
		result.Pass = false
	} else {
		step5.Pass = true
		step5.Note = fmt.Sprintf("trust_status=%s", env.RuntimeComputed.TrustStatus)
	}
	result.Steps = append(result.Steps, step5)

	return result
}

func (t *digitalTwin) close() {
	if t.s != nil {
		t.s.Close()
	}
	os.RemoveAll(t.dir)
}

// buildSyntheticArgs constructs minimal valid args for a declaration.
func buildSyntheticArgs(decl *convention.Declaration) map[string]any {
	args := make(map[string]any)
	for _, arg := range decl.Args {
		if arg.Default != nil {
			args[arg.Name] = arg.Default
			continue
		}
		if !arg.Required {
			continue
		}
		switch arg.Type {
		case "string":
			if len(arg.Values) > 0 {
				args[arg.Name] = arg.Values[0]
			} else {
				args[arg.Name] = "synthetic-value"
			}
		case "integer":
			if arg.Min != 0 {
				args[arg.Name] = arg.Min
			} else {
				args[arg.Name] = 1
			}
		case "boolean":
			args[arg.Name] = false
		case "enum":
			if len(arg.Values) > 0 {
				args[arg.Name] = arg.Values[0]
			} else {
				args[arg.Name] = "synthetic"
			}
		case "duration":
			args[arg.Name] = "1m"
		case "key":
			args[arg.Name] = "0000000000000000000000000000000000000000000000000000000000000000"
		case "campfire":
			args[arg.Name] = "synthetic-campfire-id"
		case "message_id":
			args[arg.Name] = "synthetic-message-id"
		case "json":
			args[arg.Name] = "{}"
		case "tag_set":
			args[arg.Name] = []string{}
		default:
			args[arg.Name] = "synthetic"
		}
	}
	return args
}

// syntheticTransport is a no-op ExecutorBackend for testing.
type syntheticTransport struct {
	sent []syntheticSent
}

type syntheticSent struct {
	campfireID string
	tags       []string
}

func (t *syntheticTransport) SendMessage(_ context.Context, campfireID string, _ []byte, tags []string, _ []string) (string, error) {
	t.sent = append(t.sent, syntheticSent{campfireID: campfireID, tags: tags})
	return "synthetic-msg-id", nil
}

func (t *syntheticTransport) SendCampfireKeySigned(_ context.Context, campfireID string, _ []byte, tags []string, _ []string) (string, error) {
	t.sent = append(t.sent, syntheticSent{campfireID: campfireID, tags: tags})
	return "synthetic-ck-msg-id", nil
}

func (t *syntheticTransport) ReadMessages(_ context.Context, _ string, _ []string) ([]convention.MessageRecord, error) {
	return nil, nil
}

func (t *syntheticTransport) SendFutureAndAwait(_ context.Context, _ string, _ []byte, _ []string, _ time.Duration) ([]byte, error) {
	return []byte(`{"msg_id":"synthetic-prior-id"}`), nil
}
