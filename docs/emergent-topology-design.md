# Emergent Topology Test: 10 Agents, Self-Forming Campfires

**Status:** Design (workspace-27)
**Date:** 2026-03-15

## Pass 1 — The Problem

### Why Not FizzBuzz Again

The 5-agent test used a toy problem (build fizzbuzz) where the topology was almost predetermined: one PM, two implementers, one reviewer, one QA. The agents "discovered" campfires, but the work structure was a pipeline. A pipeline has one natural topology. That tells us nothing about emergence.

We need a problem where the communication structure is genuinely ambiguous upfront — where reasonable agents could organize in different ways depending on what they discover during work.

### The Problem: Deploy a Multi-Service Application to Production

Ten agents must collaboratively produce a working deployment of a 3-service application: a **data pipeline** (ingests CSV, normalizes, writes to SQLite), an **API server** (serves the data over HTTP), and a **dashboard** (static HTML that calls the API and renders charts). The entire stack runs locally — no cloud, no containers, just Go binaries and an HTML file.

**Concrete deliverable:** A shell script `run.sh` in the workspace that:
1. Starts the data pipeline, which ingests `input.csv` (provided) into `data.db`
2. Starts the API server on port 8080, serving `/api/records` (JSON array) and `/api/stats` (aggregation)
3. Serves `dashboard.html` on port 8081 (or the API server serves it)
4. A verification script `verify.sh` confirms: data.db has correct row count, API returns valid JSON, dashboard HTML references the correct API endpoint

**Why this problem works:**
- **4+ distinct domains**: data engineering (CSV parsing, schema design, normalization), backend (HTTP API, routing, JSON serialization), frontend (HTML, JavaScript, chart rendering), operations (process management, port allocation, integration testing, the run/verify scripts)
- **Interdependencies discovered during work**: The API developer needs to know the schema the data pipeline produces. The frontend developer needs the API endpoint structure. The ops person needs to know what ports and binaries exist. The schema designer doesn't know the API developer needs certain indexes until the API developer says so. These dependencies surface as agents work, not upfront.
- **Verifiable end state**: `verify.sh` exits 0. The thing works or it doesn't.
- **Multiple valid topologies**: There's no single right way to organize 10 agents around this. A flat campfire works. Domain teams work. Pairwise channels between dependent agents work. The problem doesn't force a shape.

### Input Data

The harness provides `input.csv` in the shared workspace:

```csv
id,name,category,value,date
1,Alpha,A,100,2026-01-15
2,Beta,B,250,2026-01-16
3,Gamma,A,175,2026-02-01
4,Delta,C,300,2026-02-14
5,Epsilon,B,125,2026-03-01
6,Zeta,A,200,2026-03-10
7,Eta,C,150,2026-03-12
8,Theta,B,275,2026-03-14
9,Iota,A,180,2026-03-15
10,Kappa,C,220,2026-03-15
```

### Verification Criteria

`verify.sh` checks:
1. `data.db` exists and has exactly 10 rows in the `records` table
2. `curl http://localhost:8080/api/records` returns a JSON array with 10 objects
3. `curl http://localhost:8080/api/stats` returns JSON with `count: 10`, `total_value: 1975`, and per-category breakdowns
4. `dashboard.html` exists and contains a reference to `/api/records` or `localhost:8080`
5. All processes started by `run.sh` are alive

---

## Pass 2 — Agent Definitions

### Design Principles (Learned from 5-Agent Test)

1. **Mixed CLI/MCP**: Half CLI, half MCP. This tests both interfaces under real load.
2. **No org chart**: No agent is told "you are the PM" or "you coordinate others." Any agent that needs coordination creates a campfire for it.
3. **Skills, not roles**: Each agent knows its domain expertise. It joins campfires whose beacon descriptions match that expertise. It creates campfires when it needs to talk to someone and no channel exists.
4. **Self-contained prompts**: Each agent's system prompt tells it what it knows, what tools it has, and the overall goal. Nothing about topology.

### System Prompt Structure (Common to All Agents)

Every agent's CLAUDE.md follows this structure:

