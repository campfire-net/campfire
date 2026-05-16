package fs

// migrate_store.go — MigrateStore implements the v0.19.2 flat → v0.31 bucketed
// migration per design §3 of docs/design/0.31-storage-scaling.md.
//
// Algorithm (§3.3):
//   1. Acquire LOCK_EX on <campfire-dir>/.migrate.lock.
//   2. Detect layout (§3.5) — directory-shape is authoritative.
//   3. If already bucketed → release lock → return nil (idempotent).
//   4. Handle mid-swap crash recovery states (§3.3 table).
//   5. Create messages.new/ (0700).
//   6. For each *.cbor in messages/: parse 19-nanos prefix → bucket → MkdirAll → copy bytes.
//   7. Verification (§3.6): count check + spot-check min(64, count) random files.
//   8. rename(messages → messages.old), rename(messages.new → messages).
//   9. Write messages/.layout-version hint.
//  10. Release LOCK_EX.
//
// Error types: MigrationInconsistentLayoutError, MigrationCountMismatchError,
// MigrationByteMismatchError, MigrationCorruptedOtherStateError.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// isInvalidArg reports whether err wraps a syscall EINVAL — used to treat
// directory fsync as best-effort on filesystems that reject it.
func isInvalidArg(err error) bool {
	return errors.Is(err, syscall.EINVAL)
}

// MigrationInconsistentLayoutError is returned when messages/ contains a mix
// of flat *.cbor files and bucketed YYYY-MM/ directories (corrupt mid-state).
type MigrationInconsistentLayoutError struct {
	FlatFiles      []string // flat *.cbor entries found
	BucketedDirs   []string // YYYY-MM/ dirs found
}

func (e *MigrationInconsistentLayoutError) Error() string {
	return fmt.Sprintf("messages/ has mixed flat+bucketed layout: flat=%v, bucketed=%v", e.FlatFiles, e.BucketedDirs)
}

// MigrationCountMismatchError is returned when the file count in messages.new/
// does not match the file count in messages/ after the copy step.
type MigrationCountMismatchError struct {
	SourceCount int
	DestCount   int
}

func (e *MigrationCountMismatchError) Error() string {
	return fmt.Sprintf("migration verification failed: source has %d files, dest has %d files", e.SourceCount, e.DestCount)
}

// MigrationByteMismatchError is returned when a spot-checked file in messages.new/
// has different bytes than its counterpart in messages/.
type MigrationByteMismatchError struct {
	LeafName string
	SrcPath  string
	DstPath  string
}

func (e *MigrationByteMismatchError) Error() string {
	return fmt.Sprintf("migration verification failed: byte mismatch on %s (src=%s dst=%s)", e.LeafName, e.SrcPath, e.DstPath)
}

// MigrationCorruptedOtherStateError is returned when the stat of members/,
// push-subscribers/, campfire.cbor, or .cf/ changes between pre- and post-migration.
type MigrationCorruptedOtherStateError struct {
	Path   string
	Reason string
}

func (e *MigrationCorruptedOtherStateError) Error() string {
	return fmt.Sprintf("migration corrupted other state at %s: %s", e.Path, e.Reason)
}

// layoutVersionContent is written to messages/.layout-version as a hint marker.
const layoutVersionContent = "v0.31-bucketed-utc-day\n"

// layoutVersionFile is the name of the layout hint marker file.
const layoutVersionFile = ".layout-version"

// MigrateStoreOptions configures the MigrateStore operation.
type MigrateStoreOptions struct {
	// DryRun: print plan, make no writes.
	DryRun bool

	// KeepBackup: not used for behaviour (messages.old/ is always kept by default);
	// retained for CLI flag symmetry. The operator must explicitly --finalize to remove.
	KeepBackup bool

	// Finalize: remove messages.old/ only (skip migration if no messages.old/ exists).
	Finalize bool

	// LogWriter is where progress messages are written. Defaults to os.Stdout.
	LogWriter io.Writer

	// failpointBeforeSecondRename, if non-nil, is called between the two renames
	// (after messages→messages.old, before messages.new→messages). Used by tests
	// to simulate a crash in the swap window.
	failpointBeforeSecondRename func()

	// failpointBeforeVerify, if non-nil, is called after the copy step completes
	// and before the verification step. Tests use this to tamper with messages.new/.
	failpointBeforeVerify func()

	// rng, if non-nil, is used for spot-check sampling. Tests inject a seeded source.
	rng *rand.Rand
}

