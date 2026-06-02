package storage

import (
	"fmt"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
)

// LocalStorage is the single-machine backend. The filesystem transport
// directory is the SOURCE OF TRUTH and the embedded SQLite store is a
// rebuildable cache.
//
// LocalStorage embeds store.Store so every store operation forwards to SQLite
// unchanged. The fs-truth-over-cache behavior is confined to GetMembership:
// on a SQLite cache miss, GetMembership reconstructs the membership from the
// filesystem transport directory and writes it back into the cache, so the
// next read is warm.
//
// SCOPE OF THE FS FALLBACK: memberships ONLY. Memberships are the single
// store category with a filesystem source (members/<pk>.cbor + campfire.cbor).
// Store-only categories — threshold_shares, pending_threshold_shares,
// campfire_epoch_secrets, campfire_invites, peer_endpoints, read_cursors,
// pending_messages — have NO filesystem source, so a miss there is
// authoritative and passes through untouched (embedded store.Store handles them
// verbatim). Messages rehydrate via SyncFilesystem elsewhere and are not touched
// here.
type LocalStorage struct {
	store.Store

	// selfPubkeyHex is the hex-encoded Ed25519 public key of THIS identity. It
	// is required to identify which on-disk member record is "me" during
	// rehydrate. When empty, GetMembership falls back to pure SQLite passthrough
	// (it cannot know which member to reconstruct), preserving the original
	// passthrough behavior for callers that do not opt in.
	selfPubkeyHex string

	// transportBaseDir is the filesystem transport root. When empty, it is
	// resolved lazily via fs.DefaultBaseDir() (CF_TRANSPORT_DIR / CF_HOME /
	// .cf/config.toml storage_root / ~/.campfire), per campfireagent-3f0.
	transportBaseDir string
}

// Compile-time assertion that *LocalStorage satisfies Storage.
var _ Storage = (*LocalStorage)(nil)

// Option configures a LocalStorage at construction time.
type Option func(*LocalStorage)

// WithSelfPubkeyHex sets the hex-encoded Ed25519 public key of this identity,
// enabling membership rehydrate (the fs fallback needs to know which on-disk
// member is "me"). Without it, GetMembership is pure SQLite passthrough.
func WithSelfPubkeyHex(pubkeyHex string) Option {
	return func(l *LocalStorage) { l.selfPubkeyHex = pubkeyHex }
}

// WithTransportBaseDir overrides the filesystem transport root used for
// rehydrate. When unset, fs.DefaultBaseDir() resolves it.
func WithTransportBaseDir(dir string) Option {
	return func(l *LocalStorage) { l.transportBaseDir = dir }
}

// NewLocalStorage wraps a SQLite-backed store.Store as a LocalStorage. Pass
// WithSelfPubkeyHex to enable membership rehydrate from the filesystem.
func NewLocalStorage(st store.Store, opts ...Option) *LocalStorage {
	l := &LocalStorage{Store: st}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Backend reports the local backend.
func (l *LocalStorage) Backend() Backend { return BackendLocal }

// GetMembership returns the membership for campfireID, treating the filesystem
// transport directory as the source of truth and SQLite as a cache.
//
// Flow:
//  1. Consult the embedded SQLite cache. On a hit, return it (warm path).
//  2. On a miss (nil), and only if a self-pubkey is configured, REHYDRATE from
//     the filesystem: resolve the campfire dir via the fs locator, read the
//     campfire state and the members/<selfPubkeyHex>.cbor file. If "me" is a
//     member on disk, reconstruct a fully-populated store.Membership (Role from
//     the member file, TransportDir = resolved campfire dir, JoinProtocol /
//     Threshold / Encrypted from the campfire state, TransportType "filesystem"),
//     write it back into the SQLite cache (idempotent), and return it.
//  3. If the filesystem has no state or no member file for "me", the miss is
//     authoritative: return nil.
//
// This generalizes the handleJoin alreadyOnDisk reconciliation
// (cmd/cf-mcp/main.go) so ALL gates (Send, Read, Members) benefit, not just
// join. The reconstructed Role lets checkRoleCanSend pass; the TransportDir lets
// sendFilesystem locate the campfire.
func (l *LocalStorage) GetMembership(campfireID string) (*store.Membership, error) {
	// 1. SQLite cache first.
	m, err := l.Store.GetMembership(campfireID)
	if err != nil {
		return nil, err
	}
	if m != nil {
		return m, nil
	}

	// 2. Cache miss. Without a self identity we cannot pick "me" out of the
	// on-disk member set, so passthrough (preserve original behavior).
	if l.selfPubkeyHex == "" {
		return nil, nil
	}

	rehydrated, err := l.rehydrateMembershipFromFS(campfireID)
	if err != nil {
		return nil, err
	}
	if rehydrated == nil {
		// 3. No filesystem source for this campfire/identity — authoritative miss.
		return nil, nil
	}

	// Warm the cache so the next call hits SQLite directly. AddMembership is a
	// plain INSERT against the campfire_id primary key, NOT an upsert — two
	// concurrent rehydrates of the same campfire (e.g. parallel Send/Read/Members
	// gates) both miss, both reconstruct, and both INSERT. The loser of that race
	// gets a UNIQUE-constraint error. That is a benign warm-race: the winner has
	// already written the identical row, so re-read and return it rather than
	// failing a legitimate membership query.
	if err := l.Store.AddMembership(*rehydrated); err != nil {
		if cached, reErr := l.Store.GetMembership(campfireID); reErr == nil && cached != nil {
			return cached, nil
		}
		return nil, fmt.Errorf("storage: warming membership cache: %w", err)
	}
	return rehydrated, nil
}

// rehydrateMembershipFromFS reconstructs a Membership for campfireID from the
// filesystem transport directory, or returns (nil, nil) if there is no on-disk
// state or no member file for this identity. It performs NO cache write — the
// caller owns warming. It hand-rolls no CBOR: it reuses the fs transport's
// ReadState and ListMembers.
func (l *LocalStorage) rehydrateMembershipFromFS(campfireID string) (*store.Membership, error) {
	baseDir := l.transportBaseDir
	if baseDir == "" {
		baseDir = fs.DefaultBaseDir()
	}
	tr := fs.New(baseDir)

	state, err := tr.ReadState(campfireID)
	if err != nil {
		// No campfire state on disk: not a filesystem-backed membership. This is
		// an expected miss, not a failure — the campfire simply isn't local.
		return nil, nil
	}

	members, err := tr.ListMembers(campfireID)
	if err != nil {
		// State exists but members dir is unreadable/absent: treat as miss.
		return nil, nil
	}

	var self *campfire.MemberRecord
	for i := range members {
		if fmt.Sprintf("%x", members[i].PublicKey) == l.selfPubkeyHex {
			self = &members[i]
			break
		}
	}
	if self == nil {
		// I am not a member on disk: authoritative miss.
		return nil, nil
	}

	// Reconstruct the role exactly as admission.AdmitMember would derive it: an
	// explicit on-disk role wins; otherwise an encrypted campfire makes us a
	// blind relay, and a plaintext campfire makes us full.
	role := self.Role
	if role == "" {
		if state.Encrypted {
			role = campfire.RoleBlindRelay
		} else {
			role = campfire.RoleFull
		}
	}

	return &store.Membership{
		CampfireID:    campfireID,
		TransportDir:  tr.CampfireDir(campfireID),
		TransportType: "filesystem",
		JoinProtocol:  state.JoinProtocol,
		Role:          role,
		JoinedAt:      self.JoinedAt,
		Threshold:     state.Threshold,
		Encrypted:     state.Encrypted,
	}, nil
}

