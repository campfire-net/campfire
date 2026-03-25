# Design: Human-App Edge for the Hosted Campfire Service

**Status:** Specification
**Date:** 2026-03-24
**Inputs:** design-human-ui-audit.md, design-human-ui-flow-model.md, design-human-ui-ia.md, design-human-ui-architecture.md, design-human-ui-review.md
**RPT grounding:** RPT spec v0.5, sections 1.2-1.8, 3.1-3.4, 4.1

---

## 1. Problem Statement

The hosted campfire MCP service (`mcp.getcampfire.dev`) serves agents. Agents connect via MCP-over-HTTP, coordinate through structured message exchange, and manage their own campfires. No human can observe, govern, or participate in this coordination without using the CLI or asking an agent to summarize — both of which are expensive relay paths (RPT 1.4).

The result: the human operator is blind. They cannot see what their agents are doing. They cannot intervene when coordination breaks down. They cannot make the decisions only humans can make — security approvals, quality gates, conflict resolution — without burning tokens or attention to relay information that the product should deliver directly.

The human-app edge is the product surface that faces the human operator (RPT 1.3). It must deliver coordination state directly to the human, route human judgment to the moments that require it, and do so without degrading the agent experience. The hosted service without this edge is half a product: it serves one species of user and ignores the other.

### What this design covers

A web-based operator interface for the hosted campfire service. The operator observes agent coordination, governs campfire access, and participates in the protocol through the same MCP tool surface agents use. The UI is a projection of protocol state, not a separate state machine.

### What this design does not cover

- Visual design follows the campfire design language — defined as part of implementation, not a separate engagement
- Mobile-native application (web-responsive only)
- Self-hosted deployment of the UI (this covers the hosted service at `mcp.getcampfire.dev`)
- Changes to the campfire protocol specification
- Marketplace listing or pricing (CEO/strategy scope)

---

## 2. Honest Security Boundary

This section states what the human UI protects against and what it does not. Any documentation or UI copy that implies stronger guarantees than stated here is misleading. This follows the structure and honesty standard of design-mcp-security.md section 2.

### 2.1 What the Human UI Protects Against

| Threat | Protection mechanism | Confidence |
|--------|---------------------|------------|
| **Unauthenticated access to operator views** | Server-side session with HttpOnly/Secure/SameSite=Strict cookie. Ed25519 key material never touches the browser. | High — standard web session model |
| **CSRF on mutation endpoints** | CSRF token on every htmx POST. Server validates token. SameSite=Strict cookies. | High — standard mitigation |
| **SSE connection exhaustion** | Max 1 SSE connection per session token (new kills old). Connection budget of 3 per operator. 30-second keepalive prevents ACA idle timeout disconnection. | Medium — limits blast radius, does not prevent distributed attacks |
| **Invite code enumeration via redirect endpoint** | Rate limit on `GET /invite/{code}` (10/IP/minute). Constant-time response regardless of code validity. Redirect always goes to app login/join flow. 128-bit minimum entropy on codes. | High — enumeration is computationally infeasible |
| **Agent manipulation of operator attention** | Security events pinned above agent-generated items. Per-agent rate limit on unresolved futures (5 per agent per campfire). Meta-alert when single agent generates >10 attention items/hour. Fulfillment of security events requires explicit operator confirmation. | Medium — heuristic, not formal guarantee |
| **Cross-origin SSE hijacking** | SSE endpoint authenticates via session token in cookie + CSRF validation on initial connection. | High |

### 2.2 What the Human UI Does NOT Protect Against

These are structural properties of the hosted deployment model. They are not gaps to be fixed.

1. **Operator reading all unencrypted campfire content.** The cf-ui server has the same Table Storage access as cf-mcp. The operator (us) can read everything. This is inherent to hosting. For confidentiality, use encrypted campfires with at least one self-hosted member (see design-mcp-security.md section 2.3).

2. **Browser-side key custody.** The operator's Ed25519 private key is held server-side, never in the browser. The browser holds a session cookie. The cf-ui server signs protocol messages on the operator's behalf. This means the cf-ui server can impersonate the human operator at the protocol level — the same fundamental constraint as agent sessions (design-mcp-security.md section 2.3).

3. **Real-time attack by a compromised hosting operator.** If the hosting operator is compromised, they control the cf-ui server, the session store, and the Table Storage backend. All detection mechanisms (audit log, transparency log) are retrospective. The UI cannot protect the human operator against the infrastructure operator.

4. **Privacy of operator behavior patterns.** The cf-ui server necessarily observes which campfires the operator visits, which messages they read, and how they interact with the UI. Tier progression is computed client-side (localStorage) to minimize server-side behavioral tracking, but the server still sees HTTP requests. Operators who require behavioral privacy should use the CLI.

### 2.3 Authentication Model

The human UI uses a server-mediated session model. This is a load-bearing architectural decision, not a deferral.

**Flow:**
1. The operator authenticates via `campfire_init` (creating or resuming a session). The cf-ui server calls `campfire_init` as a library function, server-side.
2. The server generates a short-lived session cookie (HttpOnly, Secure, SameSite=Strict) bound to the campfire session token.
3. The browser holds only the cookie. Ed25519 key material stays in cf-ui server memory.
4. The cf-ui server signs protocol messages on the operator's behalf using the session's Ed25519 key.
5. Cookie TTL matches the campfire session token TTL (1 hour, renewable via implicit rotation on active use).

**Why not browser-side keys:** The architecture review (Option 4) correctly identifies the key custody problem — Ed25519 private keys in localStorage are accessible to any JS on the page. Server-side custody is the pragmatic choice until Web Crypto API + MCP client crypto support mature.

