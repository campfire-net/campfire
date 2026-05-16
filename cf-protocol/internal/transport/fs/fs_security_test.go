package fs

// fs_security_test.go — adversarial regression tests for path-traversal via
// msg.ID (campfireagent-3d0).
//
// Pre-fix, fs.WriteMessage joined an unvalidated msg.ID into the on-disk
// filename. An attacker supplying msg.ID="../../../etc/pwned" got the file
// written outside the bucket directory via filepath.Join's cleaning. These
// tests prove the validation rejects every traversal shape and that no file
// is written anywhere on disk for an invalid ID.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/cf-protocol/internal/message"
)

// newInitializedTransport returns a transport + initialized campfire ready for
// WriteMessage. The campfire directory is created by Init so the migration lock
// path exists — without this, even the validation-success case fails to acquire
// the LOCK_SH on a missing .migrate.lock.
func newInitializedTransport(t *testing.T) (*Transport, string) {
	t.Helper()
	tr := newTestTransport(t)
	cf := newTestCampfire(t)
	if err := tr.Init(cf); err != nil {
		t.Fatalf("tr.Init: %v", err)
	}
	return tr, cf.PublicKeyHex()
}

// assertNoMessageFilesAnywhere walks the entire test temp tree and fails if
// any *.cbor file matching the message-filename shape (digits-prefix +
// "<id>.cbor") exists. Init() legitimately writes campfire.cbor as the state
// file — we exclude that. Successful traversal rejection means no message
// file was written anywhere on disk.
func assertNoMessageFilesAnywhere(t *testing.T, tr *Transport) {
	t.Helper()
	root := tr.BaseDir
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.SkipDir
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		name := filepath.Base(path)
		// campfire.cbor is the state file, written by Init(). Member records
		// live in members/<hex>.cbor and are not message files. Message files
		// have the shape "<19-digit-nanos>-<id>.cbor". We pattern-match the
		// 19-digit prefix to identify message writes.
		if !strings.HasSuffix(name, ".cbor") {
			return nil
		}
		if name == "campfire.cbor" {
			return nil
		}
		// Member records live in a "members" component of the path — exclude.
		if strings.Contains(path, string(os.PathSeparator)+"members"+string(os.PathSeparator)) {
			return nil
		}
		// Any other cbor file is suspect — a message write or a traversal escape.
		t.Errorf("traversal rejected but message-shaped CBOR file written: %s", path)
		return nil
	})
	if walkErr != nil && !os.IsNotExist(walkErr) {
		t.Fatalf("walking transport root: %v", walkErr)
	}
}

// TestWriteMessage_RejectsPathTraversalID verifies the core exploit shape from
// campfireagent-3d0: a "../../etc/pwned" ID escapes the bucket dir via
// filepath.Join cleaning. The fix must reject the call with ErrInvalidMessageID
// and write nothing.
func TestWriteMessage_RejectsPathTraversalID(t *testing.T) {
	tr, campfireID := newInitializedTransport(t)

	msg := newTestMessage(t)
	msg.ID = "../../../../etc/pwned"

	err := tr.WriteMessage(campfireID, msg)
	if err == nil {
		t.Fatal("WriteMessage with traversal ID returned nil error")
	}
	if !errors.Is(err, message.ErrInvalidMessageID) {
		t.Errorf("expected ErrInvalidMessageID, got %T: %v", err, err)
	}
	assertNoMessageFilesAnywhere(t, tr)
}

// TestWriteMessage_RejectsSlashInID rejects any forward slash in the ID, which
// would otherwise create subdirectories inside the bucket.
func TestWriteMessage_RejectsSlashInID(t *testing.T) {
	tr, campfireID := newInitializedTransport(t)

	msg := newTestMessage(t)
	msg.ID = "a/b"

	err := tr.WriteMessage(campfireID, msg)
	if err == nil {
		t.Fatal("WriteMessage with slash ID returned nil error")
	}
	if !errors.Is(err, message.ErrInvalidMessageID) {
		t.Errorf("expected ErrInvalidMessageID, got %T: %v", err, err)
	}
	assertNoMessageFilesAnywhere(t, tr)
}

// TestWriteMessage_RejectsDotDotInID rejects bare ".." which collapses through
// filepath.Join even without a leading slash.
func TestWriteMessage_RejectsDotDotInID(t *testing.T) {
	tr, campfireID := newInitializedTransport(t)

	msg := newTestMessage(t)
	msg.ID = ".."

	err := tr.WriteMessage(campfireID, msg)
	if err == nil {
		t.Fatal("WriteMessage with .. ID returned nil error")
	}
	if !errors.Is(err, message.ErrInvalidMessageID) {
		t.Errorf("expected ErrInvalidMessageID, got %T: %v", err, err)
	}
	assertNoMessageFilesAnywhere(t, tr)
}

// TestWriteMessage_RejectsNullBytes rejects embedded null bytes — some kernels
// interpret these as string terminators when crossing the syscall boundary, so
// a path validated as safe at the Go layer can be truncated to an unsafe one.
func TestWriteMessage_RejectsNullBytes(t *testing.T) {
	tr, campfireID := newInitializedTransport(t)

	msg := newTestMessage(t)
	msg.ID = "a\x00b"

	err := tr.WriteMessage(campfireID, msg)
	if err == nil {
		t.Fatal("WriteMessage with null-byte ID returned nil error")
	}
	if !errors.Is(err, message.ErrInvalidMessageID) {
		t.Errorf("expected ErrInvalidMessageID, got %T: %v", err, err)
	}
	assertNoMessageFilesAnywhere(t, tr)
}

// TestWriteMessage_RejectsEmptyID rejects the empty string. An empty ID
// collapses the filename to "<19-nanos>-.cbor", which is not a traversal per
// se but is still malformed and must not be allowed past the regex.
func TestWriteMessage_RejectsEmptyID(t *testing.T) {
	tr, campfireID := newInitializedTransport(t)

	msg := newTestMessage(t)
	msg.ID = ""

	err := tr.WriteMessage(campfireID, msg)
	if err == nil {
		t.Fatal("WriteMessage with empty ID returned nil error")
	}
	if !errors.Is(err, message.ErrInvalidMessageID) {
		t.Errorf("expected ErrInvalidMessageID, got %T: %v", err, err)
	}
	assertNoMessageFilesAnywhere(t, tr)
}

// TestWriteMessage_AcceptsValidUUID is the positive-control: the canonical
// UUID format produced by uuid.New().String() must continue to be accepted.
// Without this test, a regression that rejects everything would be silent.
func TestWriteMessage_AcceptsValidUUID(t *testing.T) {
	tr, campfireID := newInitializedTransport(t)

	// newTestMessage uses message.NewMessage, which assigns a valid UUID.
	msg := newTestMessage(t)
	if err := message.ValidateID(msg.ID); err != nil {
		t.Fatalf("test fixture has invalid ID: %v", err)
	}

	if err := tr.WriteMessage(campfireID, msg); err != nil {
		t.Fatalf("WriteMessage with valid UUID failed: %v", err)
	}
}
