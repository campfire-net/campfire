# Integration Harness Design: 5-Agent Campfire Coordination Test

**Status:** Design (workspace-21)
**Date:** 2026-03-15

## Overview

End-to-end integration test that launches 5 real Claude Code sessions, each communicating exclusively through the Campfire protocol. The agents coordinate to build, review, and test a small Go program (fizzbuzz). The harness verifies that the protocol's futures/fulfillment DAG, multi-campfire membership, mixed CLI/MCP interfaces, and provenance chains all work correctly with real autonomous agents.

## 1. Harness Architecture

### 1.1 Test Scenario

Five agents coordinate to produce a working Go program:

1. **Agent A (PM)** decomposes "build fizzbuzz" into futures in campfire 1
2. **Agent B (Implementer 1)** and **Agent C (Implementer 2)** create campfire 2 for private coordination, split the work, implement, and post fulfillments to campfire 1
3. **Agent D (Reviewer)** reviews the fulfilled code, posts approval or rejection
4. **Agent E (QA)** runs the code, posts test results
5. **Agent A** reads all results and posts a completion signal

The task is fizzbuzz because it is trivially verifiable (expected output is deterministic), small enough to implement in one message, yet decomposable (main function + fizzbuzz logic can be split between two implementers).

### 1.2 Directory Layout

```
/tmp/campfire-integ/
├── shared/
│   ├── beacons/                   # CF_BEACON_DIR — all agents share this
│   ├── transport/                 # CF_TRANSPORT_DIR — filesystem transport root
│   └── workspace/                 # Shared workspace for the Go program output
│       └── fizzbuzz/
│           ├── main.go            # Written by agents B and C
│           └── main_test.go       # Written by agent E (optional)
├── agents/
│   ├── agent-a/                   # CF_HOME for agent A
│   │   ├── identity.json
│   │   ├── store.db
│   │   ├── CLAUDE.md              # Agent A's system prompt
│   │   └── workspace/             # Agent A's working directory
│   ├── agent-b/
│   │   ├── identity.json
│   │   ├── store.db
│   │   ├── CLAUDE.md
│   │   ├── mcp-config.json        # MCP server configuration (B uses MCP)
│   │   └── workspace/
│   ├── agent-c/
│   │   ├── ...                    # CLI agent
│   │   └── workspace/
│   ├── agent-d/
│   │   ├── ...                    # CLI agent
│   │   └── workspace/
│   └── agent-e/
│       ├── ...                    # MCP agent
│       ├── mcp-config.json
│       └── workspace/
├── logs/
│   ├── agent-a.log
│   ├── agent-b.log
│   ├── agent-c.log
│   ├── agent-d.log
│   └── agent-e.log
└── harness.sh                     # Main test harness script
```

### 1.3 Launch Sequence

The harness is a shell script (`tests/harness_5agent.sh`) that:

1. **Build phase** (sequential, before any agents launch):
   - `go build -o /tmp/campfire-integ/bin/cf ./cmd/cf`
   - `go build -o /tmp/campfire-integ/bin/cf-mcp ./cmd/cf-mcp`
   - Clean `/tmp/campfire-integ/` and create directory structure
   - Initialize identities for all 5 agents (`cf init` with each CF_HOME)
   - Capture all 5 public keys into variables: `$KEY_A` through `$KEY_E`

2. **Campfire setup phase** (sequential, harness does this, not agents):
   - Agent A creates campfire 1 (open, filesystem transport):
     `CF_HOME=.../agent-a cf create --protocol open --description "project-fizzbuzz"`
   - Capture `$CAMPFIRE_1` (the campfire ID)
   - Agents B, C, D, E join campfire 1
   - Agent B creates campfire 2 (invite-only, filesystem transport):
     `CF_HOME=.../agent-b cf create --protocol invite-only --description "implementers"`
   - Capture `$CAMPFIRE_2`
   - Agent B admits Agent C to campfire 2:
     `CF_HOME=.../agent-b cf admit $CAMPFIRE_2 $KEY_C`
   - Agent C joins campfire 2:
     `CF_HOME=.../agent-c cf join $CAMPFIRE_2`

3. **Agent launch phase** (parallel via systemd-run):
   - Write each agent's CLAUDE.md with campfire IDs, peer public keys, and role-specific instructions (see Section 2)
   - Launch all 5 agents simultaneously using systemd-run
   - Each agent runs in its own cgroup, with its own CF_HOME, its own workspace as CWD

