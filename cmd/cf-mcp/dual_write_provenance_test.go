package main

// dual_write_provenance_test.go — Regression test for TOCTOU race in
// dualWriteProvenanceStore.syncToCloud (campfireagent-2b2).
//
// Without the mutex, concurrent AddAttestation calls interleave:
//   goroutine A mutates disk → goroutine B mutates disk → B reads file (only B's state) →
//   A reads file (only A's state) → one upload silently reverts the other.
//
// With the mutex, each mutation + cloud upload is atomic, so the cloud always
// reflects the post-mutation state accumulated by all goroutines.

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/provenance"
)

// inMemoryAttestationPersister is a thread-safe stub that records every
// SaveAttestations call and allows callers to read back the last saved blob.
type inMemoryAttestationPersister struct {
	mu   sync.Mutex
	data map[string][]byte
	// calls tracks every SaveAttestations invocation for post-test inspection.
	calls [][]byte
}

func newInMemoryAttestationPersister() *inMemoryAttestationPersister {
	return &inMemoryAttestationPersister{data: make(map[string][]byte)}
}

func (p *inMemoryAttestationPersister) SaveAttestations(internalID string, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	p.data[internalID] = cp
	p.calls = append(p.calls, cp)
	return nil
}

func (p *inMemoryAttestationPersister) LoadAttestations(internalID string) ([]byte, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	d, ok := p.data[internalID]
	return d, ok, nil
}

// lastSave returns the most recently saved blob for the given internalID.
func (p *inMemoryAttestationPersister) lastSave(internalID string) []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.data[internalID]
}

// TestSyncToCloud_ConcurrentMutations verifies that concurrent AddAttestation
// calls on dualWriteProvenanceStore do not lose cloud state due to a TOCTOU
// race in syncToCloud.
//
// Done condition: after N concurrent goroutines each add one attestation, the
// last cloud upload reflects all N attestations — no goroutine's write is
// silently reverted by another goroutine re-reading a stale on-disk file.
func TestSyncToCloud_ConcurrentMutations(t *testing.T) {
	const (
		goroutines = 20
		internalID = "test-session-id"
	)

	// Set up a temp dir for the file store.
	dir := t.TempDir()
	storePath := dir + "/attestations.json"

	// Open the inner FileStore.
	fs, err := provenance.NewFileStore(storePath, provenance.DefaultConfig())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	// Wire up the in-memory cloud persister.
	cloud := newInMemoryAttestationPersister()

	store := &dualWriteProvenanceStore{
		inner:      fs,
		filePath:   storePath,
		cloud:      cloud,
		internalID: internalID,
	}

	// Generate N distinct attester keys (just hex strings — provenance.Attestation
	// uses string keys, not actual crypto material in this path).
	keys := make([]string, goroutines)
	for i := range keys {
		keys[i] = fmt.Sprintf("%064x", i+1)
	}

	// Launch goroutines that each add one attestation, staggered slightly
	// so they genuinely overlap on the mutation + syncToCloud path.
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-start // all goroutines start at the same instant
			att := &provenance.Attestation{
				ID:          fmt.Sprintf("att-%d", i),
				TargetKey:   keys[i],
				VerifierKey: keys[(i+1)%goroutines],
				VerifiedAt:  time.Now(),
			}
			if addErr := store.AddAttestation(att); addErr != nil && addErr != provenance.ErrNotCoSigned {
				t.Errorf("goroutine %d: AddAttestation: %v", i, addErr)
			}
		}()
	}
	close(start)
	wg.Wait()

	// The last cloud upload must contain ALL attestations written to disk.
	// Read the on-disk file directly — it is the ground truth.
	diskData, readErr := os.ReadFile(storePath)
	if readErr != nil {
		t.Fatalf("reading disk file after concurrent mutations: %v", readErr)
	}

	cloudData := cloud.lastSave(internalID)
	if cloudData == nil {
		t.Fatal("cloud persister received no SaveAttestations calls")
	}

	// The cloud data must be identical to the current disk file.  Because the
	// mutex serialises mutation + upload, the final upload must have read the
	// fully-accumulated file, not a stale intermediate.
	if string(diskData) != string(cloudData) {
		t.Errorf("cloud data diverges from disk:\n  disk  (%d bytes): %s\n  cloud (%d bytes): %s",
			len(diskData), diskData, len(cloudData), cloudData)
	}

	// Sanity: verify the cloud data represents all goroutines' attestations.
	// Open a fresh FileStore from the cloud data to count attestations.
	recoveryDir := t.TempDir()
	recoveryPath := recoveryDir + "/recovered.json"
	if writeErr := os.WriteFile(recoveryPath, cloudData, 0o600); writeErr != nil {
		t.Fatalf("writing cloud data for recovery check: %v", writeErr)
	}
	recovered, openErr := provenance.NewFileStore(recoveryPath, provenance.DefaultConfig())
	if openErr != nil {
		t.Fatalf("opening recovered store: %v", openErr)
	}

	// Each goroutine added one attestation with TargetKey=keys[i]. Verify all
	// target keys are present in the recovered store.
	missing := 0
	for i, key := range keys {
		atts := recovered.Attestations(key)
		if len(atts) == 0 {
			t.Errorf("goroutine %d: attestation for target key %s missing from cloud data", i, key)
			missing++
		}
	}
	if missing > 0 {
		t.Errorf("%d/%d attestations missing from cloud after concurrent mutations — TOCTOU race not fixed", missing, goroutines)
	}
}