**Login flow:** GitHub OAuth is the primary authentication method — the operator clicks "Sign in with GitHub" and the cf-ui server exchanges the OAuth code for a verified identity. Magic link via email is the fallback for operators without GitHub accounts. After OAuth/magic-link authentication, the server calls `campfire_init` server-side to create or resume the operator's campfire session, then issues the HttpOnly session cookie. The operator never sees or handles a campfire session token.

**Why OAuth + campfire session (not OAuth alone):** OAuth authenticates the human. The campfire session provides protocol-layer identity (Ed25519 keypair for signing messages). Both are needed — the OAuth token gates UI access, the campfire session token gates protocol participation. The cf-ui server bridges the two: it maps the OAuth identity to a campfire session and manages the lifecycle of both.

---

## 3. Product Design Goals

### 3.1 The Operator Model

The human facing campfire is not a chat participant. They are an operator: someone whose job is to maintain a system that runs without them most of the time and needs them precisely when it cannot run without them. The closest analog is air traffic control, not Slack.

**Flow calibration (RPT 1.3):** The operator is in flow when every notification they receive requires a decision only they can make, and every decision they need to make reaches them without delay. The system continuously adjusts what it surfaces so the operator is always at peak competence — never bored by trivia, never overwhelmed by volume.

Flow calibration operates at two timescales:

1. **Across sessions (tier progression):** The system tracks which UI capabilities the operator uses and progressively foregrounds more sophisticated tools as the operator's mental model deepens. Tier data is computed client-side in localStorage — never transmitted to the server, never associated with the operator's identity in any persistent store. The server ships all tier content; CSS/JS on the client controls visibility.

2. **Within sessions (attention queue throttling):** When the operator has 15+ unresolved attention items, the attention queue raises its threshold — showing only critical items (security events, futures directed at the operator). When the queue drops below 3, the threshold lowers — showing informational items normally suppressed (governance events, agent status shifts). This is a simple heuristic applied to the fast loop (RPT 1.7), not ML-based personalization.

### 3.2 Progressive Disclosure

Three tiers. Not gates — every primitive is accessible from day one. What changes is prominence.

**Tier 1 — Observer:** "I want to see what my agents are doing." The system foregrounds active campfires, message streams grouped by tag, and a single attention indicator for campfires with blockers or questions. Audit details, membership management, encryption state, and filter construction are backgrounded.

**Tier 2 — Manager:** "I want to direct and constrain what agents can do." Membership controls, audit trails with causal threading, campfire lifecycle operations, tag-based filtering with saved views, and rate limit visibility move forward. Triggered by behavioral signals: 5+ distinct campfires opened, at least one future approved/rejected, 100+ messages scrolled in one session. No modal, no banner — controls appear in context on the next session.

**Tier 3 — Composer:** "I want to build my own coordination workflows." Full predicate builder, alert rules, campfire templates, audit graph mode, saved queries as sidebar filters, customizable Home widgets. Triggered by: at least one saved query created, at least one alert rule created, 3+ campfires managed.

**Persistent parity constraint:** Any protocol operation an agent can perform, the operator must be able to perform from the UI at any tier. Operations may be behind a "More actions" overflow menu at tier 1, but they are never absent.

### 3.3 The Composable Substrate (RPT 1.6)

RPT 1.6: do not implement desire paths. Provide composable primitives. Let users compose. Capture what gets composed. Promote the best compositions to canonical interface.

**True primitives** (the atomic building blocks):

| Primitive | Description |
|-----------|-------------|
| **View** | A filtered, ordered projection of messages. Configurable by campfire scope (single or multi), tag filter, time range, agent filter, threading mode. |
| **Predicate** | A structured filter expression. Combines campfire properties with message properties and logical operators. First-class objects with IDs. |
| **Delivery channel** | A notification target: in-app badge, webhook URL, email digest. |
| **Action** | A parameterized protocol operation: send, invite, revoke, compact, fulfill. Exposed as contextual buttons, not a menu. |

**Named compositions** (sugar over primitives — the UI offers these as shortcuts, but they are not atomic types):

| Composition | Components | Description |
|-------------|------------|-------------|
| **Saved query** | Predicate + view config + name | A reusable, shareable lens on coordination state |
| **Alert rule** | Predicate + delivery channel | "When X matches, notify me via Y" |
| **Campfire template** | Configuration preset (protocol, encryption, tags, compaction) | One-click campfire creation |
| **Audit projection** | View + causal graph rendering | Messages as nodes, reply-to and fulfills as edges |

This distinction matters for implementation: the system must allow any primitive to connect to any other primitive. A predicate can drive a view, an alert, or an action. The named compositions are convenience paths, not the only paths. An operator who wants a predicate applied transiently to the current view should not be forced through the saved query creation flow.

**Desire path capture (human-side):** When an operator applies the same tag filter 3+ times in a session, the UI surfaces "Save this filter?" — pre-populated with the predicate. When an operator manually approves 3+ futures from the same agent, the UI offers to create an alert rule for that pattern.

**Alias capture (agent-side):** The cf-mcp server logs tool invocation sequences per session. When a sequence recurs across sessions (e.g., `campfire_create` + `campfire_invite` x5 + `campfire_send --tag status`), it is flagged as a composition candidate. These candidates surface in the Compose view for the operator to review and promote to templates. This closes the RPT 1.6 flywheel for the agent edge, not just the human edge.

### 3.4 Dual-Audience Constraint (RPT 1.8 Rule 1)

Every interface has two audiences. The human UI must not degrade the agent experience. Every UI action that modifies protocol state calls the same package functions that cf-mcp exposes as MCP tools. The UI does not write directly to Table Storage — it goes through the same store interfaces and business logic.

**Two-audience gap analysis:** The following elements are marked human-only with justification:

| Human-only element | Justification | Agent-facing path |
|--------------------|---------------|-------------------|
| **Attention queue** | Agents should know what requires human attention to avoid redundant escalations. | New MCP tool: `campfire_attention_queue` returns the operator's pending items (futures, unresolved blockers). Ships in month 1 milestone. |
| **Cross-campfire audit** | PM agents managing swarms need cross-campfire governance views. | New MCP tool: `campfire_cross_audit` returns audit events across owned campfires. Ships in month 3 milestone. |
| **Notification preferences** | Agents sending futures benefit from knowing if the operator is in quiet hours. | New MCP tool: `campfire_notification_status` returns the operator's notification state (active/quiet). Ships in month 3 milestone. |

These MCP tools are not in the MVP scope but their data models are designed now so the human-only views do not create data structures that are difficult to expose to agents later.

**Rejected UI features (fail dual-audience test):**
- Message editing: breaks message authenticity (agents signed the original)
- Message pinning: adds metadata agents must account for (unless pin is human-only metadata invisible to agents)
- Rate limit override without notification: changes agent operating environment without awareness

**Required safeguards:**
- Rate limit changes via UI emit a system message visible to all campfire members
- Campfire archival sends a final message to all members before closing

---

## 4. API Surface

### 4.1 Tool Categorization

20 MCP tools cataloged (from `cmd/cf-mcp/main.go`):

| Category | Count | Tools |
|----------|-------|-------|
| **human-app** (UX priority) | 8 | campfire_init, campfire_export, campfire_ls, campfire_read, campfire_trust, campfire_invite, campfire_revoke_invite, campfire_revoke_session, campfire_rotate_token |
| **agent-only** | 2 | campfire_commitment, campfire_await |
| **dual-audience** | 10 | campfire_id, campfire_create, campfire_join, campfire_discover, campfire_members, campfire_send, campfire_inspect, campfire_dm, campfire_audit, campfire_trust |

### 4.2 New Endpoints Required

Ordered by blocking impact on a functional human web UI:

| Priority | Endpoint | Purpose | Blocks |
|----------|----------|---------|--------|
| **P0** | `GET /events` (SSE, multiplexed) | Live message feed, unread counts, presence, futures, system events | Without this, the UI is a manual-refresh viewer |
| **P0** | Session auth (cookie-based, section 2.3) | Every authenticated endpoint | Everything |
| **P1** | `GET /invite/{code}` (redirect) | Shareable invite links resolve to app join flow | Invite sharing UX |
| **P1** | `GET /session/export` (presigned URL redirect) | Data portability as .tar.gz download via presigned Azure Blob Storage URL | Export UX — keys never transit through LLM provider (resolves S4/S12). Both cf-ui and cf-mcp use presigned URLs. |
| **P1** | `POST/GET/DELETE /c/{id}/invites` | Invite CRUD REST | Invite management UI |
| **P1** | `POST /c/{id}/m/{id}/fulfill` | Human fulfillment of agent futures | Human-in-the-loop approval flows |
| **P2** | Presence heartbeat via SSE + `last_seen` in members | Member online status | Member status indicators |
| **P2** | `GET /search?q=...` | Full-text search across campfires | "Find that old message" workflow |
| **P3** | `GET /dashboard` (or derive from SSE) | Aggregated activity summary | Home screen activity numbers |
| **P3** | `POST /c/{id}/compact` | Compaction via UI | Long-lived campfire maintenance |

### 4.3 MCP Call-Through

For tools not listed above, the web UI calls MCP tools as library functions via the cf-ui server (direct package call, no HTTP round-trip to cf-mcp). This is viable for low-frequency, non-streaming operations: create, join, trust, revoke, rotate, discover, members, inspect, dm, audit.

MCP call-through is **not viable** for: streaming/real-time (read feed, presence, unread), file downloads (export), URL-based flows (invite links), and background operations that should not require an active MCP session frame.

---

## 5. UI Architecture

### 5.1 Decision

**Hybrid: Go backend (Azure Container Apps) + htmx + SSE for real-time. MCP-over-HTTP remains the agent API.**

The agent interface (MCP-over-HTTP on Azure Functions) is unchanged. The human UI is a Go binary on Azure Container Apps, serving server-rendered HTML enhanced with htmx and a single multiplexed SSE stream for live updates.

```
Agent (MCP client)
  |
  | MCP-over-HTTP (unchanged)
  v
Azure Functions (func-campfire-bpjpsl) -- existing
  +-- cf-functions.exe + cf-mcp.exe

Human operator (browser)
  |
  | HTTPS (HTML + SSE)
  v
Azure Container Apps -- new
  +-- cf-ui.exe (Go binary)
        |-- /                       -> campfire list (observer view)
        |-- /c/{id}                 -> message feed + SSE
        |-- /c/{id}/members         -> membership panel
        |-- /c/{id}/invites         -> invite CRUD
        |-- /settings               -> identity, session, audit
        |-- /search                 -> full-text search
        |-- /invite/{code}          -> 302 redirect (invite resolution)
        |-- GET /session/export     -> streamed .tar.gz download
        |-- POST /c/{id}/m/{id}/fulfill -> future fulfillment
        +-- GET /events             -> SSE stream (multiplexed)

Shared backend:
  +-- Azure Table Storage (stcampfirebpjpsl)
```

### 5.2 SSE Design

The SSE stream at `/events` multiplexes all of the operator's campfires. The cf-ui server maintains one fanout goroutine per operator session.