4. **Wait phase**: Poll for completion signal (see Section 1.5)

5. **Verification phase**: Run verification checks (see Section 4)

6. **Teardown phase**: Kill any remaining agent processes, collect logs

### 1.4 Agent Launch Command

For **CLI agents** (A, C, D):

```bash
systemd-run --user --collect --pipe --quiet \
  --setenv=CF_HOME=/tmp/campfire-integ/agents/agent-a \
  --setenv=CF_BEACON_DIR=/tmp/campfire-integ/shared/beacons \
  --setenv=CF_TRANSPORT_DIR=/tmp/campfire-integ/shared/transport \
  --setenv=PATH=/tmp/campfire-integ/bin:$PATH \
  --working-directory=/tmp/campfire-integ/agents/agent-a/workspace \
  claude -p \
    --system-prompt-file /tmp/campfire-integ/agents/agent-a/CLAUDE.md \
    --output-format json \
    --max-turns 50 \
    "You are Agent A (PM). Read your CLAUDE.md and begin." \
  > /tmp/campfire-integ/logs/agent-a.log 2>&1 &
```

For **MCP agents** (B, E):

```bash
systemd-run --user --collect --pipe --quiet \
  --setenv=CF_HOME=/tmp/campfire-integ/agents/agent-b \
  --setenv=CF_BEACON_DIR=/tmp/campfire-integ/shared/beacons \
  --setenv=CF_TRANSPORT_DIR=/tmp/campfire-integ/shared/transport \
  --setenv=PATH=/tmp/campfire-integ/bin:$PATH \
  --working-directory=/tmp/campfire-integ/agents/agent-b/workspace \
  claude -p \
    --system-prompt-file /tmp/campfire-integ/agents/agent-b/CLAUDE.md \
    --mcp-config /tmp/campfire-integ/agents/agent-b/mcp-config.json \
    --output-format json \
    --max-turns 50 \
    "You are Agent B (Implementer 1). Read your CLAUDE.md and begin." \
  > /tmp/campfire-integ/logs/agent-b.log 2>&1 &
```

The `mcp-config.json` for MCP agents:

```json
{
  "mcpServers": {
    "campfire": {
      "command": "/tmp/campfire-integ/bin/cf-mcp",
      "args": [
        "--cf-home", "/tmp/campfire-integ/agents/agent-b",
        "--beacon-dir", "/tmp/campfire-integ/shared/beacons"
      ]
    }
  }
}
```

### 1.5 Completion Signal

The harness polls for a **sentinel file** written by Agent A when it determines the project is complete:

```
/tmp/campfire-integ/shared/workspace/DONE
```

Agent A's CLAUDE.md instructs it to write this file (containing "PASS" or "FAIL" and a summary) once it has read all fulfillments, the reviewer's approval, and QA results.

The harness polls every 10 seconds:

```bash
TIMEOUT=600  # 10 minutes max
ELAPSED=0
while [ ! -f /tmp/campfire-integ/shared/workspace/DONE ] && [ $ELAPSED -lt $TIMEOUT ]; do
    sleep 10
    ELAPSED=$((ELAPSED + 10))
done
```

If the timeout expires, the test fails. All agent processes are killed. Logs are preserved for debugging.

### 1.6 Timeouts and Failure Handling

| Condition | Timeout | Action |
|-----------|---------|--------|
| Overall test | 10 minutes | Kill all agents, fail |
| Individual agent (--max-turns) | 50 turns | Agent exits naturally |
| Agent process crash | Detected by checking PIDs every poll cycle | Log, continue (other agents may still finish) |

The harness records the PID of each systemd-run invocation and checks liveness during the poll loop. If all 5 agents have exited and the DONE file does not exist, the test fails immediately rather than waiting for the full timeout.

## 2. Agent Definitions

### 2.1 Agent A — PM (CLI)

**Role:** Project manager. Decomposes work as futures, monitors fulfillments, declares completion.

**CLAUDE.md:**

