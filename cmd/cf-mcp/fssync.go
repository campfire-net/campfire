package main

// fssync.go — budgeted filesystem→store sync for FS-mode tool handlers
// (campfireagent-6d3).
//
// Problem: the old syncFSVerified primitive re-read the campfire's ENTIRE
// on-disk message history on every call — no cursor, all of it in memory, one
// fsync'd autocommit transaction per insert. On a long-lived campfire (real
// body cfs: >260k messages, 2GB) that is hours of work, called from
// campfire_join (observed as an indefinite join hang on every embodiment
// wake), from every view read, and from every 2-second await poll tick.
// Worse, when the session store is rate-limit wrapped, most of those inserts
// were silently REJECTED by the limiter (default 100/min) and, with no
// cursor, simply lost.
//
// Fix: all FS-mode sync now goes through fsSyncManager, which
//
//   - uses the canonical cursor-based chunked sync (protocol.SyncFilesystemChunk)
//     — durable O(new) progress, bounded memory, one batch transaction per chunk;
//   - bounds the FOREGROUND work a tool call performs (fsSyncForegroundChunks ×
//     fsSyncChunkSize messages, fsSyncForegroundDeadline wall clock) so handlers
//     return promptly no matter how large the history is;
//   - finishes incomplete histories in a per-campfire BACKGROUND goroutine and
//     then re-runs convention tool/view registration, so declarations published
//     beyond the foreground budget surface as MCP tools shortly after join;
//   - syncs through its own PLAIN store connection (same SQLite file), never
//     the rate-limit-wrapped session store: transport sync imports messages
//     that already exist on disk — it is not message ingestion, must not be
//     dropped by free-tier limits, and must not count toward billing caps.
//
// Serialization: one mutex per campfire. Foreground calls hold it for their
// whole budget; the background worker takes it per chunk, so foreground calls
// interleave with at most one chunk of latency.

import (
	"log"
	"sync"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
)

// Foreground sync budget. Vars, not consts, so tests can shrink them to force
// the background path deterministically.
var (
	// fsSyncChunkSize is how many messages each sync chunk reads, verifies, and
	// commits in one batch transaction.
	fsSyncChunkSize = 500
	// fsSyncForegroundChunks caps the chunks a tool-call path runs before
	// handing the remainder to the background worker. 2×500 comfortably covers
	// creation-time convention declarations (real body cfs publish them as
	// messages #1-121) while keeping the worst-case foreground work ~1s.
	fsSyncForegroundChunks = 2
	// fsSyncForegroundDeadline bounds foreground wall clock (cold-cache I/O can
	// make even a small chunk slow; a tool call must still return promptly).
	fsSyncForegroundDeadline = 5 * time.Second
	// fsSyncBgChunkSize is the background worker's chunk size. Each chunk pays
	// a full directory listing of the campfire's message tree, so larger
	// background chunks amortize that listing across more work (5000 × ~10KB ≈
	// 50MB peak — bounded, unlike the old full-history load).
	fsSyncBgChunkSize = 5000
)

// fsSyncBgGate, when non-nil, blocks each background worker before its first
// chunk until the channel is closed. Tests use it to deterministically observe
// the post-foreground state (e.g. "join returned with a partial store") without
// racing the background worker. Always nil in production.
var fsSyncBgGate chan struct{}

// fsSyncManager coordinates budgeted foreground syncs and background
// completion per campfire. One instance per server lifetime (propagated across
// per-request server views via the Session, like conventionTools).
type fsSyncManager struct {
	storePath string

	mu        sync.Mutex
	campfires map[string]*campfireSyncState
}

type campfireSyncState struct {
	// mu serializes sync work for this campfire: cursor read → chunk → cursor
	// advance must not interleave between the foreground and background paths.
	mu sync.Mutex
	// bgLive is true while a background completion goroutine exists. Guarded by mu.
	bgLive bool
	// onComplete callbacks run (outside mu) when a background pass reaches the
	// end of the history — e.g. convention tool re-registration after a join
	// whose foreground budget didn't cover the full history. Guarded by mu.
	onComplete []func()
}

func newFSSyncManager(storePath string) *fsSyncManager {
	return &fsSyncManager{
		storePath: storePath,
		campfires: make(map[string]*campfireSyncState),
	}
}

func (m *fsSyncManager) state(campfireID string) *campfireSyncState {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.campfires[campfireID]
	if !ok {
		st = &campfireSyncState{}
		m.campfires[campfireID] = st
	}
	return st
}

// syncForeground runs up to the foreground budget of verified sync chunks for
// the campfire and reports whether the on-disk history is fully synced. When
// it is not, a background goroutine is (idempotently) started to finish the
// job; onComplete, if non-nil, runs after that background pass completes.
//
// When the history is already fully synced (complete == true), onComplete is
// NOT called — the caller proceeds synchronously with a complete store.
func (m *fsSyncManager) syncForeground(transportDir, campfireID string, onComplete func()) (complete bool, err error) {
	state := m.state(campfireID)
	state.mu.Lock()
	defer state.mu.Unlock()

	st, err := store.Open(m.storePath)
	if err != nil {
		return false, err
	}
	defer st.Close()

	deadline := time.Now().Add(fsSyncForegroundDeadline)
	for i := 0; i < fsSyncForegroundChunks; i++ {
		done, _, chunkErr := protocol.SyncFilesystemChunk(st, campfireID, transportDir, fsSyncChunkSize)
		if chunkErr != nil {
			return false, chunkErr
		}
		if done {
			return true, nil
		}
		if time.Now().After(deadline) {
			break
		}
	}

	// Budget exhausted with history remaining: hand off to the background worker.
	if onComplete != nil {
		state.onComplete = append(state.onComplete, onComplete)
	}
	if !state.bgLive {
		state.bgLive = true
		go m.runBackground(transportDir, campfireID)
	}
	return false, nil
}