| Event type | Payload | Drives |
|------------|---------|--------|
| `message` | campfire ID, sender, tag, body excerpt, thread ID | Live message feed (htmx swap) |
| `unread` | campfire ID, count delta | Sidebar unread badge |
| `presence` | campfire ID, member ID, last_seen | Member status indicator |
| `future` | campfire ID, message ID, preview | Attention queue card |
| `system` | campfire ID, event type | Campfire list updates |
| `:keepalive` | (SSE comment, no payload) | Prevents ACA idle timeout disconnection |

**SSE threat model (resolves review finding A-1):**
- Max 1 SSE connection per session token. Opening a new connection kills the old one.
- Connection budget: 3 concurrent SSE streams per operator (for multi-tab use).
- Authentication: session cookie validated on connection open. Periodic re-validation via heartbeat (every 60 seconds the server checks the session is still valid).
- Goroutine budget: each SSE connection spawns exactly one goroutine for fanout. The goroutine exits when the connection closes. No unbounded goroutine creation.
- Keepalive: `:keepalive\n\n` comment every 30 seconds. This prevents ACA's ingress proxy from killing idle connections (default idle timeout is 4 minutes; configurable via `ingress.transport.connectionIdleTimeout`).

**ACA idle timeout (resolves review finding S-1):** ACA's HTTP ingress has a 4-minute idle timeout on connections. The 30-second keepalive ensures the SSE stream survives quiet periods. The specific ACA setting (`ingress.transport.connectionIdleTimeout`) should be set to 300 seconds in the Bicep template as defense-in-depth.

### 5.3 Consistency Model (resolves review finding S-2)

The cf-ui server and cf-mcp (on Azure Functions) share the same Table Storage account. The consistency model is:

**Partition key scheme:** Campfire ID is the partition key. Message row keys are `{timestamp-nanos}-{sender-hash-prefix}` — this prevents collisions when both processes write simultaneously to the same campfire.

**Write coordination:** The cf-ui server calls cf-mcp package functions as a Go library (in-process). There is one writer implementation, not two. Both the Azure Functions handler and the cf-ui handler call the same `store.AddMessage()` function, which uses Table Storage optimistic concurrency (ETags). On 412 Precondition Failed, the caller retries with a fresh ETag.

**Read latency for SSE fanout:** The cf-ui fanout goroutine polls Table Storage with a watermark (last-seen timestamp) on a 1-second interval. Expected latency from cf-mcp write to cf-ui SSE delivery: 1-2 seconds. This is documented as the latency budget. The polling interval is configurable per deployment.

**Table Storage consistency guarantee:** Strong consistency within a partition (campfire). Since all messages for a campfire share a partition key, reads within a campfire are consistent. Cross-campfire reads (e.g., the activity dashboard) may reflect different campfires at slightly different times — this is acceptable for a UI that shows approximate aggregate counts.

### 5.4 htmx Pattern

Every UI interaction is a server round-trip returning an HTML fragment. No client-side state machine.

```html
<!-- Send message -->
<form hx-post="/c/{id}/send" hx-target="#message-feed" hx-swap="beforeend">
  <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
  <textarea name="message"></textarea>
  <select name="tag"><option value="">No tag</option>...</select>
  <button type="submit">Send</button>
</form>
```

The server handler calls `campfire_send` (direct package call), then returns an HTML fragment of the new message. htmx inserts it. No JSON parsing, no component lifecycle.

**CSRF protection (resolves review finding A-6):** Every htmx form includes a CSRF token injected via Go template. The cf-ui server validates the CSRF token on every POST using standard Go `csrf` middleware. Session cookies are `SameSite=Strict`.

### 5.5 Large-Campfire Rendering (resolves review finding A-3)

For campfires with 50+ members or high message volume:

- **Message batching:** The client buffers SSE `message` events for 500ms and renders them as a single DOM insert. This requires a small client-side JS module (~30 lines) that is compatible with htmx. Each batch is one DOM mutation, not N.
- **Member list pagination:** The members panel shows the first 50 members with a "Show all N" link. Virtualized rendering is deferred to month 3 — pagination is sufficient for MVP.
- **Server-side throttling:** For campfires exceeding 10 messages/second, the SSE stream downsamples to 10 events/second and sends a `throttled` indicator. The UI shows "High activity — showing summary" and switches to a tag-frequency summary view.
- **Scale indicator:** Campfires with 50+ members display a "Large campfire" badge. The default view shifts from full message feed to a summary: tag frequency breakdown, active agents count, last 10 messages. The operator can switch to full feed explicitly.

### 5.6 Why Azure Container Apps

Azure Functions is correct for the agent MCP interface (stateless, scale-to-zero, request-response). It is wrong for the human UI:

- SSE requires persistent HTTP connections (minutes, not seconds)
- Cold starts break ongoing SSE streams
- No keepalive mechanism in the Functions model

ACA supports persistent HTTP connections natively, custom scaling, and VNet egress to the same Table Storage backend. The cf-ui binary runs with `minReplicas=1` to avoid cold-start disruption of SSE streams. Cost: ~$30-50/month (0.25 vCPU, 0.5 GiB). Acceptable for early launch; revisit scale-to-zero with KEDA HTTP scaler if cost becomes significant.

---

## 6. Information Architecture

### 6.1 Navigation Structure

Five top-level sections in a persistent left rail:

| Section | Tier foregrounded | Human action |
|---------|------------------|-------------|
| **Home** | Tier 1 | Attention routing: "what needs me now?" |
| **Campfires** | Tier 1 | Browse, open, read a specific campfire |
| **Activity** | Tier 2 | Audit, membership, governance across campfires |
| **Compose** | Tier 3 | Build saved queries, alert rules, templates |
| **Settings** | All | Identity, security, export, notifications |

Red badge on rail icons indicates attention-needed state (unresolved blockers, pending futures). Updated live via SSE. Depth never exceeds two levels: section -> item detail.

### 6.2 Home (Attention Queue)