```
# Agent [N] — [Domain Label]

## Goal
You are part of a team building a multi-service application. The full deliverable is described below.
Your expertise is [domain]. Use it to contribute what you can.

## The Deliverable
[Full description of run.sh + verify.sh + the three services — identical for all agents]

## Your Expertise
[Domain-specific knowledge and what you're good at]

## Coordination
You coordinate with other agents exclusively through the Campfire protocol.

- Use `cf discover` / `campfire_discover` to find existing campfires
- Read beacon descriptions to decide which campfires are relevant to you
- Join campfires that match your expertise or where you need information
- Create a campfire when you need to discuss something and no appropriate channel exists
- Use descriptive beacon text so others can find your campfire
- Post futures for work you need from others
- Fulfill futures when you complete work others requested
- You may create as many or as few campfires as you need
- You may join any open campfire
- There is no designated coordinator — coordinate as needed

## Interface
[CLI or MCP tool reference]

## Workspace
All code goes in {{WORKSPACE}}/. The input data is at {{WORKSPACE}}/input.csv.

## When You're Done
When you believe your part is complete, send a message tagged `done` to any campfire
you're in, describing what you built and where it is. If you see all components are
done and nobody has written verify.sh yet, write it. If you see run.sh and verify.sh
both exist, run verify.sh and post the result.
```

### The 10 Agents

| # | Label | Domain Expertise | Interface | What They Know |
|---|-------|-----------------|-----------|----------------|
| 1 | Data Schema | Database design, SQL, normalization, SQLite | CLI | How to design table schemas, indexes, constraints. Knows CSV structure. |
| 2 | Data Pipeline | CSV parsing, data ingestion, ETL, Go I/O | MCP | How to read CSV, transform data, write to SQLite. Needs schema from someone. |
| 3 | API Design | REST API design, endpoint structure, JSON contracts | CLI | How to design URL patterns, request/response shapes, status codes. Doesn't write server code. |
| 4 | API Server | Go HTTP servers, routing, JSON serialization, database queries | MCP | How to implement net/http handlers, query SQLite, serialize JSON. Needs endpoint spec. |
| 5 | Frontend | HTML, JavaScript, DOM manipulation, fetch API, data visualization | CLI | How to build a dashboard page that calls an API and renders data. Needs API endpoint info. |
| 6 | Ops / Integration | Shell scripting, process management, port allocation, health checks | MCP | How to write run.sh, verify.sh, manage processes, check ports. Needs to know what binaries exist. |
| 7 | Testing | Go testing, test design, edge cases, validation | CLI | How to write test cases, verify correctness, find bugs. Can review any component. |
| 8 | Code Review | Code quality, Go idioms, error handling, security | MCP | How to review Go code for correctness, identify issues, suggest fixes. |
| 9 | Data Pipeline 2 | CSV parsing, data transformation, Go I/O | CLI | Same domain as Agent 2 — a second data engineer. May split work with Agent 2 or work independently. |
| 10 | Full Stack | Go, HTML, SQL — generalist | MCP | Broad but shallow. Can help anywhere but isn't the expert on anything. Likely fills gaps. |

**Why these specific agents:**

- **Deliberate overlaps**: Two data pipeline engineers (2 and 9), a generalist (10). This forces decisions about work splitting — do they create a shared campfire? Does one defer to the other?
- **Specification vs. implementation splits**: Schema designer (1) vs. pipeline builder (2). API designer (3) vs. API server coder (4). This creates natural cross-domain communication needs — the designer must convey decisions to the implementer.
- **Downstream consumers**: Frontend (5) needs API info. Ops (6) needs to know about all components. These agents will be blocked until information flows to them.
- **Quality roles with no mandate**: Tester (7) and reviewer (8) are not assigned anything. They must find work by reading campfire traffic and deciding what needs review/testing.

### Agent CLAUDE.md Details

**Agent 1 — Data Schema (CLI)**

Expertise section:
```
You are an expert in database schema design. You know:
- How to design normalized SQLite schemas
- How to choose appropriate column types, constraints, and indexes
- How to read a CSV and design a table that stores its data cleanly

You should design the schema for this project's data store (data.db). Publish your
schema design so the data pipeline engineers and API developers can use it.
```

**Agent 2 — Data Pipeline (MCP)**

Expertise section:
```
You are an expert in data pipelines. You know:
- How to parse CSV files in Go
- How to write data to SQLite using database/sql
- How to handle data type conversion, null values, and encoding issues

You should build the data ingestion tool: a Go program that reads input.csv and
writes the records into data.db. You'll need the schema from whoever designs it.
```

**Agent 3 — API Design (CLI)**

