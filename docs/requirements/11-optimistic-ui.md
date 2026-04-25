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
