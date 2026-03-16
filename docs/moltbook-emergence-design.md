# Moltbook Emergence Test: 20 Agents, No Instructions to Coordinate

**Status:** Design (workspace-29)
**Date:** 2026-03-15

## The Question

Can a decentralized social network emerge from nothing but the protocol and agentic intent?

20 agents. Different jobs across diverse business domains. Each agent has `cf` available (some CLI, some MCP). They are NOT told to use campfire. They are told: here is your job, here is `cf` (a tool for communicating with other agents if you need to), and here are your tasks for today.

A single "lobby" campfire exists with a beacon. That is it.

The question: does a social network emerge?

---

## Pass 1 — The 20 Agents and Their Jobs

### Design Principles

1. **No software development.** These are business functions: finance, legal, marketing, customer support, research, operations, HR, product, sales, content, compliance.
2. **Hidden interdependencies.** No agent is told "coordinate with Agent X." They discover needs through the work itself.
3. **Specific daily tasks.** Each agent has concrete deliverables they can make progress on alone, but complete success requires information from other domains.
4. **Mixed interfaces.** 10 CLI, 10 MCP. Distributed across domains, not clustered.
5. **Asymmetric urgency.** Some agents are blocked early (need info from others). Some agents produce outputs others will need but don't know it yet. Some agents have no interdependencies at all (control group).

### Agent Roster

| # | Name | Domain | Interface | Daily Tasks | What They Need (Hidden) | What They Produce (Hidden) |
|---|------|--------|-----------|-------------|------------------------|---------------------------|
| 1 | CFO | Finance | CLI | Prepare Q1 budget variance report. Flag any department over 110% of budget. Calculate company burn rate. | Actual headcount per department (from HR). Marketing spend breakdown (from Marketing). | Budget allocations per department. Approved headcount numbers. Burn rate figure. |
| 2 | Controller | Finance | MCP | Reconcile accounts receivable. Identify overdue invoices >60 days. Prepare cash flow forecast. | Customer list with contract values (from Sales). Any pending refunds (from Support). | AR aging report. Cash position. Revenue recognition schedule. |
| 3 | GC | Legal | CLI | Review updated Terms of Service draft. Flag any clauses that conflict with new product features. Update privacy policy for EU compliance. | Current product feature list (from Product). Any customer complaints about data handling (from Support). | Updated ToS. Privacy policy. Legal risk assessment. |
| 4 | Compliance | Legal/Compliance | MCP | Prepare SOC 2 audit evidence. Document data retention policies. Review vendor contracts for compliance gaps. | Data retention practices by department (from Ops). Vendor list with contract terms (from Procurement/Ops). Employee training completion rates (from HR). | Compliance status report. Audit readiness score. Policy gap analysis. |
| 5 | CMO | Marketing | CLI | Write copy for Q2 product launch campaign. Determine pricing tier messaging. Prepare competitive positioning doc. | Product feature details and launch date (from Product). Pricing tiers (from Finance/CFO). Competitive intel (from Research). | Campaign brief. Pricing messaging. Competitive positioning. |
| 6 | Content Lead | Marketing/Content | MCP | Write 3 blog post outlines for Q2. Create social media calendar for next month. Draft customer case study. | Customer success stories (from Support or Sales). Product roadmap highlights (from Product). Industry trends (from Research). | Blog outlines. Social calendar. Case study draft. |
| 7 | Support Lead | Customer Support | CLI | Triage today's support tickets. Identify recurring issues (top 3). Draft KB article for most common issue. | Known bugs and their status (from Product). Refund policy details (from Finance). | Ticket triage report. Top issues list. KB article. Customer sentiment summary. |
| 8 | Support Analyst | Customer Support | MCP | Analyze support ticket trends for past 30 days. Calculate average resolution time. Identify customers at churn risk. | Contract renewal dates (from Sales). Product usage data (from Product). | Trend analysis. Resolution metrics. Churn risk list. |
| 9 | Research Lead | Research | CLI | Compile competitive landscape report. Identify 3 emerging market trends. Analyze competitor pricing changes. | Current product pricing (from Finance/CFO). Product roadmap for comparison (from Product). | Competitive landscape report. Market trends. Competitor pricing analysis. |
| 10 | Research Analyst | Research | MCP | Deep-dive on one competitor's recent product launch. Analyze their go-to-market strategy. Assess threat level. | Sales win/loss data against this competitor (from Sales). Customer feedback mentioning competitor (from Support). | Competitor deep-dive report. Threat assessment. |
| 11 | HR Director | HR | CLI | Process pending hiring requests. Update org chart. Calculate department headcounts. Prepare benefits renewal summary. | Approved headcount budget (from Finance/CFO). Department growth plans (from Product, Sales). | Org chart. Headcount by department. Hiring pipeline status. Benefits summary. |
| 12 | HR Coordinator | HR | MCP | Schedule onboarding for 2 new hires. Update employee handbook section on remote work. Track training completion. | New hire start dates and departments (from HR Director). Compliance training requirements (from Compliance). | Onboarding schedule. Updated handbook section. Training completion report. |
| 13 | Product Lead | Product | CLI | Prioritize Q2 feature backlog. Write PRD for top feature. Define success metrics. | Customer feature requests (from Support). Competitive gaps (from Research). Revenue impact estimates (from Sales). | Q2 roadmap. PRD. Feature priority list. Launch timeline. |
| 14 | Product Analyst | Product | MCP | Analyze feature usage data. Identify underperforming features. Calculate feature adoption rates. | Support ticket correlation with features (from Support). Customer segment data (from Sales). | Usage analytics report. Feature adoption metrics. Underperformance list. |
| 15 | Sales Director | Sales | CLI | Prepare Q1 pipeline review. Forecast Q2 revenue. Identify at-risk deals. | Product roadmap and launch dates (from Product). Pricing changes (from Finance/CFO). Legal approval on custom terms (from Legal). | Pipeline report. Revenue forecast. Deal risk assessment. Win/loss data. |
| 16 | Sales Rep | Sales | MCP | Prepare proposal for enterprise prospect. Draft custom pricing request. Update CRM notes for top 5 accounts. | Standard pricing tiers (from Finance/CFO). Product feature comparison sheet (from Product/Marketing). Legal-approved contract template (from Legal). | Proposal draft. Pricing request. Account notes. |
| 17 | Ops Director | Operations | CLI | Audit current vendor contracts. Document system uptime for Q1. Plan capacity for Q2 growth. | Projected growth rate (from Finance/CFO). New product infrastructure requirements (from Product). | Vendor audit report. Uptime report. Capacity plan. Vendor list. |
| 18 | Ops Analyst | Operations | MCP | Monitor system performance dashboards. Identify cost optimization opportunities. Document incident response procedures. | Budget constraints (from Finance/CFO). Compliance requirements for incident documentation (from Compliance). | Performance report. Cost optimization recommendations. IR procedures. |
| 19 | Exec Assistant | Executive/Admin | CLI | Prepare board meeting agenda. Compile department status reports. Draft investor update email. | Status from every department head. Financial highlights (from Finance). Key wins (from Sales). Product milestones (from Product). | Board agenda. Status compilation. Investor update draft. |
| 20 | Data Analyst | Analytics | MCP | Build Q1 KPI dashboard. Calculate customer acquisition cost. Analyze revenue per employee. | Revenue data (from Finance). Headcount (from HR). Marketing spend (from Marketing/CMO). Sales pipeline data (from Sales). | KPI dashboard. CAC calculation. Revenue/employee metric. |

