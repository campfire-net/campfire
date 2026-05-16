//go:build !windows

package fs

// flock_contention_unix_test.go — LOCK_EX contention regression test.
//
// Verifies that WriteMessage's LOCK_SH acquisition actually contends with a
// concurrent LOCK_EX holder (e.g. migrate-store during atomic swap). This is
// the veracity adversary wave-2 finding: without this test, the LOCK_SH
// discipline in lock_unix.go is present in code but unproven at runtime.
//
// Covered: campfireagent-aea (rework on PR #580).

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestWriteMessage_BlockedByMigrateLockEx proves that WriteMessage (which
// acquires LOCK_SH on .migrate.lock) is blocked when a concurrent goroutine
// holds LOCK_EX on that file — and unblocks promptly once LOCK_EX is released.
//
// Six-step recipe:
//  1. Init a campfire dir; ensure .migrate.lock exists (WriteMessage creates it
//     on first use, but we open it explicitly here to get a separate fd for
//     the exclusive lock).
//  2. Open .migrate.lock with O_RDWR in a separate goroutine and acquire LOCK_EX.
//  3. Start WriteMessage from the main goroutine in another goroutine; signal
//     via doneCh when it returns.
//  4. Assert doneCh does NOT fire within ~100 ms (write is blocked).
//  5. Release LOCK_EX in the holder goroutine.
//  6. Assert doneCh fires within ~100 ms after release (write proceeds).
//  7. Verify the message landed in the bucketed layout via ListMessages.
func TestWriteMessage_BlockedByMigrateLockEx(t *testing.T) {
	tr := newTestTransport(t)
	cf := newTestCampfire(t)
	if err := tr.Init(cf); err != nil {
		t.Fatalf("Init(): %v", err)
	}

	campfireDir := tr.CampfireDir(cf.PublicKeyHex())
	lockPath := filepath.Join(campfireDir, ".migrate.lock")

	// Pre-create the lockfile so our separate fd can open it before WriteMessage
	// runs (WriteMessage also creates it via O_CREATE, but we want our fd ready
	// before the race begins).
	if err := os.WriteFile(lockPath, nil, 0700); err != nil { //nolint:gosec
		t.Fatalf("pre-creating .migrate.lock: %v", err)
	}

	// Step 1 → 2: open .migrate.lock and acquire LOCK_EX.
	holderF, err := os.OpenFile(lockPath, os.O_RDWR, 0700) //nolint:gosec
	if err != nil {
		t.Fatalf("opening .migrate.lock for LOCK_EX holder: %v", err)
	}
	defer holderF.Close()

	if err := syscall.Flock(int(holderF.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("acquiring LOCK_EX: %v", err)
	}

	// Step 3: run WriteMessage in a goroutine; it will try to acquire LOCK_SH
	// and block until we release LOCK_EX.
	msg := newTestMessage(t)
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- tr.WriteMessage(cf.PublicKeyHex(), msg)
	}()

	// Step 4: WriteMessage must NOT complete within the blocking window.
	// We give it 100 ms — enough margin for any scheduling jitter on CI.
	const blockWindow = 100 * time.Millisecond
	select {
	case err := <-doneCh:
		// If WriteMessage returned before we released LOCK_EX, the LOCK_SH
		// discipline is not working.
		t.Fatalf("WriteMessage returned before LOCK_EX was released (err=%v); LOCK_SH contention is broken", err)
	case <-time.After(blockWindow):
		// Good: WriteMessage is blocked, as expected.
	}

	// Step 5: release LOCK_EX so WriteMessage can proceed.
	if err := syscall.Flock(int(holderF.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatalf("releasing LOCK_EX: %v", err)
	}

	// Step 6: WriteMessage must now complete within 100 ms.
	const unblockWindow = 100 * time.Millisecond
	select {
	case writeErr := <-doneCh:
		if writeErr != nil {
			t.Fatalf("WriteMessage returned error after LOCK_EX released: %v", writeErr)
		}
	case <-time.After(unblockWindow):
		t.Fatalf("WriteMessage did not unblock within %v after LOCK_EX release", unblockWindow)
	}

	// Step 7: verify the message landed in the bucketed layout.
	msgs, err := tr.ListMessages(cf.PublicKeyHex())
	if err != nil {
		t.Fatalf("ListMessages(): %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].ID != msg.ID {
		t.Errorf("message ID mismatch: got %q, want %q", msgs[0].ID, msg.ID)
	}
}
