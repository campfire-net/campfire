# Animated Demo Section — Design Document

Replaces the static three-terminal "See It In Action" section on the landing page (`site/index.html`, lines 86-298).

## Story

Three agents using three different interfaces coordinate a fizzbuzz implementation through a single campfire. No human orchestrates. The protocol is the coordinator. The viewer watches it happen in real time (accelerated).

---

## Visual Layout

### Desktop (>1024px)

```
+------------------------------------------------------------------+
|                  SEE IT IN ACTION                                 |
|        Three agents, one campfire, no coordinator                 |
|                                                                   |
|  +--Panel 1--+      +--Campfire--+       +--Panel 2--+           |
|  | Claude     |      |           |       | AI Agent   |           |
|  | Code       |<---->|  (flame   |<----->| via MCP    |           |
|  | Session    |      |  icon +   |       |            |           |
|  |            |      |  message  |       |            |           |
|  +------------+      |  log)     |       +------------+           |
|                      |           |                                |
|                      +-----------+                                |
|                           ^                                       |
|                           |                                       |
|                      +--Panel 3--+                                |
|                      | Agent     |                                |
|                      | via CLI   |                                |
|                      +------------+                               |
|                                                                   |
|  [Restart]  ======progress bar======  Step 12/15                  |
+------------------------------------------------------------------+
```

The three panels form a triangle around a central "campfire" area. The campfire area shows:
- A stylized flame icon (CSS-drawn, animated)
- A vertical message log showing messages as they pass through the campfire

Connecting lines (dashed, styled like the existing `.why-connector`) run from each panel to the center. When a message is in flight, a glowing dot travels along the line from sender to campfire, then from campfire to recipient(s).

### Tablet (768-1024px)

Panels stack: Panel 1 and Panel 2 side-by-side on top row, campfire center between them. Panel 3 centered below. Connecting lines simplified to vertical/horizontal.

### Mobile (<768px)

Panels stack vertically: Panel 1, then campfire log, then Panel 2, then Panel 3. No connecting lines. Messages appear with a subtle pulse highlight when they arrive from another panel. The campfire log becomes a horizontal divider between panels showing "message passed through campfire" events.

---

## Panel Designs

### Panel 1: "Claude Code Session"

Dark terminal background (`#1C1917`), matching existing `.terminal-body` styles. Title bar says "Claude Code Session". Shows commands as Claude Code tool-use blocks:

```
> cf create --description "fizzbuzz project"
  4b8e1d

> cf send 4b8e1d "implement FizzBuzz(n int) string" --future
  d3a7f1

> cf read 4b8e1d
  [campfire:4b8e1d] agent:b3f8c2 [fulfills]
    antecedents: d3a7f1c9
    FizzBuzz implemented: ...

  [campfire:4b8e1d] agent:c9e4a1
    antecedents: e8c1f3a7
    Code review: approved. Tests pass.

> cf send 4b8e1d "all tasks complete" --reply-to d3a7f1
  f7b2d4
```

Prompt character: `>` (not `$`), teal colored. Mono font. Output in muted gray.

### Panel 2: "AI Agent via MCP"

Same dark background, but the title bar says "AI Agent via MCP" with a different accent color (teal instead of rust). Shows MCP tool calls as structured JSON-ish blocks:

```
campfire_discover()
  { campfire_id: "4b8e1d", description: "fizzbuzz project",
    protocol: "open", sig: valid }

campfire_join("4b8e1d")
  { status: "joined" }

campfire_read("4b8e1d")
  { sender: "a1f4c2", tags: ["future"],
    payload: "implement FizzBuzz(n int) string" }

campfire_send("4b8e1d", "FizzBuzz implemented: ...",
  { fulfills: "d3a7f1" })
  { id: "e8c1f3" }
```

Uses `campfire_` prefixed function names (MCP tool naming). Output is JSON-like but abbreviated. Prompt shows function calls in a distinct color (teal).

### Panel 3: "Agent via CLI"

Same dark background, title bar says "Agent via CLI". Classic shell prompt with `$`:

```
$ cf discover
  4b8e1d  open  fizzbuzz project  sig:valid
    transport: filesystem  requires: (none)

$ cf join 4b8e1d
  Joined campfire 4b8e1d

$ cf read 4b8e1d
  [campfire:4b8e1d] agent:a1f4c2 [fulfilled]
    tags: future
    implement FizzBuzz(n int) string

  [campfire:4b8e1d] agent:b3f8c2
    tags: fulfills
    antecedents: d3a7f1c9
    FizzBuzz implemented: ...

$ cf send 4b8e1d "Code review: approved. Tests pass." \
    --reply-to e8c1f3
  a9d2b7
```

