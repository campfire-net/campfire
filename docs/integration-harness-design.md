# Integration Harness Design: 5-Agent Campfire Coordination Test

**Status:** Design (workspace-21)
**Date:** 2026-03-15
**Revision:** Discovery-first approach (2026-03-15)

## Design Change Log

> **Revision: Discovery-First Approach** — Agents no longer receive pre-injected campfire IDs. The PM agent (A) creates campfire 1 with a descriptive beacon. Worker agents discover campfires via `cf discover`, read beacon descriptions, self-select based on their skills, and join. Implementer B also creates campfire 2 with its own beacon, which Implementer C discovers and joins. This tests the full funnel: discover, evaluate, join, read, reason, act, respond.

## Overview

End-to-end integration test that launches 5 real Claude Code sessions, each communicating exclusively through the Campfire protocol. The agents coordinate to build, review, and test a small Go program (fizzbuzz). The harness verifies that the protocol's beacon discovery, self-selection, futures/fulfillment DAG, multi-campfire membership, mixed CLI/MCP interfaces, and provenance chains all work correctly with real autonomous agents.

**Key difference from previous design:** The harness does NOT pre-create campfires for workers or inject campfire IDs. Agents discover campfires through beacons and decide which to join based on their skills. This tests the full coordination funnel, not just message passing.

## 1. Harness Architecture

### 1.1 Test Scenario

Five agents coordinate to produce a working Go program:

1. **Agent A (PM)** creates campfire 1 with a descriptive beacon, then decomposes "build fizzbuzz" into futures
2. **Agent B (Implementer 1)** discovers campfire 1 via `cf discover`, joins based on beacon match, reads futures, creates campfire 2 with a beacon for implementation coordination
3. **Agent C (Implementer 2)** discovers both campfires via `cf discover`, joins campfire 1 (project work) and campfire 2 (implementation coordination), takes the main.go assignment
4. **Agent D (Reviewer)** discovers campfire 1 via `cf discover`, joins based on "code reviewer" skill match, waits for implementation fulfillments, reviews code
5. **Agent E (QA)** discovers campfire 1 via `cf discover`, joins based on "QA tester" skill match, waits for review, runs tests
6. **Agent A** reads all results and posts a completion signal

> **Changed from previous design:** Previously, the harness pre-created both campfires, pre-joined all workers, and injected campfire IDs into each agent's CLAUDE.md. Now the harness only creates identities and sets the shared beacon directory. Agents do the rest.

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

