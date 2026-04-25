// Package jscalendar implements the JSCalendar object model (RFC 8984)
// plus a minimal RFC 5545 iCalendar parser scoped to iMIP intake
// (RFC 5546). It is the data-model + serializer layer underneath the
// JMAP Calendars binding (Wave 2.7); the JMAP handlers and the store
// schema live in sibling packages and call into here.
//
// Scope and v1 limitations:
//
//   - Datatype coverage. RFC 8984 defines three top-level objects:
//     Event, Task, and Group. v1 implements Event only. Task and Group
//     are out of scope until we have a JMAP Tasks story; attempting to
//     unmarshal one returns an error from Validate.
//
//   - Recurrence. Expand handles the RRULE shapes we observe on the
//     real corpus: yearly, monthly, weekly, daily with Interval,
//     Count/Until, ByMonth, ByMonthDay, ByDay (incl. ordinal weekday
//     selectors like the "last Friday" pattern). BYSETPOS is partially
//     applied (we narrow ByDay-derived day sets per period; pathological
//     interactions with ByMonthDay are deferred). Sub-daily frequencies
//     (hourly/minutely/secondly) and BYHOUR/BYMINUTE/BYSECOND expansion
//     are accepted at parse time but Expand returns the deferred-shape
//     error rather than wrong results — see TODO(phase3) markers in
//     recurrence.go.
//
//   - iCalendar parsing. icalendar.go is the bare minimum to convert
//     iMIP REQUEST/CANCEL/REPLY/COUNTER messages from Google,
//     Microsoft, and Apple senders into JSCalendar Events. Not
//     supported in v1: VTODO, VJOURNAL, VFREEBUSY, VTIMEZONE
//     (we do not host calendar objects we cannot index), VALARM
//     (Alerts map only on outbound, Phase 3), and per-occurrence
//     overrides via separate VEVENTs sharing a UID + RECURRENCE-ID.
//     Unknown properties round-trip through VEvent.Other and into the
//     JSCalendar Event RawJSON.
//
//   - Windows time-zone identifiers. Microsoft Exchange / Outlook ship
//     iMIP with non-IANA TZID values (Pacific Standard Time, Central
//     European Standard Time, ...). We carry a static map of the most
//     common ~20 zones to their IANA equivalents; unmapped values fall
//     back to UTC and emit a slog.Warn so operators can spot novel
//     senders.
//
// Hybrid wire model. Frequently-queried fields are typed Go-natively
// so the store can populate denormalised columns without re-parsing
// the blob; everything else round-trips through RawJSON. The pattern
// matches internal/protojmap/contacts (Wave 2.6).
//
// Concurrency: pure-CPU helpers (Marshal, Unmarshal, Expand, ParseICS)
// do not take a context.Context — same posture as mailparse. Callers
// invoke them inside a request's lifecycle and rely on the surrounding
// handler's deadline.
//
// Standards anchors:
//
//   - RFC 8984 — JSCalendar object model.
//   - RFC 5545 — iCalendar (the structural subset we parse).
//   - RFC 5546 — iMIP scheduling messages.
//   - RFC 8984 §A.1 — JSCalendar to iCalendar conversion (the bridge).
package jscalendar