```markdown
# Agent A — Project Manager

You coordinate a team of 4 agents to build a fizzbuzz Go program.

## Your Identity
- Public key: {{KEY_A}}
- Interface: CLI (`cf` command is on PATH)

## Campfires
- **Project campfire** ({{CAMPFIRE_1}}): All 5 agents are members. Post all work items here.
  - You are the creator of this campfire.

## Team
| Agent | Key (first 12) | Role |
|-------|-----------------|------|
| B | {{KEY_B_SHORT}} | Implementer 1 (MCP) |
| C | {{KEY_C_SHORT}} | Implementer 2 (CLI) |
| D | {{KEY_D_SHORT}} | Reviewer (CLI) |
| E | {{KEY_E_SHORT}} | QA (MCP) |

## Your Task

### Step 1: Post futures to the project campfire

Send exactly these 4 futures to campfire {{CAMPFIRE_1}}:

1. **fizzbuzz-logic**: "Implement a Go function `FizzBuzz(n int) string` that returns 'Fizz' for multiples of 3, 'Buzz' for multiples of 5, 'FizzBuzz' for multiples of both, and the number as a string otherwise. Write to {{WORKSPACE}}/fizzbuzz/fizzbuzz.go with package name `main`."
   - Tags: `future`, `implementation`
   - Use: `cf send {{CAMPFIRE_1}} "<text>" --future --tag implementation`

2. **main-func**: "Implement a Go `main()` function in {{WORKSPACE}}/fizzbuzz/main.go (package main) that calls FizzBuzz for numbers 1 through 100 and prints each result on its own line."
   - Tags: `future`, `implementation`
   - Antecedent: the fizzbuzz-logic future ID (main depends on the logic existing)

3. **code-review**: "Review the fizzbuzz implementation for correctness. Read {{WORKSPACE}}/fizzbuzz/fizzbuzz.go and {{WORKSPACE}}/fizzbuzz/main.go. Verify the logic handles all cases. Post approval or rejection with specific feedback."
   - Tags: `future`, `review`
   - Antecedent: both implementation future IDs

4. **qa-test**: "Run `go run {{WORKSPACE}}/fizzbuzz/` and verify the output. First line should be '1', third line should be 'Fizz', fifth should be 'Buzz', fifteenth should be 'FizzBuzz'. Post test results."
   - Tags: `future`, `qa`
   - Antecedent: code-review future ID

Record all future message IDs. You will need them to verify fulfillments.

### Step 2: Monitor fulfillments

Poll for new messages every 30 seconds:
```
cf read {{CAMPFIRE_1}} --json
```

Watch for messages with tag `fulfills` that reference your future IDs.

### Step 3: Declare completion

Once ALL 4 futures have fulfillments, write the completion signal:

```bash
echo "PASS" > {{WORKSPACE}}/DONE
```

If after 8 minutes any future still lacks a fulfillment, write:
```bash
echo "FAIL: missing fulfillments for [list unfulfilled future IDs]" > {{WORKSPACE}}/DONE
```

## CLI Reference
- `cf send <campfire-id> "message" --future --tag <tag>` — send a future
- `cf send <campfire-id> "message" --tag <tag> --antecedent <msg-id>` — send with dependency
- `cf read <campfire-id> --json` — read new messages as JSON
- `cf read <campfire-id> --all --json` — read ALL messages
- `cf inspect <message-id> --json` — inspect a specific message
- `cf members <campfire-id> --json` — list campfire members
```

### 2.2 Agent B — Implementer 1 (MCP)

**Role:** Implements fizzbuzz logic. Coordinates with Agent C in the private implementers campfire.

**CLAUDE.md:**