// MigrateStore migrates campfireDir from the v0.19.2 flat messages/ layout to the
// v0.31 YYYY-MM/DD/ bucketed layout. campfireDir is the campfire's root directory
// (e.g. /home/baron/.cf/campfires/<id>/).
//
// It is safe to call MigrateStore concurrently with WriteMessage — migration holds
// LOCK_EX on .migrate.lock, which blocks WriteMessage's LOCK_SH acquisition.
// Concurrent ListMessages calls continue without locking (dual-read handles mid-migration).
func MigrateStore(campfireDir string, opts MigrateStoreOptions) error {
	logf := func(format string, a ...any) {
		w := opts.LogWriter
		if w == nil {
			w = os.Stdout
		}
		fmt.Fprintf(w, format+"\n", a...)
	}

	if opts.Finalize {
		return finalize(campfireDir, opts.DryRun, logf)
	}

	// §3.7 defense-in-depth: snapshot stat of non-messages siblings before any writes.
	preStats, err := snapshotOtherState(campfireDir)
	if err != nil {
		return fmt.Errorf("snapshotting other state: %w", err)
	}

	if opts.DryRun {
		return dryRun(campfireDir, logf)
	}

	// Step 1: Acquire LOCK_EX on .migrate.lock.
	lockPath := migrateLockPath(campfireDir)
	release, err := acquireMigrateLockExclusive(lockPath)
	if err != nil {
		return fmt.Errorf("acquiring migration lock: %w", err)
	}
	defer release()

	// Handle mid-swap crash recovery states (§3.3 table).
	if err := recoverCrashState(campfireDir, logf); err != nil {
		return fmt.Errorf("recovering from crash state: %w", err)
	}

	messagesDir := filepath.Join(campfireDir, "messages")
	messagesNew := filepath.Join(campfireDir, "messages.new")

	// Step 2: Detect layout.
	layout, flatFiles, err := detectLayout(messagesDir)
	if err != nil {
		return err
	}

	switch layout {
	case layoutAbsent:
		// No messages directory: nothing to migrate.
		logf("messages/ does not exist — nothing to migrate")
		return nil
	case layoutBucketed:
		// Already migrated: idempotent exit.
		logf("messages/ is already in bucketed layout — nothing to do")
		return nil
	case layoutInconsistent:
		// Mixed state — should not occur if recovery rules are followed.
		bucketDirs, _ := listBucketDirs(messagesDir)
		return &MigrationInconsistentLayoutError{
			FlatFiles:    flatFiles,
			BucketedDirs: bucketDirs,
		}
	case layoutFlat:
		// Fall through: proceed with migration.
	}

	logf("migrating %d flat messages in %s", len(flatFiles), messagesDir)

	// Step 3: Create messages.new/ (0700).
	if err := os.MkdirAll(messagesNew, 0700); err != nil {
		return fmt.Errorf("creating messages.new: %w", err)
	}

	// Step 4/5/6: For each *.cbor in messages/, parse 19-nanos prefix → bucket → copy.
	for _, name := range flatFiles {
		nanos, err := parseNanosPrefix(name)
		if err != nil {
			return fmt.Errorf("parsing timestamp from filename %q: %w", name, err)
		}
		t := time.Unix(0, nanos).UTC()
		yearMonth, day := bucketFor(t)

		bucketDir := filepath.Join(messagesNew, yearMonth, day)
		if err := os.MkdirAll(bucketDir, 0700); err != nil {
			return fmt.Errorf("creating bucket dir %s: %w", bucketDir, err)
		}

		src := filepath.Join(messagesDir, name)
		dst := filepath.Join(bucketDir, name)
		if err := copyFileBytes(src, dst); err != nil {
			return fmt.Errorf("copying %s to %s: %w", src, dst, err)
		}
	}

	// DURABILITY (campfireagent-526): fsync the messages.new tree before
	// verification. copyFileBytes fsyncs each file; this fsyncs every bucket
	// directory plus the top-level messages.new so directory-entry updates
	// (file creation) reach disk before we proceed.
	if err := filepath.Walk(messagesNew, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return fsyncDir(path)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("fsyncing messages.new tree: %w", err)
	}

	// Failpoint: called after copy, before verify (used by M5/M6 tests to tamper with messages.new/).
	if opts.failpointBeforeVerify != nil {
		opts.failpointBeforeVerify()
	}

	// Step 6b: Verification (§3.6).
	if err := verify(messagesDir, messagesNew, flatFiles, opts.rng); err != nil {
		return err
	}

	// Step 7: rename(messages → messages.old), then (optionally inject failpoint), rename(messages.new → messages).
	messagesOld := filepath.Join(campfireDir, "messages.old")
	if err := os.Rename(messagesDir, messagesOld); err != nil {
		return fmt.Errorf("renaming messages → messages.old: %w", err)
	}

	// Failpoint: between the two renames (M3 crash simulation).
	if opts.failpointBeforeSecondRename != nil {
		opts.failpointBeforeSecondRename()
	}

	if err := os.Rename(messagesNew, messagesDir); err != nil {
		return fmt.Errorf("renaming messages.new → messages: %w", err)
	}

	// DURABILITY (campfireagent-526): fsync the campfire directory so the two
	// renames (messages → messages.old, messages.new → messages) are durable.
	// Without this, the new directory entries can live only in the page cache
	// and a power loss leaves an inconsistent layout on recovery.
	if err := fsyncDir(campfireDir); err != nil {
		return fmt.Errorf("fsyncing campfire dir after swap: %w", err)
	}

	// Step 8: Write layout-version hint.
	if err := os.WriteFile(filepath.Join(messagesDir, layoutVersionFile), []byte(layoutVersionContent), 0600); err != nil {
		// Non-fatal: hint file failure is not a migration failure.
		logf("warning: writing .layout-version: %v", err)
	}

	// §3.7 defense-in-depth: verify other state is stat-identical post-migration.
	if err := assertOtherStateUnchanged(campfireDir, preStats); err != nil {
		return err
	}

	logf("migration complete: %d messages migrated; messages.old/ retained (run --finalize to remove)", len(flatFiles))
	return nil
}

