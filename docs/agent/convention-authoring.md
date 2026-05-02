# Convention Authoring — cf 0.30

A convention is a JSON declaration that describes one typed operation: its arguments, the tags it composes onto outgoing messages, how it is signed, and optional rate limits. Once promoted to a campfire, every connected MCP client sees it as a callable tool automatically.

## Minimal declaration

```json
{
  "convention": "my-service",
  "version": "0.1",
  "operation": "submit-task",
  "signing": "member_key",
  "args": [
    { "name": "task_id",  "type": "message_id", "required": true },
    { "name": "payload",  "type": "string",     "required": true, "max_length": 4096 }
  ],
  "produces_tags": [
    { "tag": "task:submitted", "cardinality": "exactly_one" }
  ]
}
```

Save this as `my-service-submit-task.json`. Then:

```bash
# Validate locally (no live campfire needed)
cf convention lint my-service-submit-task.json

# Run through the full executor pipeline against an ephemeral campfire
cf convention test my-service-submit-task.json

# Promote to a live registry campfire
cf convention promote my-service-submit-task.json --registry <campfire-id>
```

## Runnable demo

`cf-conventions/demos/agent-cold-start.sh` writes a convention, lints it, promotes it to a local campfire, and calls it. Reading those ~100 lines gives you a working template.

## Declaration fields

| Field | Required | Notes |
|---|---|---|
| `convention` | yes | Convention name. Scopes all operations in this family. |
| `version` | yes | Semver string. |
| `operation` | yes | The callable operation name. Becomes the MCP tool name. |
| `signing` | yes | `"member_key"` or `"campfire_key"`. |
| `args` | no | Array of `ArgDescriptor`. Empty = no args. |
| `produces_tags` | no | Tag rules composed onto the outgoing message. |
| `antecedents` | no | Threading rule. See §Antecedent rules below. |
| `rate_limit` | no | `{ "per": "keypair", "count": 10, "window": "1m" }` |
| `min_operator_level` | no | Provenance gate: 0 (none), 1–3. See `gate-predicates.md`. |
| `description` | no | Human-readable; appears in MCP tool description. |

### Arg types

| Type | Validates |
|---|---|
| `string` | UTF-8, optional `max_length` |
| `message_id` | 64-char hex string |
| `enum` | Must be in `values` list |
| `object` | JSON object |
| `int` | Integer |

### Tag cardinality

| Cardinality | Meaning |
|---|---|
| `exactly_one` | Exactly one message with this tag is produced |
| `at_most_one` | Zero or one |
| `any` | Any number |

Tag patterns with `*` suffix (e.g. `"result:status:*"`) match a single-segment wildcard: `result:status:ok`, `result:status:error`.

### Antecedent rules

| Rule | Behaviour |
|---|---|
| `""` (omit) | No antecedents |
| `"exactly_one(target)"` | Uses the `message_id`-typed arg as sole antecedent |
| `"exactly_one(self_prior)"` | Requires caller's prior message with the same operation tag |
| `"zero_or_one(self_prior)"` | Like above but allows genesis (first message) |

## Gate predicates

Add `"min_operator_level": 2` to require contactable provenance before executing. For full predicate language (grant checks, quorum, quota bounds), see `docs/agent/gate-predicates.md`.

## Reserved-op floor

Ten operations are reserved and cannot be made less restrictive by any convention or grant. You MUST NOT declare operations with these names:

```
disband  evict  admit  grant  revoke
delegation-grant  delegation-revoke  delegation-accept
member-roster  compaction
```

These are enforced at L2 before your L3 convention handler is ever invoked. Attempting to declare them will produce `DENY(reserved_op_floor)` at lint time.

## What NOT to do

**Do not use tags for access control alone.** Tags are tainted — any sender can assert any tag. Use the gate predicate system (`min_operator_level`, `grant:`, `chain_to:`) for authorization decisions.

**Do not declare `"not:"` predicates.** They are rejected at parse time by design (§2.2 of the authority spec). Use `any_of` with explicit allow-list predicates instead.

**Do not encode trust levels in display names.** Display names are tainted. Use `chain_to: <pubkey>` to anchor authority to a specific Ed25519 key.

**Do not use `present_as`.** Removed in 0.30. Use cf-authority scoped grants instead.

**Do not use GitHub transport.** Removed in 0.30 (no named consumer). Use `fs` for local dev, `http` for production.

## Promoting and superseding

```bash
# First version
cf convention promote v1.json --registry <campfire-id>

# Supersede with a new version (old declaration retired)
cf convention promote v2.json --registry <campfire-id> --supersedes v1-op-name
```

Once promoted, MCP clients re-run `tools/list` to discover the new operation.

## See also

- `docs/convention-sdk.md` — Go SDK: `convention.NewServer`, `Declaration`, `ArgDescriptor`
- `docs/agent/gate-predicates.md` — full predicate language reference
- `docs/cf-authority-spec.md` — wire format for grants and predicates
