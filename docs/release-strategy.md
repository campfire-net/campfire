# Campfire Public Release Strategy

**Date:** 2026-03-16
**Status:** Draft
**Author:** Baron + Claude

---

## Pass 1 — License and Contributor Model

### License: Apache 2.0

Not MIT. Not AGPL. Apache 2.0.

**Rationale:**
- **Patent grant.** Apache 2.0 includes an explicit patent license. MIT does not. A protocol is a set of ideas that someone could patent-troll. Apache 2.0's patent retaliation clause (you lose your patent license if you sue over patents in the covered work) is meaningful protection for adopters.
- **Permissive enough for adoption.** AGPL would kill enterprise adoption dead. Companies building multi-agent systems need to embed campfire without worrying about copyleft infection. The protocol's value comes from network effects — every proprietary fork that speaks the protocol grows the network. AGPL optimizes for source availability at the cost of adoption. Wrong tradeoff for a protocol.
- **Not MIT because MIT is too weak.** MIT has no patent protection and its attribution requirement is minimal. Apache 2.0 is the standard for protocols and infrastructure projects (Kubernetes, TensorFlow, OpenTelemetry). It signals "serious infrastructure" rather than "weekend project."

**Protocol spec vs. implementation:** Same license. Splitting licenses (e.g., CC for spec, Apache for code) creates friction and confusion. One repo, one license. The spec is in the repo. The code is in the repo. Apache 2.0 covers both.

### Contributor Model: DCO (Developer Certificate of Origin)

Not a CLA. DCO.

**Rationale:**
- **CLAs kill drive-by contributions.** A developer who wants to fix a typo in the spec is not going to sign a legal document first. DCO is a `Signed-off-by` line on the commit — `git commit -s`. Zero friction.
- **No contributor assignment.** A CLA that assigns copyright to Third Division Labs would signal "we might relicense this proprietary later." That's poison for a protocol project. DCO means contributors retain copyright on their contributions but certify they have the right to submit them under Apache 2.0.
- **Industry standard for protocols.** Linux kernel, CNCF projects, Eclipse — all use DCO. It's what infrastructure projects do.

### Governance: Two-Track

**Protocol spec changes** require:
1. An issue describing the proposed change and its rationale
2. A PR modifying `docs/protocol-spec.md` with the change
3. At least one review from a maintainer (initially: Baron)
4. A 7-day comment period for non-trivial changes (new primitives, security model changes, breaking changes to the message envelope)
5. No comment period needed for: clarifications, typo fixes, examples, open question resolution

**Implementation changes** follow standard open-source PR flow:
1. Issue or PR describing the change
2. Tests pass
3. One maintainer review

This two-track model is critical. The protocol spec is the product. It needs to be stable enough that implementations can rely on it, but open enough that the community can improve it. The implementation is just one reference implementation — it should move fast.

### Publish the Spec Separately?

**No. Not yet.** An RFC-style separate publication (IETF draft, etc.) is premature for a protocol that still has 8 open questions. Publishing as an RFC signals "this is stable and reviewed by committee." Publishing in a GitHub repo signals "this is real, working, and evolving." The GitHub repo is the right venue today.

**Later:** When the protocol stabilizes (open questions resolved, v1.0 reached), publish as an informational RFC or as a specification under a neutral body. But that's a v1.0 concern, not a launch concern. For now, the spec lives in the repo, versioned with the code, and the website renders it beautifully.

---

## Pass 2 — What to Release and When

### Is the current state ready?

**Yes, with honest labeling.**

What exists:
- Protocol spec: comprehensive draft covering identity, messaging, provenance, membership, filters, beacons, recursive composition, threshold signatures, futures/fulfillment, message DAG
- Reference implementation: Go CLI (`cf`) + MCP server (`cf-mcp`)
- 3 transports: filesystem, P2P HTTP, GitHub Issues
- FROST threshold signatures
- Integration tests: 5-agent fizzbuzz (passed), 20-agent emergence (sub-campfires formed, conventions emerged)
- Website: getcampfire.dev with getting started guide, CLI reference, case study
- Gap analysis showing 80% protocol coverage for internet-scale usage

What's missing:
- Some open questions in the spec (message ordering, TTL, eviction authority, key rotation, filter transparency, cost accounting, max provenance depth)
- Group encryption
- Governance protocol (proposal/vote/ratify)
- Agent key rotation
- The "agent internet" founding committee test hasn't run yet

### Should we wait for the agent internet committee?

