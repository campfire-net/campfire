# Cold-Start Verification Prompt — cf 0.30 §10.5

**You are a fresh Claude session with NO prior knowledge of campfire (cf).**
You must read ONLY the documents listed below. Do not use any other knowledge
about campfire internals, wire formats, or prior versions. If you find yourself
guessing at something not covered in the listed docs, stop and flag it as a
doc gap.

## Documents you are allowed to read

Read these files from the campfire repo root (adjust path as needed):

1. `docs/agent/quickstart.md` — identity, minimal flow, integration hierarchy
2. `docs/agent/convention-authoring.md` — how to write a convention declaration
3. `docs/agent/gate-predicates.md` — gate predicates including `level:`
4. `cf-conventions/demos/agent-cold-start.sh` — runnable reference implementation

You MAY also read `docs/upgrade-0.19-to-0.30.md` if you need to understand
what changed from a prior version — but you should not need it for this task.

## Your task

Implement, deploy, and demonstrate a new convention called **`hello-world`**
with a single operation **`greet`**.

### Convention specification

```
convention:  hello-world
version:     0.1
operation:   greet
signing:     member_key
args:
  - name: name
    type: string
    required: true
    max_length: 64
gate:        level: 0  (anonymous — any valid keypair)
produces:    tag "hello:greeted" (cardinality: exactly_one)
description: "Send a greeting to the campfire"
```

### What you must produce

1. **The convention declaration file** — `hello-world-greet.json` containing
   the valid JSON declaration for the operation above.

2. **A lint verification** — run `cf convention lint hello-world-greet.json`
   and show the output confirming it passes.

3. **A test campfire** — create a local filesystem campfire:
   ```bash
   cf init --display-name "hello-world-agent"
   cf create --transport filesystem --description "hello-world-test"
   # note the campfire ID
   ```

4. **Deploy the convention** — promote the declaration to the test campfire:
   ```bash
   cf convention promote hello-world-greet.json --registry <campfire-id>
   ```

5. **Call it via cf CLI** — send the `greet` operation:
   ```bash
   cf <campfire-id> greet --name "Alice"
   ```
   Show the output.

6. **Read it back** — read the campfire and confirm the `hello:greeted` tagged
   message appears:
   ```bash
   cf <campfire-id> read --all
   ```
   (or `cf read <campfire-id> --all` — whichever form the docs show)

7. **Call it via cf-mcp** — start cf-mcp pointed at the same campfire and call
   the `hello-world_greet` tool. Show the MCP tool call and response JSON.

   Hint: `cf-mcp` auto-registers convention tools on join. The tool name is
   `<convention>_<operation>` with hyphens replaced by underscores if needed.

## Success criteria

The run passes if ALL of the following are true:

- [ ] `hello-world-greet.json` is valid JSON with all required fields
  (`convention`, `version`, `operation`, `signing`, `args`, `produces_tags`,
  `min_operator_level`)
- [ ] `cf convention lint` exits 0
- [ ] `cf <campfire-id> greet --name "Alice"` exits 0
- [ ] `cf read <campfire-id> --all` shows a message tagged `hello:greeted`
- [ ] The MCP tool call `hello-world_greet` with `{"name":"Alice"}` returns
  a response that includes the message ID and `hello:greeted` tag

If any step fails, note EXACTLY which doc was missing or incorrect and what
information would have prevented the failure. These become doc-gap findings.

## Output format

Structure your output as:

```
## Declaration
<contents of hello-world-greet.json>

## Lint output
<cf convention lint output>

## Campfire ID
<the ID cf create returned>

## CLI call output
<cf <campfire-id> greet --name "Alice" output>

## Read-back output
<cf read <campfire-id> --all output>

## MCP call output
<the MCP tool call and response>

## Result
PASS or FAIL

## Doc gaps (if any)
- <doc name>: <what was missing>
```
