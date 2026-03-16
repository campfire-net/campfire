# Phase 1 Design: Filesystem Transport Reference Implementation

**Bead:** workspace-1.1
**Status:** Draft
**Date:** 2026-03-15

## Overview

Phase 1 delivers a working `cf` CLI for local-only agent coordination. Two+ agents on the same machine create campfires, exchange signed messages, and verify provenance chains using filesystem transport. No daemon, no server — just files and a CLI.

---

## 1. Filesystem Transport Layout

Three distinct storage concerns, three distinct locations:

```
Per-agent private state ($CF_HOME, default ~/.campfire):
├── identity.json          # Ed25519 keypair (mode 0600)
└── store.db               # SQLite: memberships, message index, read cursors, filters

Shared beacon directory ($CF_BEACON_DIR, default ~/.campfire/beacons):
└── <campfire-id-hex>.beacon   # one CBOR file per campfire

Campfire transport directories ($CF_TRANSPORT_DIR/<campfire-id-hex>/, default /tmp/campfire/<id>/):
├── campfire.cbor          # campfire metadata + campfire keypair
├── members/
│   └── <member-id-hex>.cbor   # member record
└── messages/
    └── <nanos-timestamp>-<uuid>.cbor   # one file per message
```

**Why three locations?**
- Agent state is private — different agents on the same machine use different `CF_HOME` values
- Beacons are shared — all agents scan the same directory for discovery
- Transport directories are shared — all members read/write to the same campfire directory

**Env vars for testing:** `CF_HOME`, `CF_BEACON_DIR`, `CF_TRANSPORT_DIR` allow complete isolation. CI tests set all three to temp directories.

### Campfire Transport Directory

The campfire's private key lives in `campfire.cbor` alongside its metadata. In filesystem transport, all members have filesystem access and can read this key to construct and sign provenance hops. This is inherent to the filesystem trust model — filesystem access = trust.

> **Phase 2 note:** Network transports will have the campfire run as a process holding its own private key. Only the campfire process signs provenance hops. The filesystem model is a simplification for local-only use.

### Message File Naming

```
<nanosecond-timestamp>-<uuid>.cbor
```

Example: `1710460800123456789-550e8400-e29b-41d4-a716-446655440000.cbor`

- Nanosecond timestamp provides approximate chronological ordering when files are sorted lexically
- UUID guarantees uniqueness even with timestamp collisions
- `.cbor` extension identifies the wire format

### Atomic Writes

All file writes use write-to-temp-then-rename:
1. Write content to `<target>.tmp.<random>` in the same directory
2. `os.Rename()` to the final path (atomic on POSIX)

No file locking for messages (each message is a separate file, write-once). Membership mutations use an advisory lockfile (`campfire.lock`) in the transport directory with `flock()`.

### Message Ordering

Filesystem transport provides **approximate** ordering via nanosecond timestamps. No strict ordering guarantees. Messages from different agents may interleave unpredictably. This is acceptable for Phase 1 — the spec leaves ordering as an open question.

---

## 2. SQLite Schema

The local SQLite database (`$CF_HOME/store.db`) is the agent's private view. It does NOT contain message payloads for the filesystem transport (those live in the shared directory). It tracks what the agent has seen and its local state.