The operator's triage surface. Answers one question: what requires human judgment right now?

**Components in priority order:**

1. **Attention queue** — ranked list of items requiring operator action:
   - Security events (always pinned to top, resolves review finding A-5)
   - Unresolved futures directed at this operator's session
   - Unresolved blockers exceeding age threshold (no fulfills)
   - At tier 2+: governance events (new member joined, invite code used)
   - At tier 3+: alert rule triggers the operator has defined

2. **Activity summary** — three numbers only: active campfires (last hour), pending attention items, agents online (last 5 minutes).

3. **Recent campfires** — 3-5 campfires with most recent activity. One click to open.

**Within-session flow calibration (resolves review finding R-1):** The attention queue dynamically adjusts its threshold based on current queue depth:
- Queue depth > 15: show only security events and futures directed at operator
- Queue depth 5-15: show security events, futures, and blockers > 30 min
- Queue depth < 3: show all configured events including informational items

This is the fast-loop application of RPT 1.3 — adjusting challenge to ability within a session, not just across sessions via tier progression.

**Real-time:** Attention queue and activity summary update live via SSE. A new pending future appears within 500ms. "Agents online" updates on 30-second presence heartbeat.

### 6.3 Campfire Detail View

Two-panel layout: message feed (left), context panel (right).

**Message feed:** Chronological, newest at bottom. Each message shows: agent display name (from trust labels), tag chips (color-coded), message text (collapsed to 3 lines), age, thread indicator, fulfillment indicator, pending future badge.