// layout describes the detected layout of messages/.
type layout int

const (
	layoutAbsent       layout = iota // messages/ does not exist
	layoutFlat                       // all *.cbor files at top level
	layoutBucketed                   // all YYYY-MM/DD/ bucketed dirs
	layoutInconsistent               // mixed flat+bucketed
)

// detectLayout examines messagesDir and returns the layout, plus the list of flat *.cbor names.
func detectLayout(messagesDir string) (layout, []string, error) {
	entries, err := os.ReadDir(messagesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return layoutAbsent, nil, nil
		}
		return 0, nil, fmt.Errorf("reading messages dir: %w", err)
	}

	// Filter out the layout hint file; it's neither flat nor bucketed.
	var flatFiles []string
	hasBuckets := false

	for _, e := range entries {
		name := e.Name()
		if name == layoutVersionFile {
			continue // hint file — not flat, not bucket
		}
		if !e.IsDir() && strings.HasSuffix(name, ".cbor") {
			flatFiles = append(flatFiles, name)
			continue
		}
		if e.IsDir() && yearMonthRE.MatchString(name) {
			hasBuckets = true
			continue
		}
		// Silently ignore unknown entries (forward-compat, A7).
	}

	hasFlat := len(flatFiles) > 0

	switch {
	case !hasFlat && !hasBuckets:
		// Empty messages dir — treat as bucketed (already migrated or new).
		return layoutBucketed, nil, nil
	case hasFlat && hasBuckets:
		return layoutInconsistent, flatFiles, nil
	case hasBuckets:
		return layoutBucketed, nil, nil
	default:
		sort.Strings(flatFiles)
		return layoutFlat, flatFiles, nil
	}
}

// listBucketDirs returns the YYYY-MM directory names in messagesDir.
func listBucketDirs(messagesDir string) ([]string, error) {
	entries, err := os.ReadDir(messagesDir)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && yearMonthRE.MatchString(e.Name()) {
			dirs = append(dirs, e.Name())
		}
	}
	return dirs, nil
}