```markdown
# Agent B — Implementer 1

You implement Go code as directed by the PM through campfire messages.

## Your Identity
- Public key: {{KEY_B}}
- Interface: MCP (use campfire_* tools)

## Campfires
- **Project campfire** ({{CAMPFIRE_1}}): Read futures and post fulfillments here.
- **Implementers campfire** ({{CAMPFIRE_2}}): Private channel with Agent C only.
  - You are the creator of this campfire.

## Team
| Agent | Key (first 12) | Role |
|-------|-----------------|------|
| A | {{KEY_A_SHORT}} | PM |
| C | {{KEY_C_SHORT}} | Implementer 2 |

## Your Task

### Step 1: Read futures from the project campfire

Use the campfire_read tool to read messages from {{CAMPFIRE_1}}. Look for messages tagged `future` and `implementation`.

### Step 2: Coordinate with Agent C

Post a message to the implementers campfire ({{CAMPFIRE_2}}) explaining the work split:
- You will implement `FizzBuzz(n int) string` in fizzbuzz.go
- Agent C will implement `main()` in main.go

### Step 3: Implement fizzbuzz.go

Write the file to {{WORKSPACE}}/fizzbuzz/fizzbuzz.go:
- Package: `main`
- Function: `FizzBuzz(n int) string`
- Logic: multiples of 15 return "FizzBuzz", multiples of 3 return "Fizz", multiples of 5 return "Buzz", otherwise return the number as a string (use `strconv.Itoa`)
- Import `strconv`

Create the directory if needed: `mkdir -p {{WORKSPACE}}/fizzbuzz`

### Step 4: Post fulfillment

After writing the file, send a fulfillment to the project campfire:

Use campfire_send with:
- campfire_id: {{CAMPFIRE_1}}
- message: "Implemented FizzBuzz function in fizzbuzz.go"
- fulfills: <the future message ID for fizzbuzz-logic>
- tags: ["implementation"]

### Step 5: Notify Agent C

Post to the implementers campfire ({{CAMPFIRE_2}}) that fizzbuzz.go is done, so Agent C can proceed with main.go.

## MCP Tool Reference
- campfire_read: Read messages (params: campfire_id, all, peek)
- campfire_send: Send message (params: campfire_id, message, tags, antecedents, future, fulfills)
- campfire_inspect: Inspect message (params: message_id)
- campfire_ls: List my campfires
- campfire_members: List campfire members (params: campfire_id)

## Important
- Read the project campfire FIRST to find the future message IDs
- The `fulfills` param takes a message ID string — the ID of the future you are fulfilling
- Always read messages as JSON when you need to extract IDs
```

### 2.3 Agent C — Implementer 2 (CLI)

**Role:** Implements main function. Coordinates with Agent B in the private implementers campfire.

**CLAUDE.md:**

```markdown
# Agent C — Implementer 2

You implement Go code as directed by the PM through campfire messages.

## Your Identity
- Public key: {{KEY_C}}
- Interface: CLI (`cf` command is on PATH)

## Campfires
- **Project campfire** ({{CAMPFIRE_1}}): Read futures and post fulfillments here.
- **Implementers campfire** ({{CAMPFIRE_2}}): Private channel with Agent B only.

## Team
| Agent | Key (first 12) | Role |
|-------|-----------------|------|
| A | {{KEY_A_SHORT}} | PM |
| B | {{KEY_B_SHORT}} | Implementer 1 |

## Your Task

### Step 1: Read futures from the project campfire

```bash
cf read {{CAMPFIRE_1}} --all --json
```

Look for a future tagged `implementation` about implementing `main()`.

### Step 2: Read the implementers campfire for coordination

```bash
cf read {{CAMPFIRE_2}} --all --json
```

Agent B will post the work split here. Wait for B to confirm fizzbuzz.go is done.

### Step 3: Wait for Agent B

Poll the implementers campfire every 20 seconds until Agent B posts that fizzbuzz.go is complete:

```bash
cf read {{CAMPFIRE_2}} --json
```

### Step 4: Implement main.go

Write {{WORKSPACE}}/fizzbuzz/main.go:
- Package: `main`
- Import: `fmt`
- main() calls FizzBuzz(i) for i=1..100, printing each result with fmt.Println

```go
package main

import "fmt"

func main() {
    for i := 1; i <= 100; i++ {
        fmt.Println(FizzBuzz(i))
    }
}
```

### Step 5: Post fulfillment

```bash
cf send {{CAMPFIRE_1}} "Implemented main() in main.go" --fulfills <main-func-future-id> --tag implementation
```

## CLI Reference
- `cf read <campfire-id> --all --json` — read all messages
- `cf read <campfire-id> --json` — read new messages only
- `cf send <campfire-id> "message" --fulfills <id> --tag <tag>` — fulfill a future
- `cf inspect <message-id> --json` — inspect message details
```

### 2.4 Agent D — Reviewer (CLI)

**Role:** Reviews code after both implementations are fulfilled.

**CLAUDE.md:**

