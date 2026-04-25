# Open questions

Items blocking specific requirements. Each one, when resolved, typically updates a single doc.

The Resolved log at the bottom records decisions (with date) so the trail is searchable.

## Open

| # | Question | Affects | Status |
|---|----------|---------|--------|
| 4 | Image proxy: details of the proxy endpoint shape (caching, retries, abuse limits, whether the proxy serves a placeholder on fetch failure or 404) | `requirements/13-nonfunctional.md` REQ-SEC-07, `notes/server-contract.md` § Image proxy | Defer — needs more info, discuss with herold-side image-proxy implementation |
| 5 | Push channel choice: EventSource (RFC 8620 §7) only for v1, or also WebSocket (RFC 8887)? | `architecture/03-sync-and-state.md` | Defer — default EventSource only; revisit if SSE proves inadequate under load |
| 6 | What threshold makes an action a P0 keyboard shortcut? | `requirements/10-keyboard.md` | Open until first capture lands. Default in `notes/capture-integration.md`: count ≥ 10 AND ≥ 50% keyboard. |
| 8 | Multi-account: confirmed out for v1 only, or out forever? | `00-scope.md` NG3 | Revisit later. Tentative: out for v1 only. |
| 10 | Plain-text vs HTML compose default | `requirements/02-mail-basics.md` REQ-MAIL-16 | Open until capture |

## Resolved

### 2026-04-25

- **R1 (was Q1) — Auth flow.** Tabard and herold are deployed at the same origin; herold serves both static assets and the JMAP API. Herold authenticates users (password+TOTP locally, or OIDC redirect to external IdP — herold is a relying party only, not an issuer). Authentication state is an HTTP-only session cookie set by herold on the suite origin. Tabard does not store, read, or transmit auth tokens in JS-accessible storage. Affects `architecture/01-system-overview.md`, `architecture/02-jmap-client.md`, `requirements/13-nonfunctional.md` REQ-SEC-01..03, `notes/server-contract.md`.
- **R2 (was Q2) — Sieve.** Herold supports JMAP Sieve scripts (`urn:ietf:params:jmap:sieve`, RFC 9007). Currently a gap on the herold side (`notes/herold-coverage.md`); herold commits to ManageSieve protocol but not yet to the JMAP datatype. Tabard treats it as committed per Q14. Affects `requirements/04-filters.md` REQ-FLT-22.
- **R3 (was Q3) — Snooze.** Herold implements the snooze contract per `notes/server-contract.md` § Snooze. Herold has committed to this in `requirements/01-protocols.md` REQ-PROTO-49. Affects `requirements/06-snooze.md`.
- **R7 + R11 (was Q7 / Q11) — Categorisation.** Automatic categorisation via LLM running on herold; user-configurable category set and prompt; defaults to Gmail's Primary / Social / Promotions / Updates / Forums. Replaces user-defined groupings of labels. Affects `requirements/05-categorisation.md` (full rewrite), `notes/server-contract.md` (new categorisation capability), `notes/herold-coverage.md` (gap — herold's LLM is currently spam-only).
- **R9 (was Q9) — Recipient autocomplete.** Source: JMAP for Contacts (`urn:ietf:params:jmap:contacts`) as primary, supplemented by a client-local seen-addresses history for addresses the user has corresponded with but not saved as contacts. Affects `requirements/02-mail-basics.md` REQ-MAIL-11.
- **R12 (was Q12) — Settings panel.** Implement per `requirements/20-settings.md`'s scoping: theme, default From, per-Identity signature, image-load defaults, Undo window, mailing-list mute list, vacation responder. Density / custom-shortcut-editor / multi-account cut.
- **R13 (was Q13) — Monorepo tooling.** pnpm with workspaces. `apps/mail`, `apps/calendar`, `apps/contacts` plus shared `packages/*`. Layout lands when the second app starts; flat at root until then. Affects `implementation/01-tech-stack.md`.
- **R14 (was Q14) — tabard-calendar timing.** Herold finishes before tabard implements. Tabard's spec treats herold capabilities as available; gaps in herold's commitment are flagged in `notes/herold-coverage.md` for the herold side to address. Affects `notes/server-contract.md` framing and `notes/herold-coverage.md`.
- **R15 (was Q15) — tabard-contacts timing.** Same as R14.
- **R16 (was Q16) — Cross-app handoff.** Same-origin URLs. Cross-app navigation is plain `<a href>` links; no postMessage between iframes, no shared parent shell. Affects `architecture/01-system-overview.md`.
