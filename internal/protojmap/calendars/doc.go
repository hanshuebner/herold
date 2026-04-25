// Package calendars implements the JMAP Calendars binding (Phase 2
// Wave 2.7 / REQ-PROTO-56) on top of the JSCalendar object model
// (RFC 8984) and the Phase-2 calendar store types added in the same
// wave by the parallel store agent. It is the JMAP-side handler set
// for the Calendar/* and CalendarEvent/* method families plus the
// change-feed-driven iMIP intake worker that materialises inbound
// scheduling messages into the recipient's default calendar.
//
// Capability. The package owns
// protojmap.CapabilityJMAPCalendars (the URI
// "urn:ietf:params:jmap:calendars"). The capability descriptor is the
// empty object on the server-wide axis; per-account knobs are exposed
// through accountCapabilities (max calendars, max events per calendar,
// max blob size) per the JMAP-Calendars binding draft.
//
// Pinned binding-draft revision. The handlers track
// draft-ietf-jmap-calendars-32 (the latest revision the parallel
// agents agree on at Wave 2.7 start). New revisions are integrated
// additively; client capability detection happens through the
// session-descriptor path so a future revision bump is non-breaking
// at the wire.
//
// Layout. The handler types and the iMIP worker live as one Go
// package. The parent prompt left room for a sub-folder per family
// (calendar/, event/, imip/); we collapsed to a single package
// because the JSON wire-shapes share helpers (state-string encoding,
// principal+account validation, ID coercion) and splitting them
// across sub-packages would have meant duplicating those helpers or
// inventing an internal "calendarsutil" package — a grab-bag of the
// kind STANDARDS.md §4 forbids. The contacts sibling package uses
// the same flat layout; this stays consistent with that precedent.
//
// Scope and v1 limitations:
//
//   - Only the Event datatype from RFC 8984 is exposed. Tasks and
//     groups are out of scope; the JSCalendar layer rejects them at
//     parse time.
//   - CalendarEvent/queryChanges always returns
//     "cannotCalculateChanges". Clients re-issue CalendarEvent/query.
//     This matches the contacts and mail wave's choice and the
//     binding draft permits it explicitly.
//   - Recurrence expansion in CalendarEvent/query is bounded by
//     Options.MaxOccurrencesPerExpansion (default 1000) so a
//     pathological RRULE cannot blow up a single request.
//   - Outbound iMIP (REPLY, REFRESH responses) is Phase 3. The
//     intake worker only consumes inbound REQUEST/CANCEL/REPLY/
//     COUNTER and logs+ignores REFRESH.
//
// Standards anchors:
//
//   - RFC 8984 — JSCalendar object model.
//   - RFC 5545 — iCalendar (the structural subset the iMIP worker
//     consumes through internal/protojmap/calendars/jscalendar).
//   - RFC 5546 — iMIP scheduling messages.
//   - draft-ietf-jmap-calendars-32 — JMAP Calendars binding.
//
// Concurrency. Method handlers run on the dispatcher's goroutine and
// call into the store; they take ctx as the first parameter and
// honour cancellation. The iMIP intake worker is a single bounded
// goroutine launched from internal/admin/server.go's lifecycle
// errgroup. No package-level goroutines start at init.
package calendars
