# New-reply notification while reading a thread

Tracking: issue #118.

Status: design proposal, awaiting maintainer redline.

## Problem

The user is viewing a thread, possibly composing a reply. While they
read, a new reply lands in the same thread. Today nothing surfaces:
the new message is appended to the bottom of the thread silently, the
unread count in the sidebar updates, and the reader has no in-context
signal. The risk is real: the reader sends a reply that ignores
information they would have wanted to address.

Issue #118 explicitly flags this as a design decision rather than a
single fix. This note picks the smallest set of decisions that lets
an implementation start, leaves the harder questions visible, and
lists the alternatives we discarded so the next round of discussion
does not relitigate them.

## Decisions proposed for this note

1. The notification surfaces **as an inline banner inserted into the
   thread reader**, above the new reply but below the older messages
   the user was already looking at.
2. The banner **persists until explicitly dismissed**. It does not
   auto-hide on a timer.
3. The banner **appears whenever a new reply arrives in the currently
   open thread**, irrespective of whether the compose pane is open.
   The compose state is not consulted.
4. The banner is **per-thread**, not global. Other threads accumulate
   unread count silently as today.

Each decision is justified below.

## 1. Surface — inline banner, not toast or bell

A toast over the reading pane wins on visibility but loses on
context: a toast that says "new reply" leaves the user to figure out
where to look. An inline banner anchored above the new reply *is*
the context: "the next message you scroll past is the new one." The
banner can carry a one-line preview (sender + subject snippet) so a
glance suffices to decide whether to interrupt the compose flow.

A bell badge in the global bar is silent and dismissable, but the
reader has to *look* at the bell. The whole point of the
notification is to break the assumption that there are no surprises
in the open thread. The bell carries a stale read-state by design.

## 2. Dismissal — explicit, not timed

If the banner auto-hides, the reader who looked away for thirty
seconds (phone, doorbell, train) loses the signal. The whole point
is that the reader confirms they have seen the new context before
they keep composing. An explicit confirm matches that intent.

Two confirm shapes work:

- **Acknowledge** ("Got it", X) — banner closes, the new reply is
  scrolled into view, focus returns to the compose pane.
- **Open reply** ("Show new reply") — banner closes, the page
  scrolls so the new reply's accordion is at the top of the
  viewport, and the new reply is expanded. Compose pane stays open
  in the bottom drawer.

I propose making "Open reply" the primary action (more useful for
the common case) and "Got it" the secondary text link.

## 3. Trigger — irrespective of compose state

We could narrow the trigger to "only when the compose pane is open"
on the theory that a reader who is *just reading* needs less
intervention than one who is *about to send*. But the cost of the
banner when the compose pane is closed is one quiet acknowledgement
click; the cost of a missed notification when the compose pane
opens *after* the new reply arrives is a wrong-send. The asymmetry
favours always showing the banner; reader-only mode treats it as a
soft "FYI new reply", composer mode treats it as the same banner
plus the obvious "do I still want to send what I have?" question.

## 4. Scope — per-thread

A global "new message" banner would have to fight with the existing
unread-count UI in the sidebar. The thread reader is the only place
where stale context is actively misleading, so the banner stays
scoped there.

## Mockup

```
+----------------------------------------------------+
|   <archive> <trash> <unread> <snooze> ... <back>   |
+----------------------------------------------------+
|  Thread subject: "Re: deploy schedule"             |
|                                                    |
|  + alice@...   "Original message body."   2 days   |
|                                                    |
|  + bob@...     "First reply."             1 day    |
|                                                    |
|  + alice@...   "Your turn."               yesterday|
|                                                    |
|  +----------------------------------------------+  |
|  |  *  New reply from carol@example.test         | <- banner
|  |     "Quick correction: the new deploy time is | (inserted inline,
|  |     16:00 UTC, not 18:00."                    |  pulsing border
|  |                                               |  for 2s, then static)
|  |    [Show new reply]   Got it                  |
|  +----------------------------------------------+  |
|                                                    |
|  + carol@...   (collapsed, will expand on click)   |
|                                                    |
+----------------------------------------------------+
|  +------ Compose drawer (sticky bottom) ---------+ |
|  |  To: alice@...                                | |
|  |  ProseMirror compose body                     | |
|  +-----------------------------------------------+ |
+----------------------------------------------------+
```

## Implementation sketch

Single new component: `ThreadNewReplyBanner.svelte` inserted into
`ThreadReader.svelte` between the last message the reader had seen
before the arrival and the new message itself.

- The EventSource push channel already feeds `Email/changes` into the
  `mail` store; the store knows the currently-open thread id and can
  emit a derived `pendingArrivals: Email[]` array.
- `ThreadReader` reads `pendingArrivals` from the store and renders
  the banner once per arrival.
- "Got it" dismisses by removing the entry from `pendingArrivals`.
- "Show new reply" dismisses, scrolls the new accordion into view,
  expands it, and (if compose is open) gives focus back to the
  compose pane.
- An i18n key per banner string, with en/de pairs.
- Telemetry: a single web-vital-style event when the banner appears
  and another when it is dismissed, so we can later see how often
  users actually look.

No new wire types. No JMAP changes. No new endpoints.

## Out of scope / alternatives discarded

- **System-level Notification API:** the suite is content-blind on
  the wire; pushing a sender/subject preview through the OS
  notification surface would leak content to the OS layer. Punt.
- **Sound:** silent web app. Not adding a sound effect for one
  signal.
- **Counter badge on the thread reader header:** redundant with the
  inline banner, doubles the cognitive surface.
- **Auto-include the new reply quoted in the compose body:** out of
  scope — a separate decision about quoting behaviour.

## Open questions for redline

- "Show new reply" — should it also pause the compose draft auto-save
  while the user reads? Probably no; the draft auto-save is invisible
  and the reader can edit the compose body after reading without
  noticing the save.
- Multiple new replies arriving while the banner is up — collapse
  into one banner ("3 new replies") or stack three banners?
  Recommendation: collapse, with the latest preview shown and a
  "+2 more" hint. Three stacked banners would push the new content
  off-screen.
- Banner colour: a calm signal (light brand-blue background, thin
  border) is enough. Avoid the warning/yellow chrome since this is
  not an error condition.
- Keyboard: should pressing `n` while a banner is up be the
  shortcut for "Show new reply"? Probably yes; the existing
  shortcut grid in `MailView.svelte` is the place to wire it.
