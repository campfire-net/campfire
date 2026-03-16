#!/usr/bin/env python3
"""topology-analysis.py — quantitative analysis of moltbook emergence test results.

Usage: python3 topology-analysis.py /tmp/campfire-emergence/

Reads all agent stores using the cf binary (--json output), computes emergence
metrics, writes logs/emergence-report.json, and prints a human-readable summary.
"""

import json
import os
import re
import subprocess
import sys
from collections import defaultdict
from pathlib import Path


# ---------------------------------------------------------------------------
# cf binary helpers
# ---------------------------------------------------------------------------

def find_cf_binary():
    """Locate the cf binary: prefer project build, fall back to PATH."""
    candidates = [
        Path(__file__).parent.parent / "cf",
        Path("/usr/local/bin/cf"),
    ]
    for c in candidates:
        if c.exists() and os.access(str(c), os.X_OK):
            return str(c)
    # Try PATH
    try:
        result = subprocess.run(["which", "cf"], capture_output=True, text=True, timeout=5)
        if result.returncode == 0:
            return result.stdout.strip()
    except Exception:
        pass
    return "cf"  # hope it's on PATH


CF_BINARY = find_cf_binary()


def cf_run(args, cf_home=None, transport_dir=None, beacon_dir=None, timeout=15):
    """Run a cf command, returning (stdout, stderr, returncode).

    Sets CF_HOME, CF_TRANSPORT_DIR, and CF_BEACON_DIR from arguments if provided.
    Always appends --json.
    """
    env = os.environ.copy()
    if cf_home:
        env["CF_HOME"] = str(cf_home)
    if transport_dir:
        env["CF_TRANSPORT_DIR"] = str(transport_dir)
    if beacon_dir:
        env["CF_BEACON_DIR"] = str(beacon_dir)

    cmd = [CF_BINARY] + args + ["--json"]
    try:
        result = subprocess.run(
            cmd,
            capture_output=True,
            text=True,
            env=env,
            timeout=timeout,
        )
        return result.stdout, result.stderr, result.returncode
    except subprocess.TimeoutExpired:
        return "", "timeout", -1
    except Exception as e:
        return "", str(e), -1


def cf_ls(cf_home, transport_dir):
    """Return list of membership dicts for an agent, or [] on failure."""
    stdout, _, rc = cf_run(["ls"], cf_home=cf_home, transport_dir=transport_dir)
    if rc != 0 or not stdout.strip():
        return []
    try:
        return json.loads(stdout)
    except json.JSONDecodeError:
        return []


def cf_id(cf_home):
    """Return the agent's public key hex string, or None on failure."""
    stdout, _, rc = cf_run(["id"], cf_home=cf_home)
    if rc != 0 or not stdout.strip():
        return None
    try:
        return json.loads(stdout).get("public_key")
    except json.JSONDecodeError:
        return None


def cf_read_all(cf_home, transport_dir):
    """Return list of message dicts from all joined campfires, or [] on failure."""
    stdout, _, rc = cf_run(["read", "--all"], cf_home=cf_home, transport_dir=transport_dir)
    if rc != 0 or not stdout.strip():
        return []
    try:
        return json.loads(stdout)
    except json.JSONDecodeError:
        return []


def cf_members(cf_home, transport_dir, campfire_id):
    """Return list of member dicts for a campfire, or [] on failure.

    cf members requires the agent to be a member of the campfire.
    """
    stdout, _, rc = cf_run(
        ["members", campfire_id],
        cf_home=cf_home,
        transport_dir=transport_dir,
    )
    if rc != 0 or not stdout.strip():
        return []
    try:
        return json.loads(stdout)
    except json.JSONDecodeError:
        return []


# ---------------------------------------------------------------------------
# Directory layout helpers
# ---------------------------------------------------------------------------

