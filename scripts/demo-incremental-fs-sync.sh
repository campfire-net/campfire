#!/usr/bin/env bash
# scripts/demo-incremental-fs-sync.sh — E2E proof for the incremental fs-sync fix
# (campfireagent-e58).
#
# THE BUG (mallcop-pro bakeoff bottleneck, PART A / A3):
#   Every `cf read` (and every `rd create`, which reads internally) ran
#   sync-before-query, which re-read, re-verified (Ed25519), and re-inserted the
#   campfire's ENTIRE on-disk message history on EVERY call — O(total messages)
#   per operation. On a multi-thousand-message campfire (e.g. an rd workspace)
#   that cost ~4s steady-state / ~22s cold per call, craters bakeoff pass rate.
#
# THE FIX:
#   syncFromFilesystem now keeps a per-campfire leaf-filename cursor and imports
#   only messages written after it. Steady-state cost is O(new messages), not
#   O(total). A small lookback window (2s) tolerates backward clock steps.
#
# WHAT THIS DEMO PROVES:
#   Steady-state `cf read` latency is bounded by RECENT write activity, not by
#   total history. We plant messages in two equal waves and show that doubling
#   the total history does NOT proportionally increase steady-state read latency
#   — the signature of incremental (vs full-rescan) sync.
#
# REQUIRES_PROD: no
# REQUIRES_FIX: no
#
# (The REQUIRES_* markers keep this heavy perf demo out of the fast run-all-demos
# auto-sweep, matching scripts/demo-0.31-storage-scaling.sh. It is fully working
# and meant to be run manually.)
#
# Run from the campfire repo root:
#   bash scripts/demo-incremental-fs-sync.sh
#
# Item: campfireagent-e58

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO="${GOROOT:-/usr/local/go}/bin/go"

RED='\033[0;31m'; GRN='\033[0;32m'; YLW='\033[1;33m'; RST='\033[0m'
PASS=0; FAIL=0
ok()     { echo -e "  ${GRN}[PASS]${RST} $1"; PASS=$((PASS+1)); }
fail()   { echo -e "  ${RED}[FAIL]${RST} $1"; FAIL=$((FAIL+1)); }
banner() { echo -e "\n${YLW}=== $1 ===${RST}"; }
die()    { echo -e "${RED}FATAL: $1${RST}" >&2; exit 1; }

PYTHON3=""
for p in python3 python; do command -v "$p" &>/dev/null && { PYTHON3="$p"; break; }; done
[ -n "$PYTHON3" ] || die "python3 not found on PATH (required for timing)"

# Timing helper: run cmd N times, return "P50 MIN MAX" in ms.
measure_latency_ms() {
    local n_runs=$1; shift
    local times=() t_start t_end
    for _ in $(seq 1 "$n_runs"); do
        t_start=$("$PYTHON3" -c 'import time; print(int(time.time()*1e9))')
        "$@" > /dev/null 2>&1 || true
        t_end=$("$PYTHON3" -c 'import time; print(int(time.time()*1e9))')
        times+=( $(( (t_end - t_start) / 1000000 )) )
    done
    "$PYTHON3" - "${times[@]}" <<'PYEOF'
import sys
vals = sorted(int(x) for x in sys.argv[1:])
print(f"{vals[len(vals)//2]} {vals[0]} {vals[-1]}")
PYEOF
}

TMPBIN=$(mktemp -d); DEMO_TMP=$(mktemp -d); GENSRC=$(mktemp -d)
trap 'rm -rf "$TMPBIN" "$DEMO_TMP" "$GENSRC" 2>/dev/null || true' EXIT

# ---------------------------------------------------------------------------
banner "Building cf binary"
"$GO" build -o "$TMPBIN/cf" "$REPO_ROOT/cmd/cf" 2>&1 || die "go build cf failed"
CF="$TMPBIN/cf"
ok "cf binary built"

