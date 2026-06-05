package fs

import (
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/internal/campfire"
	cfencoding "github.com/campfire-net/campfire/cf-protocol/internal/encoding"
	"github.com/campfire-net/campfire/cf-protocol/internal/message"
)

// Transport manages the filesystem transport for campfires.
type Transport struct {
	BaseDir string // $CF_TRANSPORT_DIR, default /tmp/campfire
	rootDir string // if set, CampfireDir returns this directly (path-rooted mode)
}

// DefaultBaseDir returns the default transport base directory.
//
// Resolution order (campfireagent-3f0):
//  1. $CF_TRANSPORT_DIR — explicit override (back-compat, highest priority).
//  2. $CF_HOME — explicit override; returns CF_HOME/campfires (back-compat).
//  3. Tree-walk from CWD for a .cf/config.toml with transport.storage_root;
//     returns storage_root/campfires.
//  4. ~/.campfire/campfires — compiled-in default.
//
// CF_HOME is now override-only: when neither CF_TRANSPORT_DIR nor CF_HOME is set,
// the transport resolves its root from the project's .cf/config.toml, allowing
// jailed automata to point storage_root at a persona's data directory without
// setting any process-wide environment variables.
//
// The suffix decision ("campfires" appended or not) is made from the resolution
// source returned by resolveStorageRootFull — environment variables are NOT
// re-read here (campfireagent-75f double-read fix).
func DefaultBaseDir() string {
	cwd, _ := os.Getwd()
	result := resolveStorageRootFull(cwd)
	switch result.Source {
	case sourceTransportDir:
		// CF_TRANSPORT_DIR is the BaseDir as-is (existing semantics — no suffix).
		return result.Root
	case sourceCFHome:
		// CF_HOME path already ends in "campfires" — returned directly.
		return result.Root
	default:
		// sourceConfig or sourceDefault: storage_root or ~/.campfire — append "campfires".
		return filepath.Join(result.Root, "campfires")
	}
}

// New creates a Transport with the given base directory.
// Campfire directories are derived as baseDir/campfireID.
func New(baseDir string) *Transport {
	return &Transport{BaseDir: baseDir}
}

// NewPathRooted creates a Transport where the campfire directory is the given
// path directly, not derived from a base directory + campfire ID. Use this
// when a campfire's state lives at a known filesystem path (e.g. a project's
// .campfire/ directory, or any folder that owns its campfire).
func NewPathRooted(dir string) *Transport {
	return &Transport{rootDir: dir}
}

// IsPathRooted reports whether this transport uses a fixed directory rather
// than deriving campfire directories from a base directory + ID.
func (t *Transport) IsPathRooted() bool {
	return t.rootDir != ""
}

// ForDir returns a Transport that resolves the given directory directly.
// If dir is empty, falls back to a standard transport using DefaultBaseDir().
// Use this to reconstruct a transport from a stored TransportDir.
func ForDir(dir string) *Transport {
	if dir != "" {
		return &Transport{rootDir: dir}
	}
	return &Transport{BaseDir: DefaultBaseDir()}
}

// CampfireDir returns the transport directory for a campfire.
// In path-rooted mode, this returns the root directory directly,
// ignoring campfireID.
func (t *Transport) CampfireDir(campfireID string) string {
	if t.rootDir != "" {
		return t.rootDir
	}
	return filepath.Join(t.BaseDir, campfireID)
}

// Init creates the transport directory structure for a new campfire
// and writes the campfire state and creator's member record.
func (t *Transport) Init(c *campfire.Campfire) error {
	dir := t.CampfireDir(c.PublicKeyHex())

	// Create directory structure.
	// Use 0700 — campfire.cbor in the parent dir contains the campfire
	// private key, and member/message sub-directories sit inside the same
	// campfire root. World-readable directories would expose private key
	// material to other users on the same host.
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0700); err != nil {
			return fmt.Errorf("creating %s directory: %w", sub, err)
		}
	}

	// Write campfire state
	state := c.State()
	if err := atomicWriteCBOR(filepath.Join(dir, "campfire.cbor"), state); err != nil {
		return fmt.Errorf("writing campfire state: %w", err)
	}

	return nil
}