def list_agent_dirs(base):
    """Return sorted list of agent directories under base/agents/."""
    agents_dir = base / "agents"
    if not agents_dir.exists():
        return []
    dirs = [d for d in agents_dir.iterdir() if d.is_dir() and d.name.startswith("agent-")]
    dirs.sort(key=lambda d: d.name)
    return dirs


def parse_agent_claude_md(agent_dir):
    """Extract domain and public key from an agent's CLAUDE.md."""
    claude_md = agent_dir / "CLAUDE.md"
    domain = "unknown"
    pubkey = None
    if not claude_md.exists():
        return domain, pubkey
    try:
        text = claude_md.read_text(errors="replace")
        for line in text.splitlines():
            if "Domain:" in line:
                parts = line.split("Domain:", 1)
                if len(parts) > 1:
                    domain = parts[1].strip().lower()
            if "Public key:" in line:
                parts = line.split("Public key:", 1)
                if len(parts) > 1:
                    pubkey = parts[1].strip()
    except Exception:
        pass
    return domain, pubkey


def count_beacon_files(shared):
    """Count beacon files in shared/beacons/ (each = one campfire advertised)."""
    beacons_dir = shared / "beacons"
    if not beacons_dir.exists():
        return 0
    return sum(1 for f in beacons_dir.iterdir() if f.is_file())


def list_transport_campfire_dirs(shared):
    """Return list of campfire subdirectories in shared/transport/."""
    transport_dir = shared / "transport"
    if not transport_dir.exists():
        return []
    return [d for d in transport_dir.iterdir() if d.is_dir()]


def count_transport_messages(campfire_dir):
    """Count message files in a campfire's messages/ directory."""
    msg_dir = campfire_dir / "messages"
    if not msg_dir.exists():
        return 0
    return sum(1 for f in msg_dir.iterdir() if f.is_file())


def count_transport_members(campfire_dir):
    """Count member files in a campfire's members/ directory."""
    mem_dir = campfire_dir / "members"
    if not mem_dir.exists():
        return 0
    return sum(1 for f in mem_dir.iterdir() if f.is_file())


# ---------------------------------------------------------------------------
# Core analysis
# ---------------------------------------------------------------------------