**Feed controls:** Tag filter chips (OR'd), search within campfire, time range selector (defaults to 7d for campfires with 1000+ messages).

**Compose box:** Text input, tag selector, reply toggle, future toggle, send (Cmd/Ctrl+Enter).

**Real-time:** New messages arrive via SSE. "N new messages" banner when operator has scrolled up. No auto-scroll during history reading.

**Thread visualization:** Collapsed by default (flat feed with "N replies" count). Click to expand inline, capped at 3 indentation levels. Deeper chains link to audit graph. Mode toggle visible at tier 2+.

**Context panel tabs:**
- **Members** — list with display name, role badge, online indicator, join date. Read-only at tier 1; invite/revoke at tier 2+.
- **Details** — campfire ID, protocol, encryption, message/member count. Edit and compaction at tier 2+.
- **Audit** — flat table at tier 2 (default), causal graph at tier 3. Interactive: click nodes, zoom, highlight causal chains.
- **Invites** — tier 2+ only. Active codes with copy link and revoke. Generate new with optional use-limit and expiry.

### 6.4 Activity View (Manager Surface)

Cross-campfire governance. Tabs:
- **Audit log:** unified chronological table, filterable by campfire/event type/date/agent, exportable as CSV. Live updates via SSE.
- **Pending escalations:** all unresolved futures across campfires. Approve/reject/reply actions.
- **Members:** cross-campfire view of all agents. Bulk revoke capability.

### 6.5 Compose View (Composer Surface)

Library of composable objects:
- **Saved queries:** predicate + view config + name. Predicate builder with live preview.
- **Alert rules:** predicate + delivery channel. Activates immediately on save.
- **Campfire templates:** configuration presets for one-click campfire creation.
- **API panel:** raw MCP tool invocation. Lists all 20 tools with parameter forms and response viewer. Always accessible at any tier — the escape hatch.

**Predicate builder responsiveness (resolves review finding S-4):** The predicate builder debounces server calls at 300ms (`hx-trigger="input changed delay:300ms"`). If round-trip latency still produces sluggish UX, a self-contained vanilla JS filter module (~200 lines, no framework) replaces the htmx version. This does not violate the no-framework constraint. Deferred to month 3 per MVP scope.

### 6.6 Settings

- **Identity:** display name, public key with copy/QR
- **Security:** active sessions with terminate, rotate credentials
- **Trust/Contacts:** known public keys with editable display names
- **Notifications:** global preferences, per-campfire overrides, delivery channels
- **Export:** "Export my data" button triggers `GET /session/export` as file download

### 6.7 Real-Time Elements

| Element | Mechanism | Latency target |
|---------|-----------|---------------|
| New messages in open campfire | SSE `message` event | < 500ms |
| Unread counts in sidebar | SSE `unread` event | < 500ms |
| Attention queue updates | SSE `future` event | < 500ms |
| Presence indicators | SSE `presence` event, 30s interval | 30s |
| Audit log (Activity view) | SSE `system` event | < 2s |
| Alert rule triggers | SSE event | < 1s |

### 6.8 Notification Model

**Anti-firehose default:** A new operator receives notifications only for futures directed at them and security events. Routine agent status updates never trigger notifications. The system is quiet by default.

| Event | Default (owner) | Default (member) |
|-------|-----------------|------------------|
| Future directed at your session | Always | Always |
| Blocker unresolved > 30 min (owned) | Always | Off |
| Invite code used | Always | Off |
| New member joined (owned) | Always | Off |
| Schema-change tag (owned) | Digest | Off |
| Any message | Off | Off |

**Grouping:** Events within 30-second window grouped for external delivery (webhook, email). In-app attention queue always shows individual items.

**Quiet hours:** Time range during which only futures directed at the operator are delivered.

---

## 7. RPT Compliance Matrix

Every major design decision mapped to RPT principles. Honest about where compliance is mechanical (enforced by architecture) versus aspirational (depends on implementation quality).

| Decision | RPT Principle | Compliance | Evidence |
|----------|--------------|------------|----------|
| Operator as observer/governor, not chat participant | 1.3 (human-app edge: calibrate challenge to ability) | **Mechanical.** The UI surfaces attention items ranked by decision-requiring severity. It does not present a raw message stream as the default view. | Home attention queue with priority ranking and within-session threshold adjustment. |
| Progressive disclosure (3 tiers) | 1.3 (Vygotsky ZPD: just past what you can do alone) | **Aspirational.** Tier progression is heuristic-based (behavioral signals). Whether the thresholds are correctly calibrated requires measurement. | Tier computation is client-side. Thresholds (5 campfires, 100 messages, etc.) are starting points. The medium loop (section 8) measures whether they are correct. |
| True primitives vs. named compositions | 1.6 (composable substrate) | **Mechanical.** Implementation must allow any primitive to connect to any other. | Predicate, view, delivery channel, and action are independent types. Saved queries, alert rules, and templates are compositions stored as references to primitives, not monolithic objects. |
| Agent alias capture | 1.6 (capture what gets composed) | **Aspirational.** Requires MCP-side tool invocation logging and sequence pattern detection. The data pipeline is designed but unbuilt. | Ships in month 3. The cf-mcp server logs tool sequences now (append-only JSONL); pattern detection is the month-3 addition. |
| Human desire path capture | 1.6 (hallucination-as-desire-path applied to humans) | **Mechanical.** The "Save this filter?" prompt is triggered by repeating filter use. | Client-side counter. Fires after 3 identical filter applications in a session. |
| Dual-audience guarantee | 1.8 rule 1 (every interface has two audiences) | **Partially mechanical.** UI mutations call the same package functions as MCP tools. Three human-only views have identified agent-facing gaps (section 3.4) with planned MCP tool additions. | The cf-ui server imports the same `store` and `mcp` packages as cf-mcp. Code path is shared. |
| Tag chips, audit graph, attention queue: declared recipients | 1.8 rule 3 (declare your recipient) | **Mechanical.** Every UI element has a declared recipient (section 6). | IA document assigns Human/Agent/Both to every element. |
| SSE stream, not polling | 1.4 (asymmetric relay costs) | **Mechanical.** Information arrives when state changes, not on a refresh interval. The operator's mental model updates passively. | SSE endpoint pushes events. No periodic HTTP polling from the browser. |
| All mutations through MCP tool functions | 1.8 rule 1 (no side channel) | **Mechanical.** The UI cannot do anything an agent cannot do via MCP. The API panel (tier 3) proves this by exposing raw MCP tool invocation in the browser. | Code review can verify: no direct Table Storage writes from UI handlers. |
| CSRF, auth, SSE threat model | 3.2 (circuit breakers) | **Mechanical.** Security controls are middleware, not prompt-level instructions. | Go `csrf` middleware, session validation on every request, SSE connection limits. |

---

## 8. Instrumentation Plan

RPT 1.7 requires three loops running inside the product. RPT 1.8 rule 6 requires instrumentation to ship with the feature. This section defines what telemetry ships with the UI, organized by loop.

### 8.1 Fast Loop (hours) — Agent-Edge Adaptation

Ships with MVP (week 1).

| Signal | Source | Storage | Used by |
|--------|--------|---------|---------|
| Message volume per campfire per hour | SSE fanout goroutine counter | In-memory, exposed via `/metrics` | Activity heatmap on Home; SSE throttling trigger |
| Tag frequency distribution per campfire | Computed from message metadata on SSE delivery | In-memory ring buffer (last 1000 messages per campfire) | Tag frequency shift detection; "regime change" indicator when blocker ratio spikes |
| SSE connection count per operator | cf-ui connection manager | In-memory, exposed via `/metrics` | DoS detection; connection budget enforcement |
| htmx request latency (p50, p95, p99) | cf-ui HTTP middleware | In-memory histogram, exposed via `/metrics` | Performance monitoring; predicate builder responsiveness |

### 8.2 Medium Loop (days) — Human-Edge Calibration

Ships with month 1 milestone. The data model is designed in MVP so events accumulate from day one.

| Signal | Source | Storage | Used by |
|--------|--------|---------|---------|
| Operator session events (start, view-campfire, act-on-future, session-end) | cf-ui HTTP handlers emit structured events | Append-only JSONL in Table Storage (`OperatorEvents` table) | Tier progression calibration; within-session flow adjustment |
| Campfire visit sequence per session | Derived from session events | Same table | Drop-off analysis (opened UI, left without entering any campfire) |
| Time-to-act on futures (time from future delivery to operator fulfillment) | Computed from SSE delivery timestamp vs. fulfill POST timestamp | Derived metric, stored per-session | Attention queue effectiveness measurement |
| Desire path frequency (filter reuse, manual future approvals) | Client-side counter, reported to server on session end | Same table | Desire path capture accuracy |

**Privacy constraint:** Operator session events are associated with the operator's session ID, not their public key. The session ID is ephemeral (1-hour TTL). Aggregation for the slow loop operates on anonymized session data. Operators can disable event collection in Settings -> Privacy (opt-out, not opt-in — the default is collection, because the medium loop needs data to calibrate).

### 8.3 Slow Loop (weeks) — Strategic Product Direction

Ships with month 3 milestone. Depends on medium-loop data accumulation.

| Question | Data source | Mechanism |
|----------|-------------|-----------|
| Are operators using composable primitives or stuck at tier 1? | Tier distribution from medium-loop events | Aggregate tier histogram across operators. If 90%+ remain tier 1 after 30 days, the substrate is not composable enough. |
| Are operators asking agents to summarize campfire state? | Detect `campfire_send` messages containing summarization requests (heuristic: messages from human sessions containing "summarize", "what happened", "catch me up") | This is a relay cost violation (RPT 1.4). Each detected instance is a product failure. Track count over time. |
| Which saved queries appear across multiple operators? | Saved query table with usage counts | Queries used by 10+ operators are candidates for promotion to built-in views. |
| What is the within-session flow calibration accuracy? | Compare attention queue threshold adjustments to operator behavior (did they act on the items surfaced? did they manually dig for items suppressed?) | Accuracy metric: (items surfaced AND acted on) / (items surfaced). If < 50%, the threshold heuristic is wrong. |

**Product health endpoint:** `GET /admin/health` (authenticated, operator-only) returns aggregated slow-loop metrics. Not exposed to the public UI. This is the product team's (our) view into whether the UI is resonating.

---

## 9. MVP Scope

The full design is 3+ months of work. Building all of it before validating the core loop violates RPT 1.5 (prove the loop first). Three milestones with kill/continue gates.

### Week 1 — Proves the Loop

A single Go binary (`cf-ui`) serving:

1. Campfire list with unread counts (SSE `unread` events)
2. Message feed for one campfire with SSE live updates (`message` events)
3. Compose box that sends messages via the same store functions cf-mcp uses
4. 30-second SSE keepalive for ACA idle timeout survival
5. Cookie-based authentication (section 2.3)
6. CSRF protection on all POST endpoints

**What is explicitly not in week 1:** No tiers. No attention queue. No predicate builder. No audit graph. No alert rules. No templates. No search. No notifications beyond in-app.

**Kill/continue gate:** Does a human operator find it useful to observe and participate in agent coordination via a web browser? If operators prefer the CLI or agent summaries, revisit the entire design before investing further.

**Instrumentation in week 1:** Fast-loop signals (message volume, SSE connection count, htmx latency) and the medium-loop event model (events start accumulating even though the medium-loop analysis ships later).

### Month 1 — Proves the Operator Model

Add to week 1:

1. Home attention queue (futures + blockers only, with within-session threshold adjustment)
2. Members panel (read-only)
3. Invite link generation and resolution (`GET /invite/{code}` with rate limiting and enumeration resistance)
4. Future fulfillment flow (`POST /c/{id}/m/{id}/fulfill`)
5. Basic notification (in-app badge only)
6. `campfire_attention_queue` MCP tool (dual-audience parity)

**Kill/continue gate:** Does the attention-routing model (surface only what needs human judgment) actually reduce cognitive load compared to reading the raw message feed? Measure: time-to-act on futures, operator session duration, attention queue accuracy metric.

### Month 3 — Proves the Composable Substrate

Add to month 1:

1. Tier progression (client-side computation, CSS/JS visibility control)
2. Saved queries and predicate builder (with debounced server calls)
3. Alert rules with webhook and email delivery
4. Audit graph (causal visualization)
5. Campfire templates
6. Notification preferences (per-campfire overrides, quiet hours)
7. Agent alias capture (MCP-side tool sequence logging + pattern detection)
8. `campfire_cross_audit` and `campfire_notification_status` MCP tools
9. Search (`GET /search?q=...`)
10. Export as file download

**Kill/continue gate:** Do operators actually compose, or do they stay at tier 1? Measure: tier distribution, saved query creation rate, alert rule creation rate. If 90%+ remain tier 1, the composable substrate needs rethinking — the primitives may be too low-level or too poorly surfaced.

### Deferred (Beyond Month 3)

These items enrich the design but are not required to validate the core hypotheses:

| Item | Review finding | Rationale for deferral |
|------|---------------|----------------------|
| Coordination graph / "radar view" | C-1 | High implementation complexity (requires graph layout library). The ATC metaphor is powerful but the attention queue is the functional equivalent for MVP. Consider for month 4 if operators request spatial visualization. |
| Replay / time-travel scrubber | C-2 | Temporal projection of the audit graph. The data is already ordered by timestamp; the client needs a playback loop. Medium complexity. Consider for month 4 as part of post-incident review tooling. |
| Visual identity / "campfire feel" | C-3 | The IA declares no aesthetic intent. The design is functionally correct but emotionally flat. Declare aesthetic intent (warm palette, glow intensity for activity, subtle animation for state transitions) when a visual designer is engaged. Not an architecture concern. |
| Agent behavioral fingerprinting | C-4 | Lightweight behavioral summary on member profile cards (message frequency, tag distribution). Low complexity but low priority — useful only at scale (10+ agents). |
| Scale-to-zero for cf-ui | S-1 (partial) | KEDA HTTP scaler with 0 min replicas trades $30/month for occasional cold-start SSE disruption. Evaluate when cost matters. |

---

## 10. Attack Resolution Matrix

Every adversarial review finding with resolution status.

### Must-Fix (resolved in this spec)

| ID | Finding | Severity | Resolution | Status |
|----|---------|----------|------------|--------|
| **A-1** | SSE stream is an unauthenticated DoS surface | Critical | Max 1 SSE per session token. Connection budget 3/operator. Session auth on connect + periodic re-validation. Goroutine budget: 1 per connection, exits on close. 30s keepalive. (Section 5.2) | **Resolved** |
| **S-5** | Authentication for human UI is unresolved | High | Server-mediated session: cf-ui calls `campfire_init` server-side, browser holds HttpOnly/Secure/SameSite=Strict cookie, Ed25519 key stays in server memory. (Section 2.3) | **Resolved** |
| **S-2** | Two processes reading same Table Storage, no consistency model | High | Single writer implementation (shared package functions). Partition key = campfire ID. Row key = timestamp-nanos + sender-hash. Optimistic concurrency via ETags. SSE fanout polls with 1s watermark. Documented 1-2s latency budget. (Section 5.3) | **Resolved** |
| **S-3** | MVP surface area too large (3+ months) | Critical | Three milestones: week 1 (proves the loop), month 1 (proves the operator model), month 3 (proves composable substrate). Each has kill/continue gate. (Section 9) | **Resolved** |
| **A-2** | Invite URL endpoint enables code enumeration | High | Rate limit 10/IP/minute. Constant-time response regardless of code validity. 128-bit minimum entropy. Redirect always goes to login/join flow. (Section 2.1) | **Resolved** |

### Should-Fix (resolved in this spec)

| ID | Finding | Severity | Resolution | Status |
|----|---------|----------|------------|--------|
| **A-6** | No CSRF protection on htmx POST endpoints | Medium | CSRF token on every form. Go `csrf` middleware. SameSite=Strict cookies. SSE uses session token + CSRF on initial connect. (Section 5.4) | **Resolved** |
| **R-2** | "8 composable primitives" are not all primitives | High | Refactored: 4 true primitives (view, predicate, delivery channel, action) + 4 named compositions (saved query, alert rule, template, audit projection). Implementation must allow any primitive to connect to any other. (Section 3.3) | **Resolved** |
| **R-3** | Three loops described but only one has concrete mechanisms | High | Instrumentation plan with specific signals, storage, and consumers for all three loops. Fast loop ships with MVP. Medium loop data model designed in MVP, analysis ships month 1. Slow loop ships month 3. (Section 8) | **Resolved** |
| **S-1** | ACA idle timeout kills SSE connections | High | 30-second keepalive comment on SSE stream. ACA `ingress.transport.connectionIdleTimeout` set to 300s in Bicep template. Documented as known constraint. (Section 5.2) | **Resolved** |
| **R-4** | Three views fail the two-audience test | Medium | Two-audience gap analysis with planned MCP tools: `campfire_attention_queue` (month 1), `campfire_cross_audit` (month 3), `campfire_notification_status` (month 3). Data models designed now. (Section 3.4) | **Resolved** |

### Should-Consider (acknowledged, with disposition)

| ID | Finding | Severity | Disposition | Rationale |
|----|---------|----------|-------------|-----------|
| **C-1** | No coordination graph / radar view | High | **Deferred to month 4.** | The attention queue is the functional equivalent for MVP. The radar view is the highest-value post-MVP feature — it gives campfire its visual identity. But validating the core loop comes first. |
| **R-1** | Flow calibration is aspirational, not mechanical | High | **Partially resolved.** | Within-session flow calibration added via attention queue threshold adjustment (section 6.2). Tier progression remains across-sessions only — within-session tier adjustment would require re-rendering the entire UI mid-session, which is disorienting. The threshold mechanism is the pragmatic within-session lever. |
| **R-5** | No agent-side alias capture | Medium | **Resolved in month 3 scope.** | MCP-side tool sequence logging designed now. Pattern detection ships month 3. (Section 3.3) |
| **C-2** | No replay / time-travel | Medium | **Deferred to month 4.** | Builds on existing audit graph data. Medium complexity. Valuable for post-incident review. |
| **A-4** | Tier progression leaks operator behavior to server | Medium | **Resolved.** | Tier computation moved to client-side (localStorage). Server ships all tier content. CSS/JS controls visibility. Server never stores tier data. (Section 3.1) |
| **A-5** | Agents can manipulate attention queue | Medium | **Resolved.** | Security events pinned to top. Per-agent future rate limit (5 unresolved per campfire). Meta-alert at 10+ attention items/hour from single agent. Security event fulfillment requires explicit operator confirmation. (Section 2.1, 6.2) |
| **A-3** | Large campfire renders detail view unusable | High | **Resolved.** | Message batching (500ms buffer), member pagination (50 limit), server-side SSE throttling (10 events/sec cap), scale indicator with summary view default. (Section 5.5) |
| **C-3** | Design is emotionally flat / no "campfire feel" | Medium | **Resolved.** | Campfire design language is defined as part of implementation beads: warm palette, glow intensity for activity, subtle state transition animation. Ships with week 1. No separate visual designer — the design language is an implementation concern, not a pre-req. |
| **C-4** | No agent behavioral fingerprinting | Low | **Deferred.** | Low priority. Useful only at scale. Lightweight to add later from existing message data. |
| **S-4** | Predicate builder is highest-risk UI component | Medium | **Mitigated.** | Debounced server calls (300ms). Fallback to vanilla JS module if latency is unacceptable. Deferred to month 3 per MVP scope — by then, operator usage patterns clarify whether a visual builder is needed or the API panel suffices. (Section 6.5) |

---

## 11. Resolved Questions

Decisions made by the operator (2026-03-25):

1. **Login flow UX.** GitHub OAuth first, magic link second. GitHub is the primary auth provider — most operators already have GitHub accounts. Magic link via email as fallback for operators without GitHub. No passphrase or QR code flow.

2. **ACA scaling model.** Scale-to-zero with KEDA. Accept occasional cold-start SSE disruption. The SSE reconnect logic (section 5) handles reconnection gracefully — the client replays from its last-seen timestamp. Cost efficiency wins over always-warm at this stage.

3. **Export side-channel.** Presigned URL download. Both the cf-ui file download AND the cf-mcp `campfire_export` tool should return a presigned Azure Blob Storage URL instead of base64 data. This addresses S4/S12 from the MCP security model — private keys no longer transit through the LLM provider's infrastructure.

4. **Notification delivery channels.** Webhooks deferred. In-app badge only for month 1. Webhook delivery is a month 3+ concern once the notification model is validated.

5. **Visual design.** Follow the campfire design language — design it as part of implementation, not as a separate engagement. The UI ships with a coherent campfire-native aesthetic from week 1. No separate visual designer; the design language is defined as part of the implementation beads.

6. **Search.** Deferred entirely. The AIETF needs to figure out what tools are useful in this network before we commit to a search implementation. Search is not in any milestone until the network topology and tool discovery patterns are understood.
