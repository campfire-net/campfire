#!/usr/bin/env bash
# scripts/check-floor.sh
#
# Verifies that the cf-protocol floor version declared in
# cf-conventions/floor.txt is consistent with the actual go.mod constraint
# (or the module version in single-module mode).
#
# Floor file: cf-conventions/floor.txt
#   One line: "cf-protocol v<SEMVER>" (e.g. "cf-protocol v1.0.0")
#
# In the current single-module layout (pre-split), the "floor" is verified
# against the module version tag present in go.mod or the version reported by
# `go list -m`. When the repo splits into separate go modules, this script
# will verify cf-conventions/go.mod's `require github.com/campfire-net/cf-protocol`
# line instead.
#
# Exit codes:
#   0  — floor is met; declared version ≤ available version
#   1  — floor violation; declared version > available version, OR
#          floor.txt is missing or malformed
#
# Usage:
#   bash scripts/check-floor.sh
#   bash scripts/check-floor.sh --floor v1.0.0     # override floor (for tests)
#   bash scripts/check-floor.sh --available v1.2.0 # override available (for tests)
#
# Design reference: docs/design/cross-module-versioning.md §4.5

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FLOOR_FILE="$REPO_ROOT/cf-conventions/floor.txt"

# Parse optional overrides (used by test harness)
FLOOR_OVERRIDE=""
AVAILABLE_OVERRIDE=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --floor)      FLOOR_OVERRIDE="$2";     shift 2 ;;
    --available)  AVAILABLE_OVERRIDE="$2"; shift 2 ;;
    *) echo "Unknown argument: $1" >&2; exit 1 ;;
  esac
done

# ── Resolve floor version ───────────────────────────────────────────────────

if [[ -n "$FLOOR_OVERRIDE" ]]; then
  FLOOR_VERSION="$FLOOR_OVERRIDE"
else
  if [[ ! -f "$FLOOR_FILE" ]]; then
    echo "ERROR: floor file not found: $FLOOR_FILE" >&2
    echo "Create cf-conventions/floor.txt with one line: cf-protocol v<SEMVER>" >&2
    exit 1
  fi

  # Expected format: "cf-protocol v1.0.0"
  FLOOR_LINE=$(grep -v '^#' "$FLOOR_FILE" | grep -v '^[[:space:]]*$' | head -1)
  if [[ -z "$FLOOR_LINE" ]]; then
    echo "ERROR: floor.txt is empty or contains only comments." >&2
    exit 1
  fi

  FLOOR_VERSION=$(echo "$FLOOR_LINE" | awk '{print $2}')
  if [[ ! "$FLOOR_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "ERROR: malformed floor version '$FLOOR_VERSION' in $FLOOR_FILE" >&2
    echo "Expected format: cf-protocol v<MAJOR>.<MINOR>.<PATCH>" >&2
    exit 1
  fi
fi

# ── Resolve available version ───────────────────────────────────────────────

if [[ -n "$AVAILABLE_OVERRIDE" ]]; then
  AVAILABLE_VERSION="$AVAILABLE_OVERRIDE"
else
  # In single-module mode: use the most recent git tag that matches v<semver>.
  # In multi-module mode (post-split): would use `go list -m github.com/campfire-net/cf-protocol`.
  if command -v git &>/dev/null && git -C "$REPO_ROOT" rev-parse --is-inside-work-tree &>/dev/null; then
    AVAILABLE_VERSION=$(git -C "$REPO_ROOT" tag --list 'v[0-9]*.[0-9]*.[0-9]*' \
      | sort -t. -k1,1V -k2,2n -k3,3n 2>/dev/null | tail -1)
    # Strip leading "v" for comparison below if needed (keep it for display)
  fi

  # Fallback: read from CHANGELOG.md first entry if no tags yet
  if [[ -z "${AVAILABLE_VERSION:-}" ]]; then
    if [[ -f "$REPO_ROOT/CHANGELOG.md" ]]; then
      AVAILABLE_VERSION=$(grep -m1 '^## v[0-9]' "$REPO_ROOT/CHANGELOG.md" \
        | sed 's/^## \(v[0-9][^ ]*\).*/\1/')
    fi
  fi

  if [[ -z "${AVAILABLE_VERSION:-}" ]]; then
    echo "ERROR: cannot determine available cf-protocol version." >&2
    echo "Ensure the repo has at least one vX.Y.Z git tag, or pass --available v<VERSION>." >&2
    exit 1
  fi
fi

# ── Version comparison (semver X.Y.Z core, pre-release suffix stripped) ────
#
# Pre-release versions (e.g. v0.30.0-rc.1) and build metadata (+sha) are
# treated as the underlying X.Y.Z for floor-met comparison. Per semver, a
# pre-release of X.Y.Z is ordered below the X.Y.Z release, but for the
# purpose of "available version satisfies floor v<MAJOR>.<MINOR>.<PATCH>"
# we accept v0.30.0-rc.1 as satisfying a floor of v0.19.0. Tighter
# semantics (rejecting pre-releases against released floors of the same
# X.Y.Z) can be added when it becomes a real concern.

# Strip leading 'v' and any pre-release / build suffix for arithmetic
floor_clean="${FLOOR_VERSION#v}"
avail_clean="${AVAILABLE_VERSION#v}"
floor_clean="${floor_clean%%[-+]*}"
avail_clean="${avail_clean%%[-+]*}"

IFS='.' read -r f_major f_minor f_patch <<< "$floor_clean"
IFS='.' read -r a_major a_minor a_patch <<< "$avail_clean"

# Ensure all parts are numeric
for part in "$f_major" "$f_minor" "$f_patch" "$a_major" "$a_minor" "$a_patch"; do
  if [[ ! "$part" =~ ^[0-9]+$ ]]; then
    echo "ERROR: non-numeric version component '$part' in floor='$FLOOR_VERSION' or available='$AVAILABLE_VERSION'" >&2
    exit 1
  fi
done

floor_met=false
if (( a_major > f_major )); then
  floor_met=true
elif (( a_major == f_major )); then
  if (( a_minor > f_minor )); then
    floor_met=true
  elif (( a_minor == f_minor )); then
    if (( a_patch >= f_patch )); then
      floor_met=true
    fi
  fi
fi

# ── Report ──────────────────────────────────────────────────────────────────

echo "check-floor: cf-protocol floor check"
echo "  Declared floor:     cf-protocol ${FLOOR_VERSION}"
echo "  Available version:  cf-protocol ${AVAILABLE_VERSION}"

if $floor_met; then
  echo "  Result: PASS — available version satisfies the declared floor."
  exit 0
else
  echo "  Result: FAIL — available version ${AVAILABLE_VERSION} is below the declared floor ${FLOOR_VERSION}." >&2
  echo "" >&2
  echo "  Fix: either bump the available cf-protocol to >= ${FLOOR_VERSION}," >&2
  echo "       or lower the floor in cf-conventions/floor.txt if the floor" >&2
  echo "       was declared too aggressively." >&2
  exit 1
fi