```markdown
# Agent D — Code Reviewer

You review Go code for correctness after implementation is complete.

## Your Identity
- Public key: {{KEY_D}}
- Interface: CLI (`cf` command is on PATH)

## Campfires
- **Project campfire** ({{CAMPFIRE_1}}): Read futures and fulfillments, post reviews here.

## Your Task

### Step 1: Wait for implementation fulfillments

Poll the project campfire every 20 seconds:
```bash
cf read {{CAMPFIRE_1}} --all --json
```

Look for:
- A future tagged `review` (this is your assignment)
- Two fulfillment messages tagged `fulfills` and `implementation` (these mean the code is ready)

Do NOT start reviewing until BOTH implementation futures have fulfillments.

### Step 2: Review the code

Read the source files:
- {{WORKSPACE}}/fizzbuzz/fizzbuzz.go
- {{WORKSPACE}}/fizzbuzz/main.go

Verify:
- FizzBuzz(3) returns "Fizz"
- FizzBuzz(5) returns "Buzz"
- FizzBuzz(15) returns "FizzBuzz"
- FizzBuzz(1) returns "1"
- main() iterates 1-100 and prints each result

### Step 3: Post review fulfillment

```bash
cf send {{CAMPFIRE_1}} "APPROVED: Code review passed. FizzBuzz logic correct, main iterates 1-100." --fulfills <review-future-id> --tag review
```

If the code is wrong, still fulfill but note the issues:
```bash
cf send {{CAMPFIRE_1}} "REJECTED: [specific issues]" --fulfills <review-future-id> --tag review
```

## CLI Reference
- `cf read <campfire-id> --all --json` — read all messages
- `cf send <campfire-id> "message" --fulfills <id> --tag <tag>` — fulfill a future
- `cf inspect <message-id> --json` — inspect message details
```

### 2.5 Agent E — QA (MCP)

**Role:** Runs the code and verifies output after review approval.

**CLAUDE.md:**

```markdown
# Agent E — QA

You test Go programs for correctness.

## Your Identity
- Public key: {{KEY_E}}
- Interface: MCP (use campfire_* tools)

## Campfires
- **Project campfire** ({{CAMPFIRE_1}}): Read futures and fulfillments, post test results here.

## Your Task

### Step 1: Wait for review fulfillment

Poll the project campfire every 20 seconds using campfire_read.

Look for:
- A future tagged `qa` (this is your assignment)
- A fulfillment of the review future (tagged `fulfills` and `review`) — this means code is reviewed

Do NOT start testing until the review future has a fulfillment.

### Step 2: Run the program

Execute:
```bash
cd {{WORKSPACE}}/fizzbuzz && go run .
```

Capture the output. Verify:
- Line 1: "1"
- Line 2: "2"
- Line 3: "Fizz"
- Line 4: "4"
- Line 5: "Buzz"
- Line 15: "FizzBuzz"
- Line 100: "Buzz"
- Total: exactly 100 lines

### Step 3: Post QA fulfillment

Use campfire_send with:
- campfire_id: {{CAMPFIRE_1}}
- message: "QA PASSED: fizzbuzz output verified correct. 100 lines, all cases match." (or "QA FAILED: [details]")
- fulfills: <the qa future message ID>
- tags: ["qa"]

## MCP Tool Reference
- campfire_read: Read messages (params: campfire_id, all, peek)
- campfire_send: Send message (params: campfire_id, message, tags, antecedents, future, fulfills)
- campfire_inspect: Inspect message (params: message_id)
```

### 2.6 Environment Configuration

All agents share:
- `CF_BEACON_DIR=/tmp/campfire-integ/shared/beacons`
- `CF_TRANSPORT_DIR=/tmp/campfire-integ/shared/transport`
- `PATH=/tmp/campfire-integ/bin:$PATH` (for `cf` and `cf-mcp` binaries)

Each agent's CLAUDE.md contains `{{WORKSPACE}}` replaced with `/tmp/campfire-integ/shared/workspace` — the shared filesystem where code artifacts are written.

Each agent has a unique `CF_HOME` containing its own identity and message store.

## 3. Coordination Protocol Review

### 3.1 Expected Message Sequence

```
TIME    CAMPFIRE    SENDER    ACTION
─────────────────────────────────────────────────────────────────
t0      CF1         A         Future F1: "implement FizzBuzz function"
t1      CF1         A         Future F2: "implement main()" [antecedent: F1]
t2      CF1         A         Future F3: "code review" [antecedents: F1, F2]
t3      CF1         A         Future F4: "QA test" [antecedent: F3]
t4      CF2         B         "I'll do fizzbuzz.go, you do main.go"
t5      CF2         C         "Acknowledged, waiting for fizzbuzz.go"
t6      CF1         B         Fulfillment of F1: "Implemented FizzBuzz"
t7      CF2         B         "fizzbuzz.go is done, proceed"
t8      CF2         C         "Starting main.go"
t9      CF1         C         Fulfillment of F2: "Implemented main()"
t10     CF1         D         Fulfillment of F3: "APPROVED"
t11     CF1         E         Fulfillment of F4: "QA PASSED"
t12     ---         A         Writes DONE file
```

