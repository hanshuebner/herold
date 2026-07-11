# Gmail gap analysis

**2026-07-11.** A current read of how herold compares to the Gmail feature set
Google documents on its end-user training and help pages. The training hub is
`support.google.com/a/users/answer/9259748` ("Get started with Gmail"); the
detail below is drawn from the individual how-to articles it links (filters,
search operators, confidential mode, snooze, schedule send, delegation,
templates, vacation responder, send-as, Tasks, offline, nudges, and the Gemini /
Smart Compose / Smart Reply pages). The training pages describe the *user-facing
product*, so the comparison is against herold's **Suite** (the JMAP web client)
plus the mail substrate it depends on — not the SMTP/IMAP/plugin internals.

This is a point-in-time gap read. The per-feature disposition index lives in
`gmail-feature-map.md` (which predates scope revs 4-11 and is stale on several
rows). Definitive scope is `../00-scope.md`; the coverage herold commits to the
Suite is `herold-coverage.md`.

Status labels: **Built** (Suite view / lib exists and the requirement is
written), **Partial** (built but narrower than Gmail — see the feature note),
**Specced** (requirement exists, Suite surface not built), **Non-goal**
(excluded by an `../00-scope.md` NG or a standing product line).

Grounding note: three help pages returned 404 during collection (templates,
multiple-inboxes, and one Gemini page); those points lean on adjacent Google
pages and general product knowledge, and are flagged inline.

## Parity: Gmail workflows herold reproduces

For the everyday mechanics of mail, herold is a deliberate Gmail-workflow clone.

| Gmail feature | herold status | Reference |
|---|---|---|
| System mailboxes + thread/accordion reading | Built | `../requirements/02-mail-basics.md` |
| Compose / reply / reply-all / forward, multi-window stack | Built | `../requirements/02-mail-basics.md` |
| Star, mark read/unread, archive, delete | Built | `../requirements/02-mail-basics.md` |
| Labels (nested, coloured, unread badges, multi-label) | Built | `../requirements/03-labels.md` |
| Inbox categories (Primary/Social/Promotions/Updates/Forums) | Built (LLM-classified, editable prompt) | `../requirements/05-categorisation.md` |
| Snooze | Built (parity — see note) | `../requirements/06-snooze.md` |
| Undo send | Built (configurable 0-30 s window) | `../requirements/20-settings.md` REQ-SET-06 |
| Signatures, multiple send-as identities | Built (plain-text sig; identity verify + import) | `../requirements/20-settings.md` |
| Drafts auto-save + recovery | Built | `../requirements/19-drafts.md` |
| Chat + Spaces | Built | `../requirements/08-chat.md` |
| Contacts | Built | `../requirements/27-contacts.md` |
| Unsubscribe (RFC 2369 / 8058 one-click) | Built | `../requirements/14-unsubscribe.md` |
| Keyboard shortcuts + behavioural shortcut coach | Built | `../requirements/10-keyboard.md`, `../requirements/23-shortcut-coach.md` |
| External-image gating, inline images, attachments | Built | `../requirements/17-attachments.md` |
| Push notifications, mobile swipe actions | Built | `../requirements/25-push-notifications.md`, `../requirements/24-mobile-and-touch.md` |

**Snooze note.** Gmail's presets are Later today, Tomorrow, This weekend, Next
week, Someday, and Pick date & time. herold matches all but "Someday" (Later
today / Tomorrow morning / This weekend / Next week / Custom — REQ-SNZ-01..05).
Effective parity.

## Partial parity: herold has it, narrower than Gmail

These are built, but a side-by-side against the Gmail how-to pages shows a
materially smaller surface. Worth tracking because they read as "done" in the
parity table while a Gmail migrant will hit the edges.

### Search operators

herold parses `from:`, `to:`, `subject:`, `label:`, `in:` (inbox/trash/junk/
sent/drafts/anywhere), `has:attachment`, `is:unread`, `is:starred`,
`is:snoozed`, `before:`, `after:` (REQ-SRC-10) — about 11 operators. Gmail's
search-operator page documents roughly 30. Missing from herold:

| Gmail operator | Purpose |
|---|---|
| `cc:` / `bcc:` | recipient-field search |
| `older_than:` / `newer_than:` (`d`/`m`/`y`) | relative date windows |
| `size:` / `larger:` / `smaller:` | by message size |
| `filename:` | attachment name / type |
| `list:` | mailing-list id |
| `deliveredto:` | envelope recipient |
| `category:` | search a category directly |
| `rfc822msgid:` | Message-Id lookup |
| `has:drive` / `document` / `youtube` etc. | linked-content types |
| `is:muted` / `is:important` | muted / importance |
| `OR`, `{ }`, `AND`, `-`, `( )`, `AROUND`, `+`, `" "` | boolean / grouping / proximity / exact |