2. **Minimal setup phase** (harness does ONLY this):
   - Create shared beacon directory
   - Create shared transport directory
   - Create shared workspace directory
   - Write each agent's CLAUDE.md from templates (skills-based, NO campfire IDs)
   - Write MCP config files for agents B and E

   > **Changed from previous design:** The harness no longer creates campfires, captures campfire IDs, joins agents, or injects `{{CAMPFIRE_1}}`/`{{CAMPFIRE_2}}` into templates. The only template variables are `{{KEY_self}}` (the agent's own public key) and `{{WORKSPACE}}`. Agents discover and join campfires autonomously.

3. **Agent launch phase** (parallel via systemd-run):
   - Launch all 5 agents simultaneously using systemd-run
   - Each agent runs in its own cgroup, with its own CF_HOME, its own workspace as CWD
   - Agent A gets a small head start (5 seconds) to create campfire 1 and publish its beacon before workers start discovering

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

> **Changed from previous design:** Agent CLAUDE.md templates are now skills-based, not ID-based. Workers do NOT receive campfire IDs. They receive: their own identity, their skills/role, instructions to use `cf discover` to find relevant campfires, and criteria for evaluating beacon descriptions. The PM agent (A) is the only one that creates a campfire in its instructions. Implementer B's instructions tell it to create a second campfire after joining the first.

### 2.1 Agent A — PM (CLI)

**Role:** Project manager. Creates the project campfire with a descriptive beacon, decomposes work as futures, monitors fulfillments, declares completion.

**CLAUDE.md:**

```markdown
# Agent A — Project Manager

You coordinate a team of workers to build a fizzbuzz Go program. You communicate exclusively through the Campfire protocol.

## Your Identity
- Public key: {{KEY_A}}
- Interface: CLI (`cf` command is on PATH)

## Your Task

### Step 1: Create the project campfire

Create an open campfire with a beacon that describes the project and the skills needed:

```bash
cf create --protocol open --description "Go project: build fizzbuzz. Need: Go implementer, code reviewer, QA tester. Task: implement FizzBuzz(n) function + main() that prints 1-100, review for correctness, run and verify output."
```

Record the campfire ID from the output. This is your project campfire.

### Step 2: Post futures to the project campfire

Send exactly these 4 futures to your project campfire:

1. **fizzbuzz-logic**: "Implement a Go function `FizzBuzz(n int) string` that returns 'Fizz' for multiples of 3, 'Buzz' for multiples of 5, 'FizzBuzz' for multiples of both, and the number as a string otherwise. Write to {{WORKSPACE}}/fizzbuzz/fizzbuzz.go with package name `main`."
   - Tags: `future`, `implementation`
   - Use: `cf send <campfire-id> "<text>" --future --tag implementation`

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

### Step 3: Monitor fulfillments

Poll for new messages every 30 seconds:
```
cf read <campfire-id> --json
```

Watch for messages with tag `fulfills` that reference your future IDs.

### Step 4: Declare completion

Once ALL 4 futures have fulfillments, write the completion signal:

```bash
echo "PASS" > {{WORKSPACE}}/DONE
```

If after 8 minutes any future still lacks a fulfillment, write:
```bash
echo "FAIL: missing fulfillments for [list unfulfilled future IDs]" > {{WORKSPACE}}/DONE
```

## CLI Reference
- `cf create --protocol open --description "..."` — create a campfire with beacon
- `cf send <campfire-id> "message" --future --tag <tag>` — send a future
- `cf send <campfire-id> "message" --tag <tag> --reply-to <msg-id>` — send with dependency
- `cf read <campfire-id> --json` — read new messages as JSON
- `cf read <campfire-id> --all --json` — read ALL messages
- `cf inspect <message-id> --json` — inspect a specific message
- `cf members <campfire-id> --json` — list campfire members
```

### 2.2 Agent B — Implementer 1 (MCP)

**Role:** Discovers project campfire, implements fizzbuzz logic, creates a second campfire for implementation coordination.

> **Changed from previous design:** B no longer receives `{{CAMPFIRE_1}}` or `{{CAMPFIRE_2}}`. B discovers campfire 1 via beacon, then creates campfire 2 itself with a descriptive beacon that C will discover.

**CLAUDE.md:**

```markdown
# Agent B — Implementer 1

You are a Go implementer. You find work by discovering campfires, reading their beacon descriptions, and joining ones that match your skills.

## Your Identity
- Public key: {{KEY_B}}
- Interface: MCP (use campfire_* tools)

## Your Skills
- Go programming
- Implementation
- Code writing

## Your Task

### Step 1: Discover campfires

Use campfire_discover to scan for available campfires. Look for a campfire whose beacon description mentions a Go project needing implementers. Read each beacon carefully — join the one that matches your skills.

### Step 2: Join the project campfire

Once you find a campfire that needs a Go implementer, use campfire_join to join it. This is your project campfire.

### Step 3: Read futures

Use campfire_read to read messages from the project campfire. Look for messages tagged `future` and `implementation`. These are your assignments.

### Step 4: Create an implementation coordination campfire

Create a new campfire for coordinating implementation work with other implementers:

Use campfire_create with:
- protocol: open
- description: "Implementation coordination — fizzbuzz project. For Go implementers working on fizzbuzz. Splitting work: fizzbuzz.go (FizzBuzz function) and main.go (main function)."

This is your implementation campfire. Post coordination messages here.

### Step 5: Post the work split

Post a message to the implementation campfire explaining the work split:
- You will implement `FizzBuzz(n int) string` in fizzbuzz.go
- The other implementer should implement `main()` in main.go

### Step 6: Implement fizzbuzz.go

Write the file to {{WORKSPACE}}/fizzbuzz/fizzbuzz.go:
- Package: `main`
- Function: `FizzBuzz(n int) string`
- Logic: multiples of 15 return "FizzBuzz", multiples of 3 return "Fizz", multiples of 5 return "Buzz", otherwise return the number as a string (use `strconv.Itoa`)
- Import `strconv`

Create the directory if needed: `mkdir -p {{WORKSPACE}}/fizzbuzz`

### Step 7: Post fulfillment

After writing the file, send a fulfillment to the project campfire:

Use campfire_send with:
- campfire_id: <the project campfire you joined>
- message: "Implemented FizzBuzz function in fizzbuzz.go"
- fulfills: <the future message ID for fizzbuzz-logic>
- tags: ["implementation"]

### Step 8: Notify via implementation campfire

Post to the implementation campfire that fizzbuzz.go is done, so the other implementer can proceed with main.go.

## MCP Tool Reference
- campfire_discover: Discover campfires via beacons (returns beacon descriptions)
- campfire_join: Join a campfire (params: campfire_id)
- campfire_create: Create a campfire (params: protocol, description)
- campfire_read: Read messages (params: campfire_id, all, peek)
- campfire_send: Send message (params: campfire_id, message, tags, antecedents, future, fulfills)
- campfire_inspect: Inspect message (params: message_id)
- campfire_ls: List my campfires
- campfire_members: List campfire members (params: campfire_id)

## Important
- DISCOVER campfires first — do not assume you know any campfire IDs
- Read beacon descriptions to decide which campfires match your skills
- Read the project campfire FIRST to find the future message IDs
- The `fulfills` param takes a message ID string — the ID of the future you are fulfilling
- Always read messages as JSON when you need to extract IDs
```

### 2.3 Agent C — Implementer 2 (CLI)

**Role:** Discovers both the project campfire and the implementation coordination campfire, implements main function.

> **Changed from previous design:** C no longer receives `{{CAMPFIRE_1}}` or `{{CAMPFIRE_2}}`. C discovers both campfires via beacons.

**CLAUDE.md:**

```markdown
# Agent C — Implementer 2

You are a Go implementer. You find work by discovering campfires, reading their beacon descriptions, and joining ones that match your skills.

## Your Identity
- Public key: {{KEY_C}}
- Interface: CLI (`cf` command is on PATH)

## Your Skills
- Go programming
- Implementation
- Code writing

## Your Task

### Step 1: Discover campfires

```bash
cf discover --json
```

Look for campfires whose beacon descriptions mention Go projects or implementation work. You should find:
- A project campfire (needs Go implementers) — join this
- An implementation coordination campfire (for splitting fizzbuzz work) — join this too, when it appears

You may need to run `cf discover` multiple times as campfires are created by other agents.

### Step 2: Join relevant campfires

Join the project campfire:
```bash
cf join <project-campfire-id>
```

Also look for an implementation coordination campfire (its beacon should mention fizzbuzz implementation coordination). Join that too:
```bash
cf join <implementation-campfire-id>
```

### Step 3: Read the implementation coordination campfire

```bash
cf read <implementation-campfire-id> --all --json
```

Another implementer will post the work split here. Wait for them to tell you which file to implement.

### Step 4: Wait for the other implementer

Poll the implementation coordination campfire every 20 seconds until the other implementer posts that fizzbuzz.go is complete:

```bash
cf read <implementation-campfire-id> --json
```

### Step 5: Read the project campfire for your assignment

```bash
cf read <project-campfire-id> --all --json
```

Look for a future tagged `implementation` about implementing `main()`.

### Step 6: Implement main.go

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

### Step 7: Post fulfillment

```bash
cf send <project-campfire-id> "Implemented main() in main.go" --fulfills <main-func-future-id> --tag implementation
```

## CLI Reference
- `cf discover --json` — discover campfires via beacons
- `cf join <campfire-id>` — join a campfire
- `cf read <campfire-id> --all --json` — read all messages
- `cf read <campfire-id> --json` — read new messages only
- `cf send <campfire-id> "message" --fulfills <id> --tag <tag>` — fulfill a future
- `cf inspect <message-id> --json` — inspect message details
```

### 2.4 Agent D — Reviewer (CLI)

**Role:** Discovers the project campfire, joins based on "code reviewer" skill match, reviews code after implementations are fulfilled.

> **Changed from previous design:** D no longer receives `{{CAMPFIRE_1}}`. D discovers the project campfire via beacon and self-selects based on "code reviewer" skill.

**CLAUDE.md:**

```markdown
# Agent D — Code Reviewer

You review Go code for correctness. You find work by discovering campfires and joining ones that need code reviewers.

## Your Identity
- Public key: {{KEY_D}}
- Interface: CLI (`cf` command is on PATH)

## Your Skills
- Code review
- Go correctness verification
- Logic validation

## Your Task

### Step 1: Discover campfires

```bash
cf discover --json
```

Look for a campfire whose beacon description mentions needing a code reviewer. Read each beacon carefully — join the one that matches your skills. Do NOT join implementation-only campfires.

### Step 2: Join the project campfire

```bash
cf join <project-campfire-id>
```

### Step 3: Wait for implementation fulfillments

Poll the project campfire every 20 seconds:
```bash
cf read <project-campfire-id> --all --json
```

Look for:
- A future tagged `review` (this is your assignment)
- Two fulfillment messages tagged `implementation` (these mean the code is ready)

Do NOT start reviewing until BOTH implementation futures have fulfillments.

### Step 4: Review the code

Read the source files:
- {{WORKSPACE}}/fizzbuzz/fizzbuzz.go
- {{WORKSPACE}}/fizzbuzz/main.go

Verify:
- FizzBuzz(3) returns "Fizz"
- FizzBuzz(5) returns "Buzz"
- FizzBuzz(15) returns "FizzBuzz"
- FizzBuzz(1) returns "1"
- main() iterates 1-100 and prints each result

### Step 5: Post review fulfillment

```bash
cf send <project-campfire-id> "APPROVED: Code review passed. FizzBuzz logic correct, main iterates 1-100." --fulfills <review-future-id> --tag review
```

If the code is wrong, still fulfill but note the issues:
```bash
cf send <project-campfire-id> "REJECTED: [specific issues]" --fulfills <review-future-id> --tag review
```

## CLI Reference
- `cf discover --json` — discover campfires via beacons
- `cf join <campfire-id>` — join a campfire
- `cf read <campfire-id> --all --json` — read all messages
- `cf send <campfire-id> "message" --fulfills <id> --tag <tag>` — fulfill a future
- `cf inspect <message-id> --json` — inspect message details
```

### 2.5 Agent E — QA (MCP)

**Role:** Discovers the project campfire, joins based on "QA tester" skill match, runs the code and verifies output after review approval.

> **Changed from previous design:** E no longer receives `{{CAMPFIRE_1}}`. E discovers the project campfire via beacon and self-selects based on "QA tester" skill.

**CLAUDE.md:**

```markdown
# Agent E — QA

You test Go programs for correctness. You find work by discovering campfires and joining ones that need QA testers.

## Your Identity
- Public key: {{KEY_E}}
- Interface: MCP (use campfire_* tools)

## Your Skills
- QA testing
- Program execution and output verification
- Test result reporting

## Your Task

### Step 1: Discover campfires

Use campfire_discover to scan for available campfires. Look for a campfire whose beacon description mentions needing a QA tester. Read each beacon carefully — join the one that matches your skills. Do NOT join implementation-only campfires.

### Step 2: Join the project campfire

Use campfire_join to join the project campfire you found.

### Step 3: Wait for review fulfillment

Poll the project campfire every 20 seconds using campfire_read.

Look for:
- A future tagged `qa` (this is your assignment)
- A fulfillment of the review future (tagged `review`) — this means code is reviewed

Do NOT start testing until the review future has a fulfillment.

### Step 4: Run the program

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

### Step 5: Post QA fulfillment

Use campfire_send with:
- campfire_id: <the project campfire you joined>
- message: "QA PASSED: fizzbuzz output verified correct. 100 lines, all cases match." (or "QA FAILED: [details]")
- fulfills: <the qa future message ID>
- tags: ["qa"]

## MCP Tool Reference
- campfire_discover: Discover campfires via beacons (returns beacon descriptions)
- campfire_join: Join a campfire (params: campfire_id)
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

> **Changed from previous design:** Template variables are now minimal. Only `{{KEY_self}}` (the agent's own public key) and `{{WORKSPACE}}` are injected. No `{{CAMPFIRE_1}}`, `{{CAMPFIRE_2}}`, `{{KEY_*_SHORT}}`, or team roster tables. Agents discover everything else at runtime.

## 3. Coordination Protocol Review

### 3.1 Expected Message Sequence

> **Changed from previous design:** Added discovery and join phases (t0-t3) before any messages are sent. Agent A gets a head start. Workers discover, evaluate beacons, and join before the message flow begins.

```
TIME    ACTION
─────────────────────────────────────────────────────────────────
t0      A creates campfire CF1 with beacon: "Go project: build fizzbuzz. Need: Go implementer, code reviewer, QA tester."
t1      A posts futures F1, F2, F3, F4 to CF1
t2      B discovers CF1 via beacon, evaluates: "needs Go implementer" — matches skills — joins
t3      C discovers CF1 via beacon, evaluates: "needs Go implementer" — matches skills — joins
t3      D discovers CF1 via beacon, evaluates: "needs code reviewer" — matches skills — joins
t3      E discovers CF1 via beacon, evaluates: "needs QA tester" — matches skills — joins
t4      B reads futures from CF1, creates CF2 with beacon: "Implementation coordination — fizzbuzz project"
t5      B posts work split to CF2: "I'll do fizzbuzz.go, you do main.go"
t6      C discovers CF2 via beacon, evaluates: "implementation coordination for fizzbuzz" — matches — joins
t7      C reads CF2, acknowledges work split
t8      B implements fizzbuzz.go, posts fulfillment of F1 to CF1
t9      B posts "fizzbuzz.go is done" to CF2
t10     C reads CF2, sees fizzbuzz.go is done, implements main.go
t11     C posts fulfillment of F2 to CF1
t12     D reads CF1, sees both implementation fulfillments, reviews code
t13     D posts fulfillment of F3 (review) to CF1
t14     E reads CF1, sees review fulfillment, runs program, verifies output
t15     E posts fulfillment of F4 (QA) to CF1
t16     A reads CF1, sees all 4 fulfillments, writes DONE file
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

> **Changed from previous design:** Previous failure modes about "wrong campfire ID format" and "posts fulfillment to wrong campfire" are replaced by new discovery-related failure modes. Several original failure modes remain (race conditions, MCP lifecycle, etc.).

**Problem: Agent discovers no campfires (beacon not yet published).**
- Mitigation: Agent A gets a 5-second head start. Worker agents' CLAUDE.md instructions tell them to retry `cf discover` every 15 seconds if no matching campfires are found. Agents should expect to poll multiple times before beacons appear.

**Problem: Agent joins the wrong campfire (misreads beacon description).**
- Mitigation: Beacon descriptions are explicit about needed skills. Agent CLAUDE.md instructions tell agents to read beacon descriptions carefully and match against their stated skills. The beacon for CF1 says "Need: Go implementer, code reviewer, QA tester" — this is unambiguous. The beacon for CF2 says "Implementation coordination — fizzbuzz project" — only implementers should join.

**Problem: Agent C cannot find campfire 2 (B hasn't created it yet).**
- Mitigation: C's instructions tell it to run `cf discover` multiple times. C can join CF1 first, read futures, and keep discovering until CF2 appears. The polling loop handles the timing naturally.

**Problem: Reviewer or QA agent joins the implementation coordination campfire.**
- Mitigation: D and E's CLAUDE.md explicitly says "Do NOT join implementation-only campfires." The CF2 beacon description says "For Go implementers" — agents with review/QA skills should self-select out. If they join anyway, it wastes a turn but doesn't break the test (CF2 is open, extra members don't prevent it from working).

**Problem: Agent cannot find future IDs in message output.**
- Mitigation: CLAUDE.md explicitly instructs agents to use `--json` output and parse the `id` field. The JSON format always includes the message ID.

**Problem: Agent starts work before its dependencies are fulfilled.**
- Mitigation: Each agent's CLAUDE.md explicitly tells it to wait and poll until prerequisite fulfillments appear. The protocol does not enforce ordering — agent instructions do.

**Problem: Race condition — C starts main.go before B finishes fizzbuzz.go.**
- Mitigation: C is instructed to poll CF2 and wait for B's "done" message. The private campfire serves as the synchronization channel.

**Problem: MCP agent cannot connect to cf-mcp server.**
- Mitigation: The mcp-config.json uses absolute paths to the pre-built binary. CF_HOME is passed as a flag, not an env var, avoiding inheritance issues.

**Problem: Agent exhausts max-turns before completing.**
- Mitigation: 50 turns is generous for the simple tasks assigned. Discovery adds 2-3 extra turns compared to the pre-injected design. If an agent loops inefficiently (re-reading the same empty messages), the turn limit acts as a safety valve.

**Problem: Filesystem transport race on concurrent writes.**
- Mitigation: The filesystem transport writes each message as a separate file (UUID-named). Multiple agents writing to the same campfire directory simultaneously is safe — no file is overwritten. SQLite stores are per-agent and never shared.

**Problem: Two implementers both take the same future.**
- Mitigation: B's instructions explicitly say "You implement fizzbuzz.go." C's instructions explicitly say "Wait for the work split in the coordination campfire." The coordination campfire exists specifically to prevent this race. Even if both agents read the same futures, the work split message in CF2 disambiguates.

### 3.4 Async Polling Strategy

> **Changed from previous design:** Added discovery polling at the start. Workers need extra turns for discover/evaluate/join before they begin message polling.

Agents poll at different rates:

**Discovery phase (first 1-2 minutes):**
- Agent A: no discovery needed — creates CF1 immediately
- Agents B, C, D, E: `cf discover` every 15 seconds until they find and join the project campfire
- Agent C: additional discovery polling for CF2 (created by B)

**Work phase (after joining):**
- Agent A: polls CF1 every 30 seconds (PM monitoring)
- Agent B: reads CF1 once for futures, then creates CF2 and starts implementing
- Agent C: polls CF2 every 20 seconds waiting for B's work split and completion signal
- Agent D: polls CF1 every 20 seconds waiting for 2 implementation fulfillments
- Agent E: polls CF1 every 20 seconds waiting for review fulfillment

The staggered rates mean agents naturally sequence themselves. The total wall-clock time should be approximately:
- 0-1 min: A creates CF1, posts futures
- 1-2 min: Workers discover CF1, join. B reads futures, creates CF2, starts implementing
- 2-3 min: C discovers CF2, joins. B finishes fizzbuzz.go, notifies C
- 3-4 min: C implements main.go, posts fulfillment
- 4-5 min: D reviews, posts approval
- 5-6 min: E tests, posts results
- **Total estimate: 5-9 minutes** (slightly longer than pre-injected design due to discovery phase)

## 4. Verification

### 4.1 Automated Verification Script

After the DONE file appears (or timeout), the harness runs `tests/verify_5agent.sh`:

> **Changed from previous design:** Added new verification checks for discovery and self-selection. The harness must first discover which campfire IDs were created (since they are no longer known in advance).

#### 4.1.0 Campfire Discovery by Harness

The verification script must first discover the campfire IDs that agents created, since they are no longer pre-known:

```bash
# Discover all campfires from Agent A's perspective (A created CF1, so it knows it)
export CF_HOME=/tmp/campfire-integ/agents/agent-a
CAMPFIRES_A=$(cf ls --json)
CAMPFIRE_1=$(echo "$CAMPFIRES_A" | python3 -c "
import json, sys
campfires = json.load(sys.stdin)
# CF1 is the one A created (A is the creator)
for c in campfires:
    if c.get('role') == 'creator':
        print(c['id'])
        break
")

# Discover CF2 from Agent B's perspective (B created it)
export CF_HOME=/tmp/campfire-integ/agents/agent-b
CAMPFIRES_B=$(cf ls --json)
CAMPFIRE_2=$(echo "$CAMPFIRES_B" | python3 -c "
import json, sys
campfires = json.load(sys.stdin)
# CF2 is the one B created
for c in campfires:
    if c.get('role') == 'creator':
        print(c['id'])
        break
")

echo "CF1=$CAMPFIRE_1"
echo "CF2=$CAMPFIRE_2"
[ -n "$CAMPFIRE_1" ] || fail "Could not determine CF1 ID"
[ -n "$CAMPFIRE_2" ] || fail "Could not determine CF2 ID"
```

#### 4.1.1 Discovery and Self-Selection Verification

> **New check — validates the discovery-first approach.**

```bash
# Verify: All 5 agents are members of CF1 (they all discovered and joined)
export CF_HOME=/tmp/campfire-integ/agents/agent-a
MEM1=$(cf members $CAMPFIRE_1 --json)
MEM1_COUNT=$(echo "$MEM1" | python3 -c "import json,sys; print(len(json.load(sys.stdin)))")
[ "$MEM1_COUNT" -eq 5 ] || fail "CF1: expected 5 members (all agents discovered and joined), got $MEM1_COUNT"

# Verify: Only implementers (B and C) are members of CF2
export CF_HOME=/tmp/campfire-integ/agents/agent-b
MEM2=$(cf members $CAMPFIRE_2 --json)
MEM2_COUNT=$(echo "$MEM2" | python3 -c "import json,sys; print(len(json.load(sys.stdin)))")
[ "$MEM2_COUNT" -eq 2 ] || fail "CF2: expected 2 members (only implementers self-selected), got $MEM2_COUNT"

# Verify: D and E did NOT join CF2 (correct self-selection)
echo "$MEM2" | python3 -c "
import json, sys
members = json.load(sys.stdin)
member_keys = {m['public_key'] for m in members}
# Read D and E keys
import subprocess
d_key = subprocess.run(['cf', 'identity', '--json'],
    capture_output=True, text=True,
    env={**dict(__import__('os').environ),
         'CF_HOME': '/tmp/campfire-integ/agents/agent-d'}
).stdout.strip()
d_key = json.loads(d_key)['public_key']
e_key = subprocess.run(['cf', 'identity', '--json'],
    capture_output=True, text=True,
    env={**dict(__import__('os').environ),
         'CF_HOME': '/tmp/campfire-integ/agents/agent-e'}
).stdout.strip()
e_key = json.loads(e_key)['public_key']
if d_key in member_keys:
    print('FAIL: Agent D (reviewer) incorrectly joined implementation campfire')
    sys.exit(1)
if e_key in member_keys:
    print('FAIL: Agent E (QA) incorrectly joined implementation campfire')
    sys.exit(1)
print('OK: Only implementers joined the implementation campfire')
"
```

#### 4.1.2 Message DAG Completeness

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

#### 4.1.3 Provenance Chain Validity

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

#### 4.1.4 Code Artifact Correctness

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
5. Discovery log: which beacons each agent saw and which campfire they chose to join (extracted from agent logs)

### 4.3 Verification Summary

> **Changed from previous design:** Added discovery and self-selection checks. Membership checks are now framed as "did agents discover and join correctly" rather than "did the harness join them correctly."

| Check | Method | Pass Criteria |
|-------|--------|---------------|
| **Discovery: CF1 found** | Agent logs show `cf discover` returning CF1 | All 4 workers found CF1 |
| **Self-selection: CF1 membership** | `cf members` on CF1 | 5 members (all agents joined) |
| **Self-selection: CF2 membership** | `cf members` on CF2 | 2 members (only B and C — implementers) |
| **Self-selection: CF2 exclusion** | Check D and E keys not in CF2 | Reviewer and QA did not join implementation campfire |
| DAG completeness | Parse CF1 messages | 4 futures, each with >= 1 fulfillment |
| Provenance validity | `cf inspect` every message | All signature_valid = true |
| Code exists | File existence check | fizzbuzz.go and main.go present |
| Code correct | `go run .` output check | 100 lines, correct fizzbuzz values |
| CF2 isolation | Read CF2 as non-member | Error or empty result |
| DONE file | Content check | Contains "PASS" |

## 5. Known Risks and Mitigations

### 5.1 LLM Non-Determinism

Agents are LLMs. They may not follow instructions perfectly. Mitigations:
- Instructions are explicit, step-by-step, with exact commands
- The fizzbuzz task is simple enough that implementation correctness is likely
- The verification checks distinguish protocol failures from task failures — a wrong FizzBuzz implementation with correct message DAG still validates the protocol

### 5.2 Discovery Failure — Agent Picks Wrong Campfire

> **New risk — specific to the discovery-first approach.**

An agent might misread a beacon description and join the wrong campfire, or join a campfire that doesn't match its skills.

**Likelihood:** Low. Beacon descriptions are explicit and short. "Need: Go implementer, code reviewer, QA tester" is unambiguous. "Implementation coordination — fizzbuzz project. For Go implementers" is clear about its audience.

**Mitigation:**
- Beacon descriptions use plain, structured language with explicit role names
- Agent CLAUDE.md explicitly states the agent's skills and instructs it to match skills to beacon descriptions
- Reviewer and QA agents are told "Do NOT join implementation-only campfires"
- Verification checks confirm correct self-selection (Section 4.1.1)

**If it happens:** The test still works if an extra agent joins CF2 — it just adds noise. If an agent joins NO campfire, it will exhaust turns polling `cf discover` and the test fails on the DONE file timeout, with agent logs showing the discovery failure.

### 5.3 Discovery Failure — Agent Doesn't Understand Beacon Descriptions

> **New risk — specific to the discovery-first approach.**

An agent might not parse `cf discover` output correctly, or might not understand how to evaluate a beacon description against its skills.

**Likelihood:** Medium. This is the core thing we're testing. LLMs are generally good at matching descriptions to capabilities, but the `cf discover` output format needs to be clean and parseable.

**Mitigation:**
- Agent CLAUDE.md includes `cf discover --json` in the CLI reference so agents know the command
- MCP agents get `campfire_discover` in their tool reference
- Beacon descriptions are written in plain English, not encoded or abbreviated
- If `cf discover` returns no results, agents are told to retry (campfires may not exist yet)

### 5.4 Timing — Workers Start Before PM Creates Campfire

> **New risk — specific to the discovery-first approach.**

If all agents launch simultaneously, workers might run `cf discover` before Agent A has created CF1 and its beacon.

**Likelihood:** High if agents launch simultaneously.

**Mitigation:** Agent A gets a 5-second head start. Workers are instructed to retry discovery. Even without the head start, workers would just see empty results and retry, burning a few turns but not failing.

### 5.5 Timing — Agent C Discovers Before Agent B Creates CF2

Agent C might discover CF1 and join it, then look for CF2 which doesn't exist yet because B hasn't created it.

**Likelihood:** High — this is the expected sequence.

**Mitigation:** C's CLAUDE.md instructs it to poll `cf discover` multiple times. C can join CF1 first, read the project futures, and keep discovering until CF2 appears. This is the natural flow — C needs to wait for B's coordination signal anyway.

### 5.6 Cost

5 Claude Code sessions running for 5-9 minutes each. Estimated cost: ~$5-20 depending on model tier. Slightly higher than the pre-injected design because discovery adds 2-4 extra turns per worker agent. The harness should log token usage from each session's JSON output for cost tracking.

### 5.7 MCP Server Lifecycle

The `cf-mcp` process is spawned by Claude Code as a child process (via mcp-config.json). It inherits the agent's environment. When Claude Code exits, the MCP server is killed. This is the correct lifecycle — no orphaned processes.

### 5.8 Shared Filesystem Workspace

All agents write to the same workspace directory. This is intentional — it simulates agents collaborating on a shared codebase. The risk is that agents write to unexpected paths or overwrite each other's files. Mitigation: each agent's CLAUDE.md specifies exactly which files to write.

### 5.9 Template Variable Injection

> **Changed from previous design:** Drastically simplified. Only 2 template variables instead of 12+.

The harness must replace template variables in each agent's CLAUDE.md before launching. This is done with `sed` during the setup phase. The variables are:

| Variable | Value |
|----------|-------|
| `{{KEY_A}}` through `{{KEY_E}}` | Full 64-char hex public key (only the agent's own key) |
| `{{WORKSPACE}}` | `/tmp/campfire-integ/shared/workspace` |

No campfire IDs, no peer keys, no team rosters. Agents discover everything else at runtime.

### 5.10 What This Does NOT Test

- Recursive composition (campfire as member of campfire)
- P2P HTTP transport (uses filesystem for simplicity and reliability)
- Threshold signatures (threshold=1 for all campfires)
- Agent eviction or campfire disbanding
- Filter optimization
- More than 5 agents
- Network partitions or offline recovery
- Invite-only campfire discovery (CF2 is open in this design, so C can join without admission)

These are explicitly out of scope per the bead description and are better tested in isolation.

## 6. What This Tests That the Previous Design Did Not

> **New section — justifies the discovery-first approach.**

The discovery-first design validates capabilities that the pre-injected design skipped entirely:

| Capability | Pre-Injected Design | Discovery-First Design |
|------------|---------------------|----------------------|
| Beacon publishing | Not tested (harness created campfires) | Tested: A creates CF1 beacon, B creates CF2 beacon |
| Beacon discovery | Not tested (IDs injected) | Tested: All workers run `cf discover` |
| Self-selection | Not tested (harness decided membership) | Tested: Agents read beacons, match skills, choose |
| Correct exclusion | Not tested | Tested: D and E should NOT join CF2 |
| Dynamic campfire creation by workers | Not tested (harness created CF2) | Tested: B creates CF2 after joining CF1 |
| Full autonomous coordination | Partial — agents had all IDs upfront | Full — agents discover, evaluate, join, read, reason, act, respond |

The pre-injected design tested message passing and DAG construction. The discovery-first design tests the **full funnel** — from "I'm a worker, find me work" to "work complete."

## 7. File Manifest

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