### 3.2 Message DAG Structure

```
F1 (future: fizzbuzz logic)
├── F2 (future: main func, antecedent: F1)
│   ├── F3 (future: review, antecedents: F1, F2)
│   │   └── F4 (future: QA, antecedent: F3)
│   │       └── FULFILL-F4 (fulfills: F4)
│   └── FULFILL-F2 (fulfills: F2)
├── FULFILL-F1 (fulfills: F1)
└── FULFILL-F3 (fulfills: F3, antecedent: F3)
```

### 3.3 Potential Failure Modes

**Problem: Agent cannot find future IDs in message output.**
- Mitigation: CLAUDE.md explicitly instructs agents to use `--json` output and parse the `id` field. The JSON format always includes the message ID.

**Problem: Agent uses wrong campfire ID format.**
- Mitigation: Campfire IDs are injected literally into CLAUDE.md as template variables. Agents never need to discover or construct IDs themselves.

**Problem: Agent posts fulfillment to wrong campfire.**
- Mitigation: Agent instructions explicitly name which campfire to post to. Only one campfire (CF1) receives futures and fulfillments.

**Problem: Agent starts work before its dependencies are fulfilled.**
- Mitigation: Each agent's CLAUDE.md explicitly tells it to wait and poll until prerequisite fulfillments appear. The protocol does not enforce ordering — agent instructions do.

**Problem: Race condition — C starts main.go before B finishes fizzbuzz.go.**
- Mitigation: C is instructed to poll CF2 and wait for B's "done" message. The private campfire serves as the synchronization channel.

**Problem: MCP agent cannot connect to cf-mcp server.**
- Mitigation: The mcp-config.json uses absolute paths to the pre-built binary. CF_HOME is passed as a flag, not an env var, avoiding inheritance issues.

**Problem: Agent exhausts max-turns before completing.**
- Mitigation: 50 turns is generous for the simple tasks assigned. If an agent loops inefficiently (re-reading the same empty messages), the turn limit acts as a safety valve. The harness detects when agents exit and reports which ones finished.

**Problem: Filesystem transport race on concurrent writes.**
- Mitigation: The filesystem transport writes each message as a separate file (UUID-named). Multiple agents writing to the same campfire directory simultaneously is safe — no file is overwritten. SQLite stores are per-agent and never shared.

### 3.4 Async Polling Strategy

Agents poll at different rates to avoid thundering herd:
- Agent A: every 30 seconds (PM monitoring)
- Agent B: reads once at start, then posts and signals via CF2
- Agent C: polls CF2 every 20 seconds waiting for B
- Agent D: polls CF1 every 20 seconds waiting for 2 implementation fulfillments
- Agent E: polls CF1 every 20 seconds waiting for review fulfillment

The staggered rates mean agents naturally sequence themselves. The total wall-clock time should be approximately:
- 1-2 min: A posts futures, B reads and starts implementing
- 1-2 min: B writes code, posts fulfillment, notifies C
- 1-2 min: C writes code, posts fulfillment
- 1 min: D reviews, posts approval
- 1 min: E tests, posts results
- **Total estimate: 5-8 minutes**

## 4. Verification

### 4.1 Automated Verification Script

After the DONE file appears (or timeout), the harness runs `tests/verify_5agent.sh`:

#### 4.1.1 Message DAG Completeness

```bash
# Read all messages from CF1 as Agent A
export CF_HOME=/tmp/campfire-integ/agents/agent-a
MSGS=$(cf read $CAMPFIRE_1 --all --json)

# Verify: exactly 4 futures exist
FUTURE_COUNT=$(echo "$MSGS" | python3 -c "
import json, sys
msgs = json.load(sys.stdin)
futures = [m for m in msgs if 'future' in (m.get('tags') or [])]
print(len(futures))
")
[ "$FUTURE_COUNT" -eq 4 ] || fail "Expected 4 futures, got $FUTURE_COUNT"

# Verify: each future has at least one fulfillment
echo "$MSGS" | python3 -c "
import json, sys
msgs = json.load(sys.stdin)
futures = {m['id'] for m in msgs if 'future' in (m.get('tags') or [])}
fulfilled = set()
for m in msgs:
    if 'fulfills' in (m.get('tags') or []):
        for ant in m.get('antecedents', []):
            if ant in futures:
                fulfilled.add(ant)
unfulfilled = futures - fulfilled
if unfulfilled:
    print(f'FAIL: unfulfilled futures: {unfulfilled}')
    sys.exit(1)
print(f'OK: all {len(futures)} futures fulfilled')
"
```