The boolean/grouping and relative-date gaps are the most user-visible; herold's
`Email/query` combines conditions but the query language does not expose `OR`,
grouping, or `newer_than:`.

### Filters

herold filter conditions (REQ-FLT-01) are the closed enum `from`, `from-domain`,
`to`, `subject`, `list-id`, `has-attachment`, `thread-id`, combined with **AND
only** (REQ-FLT-02, `*` wildcard on address fields). Actions (REQ-FLT-10..15):
apply label, skip inbox, mark read, delete, forward. "Apply to matching
conversations" exists (REQ-FLT-21).

Gmail's filter builder additionally offers, as **conditions**: "Has the words" /
"Doesn't have the words" (free-text incl. body), "Size greater/less than",
"Date within", and "Doesn't include chats"; and as **actions**: star, mark
important / never important, never send to spam, and categorize-as. So herold
lacks body/free-text and size/date filter conditions, and the star / importance
/ never-spam / categorize actions. herold conversely offers `from-domain` as a
first-class condition, which Gmail expresses only via `*@domain` wildcards.

### Vacation responder

herold backs this with JMAP `VacationResponse` (RFC 8621 §8): enabled flag, date
range, subject, body (REQ-SET-09). Gmail's responder adds a "send to my contacts
only" checkbox and once-per-4-days per-sender de-duplication. RFC 8621
`VacationResponse` has no contacts-only field, so that scoping option is a gap
unless herold adds an extension property.

### Schedule send

The substrate exists: `EmailSubmission.sendAt` with cancel-before-sendAt
semantics is committed (server REQ-PROTO-58, REQ-FLOW-63) and drives the Undo
window. Gmail's user-facing schedule feature — a date/time picker at send, a
"Scheduled" folder, edit-and-reschedule, cancel-to-draft, up to 100 pending — is
not specced in the Suite. The plumbing is there; the feature and its view are
not.

### Send-as vs fetch-from-other-accounts

herold's multiple verified Identities cover Gmail's "Send mail as" (send from
addresses you own, with verification). Gmail *also* offers "Check mail from other
accounts" (POP3 fetch / Gmailify) to pull a live external mailbox into the inbox.
herold imports *into* itself (Takeout + IMAP import) but does not poll external
live accounts — see gap 8.

## Gaps: documented for Gmail, absent from herold

Ordered largest-impact first. Each notes whether the gap is a deliberate
non-goal or simply unbuilt, with the concrete Gmail mechanics from the how-to
pages.

1. **Generative-AI assistance (the largest gap).** Gmail documents a full AI
   suite with no herold analogue:
   - **Help me write** — generate a full draft from a prompt; it pulls context
     from the user's other emails and Drive files (flight times, booking codes)
     to personalise the draft.
   - **Smart Compose** — inline next-phrase autocomplete, Tab to accept.
   - **Smart Reply** — short suggested replies rendered under the message.
   - **Gemini side panel** ("Ask Gemini") — summarise a thread into bullets +
     action items, and answer questions across the mailbox, Drive, and Calendar
     ("when is my flight", "find the reservation"), with `@`-file mentions.

   Herold applies an LLM only to spam and categorisation, and by design exposes
   the prompt it used (`../00-scope.md` G14). None of compose-assist,
   autocomplete, smart reply, thread summarisation, or mailbox Q&A is built or
   specced. Herold is behind on AI capability, ahead on AI transparency.

2. **Group video meetings.** Gmail integrates Google Meet: scheduled meetings,
   group calls, screen share, recording. Herold offers 1:1 WebRTC calls only,
   over the chat WebSocket signalling path — no meeting scheduler, group room,
   screen share, or recording. Scope-limited by design
   (`../requirements/21-video-calls.md`, server `15-video-calls.md`).

3. **Confidential mode.** Gmail disables forward / copy / paste / download /
   print on the message and attachments, sets an expiration date, optionally
   gates access behind an SMS passcode, and lets the sender revoke access early
   via "Remove access" in Sent. Herold has no equivalent; server-side S/MIME and
   PGP are non-goals (`../00-scope.md`), so this is excluded rather than pending.

4. **Read receipts / importance tracking.** Gmail Workspace read receipts and
   the importance markers ("important", high-priority) have no herold surface.
   herold treats Important as advisory display only (`../requirements/01-data-model.md`
   REQ-MODEL-08); there are no receipts. Unbuilt.

5. **Mailbox delegation.** Gmail lets a delegate read, send, and delete on the
   owner's behalf (sent mail attributed to the delegate), up to 10 delegates
   (personal) / 1000 (Workspace); delegates are walled off from password, Gemini,
   Smart Compose, Meet, Tasks, and chat. Herold has attachment / file shares
   (`../requirements/17-attachments.md`, server `25-attachment-shares.md`) but no
   full-mailbox delegation or shared inbox. Unbuilt.

