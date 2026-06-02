package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/campfire-net/campfire/cf-conventions/cf-convention"
	"github.com/spf13/cobra"
)

var conventionLintCmd = &cobra.Command{
	Use:   "lint <file|->",
	Short: "Validate a convention declaration payload",
	Long: `Validate a convention:operation declaration payload (JSON).

Reads from a file path or stdin ("-"). Runs all 11 conformance checks plus
arg-to-tag mapping and enum alignment checks.

Exit codes:
  0  valid (no errors, no warnings)
  1  errors found
  2  warnings only (no errors)`,
	Args: cobra.ExactArgs(1),
	RunE: runConventionLint,
}

func init() {
	conventionCmd.AddCommand(conventionLintCmd)
}

func runConventionLint(_ *cobra.Command, args []string) error {
	payload, err := readDeclarationInput(args[0])
	if err != nil {
		return err
	}

	result := convention.Lint(payload)

	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			return err
		}
	} else {
		printLintResult(result)
	}

	// Exit codes: 1 = errors, 2 = warnings only, 0 = clean.
	if len(result.Errors) > 0 {
		os.Exit(1)
	}
	if len(result.Warnings) > 0 {
		os.Exit(2)
	}
	return nil
}

// printLintResult prints a human-readable lint report to stdout.
func printLintResult(result *convention.LintResult) {
	for _, f := range result.Errors {
		loc := ""
		if f.Field != "" {
			loc = " [" + f.Field + "]"
		}
		fmt.Fprintf(os.Stdout, "error%s: %s\n", loc, f.Message)
	}
	for _, f := range result.Warnings {
		loc := ""
		if f.Field != "" {
			loc = " [" + f.Field + "]"
		}
		fmt.Fprintf(os.Stdout, "warning%s: %s\n", loc, f.Message)
	}
	if result.Valid && len(result.Warnings) == 0 {
		fmt.Fprintln(os.Stdout, "ok: declaration is valid")
	}
}

// readDeclarationInput reads declaration payload from a file path or stdin.
func readDeclarationInput(src string) ([]byte, error) {
	if src == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(src)
}

// readDeclarationsFromPath reads one or more declaration payloads from a file or directory.
// Returns a slice of (filename, payload) pairs.
func readDeclarationsFromPath(src string) ([]declSource, error) {
	info, err := os.Stat(src)
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", src, err)
	}
	if !info.IsDir() {
		data, err := os.ReadFile(src)
		if err != nil {
			return nil, err
		}
		return expandMultiOp(src, data)
	}
	// Directory: collect all .json files.
	entries, err := os.ReadDir(src)
	if err != nil {
		return nil, fmt.Errorf("reading directory %q: %w", src, err)
	}
	var out []declSource
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(src, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %q: %w", path, err)
		}
		expanded, err := expandMultiOp(path, data)
		if err != nil {
			return nil, err
		}
		out = append(out, expanded...)
	}
	return out, nil
}

// expandMultiOp turns one declaration file into one or more single-op declSources.
//
// A single-op file (a flat object carrying a top-level "operation") is returned
// unchanged. A multi-op file — the authoring shape {convention, version,
// operations:[{operation, ...}, ...]} used in the declaration source tree — is
// expanded into one declSource per entry in "operations". The file-level
// "convention" and "version" are injected into each op only when the op omits
// them (ops may override, e.g. a higher per-op version), so downstream
// convention.Parse/Lint and the conv:op@version duplicate detection see flat
// single-op declarations exactly as before (campfire-aa5).
//
// Detection is conservative: a file is treated as multi-op only when it has a
// non-empty "operations" array AND no top-level "operation" — so existing
// single-op files (which have no "operations" key) are never misread.
func expandMultiOp(name string, data []byte) ([]declSource, error) {
	var probe struct {
		Operation  string            `json:"operation"`
		Convention string            `json:"convention"`
		Version    string            `json:"version"`
		Operations []json.RawMessage `json:"operations"`
	}
	// A parse failure here is not fatal: leave it to convention.Lint/Parse to
	// produce the canonical error for this source.
	if err := json.Unmarshal(data, &probe); err != nil || probe.Operation != "" || len(probe.Operations) == 0 {
		return []declSource{{name: name, payload: data}}, nil
	}

	out := make([]declSource, 0, len(probe.Operations))
	for i, opRaw := range probe.Operations {
		merged, err := injectConventionVersion(opRaw, probe.Convention, probe.Version)
		if err != nil {
			return nil, fmt.Errorf("%s: operation[%d]: %w", name, i, err)
		}
		out = append(out, declSource{name: fmt.Sprintf("%s#operations[%d]", name, i), payload: merged})
	}
	return out, nil
}

// injectConventionVersion sets "convention"/"version" on a single-op JSON object
// from the file-level values, but only for keys the op does not already define.
func injectConventionVersion(opRaw json.RawMessage, convention, version string) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(opRaw, &fields); err != nil {
		return nil, fmt.Errorf("decoding operation object: %w", err)
	}
	if _, ok := fields["convention"]; !ok && convention != "" {
		fields["convention"], _ = json.Marshal(convention)
	}
	if _, ok := fields["version"]; !ok && version != "" {
		fields["version"], _ = json.Marshal(version)
	}
	return json.Marshal(fields)
}

// declSource is a named declaration payload.
type declSource struct {
	name    string
	payload []byte
}