// runBackground drains the remaining history chunk by chunk, then runs any
// registered completion callbacks. It takes the campfire lock per chunk so
// foreground syncs (view reads, await polls) interleave rather than starve.
//
// On a chunk error the worker logs and exits without dropping the callbacks:
// the cursor was not advanced past the failure, and the next foreground sync
// that finds the history incomplete restarts a worker which retries from the
// cursor and eventually drains the callbacks.
func (m *fsSyncManager) runBackground(transportDir, campfireID string) {
	if fsSyncBgGate != nil {
		<-fsSyncBgGate
	}
	state := m.state(campfireID)

	st, err := store.Open(m.storePath)
	if err != nil {
		log.Printf("fssync: background sync for %s: opening store: %v", shortID(campfireID, 12), err)
		state.mu.Lock()
		state.bgLive = false
		state.mu.Unlock()
		return
	}
	defer st.Close()

	total := 0
	for {
		state.mu.Lock()
		done, synced, chunkErr := protocol.SyncFilesystemChunk(st, campfireID, transportDir, fsSyncBgChunkSize)
		total += synced
		if chunkErr != nil {
			state.bgLive = false
			state.mu.Unlock()
			log.Printf("fssync: background sync for %s stopped after %d messages: %v (will resume on next sync)", shortID(campfireID, 12), total, chunkErr)
			return
		}
		if done {
			callbacks := state.onComplete
			state.onComplete = nil
			state.bgLive = false
			state.mu.Unlock()
			log.Printf("fssync: background sync for %s complete (%d messages)", shortID(campfireID, 12), total)
			for _, cb := range callbacks {
				cb()
			}
			return
		}
		state.mu.Unlock()
	}
}

// fsSyncMgr returns the server's sync manager, creating it on first use and
// persisting it across per-request server views via the Session (the same
// pattern as conventionTools).
func (s *server) fsSyncMgr() *fsSyncManager {
	if s.fsSync == nil {
		if s.sess != nil && s.sess.fsSync != nil {
			s.fsSync = s.sess.fsSync
		} else {
			s.fsSync = newFSSyncManager(s.storePath())
			if s.sess != nil {
				s.sess.fsSync = s.fsSync
			}
		}
	}
	return s.fsSync
}

// refreshConventionSurface re-runs convention tool and view registration for a
// campfire from a fresh plain store connection. Called from the background
// sync worker after it finishes a history, so declarations published beyond
// the join's foreground budget still surface as MCP tools. Safe from a
// goroutine: it only mutates the internally-locked conventionTools map — never
// *server fields.
func (s *server) refreshConventionSurface(campfireID string) {
	tools := s.conventionTools
	if tools == nil {
		// handleJoin initializes the map before starting the background sync;
		// nothing to refresh into otherwise.
		return
	}
	st, err := store.Open(s.storePath())
	if err != nil {
		log.Printf("fssync: refreshing convention surface for %s: opening store: %v", shortID(campfireID, 12), err)
		return
	}
	defer st.Close()

	decls, declErr := readDeclarations(st, campfireID, campfireID)
	if declErr != nil {
		log.Printf("convention: reading declarations for %s after background sync: %v", shortID(campfireID, 12), declErr)
	} else if len(decls) > 0 {
		names := registerConventionTools(tools, campfireID, decls)
		log.Printf("convention: registered %d tools for campfire %s after background sync", len(names), shortID(campfireID, 12))
	}
	registerViewsFromStore(tools, st, campfireID)
}

// syncCampfireForTool is the FS-mode sync-before-query used by tool handlers
// (join, view reads, await polls, message reads). It replaces the unbounded
// syncFSVerified: same verification guarantees (the canonical chunked sync
// verifies signature and provenance hops on every message), but budgeted,
// cursor-incremental, and batch-committed. Callers in HTTP mode must not call
// this (messages arrive via push and are verified at ingestion).
//
// transportDir is the campfire's transport directory; pass "" to use the
// default transport root (CF_TRANSPORT_DIR or /tmp/campfire) — the historical
// root for the join/view/await paths. The read path passes the membership's
// recorded TransportDir.
//
// Returns whether the store now holds the campfire's complete on-disk history.
// Sync errors are logged, not returned: handlers serve whatever the store
// already has — the historical syncFSVerified behaviour.
func (s *server) syncCampfireForTool(campfireID, transportDir string, onComplete func()) (complete bool) {
	if transportDir == "" {
		transportDir = s.fsTransport().CampfireDir(campfireID)
	}
	complete, err := s.fsSyncMgr().syncForeground(transportDir, campfireID, onComplete)
	if err != nil {
		log.Printf("fssync: sync(%s): %v — serving from local store", shortID(campfireID, 12), err)
	}
	return complete
}