// parseNanosPrefix extracts the 19-digit nanosecond timestamp from a leaf filename
// of the form "<19-nanos>-<id>.cbor".
func parseNanosPrefix(name string) (int64, error) {
	// Filename format: 0000000001234567890-<uuid>.cbor (19-digit nanos prefix).
	if len(name) < 20 {
		return 0, fmt.Errorf("filename too short: %q", name)
	}
	if name[19] != '-' {
		return 0, fmt.Errorf("expected '-' at position 19 in filename %q", name)
	}
	nanos, err := strconv.ParseInt(name[:19], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing nanos prefix in %q: %w", name, err)
	}
	return nanos, nil
}

// copyFileBytes copies src to dst byte-for-byte. Creates dst's parent directories.
// Distinct from copyFile (which handles inbox dedup): this copy is idempotent via
// O_CREATE|O_WRONLY|O_TRUNC (always overwrites partial writes).
//
// DURABILITY (campfireagent-526): fsyncs the destination file before close.
// Without the explicit Sync, the kernel may buffer the write in the page cache
// and a power loss between copy and the atomic rename will pass verify()
// (which reads through the page cache) but leave a torn or zero-length file
// on disk after recovery. Parent-directory fsyncs are issued separately by the
// migration driver after all copies complete and after the atomic-swap renames.
func copyFileBytes(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening source: %w", err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("creating destination: %w", err)
	}

	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return fmt.Errorf("copying bytes: %w", err)
	}
	// fsync before close: forces bytes from the page cache to the storage device
	// so the data survives power loss. This is the load-bearing durability call.
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(dst)
		return fmt.Errorf("fsyncing destination: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return fmt.Errorf("closing destination: %w", err)
	}
	return nil
}

// fsyncDir fsyncs the given directory. On Linux this forces directory-entry
// updates (file creation, renames) to the storage device, so a subsequent
// power loss cannot leave the directory listing in an inconsistent state.
//
// On non-Linux platforms where directory fsync is a no-op or returns EINVAL
// (notably some BSDs and Windows), the call is best-effort: we attempt the
// sync, log nothing, and swallow EINVAL-style errors. Any genuine I/O error
// is still surfaced to the caller.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("opening directory for fsync: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		// Some filesystems return EINVAL on directory fsync — treat as
		// best-effort on those platforms. Other errors surface up.
		if isInvalidArg(err) {
			return nil
		}
		return fmt.Errorf("fsyncing directory %s: %w", dir, err)
	}
	return nil
}

// verify checks messages.new/ against messages/ (§3.6):
// 1. Count check: same number of files.
// 2. Spot-check: min(64, count) random files are byte-identical.
func verify(messagesDir, messagesNew string, flatFiles []string, rng *rand.Rand) error {
	// Count files in messages.new/ recursively.
	var newFiles []string
	err := filepath.Walk(messagesNew, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".cbor") {
			newFiles = append(newFiles, path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walking messages.new: %w", err)
	}

	if len(flatFiles) != len(newFiles) {
		return &MigrationCountMismatchError{
			SourceCount: len(flatFiles),
			DestCount:   len(newFiles),
		}
	}

	// Spot-check: sample min(64, count) random files from flatFiles.
	count := len(flatFiles)
	sampleSize := count
	if sampleSize > 64 {
		sampleSize = 64
	}

	// Build sample indices.
	indices := make([]int, count)
	for i := range indices {
		indices[i] = i
	}
	if rng != nil {
		rand.Shuffle(len(indices), func(i, j int) { indices[i], indices[j] = indices[j], indices[i] })
	} else {
		// Use a fresh random source.
		r := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0)) //nolint:gosec
		r.Shuffle(len(indices), func(i, j int) { indices[i], indices[j] = indices[j], indices[i] })
	}
	sample := indices[:sampleSize]

	for _, idx := range sample {
		name := flatFiles[idx]
		srcPath := filepath.Join(messagesDir, name)

		// Find destination path: parse nanos to derive bucket.
		nanos, err := parseNanosPrefix(name)
		if err != nil {
			return fmt.Errorf("verification: parsing nanos from %q: %w", name, err)
		}
		t := time.Unix(0, nanos).UTC()
		yearMonth, day := bucketFor(t)
		dstPath := filepath.Join(messagesNew, yearMonth, day, name)

		srcBytes, err := os.ReadFile(srcPath)
		if err != nil {
			return fmt.Errorf("verification: reading source %s: %w", srcPath, err)
		}
		dstBytes, err := os.ReadFile(dstPath)
		if err != nil {
			return fmt.Errorf("verification: reading dest %s: %w", dstPath, err)
		}
		if !bytes.Equal(srcBytes, dstBytes) {
			return &MigrationByteMismatchError{
				LeafName: name,
				SrcPath:  srcPath,
				DstPath:  dstPath,
			}
		}
	}
	return nil
}