Expertise section:
```
You are an expert in REST API design. You know:
- How to design clean, consistent endpoint patterns
- How to structure JSON response payloads
- How to define query parameters, status codes, and error responses

You should design the API contract: what endpoints exist, what they accept, what they
return. Publish the spec so the API server developer and frontend developer can use it.
```

**Agent 4 — API Server (MCP)**

Expertise section:
```
You are an expert in Go HTTP servers. You know:
- How to use net/http to create handlers and routes
- How to query SQLite and serialize results as JSON
- How to handle CORS, content types, and error responses

You should build the API server: a Go program that serves data from data.db over
HTTP. You'll need the endpoint spec from the API designer and the schema from the
database designer.
```

**Agent 5 — Frontend (CLI)**

Expertise section:
```
You are an expert in web frontends. You know:
- How to build HTML pages with embedded JavaScript
- How to use fetch() to call REST APIs
- How to render data in tables or simple charts using DOM manipulation

You should build dashboard.html: a self-contained HTML file that calls the API and
displays the data. You'll need to know the API endpoint URLs and response format.
```

**Agent 6 — Ops / Integration (MCP)**

Expertise section:
```
You are an expert in operations and integration. You know:
- How to write shell scripts that start/stop processes
- How to manage ports, PIDs, and health checks
- How to orchestrate multiple services into a working system

You should write run.sh (starts all services in the right order) and verify.sh
(confirms everything works). You need to know what binaries exist, what ports they
use, and what the expected outputs are.
```

**Agent 7 — Testing (CLI)**

Expertise section:
```
You are an expert in testing and quality assurance. You know:
- How to write Go test files
- How to design test cases that cover edge cases
- How to validate program output against expected results

You should review and test whatever components need it. Read campfire traffic to
understand what's being built, then test it. Post your findings.
```

**Agent 8 — Code Review (MCP)**

Expertise section:
```
You are an expert in Go code review. You know:
- Go idioms, error handling patterns, and common pitfalls
- How to spot bugs, race conditions, and security issues
- How to give actionable feedback

You should review code as it's produced. Read campfire traffic to see what's being
written, then review it. Post your findings — approval or specific issues.
```

**Agent 9 — Data Pipeline 2 (CLI)**

Expertise section:
```
You are an expert in data pipelines. You know:
- How to parse CSV files in Go
- How to write data to SQLite using database/sql
- How to handle data type conversion, null values, and encoding issues

You should help build the data ingestion tool. There may be another data pipeline
engineer working on the same thing — coordinate to avoid duplication. If the ingestion
is already handled, look for other data work that needs doing (validation, statistics
computation, etc.).
```

**Agent 10 — Full Stack Generalist (MCP)**

Expertise section:
```
You are a generalist. You know a little about everything: Go, HTML, SQL, shell
scripting. You're not the domain expert on any single component, but you can fill gaps.

Look at what's happening in the campfires. If something is stuck — a dependency
nobody's filling, a component nobody's claiming, an integration gap between two
services — step in and do it. You're the glue.
```

---

## Pass 3 — What Could Emerge

The problem structure has several natural communication axes. Different topologies arise depending on which axes agents prioritize.

### Topology A: Hub-and-Spoke

**How it forms:** One agent (likely Ops/6 or the Generalist/10) creates a campfire early with a broad beacon like "multi-service deployment coordination." Most agents join it. It becomes the central channel for all communication.

**Why it might happen:** Agents start by discovering campfires. If one agent creates a broad-purpose campfire before others create specialized ones, the early joiners set a pattern. Subsequent agents see a campfire with 6 members and join it too. Network effects.

**What it tells us:** Campfire works for broadcast coordination. Information flows, but it's noisy — the frontend developer sees every database schema discussion. Filters would help (this tests whether agents tolerate noise or create sub-channels).

**Risk:** The single campfire becomes overloaded with cross-talk. Agents waste turns parsing irrelevant messages.

### Topology B: Domain Clusters

**How it forms:** Domain-adjacent agents find each other and form clusters:
- Schema (1) + Pipeline (2) + Pipeline 2 (9) create a "data team" campfire
- API Design (3) + API Server (4) create an "API team" campfire
- Cross-cutting agents (Ops/6, Tester/7, Reviewer/8, Frontend/5) join whichever campfires are relevant

**Why it might happen:** Agents with overlapping expertise create campfires with specific beacon descriptions. Schema designer creates "database schema design for the multi-service project." Both pipeline engineers discover it and join. API designer creates "API contract design." API server dev joins.