// WriteMember writes a member record to the transport directory.
func (t *Transport) WriteMember(campfireID string, member campfire.MemberRecord) error {
	dir := t.CampfireDir(campfireID)
	memberID := fmt.Sprintf("%x", member.PublicKey)
	path := filepath.Join(dir, "members", memberID+".cbor")
	return atomicWriteCBOR(path, member)
}

// ReadState reads the campfire state from the transport directory.
func (t *Transport) ReadState(campfireID string) (*campfire.CampfireState, error) {
	path := filepath.Join(t.CampfireDir(campfireID), "campfire.cbor")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading campfire state: %w", err)
	}
	var state campfire.CampfireState
	if err := cfencoding.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decoding campfire state: %w", err)
	}
	return &state, nil
}

// ListMembers reads all member records from the transport directory.
func (t *Transport) ListMembers(campfireID string) ([]campfire.MemberRecord, error) {
	dir := filepath.Join(t.CampfireDir(campfireID), "members")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("listing members: %w", err)
	}

	var members []campfire.MemberRecord
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".cbor" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading member %s: %w", e.Name(), err)
		}
		var m campfire.MemberRecord
		if err := cfencoding.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("decoding member %s: %w", e.Name(), err)
		}
		members = append(members, m)
	}
	return members, nil
}

// RemoveMember removes a member record from the transport directory.
func (t *Transport) RemoveMember(campfireID string, memberPubKey []byte) error {
	memberID := fmt.Sprintf("%x", memberPubKey)
	path := filepath.Join(t.CampfireDir(campfireID), "members", memberID+".cbor")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing member: %w", err)
	}
	return nil
}

// bucketFor returns the (yearMonth, day) bucket components for a given time in UTC.
// yearMonth is "YYYY-MM" and day is "DD" — both zero-padded and lex-sortable.
// This is the v0.31 bucket key per design §1.3.
func bucketFor(t time.Time) (yearMonth, day string) {
	u := t.UTC()
	return u.Format("2006-01"), u.Format("02")
}

// migrateLockPath returns the path of the migration lockfile for a campfire directory.
func migrateLockPath(campfireDir string) string {
	return filepath.Join(campfireDir, ".migrate.lock")
}

// WriteMessage writes a message to the campfire's messages directory using the
// day-bucketed layout introduced in v0.31:
//
//	messages/<YYYY-MM>/<DD>/<NanosWidth-nanos>-<message-id>.cbor
//
// Before writing, a shared flock (LOCK_SH) is acquired on .migrate.lock so that
// any concurrent migrate-store run (which holds LOCK_EX) will block all writers
// until the atomic swap completes. Multiple concurrent writers can all hold
// LOCK_SH simultaneously — there is no writer-writer contention.
//
// After writing, the message file is copied synchronously to all push subscribers' inbox dirs.
// Push subscriber inboxes are NOT bucketed (§1.6 explicit non-scope).
func (t *Transport) WriteMessage(campfireID string, msg *message.Message) error {
	// SECURITY (campfireagent-3d0): validate msg.ID before constructing the
	// on-disk filename. An unvalidated ID enables path traversal —
	// filepath.Join cleans "../../../etc/pwned" out of the bucket dir, so any
	// signed message with a crafted ID could write CBOR to arbitrary paths.
	// The UUID regex restriction makes traversal lexically impossible (no /,
	// no ., no null bytes, no anything but [0-9a-fA-F-]).
	if err := message.ValidateID(msg.ID); err != nil {
		return err
	}

	campfireDir := t.CampfireDir(campfireID)

	// Acquire LOCK_SH on the migration lockfile. This blocks if migrate-store
	// holds LOCK_EX during the atomic swap window; it is a no-op otherwise.
	release, err := acquireMigrateLockShared(migrateLockPath(campfireDir))
	if err != nil {
		return fmt.Errorf("acquiring migration lock: %w", err)
	}
	defer release()

	now := timeNow()
	yearMonth, day := bucketFor(now)
	bucketDir := filepath.Join(campfireDir, "messages", yearMonth, day)
	if err := os.MkdirAll(bucketDir, 0700); err != nil {
		return fmt.Errorf("creating bucket directory %s: %w", bucketDir, err)
	}

	filename := fmt.Sprintf("%0*d-%s.cbor", NanosWidth, now.UnixNano(), msg.ID)
	path := filepath.Join(bucketDir, filename)
	if err := atomicWriteCBOR(path, msg); err != nil {
		return err
	}

	// Push delivery: copy the message file to each subscriber's inbox dir.
	// The inbox receives the flat filename (no bucket path) — inboxes are not bucketed.
	subs, err := t.ListPushSubscribers(campfireID)
	if err != nil {
		// Non-fatal: log and continue.
		log.Printf("fs transport: listing push subscribers for %s: %v", campfireID, err)
		return nil
	}
	for _, sub := range subs {
		if err := copyFile(path, filepath.Join(sub.InboxDir, filename)); err != nil {
			// Non-fatal: log and continue so other subscribers still receive the message.
			log.Printf("fs transport: push delivery to %s failed: %v", sub.InboxDir, err)
		}
	}
	return nil
}