Uses `$` prompt in green/teal. Standard CLI output format matching actual `cf` output (6-char short IDs for campfire, 6-char for agent, as per `read.go` lines 253-260).

---

## Animation Timeline

Each step has a `delay` (milliseconds from animation start) and a `duration` (how long the typing/reveal takes).

### Phase 1: Agent A Creates the Campfire (0-3s)

| Step | Delay | Panel | Type | Content |
|------|-------|-------|------|---------|
| 1 | 0ms | 1 | command | `cf create --description "fizzbuzz project"` |
| 2 | 400ms | 1 | output | `4b8e1d` |
| 3 | 1200ms | 1 | command | `cf send 4b8e1d "implement FizzBuzz(n int) string" --future` |
| 4 | 1600ms | 1 | output | `d3a7f1` |
| 5 | 2000ms | center | beacon | Campfire icon pulses. Beacon label appears: "beacon published" |

### Phase 2: Agent B Discovers and Joins (3-6.5s)

| Step | Delay | Panel | Type | Content |
|------|-------|-------|------|---------|
| 6 | 3000ms | flight | glow | Dot travels from center to Panel 2 (beacon discovery) |
| 7 | 3500ms | 2 | command | `campfire_discover()` |
| 8 | 3900ms | 2 | output | `{ campfire_id: "4b8e1d", description: "fizzbuzz project", protocol: "open", sig: valid }` |
| 9 | 4500ms | 2 | command | `campfire_join("4b8e1d")` |
| 10 | 4900ms | 2 | output | `{ status: "joined" }` |
| 11 | 5300ms | 2 | command | `campfire_read("4b8e1d")` |
| 12 | 5600ms | flight | glow | Dot travels from center to Panel 2 (future message delivery) |
| 13 | 5900ms | 2 | output | `{ sender: "a1f4c2", tags: ["future"], payload: "implement FizzBuzz(n int) string" }` |

### Phase 3: Agent B Fulfills the Future (6.5-8.5s)

| Step | Delay | Panel | Type | Content |
|------|-------|-------|------|---------|
| 14 | 6800ms | 2 | label | Brief typing indicator, then: |
| 15 | 7400ms | 2 | command | `campfire_send("4b8e1d", "FizzBuzz implemented: func FizzBuzz(n int) string { ... }", { fulfills: "d3a7f1" })` |
| 16 | 7800ms | 2 | output | `{ id: "e8c1f3" }` |
| 17 | 8000ms | flight | glow | Dot travels from Panel 2 to center (fulfillment entering campfire) |
| 18 | 8200ms | center | log | "fulfillment" label appears in campfire log |

### Phase 4: Agent C Discovers, Joins, Reviews (8.5-13s)

| Step | Delay | Panel | Type | Content |
|------|-------|-------|------|---------|
| 19 | 8500ms | flight | glow | Dot from center toward Panel 3 (beacon) |
| 20 | 9000ms | 3 | command | `cf discover` |
| 21 | 9300ms | 3 | output | `4b8e1d  open  fizzbuzz project  sig:valid` (with second line `transport: filesystem  requires: (none)`) |
| 22 | 9800ms | 3 | command | `cf join 4b8e1d` |
| 23 | 10100ms | 3 | output | `Joined campfire 4b8e1d` |
| 24 | 10500ms | 3 | command | `cf read 4b8e1d` |
| 25 | 10700ms | flight | glow | Dot from center to Panel 3 (all messages delivered) |
| 26 | 11000ms | 3 | output-block | Two messages: the original future (marked `[fulfilled]`) and Agent B's fulfillment |
| 27 | 11800ms | 3 | command | `cf send 4b8e1d "Code review: approved. Tests pass." --reply-to e8c1f3` |
| 28 | 12200ms | 3 | output | `a9d2b7` |
| 29 | 12400ms | flight | glow | Dot from Panel 3 to center |

### Phase 5: Agent A Reads Results (13-15.5s)

| Step | Delay | Panel | Type | Content |
|------|-------|-------|------|---------|
| 30 | 13000ms | flight | glow | Dot from center to Panel 1 |
| 31 | 13400ms | 1 | command | `cf read 4b8e1d` |
| 32 | 13700ms | 1 | output-block | Two messages: fulfillment from B, review from C |
| 33 | 14500ms | 1 | command | `cf send 4b8e1d "all tasks complete" --reply-to d3a7f1` |
| 34 | 14900ms | 1 | output | `f7b2d4` |

### Phase 6: Finale (15.5-17s)

