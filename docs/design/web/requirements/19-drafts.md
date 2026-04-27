# 19 — Drafts

Auto-save, recovery, multi-device editing, and what happens when send fails.

A draft is an `Email` in the user's `drafts` mailbox with the `$draft` keyword. Drafts are server-stored; tabard does not maintain a parallel client-side draft store. Compose state that hasn't yet auto-saved lives in `sessionStorage` for crash recovery only.

## Auto-save

| ID | Requirement |
|----|-------------|
| REQ-DFT-01 | While compose is open and the body is dirty (any field changed since the last save), tabard auto-saves on a debounce of 5 seconds. The 5 s window resets on every keystroke. |
| REQ-DFT-02 | The first auto-save creates the Email via `Email/set { create: ... }` with `keywords/$draft: true` and `mailboxIds/<drafts-id>: true`. Subsequent auto-saves are `Email/set { update: ... }` against the assigned id. |
| REQ-DFT-03 | The auto-save call captures: From identity, To/Cc/Bcc, Subject, body (plain or HTML), in-reply-to/references for replies, attachment blobIds. It does NOT capture compose-window position or focus state. |
| REQ-DFT-04 | Auto-save respects the `state` of `Email`: each save passes `ifInState` to detect concurrent edits (`REQ-DFT-30`). On state mismatch, the multi-device conflict path runs. |
| REQ-DFT-05 | While an auto-save is in flight, additional changes accumulate; the next save fires after the current one resolves. Saves do not stack (no two `Email/set` in flight on the same draft). |

## Indicators

| ID | Requirement |
|----|-------------|
| REQ-DFT-10 | The compose-window header shows a small status indicator: "Draft saved" / "Saving…" / "Save failed". |
| REQ-DFT-11 | "Save failed" persists until the next successful save or until the user closes the compose. The indicator is non-blocking — it does not prevent further editing. |

## Drafts list

| ID | Requirement |
|----|-------------|
| REQ-DFT-20 | The "Drafts" sidebar entry opens a list of every draft (`Email/query` filter `inMailbox=<drafts-id>` sort by `sentAt desc`, falling back to `receivedAt desc` since drafts have no sent date until sent). |
| REQ-DFT-21 | Each row shows: To recipients (truncated), Subject, the body snippet, last-edited time. |
| REQ-DFT-22 | Clicking a draft row opens compose loaded with the draft. Multiple drafts can be open simultaneously up to the compose-stack cap (`09-ui-layout.md` REQ-UI-04). |

## Recovery

| ID | Requirement |
|----|-------------|
| REQ-DFT-50 | While a compose is open, its un-auto-saved field state persists in `sessionStorage` keyed by the draft's id (or a temporary id pre-first-save). Updated on each keystroke (no debounce; this is cheap and crash-survivable). |
| REQ-DFT-51 | On tab reload, tabard reads `sessionStorage`: if any compose state is present, it re-opens those compose windows pre-populated. After successful re-render, the `sessionStorage` entries are kept until the next successful auto-save. |
| REQ-DFT-52 | `localStorage` is NOT used for draft recovery (privacy: drafts shouldn't outlive a tab). The user opting in to `localStorage` for the auth token (`13-nonfunctional.md` REQ-SEC-02) does not extend to draft state. |

## Multi-device conflicts

| ID | Requirement |
|----|-------------|
| REQ-DFT-30 | An `Email/set { update }` for a draft includes `ifInState: <state-string>` (RFC 8620 §3.2.6). The state string is the `Email` state captured at the most recent successful save in this compose session. |
| REQ-DFT-31 | If the server returns `stateMismatch`, tabard pauses auto-save, fetches the current server-side draft via `Email/get`, and presents a non-blocking inline banner in the compose: "This draft was edited from another device. [Take this version] [Use the server version] [Show diff]". |
| REQ-DFT-32 | "Take this version" forces the local state into the server with a fresh `ifInState`. "Use the server version" replaces the compose body with the server's draft. "Show diff" reveals a side-by-side view (a v1.5 nicety; cut for v1 if implementation-time pressure). |
| REQ-DFT-33 | While the conflict banner is shown, auto-save is paused. Resolving the conflict resumes auto-save. |

## Discarding

| ID | Requirement |
|----|-------------|
| REQ-DFT-40 | Discard is a single button in the compose toolbar (a trash icon) plus the keyboard shortcut `Cmd+Shift+D` (or `Ctrl+Shift+D` on non-Mac). |
| REQ-DFT-41 | Discard prompts a confirmation only if the body is non-empty AND the draft has been auto-saved at least once. (A draft with nothing in it doesn't warrant a confirmation.) |
| REQ-DFT-42 | Discard issues `Email/set { destroy: [<draft-id>] }`, removes the compose window, and shows a toast "Draft discarded" with Undo for 5 seconds. |
| REQ-DFT-43 | Undo on discard re-creates the Email via `Email/set { create }` with the stored body content; the new draft has a different id from the original. |

## Send-failure-keeps-as-draft

| ID | Requirement |
|----|-------------|
| REQ-DFT-60 | If `EmailSubmission/set` fails (server error, transport error), tabard does NOT remove the draft. The compose window re-opens (or stays open) with the body intact, the `$draft` keyword still set, and a toast: "Send failed: <reason>. The message is saved as a draft." |
| REQ-DFT-61 | Send-Undo (`11-optimistic-ui.md` REQ-OPT-11) operates on the planned `EmailSubmission` before it fires; it does not depend on send-failure recovery. |

## Reply / forward as draft

| ID | Requirement |
|----|-------------|
| REQ-DFT-70 | A reply or forward (`02-mail-basics.md` REQ-MAIL-30..32) starts as a compose window with `inReplyTo` and `references` properties pre-populated. The first auto-save creates the draft with those headers. |
| REQ-DFT-71 | The parent email is NOT marked `$answered` / `$forwarded` until the user actually sends. A draft reply that's discarded leaves the parent's keywords untouched. |