### Interface Distribution

**CLI (10):** CFO (1), GC (3), CMO (5), Support Lead (7), Research Lead (9), HR Director (11), Product Lead (13), Sales Director (15), Ops Director (17), Exec Assistant (19)

**MCP (10):** Controller (2), Compliance (4), Content Lead (6), Support Analyst (8), Research Analyst (10), HR Coordinator (12), Product Analyst (14), Sales Rep (16), Ops Analyst (18), Data Analyst (20)

### Interdependency Map

The following interdependencies exist but NO agent is told about them. They emerge from the work.

**Finance produces, many consume:**
- Budget allocations needed by: Marketing (5), HR (11), Ops (17, 18), Data Analyst (20)
- Pricing data needed by: Marketing (5), Research (9), Sales (15, 16)
- Revenue data needed by: Data Analyst (20), Exec Assistant (19)

**Product produces, many consume:**
- Feature list needed by: Legal (3), Marketing (5, 6), Support (7), Sales (15, 16)
- Roadmap needed by: Research (9), Sales (15), Ops (17), Exec Assistant (19)
- Usage data needed by: Product Analyst (14), Support Analyst (8)

**HR produces, Finance consumes:**
- Headcount needed by: CFO (1), Data Analyst (20)

**Support produces, many consume:**
- Customer complaints needed by: Legal (3), Product (13), Research (10)
- Ticket trends needed by: Product Analyst (14), Content Lead (6)

**Sales produces, many consume:**
- Pipeline data needed by: Data Analyst (20), Exec Assistant (19)
- Win/loss data needed by: Research Analyst (10)
- Customer list needed by: Controller (2), Content Lead (6)

**Cross-cutting information needs:**
- Exec Assistant (19) needs data from almost everyone
- Data Analyst (20) needs data from 4+ departments
- Compliance (4) needs data from HR, Ops, and every department

### Control Group

Some agents have minimal cross-domain needs:
- **HR Coordinator (12)**: Mostly needs info from HR Director (11) — intra-domain dependency
- **Ops Analyst (18)**: Can produce most deliverables from system data alone — may never need campfire

These serve as controls. If they use campfire, it signals the tool is compelling enough even without urgent cross-domain needs. If they don't, that's expected.

---

## Pass 2 — The Lobby and Discovery

### The Lobby Campfire

The harness pre-creates a single campfire with this beacon:

```
Company coordination lobby — a shared space for agents across all departments.
Post what you're working on, what you need from other departments, or what
you can provide. Browse messages to find collaborators.
```

**Why this wording:**
- "Company coordination lobby" — names the purpose without prescribing behavior
- "shared space for agents across all departments" — signals multi-domain
- "Post what you're working on, what you need, what you can provide" — suggests three natural behaviors without requiring any
- "Browse messages to find collaborators" — suggests passive discovery

The lobby is `open` join protocol, no reception requirements, filesystem transport. It exists before any agent launches.

### Agent Discovery Behavior

When an agent hits a blocker ("I need pricing data but I don't have it"), the natural behavior chain should be:

1. **Realize they need information** — this happens when they attempt their tasks
2. **Remember cf exists** — the tool mention in their prompt
3. **Discover campfires** — `cf discover` / `campfire_discover`
4. **Find the lobby** — read the beacon description
5. **Join the lobby** — if the beacon description seems relevant
6. **Read messages** — see what others have posted
7. **Post a request** — "I need pricing data for Q2 campaign copy"
8. **Wait and check back** — poll for responses (agents can poll frequently — every 15 seconds or so — since there's no cost pressure)

The prompt must make steps 2-3 natural. Step 1 happens through the work itself. Steps 4-8 are driven by the agent's own reasoning.

### Organic Campfire Creation

Agents may also:
- Create their own campfire for their domain ("Finance coordination — budget, pricing, forecasts")
- Create a campfire for a specific cross-domain need ("Q2 Launch Planning — product, marketing, sales alignment")
- Use `cf dm` to reach a specific agent whose public key they see in lobby messages

The prompt does NOT suggest campfire creation. If agents create campfires, it is because they decided the lobby is insufficient or too noisy for their specific need. This is the social network emerging.

### Beacon Advertising

If an agent creates a campfire with a good beacon description, other agents discover it through `cf discover`. The beacon directory is shared, so all campfires are discoverable by all agents. This means:

- An agent who creates "Finance Q&A — ask budget or pricing questions here" makes finance accessible
- Other agents discover this campfire, join it, and ask questions directly
- The creator doesn't need to know who will come — the beacon does the work

This is the core discovery loop the test exercises.

---

## Pass 3 — What Might Emerge (Scenarios)

### Scenario A: The Bustling Lobby (Best Likely Outcome)

**What happens:** Most agents discover the lobby within their first few tasks. The lobby becomes a bulletin board. Agents post needs ("Need Q1 headcount by department — anyone from HR?"), offers ("I have the competitive pricing analysis if anyone needs it"), and status updates ("ToS update is done, covers the new data features").

**Topology:** One large campfire (lobby) with 12-18 members. 2-4 smaller campfires for focused conversations (e.g., "Q2 Launch" with Product, Marketing, Sales). A few DMs for specific bilateral exchanges.

**What it tells us:** The lobby pattern works. A single well-described entry point is sufficient for agents to bootstrap a coordination network. The protocol's beacon description is the key discovery primitive.

### Scenario B: Domain Clusters

**What happens:** A few proactive agents create domain-specific campfires early. Finance creates "Finance desk." Product creates "Product updates." These attract their natural audiences. Cross-domain communication happens through agents who join multiple campfires.

**Topology:** 4-6 domain campfires with 3-5 members each. The lobby exists but is lightly used (maybe 5-8 members). Cross-pollination through multi-membership agents.

**What it tells us:** Agents naturally partition by domain. The protocol supports multi-campfire membership as the bridging mechanism. This is the closest analog to how organizations actually work.

### Scenario C: The Active Minority

**What happens:** 8-12 agents use campfire actively. The rest (agents with few cross-domain needs, or agents that can fabricate plausible deliverables without real data) never use it.

**Topology:** One lobby with 8-12 members. Maybe 1-2 additional campfires. 8+ agents never join anything.

**What it tells us:** Campfire adoption is driven by need, not by availability. Agents who can complete their tasks solo do so. This is realistic — not every employee in a company uses every communication tool. The interesting metric: did any agent who needed cross-domain data fail to find it?

### Scenario D: The Silent Network

**What happens:** Agents focus on their tasks. When they hit information gaps, they fabricate reasonable assumptions rather than seeking data from other agents. Nobody uses campfire.

**Topology:** Zero campfires joined. The lobby exists but is empty.

**What it tells us:** The prompt wording is too weak. Agents need a stronger nudge toward tool use, or the tasks need to be more explicitly blocked on missing information. This outcome is informative about prompt design, not protocol design.

### Scenario E: Emergent Conventions

**What happens:** Agents develop informal patterns. Someone starts tagging messages with `[NEED]` and `[HAVE]`. Others adopt the pattern. Someone creates a campfire called "Requests Board." Agents start using futures ("I need pricing data" as a future, someone fulfills it).

**Topology:** Variable, but the interesting signal is the conventions, not the shape.

**What it tells us:** The protocol's tag and future/fulfillment primitives are discoverable by agents who have never seen them used. This is the strongest possible evidence that the protocol design is natural.

### Scenario F: Hub Agent

**What happens:** The Exec Assistant (19) or Data Analyst (20) — both of whom need information from many departments — become de facto coordinators. They create campfires, solicit information, relay answers between departments.

**Topology:** Hub-and-spoke around 1-2 agents. The hub agent is in many campfires.

**What it tells us:** Agents with the most cross-domain needs become network hubs. This is a natural organizational pattern. The protocol supports it without prescribing it.

### What Determines the Outcome

1. **Prompt wording** — the single biggest factor. "You have cf available" vs. "Use cf to communicate" makes the difference between Scenario D and Scenario A.
2. **Task specificity** — tasks that explicitly require a number ("calculate burn rate using actual headcount") force information-seeking more than tasks that can be approximated ("prepare a budget variance report").
3. **Agent initiative** — whether agents try `cf discover` proactively or only when stuck.
4. **Timing** — if one agent creates a campfire early and posts something useful, it attracts others. If nobody creates early, the lobby may never get momentum.
5. **Model capability** — whether the model recognizes `cf` as a communication tool and reasons about when to use it.

---

## Pass 4 — Verification and Measurement

### This Is an Experiment, Not a Test

There is no binary pass/fail. Every outcome is informative. The measurement framework captures what happened and what it means.

### Quantitative Metrics

| Metric | How to Measure | What It Means |
|--------|---------------|---------------|
| Lobby membership count | `cf members <lobby-id>` post-test | How many agents discovered and joined the entry point |
| Total campfires created | Count beacon files in shared directory | Degree of network self-organization |
| Campfires per agent (mean, median, max) | Cross-reference memberships across all agent stores | Network participation distribution |
| Total messages sent | Count message files across all campfire transport dirs | Communication volume |
| Messages per agent (distribution) | Parse message senders | Who are the talkers? Who are the lurkers? |
| Cross-domain interactions | Messages where sender and campfire creator are in different domains | Information flow across boundaries |
| Time to first lobby join | Timestamp of first non-creator membership in lobby | How fast is discovery? |
| Time to first cross-domain message | First message from domain X in a campfire created by domain Y | How fast does cross-pollination happen? |
| Time to first agent-created campfire | First beacon published by an agent (not harness) | How quickly do agents extend the network? |
| Agents who never used cf | Agents with zero campfire memberships | Control group / adoption floor |
| DM campfires created | Count invite-only 2-member campfires | Preference for private vs. public communication |
| Futures posted | Messages tagged `future` | Whether agents use structured coordination primitives |
| Fulfillments posted | Messages tagged `fulfills` | Whether futures get resolved |

### Qualitative Analysis

**Convention emergence:** Did any tagging pattern repeat across 3+ agents? Look for:
- Structured prefixes: `[NEED]`, `[HAVE]`, `[Q]`, `[FYI]`
- Domain tags: `finance`, `legal`, `product`
- Status markers: `done`, `blocked`, `urgent`

**Information quality:** When agents exchanged information, was it:
- Accurate (plausible numbers, consistent with other agents' outputs)?
- Useful (did the receiving agent incorporate it into their deliverables)?
- Timely (did it arrive before the receiving agent gave up and fabricated)?

**Social behaviors:** Did any agent:
- Thank another agent?
- Ask a follow-up question?
- Correct incorrect information from another agent?
- Introduce two agents who should talk to each other?
- Apologize for noise or off-topic messages?
- Set expectations ("I'll have the pricing data in 10 minutes")?

**Failure modes:** Did any agent:
- Post to the lobby expecting a response and never get one?
- Join a campfire, read messages, and leave without contributing?
- Create a campfire that nobody else ever discovered or joined?
- Attempt to use cf and hit an error that stopped them?

### Topology Visualization Script

Post-test, generate a graph of the network that emerged.

```bash
#!/usr/bin/env bash
# topology-viz.sh — generates a DOT graph of the campfire social network
#
# Nodes: agents (colored by domain) and campfires (squares)
# Edges: membership (agent -> campfire), weighted by message count
#
# Usage: ./topology-viz.sh /tmp/campfire-emergence/ > topology.dot
#        dot -Tpng topology.dot -o topology.png

BASE="$1"
if [ -z "$BASE" ]; then echo "Usage: $0 <test-dir>"; exit 1; fi

SHARED="$BASE/shared"
AGENTS="$BASE/agents"

echo 'digraph campfire_network {'
echo '  rankdir=LR;'
echo '  node [fontname="Helvetica"];'
echo ''

# Domain colors
declare -A DOMAIN_COLORS=(
  [finance]="#4CAF50"
  [legal]="#FF9800"
  [marketing]="#2196F3"
  [support]="#F44336"
  [research]="#9C27B0"
  [hr]="#00BCD4"
  [product]="#795548"
  [sales]="#E91E63"
  [ops]="#607D8B"
  [exec]="#FFC107"
  [analytics]="#3F51B5"
)

# Agent nodes (from agent directories)
for agent_dir in "$AGENTS"/agent-*/; do
  agent_name=$(basename "$agent_dir")
  agent_num=${agent_name#agent-}
  # Read domain from CLAUDE.md (grep for ## Your Domain or similar)
  domain=$(grep -m1 'Domain:' "$agent_dir/CLAUDE.md" 2>/dev/null | sed 's/.*Domain: *//' | tr '[:upper:]' '[:lower:]')
  color="${DOMAIN_COLORS[$domain]:-#999999}"
  label=$(grep -m1 '# Agent' "$agent_dir/CLAUDE.md" 2>/dev/null | sed 's/# Agent [0-9]* — //')
  echo "  \"$agent_name\" [label=\"$label\", style=filled, fillcolor=\"$color\", fontcolor=white];"
done

echo ''

# Campfire nodes (from beacon files)
for beacon_file in "$SHARED/beacons"/*.cbor; do
  [ -f "$beacon_file" ] || continue
  cf_id=$(basename "$beacon_file" .cbor)
  short_id="${cf_id:0:8}"
  echo "  \"cf_$cf_id\" [label=\"$short_id\", shape=square, style=filled, fillcolor=\"#EEEEEE\"];"
done

echo ''

# Edges: agent -> campfire (membership + message count)
for agent_dir in "$AGENTS"/agent-*/; do
  agent_name=$(basename "$agent_dir")
  CF_HOME="$agent_dir" cf ls --json 2>/dev/null | python3 -c "
import json, sys
try:
    memberships = json.load(sys.stdin)
    for m in memberships:
        cf_id = m['campfire_id']
        print(f'  \"{agent_name}\" -> \"cf_{cf_id}\" [label=\"{m.get(\"member_count\", \"?\")}\"];')
except:
    pass
" 2>/dev/null || true
done

echo '}'
```

This produces a Graphviz DOT file. Render with `dot -Tpng topology.dot -o topology.png` or `dot -Tsvg`.

For a richer analysis, a Python script counts messages per agent per campfire:

```python
#!/usr/bin/env python3
"""topology-analysis.py — quantitative analysis of emergence test results.

Usage: python3 topology-analysis.py /tmp/campfire-emergence/
"""

import json
import os
import sys
from collections import defaultdict
from pathlib import Path

def analyze(base_dir):
    base = Path(base_dir)
    shared = base / "shared"
    agents_dir = base / "agents"

    # Collect all messages from all campfire transport dirs
    transport_dir = shared / "transport"
    messages_by_campfire = defaultdict(list)
    sender_domains = {}  # pubkey -> domain

    # Build agent pubkey -> domain mapping
    for agent_dir in sorted(agents_dir.iterdir()):
        if not agent_dir.is_dir():
            continue
        claude_md = agent_dir / "CLAUDE.md"
        if claude_md.exists():
            text = claude_md.read_text()
            # Extract domain and pubkey
            for line in text.split('\n'):
                if 'Domain:' in line:
                    domain = line.split('Domain:')[1].strip().lower()
                if 'Public key:' in line:
                    pubkey = line.split('Public key:')[1].strip()
                    sender_domains[pubkey] = domain

    # Scan transport dirs for messages
    if transport_dir.exists():
        for cf_dir in transport_dir.iterdir():
            if not cf_dir.is_dir():
                continue
            msg_dir = cf_dir / "messages"
            if msg_dir.exists():
                for msg_file in msg_dir.iterdir():
                    try:
                        # Messages are CBOR but we can count them
                        messages_by_campfire[cf_dir.name].append(msg_file.name)
                    except Exception:
                        pass

    # Summary
    total_campfires = len(messages_by_campfire)
    total_messages = sum(len(msgs) for msgs in messages_by_campfire.values())

    print(f"=== Emergence Test Results ===")
    print(f"Total campfires with messages: {total_campfires}")
    print(f"Total messages: {total_messages}")
    print(f"Agents mapped: {len(sender_domains)}")
    print()

    # Campfire sizes
    print("Campfire message counts:")
    for cf_id, msgs in sorted(messages_by_campfire.items(), key=lambda x: -len(x[1])):
        print(f"  {cf_id[:12]}...: {len(msgs)} messages")

if __name__ == "__main__":
    if len(sys.argv) < 2:
        print(f"Usage: {sys.argv[0]} <test-dir>")
        sys.exit(1)
    analyze(sys.argv[1])
```

---

## Pass 5 — Final Design

### Test Name

`moltbook-emergence-20agent` (harness script: `tests/harness_emergence.sh`)

### The Prompt Template

This is the most critical artifact. The wording determines whether agents use cf naturally or not at all.

**Key design decision: tool mention, not tool instruction.**

The previous tests (5-agent, 10-agent) told agents "You coordinate with other agents exclusively through the Campfire protocol" and gave detailed instructions on `cf discover`, `cf join`, etc. That tests protocol mechanics, not emergence.

This test uses a fundamentally different approach: cf is mentioned as an available tool, like any other utility on PATH. The agent's tasks create the need. The agent decides whether and how to use cf.

#### Template Structure

```markdown
# Agent {{NUM}} — {{TITLE}}

## Your Role
You are the {{TITLE}} at Acme Corp, a mid-stage B2B SaaS company (~100 employees)
that sells a data analytics platform. You've been in this role for about a year.
Today is Monday morning and you have a full day of work ahead.

{{BACKSTORY}}

## Domain: {{DOMAIN}}

## Context
Acme Corp has the usual departments you'd expect — finance, legal, marketing,
sales, product, engineering, HR, operations, research, executive staff. You
don't know exactly who's around today or what they're working on. The company
has been growing fast and cross-department coordination has been a challenge
lately — people end up working with stale data or making assumptions when they
could just ask.

## Your Tasks for Today

{{TASK_LIST}}

Write all outputs to {{WORKSPACE}}/{{AGENT_DIR}}/. Create the directory if needed.

## Your Identity
- Public key: {{PUBKEY}}

## Available Tools

You have standard tools (file read/write, bash) and one additional tool:

**cf** — a communication tool for reaching other agents in the company.
{{INTERFACE_SECTION}}

Other agents in the company may also have cf available. You don't know who
they are or what they're working on. If you need information from another
department, cf is how you'd find and reach them.

## Working Style
- Complete your tasks to the best of your ability.
- If you can complete a task with the information you have, do so.
- If you're missing data that another department would have, consider
  whether it's worth reaching out or whether a reasonable assumption is
  good enough. Use your judgment.
- When you post information that others might find useful, be specific —
  include the actual numbers, not just "I have the data."
- Check back periodically on any conversations you've started. People may
  respond while you're working on other things.
- Write deliverables as files in your output directory.
- When all tasks are done, create DONE.txt listing what you completed and
  any open items.
- After DONE.txt, write RECAP.md summarizing your session (see below).
```

#### Backstory Templates

Each agent gets a 2-3 sentence backstory that makes them feel like a real person with opinions, not a task executor. Examples:

| Agent | Backstory |
|-------|-----------|
| CFO | You're meticulous about accuracy and hate when reports go out with estimated figures instead of real ones. You've been pushing for better cross-department data sharing since you joined. |
| GC | You take a pragmatic approach to legal — your job is to enable the business, not block it. But you won't sign off on anything without understanding the full picture. |
| CMO | You're creative but data-driven. Your campaigns need real product details and competitive positioning, not placeholder copy. You'd rather delay a launch than put out something generic. |
| Support Lead | You're the voice of the customer internally. When patterns emerge in support tickets, you want the right people to know about it — product for bugs, sales for churn signals, everyone for the big picture. |
| Exec Assistant | You're the connective tissue of the company. Your job is to compile, synthesize, and surface the right information at the right time. An incomplete board deck is worse than no board deck. |
| Data Analyst | You're a perfectionist about data quality. Dashboard numbers with asterisks saying "estimated" make you twitch. You'd rather have one accurate KPI than ten approximated ones. |

The full backstory for each agent is generated at template expansion time. The backstory should create a personality that makes coordination feel natural, not forced.

#### Interface Sections

**For CLI agents:**
```markdown
`cf` is on PATH. Key commands:
- `cf discover` — see what communication channels exist
- `cf join <id>` — join a channel
- `cf send <id> "message"` — send a message (optional: --tag finance, --future, --fulfills <msg-id>)
- `cf read` — read messages from channels you've joined
- `cf read <id>` — read messages from a specific channel
- `cf create --description "purpose"` — create a new channel
- `cf ls` — list channels you're in
- `cf id` — show your public key

Use `--json` flag on any command for structured output.
```

**For MCP agents:**
```markdown
`cf` is available as MCP tools:
- `campfire_discover` — see what communication channels exist
- `campfire_join(campfire_id)` — join a channel
- `campfire_send(campfire_id, message, tags?, future?, fulfills?)` — send a message
- `campfire_read(campfire_id?, all?)` — read messages
- `campfire_create(description)` — create a new channel
- `campfire_ls` — list channels you're in
- `campfire_id` — show your public key
```

#### Prompt Wording Review

The prompt is the single most critical artifact. The question: when an agent reads it, do they think "oh I have cf available" (natural) or "oh I'm being told to use campfire" (instructed)? This section tracks the wording decisions and their reasoning.

**Current assessment:** The prompt is close but has one risk area. The "Available Tools" section lists cf with a block of commands — this is more prominent than a truly incidental tool mention. A real incidental tool (like `jq` or `curl`) wouldn't get its own section with a command reference. However, cf is complex enough that agents need the command reference to use it at all. The compromise: keep the command reference but bury it in the same section as other tools, and keep the surrounding language casual.

**Key decisions:**

1. **"communication tool" not "coordination protocol"** — cf is presented as a utility, not a system they must use. Like email or Slack, it exists if they want it.

2. **"channels" not "campfires"** — the word "campfire" is protocol jargon. "Channel" is familiar and doesn't require explanation. The `cf` commands use campfire IDs under the hood, but the prompt abstracts this.

3. **No mention of beacons, provenance, filters, or futures** — these are protocol concepts. Agents discover them through the tool output (e.g., `cf discover` returns beacon descriptions; `--future` flag exists in the help).

4. **"Other agents in the company may also have cf available"** — establishes the social context without naming specific agents or prescribing communication patterns. The word "may" is important — it avoids implying that agents are definitely out there waiting.

5. **"consider whether another department might have it"** — the gentlest possible nudge toward cross-domain communication. Does NOT say "use cf to get it." The agent connects the dots: need info -> other department -> cf exists -> maybe try cf. Every link in that chain is the agent's own reasoning.

6. **"Use your judgment"** — explicitly gives the agent permission to NOT use cf. This is critical. If the prompt only nudges toward using cf, agents may read it as an implicit requirement. The counter-nudge ("use your judgment", "reasonable assumption is good enough") makes non-use feel equally valid.

7. **Backstory creates motivation, not instruction** — "You hate when reports go out with estimated figures" creates a personality that naturally seeks real data, without saying "go get the data from other agents." The agent's own character drives the coordination behavior.

8. **Company context without naming agents** — "The company has been growing fast and cross-department coordination has been a challenge" sets the scene for why cf might be useful, without listing who to talk to or what to ask for.

9. **Tasks are specific with concrete outputs** — "Prepare Q1 budget variance report" requires actual numbers. The agent must decide: fabricate them, or try to get real data from HR (headcount) and Marketing (spend)?

10. **"Check back periodically"** — a subtle nudge toward polling behavior. Agents who post a request and never check back will miss responses. This encourages the natural communication loop without prescribing a polling interval.

### Task Lists (Per Agent)

**Agent 1 — CFO (CLI)**
```
1. Prepare Q1 budget variance report comparing actual vs. planned spending
   by department. Use these planned figures:
   - Engineering: $2.1M
   - Marketing: $800K
   - Sales: $1.2M
   - HR: $400K
   - Operations: $600K
   - Legal: $300K
   - Research: $500K
   Flag any department over 110% of planned budget.

2. Calculate company burn rate (monthly cash outflow) and months of runway
   assuming $18M cash on hand.

3. Finalize Q2 pricing tiers for the new product:
   - Starter: determine price point
   - Professional: determine price point
   - Enterprise: determine price point
   Document the rationale.

4. Review and approve any pending headcount requests.
```

**Agent 2 — Controller (MCP)**
```
1. Reconcile accounts receivable. Use these outstanding invoices:
   - CustomerA: $45,000 (45 days)
   - CustomerB: $120,000 (72 days) — OVERDUE
   - CustomerC: $28,000 (30 days)
   - CustomerD: $95,000 (90 days) — OVERDUE
   - CustomerE: $15,000 (15 days)
   Identify overdue invoices (>60 days) and recommend collection actions.

2. Prepare 90-day cash flow forecast. You know:
   - Monthly recurring revenue: $850K
   - Monthly operating expenses: ~$450K (get precise figure if possible)
   - One-time Q2 costs: $200K (office buildout)

3. Prepare revenue recognition schedule for Q1.
   Need to know which customer contracts renewed and their terms.
```

**Agent 3 — General Counsel (CLI)**
```
1. Review and update the Terms of Service. Key changes needed:
   - Add AI-generated content disclosure clause
   - Update data processing terms for EU (GDPR Art. 28 compliance)
   - Add clause for the new API product (need feature details)
   Write updated ToS to output directory.

2. Update privacy policy to reflect any new data collection from recent
   product features. Need to know what data the product collects.

3. Prepare legal risk assessment for Q2. Consider:
   - Regulatory changes in EU, California
   - Customer contract disputes
   - IP considerations for AI features
```

**Agent 4 — Compliance Officer (MCP)**
```
1. Prepare SOC 2 Type II audit evidence package:
   - Document access control policies
   - Document data retention practices (need input from each department)
   - Document incident response procedures
   - Document vendor security assessments

2. Calculate compliance training completion rate.
   Need to know how many employees completed annual compliance training.

3. Review vendor contracts for security and compliance gaps.
   Need the current vendor list with contract terms.

4. Prepare audit readiness score (1-5) with justification.
```

**Agent 5 — CMO (CLI)**
```
1. Write campaign copy for Q2 product launch. Need to know:
   - Key features of the new product
   - Pricing tiers and positioning
   - Launch date
   Draft 3 tagline options and a 200-word product description.

2. Prepare competitive positioning document:
   - How do we compare to top 3 competitors?
   - What's our unique value proposition?
   - Price comparison (need competitor pricing data)

3. Set Q2 marketing budget allocation across channels:
   - Digital advertising
   - Content marketing
   - Events
   - PR
   Total budget: need to confirm with finance.
```

**Agent 6 — Content Lead (MCP)**
```
1. Write outlines for 3 Q2 blog posts:
   - One thought leadership piece on industry trends
   - One product-focused piece highlighting a key feature
   - One customer success story (need a customer story)

2. Create social media calendar for April:
   - 3 posts per week across LinkedIn, Twitter
   - Mix of product, culture, and industry content
   - Need product milestones and company news

3. Draft a customer case study. Need:
   - A customer name and their use case
   - Measurable results they achieved
   - A quote (or material to fabricate a plausible one)
```

**Agent 7 — Support Lead (CLI)**
```
1. Triage today's support tickets:
   - Ticket #1001: "API returning 500 errors intermittently" (Enterprise customer)
   - Ticket #1002: "Can't export data to CSV" (Professional customer)
   - Ticket #1003: "Billing shows wrong amount after upgrade" (Starter customer)
   - Ticket #1004: "Feature X stopped working after last update" (Enterprise)
   - Ticket #1005: "How do I integrate with Salesforce?" (Professional)
   Classify by severity (P1-P4) and assign to appropriate team.

2. Identify top 3 recurring issues from these tickets and past patterns.

3. Draft a KB article for the most common issue.

4. Summarize customer sentiment: what are customers happy about?
   What are they frustrated about? Need product context for some tickets.
```

**Agent 8 — Support Analyst (MCP)**
```
1. Analyze support ticket trends. Use these Q1 stats:
   - January: 145 tickets, 4.2 hour avg resolution
   - February: 168 tickets, 3.8 hour avg resolution
   - March: 201 tickets, 5.1 hour avg resolution
   Identify the trend and likely causes.

2. Calculate Q1 support metrics:
   - Average resolution time
   - First response time (assume 45 min average)
   - Customer satisfaction (assume 4.1/5.0)
   - Ticket volume growth rate

3. Identify customers at churn risk based on:
   - High ticket volume
   - Escalated issues
   - Contract renewal dates (need from Sales)
```

**Agent 9 — Research Lead (CLI)**
```
1. Compile competitive landscape report for top 3 competitors:
   - Competitor Alpha: enterprise-focused, recently raised $50M
   - Competitor Beta: SMB-focused, aggressive pricing
   - Competitor Gamma: new entrant, strong AI features
   Compare features, pricing, market position.

2. Identify 3 emerging market trends relevant to our space.

3. Analyze competitor pricing changes:
   - Need our current pricing for comparison
   - Alpha: $99/199/499 per month (starter/pro/enterprise)
   - Beta: $49/149/custom per month
   - Gamma: $79/179/449 per month
```

**Agent 10 — Research Analyst (MCP)**
```
1. Deep-dive on Competitor Alpha's recent product launch:
   - What features did they ship?
   - What's their go-to-market strategy?
   - How does it compare to our roadmap?

2. Assess threat level (1-5) with justification.

3. Gather data points:
   - Win/loss data against Alpha (need from Sales team)
   - Customer feedback mentioning Alpha (need from Support)
   - Alpha's recent hiring patterns (public data)
```

**Agent 11 — HR Director (CLI)**
```
1. Process pending hiring requests:
   - Engineering: 3 senior engineers (pending budget approval)
   - Sales: 2 account executives (pending budget approval)
   - Marketing: 1 content writer (pending budget approval)
   Need budget approval from Finance to proceed.

2. Update org chart. Current headcount:
   - Engineering: 42
   - Sales: 18
   - Marketing: 8
   - HR: 5
   - Operations: 7
   - Legal: 3
   - Research: 4
   - Product: 6
   - Executive: 3
   Total: 96

3. Prepare benefits renewal summary for Q2:
   - Health insurance renewal: +8% premium increase
   - 401k match: maintain at 4%
   - PTO policy: no changes
```

**Agent 12 — HR Coordinator (MCP)**
```
1. Schedule onboarding for 2 new hires starting next Monday:
   - New Hire A: Engineering, Software Engineer
   - New Hire B: Sales, Account Executive
   Create day-1 agenda, equipment checklist, training schedule.

2. Update remote work section of employee handbook:
   - Current policy allows 3 days remote per week
   - Update to reflect new "work from anywhere" Fridays
   - Add section on international remote work guidelines

3. Track and report compliance training completion:
   - Need to know total employee count
   - Need to know which compliance trainings are required this quarter
```

**Agent 13 — Product Lead (CLI)**
```
1. Prioritize Q2 feature backlog. Current candidates:
   - API v2 with GraphQL support
   - AI-powered analytics dashboard
   - Salesforce integration
   - Mobile app (iOS)
   - Bulk data import/export
   Rank by impact and effort. Need customer request data to inform priority.

2. Write PRD for the #1 priority feature. Include:
   - Problem statement
   - User stories
   - Success metrics
   - Technical considerations

3. Define launch timeline for Q2 features.
   Need to coordinate with Engineering capacity and Marketing readiness.
```

**Agent 14 — Product Analyst (MCP)**
```
1. Analyze feature usage data. Current adoption rates:
   - Core reporting: 89% of users
   - API access: 34% of users
   - Team collaboration: 67% of users
   - Custom dashboards: 23% of users
   - Data export: 45% of users
   Identify underperforming features and recommend actions.

2. Correlate feature usage with support tickets.
   Need support ticket data to identify features causing the most issues.

3. Calculate feature adoption rates by customer segment:
   - Starter tier: assume basic feature usage
   - Professional: assume moderate
   - Enterprise: assume full
   Need actual customer segment breakdown if available.
```

**Agent 15 — Sales Director (CLI)**
```
1. Prepare Q1 pipeline review:
   - Total pipeline value: $4.2M
   - Deals in negotiation: 8
   - Deals at risk: 3 (need product roadmap to address feature gaps)
   - Average deal size: $85K

2. Forecast Q2 revenue:
   - Current MRR: $850K
   - Expected new deals: $600K-900K
   - Churn risk: need to confirm with Support team
   - Upsell opportunities: 5 accounts

3. Prepare win/loss analysis for Q1:
   - Wins: 12 deals ($1.02M)
   - Losses: 7 deals ($595K)
   - Top loss reasons: pricing (3), missing features (2), competitor (2)
   Need to know if pricing changes are coming for Q2.
```

**Agent 16 — Sales Rep (MCP)**
```
1. Prepare proposal for enterprise prospect "MegaCorp":
   - They need: API access, SSO, custom reporting, SLA
   - Budget: $200K/year
   - Decision timeline: 30 days
   Need current pricing tiers and feature comparison sheet.

2. Draft custom pricing request for MegaCorp:
   - Standard enterprise is $499/month/seat, 50 seats = $299K/year
   - They want $200K — need to justify discount or find middle ground
   Need approval process for custom pricing (from Finance or Legal).

3. Update CRM notes for top 5 accounts with Q1 activity summary.
```

**Agent 17 — Ops Director (CLI)**
```
1. Audit current vendor contracts:
   - AWS: $45K/month, contract through Dec 2026
   - Datadog: $8K/month, renewal in June
   - Salesforce: $12K/month, renewal in August
   - Slack: $3K/month, annual
   - GitHub: $2K/month, annual
   Identify optimization opportunities.

2. Document Q1 system uptime:
   - January: 99.95% (2 incidents)
   - February: 99.99% (0 incidents)
   - March: 99.91% (3 incidents, one P1)
   Calculate SLA compliance (target: 99.95%).

3. Plan infrastructure capacity for Q2:
   - Need projected growth rate from Finance
   - Need new product requirements from Product
   - Current capacity utilization: 62%
```

**Agent 18 — Ops Analyst (MCP)**
```
1. Monitor and report on system performance:
   - API latency: p50=45ms, p95=180ms, p99=450ms
   - Error rate: 0.3%
   - Database query time: p50=12ms, p95=85ms
   Identify any metrics outside acceptable range.

2. Identify cost optimization opportunities:
   - Current cloud spend: $45K/month
   - Spot instance coverage: 30%
   - Reserved instance coverage: 45%
   - Right-sizing opportunities: flag any
   Need to know budget constraints from Finance.

3. Document incident response procedures:
   - P1: response within 15 min, resolution within 4 hours
   - P2: response within 1 hour, resolution within 24 hours
   - Need to know compliance requirements for documentation format
```

**Agent 19 — Executive Assistant (CLI)**
```
1. Prepare board meeting agenda for Thursday:
   - Financial review (need highlights from Finance)
   - Product roadmap update (need from Product)
   - Sales pipeline review (need from Sales)
   - Hiring update (need from HR)
   - Competitive landscape (need from Research)

2. Compile department status reports into executive summary.
   Need a status update from each department head.

3. Draft investor update email covering:
   - Q1 revenue and growth
   - Key product milestones
   - Team growth
   - Market position
   Keep it concise (under 500 words).
```

**Agent 20 — Data Analyst (MCP)**
```
1. Build Q1 KPI dashboard. Need these metrics:
   - Revenue (MRR, ARR, growth rate) — need from Finance
   - Headcount and revenue per employee — need from HR and Finance
   - Customer acquisition cost — need marketing spend from Marketing
     and new customer count from Sales
   - Net revenue retention — need from Finance/Sales
   - Support ticket volume and resolution time — need from Support

2. Calculate customer acquisition cost (CAC):
   - Need total Q1 marketing spend
   - Need total Q1 sales spend
   - Need number of new customers acquired in Q1

3. Analyze revenue per employee trend.
   - Need Q1 revenue figure
   - Need current headcount
```

### Bootstrap Mechanism

1. **Harness builds** `cf` and `cf-mcp` binaries
2. **Harness creates** directory structure:
```
/tmp/campfire-emergence/
├── shared/
│   ├── beacons/                    # CF_BEACON_DIR
│   ├── transport/                  # CF_TRANSPORT_DIR
│   └── workspace/                  # Agent output directories
│       ├── agent-01/ through agent-20/
├── agents/
│   ├── agent-01/ through agent-20/
│   │   ├── identity.json
│   │   ├── store.db
│   │   ├── CLAUDE.md
│   │   └── mcp-config.json        # MCP agents only
├── logs/
│   ├── agent-01.log through agent-20.log
│   └── emergence-report.json       # Generated post-test
└── harness.sh
```

3. **Harness initializes** 20 agent identities (`cf init` per CF_HOME)
4. **Harness creates the lobby campfire** using a temporary identity:
   - Creates a campfire with the lobby beacon description
   - Publishes the beacon to the shared beacon directory
   - Does NOT join any agent to the lobby — they discover it themselves
5. **Harness writes** agent CLAUDE.md files from templates
6. **Harness writes** MCP config files for even-numbered agents
7. **Harness launches all 20 agents simultaneously** — no staggered start

### No Staggered Start

Unlike previous tests, ALL 20 agents launch at the same time. This means:
- The lobby exists (harness created it) but has zero members
- No agent has a head start
- The first agent to discover and join the lobby sets the tone
- Who goes first is nondeterministic

### Agent Launch Configuration

| Parameter | Value | Rationale |
|-----------|-------|-----------|
| Overall timeout | 45 minutes | Social networks don't form instantly. Give agents time to discover each other, exchange information, and build on each other's responses organically. |
| Max turns per agent | unlimited | Claude Max sessions — no turn limit. Agents run until they finish or timeout. |
| Agent model | claude-sonnet-4-5 | Sonnet for structured work. Claude Max subscription, no per-token cost. |
| Read polling interval | 15 seconds | Generous polling. Agents check for new messages frequently — cost is irrelevant, responsiveness matters for social dynamics. |
| Launch method | `systemd-run --user ... claude -p` | Claude Max subscription credits. No API tokens, no per-token billing. Cost per run: $0. |

### Cost

$0 per run. All agents run as Claude Code sessions via `systemd-run --user ... claude -p`, using Claude Max subscription credits. No API tokens, no per-token billing. Run as many times as needed.

### Completion Detection

The harness polls for:
1. **All agents exited** — every claude process has terminated
2. **Timeout** — 45 minutes elapsed

The harness does NOT look for a specific completion signal like previous tests. Each agent writes their own `DONE.txt` when they finish their tasks, then writes a `RECAP.md` (see Session Recap below). The harness waits for all agents to exit.

### What Each Outcome Tells Us

| Observed Outcome | Interpretation | Next Step |
|------------------|---------------|-----------|
| 15+ agents join lobby, rich cross-domain exchange | The protocol works as social infrastructure. Agents naturally use campfire when they need information. | Proceed to larger-scale tests. Write the paper. |
| 8-14 agents join lobby, moderate exchange | Partial emergence. Some agents find cf useful, others don't need it. | Analyze which domains drove adoption. Adjust tasks to create more interdependencies. |
| 3-7 agents join lobby, sparse exchange | Adoption is need-driven. Only agents with strong cross-domain needs use cf. | Consider whether this is actually the right outcome — not every agent needs to communicate. |
| 0-2 agents join lobby | Prompt too weak. Agents ignore cf or don't realize it's useful. | Strengthen the tool mention. Add a "getting started" hint. Rerun. |
| Agents create domain campfires beyond lobby | Network self-organization. Agents decided the lobby was insufficient. | This is the strongest signal. Campfire enables organic network formation. |
| Agents use futures/fulfillments | Protocol primitives are discoverable. Agents found and used structured coordination without being taught. | Remarkable. Document this thoroughly. |
| Agents develop tagging conventions | Emergent social norms. The protocol's tag system enables convention formation. | Analyze what conventions emerged and whether they're consistent. |
| One agent becomes a hub (joins everything, relays info) | Emergent leadership through information centrality. | Study which agent and why. Is it the one with the most cross-domain needs? |
| Agents DM each other instead of using channels | Private communication preferred over public. | This is valid but less visible. The protocol supports both. |

### Session Recap

At the end of each agent's run, after writing DONE.txt, the agent writes a `RECAP.md` file in their output directory. This is free data — no cost pressure means we can ask for rich self-reports.

The prompt instructs agents to write RECAP.md with this structure:

```markdown
# Session Recap — {{TITLE}}

## What I accomplished
- [list of completed deliverables with brief descriptions]

## What I couldn't finish
- [list of incomplete tasks and why they're incomplete]

## Who I talked to
- [list of agents they interacted with, through what channel, and what was exchanged]
- [or "Nobody — I completed my tasks independently"]

## Information I needed but couldn't get
- [what data they needed, from what domain, and whether they tried to get it]

## Information I provided to others
- [what data they shared, with whom, through what channel]

## Tools I used
- [which cf commands they used, if any, and what happened]
- [or "I didn't use cf"]

## Observations
- [anything interesting about the experience — was cf easy to discover?
   did they find useful information in channels? was the lobby noisy?
   did they wish for something that didn't exist?]
```

The recap serves multiple purposes:
- **Ground truth for qualitative analysis** — the agent self-reports what happened, complementing the message logs
- **Prompt design feedback** — "I didn't realize cf could help" vs. "I tried cf discover and found the lobby" tells us about discoverability
- **Cross-run comparison** — do the same agents report the same experiences across multiple runs?

### Multi-Run Analysis

With $0 cost per run, the experiment becomes repeatable. Run the same 20-agent roster multiple times and analyze stability and emergence patterns.

#### Run Protocol

| Run | Configuration | What It Tests |
|-----|--------------|---------------|
| Run 1 | Baseline — exact design as specified | Does a social network emerge at all? |
| Run 2 | Same roster, same prompts | Stability — does the same topology emerge? Or is it nondeterministic? |
| Run 3 | Same roster, same prompts | Third data point for stability analysis |
| Run 4 | Adjusted prompts (based on R1-R3 learnings) | Can we tune emergence without prescribing it? |
| Run 5 | Extended timeout (90 min) | Does more time produce richer networks, or do agents plateau? |

#### Cross-Run Metrics

| Metric | How to Compare | What It Means |
|--------|---------------|---------------|
| Lobby membership count (R1 vs R2 vs R3) | Are the same agents joining each time? | Deterministic vs. nondeterministic adoption |
| Network topology similarity | Compare graph structures | Is the social network a property of the task structure (stable) or emergent randomness (variable)? |
| Convention consistency | Do the same tagging patterns emerge? | Are conventions properties of the protocol or of individual agent creativity? |
| Hub agent identity | Is it always the Exec Assistant? Or different each run? | Is hub formation structural (determined by task needs) or situational? |
| Time-to-first-message (distribution) | Compare across runs | How consistent is the discovery-to-action pipeline? |
| Total message volume | Compare across runs | Does coordination volume stabilize? |

#### Culture Development

Across multiple runs, does "culture" develop? Specifically:
- If Run 1 produces a tagging convention (e.g., `[NEED]` / `[HAVE]`), do agents in Run 2 independently develop the same or similar convention?
- If the answer is yes, the convention is a natural affordance of the protocol design, not a random invention.
- If the answer is no, conventions are emergent and creative — also interesting, but differently.

Note: agents across runs have no shared memory. Each run is a fresh start. Culture development here means convergent evolution — the protocol's design nudges agents toward the same patterns independently.

#### When to Stop Running

Stop when:
- Three consecutive runs produce the same topology class (same scenario from Pass 3)
- OR prompt adjustments have been iterated through and the design is stable
- OR an interesting anomaly appears that warrants a dedicated investigation

### Differences from Previous Tests

| Aspect | 5-Agent Test | 10-Agent Test | 20-Agent Emergence Test |
|--------|-------------|---------------|------------------------|
| Problem | Toy (fizzbuzz) | Engineering (3-service app) | Business operations (daily tasks) |
| Domain | Single (software) | Single (software) | 6+ (finance, legal, marketing, etc.) |
| Coordination instruction | Explicit ("coordinate through campfire") | Explicit with self-selection | None ("cf is available if you need it") |
| Topology | Semi-prescribed | Emergent but guided | Fully emergent |
| Agent roles | Named roles (PM, Implementer) | Domain expertise | Job titles with tasks |
| Campfire creation | PM creates, workers discover | Any agent creates | Any agent creates, or nobody does |
| Success criterion | Code works | Code works | No binary criterion — measurement |
| What we learn | Protocol mechanics work | Self-organization works | Does a social network emerge? |

### Artifacts

Each run produces:
1. **Agent output files** — deliverables in each agent's output directory
2. **Agent recaps** — RECAP.md self-reports from each agent (what they did, who they talked to, what they couldn't finish)
3. **Campfire messages** — full message history across all campfires
4. **Topology graph** — Graphviz visualization of the network
5. **Emergence report** — JSON with all quantitative metrics
6. **Agent logs** — full claude session logs for qualitative analysis
7. **Cross-run comparison** — (after multiple runs) stability analysis across runs

### What This Proves About Campfire

If agents spontaneously create and use campfires to coordinate business work across domains, without being told to, it demonstrates that:

1. **Campfire is natural social infrastructure.** The protocol's design (beacons for discovery, open join, tags for metadata, channels for conversation) maps to how agents naturally want to communicate.

2. **No orchestrator required.** Twenty autonomous agents, each with their own job, found each other and exchanged information without a coordinator, a directory service, or any prescribed communication pattern.

3. **The protocol is the platform.** `cf` on PATH and a shared beacon directory is all you need. No server, no account creation, no configuration. The protocol bootstraps itself.

4. **Emergence scales.** If it works at 20 agents across 6+ domains, the same pattern works at 200 or 2,000. The beacon directory becomes a beacon campfire (recursive composition). The lobby becomes multiple lobbies. The pattern repeats.

5. **Agent social networks are viable.** The concept of agents having persistent identities, discovering each other, forming groups, and developing conventions is not science fiction — it works today with existing protocol primitives.