// AddPushSubscriber registers a push subscriber for a campfire.
// inboxDir is the directory to which message files are copied on each WriteMessage call.
// Calling AddPushSubscriber with the same memberPubkey overwrites the previous entry.
func (t *Transport) AddPushSubscriber(campfireID string, memberPubkey []byte, inboxDir string) error {
	dir := filepath.Join(t.CampfireDir(campfireID), "push-subscribers")
	// 0700: push-subscribers lives inside the campfire root dir and
	// contains member pubkey filenames — restrict to owner only.
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating push-subscribers directory: %w", err)
	}
	memberID := fmt.Sprintf("%x", memberPubkey)
	path := filepath.Join(dir, memberID+".txt")
	if err := os.WriteFile(path, []byte(inboxDir), 0600); err != nil {
		return fmt.Errorf("writing push subscriber: %w", err)
	}
	return nil
}

// RemovePushSubscriber removes a push subscriber for a campfire.
// It is idempotent: removing a non-existent subscriber is not an error.
func (t *Transport) RemovePushSubscriber(campfireID string, memberPubkey []byte) error {
	memberID := fmt.Sprintf("%x", memberPubkey)
	path := filepath.Join(t.CampfireDir(campfireID), "push-subscribers", memberID+".txt")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing push subscriber: %w", err)
	}
	return nil
}

// PushSubscriber holds the pubkey and inbox directory for a push subscriber.
type PushSubscriber struct {
	MemberPubkey []byte
	InboxDir     string
}

// ListPushSubscribers returns all push subscribers for a campfire.
// Returns an empty slice (not an error) if no subscribers exist.
func (t *Transport) ListPushSubscribers(campfireID string) ([]PushSubscriber, error) {
	dir := filepath.Join(t.CampfireDir(campfireID), "push-subscribers")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing push subscribers: %w", err)
	}

	var subs []PushSubscriber
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".txt" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			if os.IsNotExist(err) {
				continue // removed between ReadDir and ReadFile
			}
			return nil, fmt.Errorf("reading push subscriber %s: %w", e.Name(), err)
		}
		// Derive pubkey bytes from the hex filename (strip .txt suffix).
		hexID := strings.TrimSuffix(e.Name(), ".txt")
		pubkeyBytes, err := hexDecode(hexID)
		if err != nil {
			log.Printf("fs transport: ignoring subscriber file with invalid name %s: %v", e.Name(), err)
			continue
		}
		subs = append(subs, PushSubscriber{
			MemberPubkey: pubkeyBytes,
			InboxDir:     string(data),
		})
	}
	return subs, nil
}

// copyFile copies src to dst byte-for-byte. dst's parent directory must already exist.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening source: %w", err)
	}
	defer in.Close()

	// 0700: inbox dirs receive message copies delivered from the campfire.
	// Keep consistent with campfire transport directory permissions.
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return fmt.Errorf("creating destination directory: %w", err)
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if os.IsExist(err) {
			// Dedup: file already delivered (same UUID filename).
			return nil
		}
		return fmt.Errorf("creating destination: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst) // clean up partial write
		return fmt.Errorf("copying: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(dst) // flush failed — partial write
		return fmt.Errorf("closing destination: %w", err)
	}
	return nil
}

