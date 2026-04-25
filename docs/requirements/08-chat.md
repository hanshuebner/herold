# 08 — Chat

> **⚠ PLACEHOLDER** — scope unresolved. JMAP does not natively cover chat; including chat would mean a parallel API and a separate transport. Capture data will indicate whether the user actually visits Chat / Spaces / DMs in Gmail; if those view counts are low, chat is cut from v1.

## Open scope questions

- Which chat product is in scope, if any? (Google Chat Spaces, DMs, something else.)
- Is chat a first-class view, a side-panel overlay, or a separate route?
- DMs only, or Spaces too?
- Is message history search in scope?
- What API / transport is on the server side?

## Provisional requirements (only if chat is in scope)

| ID | Requirement |
|----|-------------|
| REQ-CHAT-01 | Chat does not interrupt the mail UI. Notifications are a non-blocking indicator, not a modal. |
| REQ-CHAT-02 | Chat data does not flow through tabard's JMAP code paths. |

Resolution gate: count of `view_change` events with `view=chat` / `view=chat_dm` / `view=chat_space` in the gmail-logger analysis report. If `< 5` across the capture window, chat is cut.
