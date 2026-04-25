# 15 — Calendar invites in mail (iMIP)

A message containing a `text/calendar` MIME part with a `METHOD` parameter (REQUEST, CANCEL, REPLY, COUNTER, REFRESH) is an iMIP message (RFC 6047) carrying iCalendar (RFC 5545). Tabard renders the meeting details inline and exposes RSVP actions.

This is **mail rendering**, not calendar management. Calendar management lives in tabard-calendar (`../00-scope.md` § "Tabard is a suite"). When tabard-calendar exists, the actions here hand off to it; until then, tabard-mail talks directly to herold's JMAP for Calendars to apply the user's RSVP and add the event to their calendar.

## Detection

| ID | Requirement |
|----|-------------|
| REQ-CAL-01 | Tabard recognises an iMIP message by the presence of a MIME part with content type `text/calendar` and a `method` parameter. The method values supported: `REQUEST`, `CANCEL`, `REPLY`, `COUNTER`, `REFRESH`. |
| REQ-CAL-02 | The iCalendar part is parsed (RFC 5545). At minimum the following properties are extracted from the first `VEVENT`: `UID`, `SEQUENCE`, `DTSTAMP`, `DTSTART`, `DTEND` (or `DURATION`), `SUMMARY`, `LOCATION`, `DESCRIPTION`, `ORGANIZER`, `ATTENDEE` (multiple), `RRULE`, `STATUS`. |
| REQ-CAL-03 | If parsing fails, tabard renders a single error chip: "This message contains a calendar invitation that couldn't be read" and shows the raw `text/calendar` part as a download. The message continues to render normally. |

## Render

The iMIP card appears inline at the top of the message body, above the prose.

| ID | Requirement |
|----|-------------|
| REQ-CAL-10 | The card shows: summary (event title), formatted date+time range in the user's local timezone, location, organizer name+email, attendee count + the user's PARTSTAT (`NEEDS-ACTION` / `ACCEPTED` / `TENTATIVE` / `DECLINED`). |
| REQ-CAL-11 | If the event timezone differs from the user's, the local-timezone time is primary; the event's original timezone is shown as a tooltip. |
| REQ-CAL-12 | All-day events render without a time component ("Tuesday, 12 May" rather than "Tuesday, 12 May 00:00–23:59"). |
| REQ-CAL-13 | Recurring events render an `RRULE` summary in plain text ("Weekly on Tuesdays", "Monthly on the first", "Every 2 weeks until 31 Dec 2026"). The summariser handles the common patterns (`FREQ`+`INTERVAL`+`BYDAY`+`UNTIL`+`COUNT`); uncommon patterns fall back to "Custom recurrence — see message". |
| REQ-CAL-14 | The full attendee list is collapsed to a count by default ("12 attendees") with a click-to-expand. Each row shows name, email, and PARTSTAT. |
| REQ-CAL-15 | The description (`DESCRIPTION` property) renders below the structured fields. Plain text by default; HTML (if `X-ALT-DESC` carries `FMTTYPE=text/html`) goes through the same sanitiser pipeline as HTML mail body (`../architecture/04-rendering.md`). |

## Method-specific rendering

| Method | What the card looks like |
|--------|--------------------------|
| `REQUEST` | "You're invited" with Accept / Tentative / Decline buttons. The user's existing PARTSTAT is reflected if a prior REQUEST or REPLY was processed. |
| `CANCEL` | "This event was cancelled" — the card is rendered with strike-through styling. Buttons: "Remove from calendar" (if the event is on the user's calendar). |
| `REPLY` | "Alice accepted / declined / tentatively accepted." Card is a status update; no buttons unless the user is the organizer (in which case "View responses" expands the attendee list with current PARTSTATs). |
| `COUNTER` | "Bob proposed a different time." Card shows old vs new time. Buttons (organizer only): "Accept the proposal" / "Keep the original". |
| `REFRESH` | "Bob asked for the latest version of this event." If the user is the organizer, button: "Send the latest version" — re-issues the current REQUEST to the requester. |

## Actions: REQUEST → REPLY

| ID | Requirement |
|----|-------------|
| REQ-CAL-20 | Accept / Tentative / Decline build an iCalendar `REPLY` with the user's PARTSTAT updated, identical UID and SEQUENCE to the REQUEST. |
| REQ-CAL-21 | The REPLY is wrapped as an `text/calendar; method=REPLY` MIME part inside an `multipart/alternative` (with a plain-text human-readable summary as the alternative). |
| REQ-CAL-22 | The REPLY is sent to the organizer's address (`ORGANIZER` property's `mailto:`) via `EmailSubmission/set` (`../requirements/02-mail-basics.md` REQ-MAIL-14 path). |
| REQ-CAL-23 | The user's local copy of the event is updated immediately (optimistic; see below for the calendar handoff). |
| REQ-CAL-24 | "Decline" without sending a REPLY (some calendars expose this) is supported via a secondary "Decline silently" option in the action menu — does not send the REPLY, but does record the decline locally. |

## Calendar handoff

| ID | Requirement |
|----|-------------|
| REQ-CAL-30 | When the user accepts (or accepts-tentative) a REQUEST, the event is also added to their calendar. |
| REQ-CAL-31 | While tabard-calendar does not exist (`../00-scope.md` § "Tabard is a suite"), tabard-mail talks to herold's JMAP for Calendars directly to add the event. The capability `urn:ietf:params:jmap:calendars` must be advertised by herold; without it, RSVP buttons send the REPLY but do not write to a calendar (a footer note explains). |
| REQ-CAL-32 | When tabard-calendar exists, tabard-mail hands the event off via the suite's cross-app mechanism (`../notes/open-questions.md` Q16). The mail app does not directly call calendar JMAP methods. |
| REQ-CAL-33 | A CANCEL processed against an event on the user's calendar removes the event (via tabard-calendar when it exists, directly via JMAP for Calendars while it doesn't). |

## Conflict awareness

| ID | Requirement |
|----|-------------|
| REQ-CAL-40 | Before showing the action buttons, tabard checks for conflicts with existing events overlapping `DTSTART`–`DTEND` on the user's calendar. If a conflict exists, the card shows "Conflicts with: <other event title>" between the time and the buttons. |
| REQ-CAL-41 | A conflict does not block Accept; it informs the decision. |
| REQ-CAL-42 | Conflict checking depends on `urn:ietf:params:jmap:calendars`; without it, no conflict detection. |

## Series vs single-occurrence

| ID | Requirement |
|----|-------------|
| REQ-CAL-50 | When the iMIP REQUEST refers to a single occurrence of a recurring event (uses `RECURRENCE-ID`), tabard's RSVP buttons act on that occurrence only; the rest of the series remains untouched. |
| REQ-CAL-51 | When the iMIP REQUEST is for the whole series (no `RECURRENCE-ID`), the RSVP applies to every occurrence; the card explicitly says "RSVP applies to the entire series". |

## Out of scope (in tabard-mail)

- Creating new events from scratch. That's tabard-calendar.
- Managing existing events the user organised (rescheduling, sending updates, cancelling). That's tabard-calendar.
- Free/busy queries beyond the basic conflict check. That's tabard-calendar.
- Inviting attendees to a new event from compose. Could be added as a small "Insert calendar invite" affordance in compose; cut for v1 — tabard-calendar will own the create-and-invite flow when it exists.
