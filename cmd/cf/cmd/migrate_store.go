package cmd

// migrate_store.go — cf migrate-store subcommand.
//
// Migrates a campfire's messages/ directory from the v0.19.2 flat layout
// to the v0.31 YYYY-MM/DD/ bucketed layout per design §3 of
// docs/design/0.31-storage-scaling.md.
//
// Usage:
//   cf migrate-store <campfire-id> [--cf-home <dir>] [--dry-run] [--keep-backup] [--finalize]
//
// Algorithm is implemented in cf-protocol/internal/transport/fs/migrate_store.go.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
	"github.com/spf13/cobra"
)

var migrateStoreCmd = &cobra.Command{
	Use:   "migrate-store <campfire-id>",
	Short: "Migrate a campfire from flat (v0.19.2) to bucketed (v0.31) message layout",
	Long: `Migrate a campfire's messages/ directory from the legacy flat layout
(messages/<19-nanos>-<id>.cbor) to the v0.31 day-bucketed layout
(messages/<YYYY-MM>/<DD>/<19-nanos>-<id>.cbor).

The migration is safe to run against a live campfire: it acquires an
exclusive flock on .migrate.lock, which blocks concurrent WriteMessage
calls until the atomic swap completes.

Migration steps (design §3.3):
  1. Acquire LOCK_EX on .migrate.lock.
  2. Detect layout (directory-shape is authoritative; .layout-version is a hint).
  3. If already bucketed → exit 0 (idempotent).
  4. Recover from any prior crash state (see design §3.3 table).
  5. Copy all flat *.cbor files into messages.new/<YYYY-MM>/<DD>/ buckets.
  6. Verify: count check + byte-identical spot-check on min(64,count) random files.
  7. Atomic swap: rename messages → messages.old, rename messages.new → messages.
  8. Write messages/.layout-version hint.
  9. Release LOCK_EX.

messages.old/ is retained by default (audit trail). Run with --finalize to remove it.

Flags:
  --dry-run      Print the migration plan without making any changes.
  --keep-backup  Retained for future use; messages.old/ is always kept by default.
  --finalize     Remove messages.old/ (does not run migration).`,
	GroupID: groupAdvanced,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		keepBackup, _ := cmd.Flags().GetBool("keep-backup")
		finalize, _ := cmd.Flags().GetBool("finalize")

		campfireID := args[0]

		// Resolve transport base directory from --cf-home or environment.
		baseDir := fs.DefaultBaseDir()
		campfireDir := filepath.Join(baseDir, campfireID)

		// Verify the campfire directory exists (provide a useful error message).
		if _, err := os.Stat(campfireDir); err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("campfire directory not found: %s\n(Is --cf-home correct? Current base: %s)", campfireDir, baseDir)
			}
			return fmt.Errorf("checking campfire directory: %w", err)
		}

		opts := fs.MigrateStoreOptions{
			DryRun:     dryRun,
			KeepBackup: keepBackup,
			Finalize:   finalize,
			LogWriter:  os.Stdout,
		}

		err := fs.MigrateStore(campfireDir, opts)
		if err != nil {
			// Provide friendly error messages for known migration error types.
			var inconsistent *fs.MigrationInconsistentLayoutError
			var countMismatch *fs.MigrationCountMismatchError
			var byteMismatch *fs.MigrationByteMismatchError
			var corrupted *fs.MigrationCorruptedOtherStateError

			switch {
			case errors.As(err, &inconsistent):
				fmt.Fprintf(os.Stderr, "error: messages/ has mixed flat+bucketed layout — manual cleanup required\n")
				fmt.Fprintf(os.Stderr, "  flat files:    %v\n", inconsistent.FlatFiles)
				fmt.Fprintf(os.Stderr, "  bucketed dirs: %v\n", inconsistent.BucketedDirs)
				return err
			case errors.As(err, &countMismatch):
				fmt.Fprintf(os.Stderr, "error: verification failed — file count mismatch\n")
				fmt.Fprintf(os.Stderr, "  source: %d files, dest: %d files\n", countMismatch.SourceCount, countMismatch.DestCount)
				fmt.Fprintf(os.Stderr, "  messages.new/ left in place for inspection\n")
				return err
			case errors.As(err, &byteMismatch):
				fmt.Fprintf(os.Stderr, "error: verification failed — byte mismatch (possible filesystem corruption)\n")
				fmt.Fprintf(os.Stderr, "  file: %s\n", byteMismatch.LeafName)
				fmt.Fprintf(os.Stderr, "  source: %s\n", byteMismatch.SrcPath)
				fmt.Fprintf(os.Stderr, "  dest:   %s\n", byteMismatch.DstPath)
				fmt.Fprintf(os.Stderr, "  messages.new/ left in place for inspection\n")
				return err
			case errors.As(err, &corrupted):
				fmt.Fprintf(os.Stderr, "error: migration corrupted non-messages state — investigate immediately\n")
				fmt.Fprintf(os.Stderr, "  path: %s\n", corrupted.Path)
				fmt.Fprintf(os.Stderr, "  reason: %s\n", corrupted.Reason)
				return err
			default:
				return err
			}
		}
		return nil
	},
}

func init() {
	migrateStoreCmd.Flags().Bool("dry-run", false, "print migration plan without making any changes")
	migrateStoreCmd.Flags().Bool("keep-backup", false, "retained for CLI symmetry; messages.old/ is always kept by default")
	migrateStoreCmd.Flags().Bool("finalize", false, "remove messages.old/ (does not run migration)")
	rootCmd.AddCommand(migrateStoreCmd)
}