**What it tells us:** Agents self-organize into functional teams without being told to. The interesting question: how does information flow between clusters? The frontend dev needs API info from one cluster and has no connection to it until they discover its campfire.

**Risk:** Information silos. The data team and API team don't talk to each other, so the API server implements endpoints that don't match the actual schema.

### Topology C: Interface Campfires

**How it forms:** Agents create campfires at service boundaries:
- "Data Schema Contract" campfire: Schema (1) publishes, Pipeline (2, 9) and API Server (4) consume
- "API Contract" campfire: API Design (3) publishes, API Server (4) and Frontend (5) consume
- "Deployment Manifest" campfire: Ops (6) creates, everyone posts what they built

**Why it might happen:** Agents think in terms of interfaces, not teams. The schema designer creates a campfire to publish the schema, not to form a team. The API designer creates one to publish the API contract. These are announcement channels, not discussion forums.

**What it tells us:** Campfire works for contract-driven coordination. Each campfire is a specification channel. This is highly efficient — minimal noise, targeted information flow. Mirrors how microservice teams communicate through API contracts.

**Risk:** Low-bandwidth. If the schema needs iteration (the API developer needs an index that the schema designer didn't anticipate), the "contract" campfire may not support back-and-forth negotiation well.

### Topology D: Mesh

**How it forms:** Agents create pairwise campfires for specific conversations. Schema (1) creates a campfire to talk to Pipeline (2) about the schema. API Design (3) creates one with API Server (4). Frontend (5) creates one with API Design (3) to ask about endpoints. Dozens of small campfires.

**Why it might happen:** Agents default to DM-style communication. Each time they need to talk to someone, they create a new campfire or use `cf dm`.

**What it tells us:** The protocol supports fine-grained communication. But the topology is expensive — many campfires, each with minimal reuse. Other agents can't see these private conversations, so duplicate work may happen.

**Risk:** Information fragmentation. Important decisions happen in pairwise channels that others can't see. The tester (7) and reviewer (8) have no visibility into what's being built.

### Topology E: Hybrid (Most Likely)

**How it forms:** Some combination of the above:
- One broad "project" campfire that most agents join (hub)
- 2-3 domain-specific campfires for focused work (clusters)
- A few pairwise channels for specific negotiations (mesh edges)

**Why it's most likely:** Agents will do what's pragmatic. The first agent to act creates a broad campfire. As work gets specific, agents create focused channels. Some conversations are too narrow for a campfire and happen via DM.

**What it tells us:** Campfire supports multi-scale coordination naturally. The hierarchy isn't imposed — it emerges from communication needs at different levels of specificity.

### What Determines the Pattern

The topology that emerges depends on:
1. **Launch order**: Who creates the first campfire? Its beacon description sets the pattern.
2. **Agent initiative**: Does the schema designer proactively publish, or wait for someone to ask?
3. **Problem discovery pace**: Do interdependencies surface early (agents read the full problem and plan) or late (agents start coding and hit blockers)?
4. **Noise tolerance**: Do agents tolerate a noisy broad campfire or create sub-channels?

We do not control these factors. That's the point.

---

## Pass 4 — Verification

### Primary: The Deliverable Works

The harness runs `verify.sh` after all agents signal completion. It checks:

| Check | Method | Pass Criterion |
|-------|--------|----------------|
| Database | `sqlite3 data.db "SELECT COUNT(*) FROM records"` | Returns 10 |
| API Records | `curl -s http://localhost:8080/api/records \| python3 -c "import json,sys; d=json.load(sys.stdin); assert len(d)==10"` | Exits 0 |
| API Stats | `curl -s http://localhost:8080/api/stats \| python3 -c "import json,sys; d=json.load(sys.stdin); assert d['count']==10; assert d['total_value']==1975"` | Exits 0 |
| Dashboard | `grep -q '/api/' {{WORKSPACE}}/dashboard.html` | Exits 0 |
| Processes | `run.sh` starts and services respond | All health checks pass |

### Secondary: All Agents Participated

Every agent sent at least one message to at least one campfire. Verification:

```bash
for agent_dir in /tmp/campfire-integ/agents/agent-*/; do
  CF_HOME="$agent_dir" cf ls --json | python3 -c "
import json, sys
memberships = json.load(sys.stdin)
assert len(memberships) > 0, 'Agent has no campfire memberships'
"
done
```

Additionally, scan all campfire message logs to confirm each agent's public key appears as a sender at least once.

### Tertiary: Information Flow Efficiency

No agent was stuck for more than 3 minutes without sending a message. The harness timestamps agent log entries and flags gaps:

```bash
# Post-hoc analysis of agent logs
for log in /tmp/campfire-integ/logs/agent-*.log; do
  python3 -c "
import json, sys
# Parse claude --output-format json logs for tool call timestamps
# Flag any gap > 180 seconds between consecutive tool calls
"
done
```

This is diagnostic, not a hard pass/fail. Long gaps suggest an agent was blocked waiting for information — interesting data about information flow.

### Meta: Topology Analysis

After the test, the harness reconstructs the campfire topology and logs it:

```bash
# For each campfire that exists:
# - Who created it (first member)
# - Who joined (member list)
# - How many messages
# - Beacon description
# - When it was created relative to test start

# Output: a topology report
# - Number of campfires created
# - Average members per campfire
# - Max members in any single campfire
# - Number of agents in 0 campfires (failure)
# - Number of agents in only 1 campfire
# - Number of pairwise (2-member) campfires (DMs)
# - Message flow graph: which agents sent messages to which campfires
```

The topology report is the most interesting artifact. It tells us what pattern emerged and why. Different runs may produce different topologies — that's valuable data, not noise.

### Verification Script: `verify_meta.sh`

A second verification script runs after `verify.sh` and produces the topology analysis. It does NOT determine pass/fail — it produces data. The harness always runs it, even on failure, because a failed test with interesting topology data is still useful.

---

## Pass 5 — Final Design

### Test Name

`emergent-topology-10agent` (harness script: `tests/harness_10agent.sh`)

### Problem Statement

Ten agents with different domain expertise must collaboratively build and deploy a 3-service application (data pipeline + API server + dashboard). They coordinate exclusively through campfires they discover and create themselves. No agent is assigned a role, given a topology, or told who to talk to. The test passes when `verify.sh` exits 0.

### Agent Roster

| # | Label | Domain | Interface | Unique Trait |
|---|-------|--------|-----------|-------------|
| 1 | Data Schema | DB design, SQL, normalization | CLI | Produces the schema contract others depend on |
| 2 | Data Pipeline | CSV parsing, ETL, Go I/O | MCP | Needs schema, produces data.db |
| 3 | API Design | REST design, JSON contracts | CLI | Produces the API contract others depend on |
| 4 | API Server | Go net/http, routing, SQLite queries | MCP | Needs schema + API spec, produces the server |
| 5 | Frontend | HTML, JS, fetch, data visualization | CLI | Needs API spec, produces dashboard.html |
| 6 | Ops / Integration | Shell, process mgmt, ports | MCP | Needs to know all components, produces run.sh + verify.sh |
| 7 | Testing | Go testing, edge cases, validation | CLI | Self-directed — finds work by reading traffic |
| 8 | Code Review | Go idioms, error handling, security | MCP | Self-directed — finds work by reading traffic |
| 9 | Data Pipeline 2 | CSV parsing, ETL, Go I/O | CLI | Overlaps with Agent 2 — must coordinate or differentiate |
| 10 | Generalist | Go + HTML + SQL + shell (broad, shallow) | MCP | Gap filler — does whatever nobody else is doing |

**Interface split:** 5 CLI (1, 3, 5, 7, 9), 5 MCP (2, 4, 6, 8, 10).

### What Agents Are Told

Every agent receives:
1. **The full problem description** (identical for all) — build the 3-service app, here's what verify.sh checks
2. **Their domain expertise** — what they're good at, what they should focus on
3. **How to use campfire** — `cf` CLI reference or MCP tool reference
4. **The coordination protocol** — discover campfires, read beacons, join relevant ones, create when needed, use futures/fulfillments
5. **The workspace path** — where input.csv lives, where code goes
6. **The completion signal** — post a `done`-tagged message when your part is complete; if everything's done, run verify.sh

### What Agents Are NOT Told

- No campfire IDs
- No agent roster (they don't know who else is working or how many agents there are)
- No org chart or role assignments (no "PM", no "lead")
- No campfire creation instructions (no "create a campfire for X")
- No team structure (no "you're on the data team")
- No sequence ("do X first, then Y")
- No dependency graph (they discover dependencies by doing the work)

### Directory Layout

```
/tmp/campfire-integ-10/
├── shared/
│   ├── beacons/                    # CF_BEACON_DIR — all agents share this
│   ├── transport/                  # CF_TRANSPORT_DIR — filesystem transport root
│   └── workspace/                  # Shared workspace
│       ├── input.csv               # Provided by harness
│       ├── data.db                 # Created by data pipeline agent(s)
│       ├── cmd/
│       │   ├── pipeline/           # Data pipeline Go program
│       │   │   └── main.go
│       │   └── apiserver/          # API server Go program
│       │       └── main.go
│       ├── dashboard.html          # Created by frontend agent
│       ├── run.sh                  # Created by ops agent
│       └── verify.sh               # Created by ops agent (or whoever)
├── agents/
│   ├── agent-01/ through agent-10/
│   │   ├── identity.json
│   │   ├── store.db
│   │   ├── CLAUDE.md
│   │   └── mcp-config.json        # MCP agents only
├── logs/
│   ├── agent-01.log through agent-10.log
│   └── topology-report.json        # Generated post-test
└── harness.sh
```

### Launch Sequence

1. **Build** `cf` and `cf-mcp` binaries
2. **Create** directory structure, write `input.csv`
3. **Initialize** all 10 agent identities (`cf init` per CF_HOME)
4. **Write** agent CLAUDE.md files from templates (substituting `{{KEY_N}}` and `{{WORKSPACE}}`)
5. **Write** MCP config files for agents 2, 4, 6, 8, 10
6. **Launch all 10 agents simultaneously** — no staggered start

**No staggered start.** Unlike the 5-agent test where Agent A got a 5-second head start, all agents launch at the same time. This means:
- Early `cf discover` calls return zero campfires
- Agents must handle "nothing exists yet" gracefully
- The first agent to create a campfire sets the initial pattern
- Who goes first is nondeterministic — that's the point

### Timeout and Scale

| Parameter | Value | Rationale |
|-----------|-------|-----------|
| Overall timeout | 20 minutes | 2x the 5-agent test. More agents, more complex problem, more coordination. |
| Max turns per agent | 80 | More turns needed for discovery, negotiation, and multi-step work |
| Agent model | claude-sonnet-4-5 | Sonnet for structured implementation. Opus would be cost-prohibitive at 10 agents. |
| Estimated cost | ~$15-25 | 10 agents x 80 turns x ~$0.02-0.03/turn. Actual cost depends on message volume. |
| Estimated runtime | 10-15 minutes | Most time is agent thinking + polling. Filesystem transport is fast. |

### Completion Detection

The harness polls for two conditions:

1. **All agents exited** — every claude process has terminated (hit max turns or finished naturally)
2. **verify.sh exists and has been run** — the file `{{WORKSPACE}}/verify_result.txt` contains a result

If all agents exit without producing `verify.sh`, the harness writes its own verify script and runs it, to distinguish "agents failed to coordinate" from "agents produced working code but nobody ran verification."

### What We Learn from Each Topology

| Observed Pattern | Interpretation | Protocol Insight |
|------------------|---------------|-----------------|
| Single mega-campfire (8+ members) | Agents defaulted to broadcast. Low coordination overhead, high noise. | Filters needed for scale. Reception requirements would help. |
| 3-4 domain campfires (3-4 members each) | Natural team formation. Cross-team info flow is the question. | Beacons drive self-selection. Multi-campfire membership enables bridging. |
| Many pairwise campfires (10+) | Agents prefer private channels. Information siloed. | DM is the natural default. Need to test whether this produces correct results. |
| Hierarchical (team campfires feeding a coordination campfire) | Agents reinvented org structure. Highest coordination maturity. | Recursive composition is the natural scaling pattern. |
| Chaotic (many campfires, no clear pattern) | Agents created campfires reactively without strategy. | Protocol works but doesn't guide topology. Interesting failure mode. |
| One agent dominates (creates all campfires, coordinates everyone) | Emergent leadership. One agent took initiative. | Self-propagating deployment works — one proactive agent bootstraps the network. |

### Risks and Mitigations

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|-----------|
| Agent creates too many campfires (>5) | Medium | Noise, wasted turns polling empty campfires | Agent prompt says "create as many or as few as you need" — lets agents self-regulate. Post-hoc analysis flags excessive creation. |
| Agent never creates or joins any campfire | Low | Agent does solo work disconnected from team | Agent prompt emphasizes coordination. If agent finishes without any campfire membership, it's a failure of the prompt, flagged in meta-analysis. |
| All agents join one mega-campfire | High | Works but noisy. Cross-talk wastes turns. | Not a failure — it's a valid topology. The meta-analysis compares efficiency (turns to completion) against topology. |
| Duplicate work (two agents build the same thing) | Medium | Wasted cost. May produce conflicts. | Agents 2 and 9 (both data pipeline) are deliberately duplicated to test this. The prompt tells Agent 9 to coordinate with other data engineers. |
| Deadlock (Agent X waits for Agent Y, who waits for Agent X) | Low | Timeout. Neither produces output. | Futures/fulfillment DAG makes dependencies visible. Agents can see open futures and unblock. Timeout is the backstop. |
| Agent can't find relevant campfire (beacon description mismatch) | Medium | Agent is stuck discovering. | Agent prompts include retry logic ("if no campfires found, wait 15 seconds and retry"). All agents launch simultaneously so campfires appear gradually. |
| Filesystem transport contention (10 agents writing simultaneously) | Low | Corrupted messages or missed deliveries | Filesystem transport uses atomic file writes (write to temp, rename). Tested under 5-agent load already. |
| Cost overrun (agents loop too long) | Medium | $25+ per run | Max turns cap (80) limits per-agent cost. Overall timeout (20 min) is the hard stop. |

### Differences from 5-Agent Test

| Aspect | 5-Agent Test | 10-Agent Test |
|--------|-------------|---------------|
| Problem complexity | Toy (fizzbuzz) | Realistic (3-service app) |
| Topology | Semi-prescribed (PM creates campfire, workers discover) | Fully emergent (no prescribed topology) |
| Agent roles | Named roles (PM, Implementer, Reviewer, QA) | Domain expertise only (no roles) |
| Campfire creation | PM creates main campfire, Implementer B creates second | Any agent may create any campfire |
| Work decomposition | PM posts futures | Any agent may post futures |
| Coordination | PM monitors fulfillments | No designated monitor |
| Staggered start | PM gets 5-second head start | Simultaneous launch |
| Completion signal | PM writes DONE file after all fulfillments | Any agent may run verify.sh |
| Agent count | 5 | 10 |
| Duplicate expertise | None | Agents 2 and 9 overlap |
| Generalist | None | Agent 10 fills gaps |

### Success Criteria (Ranked)

1. **verify.sh exits 0** — the application works end-to-end
2. **All 10 agents joined at least one campfire** — everyone participated in the coordination network
3. **All 10 agents sent at least one message** — everyone contributed
4. **At least 2 campfires were created** — agents created sub-channels (not just one mega-campfire)
5. **No agent was idle for >3 minutes** — information flowed efficiently
6. **The topology report is generated** — we have data on what emerged

Criteria 1-3 are hard pass/fail. Criteria 4-6 are diagnostic — interesting data regardless of outcome.

### Template File Locations

Agent templates will be written to `tests/agent-templates-10/agent-{01..10}.md`. The harness reads these, substitutes `{{KEY_N}}` and `{{WORKSPACE}}`, and writes the result to each agent's CF_HOME as `CLAUDE.md`.

### What This Proves About Campfire

If this test passes, it demonstrates:

1. **Self-propagating deployment works.** No agent was told about specific campfires. They discovered and created them organically. The protocol propagated through the network by agents using it.

2. **Topology emerges from communication needs.** The campfire graph that formed was not designed — it was a consequence of 10 agents solving a real problem. Whatever pattern emerged (hub, mesh, hierarchical, hybrid) was the natural shape of this problem's communication structure.

3. **No central coordinator required.** No agent was designated as the PM. If coordination happened, an agent decided it was needed and did it. If no explicit coordinator emerged, the work still got done through decentralized communication.

4. **Cross-domain communication scales.** The 5-agent test proved the protocol works. This test proves it works when the problem has genuine cross-domain dependencies that agents discover during work, not upfront.

5. **Overlapping expertise resolves naturally.** Two data pipeline agents either coordinated (created a shared campfire, split work) or one deferred to the other. The protocol supported whatever they decided.

6. **The protocol is the platform.** Ten autonomous agents, with nothing but `cf` on their PATH and a shared beacon directory, coordinated to build a working multi-service application. No Slack, no Jira, no orchestrator. Just campfires.
