package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var useClear bool

var useCmd = &cobra.Command{
	Use:   "use [name]",
	Short: "Set or show the current campfire context",
	Long: `Set the current campfire context for subsequent commands.

  cf use myproject        set context (resolves name to campfire ID)
  cf use                  show current context
  cf use --clear          remove current context

Non-destructive commands (send, read, compact, dag, members, await, view, serve)
use the context when no explicit campfire argument is given.
Destructive commands (disband, evict) always require an explicit argument.

The $CF_CONTEXT environment variable overrides the context file.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if useClear {
			return clearContext()
		}
		if len(args) == 0 {
			return showContext()
		}
		return setContext(args[0])
	},
}

func init() {
	useCmd.Flags().BoolVar(&useClear, "clear", false, "remove the current context")
	rootCmd.AddCommand(useCmd)
}

// contextFilePath returns the path to the context file: $CF_HOME/current.
func contextFilePath() string {
	return filepath.Join(CFHome(), "current")
}

// setContext resolves name to a campfire ID and writes it to $CF_HOME/current.
func setContext(name string) error {
	s, err := openStore()
	if err != nil {
		return err
	}
	defer s.Close()

	campfireID, err := resolveCampfireID(name, s)
	if err != nil {
		return fmt.Errorf("resolving %q: %w", name, err)
	}

	if err := os.MkdirAll(filepath.Dir(contextFilePath()), 0700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	if err := os.WriteFile(contextFilePath(), []byte(campfireID+"\n"), 0600); err != nil {
		return fmt.Errorf("writing context file: %w", err)
	}
	fmt.Printf("Context set to %s\n", campfireID[:12])
	return nil
}

// clearContext removes the context file.
func clearContext() error {
	if err := os.Remove(contextFilePath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing context file: %w", err)
	}
	fmt.Println("Context cleared")
	return nil
}

// showContext prints the current context.
func showContext() error {
	if env := os.Getenv("CF_CONTEXT"); env != "" {
		fmt.Printf("%s (from $CF_CONTEXT)\n", strings.TrimSpace(env))
		return nil
	}
	data, err := os.ReadFile(contextFilePath())
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No context set")
			return nil
		}
		return fmt.Errorf("reading context file: %w", err)
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		fmt.Println("No context set")
		return nil
	}
	fmt.Println(id)
	return nil
}

// requireImplicitCampfire resolves the implicit campfire from context
// and returns an error if no context is set.
func requireImplicitCampfire() (string, error) {
	id, err := resolveImplicitCampfire()
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("campfire ID required: no context set (use 'cf use <name>' or set $CF_CONTEXT)")
	}
	return id, nil
}

// resolveImplicitCampfire returns the implicit campfire ID from context.
// Checks: $CF_CONTEXT env var → $CF_HOME/current file.
// Returns ("", nil) if no context is set. Returns (id, nil) on success.
func resolveImplicitCampfire() (string, error) {
	if env := os.Getenv("CF_CONTEXT"); env != "" {
		id := strings.TrimSpace(env)
		if id != "" {
			return id, nil
		}
	}
	data, err := os.ReadFile(contextFilePath())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading context file: %w", err)
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return "", nil
	}
	return id, nil
}
