# 06 — Snooze (Zurückstellen)

Snooze defers a thread: it disappears from the inbox immediately and re-appears at a user-specified future time.

Snooze is a server-owned feature in the suite's design. The client picks a wake time and tells the server; the server is responsible for the wake-up. This requires herold to support a snooze contract (`../notes/server-contract.md`); a client-side fallback (timer + `localStorage`) is explicitly rejected because it fails the moment another device or web session is involved, and because a closed laptop should not delay snoozes.

## Snooze options

| ID | Requirement |
|----|-------------|
| REQ-SNZ-01 | "Later today" — if before 14:00, wake at 16:00; otherwise wake at 08:00 next morning. |
| REQ-SNZ-02 | "Tomorrow morning" — wake at 08:00 the following day. |
| REQ-SNZ-03 | "This weekend" — wake at Saturday 08:00. |
| REQ-SNZ-04 | "Next week" — wake at Monday 08:00. |
| REQ-SNZ-05 | "Custom" — date picker plus time input. |

## Behaviour

| ID | Requirement |
|----|-------------|
| REQ-SNZ-10 | A snoozed thread is removed from Inbox immediately. It appears in a "Snoozed" view with the wake time displayed. |
| REQ-SNZ-11 | At wake time, the thread returns to Inbox. The "Snoozed until …" indicator is removed automatically. |
| REQ-SNZ-12 | User can edit the wake time of a snoozed thread, or cancel the snooze (returning the thread to Inbox immediately). |
| REQ-SNZ-13 | Snooze state is stored server-side: `keywords/$snoozed` set to true, plus a `snoozedUntil` ISO 8601 datetime field on the email. The server-side wake-up flips both fields atomically. |
| REQ-SNZ-14 | The Snoozed view sorts by wake time ascending (next-to-wake first). |

## Server contract

The suite requires herold to:

- Accept `keywords/$snoozed: true` and a `snoozedUntil` extension property on `Email/set`.
- At `snoozedUntil` time, atomically: clear `$snoozed`, clear `snoozedUntil`, and re-add the inbox mailbox to `mailboxIds`.
- Emit a normal state-change event so subscribed clients refresh the thread list.

Cross-referenced in `../notes/server-contract.md`.