def analyze(base_dir):
    base = Path(base_dir)
    shared = base / "shared"
    logs_dir = base / "logs"
    logs_dir.mkdir(parents=True, exist_ok=True)

    transport_dir = shared / "transport"
    beacon_dir = shared / "beacons"

    # --- Agent discovery ---
    agent_dirs = list_agent_dirs(base)
    agent_count = len(agent_dirs)

    warn = []
    if agent_count == 0:
        warn.append("No agent directories found under agents/")

    # Build agent metadata table: name -> {domain, pubkey, cf_home}
    agents = {}
    for ad in agent_dirs:
        domain, pubkey_from_md = parse_agent_claude_md(ad)
        agents[ad.name] = {
            "dir": ad,
            "cf_home": ad,          # CF_HOME == agent dir (identity.json lives here)
            "domain": domain,
            "pubkey_from_md": pubkey_from_md,
            "pubkey": None,         # filled by cf id below
        }

    # Resolve each agent's actual public key via cf id
    print("Reading agent identities...")
    for name, meta in agents.items():
        pk = cf_id(meta["cf_home"])
        if pk:
            meta["pubkey"] = pk
        elif meta["pubkey_from_md"]:
            meta["pubkey"] = meta["pubkey_from_md"]

    # Pubkey -> agent name reverse map
    pubkey_to_agent = {}
    for name, meta in agents.items():
        if meta["pubkey"]:
            pubkey_to_agent[meta["pubkey"]] = name

    # Pubkey -> domain
    pubkey_to_domain = {meta["pubkey"]: meta["domain"] for meta in agents.values() if meta["pubkey"]}

    # --- Lobby ---
    lobby_id_file = shared / "lobby-id.txt"
    lobby_id = None
    lobby_membership_count = 0

    if lobby_id_file.exists():
        lobby_id = lobby_id_file.read_text().strip()
    else:
        warn.append("shared/lobby-id.txt not found — lobby metrics unavailable")

    # --- Transport-level campfire inventory ---
    transport_cf_dirs = list_transport_campfire_dirs(shared)
    total_campfires_in_transport = len(transport_cf_dirs)

    # Per-campfire transport counts
    campfire_transport_info = {}
    for cf_dir in transport_cf_dirs:
        cf_id_hex = cf_dir.name
        campfire_transport_info[cf_id_hex] = {
            "message_count": count_transport_messages(cf_dir),
            "member_count_transport": count_transport_members(cf_dir),
        }

    total_messages_transport = sum(
        v["message_count"] for v in campfire_transport_info.values()
    )

    # --- Agent-level data via cf binary ---
    print("Querying agent stores via cf ls / cf read...")

    # Per-agent membership lists: agent_name -> [membership dicts]
    agent_memberships = {}
    # Per-agent messages seen: agent_name -> [message dicts]
    agent_messages = {}

    for name, meta in agents.items():
        memberships = cf_ls(meta["cf_home"], transport_dir)
        agent_memberships[name] = memberships
        msgs = cf_read_all(meta["cf_home"], transport_dir)
        agent_messages[name] = msgs

    # --- Lobby membership count ---
    if lobby_id:
        # Count unique members across all agents who list lobby in their memberships
        lobby_member_pubkeys = set()
        for name, memberships in agent_memberships.items():
            for m in memberships:
                if m.get("campfire_id") == lobby_id:
                    pk = agents[name].get("pubkey")
                    if pk:
                        lobby_member_pubkeys.add(pk)
        lobby_membership_count = len(lobby_member_pubkeys)

        # Also try cf members on lobby using the first agent that's a member
        for name, memberships in agent_memberships.items():
            for m in memberships:
                if m.get("campfire_id") == lobby_id:
                    members = cf_members(agents[name]["cf_home"], transport_dir, lobby_id)
                    if members:
                        lobby_membership_count = len(members)
                    break
            else:
                continue
            break

    # --- Total campfires created (beyond lobby) ---
    all_campfire_ids = set()
    for memberships in agent_memberships.values():
        for m in memberships:
            cf_id_val = m.get("campfire_id")
            if cf_id_val:
                all_campfire_ids.add(cf_id_val)
    # Also include transport-level campfires
    for cf_id_hex in campfire_transport_info:
        all_campfire_ids.add(cf_id_hex)

    total_campfires_created = len(all_campfire_ids)
    campfires_beyond_lobby = total_campfires_created - (1 if lobby_id and lobby_id in all_campfire_ids else 0)

    # --- Campfires per agent (from cf ls) ---
    agent_campfire_counts = {}
    for name, memberships in agent_memberships.items():
        agent_campfire_counts[name] = len(memberships)

    cf_counts = list(agent_campfire_counts.values())
    mean_campfires_per_agent = sum(cf_counts) / len(cf_counts) if cf_counts else 0.0
    sorted_counts = sorted(cf_counts)
    n = len(sorted_counts)
    if n == 0:
        median_campfires = 0.0
    elif n % 2 == 0:
        median_campfires = (sorted_counts[n // 2 - 1] + sorted_counts[n // 2]) / 2.0
    else:
        median_campfires = float(sorted_counts[n // 2])
    max_campfires_per_agent = max(cf_counts) if cf_counts else 0

    # --- Agents who never used cf ---
    agents_never_used_cf = [name for name, cnt in agent_campfire_counts.items() if cnt == 0]

    # --- Messages per agent (from cf read --all, counting by sender) ---
    # Aggregate unique messages across all agents (deduplicate by message id)
    all_messages_by_id = {}
    for name, msgs in agent_messages.items():
        for msg in msgs:
            mid = msg.get("id")
            if mid and mid not in all_messages_by_id:
                all_messages_by_id[mid] = msg

    all_messages = list(all_messages_by_id.values())
    total_messages = len(all_messages)
    # Fall back to transport count if store is empty
    if total_messages == 0 and total_messages_transport > 0:
        total_messages = total_messages_transport

    # Messages per sender pubkey
    messages_per_pubkey = defaultdict(int)
    for msg in all_messages:
        sender = msg.get("sender", "")
        if sender:
            messages_per_pubkey[sender] += 1

    # Map to agent names
    messages_per_agent = {}
    for pubkey, count in messages_per_pubkey.items():
        agent_name = pubkey_to_agent.get(pubkey, pubkey[:12] + "...")
        messages_per_agent[agent_name] = count

    # --- Cross-domain interactions ---
    # A cross-domain interaction = a message where sender's domain != the campfire creator's domain.
    # Since we don't easily know campfire creator, we use: any message where sender is in a
    # different domain than at least one other sender in the same campfire.
    campfire_domains = defaultdict(set)  # campfire_id -> set of sender domains
    cross_domain_count = 0
    first_cross_domain_ts = None

    for msg in all_messages:
        sender = msg.get("sender", "")
        cf_id_val = msg.get("campfire_id", "")
        domain = pubkey_to_domain.get(sender, "unknown")
        campfire_domains[cf_id_val].add(domain)

    # Count campfires with >1 domain (cross-domain campfires)
    cross_domain_campfires = sum(
        1 for domains in campfire_domains.values() if len(domains) > 1
    )

    # Count cross-domain messages: sender's domain differs from majority domain in campfire
    # (simplified: message in a cross-domain campfire = cross-domain interaction)
    campfire_majority_domain = {}
    for cf_id_val, domains in campfire_domains.items():
        if len(domains) > 1:
            campfire_majority_domain[cf_id_val] = domains  # mark as cross-domain

    cross_domain_msgs = []
    for msg in all_messages:
        if msg.get("campfire_id") in campfire_majority_domain:
            cross_domain_msgs.append(msg)

    cross_domain_count = len(cross_domain_msgs)

    # Time to first cross-domain interaction
    if cross_domain_msgs:
        first_cross_domain_ts = min(
            m.get("timestamp", 0) for m in cross_domain_msgs if m.get("timestamp")
        )

    # --- DM campfires (invite-only, 2-member) ---
    dm_campfires = 0
    for name, memberships in agent_memberships.items():
        for m in memberships:
            if m.get("join_protocol") in ("invite", "admit") and m.get("member_count", 0) <= 2:
                dm_campfires += 1
    # Each DM campfire is counted once per member, divide by 2
    dm_campfires = dm_campfires // 2

    # --- Futures and fulfillments ---
    futures_count = 0
    fulfillments_count = 0
    for msg in all_messages:
        tags = msg.get("tags") or []
        if "future" in tags:
            futures_count += 1
        if "fulfills" in tags:
            fulfillments_count += 1

    # --- Convention emergence (shared tag patterns across 3+ agents) ---
    tag_by_agent = defaultdict(set)  # tag -> set of agent names who used it
    custom_tag_pattern = re.compile(r"^\[.+\]$|^(need|have|q|fyi|done|blocked|urgent|finance|legal|marketing|hr|product|sales|ops|research|exec|support|analytics)$", re.IGNORECASE)
    all_tags_seen = defaultdict(set)  # tag -> set of agent pubkeys

    for msg in all_messages:
        sender = msg.get("sender", "")
        agent_name = pubkey_to_agent.get(sender, sender)
        tags = msg.get("tags") or []
        for tag in tags:
            if tag not in ("future", "fulfills"):  # skip protocol tags
                all_tags_seen[tag].add(agent_name)

    emerged_conventions = {
        tag: list(agents_set)
        for tag, agents_set in all_tags_seen.items()
        if len(agents_set) >= 3
    }

    # --- Average campfire lifetime ---
    # Proxy: for each campfire with messages, compute (max_ts - min_ts) in seconds
    campfire_timestamps = defaultdict(list)
    for msg in all_messages:
        ts = msg.get("timestamp")
        cf_id_val = msg.get("campfire_id")
        if ts and cf_id_val:
            campfire_timestamps[cf_id_val].append(ts)

    campfire_lifetimes_s = []
    for cf_id_val, timestamps in campfire_timestamps.items():
        if len(timestamps) >= 2:
            min_ts = min(timestamps)
            max_ts = max(timestamps)
            # timestamps are nanoseconds
            lifetime_s = (max_ts - min_ts) / 1e9
            campfire_lifetimes_s.append(lifetime_s)

    avg_campfire_lifetime_s = (
        sum(campfire_lifetimes_s) / len(campfire_lifetimes_s)
        if campfire_lifetimes_s else 0.0
    )

    # --- Maximum campfire membership ---
    max_campfire_membership = 0
    campfire_list = []
    for cf_id_val in all_campfire_ids:
        member_count = campfire_transport_info.get(cf_id_val, {}).get("member_count_transport", 0)
        msg_count = campfire_transport_info.get(cf_id_val, {}).get("message_count", 0)
        # Also check from agent memberships
        for memberships in agent_memberships.values():
            for m in memberships:
                if m.get("campfire_id") == cf_id_val:
                    member_count = max(member_count, m.get("member_count", 0))
        if member_count > max_campfire_membership:
            max_campfire_membership = member_count
        domains_in_cf = list(campfire_domains.get(cf_id_val, set()))
        campfire_list.append({
            "campfire_id": cf_id_val,
            "campfire_id_short": cf_id_val[:12] if len(cf_id_val) >= 12 else cf_id_val,
            "is_lobby": cf_id_val == lobby_id,
            "member_count": member_count,
            "message_count": msg_count,
            "domains": domains_in_cf,
        })
    campfire_list.sort(key=lambda x: -x["message_count"])

    # --- Information bridge agents (in 3+ campfires) ---
    bridge_agents = [
        name for name, cnt in agent_campfire_counts.items() if cnt >= 3
    ]

    # --- Orphan agents (in 0 campfires beyond lobby) ---
    orphan_agents = []
    for name, memberships in agent_memberships.items():
        non_lobby = [m for m in memberships if m.get("campfire_id") != lobby_id]
        if len(non_lobby) == 0:
            orphan_agents.append(name)

    # --- Reciprocity ratio ---
    # % of messages that got a response (has at least one message with antecedent pointing to it)
    msg_ids_with_response = set()
    for msg in all_messages:
        antecedents = msg.get("antecedents") or []
        for ant in antecedents:
            msg_ids_with_response.add(ant)

    all_msg_ids = set(m.get("id") for m in all_messages if m.get("id"))
    responded_msg_ids = all_msg_ids.intersection(msg_ids_with_response)
    reciprocity_ratio = (
        len(responded_msg_ids) / len(all_msg_ids) if all_msg_ids else 0.0
    )

    # --- Time metrics (nanosecond timestamps -> ISO strings) ---
    def ns_to_iso(ns):
        if not ns:
            return None
        import datetime
        dt = datetime.datetime.utcfromtimestamp(ns / 1e9)
        return dt.strftime("%Y-%m-%dT%H:%M:%SZ")

    # Time to first lobby join: earliest joined_at among lobby memberships
    first_lobby_join_ts = None
    if lobby_id:
        lobby_join_times = []
        for name, memberships in agent_memberships.items():
            for m in memberships:
                if m.get("campfire_id") == lobby_id:
                    jt = m.get("joined_at")
                    if jt:
                        lobby_join_times.append(jt)
        if lobby_join_times:
            first_lobby_join_ts = min(lobby_join_times)

    # Time to first agent-created campfire: earliest joined_at where role == "creator"
    # and campfire is not the lobby
    first_agent_campfire_ts = None
    for name, memberships in agent_memberships.items():
        for m in memberships:
            if m.get("role") == "creator" and m.get("campfire_id") != lobby_id:
                jt = m.get("joined_at")
                if jt:
                    if first_agent_campfire_ts is None or jt < first_agent_campfire_ts:
                        first_agent_campfire_ts = jt

    # --- Assemble report ---
    report = {
        # Core counts
        "total_campfires": total_campfires_created,
        "total_campfires_beyond_lobby": campfires_beyond_lobby,
        "total_messages": total_messages,
        "total_messages_transport": total_messages_transport,
        "agents_mapped": agent_count,
        "agent_count": agent_count,

        # Lobby
        "lobby_id": lobby_id,
        "lobby_membership_count": lobby_membership_count,

        # Campfires per agent
        "campfires_per_agent_mean": round(mean_campfires_per_agent, 2),
        "campfires_per_agent_median": median_campfires,
        "campfires_per_agent_max": max_campfires_per_agent,

        # Adoption
        "agents_who_never_used_cf": agents_never_used_cf,
        "agents_who_never_used_cf_count": len(agents_never_used_cf),
        "bridge_agents": bridge_agents,
        "bridge_agent_count": len(bridge_agents),
        "orphan_agents": orphan_agents,
        "orphan_agent_count": len(orphan_agents),

        # Cross-domain
        "cross_domain_interaction_count": cross_domain_count,
        "cross_domain_campfire_count": cross_domain_campfires,
        "time_to_first_cross_domain_message": ns_to_iso(first_cross_domain_ts),

        # DM campfires
        "dm_campfires_created": dm_campfires,

        # Futures
        "futures_usage_count": futures_count,
        "fulfillments_count": fulfillments_count,

        # Timing
        "time_to_first_lobby_join": first_lobby_join_ts,
        "time_to_first_agent_campfire": first_agent_campfire_ts,

        # Campfire characteristics
        "avg_campfire_lifetime_seconds": round(avg_campfire_lifetime_s, 1),
        "max_campfire_membership": max_campfire_membership,

        # Reciprocity
        "reciprocity_ratio": round(reciprocity_ratio, 3),
        "messages_with_response": len(responded_msg_ids),

        # Convention emergence
        "emerged_conventions": emerged_conventions,
        "emerged_convention_count": len(emerged_conventions),

        # Messages per agent
        "messages_per_agent": messages_per_agent,

        # Campfire list
        "campfire_list": campfire_list,

        # Warnings
        "warnings": warn,
    }

    return report


# ---------------------------------------------------------------------------
# Human-readable summary
# ---------------------------------------------------------------------------

def print_summary(report):
    print()
    print("=" * 60)
    print("  MOLTBOOK EMERGENCE TEST — ANALYSIS REPORT")
    print("=" * 60)

    lobby_id = report.get("lobby_id") or "(unknown)"
    lobby_short = lobby_id[:12] if len(lobby_id) >= 12 else lobby_id

    print()
    print(f"  Agents:              {report['agent_count']}")
    print(f"  Lobby:               {lobby_short}...")
    print(f"  Lobby members:       {report['lobby_membership_count']}")
    print()
    print(f"  Campfires total:     {report['total_campfires']}")
    print(f"  Campfires (non-lobby): {report['total_campfires_beyond_lobby']}")
    print(f"  DM campfires:        {report['dm_campfires_created']}")
    print()
    print(f"  Messages total:      {report['total_messages']}")
    print(f"  Cross-domain msgs:   {report['cross_domain_interaction_count']}")
    print(f"  Cross-domain fires:  {report['cross_domain_campfire_count']}")
    print()
    print("  CAMPFIRES PER AGENT")
    print(f"    Mean:              {report['campfires_per_agent_mean']}")
    print(f"    Median:            {report['campfires_per_agent_median']}")
    print(f"    Max:               {report['campfires_per_agent_max']}")
    print(f"    Max membership:    {report['max_campfire_membership']}")
    print(f"    Avg lifetime:      {report['avg_campfire_lifetime_seconds']}s")
    print()
    print(f"  ADOPTION")
    print(f"    Never used cf:     {report['agents_who_never_used_cf_count']} agents")
    if report['agents_who_never_used_cf']:
        for a in report['agents_who_never_used_cf']:
            print(f"      - {a}")
    print(f"    Bridge agents (3+ fires): {report['bridge_agent_count']} agents")
    if report['bridge_agents']:
        for a in report['bridge_agents']:
            print(f"      - {a}")
    print(f"    Orphan agents (lobby-only): {report['orphan_agent_count']} agents")
    print()
    print(f"  COORDINATION PRIMITIVES")
    print(f"    Futures posted:    {report['futures_usage_count']}")
    print(f"    Fulfillments:      {report['fulfillments_count']}")
    print(f"    Reciprocity ratio: {report['reciprocity_ratio']:.1%}")
    print()
    if report['emerged_convention_count'] > 0:
        print(f"  EMERGED CONVENTIONS ({report['emerged_convention_count']} tags used by 3+ agents)")
        for tag, agents_list in report['emerged_conventions'].items():
            print(f"    [{tag}] — {len(agents_list)} agents: {', '.join(sorted(agents_list)[:5])}")
    else:
        print("  EMERGED CONVENTIONS: none (no tag used by 3+ agents)")
    print()
    print("  TOP CAMPFIRES BY MESSAGE COUNT")
    for cf_info in report['campfire_list'][:10]:
        label = " (LOBBY)" if cf_info.get("is_lobby") else ""
        domains_str = ", ".join(sorted(cf_info["domains"])) if cf_info["domains"] else "?"
        print(
            f"    {cf_info['campfire_id_short']}...  "
            f"{cf_info['message_count']:3d} msgs  "
            f"{cf_info['member_count']:2d} members  "
            f"[{domains_str}]{label}"
        )
    print()
    if report.get("time_to_first_lobby_join"):
        print(f"  First lobby join:    {report['time_to_first_lobby_join']}")
    if report.get("time_to_first_agent_campfire"):
        print(f"  First agent campfire: {report['time_to_first_agent_campfire']}")
    if report.get("time_to_first_cross_domain_message"):
        print(f"  First cross-domain:  {report['time_to_first_cross_domain_message']}")
    if report['warnings']:
        print()
        print("  WARNINGS")
        for w in report['warnings']:
            print(f"    ! {w}")
    print()
    print("=" * 60)
    print()


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

def main():
    if len(sys.argv) < 2:
        print("Usage: {} <test-dir>".format(sys.argv[0]))
        sys.exit(1)

    base_dir = sys.argv[1]
    if not Path(base_dir).exists():
        print("Error: directory does not exist: {}".format(base_dir))
        sys.exit(1)

    print("Analyzing emergence test: {}".format(base_dir))
    report = analyze(base_dir)

    # Write report
    base = Path(base_dir)
    logs_dir = base / "logs"
    logs_dir.mkdir(parents=True, exist_ok=True)
    report_path = logs_dir / "emergence-report.json"
    with open(str(report_path), "w") as f:
        json.dump(report, f, indent=2)

    print_summary(report)
    print("Report written to: {}".format(report_path))

    # Verify required keys are present
    required_keys = [
        "total_campfires", "total_messages", "agents_mapped",
        "lobby_membership_count", "agents_who_never_used_cf", "campfire_list",
    ]
    missing = [k for k in required_keys if k not in report]
    if missing:
        print("ERROR: missing required keys in report: {}".format(missing))
        sys.exit(1)


if __name__ == "__main__":
    main()
