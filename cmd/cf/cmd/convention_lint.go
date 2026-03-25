package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/campfire-net/campfire/pkg/convention"
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
		return []declSource{{name: src, payload: data}}, nil
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
		out = append(out, declSource{name: path, payload: data})
	}
	return out, nil
}

// declSource is a named declaration payload.
type declSource struct {
	name    string
	payload []byte
}
