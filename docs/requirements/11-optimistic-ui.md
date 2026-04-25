# 11 — Optimistic UI

Many user actions update the screen before the server confirms. This file specifies which actions are optimistic, how failure is handled, and where Undo applies.

## Optimistic actions

| ID | Requirement |
|----|-------------|
| REQ-OPT-01 | Archive, label apply / remove, star toggle, delete, mark-read / mark-unread MUST update the UI immediately, before any server response. |
| REQ-OPT-02 | If the server call fails, the UI reverts to the pre-action state and a toast appears: "Action failed — Retry". The Retry replays the same call. |
| REQ-OPT-03 | Optimistic updates are applied to the in-memory cache and to any list views derived from it; the cache rolls back on failure. |
| REQ-OPT-04 | The optimistic update appears within 100 ms of the input event (one frame, plus a render). See `13-nonfunctional.md` REQ-PERF-03. |

## Undo

| ID | Requirement |
|----|-------------|
| REQ-OPT-10 | Undo is offered for: archive, delete, snooze, send. Undo window: 5 seconds. |
| REQ-OPT-11 | Send-Undo holds the `EmailSubmission/set` and only fires it after the 5 s window elapses. Undo within the window cancels the planned submission. (Sieve-based delayed sends and herold's queue mean the message will not actually leave during the window.) |
| REQ-OPT-12 | Archive-Undo and delete-Undo replay the inverse `Email/set`. |
| REQ-OPT-13 | Snooze-Undo replays the inverse: clear `$snoozed`, clear `snoozedUntil`, restore the inbox mailbox in `mailboxIds`. |
| REQ-OPT-14 | Only one undo toast is visible at a time. A second optimistic action displaces the first toast (the first action is then unundoable). |

## Failure modes the user sees

| Symptom | Cause | UX |
|---------|-------|----|
| Action reverts after a beat | Server returned 4xx / 5xx for the JMAP method call | Toast: "Action failed — Retry" |
| Action reverts after a long pause | Network timed out (configurable; default 10 s) | Toast: "No server response — Retry" |
| Action vanishes silently after several seconds | EventSource push delivered a state that supersedes our optimistic write (rare; usually only on a conflict) | The cache reconciles to server truth; no toast — the user's action was preempted by a later one |

## Connection state and reconciliation

The connection states the UI reflects, and what happens to in-flight optimistic writes during transitions.

### States

| State | Trigger | UX |
|-------|---------|-----|
| Connected | EventSource push channel open and last heartbeat within `ping_interval × 2`. | No banner. |
| Reconnecting | Push channel dropped, OR last heartbeat older than `ping_interval × 2`. Polling fallback active per `../requirements/13-nonfunctional.md` REQ-REL-02. | Subtle inline indicator next to the account name in the chrome: "Reconnecting…" with a spinner. Not a banner. |
| Disconnected | Reconnect attempts have failed for > 60 s with no progress. | Banner above the thread list: "Connection lost. [Retry]". Non-modal — the cached UI continues to function. |

### Optimistic writes during disconnect

| ID | Requirement |
|----|-------------|
| REQ-OPT-30 | Optimistic actions taken during Reconnecting / Disconnected states still apply to the local cache and re-render the UI as if connected. The user does not feel the network status — the local cache is the source of truth from the user's perspective. |
| REQ-OPT-31 | The JMAP calls backing those actions queue locally in a per-action queue. When connection returns to Connected, the queue drains in submission order. |
| REQ-OPT-32 | Each queued call carries an `ifInState` matching the cache's state at the time of the optimistic write. On drain, a `stateMismatch` returned by herold means "the world moved while you were offline" — the queued write is treated as a conflict (next row). |
| REQ-OPT-33 | The queue is in-memory only. A page reload during disconnect drops the queue (the user gets a "you had unsaved actions when this page reloaded" notice). The optimistic UI changes that hadn't been queued to the server are gone too — the cache rebuilds from scratch. |

### Conflicts on reconnect

| ID | Requirement |
|----|-------------|
| REQ-OPT-40 | When a queued action fails on drain due to `stateMismatch`, tabard does NOT silently retry. The failure surfaces as a per-action toast: "Could not <action> — the conversation was changed elsewhere. [View server version] [Retry as if new]". |
| REQ-OPT-41 | "View server version" reverts the local cache to server truth (issuing `Foo/get` for the affected IDs), discarding the optimistic write. |
| REQ-OPT-42 | "Retry as if new" reissues the action without `ifInState`, accepting whatever the server's current state is as the basis. (Useful for archive-of-an-email kind of actions where the conflict isn't substantive.) |

### Reconnect reconciliation

| ID | Requirement |
|----|-------------|
| REQ-OPT-50 | On EventSource reconnect with `Last-Event-ID`, the server resumes the change stream from the last delivered event. Tabard processes the resumed events as it would live ones. |
| REQ-OPT-51 | If the server returns `cannotCalculateChanges` for a type during reconnect-replay (the disconnect was longer than retention), tabard does a full re-fetch of that type's currently-rendered IDs. The thread list briefly shows a loading indicator; cached rows render until replaced. |
| REQ-OPT-52 | Reconnect does not blank the UI. The user sees their last cached view throughout the disconnect-and-reconnect cycle; only the freshness indicator changes. |