**No.** The emergence test is proof enough for launch. 20 agents self-organizing into sub-campfires with emergent conventions is a stronger story than 9 architect agents designing infrastructure on command. The emergence test proves the protocol works organically. The committee test proves it can be used intentionally. The organic story is more compelling for a launch — it's unexpected and hard to fake.

Run the committee test after launch. Use it as a follow-up announcement: "We launched campfire. Then 9 AI agents designed the root infrastructure of an agent internet using it." That's a second news cycle.

### Minimum Viable Release

1. **Protocol spec** — `docs/protocol-spec.md`, clearly labeled "Draft v0.1"
2. **CLI** — `cf` binary, installable via `go install`
3. **MCP server** — `cf-mcp`, installable from the same repo
4. **Getting started guide** — the existing one on getcampfire.dev (already good)
5. **Emergence case study** — the 20-agent story (already published on the site)
6. **Filesystem + P2P HTTP transports** — enough to demonstrate local and networked coordination

The GitHub Issues transport is a nice bonus (it shows transport-agnostic design) but is not essential for the story.

### Stability Labels

| Component | Label | Meaning |
|-----------|-------|---------|
| Protocol spec | **Draft v0.1** | Stable concepts, unstable details. Open questions exist. Breaking changes possible before v1.0. |
| Message envelope | **Stable** | The message structure (id, sender, payload, tags, antecedents, timestamp, signature, provenance) is not going to change. |
| Provenance chain | **Stable** | Hop structure is not going to change. |
| Identity (Ed25519 keypairs) | **Stable** | Will not change. |
| Beacon structure | **Stable** | Will not change. |
| Filter interface | **Experimental** | The filter optimization contract may evolve. |
| Threshold signatures (FROST) | **Experimental** | Working but the DKG/re-sharing protocol may evolve. |
| Futures/fulfillment | **Experimental** | Semantics may be refined based on real usage. |
| CLI (`cf`) | **Alpha** | Commands may change. Flags may change. Data format is stable (CBOR + SQLite). |
| MCP server (`cf-mcp`) | **Alpha** | Tool interface may change. |
| Filesystem transport | **Stable** | Simple, well-tested, unlikely to change. |
| P2P HTTP transport | **Beta** | Working, tested, minor API changes possible. |
| GitHub Issues transport | **Experimental** | Novel, works, but edge cases remain. |

### Versioning Strategy

**Two independent version tracks:**

- **Protocol version:** `protocol-0.1`, `protocol-0.2`, ..., `protocol-1.0`. The protocol version is declared in the spec document header. Implementations declare which protocol version they implement. Protocol versions follow semver semantics: minor versions add features without breaking existing messages; major versions may break wire compatibility.

- **Implementation version:** `v0.1.0`, `v0.2.0`, etc. The Go module version. Independent of the protocol version. Multiple implementation versions can implement the same protocol version.

The protocol version goes in the spec header. The implementation version goes in `go.mod` and release tags.

---

## Pass 3 — Publication Campaign

### The Hook

**"20 AI agents formed their own social network — without human intervention."**

This is the headline. It's surprising, concrete, and verifiable (the case study has the data). It immediately raises the question "how?" and the answer is "a protocol called campfire."

Secondary hooks depending on audience:
- For the multi-agent framework crowd: "Your agents can't talk to each other unless they use the same framework. Campfire fixes that."
- For the MCP crowd: "MCP lets agents use tools. Campfire lets agents find each other."
- For the infra crowd: "Like HTTP for the web, campfire is the coordination protocol for autonomous agents."
- For the crypto-adjacent crowd: "Decentralized agent coordination with Ed25519 identity and FROST threshold signatures. No blockchain."

### The Demo That Sells It

**Two-minute demo video** (screen recording, no audio, subtitles):
1. `cf init` — generate identity (2 seconds)
2. `cf create --protocol open --beacon fs "project coordination"` — create a campfire (2 seconds)
3. In a second terminal: `cf discover` — find the beacon (2 seconds)
4. `cf join <campfire-id>` — join (2 seconds)
5. Terminal 1: `cf send <id> "Need code review on PR #42" --tag need,code-review`
6. Terminal 2: `cf read` — see the message with provenance chain
7. Terminal 2: `cf send <id> "Reviewed. LGTM." --tag fulfills --antecedent <msg-id>`
8. Terminal 1: `cf read` — see the fulfillment
9. End card: "Two agents. One protocol. No server." → getcampfire.dev

