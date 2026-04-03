package fs

import (
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/campfire-net/campfire/pkg/campfire"
	cfencoding "github.com/campfire-net/campfire/pkg/encoding"
	"github.com/campfire-net/campfire/pkg/message"
)

// Transport manages the filesystem transport for campfires.
type Transport struct {
	BaseDir    string // $CF_TRANSPORT_DIR, default /tmp/campfire
	rootDir string // if set, CampfireDir returns this directly (path-rooted mode)
}

// DefaultBaseDir returns the default transport base directory.
func DefaultBaseDir() string {
	if env := os.Getenv("CF_TRANSPORT_DIR"); env != "" {
		return env
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

// WriteMessage writes a message to the campfire's messages directory.
// After writing, the message file is copied synchronously to all push subscribers' inbox dirs.
func (t *Transport) WriteMessage(campfireID string, msg *message.Message) error {
	dir := filepath.Join(t.CampfireDir(campfireID), "messages")
	filename := fmt.Sprintf("%019d-%s.cbor", time.Now().UnixNano(), msg.ID)
	path := filepath.Join(dir, filename)
	if err := atomicWriteCBOR(path, msg); err != nil {
		return err
	}

	// Push delivery: copy the message file to each subscriber's inbox dir.
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

// ListMessages reads all messages from the campfire's messages directory, sorted by filename.
func (t *Transport) ListMessages(campfireID string) ([]message.Message, error) {
	dir := filepath.Join(t.CampfireDir(campfireID), "messages")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing messages: %w", err)
	}

	// Sort by name (timestamp prefix gives chronological order)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	var msgs []message.Message
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".cbor") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var msg message.Message
		if err := cfencoding.Unmarshal(data, &msg); err != nil {
			continue
		}
		msgs = append(msgs, msg)
	}
	return msgs, nil
}

// Remove removes the entire transport directory for a campfire.
func (t *Transport) Remove(campfireID string) error {
	return os.RemoveAll(t.CampfireDir(campfireID))
}

// randRead is the function used to fill random bytes for temp file names.
// It is a package-level variable so tests can inject a failing reader to
// exercise the nanosecond-timestamp fallback path.
var randRead = func(b []byte) (int, error) { return rand.Read(b) }

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