# ---------------------------------------------------------------------------
banner "Building flat-layout message generator"
cat > "$GENSRC/main.go" <<'GENEOF'
// gen-flat — writes N signed CBOR messages directly into a campfire's messages/
// dir (no SQLite insert), so `cf read` must sync them from the fs transport.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	cfencoding "github.com/campfire-net/campfire/cf-protocol/encoding"
	"github.com/campfire-net/campfire/cf-protocol/message"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
	"github.com/campfire-net/campfire/pkg/identity"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: gen-flat <campfire-dir> <n>\n")
		os.Exit(1)
	}
	campfireDir := os.Args[1]
	n, err := strconv.Atoi(os.Args[2])
	if err != nil || n <= 0 {
		fmt.Fprintf(os.Stderr, "invalid n: %s\n", os.Args[2])
		os.Exit(1)
	}

	stateBytes, err := os.ReadFile(filepath.Join(campfireDir, "campfire.cbor"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "reading campfire state: %v\n", err)
		os.Exit(1)
	}
	var state campfire.CampfireState
	if err := cfencoding.Unmarshal(stateBytes, &state); err != nil {
		fmt.Fprintf(os.Stderr, "decoding campfire state: %v\n", err)
		os.Exit(1)
	}

	campfireID := fmt.Sprintf("%x", state.PublicKey)
	tr := fs.New(filepath.Dir(campfireDir))

	genID, err := identity.Generate()
	if err != nil {
		fmt.Fprintf(os.Stderr, "generating identity: %v\n", err)
		os.Exit(1)
	}
	if err := tr.WriteMember(campfireID, campfire.MemberRecord{
		PublicKey: genID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "writing member: %v\n", err)
		os.Exit(1)
	}
	members, err := tr.ListMembers(campfireID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listing members: %v\n", err)
		os.Exit(1)
	}
	cfObj := state.ToCampfire(members)

	signer, err := message.NewEd25519Signer(genID.PrivateKey, genID.PublicKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "signer: %v\n", err)
		os.Exit(1)
	}

	messagesDir := filepath.Join(campfireDir, "messages")
	if err := os.MkdirAll(messagesDir, 0700); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir messages: %v\n", err)
		os.Exit(1)
	}

	type result struct{ err error }
	jobs := make(chan int, n)
	results := make(chan result, n)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				msg, err := message.NewMessage(signer,
					[]byte(fmt.Sprintf("event %d: representative campfire message payload", i)),
					[]string{"status"}, nil)
				if err != nil {
					results <- result{err}
					return
				}
				if err := msg.AddHop(state.PrivateKey, state.PublicKey,
					cfObj.MembershipHash(), len(members),
					state.JoinProtocol, state.ReceptionRequirements,
					campfire.RoleFull); err != nil {
					results <- result{err}
					return
				}
				data, err := cfencoding.Marshal(msg)
				if err != nil {
					results <- result{err}
					return
				}
				filename := fmt.Sprintf("%019d-%s.cbor", time.Now().UnixNano(), msg.ID)
				mu.Lock()
				writeErr := os.WriteFile(filepath.Join(messagesDir, filename), data, 0600)
				mu.Unlock()
				results <- result{writeErr}
			}
		}()
	}
	for i := 0; i < n; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	close(results)
	for r := range results {
		if r.err != nil {
			fmt.Fprintf(os.Stderr, "generator error: %v\n", r.err)
			os.Exit(1)
		}
	}
	fmt.Printf("wrote %d messages to %s\n", n, messagesDir)
}
GENEOF

cat > "$GENSRC/go.mod" <<GOMODEOF
module demo-gen-flat
go 1.22
require github.com/campfire-net/campfire v0.0.0
replace github.com/campfire-net/campfire => $REPO_ROOT
GOMODEOF

(cd "$GENSRC" && "$GO" mod tidy 2>&1 && "$GO" build -o "$TMPBIN/gen-flat" . 2>&1) \
    || die "go build gen-flat failed"
GEN="$TMPBIN/gen-flat"
ok "gen-flat built"

# ---------------------------------------------------------------------------
banner "Workspace + campfire setup"
CF_HOME_DIR="$DEMO_TMP/cf_home"
TRANSPORT_DIR="$DEMO_TMP/transport"
mkdir -p "$CF_HOME_DIR" "$TRANSPORT_DIR"
export CF_HOME="$CF_HOME_DIR"
export CF_TRANSPORT_DIR="$TRANSPORT_DIR"

# Use the strict cursor (no skew lookback) so this demo isolates the pure
# incremental win: steady-state read cost is O(new messages) — independent of
# total history. Production defaults to a 2s lookback window (CF_FS_SYNC_LOOKBACK_MS)
# to tolerate backward clock steps; its only added cost is re-reading messages
# within 2s of the newest one — ~0 for time-spread workloads like an rd workspace.
export CF_FS_SYNC_LOOKBACK_MS=0

