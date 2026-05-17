package cmd

// migrate_store.go — cf migrate-store subcommand.
//
// Migrates a campfire's messages/ directory from the v0.19.2 flat layout
// to the v0.31 YYYY-MM/DD/ bucketed layout per design §3 of
// docs/design/0.31-storage-scaling.md.
//
// Usage:
//   cf migrate-store <campfire-id> [--cf-home <dir>] [--dry-run] [--keep-backup] [--finalize] [--force]
//
// Algorithm is implemented in cf-protocol/internal/transport/fs/migrate_store.go.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
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
calls until the atomic swap completes. On Windows, migration lock is a no-op;
see docs/design/0.31-storage-scaling.md §3.2 for details on degraded-mode
concurrent writes.

Migration steps (design §3.3):
  1. Acquire LOCK_EX on .migrate.lock (no-op on Windows).
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
  --finalize     Remove messages.old/ (does not run migration).
  --force        Bypass membership/role check. Use only for disaster recovery when
                 the local store does not contain a membership record for this campfire
                 (e.g. restoring from backup on a fresh machine). Without --force,
                 the command requires "full" membership role (same as cf compact).`,
	GroupID: groupAdvanced,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// WINDOWS MIGRATION LOCK DEGRADATION (campfireagent-22d)
		// On Windows, the migration lock is a no-op (see lock_windows.go).
		// Concurrent writes during migration may corrupt the store.
		if runtime.GOOS == "windows" {
			fmt.Fprintln(os.Stderr, "WARNING: cf migrate-store on Windows. Migration lock is a no-op on this platform.")
			fmt.Fprintln(os.Stderr, "Stop all cf processes that may write to this campfire before continuing. Concurrent")
			fmt.Fprintln(os.Stderr, "writes during migration may corrupt the store. See docs/install.md for details.")
		}

		dryRun, _ := cmd.Flags().GetBool("dry-run")
		keepBackup, _ := cmd.Flags().GetBool("keep-backup")
		finalize, _ := cmd.Flags().GetBool("finalize")
		force, _ := cmd.Flags().GetBool("force")

		campfireID := args[0]

		// SECURITY (campfireagent-8b2): validate the CLI argument before joining
		// it into a filesystem path. Without this check, `cf migrate-store ../../foo`
		// escapes baseDir via filepath.Join cleaning. isValidCampfireID enforces
		// the exact campfire-ID shape (64 lowercase-hex chars decoding to 32 bytes).
		if !isValidCampfireID(campfireID) {
			return fmt.Errorf("invalid campfire ID %q: must be 64 hex characters (Ed25519 public key)", campfireID)
		}

		// SECURITY (campfireagent-6a9): defense-in-depth membership check.
		// Require "full" role membership before allowing any store mutation.
		// Mirror the pattern used by `cf compact` (checkRoleCanSend).
		// --force bypasses this check for disaster-recovery scenarios where the
		// local store does not yet contain the membership record (e.g. restoring
		// from backup on a fresh machine).
		if !force {
			_, s, err := requireAgentAndStore()
			if err != nil {
				return fmt.Errorf("loading identity/store for membership check: %w", err)
			}
			defer s.Close()

			m, err := s.GetMembership(campfireID)
			if err != nil {
				return fmt.Errorf("querying membership: %w", err)
			}
			if m == nil {
				return fmt.Errorf("not a member of campfire %s — refusing to migrate (use --force to override for disaster recovery)", campfireID[:min(12, len(campfireID))])
			}
			if err := checkRoleCanSend(m.Role, []string{campfire.TagCompact}); err != nil {
				// Reuse TagCompact as the sentinel for "full role required" — same
				// semantics as compact: only full members may mutate campfire state.
				return fmt.Errorf("migrate-store requires full membership role: %w", err)
			}
		}

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
	migrateStoreCmd.Flags().Bool("force", false, "bypass membership check (disaster recovery: use when local store lacks membership record)")
	rootCmd.AddCommand(migrateStoreCmd)
}