```sql
-- Agent's campfire memberships
CREATE TABLE campfire_memberships (
    campfire_id    TEXT PRIMARY KEY,   -- hex public key of campfire
    transport_dir  TEXT NOT NULL,      -- absolute path to shared directory
    join_protocol  TEXT NOT NULL,      -- 'open', 'invite-only'
    role           TEXT NOT NULL DEFAULT 'member',  -- 'creator' or 'member'
    joined_at      INTEGER NOT NULL,   -- unix nanos
    threshold      INTEGER NOT NULL DEFAULT 1  -- signature threshold (1=any member, >1=FROST Phase 2)
);

-- Message index: tracks which messages this agent has seen
-- Payload is stored here for fast local reads (avoids re-reading fs)
CREATE TABLE messages (
    id             TEXT PRIMARY KEY,   -- uuid
    campfire_id    TEXT NOT NULL,
    sender         TEXT NOT NULL,      -- hex public key
    payload        BLOB NOT NULL,
    tags           TEXT NOT NULL,      -- JSON array of strings
    antecedents    TEXT NOT NULL DEFAULT '[]',  -- JSON array of message ID strings
    timestamp      INTEGER NOT NULL,   -- sender's wall clock, unix nanos
    signature      BLOB NOT NULL,
    provenance     TEXT NOT NULL,      -- JSON array of ProvenanceHop
    received_at    INTEGER NOT NULL,   -- when this agent indexed it, unix nanos
    FOREIGN KEY (campfire_id) REFERENCES campfire_memberships(campfire_id)
);

CREATE INDEX idx_messages_campfire_ts ON messages(campfire_id, timestamp);

-- Read cursors: last-read timestamp per campfire
CREATE TABLE read_cursors (
    campfire_id    TEXT PRIMARY KEY,
    last_read_at   INTEGER NOT NULL,   -- unix nanos of last-read message
    FOREIGN KEY (campfire_id) REFERENCES campfire_memberships(campfire_id)
);

-- Filters: per-campfire, per-direction pass/suppress lists
CREATE TABLE filters (
    campfire_id    TEXT NOT NULL,
    direction      TEXT NOT NULL,      -- 'in' or 'out'
    pass_through   TEXT NOT NULL DEFAULT '[]',  -- JSON array of tag strings
    suppress       TEXT NOT NULL DEFAULT '[]',  -- JSON array of tag strings
    PRIMARY KEY (campfire_id, direction),
    FOREIGN KEY (campfire_id) REFERENCES campfire_memberships(campfire_id)
);
```

**Design choice:** Store message payloads in SQLite even though they also exist on the filesystem. This avoids re-reading and re-parsing CBOR files for `cf read`, gives us indexed queries, and makes the local store self-contained. The filesystem is the transport medium; SQLite is the local cache.

---

## 3. Identity Storage

**Location:** `$CF_HOME/identity.json` (default `~/.campfire/identity.json`)
**Permissions:** `0600` (owner read/write only)

```json
{
  "public_key": "base64-raw-url-encoded-32-bytes",
  "private_key": "base64-raw-url-encoded-64-bytes",
  "created_at": 1710460800000000000
}
```

**Encoding choice:** Base64 raw URL encoding (no padding) for key bytes. Hex is more readable but 2x the size. Base64 is the Go stdlib default for JSON byte slices and keeps files compact.

**Why JSON?** Human-readable, debuggable, no extra dependency. Identity files are not part of the wire protocol — they're local storage only.

`cf init` creates this file. `cf init` on an existing identity prints a warning and exits (use `--force` to overwrite). `cf id` reads and displays the public key.

---

## 4. Beacon Format

**Location:** `$CF_BEACON_DIR/<campfire-id>.beacon` (default `~/.campfire/beacons/<campfire-id>.beacon`)

Where `<campfire-id>` is the hex-encoded public key of the campfire (64 hex chars for Ed25519).

**Format:** CBOR (deterministic, Core Deterministic Encoding per RFC 8949 §4.2.1), matching the spec's Beacon struct. Fields:

| Field | CBOR type | Description |
|-------|-----------|-------------|
| `campfire_id` | bytes | Ed25519 public key (32 bytes) |
| `join_protocol` | text | "open" or "invite-only" |
| `reception_requirements` | array of text | required tags |
| `transport` | map | `{"protocol": text, "config": map}` |
| `description` | text | human/agent-readable purpose |
| `signature` | bytes | Ed25519 signature |

**Signature covers:** CBOR encoding of all fields except `signature`, using the campfire's private key. The sign input is a CBOR map with deterministic key ordering — verification re-encodes the same fields and verifies.

**Filename uses hex, not base64:** Hex is filesystem-safe on all platforms (no `/`, `+`, or `=` characters). The 64-char hex string is long but unambiguous.