// hexDecode decodes a hex string into bytes.
func hexDecode(s string) ([]byte, error) {
	if len(s)%2 != 0 {
		return nil, fmt.Errorf("odd-length hex string")
	}
	b := make([]byte, len(s)/2)
	for i := range b {
		var v byte
		hi, lo := s[2*i], s[2*i+1]
		hv, err := hexNibble(hi)
		if err != nil {
			return nil, err
		}
		lv, err := hexNibble(lo)
		if err != nil {
			return nil, err
		}
		v = hv<<4 | lv
		b[i] = v
	}
	return b, nil
}

// hexNibble converts a single hex character to its numeric value.
func hexNibble(c byte) (byte, error) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', nil
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, nil
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, nil
	}
	return 0, fmt.Errorf("invalid hex character %q", c)
}

// yearMonthRE matches YYYY-MM directory names (v0.31 bucketed layout month dirs).
var yearMonthRE = regexp.MustCompile(`^\d{4}-\d{2}$`)

// dayRE matches DD directory names (v0.31 bucketed layout day dirs).
var dayRE = regexp.MustCompile(`^\d{2}$`)

// ListMessages reads all messages from the campfire's messages directory.
//
// v0.31 dual-read: reads both the bucketed layout (YYYY-MM/DD/*.cbor) and any
// legacy flat *.cbor files at the top of messages/. Results are merged by
// lex-sort of the leaf filename (which equals lex-sort of the 19-nanos prefix,
// which equals chronological order). This dual-read is transitional and will
// be removed in v0.32.
//
// Directory entries that match neither the bucket pattern nor "*.cbor" are
// silently ignored for forward-compatibility (A7).
//
// Returns (nil, nil) if the messages directory does not exist (e.g. during the
// atomic swap window in migrate-store or for a brand-new campfire).
func (t *Transport) ListMessages(campfireID string) ([]message.Message, error) {
	leaves, err := t.collectLeaves(campfireID, "")
	if err != nil {
		return nil, err
	}
	lms := readLeaves(leaves)
	if len(lms) == 0 {
		// Preserve the historical nil-slice contract for empty/missing dirs
		// (callers and tests distinguish nil from an empty non-nil slice).
		return nil, nil
	}
	msgs := make([]message.Message, len(lms))
	for i := range lms {
		msgs[i] = lms[i].Message
	}
	return msgs, nil
}

// LeafMessage pairs a decoded message with its on-disk leaf filename. The leaf
// carries the fixed-width nanos prefix that defines chronological order and is
// the value persisted as the incremental-sync cursor.
type LeafMessage struct {
	Leaf    string
	Message message.Message
}

// ListMessagesSince returns the messages whose leaf filename sorts strictly after
// afterLeaf, in chronological order, each paired with its leaf filename. Because
// leaf filenames carry a fixed-width zero-padded nanosecond prefix, lex order
// equals chronological order, so a leaf cursor with strict ">" comparison selects
// exactly the messages written after the cursor — no re-reads, no skips.
//
// Pass afterLeaf == "" to read the full history (first sync).
//
// Critically, only the surviving (post-cursor) files are read from disk and
// unmarshalled — old messages are dropped at the directory-listing level before
// any file is opened. This is the difference between O(total messages) and
// O(new messages) per sync.
func (t *Transport) ListMessagesSince(campfireID, afterLeaf string) ([]LeafMessage, error) {
	page, err := t.ListMessagesPage(campfireID, afterLeaf, 0)
	return page.Messages, err
}

// ListPage is one chunk of a paged read over a campfire's message history.
type ListPage struct {
	// Messages are the successfully decoded messages in this page, oldest first.
	Messages []LeafMessage
	// LastListed is the leaf filename of the last directory entry examined for
	// this page ("" if the page is empty). It can trail Messages when files at
	// the end of the page were corrupt or disappeared mid-walk: a paging caller
	// must advance its cursor to LastListed — not to the last decoded message —
	// or a run of undecodable files at a page boundary would stall the cursor
	// permanently.
	LastListed string
	// More reports whether directory entries beyond LastListed remained when
	// the page was cut. False means this page reached the end of the history.
	More bool
}