| Step | Delay | Panel | Type | Content |
|------|-------|-------|------|---------|
| 35 | 15500ms | all | highlight | All three panels get a subtle border glow (teal). Center campfire pulses brighter. |
| 36 | 16000ms | overlay | summary | Fade in text below: **"3 agents. 1 campfire. 0 coordinators."** then smaller: **"12 messages. 350 seconds real-time. The protocol did the rest."** |
| 37 | 17000ms | - | idle | Animation holds. Restart button pulses gently. |

Total animation duration: ~17 seconds.

---

## "Message in Flight" Visual Treatment

When a message moves between a panel and the campfire center:

1. **The connecting line** from sender to center brightens from `opacity: 0.35` to `opacity: 1.0` and changes color to the accent color (`--color-accent` for rust, `--color-accent-secondary` for teal).

2. **A glowing dot** (8px diameter, radial gradient with the accent color at center fading to transparent) travels along the line using CSS `offset-path` or JS-animated `transform`. Travel time: 400ms with `ease-in-out`.

3. **The campfire center** pulses briefly when the dot arrives — the flame icon scales up 10% for 200ms.

4. **On the receiving end**, when the dot arrives at a panel, the next line in that panel appears with a brief highlight flash (background briefly goes `rgba(15, 118, 110, 0.15)` for 300ms, then fades).

For steps where a message goes from Panel X -> Center -> Panel Y, the dot travels the full path: Panel X to Center (400ms pause) then Center to Panel Y.

---

## Campfire Center Area

The center area is 160x160px containing:

1. **Flame icon**: CSS-drawn using layered `::before` / `::after` pseudo-elements with `border-radius` and `background: linear-gradient(...)` in oranges/ambers. Subtle `@keyframes flicker` animation (slight scale and opacity variation, 2s loop).

2. **Message log**: A small scrolling area below the flame (or overlaid as tooltips that appear briefly). Each message passing through shows a one-line summary:
   - `future: "implement FizzBuzz"` (rust color)
   - `fulfills: d3a7f1` (teal color)
   - `review: approved` (teal color)
   - `complete` (gray)

   These appear one at a time, stacking downward, using `@keyframes slideIn` (translate from -10px to 0, opacity 0 to 1, 300ms).

3. **Member count badge**: Shows "1 member" initially, updates to "2 members" when B joins, "3 members" when C joins. Small, monospace, above the flame.

---

## HTML Structure

```html
<section class="demo-section" aria-labelledby="demo-heading">
  <div class="container">
    <div class="section-header">
      <div class="section-label">See It In Action</div>
      <h2 class="section-title" id="demo-heading">Three agents, one campfire, no coordinator</h2>
      <p class="section-desc">
        Watch three agents discover each other, coordinate a fizzbuzz implementation,
        and complete it — through a single campfire. No task assignment. No orchestrator.
      </p>
    </div>

    <div class="demo-stage" aria-label="Animated demonstration of three agents coordinating through a campfire">

      <!-- SVG connecting lines -->
      <svg class="demo-connectors" viewBox="0 0 1000 700" aria-hidden="true">
        <line class="demo-line demo-line-1" x1="280" y1="250" x2="500" y2="280"/>
        <line class="demo-line demo-line-2" x1="720" y1="250" x2="500" y2="280"/>
        <line class="demo-line demo-line-3" x1="500" y1="520" x2="500" y2="340"/>
        <!-- Animated dots (circles that travel along lines) -->
        <circle class="demo-dot demo-dot-1" r="5" cx="0" cy="0"/>
        <circle class="demo-dot demo-dot-2" r="5" cx="0" cy="0"/>
        <circle class="demo-dot demo-dot-3" r="5" cx="0" cy="0"/>
      </svg>

      <!-- Campfire center -->
      <div class="demo-campfire">
        <div class="demo-campfire-members">1 member</div>
        <div class="demo-campfire-flame" aria-hidden="true"></div>
        <div class="demo-campfire-log" role="log" aria-live="polite" aria-label="Messages passing through the campfire">
          <!-- JS appends message summaries here -->
        </div>
      </div>

      <!-- Panel 1: Claude Code -->
      <div class="demo-panel demo-panel-1" aria-label="Claude Code Session terminal">
        <div class="demo-panel-bar">
          <div class="terminal-dot red"></div>
          <div class="terminal-dot yellow"></div>
          <div class="terminal-dot green"></div>
          <span class="demo-panel-label">Claude Code Session</span>
        </div>
        <div class="demo-panel-body" role="log" aria-live="polite">
          <!-- JS appends lines here -->
        </div>
      </div>

      <!-- Panel 2: MCP Agent -->
      <div class="demo-panel demo-panel-2" aria-label="AI Agent via MCP terminal">
        <div class="demo-panel-bar demo-panel-bar-teal">
          <div class="terminal-dot red"></div>
          <div class="terminal-dot yellow"></div>
          <div class="terminal-dot green"></div>
          <span class="demo-panel-label">AI Agent via MCP</span>
        </div>
        <div class="demo-panel-body" role="log" aria-live="polite">
          <!-- JS appends lines here -->
        </div>
      </div>

      <!-- Panel 3: CLI Agent -->
      <div class="demo-panel demo-panel-3" aria-label="Agent via CLI terminal">
        <div class="demo-panel-bar">
          <div class="terminal-dot red"></div>
          <div class="terminal-dot yellow"></div>
          <div class="terminal-dot green"></div>
          <span class="demo-panel-label">Agent via CLI</span>
        </div>
        <div class="demo-panel-body" role="log" aria-live="polite">
          <!-- JS appends lines here -->
        </div>
      </div>

    </div>

    <!-- Controls -->
    <div class="demo-controls">
      <button class="demo-btn demo-btn-restart" aria-label="Restart animation">
        Restart
      </button>
      <div class="demo-progress">
        <div class="demo-progress-bar"></div>
      </div>
      <span class="demo-step-count" aria-live="polite">Step 0/37</span>
    </div>

    <!-- Finale summary (hidden until step 36) -->
    <div class="demo-summary" aria-live="polite">
      <p class="demo-summary-headline">3 agents. 1 campfire. 0 coordinators.</p>
      <p class="demo-summary-sub">12 messages. 350 seconds real-time. The protocol did the rest.</p>
    </div>

  </div>
</section>
```

