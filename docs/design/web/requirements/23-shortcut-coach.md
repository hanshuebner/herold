# 23 — Shortcut coach

A continuous, behaviour-driven nudge system that helps the user move from mouse to keyboard for actions that have shortcuts. Always-on by default; designed to be unobtrusive enough that the user accepts it as ambient rather than seeking it out.

The coach observes the user's invocation patterns, identifies actions with shortcuts the user isn't using, and surfaces a single hint in a fixed strip when a teachable moment arises (typically: the user just did action X via mouse, and X has a shortcut they don't yet use). Over time, as the user internalises shortcuts, the coach stops hinting about them; if usage regresses, the coach re-surfaces.

This is a suite-centric feature in v1. Chat and (future) calendar / contacts get their own coach state once those apps mature, sharing the same machinery.

## Concept

| ID | Requirement |
|----|-------------|
| REQ-COACH-01 | The shortcut coach is **on by default**. The user can disable it in settings (`20-settings.md` REQ-SET-12). |
| REQ-COACH-02 | The coach observes user actions, classifies them by invocation method (mouse / keyboard), tracks per-action statistics, and uses those statistics to decide whether and when to hint about a keyboard shortcut. |
| REQ-COACH-03 | Hints surface in a single fixed area in the suite shell — see § UI. Never as popup, modal, or focus-stealing element. |
| REQ-COACH-04 | The user is the only beneficiary and the only audience of their coach data. Stats are per-principal and never aggregated, exported, or shared. |

## Observation

Each user action is logged with the method used to invoke it.

| ID | Requirement |
|----|-------------|
| REQ-COACH-10 | The suite logs every "trackable action" — actions that map to a documented keyboard shortcut (`10-keyboard.md`). The full list is the union of single-key bindings and two-key sequences from REQ-KEY tables, plus chord shortcuts (`Cmd/Ctrl+Enter` for send, `Cmd/Ctrl+B` etc. for compose marks). |
| REQ-COACH-11 | Each invocation records: action name (e.g. `archive`, `reply`, `nav_inbox`), method (`keyboard` or `mouse`), timestamp, and (for keyboard) the actual key sequence pressed. |
| REQ-COACH-12 | Mouse method covers: clicks on toolbar buttons, clicks on context-menu items, clicks on action-bearing affordances (the per-message "react" button, the sidebar "compose" button, etc.). It does NOT cover navigation by clicking a thread row (because there's no shortcut for "open this specific row" — `Enter` opens the focused row, but the user clicked a different row). |
| REQ-COACH-13 | Keyboard method covers: shortcut-driven invocations through the global keyboard dispatcher (`../architecture/05-keyboard-engine.md`). Two-key sequences are tracked as one invocation of their resolved action (`g i` → one `nav_inbox` entry). |
| REQ-COACH-14 | Actions taken inside a focused input / editor (typing in compose, typing in search) are NOT tracked even when they trigger an action via shortcut (e.g. `Cmd+B` for bold inside compose is a compose-mark action, but it's a different teaching context — see REQ-COACH-71). |
| REQ-COACH-15 | The local invocation log is a per-session ring buffer (default 500 entries) plus periodic flushes to herold's per-principal stats store (default every 60 seconds while active, plus on `visibilitychange` and `beforeunload`). Same flush model as `gmail-logger/content.js`. |

## Knowledge model

When does the coach decide "the user knows this shortcut"?

| ID | Requirement |
|----|-------------|
| REQ-COACH-20 | An action's `internalised` state is computed from the per-principal stats. An action is **internalised** when, in the last 14 days: keyboard invocations ≥ 5 AND keyboard invocations ≥ mouse invocations. |
| REQ-COACH-21 | An action is **learning** when keyboard invocations < 5 in the last 14 days but the action has been invoked at all. |
| REQ-COACH-22 | An action is **forgotten** when, comparing two windows: in the 15–90-day window, keyboard invocations ≥ 10; in the recent 14-day window, keyboard invocations = 0 AND mouse invocations ≥ 3. The user used to use the shortcut, has stopped, and is now reaching for the mouse. |
| REQ-COACH-23 | An action is **dormant** when the user hasn't invoked it at all in the last 14 days. The coach makes no claims about dormant actions. |

These categories are computed per (principal, action) and refreshed on every flush.

## Hint generation

| ID | Requirement |
|----|-------------|
| REQ-COACH-30 | A hint may surface when the user has just performed a trackable action via mouse, AND that action's shortcut state is `learning` or `forgotten`. Internalised actions never trigger hints. |
| REQ-COACH-31 | At most one hint is on screen at a time. A new hint replaces the current hint with a 200ms cross-fade. |
| REQ-COACH-32 | Hint cadence is rate-limited per session: at most 1 hint per 30 seconds, at most 8 hints per session. After the cap, the coach goes quiet for the rest of the session even if more teachable moments arise. |
| REQ-COACH-33 | A hint that has been dismissed by the user (REQ-COACH-44) suppresses the same action's hint for 24 hours. |
| REQ-COACH-34 | Dismiss-counts are tracked: an action whose hint has been dismissed ≥ 3 times by this user is suppressed for 14 days. The user is signalling "I see the hint and I'm not interested" — respect it. |
| REQ-COACH-35 | Hints for `forgotten` actions carry a small "you used to use this" prefix in the hint text — the framing matters. ("Welcome back" rather than "Tip"). |

## Prediction

The "what shortcut to surface next" decision uses recent action bigrams, modelled the same way `gmail-logger`'s `top_workflows` are computed.

| ID | Requirement |
|----|-------------|
| REQ-COACH-40 | After every trackable action, the coach maintains a sliding bigram count over the last N actions (default N=200, in-memory only — no server-side bigram store). |
| REQ-COACH-41 | When the user does action X via mouse: the coach inspects the bigram store for "what action most commonly follows X for this user" — call it Y. If Y has a shortcut and Y's state is `learning` or `forgotten`, the coach queues a hint about Y's shortcut. Otherwise, it queues a hint about X's own shortcut (the action just performed). |
| REQ-COACH-42 | If neither X nor Y qualifies, no hint is shown — silent moments are part of the design. |
| REQ-COACH-43 | The bigram store is session-scoped: it does not persist across reloads or across devices. The user's per-action stats (the input to REQ-COACH-20..22) are per-principal and persistent; the bigram-driven prediction is recent and contextual. |

## UI

| ID | Requirement |
|----|-------------|
| REQ-COACH-50 | The coach strip is a thin (24px) horizontal area docked at the bottom of the suite shell, above any browser-level status. Always present; empty when no hint is active. |
| REQ-COACH-51 | A hint renders as a single chip: action label + the keyboard shortcut rendered as `<kbd>` keys + small dismiss button (×). Optional "you used to use this" prefix when the action's state is `forgotten`. |
| REQ-COACH-52 | A hint enters with a 200ms slide-up + fade-in; auto-dismisses after 6 seconds with a 200ms fade-out. The dismiss timer pauses while the user hovers the hint. |
| REQ-COACH-53 | Dismiss × on the hint records a per-action dismiss event (REQ-COACH-33..34) and immediately clears the strip. |
| REQ-COACH-54 | Hovering the hint reveals a small "Snooze hints for this session" affordance that, when clicked, suppresses all coach output until the next session. (Different from REQ-SET-12 which is a permanent disable.) |
| REQ-COACH-55 | The strip is hidden in compose modal and call modal — both contexts are user-focused work where additional UI is unwelcome. |
| REQ-COACH-56 | Coach hints are screen-reader-aware: the strip carries `role="status"` and `aria-live="polite"` so assistive tech announces hints as they appear, but only when the user isn't focused inside an input. |

## Storage and sync

| ID | Requirement |
|----|-------------|
| REQ-COACH-60 | Per-action stats live server-side via a new JMAP datatype: `ShortcutCoachStat`. Capability `https://netzhansa.com/jmap/shortcut-coach`. See `notes/server-contract.md`. |
| REQ-COACH-61 | Stat shape: `{ action: String, keyboardCount14d: Number, mouseCount14d: Number, keyboardCount90d: Number, mouseCount90d: Number, lastKeyboardAt: UTCDate, lastMouseAt: UTCDate, dismissCount: Number, dismissUntil: UTCDate? }`. The 14- and 90-day windows are computed server-side at access time (not stored as flat rolled-up counters that drift). |
| REQ-COACH-62 | The suite issues `ShortcutCoachStat/get` at bootstrap to load the user's coach state, then patches it via `ShortcutCoachStat/set` on the periodic flush (REQ-COACH-15). The flush carries the session's ring-buffer entries; herold updates the rolling counters. |
| REQ-COACH-63 | If the capability is not advertised, the coach degrades to session-only state (in-memory ring buffer + bigram store; no cross-session learning). The hint logic still works, but every fresh tab starts the user as a beginner. |
| REQ-COACH-64 | Stats are NOT synced via the standard JMAP state-string mechanism. They are mutated by the suite frequently and read rarely; per-Email-style change broadcasting would be wasteful. The state advances per call, but the suite does not subscribe to changes (we wrote them; we don't need pushed echoes). |

## Privacy and opt-out

| ID | Requirement |
|----|-------------|
| REQ-COACH-70 | The coach data is per-principal and accessible only to the principal. Admin / multi-user views are out (NG3 / NG4 in `00-scope.md`). |
| REQ-COACH-71 | The "Disable shortcut coach" preference (`20-settings.md` REQ-SET-12) suppresses observation, hint generation, and server-side flushes. When disabled, no new stats accrue; existing stats are NOT deleted (turning the coach back on resumes from where it left off). |
| REQ-COACH-72 | A "Reset coach data" action in settings allows the user to wipe their stats — fires `ShortcutCoachStat/set { destroy: <all> }`. Useful if the user wants to start fresh, has shared a fresh Suite tab with someone else briefly, etc. |
| REQ-COACH-73 | Coach data is included in any account export the user requests (a future operator feature; not in v1). |

## Compose-mark hints

A small special case worth pinning since it's the one place coach overlaps focus-input-rules.

| ID | Requirement |
|----|-------------|
| REQ-COACH-80 | Compose-internal mark shortcuts (`Cmd/Ctrl+B`, `Cmd/Ctrl+I`, `Cmd/Ctrl+U`, `Cmd/Ctrl+K` for link, `Cmd/Ctrl+E` for inline code) are NOT tracked by the global coach. The compose toolbar is the right place to teach them, not the coach strip — the strip is hidden in compose anyway (REQ-COACH-55). |
| REQ-COACH-81 | Compose-mark hints are deferred to a future refinement of compose UI; the toolbar already shows the shortcut on hover via standard browser tooltips. |

## Edge cases

| ID | Requirement |
|----|-------------|
| REQ-COACH-90 | Two-key sequence shortcuts (`g i`) are taught as a single chip showing both keys: `<kbd>g</kbd> <kbd>i</kbd>`. The hint label clarifies "press g, then i" via tooltip. |
| REQ-COACH-91 | Chord shortcuts (`Cmd/Ctrl+Enter` for send) display the platform-correct modifier glyph: ⌘ on macOS, Ctrl on others. Detection via `navigator.platform`. |
| REQ-COACH-92 | If the user has rebound a shortcut (REQ-KEY-04, future), the hint shows the user's binding, not the default. |
| REQ-COACH-93 | When the keyboard help overlay (`?`) is open, the coach strip is hidden — the overlay is the comprehensive view. |
| REQ-COACH-94 | New users see no hints in the first ~30 seconds of a session — gives the UI a chance to render and stabilise before the first nudge. |

## Out of scope

- Cross-suite coach (chat shortcuts, calendar shortcuts) for v1. Same machinery extends naturally; chat and calendar gain their own action sets when those apps land.
- Heuristic / ML-based prediction beyond simple bigrams. Bigrams cover the dominant patterns ("after open-thread, you usually reply"); deeper sequence models add complexity for marginal benefit.
- Showing more than one hint at a time. Two simultaneous hints compete for attention; the coach is a teacher, not a billboard.
- Gamification (streaks, "you used 5 shortcuts today!"). The suite is a tool, not a habit-builder.
- Coaching in the chat panel itself (typing-shortcut hints inside chat compose). Coaching is a mail-app feature surfaced in suite-shell strip.
- Coaching for actions without shortcuts. If an action has no shortcut, the coach can't help — and we don't add shortcuts purely so the coach has something to teach.
- Telemetry of coach effectiveness ("did the user adopt the shortcut after we hinted?"). Per REQ-COACH-04, coach data is the user's; we don't measure ourselves on their behaviour.