// ListMessagesPage reads at most limit messages whose leaf filenames sort
// strictly after afterLeaf, in chronological order. limit <= 0 means no cap
// (one page spanning the full remaining history, with More == false).
//
// The cap is applied at the directory-listing level, before any file is
// opened, so a caller paging through a large history reads only page-sized
// batches into memory — O(limit) per call instead of O(remaining). This is
// the primitive behind bounded/chunked sync (campfireagent-6d3: an unbounded
// first sync of a >260k-message campfire loaded 2GB into memory and could not
// complete inside a join).
func (t *Transport) ListMessagesPage(campfireID, afterLeaf string, limit int) (ListPage, error) {
	leaves, err := t.collectLeaves(campfireID, afterLeaf)
	if err != nil {
		return ListPage{}, err
	}
	// Drop leaves at or before the cursor before any file is read.
	filtered := leaves[:0:0]
	for _, lf := range leaves {
		if lf.name > afterLeaf {
			filtered = append(filtered, lf)
		}
	}
	var page ListPage
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
		page.More = true
	}
	if len(filtered) > 0 {
		page.LastListed = filtered[len(filtered)-1].name
	}
	page.Messages = readLeaves(filtered)
	return page, nil
}

// bucketCutoffFor maps a leaf cursor to the (yearMonth, day) bucket holding it,
// for directory pruning in collectLeaves. WriteMessage derives both the bucket
// path and the leaf's nanos prefix from the same UTC instant, so every leaf in
// a bucket dated strictly before the cursor's bucket has a smaller nanos
// prefix — i.e. sorts at or before the cursor and can be skipped without
// listing it (campfire-57c: re-listing all 260k+ entries per chunk made
// chunked sync O(total)/chunk instead of O(new)).
//
// Returns ok == false (no pruning) when the cursor is empty or unparseable —
// the full walk is the safe fallback.
func bucketCutoffFor(afterLeaf string) (yearMonth, day string, ok bool) {
	if afterLeaf == "" {
		return "", "", false
	}
	nanos, err := parseNanosPrefix(afterLeaf)
	if err != nil {
		return "", "", false
	}
	ym, dd := bucketFor(time.Unix(0, nanos))
	return ym, dd, true
}

// LookbackCursor returns a synthetic leaf bound that is `lookback` earlier than
// the given cursor leaf, for use as the afterLeaf argument to ListMessagesSince.
//
// The bare leaf cursor with strict ">" is exact under a monotonic clock, but a
// backward clock step (e.g. an NTP correction) between two writes can produce a
// new message whose nanos prefix sorts below the cursor — which a strict cursor
// would skip permanently. Rewinding the cursor by a small lookback window makes
// the sync re-examine recent messages so such a message is still imported.
// Re-imports are idempotent (INSERT OR IGNORE by message ID), so the only cost
// is reading the few messages inside the window — far cheaper than the full
// history, preserving the O(new) property.
//
// The returned bound has no "-<id>" suffix, so a real leaf sharing its nanos
// prefix still sorts strictly after it (longer string, same prefix). Returns ""
// unchanged (first sync reads the full history).
func LookbackCursor(leaf string, lookback time.Duration) string {
	if leaf == "" || lookback <= 0 {
		// Non-positive lookback is a no-op: return the exact leaf for strict
		// cursor semantics (used by tests; production passes a positive window).
		return leaf
	}
	nanos, err := parseNanosPrefix(leaf)
	if err != nil {
		// Unparseable cursor: fall back to the exact leaf (no rewind).
		return leaf
	}
	bound := nanos - lookback.Nanoseconds()
	if bound < 0 {
		bound = 0
	}
	return fmt.Sprintf("%0*d", NanosWidth, bound)
}

// DefaultSyncLookback is the default incremental-sync lookback window (see
// LookbackCursor). It comfortably covers typical NTP step corrections.
const DefaultSyncLookback = 2 * time.Second