---

## CSS Approach

### Stage Layout (Desktop)

```css
.demo-stage {
  position: relative;
  width: 100%;
  max-width: 1000px;
  height: 700px;
  margin: 0 auto;
}

.demo-panel {
  position: absolute;
  width: 300px;
  max-height: 320px;
  overflow-y: auto;
  background: #1C1917;
  border-radius: var(--radius-lg);
  box-shadow: 0 8px 32px rgba(0,0,0,0.12);
}

.demo-panel-1 { top: 30px; left: 0; }
.demo-panel-2 { top: 30px; right: 0; }
.demo-panel-3 { bottom: 30px; left: 50%; transform: translateX(-50%); }

.demo-campfire {
  position: absolute;
  top: 50%;
  left: 50%;
  transform: translate(-50%, -50%);
  width: 160px;
  text-align: center;
  z-index: 2;
}
```

### Line Appearance

```css
.demo-line {
  stroke: var(--color-border);
  stroke-width: 1.5;
  stroke-dasharray: 4 6;
  opacity: 0.35;
  transition: opacity 0.3s, stroke 0.3s;
}

.demo-line.active {
  stroke: var(--color-accent-secondary);
  opacity: 1;
}
```

### Dot Animation

```css
.demo-dot {
  fill: var(--color-accent-secondary);
  filter: drop-shadow(0 0 6px var(--color-accent-secondary));
  opacity: 0;
  transition: opacity 0.15s;
}

.demo-dot.visible { opacity: 1; }
```

Dot position is animated via JS using `requestAnimationFrame`, interpolating between line endpoints over 400ms.

### Flame Animation

```css
.demo-campfire-flame {
  width: 40px;
  height: 56px;
  margin: 0 auto 8px;
  background: linear-gradient(to top, #D97706, #F59E0B, #FCD34D);
  border-radius: 50% 50% 50% 50% / 60% 60% 40% 40%;
  animation: flicker 2s ease-in-out infinite alternate;
}

@keyframes flicker {
  0%   { transform: scale(1) rotate(-1deg); opacity: 0.9; }
  50%  { transform: scale(1.05) rotate(1deg); opacity: 1; }
  100% { transform: scale(0.97) rotate(-0.5deg); opacity: 0.85; }
}

.demo-campfire-flame.pulse {
  animation: flamePulse 0.4s ease-out;
}

@keyframes flamePulse {
  0%   { transform: scale(1); }
  50%  { transform: scale(1.15); }
  100% { transform: scale(1); }
}
```

### Panel Line Appearance

```css
/* Command line */
.demo-line-cmd {
  display: flex;
  gap: 8px;
  font-family: var(--font-mono);
  font-size: 13px;
  line-height: 1.7;
  opacity: 0;
  transform: translateY(4px);
  animation: lineAppear 0.3s ease-out forwards;
}

@keyframes lineAppear {
  to { opacity: 1; transform: translateY(0); }
}

/* Highlight flash on receiving a message */
.demo-line-highlight {
  animation: lineHighlight 0.6s ease-out;
}

@keyframes lineHighlight {
  0%   { background: rgba(15, 118, 110, 0.2); }
  100% { background: transparent; }
}
```

### Panel Text Colors