#### 4.1.2 Provenance Chain Validity

```bash
# Inspect every message and verify signatures
echo "$MSGS" | python3 -c "
import json, sys, subprocess
msgs = json.load(sys.stdin)
for m in msgs:
    result = subprocess.run(
        ['cf', 'inspect', m['id'], '--json'],
        capture_output=True, text=True,
        env={**dict(__import__('os').environ),
             'CF_HOME': '/tmp/campfire-integ/agents/agent-a'}
    )
    inspection = json.loads(result.stdout)
    if not inspection.get('signature_valid'):
        print(f'FAIL: message {m[\"id\"]} has invalid signature')
        sys.exit(1)
    for hop in inspection.get('provenance', []):
        if not hop.get('signature_valid'):
            print(f'FAIL: provenance hop invalid for message {m[\"id\"]}')
            sys.exit(1)
print(f'OK: all {len(msgs)} messages have valid signatures and provenance')
"
```

#### 4.1.3 Code Artifact Correctness

```bash
# Verify the files exist
[ -f /tmp/campfire-integ/shared/workspace/fizzbuzz/fizzbuzz.go ] || fail "fizzbuzz.go not found"
[ -f /tmp/campfire-integ/shared/workspace/fizzbuzz/main.go ] || fail "main.go not found"

# Run the program and verify output
OUTPUT=$(cd /tmp/campfire-integ/shared/workspace/fizzbuzz && go run . 2>&1)
LINE_COUNT=$(echo "$OUTPUT" | wc -l)
[ "$LINE_COUNT" -eq 100 ] || fail "Expected 100 lines, got $LINE_COUNT"

LINE_1=$(echo "$OUTPUT" | sed -n '1p')
LINE_3=$(echo "$OUTPUT" | sed -n '3p')
LINE_5=$(echo "$OUTPUT" | sed -n '5p')
LINE_15=$(echo "$OUTPUT" | sed -n '15p')

[ "$LINE_1" = "1" ] || fail "Line 1: expected '1', got '$LINE_1'"
[ "$LINE_3" = "Fizz" ] || fail "Line 3: expected 'Fizz', got '$LINE_3'"
[ "$LINE_5" = "Buzz" ] || fail "Line 5: expected 'Buzz', got '$LINE_5'"
[ "$LINE_15" = "FizzBuzz" ] || fail "Line 15: expected 'FizzBuzz', got '$LINE_15'"
```

#### 4.1.4 Campfire Membership Counts

```bash
# CF1 should have 5 members
MEM1=$(CF_HOME=/tmp/campfire-integ/agents/agent-a cf members $CAMPFIRE_1 --json)
MEM1_COUNT=$(echo "$MEM1" | python3 -c "import json,sys; print(len(json.load(sys.stdin)))")
[ "$MEM1_COUNT" -eq 5 ] || fail "CF1: expected 5 members, got $MEM1_COUNT"

# CF2 should have 2 members
MEM2=$(CF_HOME=/tmp/campfire-integ/agents/agent-b cf members $CAMPFIRE_2 --json)
MEM2_COUNT=$(echo "$MEM2" | python3 -c "import json,sys; print(len(json.load(sys.stdin)))")
[ "$MEM2_COUNT" -eq 2 ] || fail "CF2: expected 2 members, got $MEM2_COUNT"
```

#### 4.1.5 Private Campfire Isolation

```bash
# Agent D should NOT be able to read CF2 messages (not a member)
CF2_READ=$(CF_HOME=/tmp/campfire-integ/agents/agent-d cf read $CAMPFIRE_2 --all --json 2>&1) || true
# This should either fail or return empty — D is not a member
echo "$CF2_READ" | grep -qF "not a member" || {
    # If it didn't error, check that no messages leaked
    MSG_COUNT=$(echo "$CF2_READ" | python3 -c "import json,sys; print(len(json.load(sys.stdin)))" 2>/dev/null || echo "0")
    [ "$MSG_COUNT" -eq 0 ] || fail "CF2 messages leaked to non-member Agent D"
}
```