6. **Tasks, and calendar in the Suite.** Gmail's Tasks side panel converts an
   email into a task and supports due dates, subtasks, recurring tasks, and
   multiple task lists. Herold has no Tasks equivalent. Calendar itself is
   specced as phase-2 JMAP (`herold-coverage.md`, server REQ-PROTO-54) but the
   Suite has no calendar view yet — no `CalendarView` or `lib/calendar` in the
   tree. Calendar is Specced-not-built; Tasks is unspecced.

7. **Templates and follow-up nudges.**
   - **Templates (canned responses)** — Gmail saves a compose body as a named
     template (Advanced-settings toggle), inserts it into any compose, and can
     auto-send it via a filter; up to ~50 templates, web-only. (Templates
     how-to page 404'd; detail from adjacent Google docs.)
   - **Nudges** — Gmail surfaces "Received N days ago. Reply?" and "Sent N days
     ago. Follow up?" prompts at the top of the inbox, toggled in settings.

   Neither has a herold equivalent. Unbuilt.

8. **Fetch from other accounts, and offline.**
   - **Check mail from other accounts** — Gmail polls an external POP3 mailbox
     (or Gmailifies another Gmail) into the inbox. herold imports *into* itself
     (Takeout + IMAP import, server `16-import.md`, `19-imap-import.md`) but does
     not poll external live accounts.
   - **Offline** — Gmail (Chrome) syncs 7 / 30 / 90 days of mail locally and lets
     the user read, search, write, reply, label, and delete offline, queuing
     sends to an outbox that flushes on reconnect. herold's Suite runs a service
     worker for push only; there is no offline mail store, so the Suite is
     unusable without connectivity. Unbuilt.

9. **Inbox layouts and storage management.** Gmail offers inbox types beyond the
   category tabs — Important first, Unread first, Starred first, Priority Inbox,
   and Multiple inboxes (custom sections by search query) — plus storage-
   management tooling (find large messages, clean up). herold's category tabs are
   the only inbox-shaping control, and quotas are server-side with no end-user
   cleanup surface. (Inbox-types page 404'd; layout names from general product
   knowledge.) Unbuilt.

10. **Workspace ecosystem integration.** Drive attachments, the Calendar / Keep /
    Tasks side panel, and the third-party add-on marketplace embedded in the
    Gmail UI have no analogue. Herold's plugin system is operator-facing (DNS,
    spam, delivery hooks), a different axis. Non-goal for the mail UI.

## Divergent by design, or herold-ahead

- **LLM transparency (`../00-scope.md` G14).** Herold surfaces the exact
  categoriser / spam prompt and lets the user edit the categoriser prompt; the
  Gmail equivalent is a black box.
- **Standards-native, client-portable.** Herold speaks JMAP/IMAP/SMTP/Sieve to
  any client; Gmail treats IMAP/POP as secondary access paths to a closed
  product.
- **Self-hosted, single-tenant, no ads, no phone-home** (`../00-scope.md` G13).
- **Surfaces Gmail lacks:** emoji reactions on email
  (`../requirements/02-mail-basics.md` Reactions); RFC-clean private unsubscribe
  with no cookies/referrer (`../requirements/14-unsubscribe.md`); the keyboard
  shortcut coach; user-controlled inline-vs-attachment image handling
  (`../00-scope.md` G15/G16); the user-editable inbox-category prompt; managed
  tagged (plus) addresses with per-tag filtering (server `24-tagged-addresses.md`,
  `TaggedAddressesForm`), which Gmail supports only as raw `user+tag@` with no
  management UI; and `from-domain` as a first-class filter condition.

## Maturity caveats

- `../requirements/02-mail-basics.md` is still a placeholder skeleton pending
  capture data from real Gmail-usage traces (`capture-integration.md`); the
  fine-grained fidelity of everyday actions is still being derived.
- Calendar is specced but has no Suite view yet.
- Video is 1:1 only; there is no group-meeting product.

## Bottom line

For the day-to-day mechanics of email, herold is a faithful Gmail clone and in a
few places (unsubscribe privacy, inline-image control, AI transparency, email
reactions, tagged-address management) exceeds it. Two categories of shortfall
remain. **Partial parity** — search operators (~11 of ~30), filters (no body/
size/date conditions, no star/importance/spam/categorize actions), vacation (no
contacts-only), schedule-send (substrate only) — where a Gmail migrant hits
edges on features that read as done. **Absent** — almost entirely Google's cloud
layer: Gemini writing assistance (largest), group Meet, Confidential mode, read
receipts, delegation, Tasks / calendar UI, templates / nudges, fetch-from-other-
accounts, offline, inbox layouts, and the Drive/Calendar/add-on ecosystem. Some
of these are explicit non-goals; the rest are unbuilt rather than excluded.