// recoverCrashState handles the 5-state recovery table from §3.3.
// Called while LOCK_EX is held.
func recoverCrashState(campfireDir string, logf func(string, ...any)) error {
	messagesDir := filepath.Join(campfireDir, "messages")
	messagesNew := filepath.Join(campfireDir, "messages.new")
	messagesOld := filepath.Join(campfireDir, "messages.old")

	hasMsgs := exists(messagesDir)
	hasMsgsNew := exists(messagesNew)
	hasMsgsOld := exists(messagesOld)

	switch {
	case hasMsgs && !hasMsgsNew && !hasMsgsOld:
		// State 1: normal pre-migration or post-finalize. Nothing to recover.
		return nil

	case hasMsgs && hasMsgsNew && !hasMsgsOld:
		// State 2: crash between step 5 (copy) and step 7 (first rename).
		// messages/ is untouched. Remove messages.new/ and let migration re-run.
		logf("recovery: found messages.new/ with messages/ intact — removing partial messages.new/")
		if err := os.RemoveAll(messagesNew); err != nil {
			return fmt.Errorf("removing partial messages.new: %w", err)
		}
		return nil

	case hasMsgs && !hasMsgsNew && hasMsgsOld:
		// State 3: post-swap (step 8 done) or crash between step 8 and finalize.
		// Inspect messages/: if bucketed → complete, operator can --finalize.
		// If flat → step 8 didn't happen cleanly; recover by re-pointing.
		layout, _, err := detectLayout(messagesDir)
		if err != nil {
			return err
		}
		if layout == layoutBucketed {
			// Migration complete; messages.old/ is just awaiting --finalize.
			logf("recovery: messages/ is bucketed, messages.old/ retained — already migrated")
			return nil
		}
		// messages/ is flat and messages.old/ exists — abnormal. The flat layout
		// is authoritative. Remove messages.old/ and re-run migration.
		logf("recovery: messages/ is flat but messages.old/ exists — removing messages.old/ and re-running")
		if err := os.RemoveAll(messagesOld); err != nil {
			return fmt.Errorf("removing stale messages.old: %w", err)
		}
		return nil

	case !hasMsgs && !hasMsgsNew && hasMsgsOld:
		// State 4: crash during step 7 (first rename — messages→messages.old done,
		// messages.new→messages not done). Restore messages/ from messages.old/.
		logf("recovery: only messages.old/ present — restoring messages/ from messages.old/")
		if err := os.Rename(messagesOld, messagesDir); err != nil {
			return fmt.Errorf("restoring messages from messages.old: %w", err)
		}
		return nil

	case !hasMsgs && hasMsgsNew && !hasMsgsOld:
		// State 5: crash during step 8 (messages.new→messages not done).
		// Rename messages.new/ → messages/ to restore.
		logf("recovery: only messages.new/ present — restoring messages/ from messages.new/")
		if err := os.Rename(messagesNew, messagesDir); err != nil {
			return fmt.Errorf("restoring messages from messages.new: %w", err)
		}
		return nil

	default:
		// Unrecognised combination (e.g. all three exist). Log and let
		// detectLayout figure out what to do.
		return nil
	}
}

// exists reports whether path exists (via Stat).
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// dryRun prints the migration plan without making any writes.
func dryRun(campfireDir string, logf func(string, ...any)) error {
	messagesDir := filepath.Join(campfireDir, "messages")
	layout, flatFiles, err := detectLayout(messagesDir)
	if err != nil {
		return err
	}
	switch layout {
	case layoutAbsent:
		logf("dry-run: messages/ does not exist — nothing to migrate")
		return nil
	case layoutBucketed:
		logf("dry-run: messages/ is already bucketed — nothing to do")
		return nil
	case layoutInconsistent:
		logf("dry-run: messages/ has inconsistent layout — migration would fail")
		return nil
	}

	// Count buckets that would be created.
	bucketSet := map[string]struct{}{}
	for _, name := range flatFiles {
		nanos, err := parseNanosPrefix(name)
		if err != nil {
			continue
		}
		t := time.Unix(0, nanos).UTC()
		ym, d := bucketFor(t)
		bucketSet[ym+"/"+d] = struct{}{}
	}

	logf("dry-run: would migrate %d messages into %d day buckets", len(flatFiles), len(bucketSet))
	return nil
}