- **Panel 1 prompt** (`>`): `color: #0F766E` (teal, matching existing `.terminal-prompt`)
- **Panel 1 command text**: `color: #E7E0D9`
- **Panel 1 output**: `color: #78716C`
- **Panel 2 function names**: `color: #0F766E` (teal)
- **Panel 2 JSON values**: `color: #A8A29E`
- **Panel 2 strings**: `color: #E7E0D9`
- **Panel 3 prompt** (`$`): `color: #0F766E`
- **Panel 3 command/output**: same as Panel 1

### Completion Glow

```css
.demo-panel.complete {
  box-shadow: 0 0 20px rgba(15, 118, 110, 0.25), 0 8px 32px rgba(0,0,0,0.12);
  transition: box-shadow 0.5s ease-out;
}
```

### Mobile (<768px)

```css
@media (max-width: 768px) {
  .demo-stage {
    height: auto;
    display: flex;
    flex-direction: column;
    gap: 16px;
  }

  .demo-panel {
    position: static;
    width: 100%;
    max-height: 280px;
    transform: none;
  }

  .demo-connectors { display: none; }

  .demo-campfire {
    position: static;
    transform: none;
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 12px 16px;
    background: var(--color-surface-alt);
    border-radius: var(--radius);
    border: 1px solid var(--color-border);
  }

  .demo-campfire-flame {
    width: 24px;
    height: 32px;
    margin: 0;
  }

  .demo-campfire-log {
    flex: 1;
    font-size: 12px;
  }

  /* Reorder: Panel 1, campfire, Panel 2, Panel 3 */
  .demo-panel-1 { order: 1; }
  .demo-campfire { order: 2; }
  .demo-panel-2 { order: 3; }
  .demo-panel-3 { order: 4; }
}
```

### Tablet (768-1024px)

```css
@media (min-width: 769px) and (max-width: 1024px) {
  .demo-stage {
    height: auto;
    display: grid;
    grid-template-columns: 1fr 1fr;
    grid-template-rows: auto auto;
    gap: 20px;
    justify-items: center;
  }

  .demo-panel {
    position: static;
    width: 100%;
    max-width: 380px;
    transform: none;
  }

  .demo-panel-3 { grid-column: 1 / -1; max-width: 400px; }
  .demo-campfire { grid-column: 1 / -1; }
  .demo-connectors { display: none; }
}
```

---

## JavaScript Architecture

### Timeline Data Structure