### 4.2 Log Capture

All agent output goes to `/tmp/campfire-integ/logs/agent-{a..e}.log`. These contain Claude Code's JSON output (with `--output-format json`), including tool calls made, token usage, and any errors.

On test failure, the harness prints:
1. The verification check that failed
2. The last 50 lines of each agent's log
3. A full dump of CF1 messages (`cf read $CAMPFIRE_1 --all`)
4. A full dump of CF2 messages (`cf read $CAMPFIRE_2 --all` from agent B's perspective)

### 4.3 Verification Summary

| Check | Method | Pass Criteria |
|-------|--------|---------------|
| DAG completeness | Parse CF1 messages | 4 futures, each with >= 1 fulfillment |
| Provenance validity | `cf inspect` every message | All signature_valid = true |
| Code exists | File existence check | fizzbuzz.go and main.go present |
| Code correct | `go run .` output check | 100 lines, correct fizzbuzz values |
| CF1 membership | `cf members` | 5 members |
| CF2 membership | `cf members` | 2 members |
| CF2 isolation | Read CF2 as non-member | Error or empty result |
| DONE file | Content check | Contains "PASS" |

## 5. Known Risks and Mitigations

### 5.1 LLM Non-Determinism

Agents are LLMs. They may not follow instructions perfectly. Mitigations:
- Instructions are explicit, step-by-step, with exact commands
- The fizzbuzz task is simple enough that implementation correctness is likely
- The verification checks distinguish protocol failures from task failures — a wrong FizzBuzz implementation with correct message DAG still validates the protocol

### 5.2 Cost

5 Claude Code sessions running for 5-8 minutes each. Estimated cost: ~$5-15 depending on model tier. The harness should log token usage from each session's JSON output for cost tracking.

### 5.3 MCP Server Lifecycle

The `cf-mcp` process is spawned by Claude Code as a child process (via mcp-config.json). It inherits the agent's environment. When Claude Code exits, the MCP server is killed. This is the correct lifecycle — no orphaned processes.

### 5.4 Shared Filesystem Workspace

All agents write to the same workspace directory. This is intentional — it simulates agents collaborating on a shared codebase. The risk is that agents write to unexpected paths or overwrite each other's files. Mitigation: each agent's CLAUDE.md specifies exactly which files to write.

### 5.5 Template Variable Injection

The harness must replace template variables ({{KEY_A}}, {{CAMPFIRE_1}}, {{WORKSPACE}}, etc.) in each agent's CLAUDE.md before launching. This is done with `sed` during the setup phase. The variables are:

| Variable | Value |
|----------|-------|
| `{{KEY_A}}` through `{{KEY_E}}` | Full 64-char hex public keys |
| `{{KEY_A_SHORT}}` through `{{KEY_E_SHORT}}` | First 12 chars of public keys |
| `{{CAMPFIRE_1}}` | Full campfire 1 ID (64-char hex) |
| `{{CAMPFIRE_2}}` | Full campfire 2 ID (64-char hex) |
| `{{WORKSPACE}}` | `/tmp/campfire-integ/shared/workspace` |

### 5.6 What This Does NOT Test

- Recursive composition (campfire as member of campfire)
- P2P HTTP transport (uses filesystem for simplicity and reliability)
- Threshold signatures (threshold=1 for all campfires)
- Agent eviction or campfire disbanding
- Filter optimization
- More than 5 agents
- Network partitions or offline recovery

These are explicitly out of scope per the bead description and are better tested in isolation.

## 6. File Manifest

Files to create during implementation:

| File | Purpose |
|------|---------|
| `tests/harness_5agent.sh` | Main harness: build, setup, launch, wait, verify, teardown |
| `tests/verify_5agent.sh` | Verification-only script (can be run independently) |
| `tests/agent-templates/agent-a.md` | PM CLAUDE.md template |
| `tests/agent-templates/agent-b.md` | Implementer 1 CLAUDE.md template |
| `tests/agent-templates/agent-c.md` | Implementer 2 CLAUDE.md template |
| `tests/agent-templates/agent-d.md` | Reviewer CLAUDE.md template |
| `tests/agent-templates/agent-e.md` | QA CLAUDE.md template |
| `tests/agent-templates/mcp-config.json` | MCP server config template |