// SyncLookbackFromEnv returns the incremental-sync lookback window, read from
// CF_FS_SYNC_LOOKBACK_MS (non-negative integer milliseconds; 0 = strict cursor),
// defaulting to DefaultSyncLookback. This is the single source of truth shared by
// both filesystem-sync call paths (cmd StoreSyncer and protocol syncIfFilesystem).
func SyncLookbackFromEnv() time.Duration {
	if v := os.Getenv("CF_FS_SYNC_LOOKBACK_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms >= 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return DefaultSyncLookback
}

// leafEntry is a single on-disk message file: its leaf filename (which carries
// the chronological nanos prefix) and its full path.
type leafEntry struct {
	name string // leaf filename e.g. "0000000001234567890-<id>.cbor"
	path string // full path on disk
}

// readLeaves reads and unmarshals each leaf in order, skipping files that
// disappear mid-walk or fail to decode. Returns one LeafMessage per successfully
// decoded file, preserving chronological order.
func readLeaves(leaves []leafEntry) []LeafMessage {
	var out []LeafMessage
	for _, lf := range leaves {
		data, err := os.ReadFile(lf.path)
		if err != nil {
			continue // File disappeared mid-walk — tolerate.
		}
		var msg message.Message
		if err := cfencoding.Unmarshal(data, &msg); err != nil {
			continue // Corrupt file — skip (same behaviour as before).
		}
		out = append(out, LeafMessage{Leaf: lf.name, Message: msg})
	}
	return out
}

// collectLeaves walks the bucketed + flat message layouts for a campfire and
// returns all leaf entries sorted in chronological (lex) order. Directory
// listings are cheap metadata reads; no message file is opened here.
//
// afterLeaf, when non-empty and parseable, prunes the walk: bucket directories
// dated strictly before the cursor's UTC bucket are skipped without listing
// them (see bucketCutoffFor), so paged callers list O(days-at-or-after-cursor)
// entries instead of the full history. Pass "" to walk everything. The
// cursor's own bucket is always walked (it holds leaves on both sides of the
// cursor), and the leaf-level "> afterLeaf" filter in the callers remains the
// source of truth — pruning only removes directories that cannot contain a
// qualifying leaf.
func (t *Transport) collectLeaves(campfireID, afterLeaf string) ([]leafEntry, error) {
	cutoffYM, cutoffDay, pruneOK := bucketCutoffFor(afterLeaf)
	dir := filepath.Join(t.CampfireDir(campfireID), "messages")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// ENOENT: no messages directory — brand new campfire or mid-swap.
			// Return (nil, nil) same as today (A4, M8).
			return nil, nil
		}
		return nil, fmt.Errorf("listing messages: %w", err)
	}

	var leaves []leafEntry

	for _, e := range entries {
		name := e.Name()

		if e.IsDir() && yearMonthRE.MatchString(name) {
			// Bucketed layout: YYYY-MM directory. Descend into DD subdirs.
			if pruneOK && name < cutoffYM {
				continue // Entire month precedes the cursor's bucket.
			}
			ymDir := filepath.Join(dir, name)
			ddEntries, err := os.ReadDir(ymDir)
			if err != nil {
				if os.IsNotExist(err) {
					continue // Disappeared between ReadDir calls — tolerate.
				}
				return nil, fmt.Errorf("listing month dir %s: %w", ymDir, err)
			}
			// Sort DD entries lex (already lex from ReadDir, but make it explicit).
			sort.Slice(ddEntries, func(i, j int) bool {
				return ddEntries[i].Name() < ddEntries[j].Name()
			})
			for _, ddE := range ddEntries {
				ddName := ddE.Name()
				if !ddE.IsDir() || !dayRE.MatchString(ddName) {
					// Silently ignore non-matching entries (A7).
					continue
				}
				if pruneOK && name == cutoffYM && ddName < cutoffDay {
					continue // Entire day precedes the cursor's bucket.
				}
				ddDir := filepath.Join(ymDir, ddName)
				cborEntries, err := os.ReadDir(ddDir)
				if err != nil {
					if os.IsNotExist(err) {
						continue
					}
					return nil, fmt.Errorf("listing day dir %s: %w", ddDir, err)
				}
				sort.Slice(cborEntries, func(i, j int) bool {
					return cborEntries[i].Name() < cborEntries[j].Name()
				})
				for _, ce := range cborEntries {
					if !strings.HasSuffix(ce.Name(), ".cbor") {
						continue // A7: silently ignore non-.cbor entries.
					}
					leaves = append(leaves, leafEntry{
						name: ce.Name(),
						path: filepath.Join(ddDir, ce.Name()),
					})
				}
			}
			continue
		}

		// Flat legacy layout: *.cbor files directly under messages/.
		// Dual-read step 2 per §3.4.
		if !e.IsDir() && strings.HasSuffix(name, ".cbor") {
			leaves = append(leaves, leafEntry{
				name: name,
				path: filepath.Join(dir, name),
			})
			continue
		}

		// Silently ignore everything else (A7: forward-compat with sidecar files,
		// unknown dirs, etc.).
	}

	// Merge by lex-sort of leaf filename. Because both bucketed and flat entries
	// use the same NanosWidth-nanos prefix on the leaf filename, lex-sort of leaf names
	// is equivalent to lex-sort of the flat layout — same chronological order.
	sort.Slice(leaves, func(i, j int) bool {
		return leaves[i].name < leaves[j].name
	})

	return leaves, nil
}