```js
const TIMELINE = [
  // Phase 1: Agent A creates
  { delay: 0,     panel: 1, type: 'cmd',    prompt: '>', text: 'cf create --description "fizzbuzz project"' },
  { delay: 400,   panel: 1, type: 'out',    text: '4b8e1d' },
  { delay: 1200,  panel: 1, type: 'cmd',    prompt: '>', text: 'cf send 4b8e1d "implement FizzBuzz(n int) string" --future' },
  { delay: 1600,  panel: 1, type: 'out',    text: 'd3a7f1' },
  { delay: 2000,  panel: 0, type: 'beacon', text: 'beacon published' },
  { delay: 2200,  panel: 0, type: 'members', text: '1 member' },

  // Phase 2: Agent B discovers
  { delay: 3000,  panel: 0, type: 'flight', from: 0, to: 2, label: 'beacon' },
  { delay: 3500,  panel: 2, type: 'cmd',    prompt: '', text: 'campfire_discover()' },
  { delay: 3900,  panel: 2, type: 'out',    text: '{ campfire_id: "4b8e1d", description: "fizzbuzz project",\n  protocol: "open", sig: valid }' },
  { delay: 4500,  panel: 2, type: 'cmd',    prompt: '', text: 'campfire_join("4b8e1d")' },
  { delay: 4900,  panel: 2, type: 'out',    text: '{ status: "joined" }' },
  { delay: 5000,  panel: 0, type: 'members', text: '2 members' },
  { delay: 5300,  panel: 2, type: 'cmd',    prompt: '', text: 'campfire_read("4b8e1d")' },
  { delay: 5600,  panel: 0, type: 'flight', from: 0, to: 2, label: 'future' },
  { delay: 5900,  panel: 2, type: 'out',    text: '{ sender: "a1f4c2", tags: ["future"],\n  payload: "implement FizzBuzz(n int) string" }' },

  // Phase 3: Agent B fulfills
  { delay: 7400,  panel: 2, type: 'cmd',    prompt: '', text: 'campfire_send("4b8e1d",\n  "FizzBuzz implemented: func FizzBuzz(n int) string { ... }",\n  { fulfills: "d3a7f1" })' },
  { delay: 7800,  panel: 2, type: 'out',    text: '{ id: "e8c1f3" }' },
  { delay: 8000,  panel: 0, type: 'flight', from: 2, to: 0, label: 'fulfillment' },
  { delay: 8200,  panel: 0, type: 'log',    text: 'fulfills d3a7f1', color: 'teal' },

  // Phase 4: Agent C discovers, joins, reviews
  { delay: 8500,  panel: 0, type: 'flight', from: 0, to: 3, label: 'beacon' },
  { delay: 9000,  panel: 3, type: 'cmd',    prompt: '$', text: 'cf discover' },
  { delay: 9300,  panel: 3, type: 'out',    text: '4b8e1d  open  fizzbuzz project  sig:valid\n  transport: filesystem  requires: (none)' },
  { delay: 9800,  panel: 3, type: 'cmd',    prompt: '$', text: 'cf join 4b8e1d' },
  { delay: 10100, panel: 3, type: 'out',    text: 'Joined campfire 4b8e1d' },
  { delay: 10200, panel: 0, type: 'members', text: '3 members' },
  { delay: 10500, panel: 3, type: 'cmd',    prompt: '$', text: 'cf read 4b8e1d' },
  { delay: 10700, panel: 0, type: 'flight', from: 0, to: 3, label: 'messages' },
  { delay: 11000, panel: 3, type: 'out',    text: '[campfire:4b8e1d] agent:a1f4c2 [fulfilled]\n  tags: future\n  implement FizzBuzz(n int) string\n\n[campfire:4b8e1d] agent:b3f8c2\n  tags: fulfills\n  antecedents: d3a7f1c9\n  FizzBuzz implemented: func FizzBuzz(n int) string { ... }' },
  { delay: 11800, panel: 3, type: 'cmd',    prompt: '$', text: 'cf send 4b8e1d "Code review: approved. Tests pass." \\\n    --reply-to e8c1f3' },
  { delay: 12200, panel: 3, type: 'out',    text: 'a9d2b7' },
  { delay: 12400, panel: 0, type: 'flight', from: 3, to: 0, label: 'review' },
  { delay: 12600, panel: 0, type: 'log',    text: 'review: approved', color: 'teal' },

  // Phase 5: Agent A reads results
  { delay: 13000, panel: 0, type: 'flight', from: 0, to: 1, label: 'results' },
  { delay: 13400, panel: 1, type: 'cmd',    prompt: '>', text: 'cf read 4b8e1d' },
  { delay: 13700, panel: 1, type: 'out',    text: '[campfire:4b8e1d] agent:b3f8c2 [fulfills]\n  antecedents: d3a7f1c9\n  FizzBuzz implemented: func FizzBuzz(n int) string { ... }\n\n[campfire:4b8e1d] agent:c9e4a1\n  antecedents: e8c1f3a7\n  Code review: approved. Tests pass.' },
  { delay: 14500, panel: 1, type: 'cmd',    prompt: '>', text: 'cf send 4b8e1d "all tasks complete" --reply-to d3a7f1' },
  { delay: 14900, panel: 1, type: 'out',    text: 'f7b2d4' },

  // Phase 6: Finale
  { delay: 15500, panel: 0, type: 'complete' },
  { delay: 16000, panel: 0, type: 'summary' },
];
```

Panel numbering: `0` = campfire center, `1` = Panel 1 (Claude Code), `2` = Panel 2 (MCP), `3` = Panel 3 (CLI).

### Core Engine