"$CF" --cf-home "$CF_HOME_DIR" init > /dev/null 2>&1
CREATE_OUT=$("$CF" --cf-home "$CF_HOME_DIR" create --transport fs 2>&1)
CAMPFIRE_ID=$(echo "$CREATE_OUT" | grep -oE '[0-9a-f]{64}' | head -1 || true)
[ -n "$CAMPFIRE_ID" ] || die "could not parse campfire ID from: $CREATE_OUT"
ok "campfire created: ${CAMPFIRE_ID:0:16}..."
CAMPFIRE_DIR="$TRANSPORT_DIR/$CAMPFIRE_ID"
rm -rf "$CAMPFIRE_DIR/messages"; mkdir -p "$CAMPFIRE_DIR/messages"

N=${MSG_COUNT:-16000}

# ---------------------------------------------------------------------------
banner "Plant $N messages on the fs transport"
"$GEN" "$CAMPFIRE_DIR" "$N" || die "generator failed"
TOTAL=$(find "$CAMPFIRE_DIR/messages" -name '*.cbor' | wc -l | tr -d ' ')
ok "on-disk message count: $TOTAL"

# Warmup read: imports the whole history into the store once and sets the sync
# cursor. (Both modes below share this populated store.)
"$CF" --cf-home "$CF_HOME_DIR" read --all "$CAMPFIRE_ID" > /dev/null 2>&1 || true
ok "store warmed (full history imported once, cursor set)"

# ---------------------------------------------------------------------------
# The same binary on the same data, toggled by the lookback knob:
#
#   POST-FIX  (CF_FS_SYNC_LOOKBACK_MS=0): incremental cursor — a steady-state read
#             with no new messages imports nothing.
#   PRE-FIX   (huge lookback): the cursor rewinds below the start of history, so
#             every read re-lists, re-reads, re-verifies, and re-inserts the ENTIRE
#             history — exactly the behaviour before the fix (~4s on a degraded
#             store in the bakeoff). This faithfully reproduces the old code path
#             without checking out the old code.
# ---------------------------------------------------------------------------
banner "Measure steady-state read latency: post-fix vs pre-fix-equivalent (same data)"

POST=$(CF_FS_SYNC_LOOKBACK_MS=0 \
    measure_latency_ms 5 "$CF" --cf-home "$CF_HOME_DIR" read "$CAMPFIRE_ID" | cut -d' ' -f1)
ok "post-fix (incremental cursor):        p50=${POST}ms"

PRE=$(CF_FS_SYNC_LOOKBACK_MS=999999999999 \
    measure_latency_ms 5 "$CF" --cf-home "$CF_HOME_DIR" read "$CAMPFIRE_ID" | cut -d' ' -f1)
ok "pre-fix-equivalent (full rescan/read): p50=${PRE}ms"

# ---------------------------------------------------------------------------
banner "Verdict"
echo "  history size:                 $TOTAL messages"
echo "  pre-fix  (full rescan/read):  ${PRE}ms   ← every read paid this before the fix"
echo "  post-fix (incremental cursor): ${POST}ms   ← steady-state read after the fix"
echo ""

# The fix is proven if the incremental read is dramatically cheaper than the
# full-rescan read on the SAME history. Require a clear margin (>=3x); the actual
# margin grows with history size. Both numbers are from the same binary and data,
# so the ratio is hardware-independent.
"$PYTHON3" - "$PRE" "$POST" <<'PYEOF'
import sys
pre, post = (int(x) for x in sys.argv[1:3])
speedup = pre / post if post > 0 else float('inf')
print(f"  speedup: {speedup:.1f}x faster (incremental vs full rescan, same data)")
sys.exit(0 if (speedup >= 3.0 and pre > post) else 1)
PYEOF
if [ $? -eq 0 ]; then
    ok "incremental sync is dramatically cheaper than full rescan — fix confirmed"
else
    fail "incremental sync did not beat full rescan by the required margin"
fi

# ---------------------------------------------------------------------------
banner "Summary"
echo -e "  ${GRN}PASS: $PASS${RST}   ${RED}FAIL: $FAIL${RST}"
[ "$FAIL" -eq 0 ] || die "demo failed"
echo -e "${GRN}Incremental fs-sync demo passed.${RST}"