// Remove removes the entire transport directory for a campfire.
func (t *Transport) Remove(campfireID string) error {
	return os.RemoveAll(t.CampfireDir(campfireID))
}

// LockExclusive acquires LOCK_EX on the migration lockfile for campfireDir.
// This blocks all concurrent LOCK_SH holders (i.e. WriteMessage callers) until
// the caller releases the lock. Use this when performing bulk on-disk deletes
// (e.g. cf compact --before / --keep-last with retention=discard) so that no
// new messages can be written to the buckets being removed while deletion is in
// progress.
//
// The caller MUST call the returned release function when done.
// MigrateLockPath is exported for use by callers that need the path for testing.
func (t *Transport) LockExclusive(campfireDir string) (release func(), err error) {
	return acquireMigrateLockExclusive(migrateLockPath(campfireDir))
}

// MigrateLockPath returns the path of the migration lockfile for the given campfire directory.
// Exported for testing (e.g. to verify LOCK_EX contention with LOCK_SH holders).
func MigrateLockPath(campfireDir string) string {
	return migrateLockPath(campfireDir)
}

// randRead is the function used to fill random bytes for temp file names.
// It is a package-level variable so tests can inject a failing reader to
// exercise the nanosecond-timestamp fallback path.
var randRead = func(b []byte) (int, error) { return rand.Read(b) }

// timeNow is the clock function used by WriteMessage to determine the bucket
// and the NanosWidth-nanos filename prefix. It is a package-level variable so tests
// can inject a fixed or stepping clock without filesystem mocking (A6).
var timeNow = time.Now

// atomicWriteCBOR writes CBOR data atomically using temp file + rename.
func atomicWriteCBOR(path string, v interface{}) error {
	data, err := cfencoding.Marshal(v)
	if err != nil {
		return fmt.Errorf("encoding: %w", err)
	}

	// Generate random suffix for temp file; fall back to timestamp if crypto/rand fails.
	var randBytes [8]byte
	if _, err := randRead(randBytes[:]); err != nil {
		// Fallback: use nanosecond timestamp so concurrent writers still get distinct names.
		ns := uint64(time.Now().UnixNano()) //nolint:gosec // fallback only
		randBytes[0] = byte(ns >> 56)
		randBytes[1] = byte(ns >> 48)
		randBytes[2] = byte(ns >> 40)
		randBytes[3] = byte(ns >> 32)
		randBytes[4] = byte(ns >> 24)
		randBytes[5] = byte(ns >> 16)
		randBytes[6] = byte(ns >> 8)
		randBytes[7] = byte(ns)
	}
	tmp := fmt.Sprintf("%s.tmp.%x", path, randBytes)

	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("renaming temp file: %w", err)
	}
	return nil
}