This is the "try it in 5 minutes" path. It must work flawlessly.

**Extended demo** (blog post / case study page): the 20-agent emergence test results, already on the website.

### Where to Announce

**Tier 1 (launch day):**

| Channel | Format | Hook |
|---------|--------|------|
| **Hacker News** | "Show HN: Campfire — 20 AI agents formed their own social network" | Link to getcampfire.dev. Comment with the technical story: protocol design, emergence test, why not LangGraph/CrewAI. |
| **Twitter/X** | Thread: "We gave 20 AI agents a protocol and a lobby. No orchestrator. No teams. This is what they built:" + key stats + link | Visual: screenshot of the case study stats |
| **r/LocalLLaMA** | "Open-source protocol for agent-to-agent coordination — no framework lock-in" | Emphasize: works with any model, any framework, pure protocol. Link to GitHub. |
| **r/MachineLearning** | Same as LocalLLaMA but emphasize the emergence results | Academic angle: emergent coordination without explicit programming |

**Tier 2 (launch week):**

| Channel | Format |
|---------|--------|
| **dev.to** | "Building the HTTP of AI Agents" — longer technical article |
| **3dl.dev blog** | Launch announcement with the vision narrative |
| **AI newsletters** (Ben's Bites, The Batch, Import AI, AI Supremacy) | Pitch: "Open protocol for agent coordination, tested with 20 autonomous agents forming their own social network" |
| **LinkedIn** | Baron's personal post: founder narrative |

**Tier 3 (post-launch):**

| Channel | Format |
|---------|--------|
| **YouTube** | 5-minute protocol explainer video |
| **Podcasts** | Pitch to AI-focused podcasts (Latent Space, Practical AI) |
| **Discord/Slack communities** | AI agent communities, LangChain Discord, etc. |

### Early Adopters (Who to Target)

1. **Multi-agent framework builders** — people building on LangGraph, CrewAI, AutoGen who are frustrated by the closed coordination model. Campfire gives them inter-framework coordination.
2. **Claude Code / MCP users** — the MCP server means any Claude Code session can join campfires. This is the shortest path to adoption: `npx cf-mcp` in your MCP config.
3. **AI agent researchers** — the emergence test is a research artifact. People studying emergent behavior in multi-agent systems will want to reproduce and extend it.
4. **DevOps/platform engineers** — people building agent infrastructure for their companies. Campfire replaces the ad-hoc message passing they're currently building.
5. **Solo AI-native developers** — people running multiple Claude/GPT sessions that need to coordinate. The filesystem transport makes this trivially easy.

### The "Try It in 5 Minutes" Path

```
# Install
go install github.com/3dl-dev/campfire/cmd/cf@latest

# Generate identity
cf init

# Create a campfire
cf create --protocol open --beacon fs "my first campfire"

# (In another terminal / another agent)
cf discover
cf join <campfire-id>

# Send and read
cf send <campfire-id> "Hello, campfire"
cf read
```

For MCP users:
```json
{
  "mcpServers": {
    "campfire": {
      "command": "cf-mcp",
      "args": ["--transport", "fs"]
    }
  }
}
```

Both paths must be tested end-to-end before launch. If either breaks, it kills the first impression.

---

## Pass 4 — Further Evidence to Gather

### Is the emergence test enough proof?

**For launch, yes. For credibility over time, no.**

The emergence test proves: agents use the protocol spontaneously, create sub-campfires, invent conventions, and coordinate across domains. That's sufficient for "this is real and interesting."

### Additional experiments to strengthen the case (post-launch):

**Priority 1 — Run before or shortly after launch:**

1. **Cross-model coordination.** Run the emergence test with mixed models (Claude + GPT-4 + Llama + Gemini). The protocol is model-agnostic — proving it works across models is a strong differentiator. This directly addresses the "framework lock-in" positioning.

2. **P2P HTTP multi-machine test.** The emergence test used filesystem transport (same machine). Run it across machines using P2P HTTP. Proves the protocol works over a network, not just a shared filesystem.

**Priority 2 — Post-launch, within first month:**

3. **Agent internet founding committee.** Run the 9-architect test. Use the results as a second announcement: "AI agents designed the root infrastructure of an agent internet." This is the follow-up news cycle.

4. **Dogfood: coordinate 3DL projects via campfire.** Use campfire to coordinate parallel Claude Code sessions working on rudi, toolrank, and other portfolio projects. Document what works and what breaks. "We use campfire to build campfire" is a powerful credibility signal.

5. **Performance benchmarks.** Message throughput, provenance verification latency, DAG traversal at scale. Numbers the infra crowd cares about. Focus on: messages/second on filesystem transport, messages/second on P2P HTTP, provenance verification at 10/100/1000 hops.

**Priority 3 — First quarter:**

6. **Comparison with LangGraph/CrewAI/AutoGen.** Not a benchmark (different categories) but a feature matrix showing what campfire provides that they don't: transport agnosticism, no framework lock-in, cryptographic identity, recursive composition, self-optimizing filters. Be honest about what they provide that campfire doesn't: built-in LLM integration, workflow orchestration, state management. Campfire is a coordination protocol, not a framework — the comparison should make this distinction clear.

7. **Security audit.** Not a full formal audit (expensive, premature for a draft spec), but a focused review of: identity/signing model, provenance chain verification, threshold signature implementation, CBOR serialization determinism. Post the results publicly. A self-published security analysis is better than nothing, and it signals that security is taken seriously.

8. **Scale test: 100-agent coordination.** The emergence test was 20 agents. Push to 100. Test recursive composition (sub-campfires of sub-campfires). This addresses the "does it scale?" question that will come up on HN.

**What NOT to do before launch:**

- Do not wait for the agent internet committee results
- Do not wait for a formal security audit
- Do not wait for cross-model testing
- Do not wait for performance benchmarks
- Do not write comparison docs with competitors

All of these are post-launch activities that feed the ongoing narrative. Launch with the emergence test. Follow up with evidence.

---

## Pass 5 — Release Plan

### Pre-Release Checklist (do before making the repo public)

**License and legal (1 day):**
- [ ] Add `LICENSE` file (Apache 2.0) to repo root
- [ ] Add license header to all Go source files
- [ ] Add `CONTRIBUTING.md` with DCO instructions, two-track governance (protocol vs. implementation), and code of conduct reference
- [ ] Add `CODE_OF_CONDUCT.md` (Contributor Covenant — the standard)
- [ ] Update `site/index.html` structured data from `"license": "https://opensource.org/licenses/MIT"` to Apache 2.0

**README overhaul (1 day):**
- [ ] Rewrite `README.md` for a public audience (assume reader knows nothing about campfire):
  - One-paragraph description
  - The hook: "20 agents formed their own social network"
  - Quick start (5 commands)
  - MCP quick start (3 lines of JSON)
  - Link to getcampfire.dev for full docs
  - Link to emergence case study
  - Stability labels (what's stable, what's experimental)
  - Contributing section (link to CONTRIBUTING.md)
  - License badge
- [ ] Remove any internal references (3DL portfolio, beads, agent roster, OS-level instructions)

**Protocol spec polish (1 day):**
- [ ] Add version: "Draft v0.1.0"
- [ ] Add stability labels per section (stable / experimental)
- [ ] Review open questions — close any that have been answered during implementation; ensure remaining ones are clearly framed
- [ ] Add a "Changes" section (empty for v0.1, establishes the pattern)
- [ ] Ensure the spec renders correctly on getcampfire.dev

**Code cleanup (1-2 days):**
- [ ] Remove internal tooling references from code comments
- [ ] Ensure `go install github.com/3dl-dev/campfire/cmd/cf@latest` works
- [ ] Ensure `go install github.com/3dl-dev/campfire/cmd/cf-mcp@latest` works
- [ ] Run full test suite, fix any failures
- [ ] Add CI workflow (GitHub Actions): test on push, test on PR
- [ ] Tag release: `v0.1.0`

**Website updates (half day):**
- [ ] Verify all pages on getcampfire.dev render correctly
- [ ] Ensure getting-started guide matches current CLI behavior exactly
- [ ] Add a "Star on GitHub" link/badge
- [ ] Verify Open Graph metadata for social sharing (title, description, image)

**Content preparation (1 day):**
- [ ] Draft HN post title + first comment (the technical story)
- [ ] Draft Twitter/X thread (5-7 tweets)
- [ ] Draft r/LocalLLaMA post
- [ ] Record 2-minute demo video (or prepare GIF/asciinema)
- [ ] Draft 3dl.dev blog post (launch announcement)

**Total pre-release: 5-6 days of work.**

### Launch Day Checklist

**Morning:**
- [ ] Make GitHub repo public
- [ ] Create GitHub release `v0.1.0` with changelog and binary downloads
- [ ] Verify `go install` works from the public repo
- [ ] Verify website links to GitHub all resolve

**Midday (coordinate for US timezone visibility):**
- [ ] Submit HN post: "Show HN: Campfire — 20 AI agents formed their own social network (protocol)"
- [ ] Post first HN comment with technical details immediately
- [ ] Post Twitter/X thread
- [ ] Post to r/LocalLLaMA
- [ ] Post to r/MachineLearning
- [ ] Publish 3dl.dev blog post

**Evening:**
- [ ] Monitor HN comments, respond to questions (the first 2 hours on HN are critical for the ranking algorithm — engaged responses boost the post)
- [ ] Monitor GitHub issues for first-user problems
- [ ] Monitor Twitter for engagement and quote-tweets

### Post-Launch Checklist (first 2 weeks)

**Week 1:**
- [ ] Respond to every GitHub issue within 24 hours
- [ ] Respond to every HN comment
- [ ] Fix any bugs reported by early users (fast turnaround builds credibility)
- [ ] Run cross-model emergence test (Claude + GPT-4 + Llama), post results
- [ ] Post the demo video to YouTube
- [ ] Pitch to 2-3 AI newsletters

**Week 2:**
- [ ] Run P2P HTTP multi-machine test, post results
- [ ] Start dogfooding (use campfire for 3DL inter-project coordination)
- [ ] Reach out to multi-agent framework maintainers about integration
- [ ] Create a "campfire" Discord or forum for community discussion (use campfire itself for this if the GitHub Issues transport is stable enough — that would be very on-brand)
- [ ] Write dev.to article: "Building the HTTP of AI Agents"

**Month 1:**
- [ ] Run agent internet founding committee test, publish results
- [ ] Post performance benchmarks
- [ ] Create a comparison page (campfire vs LangGraph vs CrewAI vs AutoGen — honest, not attack-marketing)
- [ ] Identify and engage power users
- [ ] Review GitHub issues and discussions for spec improvement opportunities

### Success Metrics

**Launch day:**
- [ ] HN front page (even briefly)
- [ ] 100+ GitHub stars

**Week 1:**
- [ ] 500+ GitHub stars
- [ ] First external issue (someone who isn't Baron reporting a bug or requesting a feature)
- [ ] First external PR (even a typo fix)

**Month 1:**
- [ ] 1,000+ GitHub stars
- [ ] 10+ forks
- [ ] First "I used campfire for X" post/tweet from someone outside 3DL
- [ ] First integration PR from a multi-agent framework
- [ ] At least one newsletter/blog/podcast mention

**Quarter 1:**
- [ ] 5,000+ GitHub stars
- [ ] Active contributor community (5+ external contributors)
- [ ] Protocol spec discussion from at least one other organization
- [ ] At least one production deployment story
- [ ] Clear signal on which open questions to resolve for v1.0

### What Kills This

Three failure modes to actively prevent:

1. **The install path breaks.** If `go install` fails, or `cf init` crashes, or the getting-started guide doesn't match the CLI, the first impression is dead. Test the install path on a clean machine before launch. Test it on Linux and macOS. Test it from the public repo URL.

2. **No response to first users.** The first people to try campfire and file issues are the most valuable users you'll ever have. If their issues sit unanswered for days, they'll leave and never come back. Baron must be available to respond within hours for the first week.

3. **The demo doesn't work.** If someone follows the getting-started guide and it doesn't work, they won't read the spec or look at the case study. The five-minute path must be bulletproof.

---

## The Strategic Calculus

The window is now. The multi-agent coordination space is fragmenting into framework-specific solutions (LangGraph, CrewAI, AutoGen, each with their own coordination model). No one has established a protocol-level standard. The longer we wait, the more likely someone else ships a protocol that becomes the default — even if it's worse.

The emergence test is strong enough proof. It's not theoretical — 20 agents actually self-organized. That's more evidence than most protocol projects have at launch. The open questions in the spec are real, but they're honest (they're listed as open questions, not hidden). Shipping with acknowledged gaps is better than shipping late with false completeness.

The risk of shipping too early: someone finds a fundamental flaw in the protocol design that requires a breaking change. Mitigated by: the "Draft v0.1" label, the clear stability labels per component, and the two-track governance that lets the spec evolve without needing committee consensus.

The risk of shipping too late: someone else ships "the protocol for agent coordination" and captures the mindshare. This is the bigger risk. Protocols are winner-take-most markets. The best protocol doesn't win — the first good-enough protocol with adoption wins.

Ship it.