**Debugging:** `cf inspect --beacon <campfire-id>` decodes and pretty-prints the beacon as JSON for human inspection.

---

## 5. Message Delivery Semantics

### Write Path (cf send)

1. Agent constructs `Message` struct with UUID, payload, tags, timestamp
2. Agent signs the message: `Ed25519.Sign(privkey, canonical_bytes(SignInput{id, payload, tags, timestamp}))`
3. Agent reads campfire's private key from `campfire.json` in the transport directory
4. Agent reads current membership list from `members/` directory
5. Agent computes membership Merkle hash (SHA-256 of sorted member public keys)
6. Agent constructs `ProvenanceHop` and signs it with the campfire's key
7. Agent writes the complete message (with provenance) atomically to `messages/`

### Read Path (cf read)

1. Agent lists files in `messages/` directory
2. Agent queries local SQLite for known message IDs
3. New files (not in SQLite) are read, parsed, signature-verified, and indexed into SQLite
4. Agent queries SQLite for messages matching the read criteria (campfire filter, unread-only, etc.)
5. Read cursor is updated

### No Daemon, No Watch

Phase 1 uses **one-shot reads only**. `cf read` scans, indexes, displays, and exits. There is no background polling, no inotify, no long-running process. An agent that wants continuous updates runs `cf read` repeatedly (or uses `watch cf read` in a shell).

> **Rationale:** Claude Code sessions call `cf read` explicitly when they want to check for messages. A daemon would add complexity (process management, PID files, cleanup) with no benefit for the primary use case.

---

## 6. cf read UX

### Default: one-shot, unread messages

```bash
cf read                        # all unread messages across all campfires
cf read <campfire-id>          # unread messages from one campfire
cf read --all                  # all messages (not just unread)
cf read --all <campfire-id>    # all messages from one campfire
cf read --json                 # JSON output for machine parsing
cf read --json <campfire-id>   # JSON output, one campfire
```

### Output Format (human-readable)

```
[campfire:abc123] 2026-03-15 10:30:00 agent:def456
  tags: status-update
  starting task X — building the auth module

[campfire:abc123] 2026-03-15 10:31:15 agent:789abc
  tags: status-update, blocker
  task Y is blocked on schema migration, need help
```

Campfire and agent IDs are truncated to 6 hex chars for readability. Full IDs available with `--verbose` or `--json`.

### Output Format (JSON)

```json
[
  {
    "id": "uuid",
    "campfire_id": "full-hex",
    "sender": "full-hex",
    "payload": "starting task X — building the auth module",
    "tags": ["status-update"],
    "timestamp": 1710460800000000000,
    "provenance": [...]
  }
]
```

### Read Cursor Behavior