// finalize removes messages.old/ if it exists.
func finalize(campfireDir string, dryRun bool, logf func(string, ...any)) error {
	old := filepath.Join(campfireDir, "messages.old")
	if !exists(old) {
		logf("--finalize: messages.old/ does not exist — nothing to do")
		return nil
	}
	if dryRun {
		logf("dry-run --finalize: would remove messages.old/")
		return nil
	}
	logf("--finalize: removing messages.old/")
	return os.RemoveAll(old)
}

// otherStatEntry records the stat of a sibling path for pre/post comparison.
type otherStatEntry struct {
	path    string
	modTime time.Time
	size    int64
	exists  bool
	inode   uint64
}

// nonMessageSiblings are the names under campfireDir that migration must not touch.
var nonMessageSiblings = []string{"members", "push-subscribers", "campfire.cbor", ".cf"}

// snapshotOtherState records stat info for each non-messages sibling.
func snapshotOtherState(campfireDir string) ([]otherStatEntry, error) {
	var entries []otherStatEntry
	for _, name := range nonMessageSiblings {
		path := filepath.Join(campfireDir, name)
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				entries = append(entries, otherStatEntry{path: path, exists: false})
				continue
			}
			return nil, fmt.Errorf("statting %s: %w", path, err)
		}
		entries = append(entries, otherStatEntry{
			path:    path,
			modTime: info.ModTime(),
			size:    info.Size(),
			exists:  true,
			inode:   inodeOf(info),
		})
	}
	return entries, nil
}

// assertOtherStateUnchanged verifies that the pre-migration snapshot still matches
// the current on-disk state.
func assertOtherStateUnchanged(campfireDir string, pre []otherStatEntry) error {
	for _, pre := range pre {
		info, err := os.Stat(pre.path)
		if err != nil {
			if os.IsNotExist(err) {
				if !pre.exists {
					continue // Still absent — OK.
				}
				return &MigrationCorruptedOtherStateError{
					Path:   pre.path,
					Reason: "existed before migration but is now absent",
				}
			}
			return &MigrationCorruptedOtherStateError{
				Path:   pre.path,
				Reason: fmt.Sprintf("stat error post-migration: %v", err),
			}
		}
		if !pre.exists {
			return &MigrationCorruptedOtherStateError{
				Path:   pre.path,
				Reason: "did not exist before migration but now does",
			}
		}
		if info.Size() != pre.size {
			return &MigrationCorruptedOtherStateError{
				Path:   pre.path,
				Reason: fmt.Sprintf("size changed: before=%d after=%d", pre.size, info.Size()),
			}
		}
		if info.ModTime() != pre.modTime {
			return &MigrationCorruptedOtherStateError{
				Path:   pre.path,
				Reason: fmt.Sprintf("mtime changed: before=%v after=%v", pre.modTime, info.ModTime()),
			}
		}
		if inodeOf(info) != pre.inode {
			return &MigrationCorruptedOtherStateError{
				Path:   pre.path,
				Reason: fmt.Sprintf("inode changed: before=%d after=%d", pre.inode, inodeOf(info)),
			}
		}
	}
	return nil
}

// nanosRE matches the 19-digit nanos prefix in a leaf filename.
var nanosRE = regexp.MustCompile(`^\d{19}`)

// IsMigrationError reports whether err is any of the sentinel migration errors.
func IsMigrationError(err error) bool {
	if err == nil {
		return false
	}
	var e1 *MigrationInconsistentLayoutError
	var e2 *MigrationCountMismatchError
	var e3 *MigrationByteMismatchError
	var e4 *MigrationCorruptedOtherStateError
	return errors.As(err, &e1) || errors.As(err, &e2) || errors.As(err, &e3) || errors.As(err, &e4)
}
