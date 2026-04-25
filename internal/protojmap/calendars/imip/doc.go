// Package imip implements the change-feed-driven iMIP (RFC 5546)
// intake worker for the JMAP Calendars datatype. Inbound iMIP
// messages — the Google/Microsoft/Apple flavoured "you've been
// invited" / "you've been disinvited" / "<participant> said yes"
// emails — arrive on the global change feed as ordinary
// EntityKindEmail Created entries. The worker walks each new
// message's MIME tree for text/calendar parts and applies the
// scheduling METHOD to the recipient's calendar:
//
//   - REQUEST  → InsertCalendarEvent (or sequence-aware update of
//     an existing event with the same UID).
//   - CANCEL   → UpdateCalendarEvent setting status=cancelled.
//     The event is preserved for audit.
//   - REPLY    → UpdateCalendarEvent updating one participant's
//     participationStatus.
//   - COUNTER  → REPLY plus a `counterProposals` extension entry on
//     the stored JSCalendar Event so a future client UI
//     can surface the proposed alternative.
//   - REFRESH  → logged at debug; outbound REFRESH replies are
//     Phase 3.
//
// Mirrors the worker shape established by
// internal/protowebhook/dispatcher.go and
// internal/maildmarc/intake.go: ReadChangeFeedForFTS, persist a
// resume cursor in the cursors table (key "calendars-imip"), swallow
// per-message errors so a single malformed invite never stalls the
// feed.
//
// Concurrency. One bounded goroutine. Run blocks until ctx is
// cancelled and returns nil on graceful shutdown.
package imip