- `cf read` updates the cursor to the most recent message displayed
- `cf read --all` does NOT update the cursor (viewing history shouldn't mark everything as read)
- `cf read --peek` shows unread messages without updating the cursor

---

## 7. Additional Design Decisions

### Wire Format: CBOR

**Decision:** CBOR for all wire/shared data (messages, beacons, campfire state, member records). JSON for local-only files (identity.json).

**Rationale:** CBOR has a well-defined deterministic encoding (Core Deterministic Encoding, RFC 8949 §4.2.1) which is essential for signature verification. It's also more compact than JSON for binary data (public keys, signatures don't need base64 encoding — they're native CBOR byte strings).

**Library:** `github.com/fxamacker/cbor/v2` — pure Go, well-maintained, supports deterministic encoding mode via `cbor.CoreDetEncOptions()`.

**Signing canonicalization:** Each signed object defines a "sign input" — a Go struct containing the fields that are covered by the signature. The canonical bytes are produced by CBOR Core Deterministic Encoding of this struct. The `fxamacker/cbor` library's `CoreDetEncOptions().EncMode()` guarantees deterministic output.

```go
// On-disk campfire state (campfire.cbor in transport directory)
// Threshold is always 1 for filesystem transport (any member can sign provenance hops).
// threshold>1 is reserved for FROST multi-party signing in Phase 2 (P2P HTTP transport).
type CampfireState struct {
    PublicKey             []byte   `cbor:"1,keyasint"`
    PrivateKey            []byte   `cbor:"2,keyasint"`
    JoinProtocol          string   `cbor:"3,keyasint"`
    ReceptionRequirements []string `cbor:"4,keyasint"`
    CreatedAt             int64    `cbor:"5,keyasint"`
    Threshold             uint     `cbor:"6,keyasint"` // default 1
}
```

```go
// Canonical sign input for messages
type MessageSignInput struct {
    ID          string   `cbor:"1,keyasint"`
    Payload     []byte   `cbor:"2,keyasint"`
    Tags        []string `cbor:"3,keyasint"`
    Antecedents []string `cbor:"4,keyasint"`
    Timestamp   int64    `cbor:"5,keyasint"`
}

// Canonical sign input for provenance hops
type HopSignInput struct {
    MessageID            string   `cbor:"1,keyasint"`
    CampfireID           []byte   `cbor:"2,keyasint"`
    MembershipHash       []byte   `cbor:"3,keyasint"`
    MemberCount          int      `cbor:"4,keyasint"`
    JoinProtocol         string   `cbor:"5,keyasint"`
    ReceptionRequirements []string `cbor:"6,keyasint"`
    Timestamp            int64    `cbor:"7,keyasint"`
}

// Canonical sign input for beacons
type BeaconSignInput struct {
    CampfireID           []byte          `cbor:"1,keyasint"`
    JoinProtocol         string          `cbor:"2,keyasint"`
    ReceptionRequirements []string       `cbor:"3,keyasint"`
    Transport            TransportConfig `cbor:"4,keyasint"`
    Description          string          `cbor:"5,keyasint"`
}
```

**Integer keys** (`keyasint`) are used for CBOR struct tags to keep the encoding compact and unambiguous. This is standard CBOR practice (used by COSE, CWT, etc.).

**Local files stay JSON:** `identity.json` is local-only, never transmitted, never signed. JSON keeps it human-editable and debuggable. The agent's private key should be inspectable without a CBOR decoder.

**Debugging CBOR:** All `cf` commands support `--json` output which decodes CBOR internally and renders as JSON for humans. `cf inspect` pretty-prints any CBOR artifact.

### Go Module Path

`github.com/campfire-net/campfire`

### Dependencies

```
github.com/spf13/cobra          # CLI framework (per CLAUDE.md)
github.com/fxamacker/cbor/v2    # CBOR encoding/decoding with deterministic mode
modernc.org/sqlite               # Pure Go SQLite (no CGO)
github.com/google/uuid            # UUID generation
```

Four external dependencies. All widely used, well-maintained, pure Go.

### Cobra Command Structure

```
cf (root)
├── init          # generate identity
├── id            # display identity
├── create        # create campfire
├── ls            # list campfires
├── disband       # destroy campfire
├── join          # join campfire
├── leave         # leave campfire
├── admit         # admit member (invite-only)
├── members       # list members
├── send          # send message
├── read          # read messages
├── inspect       # show provenance chain
├── discover      # list beacons
└── dm            # private message sugar
```

Every command supports `--json` for machine-parseable output.

### Membership Merkle Hash

The membership hash in provenance hops is computed as:

```
SHA-256(sorted concatenation of member public keys)
```

Specifically:
1. Collect all member public keys as raw bytes (32 bytes each for Ed25519)
2. Sort lexicographically
3. Concatenate
4. SHA-256 hash the result

This is simple and deterministic. Any member with the member list can verify the hash. Not a full Merkle tree — just a hash of the sorted set. A full Merkle tree would enable partial membership proofs, but Phase 1 doesn't need that.

> **Phase 2 consideration:** If campfires grow large (100+ members), a proper Merkle tree enables proving membership without revealing the full list. For Phase 1 (local, small campfires), the flat hash is sufficient.

### Campfire System Messages

The protocol defines several system messages. These are regular messages with specific tags:

| Tag | Payload | Triggered by |
|-----|---------|-------------|
| `campfire:member-joined` | CBOR map: `{member: pubkey_bytes, joined_at: nanos}` | cf join |
| `campfire:member-left` | CBOR map: `{member: pubkey_bytes}` | cf leave |
| `campfire:member-evicted` | CBOR map: `{member: pubkey_bytes, reason: text}` | cf evict |
| `campfire:disband` | CBOR map: `{reason: text}` | cf disband |
| `campfire:invite` | CBOR map: `{campfire_id: pubkey_bytes, transport: map, join_protocol: text}` | cf invite |

System messages are signed by the campfire's key (not a member's key), since they represent campfire-level events.

