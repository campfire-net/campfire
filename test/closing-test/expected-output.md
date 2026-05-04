# Expected Output — hello-world Cold-Start Test

This document defines what a successful cold-start run must produce.
The `run-cold-start.sh` script compares actual output against these patterns.

## 1. Convention declaration file

`hello-world-greet.json` must be valid JSON and contain all required fields.
Exact field order does not matter. Content must match:

```json
{
  "convention": "hello-world",
  "version": "0.1",
  "operation": "greet",
  "signing": "member_key",
  "min_operator_level": 0,
  "description": "Send a greeting to the campfire",
  "args": [
    {
      "name": "name",
      "type": "string",
      "required": true,
      "max_length": 64
    }
  ],
  "produces_tags": [
    {
      "tag": "hello:greeted",
      "cardinality": "exactly_one"
    }
  ]
}
```

**Validation rules (checked by run-cold-start.sh):**
- `jq` parses without error
- `.convention == "hello-world"`
- `.operation == "greet"`
- `.signing == "member_key"`
- `.min_operator_level == 0`
- `.args | length >= 1`
- `.args[0].name == "name"` and `.args[0].type == "string"`
- `.produces_tags | length >= 1`
- `.produces_tags[0].tag == "hello:greeted"`

## 2. Lint output

`cf convention lint hello-world-greet.json` must exit 0.

Expected output pattern (any of these indicate success):
- Contains `ok` or `OK` or `valid` or `passed` or `lint: pass`
- Exit code 0 is the primary check; output text is informational

If lint is not yet implemented, `python3 -c "import json,sys; json.load(open('hello-world-greet.json'))"` exit 0 is an acceptable fallback.

## 3. Campfire ID

`cf create --transport filesystem --description "hello-world-test"` must
return a 64-hex-character campfire ID.

Expected pattern: `[0-9a-f]{64}`

## 4. CLI call output

`cf <campfire-id> greet --name "Alice"` must exit 0.

Expected output contains one of:
- A message ID (64-char hex)
- `sent` or `ok` or `posted`
- A JSON object with `message_id` field

## 5. Read-back output

`cf read <campfire-id> --all` must show a message with tag `hello:greeted`.

Expected: output contains the string `hello:greeted`

## 6. MCP tool call output

Starting `cf-mcp` and calling `hello-world_greet` with `{"name":"Alice"}`:

Expected tool response contains:
- `hello:greeted` tag reference OR
- A message ID confirming the operation was dispatched

The MCP tool name is derived from the convention as: `<convention>_<operation>`
with hyphens converted to underscores: `hello_world_greet` or `hello-world_greet`.

## 7. Attribution

`cf read <campfire-id> --all` output should show the sender's public key
matching `cf id` output (first 12+ chars). This check is advisory — skip
if the output format doesn't expose sender keys inline.

## Summary check table

| Check | Command | Pass condition |
|-------|---------|---------------|
| Declaration valid JSON | `jq . hello-world-greet.json` | exit 0 |
| Required fields present | `jq` field checks | all fields present |
| Lint passes | `cf convention lint` | exit 0 |
| Campfire created | `cf create` | 64-char hex ID in output |
| Greet op succeeds | `cf <id> greet --name Alice` | exit 0 |
| Tag appears | `cf read <id> --all` | contains `hello:greeted` |
| MCP tool callable | `cf-mcp` tool call | exit 0, response present |
