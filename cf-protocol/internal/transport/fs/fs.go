package fs

import (
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/internal/campfire"
	cfencoding "github.com/campfire-net/campfire/cf-protocol/internal/encoding"
	"github.com/campfire-net/campfire/cf-protocol/internal/message"
)

// Transport manages the filesystem transport for campfires.
type Transport struct {
	BaseDir    string // $CF_TRANSPORT_DIR, default /tmp/campfire
	rootDir string // if set, CampfireDir returns this directly (path-rooted mode)
}

// DefaultBaseDir returns the default transport base directory.
// Priority: $CF_TRANSPORT_DIR > $CF_HOME/campfires > ~/.campfire/campfires.
// The old default (/tmp/campfire) was ephemeral and caused split-brain:
// store.db survived container restarts but transport files did not.
func DefaultBaseDir() string {
	if env := os.Getenv("CF_TRANSPORT_DIR"); env != "" {
		return env
	}
	if cfHome := os.Getenv("CF_HOME"); cfHome != "" {
		return filepath.Join(cfHome, "campfires")
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		return filepath.Join(u.HomeDir, ".campfire", "campfires")
	}
	return "/tmp/campfire"
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
//   messages/<YYYY-MM>/<DD>/<NanosWidth-nanos>-<message-id>.cbor
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

	// leafFiles maps leaf filename → full path, used for deduplication and
	// lex-sort merge. We populate it from both the bucketed and flat layouts.
	type leafEntry struct {
		name string // leaf filename e.g. "0000000001234567890-<id>.cbor"
		path string // full path on disk
	}
	var leaves []leafEntry

	for _, e := range entries {
		name := e.Name()

		if e.IsDir() && yearMonthRE.MatchString(name) {
			// Bucketed layout: YYYY-MM directory. Descend into DD subdirs.
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

	var msgs []message.Message
	for _, lf := range leaves {
		data, err := os.ReadFile(lf.path)
		if err != nil {
			continue // File disappeared mid-walk — tolerate.
		}
		var msg message.Message
		if err := cfencoding.Unmarshal(data, &msg); err != nil {
			continue // Corrupt file — skip (same behaviour as before).
		}
		msgs = append(msgs, msg)
	}
	return msgs, nil
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
