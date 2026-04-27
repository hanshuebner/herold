# 14 — Unsubscribe

When a message comes from a mailing list or commercial sender that has declared an unsubscribe mechanism per RFC 2369 / RFC 8058, the suite surfaces a clear "Unsubscribe" affordance and runs the unsubscribe with as little friction as the protocol allows.

Why a dedicated doc: this is the most common single-action workflow that distinguishes "an email client a normal person can use" from "raw mail UI". Doing it well requires understanding the header forms, the one-click variant's security implications, and the failure modes.

## Detection

| ID | Requirement |
|----|-------------|
| REQ-UNS-01 | The suite parses the `List-Unsubscribe` header (RFC 2369). The header may carry one or more `<URL>` values, where `URL` is `mailto:`, `https:`, or (legacy) `http:`. |
| REQ-UNS-02 | The suite parses `List-Unsubscribe-Post: List-Unsubscribe=One-Click` (RFC 8058). When present alongside an HTTPS `List-Unsubscribe` URL, the one-click flow applies. |
| REQ-UNS-03 | The Unsubscribe affordance is shown only when at least one of these mechanisms is present. Absent headers → no affordance, no fallback to body-text scraping. |
| REQ-UNS-04 | `http://` (cleartext) URLs in `List-Unsubscribe` are NOT honoured. The suite surfaces "the sender's unsubscribe link is unencrypted; use the link in the message body if you trust it" and does not click it for the user. |

## Surface

| ID | Requirement |
|----|-------------|
| REQ-UNS-10 | The affordance is a small button labelled "Unsubscribe" in the thread header area, between the subject and the action toolbar. |
| REQ-UNS-11 | The button shows on the thread, not on individual messages — even though the header is per-message. (A list message and a one-off response from the same sender don't both need the button.) |
| REQ-UNS-12 | The button is not surfaced from the thread list; it lives only in the reading pane. |

## Action: choose mechanism

When multiple mechanisms are advertised, the suite prefers in this order:

1. RFC 8058 one-click (`List-Unsubscribe-Post: List-Unsubscribe=One-Click` + HTTPS URL). No user data leaves the client beyond the empty POST body the RFC mandates.
2. Plain HTTPS URL (open in a new tab; the suite does not auto-click).
3. `mailto:` (compose a message to the unsubscribe address).

| ID | Requirement |
|----|-------------|
| REQ-UNS-20 | One-click: the suite issues `POST <url>` with body `List-Unsubscribe=One-Click` and `Content-Type: application/x-www-form-urlencoded`. No user-agent, no cookies, no referrer. |
| REQ-UNS-21 | Plain HTTPS URL: the suite opens the URL in a new tab with `rel="noopener noreferrer"`. The unsubscribe state is the user's responsibility from that point. |
| REQ-UNS-22 | `mailto:`: the suite opens a compose window with `to`, `subject`, and `body` populated from the URI parameters. The user must hit Send to actually unsubscribe; the suite does not auto-send. |
| REQ-UNS-23 | If both one-click and a `mailto:` are present, the suite uses one-click silently. The fallback is opaque to the user. |

## Confirmation

| ID | Requirement |
|----|-------------|
| REQ-UNS-30 | One-click: NO confirmation dialog. The whole point of RFC 8058 is frictionless unsubscribe; an "are you sure?" defeats it. |
| REQ-UNS-31 | Plain HTTPS URL: NO confirmation dialog before opening the tab. The user is taking the action; opening a tab is reversible. |
| REQ-UNS-32 | `mailto:`: confirmation by virtue of the compose-and-Send flow. |

## Result feedback

| ID | Requirement |
|----|-------------|
| REQ-UNS-40 | On a successful one-click POST (any 2xx response): toast "Unsubscribed from <sender display name>". |
| REQ-UNS-41 | On a one-click POST failure (non-2xx, network error): toast "Unsubscribe failed — try the link in the message body" with the original `List-Unsubscribe` URLs revealed in a tooltip. |
| REQ-UNS-42 | On opening a plain HTTPS URL: no toast. The user sees the destination tab. |
| REQ-UNS-43 | On opening a `mailto:`: no toast on open; the toast on send comes from the normal compose-send path (`requirements/02-mail-basics.md` REQ-MAIL-15). |

## Persistence and follow-up

| ID | Requirement |
|----|-------------|
| REQ-UNS-50 | After a successful one-click unsubscribe, the suite records the sender's `From` address in a client-local "unsubscribed-from" set, stored in `localStorage` per account. The set has no v1 UI. |
| REQ-UNS-51 | Future messages from senders in the unsubscribed-from set get a small "previously unsubscribed" badge in the thread list. (Suggests a filter; doesn't auto-archive.) |
| REQ-UNS-52 | The unsubscribed-from set survives across sessions but is not synced across devices in v1. |

## Threats considered

- **Auto-clicked tracking.** A naive client that GETs the `List-Unsubscribe` URL on display defeats the user's privacy. The suite never fetches the URL until the user clicks the button.
- **Confirmation-page tracking.** A `https://` URL that wants the user's identity ("click here to confirm unsubscribe from foo@example.com") is the sender's choice, not ours; the suite delivers the user to it and steps back.
- **Phishing the button.** A spammer can advertise a `List-Unsubscribe` header pointing anywhere. The button only triggers either a POST (no user data) or a tab open (user sees the destination); it never auto-submits a form with user data.
- **Cross-site cookies on the POST.** RFC 8058 §3 requires the POST without credentials. The suite sets `credentials: "omit"` on the fetch.

## Out of scope

- Body-text scraping for "click here to unsubscribe" links when no `List-Unsubscribe` header is present. Too unreliable; opens an XSS-style attack surface where the page's "unsubscribe" link points at something else entirely.
- Mass-unsubscribe ("here are 87 senders you haven't read in 6 months — unsubscribe from all"). Worth considering post-v1 from the unsubscribed-from set + read-event capture, but not v1.
