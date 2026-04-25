# Open questions

Items blocking specific requirements. Each one, when resolved, typically updates a single doc.

| # | Question | Affects | Status |
|---|----------|---------|--------|
| 1 | Auth flow: interactive password form, or OIDC redirect through herold? | `requirements/13-nonfunctional.md` REQ-SEC, `architecture/02-jmap-client.md` | Open |
| 2 | Does herold expose `urn:ietf:params:jmap:sieve` (RFC 9007)? Implementation phase: which? | `requirements/04-filters.md`, `notes/server-contract.md` | Open — depends on herold phasing |
| 3 | Snooze contract: confirmation that herold will accept the `$snoozed` keyword + `snoozedUntil` extension property and run the wake-up timer | `requirements/06-snooze.md`, `notes/server-contract.md` | Open — needs herold-side commit |
| 4 | Image proxy: does herold expose a proxy endpoint for inline images, or do we surface the originals (and accept the tracking risk)? | `requirements/13-nonfunctional.md` REQ-SEC-07 | Open |
| 5 | Push channel choice: EventSource (RFC 8620 §7) only for v1, or also WebSocket (RFC 8887)? | `architecture/03-sync-and-state.md` | Default: EventSource only. Revisit if SSE proves inadequate. |
| 6 | What threshold makes an action a P0 keyboard shortcut? Default in `notes/capture-integration.md` is "count ≥ 10 AND ≥ 50% keyboard"; tune after first capture | `requirements/10-keyboard.md` | Open until first capture lands |
| 7 | "Categorisation" semantics: kept as user-defined label groups, or cut entirely if capture shows no engagement? | `requirements/05-categorisation.md` | Open until capture lands |
| 8 | Multi-account: confirmed out for v1 only, or out forever? | `00-scope.md` NG3 | Tentative: out for v1 only. Revisit after v1. |
| 9 | Recipient autocomplete source: a contacts API on herold (not currently planned), client-side history of seen From/To addresses, or both? | `requirements/02-mail-basics.md` REQ-MAIL-11 | Open |
| 10 | Plain-text vs HTML compose default | `requirements/02-mail-basics.md` REQ-MAIL-16 | Open until capture |
| 11 | Categories: does Gmail's automatic Promotions/Social/Updates tabbing map to anything tabard wants to expose, or is it ignored entirely? | `00-scope.md` NG7, `requirements/05-categorisation.md` | Tentative: ignored. Confirm. |
| 12 | Settings panel scope for v1 (theme, density, Undo window, default From, signature) — or defer entirely to v2? | TBD | Open |