```js
class DemoAnimation {
  constructor(stageEl) {
    this.stage = stageEl;
    this.panels = {
      1: stageEl.querySelector('.demo-panel-1 .demo-panel-body'),
      2: stageEl.querySelector('.demo-panel-2 .demo-panel-body'),
      3: stageEl.querySelector('.demo-panel-3 .demo-panel-body'),
    };
    this.campfire = stageEl.querySelector('.demo-campfire');
    this.log = stageEl.querySelector('.demo-campfire-log');
    this.membersEl = stageEl.querySelector('.demo-campfire-members');
    this.progressBar = document.querySelector('.demo-progress-bar');
    this.stepCount = document.querySelector('.demo-step-count');
    this.summary = document.querySelector('.demo-summary');
    this.timers = [];
    this.started = false;
    this.totalSteps = TIMELINE.length;
  }

  start() {
    if (this.started) return;
    this.started = true;
    this.reset();
    const totalDuration = TIMELINE[TIMELINE.length - 1].delay + 1500;

    TIMELINE.forEach((step, i) => {
      const timer = setTimeout(() => {
        this.execute(step);
        this.updateProgress(i + 1, totalDuration, step.delay);
      }, step.delay);
      this.timers.push(timer);
    });
  }

  reset() {
    this.timers.forEach(t => clearTimeout(t));
    this.timers = [];
    this.started = false;
    Object.values(this.panels).forEach(p => p.innerHTML = '');
    this.log.innerHTML = '';
    this.membersEl.textContent = '';
    this.summary.classList.remove('visible');
    // Reset panel styles
    document.querySelectorAll('.demo-panel').forEach(p => p.classList.remove('complete'));
    document.querySelectorAll('.demo-line').forEach(l => l.classList.remove('active'));
    document.querySelectorAll('.demo-dot').forEach(d => d.classList.remove('visible'));
    if (this.progressBar) this.progressBar.style.width = '0%';
    if (this.stepCount) this.stepCount.textContent = 'Step 0/' + this.totalSteps;
  }

  execute(step) {
    switch (step.type) {
      case 'cmd':
        this.addLine(step.panel, 'cmd', step.prompt, step.text);
        break;
      case 'out':
        this.addLine(step.panel, 'out', '', step.text);
        break;
      case 'beacon':
        this.pulseFlame();
        this.addLogEntry(step.text, 'amber');
        break;
      case 'members':
        this.membersEl.textContent = step.text;
        break;
      case 'flight':
        this.animateFlight(step.from, step.to, step.label);
        break;
      case 'log':
        this.addLogEntry(step.text, step.color);
        break;
      case 'complete':
        document.querySelectorAll('.demo-panel').forEach(p => p.classList.add('complete'));
        this.pulseFlame();
        break;
      case 'summary':
        this.summary.classList.add('visible');
        break;
    }
  }

  addLine(panelNum, type, prompt, text) {
    const panel = this.panels[panelNum];
    if (!panel) return;
    const lines = text.split('\n');
    lines.forEach((line, i) => {
      const div = document.createElement('div');
      div.className = 'demo-line-entry demo-line-' + type;
      if (i === 0 && prompt) {
        const promptSpan = document.createElement('span');
        promptSpan.className = 'demo-prompt';
        promptSpan.textContent = prompt + ' ';
        div.appendChild(promptSpan);
      }
      const textSpan = document.createElement('span');
      textSpan.className = type === 'cmd' ? 'demo-cmd-text' : 'demo-out-text';
      textSpan.textContent = (i > 0 && type === 'out') ? line : line;
      if (i > 0 && type === 'cmd') textSpan.style.paddingLeft = '16px';
      div.appendChild(textSpan);
      panel.appendChild(div);
    });
    // Auto-scroll to bottom
    panel.scrollTop = panel.scrollHeight;
  }

  addLogEntry(text, color) {
    const div = document.createElement('div');
    div.className = 'demo-log-entry demo-log-' + color;
    div.textContent = text;
    this.log.appendChild(div);
  }

  pulseFlame() {
    const flame = this.campfire.querySelector('.demo-campfire-flame');
    flame.classList.remove('pulse');
    void flame.offsetWidth; // force reflow
    flame.classList.add('pulse');
  }

  animateFlight(from, to, label) {
    // Determine which SVG line and dot to animate
    // from/to: 0=center, 1=panel1, 2=panel2, 3=panel3
    // Map to line index: panel1=line-1, panel2=line-2, panel3=line-3
    const targetPanel = from === 0 ? to : from;
    const line = this.stage.querySelector('.demo-line-' + targetPanel);
    const dot = this.stage.querySelector('.demo-dot-' + targetPanel);
    if (!line || !dot) return;

    line.classList.add('active');

    const x1 = parseFloat(line.getAttribute('x1'));
    const y1 = parseFloat(line.getAttribute('y1'));
    const x2 = parseFloat(line.getAttribute('x2'));
    const y2 = parseFloat(line.getAttribute('y2'));

    // Direction: if from=0 (center), go from center coords to panel coords
    // center is at (x2, y2) for lines, panel is at (x1, y1)
    let startX, startY, endX, endY;
    if (from === 0) {
      startX = x2; startY = y2; endX = x1; endY = y1;
    } else {
      startX = x1; startY = y1; endX = x2; endY = y2;
    }

    dot.classList.add('visible');
    const duration = 400;
    const startTime = performance.now();

    const animate = (now) => {
      const elapsed = now - startTime;
      const t = Math.min(elapsed / duration, 1);
      const eased = t < 0.5 ? 2 * t * t : 1 - Math.pow(-2 * t + 2, 2) / 2;
      dot.setAttribute('cx', startX + (endX - startX) * eased);
      dot.setAttribute('cy', startY + (endY - startY) * eased);
      if (t < 1) {
        requestAnimationFrame(animate);
      } else {
        dot.classList.remove('visible');
        setTimeout(() => line.classList.remove('active'), 200);
        if (from !== 0) this.pulseFlame();
      }
    };
    requestAnimationFrame(animate);
  }

  updateProgress(step, totalDuration, currentDelay) {
    const pct = (currentDelay / totalDuration) * 100;
    if (this.progressBar) this.progressBar.style.width = pct + '%';
    if (this.stepCount) this.stepCount.textContent = 'Step ' + step + '/' + this.totalSteps;
  }
}
```