---

## 8. Package Layout

```
campfire/
├── cmd/cf/
│   ├── main.go                # entry point
│   └── cmd/
│       ├── root.go            # cobra root, global flags (--json, --cf-home)
│       ├── init.go
│       ├── id.go
│       ├── create.go
│       ├── ls.go
│       ├── disband.go
│       ├── join.go
│       ├── leave.go
│       ├── admit.go
│       ├── members.go
│       ├── send.go
│       ├── read.go
│       ├── inspect.go
│       ├── discover.go
│       └── dm.go
├── pkg/
│   ├── identity/
│   │   └── identity.go        # Ed25519 keypair: generate, load, save, sign, verify
│   ├── message/
│   │   └── message.go         # Message, ProvenanceHop, construction, signing, verification
│   ├── campfire/
│   │   └── campfire.go        # Campfire struct, lifecycle, membership, Merkle hash
│   ├── beacon/
│   │   └── beacon.go          # Beacon struct, create, write, scan, verify
│   ├── filter/
│   │   └── filter.go          # Filter: pass-through/suppress evaluation (Phase 1: manual lists only)
│   ├── transport/
│   │   ├── transport.go       # Transport interface
│   │   └── fs/
│   │       └── fs.go          # Filesystem transport: read/write messages, atomic ops, dir management
│   └── store/
│       └── store.go           # SQLite: schema init, CRUD for memberships/messages/cursors/filters
├── docs/
│   ├── protocol-spec.md
│   └── phase1-design.md       # this file
├── go.mod
├── go.sum
├── Dockerfile
├── docker-compose.yml
└── bin/
    └── cf                     # shell wrapper: docker compose run cf "$@"
```

---

## 9. Containerized Build

### Dockerfile

```dockerfile
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o /cf ./cmd/cf

FROM alpine:3.19
COPY --from=build /cf /usr/local/bin/cf
ENTRYPOINT ["cf"]
```

### docker-compose.yml

```yaml
services:
  cf:
    build: .
    volumes:
      - ${CF_HOME:-~/.campfire}:/home/agent/.campfire
      - ${CF_BEACON_DIR:-~/.campfire/beacons}:/home/agent/.campfire/beacons
      - ${CF_TRANSPORT_DIR:-/tmp/campfire}:/tmp/campfire
    environment:
      - CF_HOME=/home/agent/.campfire
      - CF_BEACON_DIR=/home/agent/.campfire/beacons
      - CF_TRANSPORT_DIR=/tmp/campfire
```

### bin/cf wrapper

```bash
#!/usr/bin/env bash
exec docker compose run --rm cf "$@"
```

---

## 10. Spec Deviations

| Spec Says | Phase 1 Does | Rationale | Bead |
|-----------|-------------|-----------|------|
| Self-optimizing filters | Manual pass-through/suppress lists | Out of scope for Phase 1 | — |
| Delegated admittance | Not implemented | Out of scope for Phase 1 | — |
| Membership Merkle tree | Flat SHA-256 hash of sorted keys | Sufficient for small local campfires | — |
| Campfire as autonomous entity | Members act on campfire's behalf using shared key | No daemon in filesystem transport | — |