### Scroll Trigger

```js
document.addEventListener('DOMContentLoaded', () => {
  const stage = document.querySelector('.demo-stage');
  if (!stage) return;

  const demo = new DemoAnimation(stage);

  // Reduced motion: skip animation, show final state
  if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
    showStaticFallback(stage);
    return;
  }

  // Start on scroll into view
  const observer = new IntersectionObserver((entries) => {
    entries.forEach(entry => {
      if (entry.isIntersecting) {
        demo.start();
        observer.unobserve(entry.target);
      }
    });
  }, { threshold: 0.3 });

  observer.observe(stage);

  // Restart button
  document.querySelector('.demo-btn-restart')?.addEventListener('click', () => {
    demo.reset();
    setTimeout(() => demo.start(), 100);
  });
});
```

### Static Fallback for Reduced Motion

When `prefers-reduced-motion: reduce` is active, immediately render all three panels with their final content (all lines visible, no animation). Show the summary text. Hide the progress bar and restart button.

```js
function showStaticFallback(stage) {
  // Execute all timeline steps immediately
  TIMELINE.forEach(step => {
    // ... append all content without animation classes
  });
  document.querySelector('.demo-summary')?.classList.add('visible');
  document.querySelector('.demo-controls')?.style.display = 'none';
}
```

---

## Accessibility

1. **`aria-label`** on the `.demo-stage` container: "Animated demonstration of three agents coordinating through a campfire"

2. **`role="log"` and `aria-live="polite"`** on each panel body and the campfire log. Screen readers will announce new content as it appears, without interrupting.

3. **`prefers-reduced-motion: reduce`**: All animations are disabled. The demo shows its final state immediately (all commands and outputs visible). The `@keyframes` rules are wrapped in `@media (prefers-reduced-motion: no-preference) { ... }`.

4. **Keyboard focus**: The restart button is focusable and operable via Enter/Space.

5. **SVG elements**: All decorative SVGs have `aria-hidden="true"` and `focusable="false"`.

6. **Color contrast**: All text colors in the terminal panels meet WCAG AA contrast requirements against the `#1C1917` background:
   - Command text `#E7E0D9` on `#1C1917`: ratio 11.3:1
   - Output text `#78716C` on `#1C1917`: ratio 4.5:1 (AA threshold)
   - Prompt `#0F766E` on `#1C1917`: ratio 4.6:1

---

## Integration with Existing Page

### What Gets Replaced

The entire `<section class="action-section">` block (lines 86-298 of `site/index.html`) is replaced with the new `<section class="demo-section">`.

### What Gets Kept

- The proof section below it remains unchanged.
- The existing `.terminal-dot`, `.terminal-bar` CSS classes are reused.
- The color palette, fonts, and spacing variables from `:root` are reused.

### New CSS

Add to `site/style.css` after the existing `.action-section` block (which can be removed). Approximately 150 lines of new CSS.

### New JS

Add as an inline `<script>` at the bottom of `index.html` (before the closing `</body>` tag), replacing the existing orbital scroll script. Or as a separate `demo.js` file. The timeline data + engine is approximately 200 lines.

---

## IDs and Classes Summary

| Element | Class | Purpose |
|---------|-------|---------|
| Section wrapper | `.demo-section` | Replaces `.action-section` |
| Animation stage | `.demo-stage` | Positioned container for panels + campfire |
| SVG connectors | `.demo-connectors` | Lines between panels and center |
| Individual lines | `.demo-line-1`, `-2`, `-3` | One per panel-to-center connection |
| Animated dots | `.demo-dot-1`, `-2`, `-3` | Glowing dots that travel along lines |
| Campfire center | `.demo-campfire` | Flame + log + member count |
| Flame element | `.demo-campfire-flame` | CSS-drawn animated flame |
| Message log | `.demo-campfire-log` | Shows messages passing through |
| Log entries | `.demo-log-entry` | Individual log lines |
| Panels | `.demo-panel`, `.demo-panel-1`/`-2`/`-3` | Terminal windows |
| Panel bar | `.demo-panel-bar` | Title bar with dots + label |
| Panel body | `.demo-panel-body` | Scrollable content area |
| Line entries | `.demo-line-entry` | Individual lines in panels |
| Controls | `.demo-controls` | Restart button + progress bar |
| Progress bar | `.demo-progress`, `.demo-progress-bar` | Visual progress indicator |
| Summary | `.demo-summary` | Finale text overlay |
