# 04 — Simplifications and cuts

What tabard deliberately doesn't do, and why.

This file is the inverse of `../00-scope.md`'s non-goals: those are *categories* that are out, here are the specific *features within in-scope categories* that are simplified or cut.

## Cuts within scope

| Feature area | Full Gmail | Tabard | Why |
|--------------|------------|--------|-----|
| Compose | Up to ∞ stacked compose windows | Cap at 3 | Beyond 3 nobody is composing — they're losing track |
| Compose | Plain-text and HTML; `Help me write`; smart compose | Plain + basic HTML; no AI | NG7 |
| Compose | Schedule send | Cut for v1 | Server-side feature; depends on herold support |
| Compose | Confidential mode | Cut entirely | NG8 |
| Reading | Inline Drive previews | Cut | NG6 |
| Reading | Inline Calendar invites with RSVP | Cut | NG6 |
| Reading | Print to PDF | Cut for v1 | If capture shows real use, add as `REQ-MAIL-7n`; otherwise stays cut |
| Reading | "Smart Reply" suggestion chips | Cut | NG7 |
| Search | "Search chips" (visual filter pills) | Free-text + fielded operators only | Operators are tabard's UI for filters too — keep one input |
| Search | Saved searches as sidebar entries | Cut for v1 | Recent-searches list (REQ-SRC-22) covers most of the value |
| Labels | Label colour palette | Fixed 12-colour palette | Picking from 36 is decision fatigue; 12 is enough |
| Threading | View as conversation OR as individual messages (Gmail toggle) | Conversation view only | Gmail's per-message view is rarely used and adds list complexity |
| Snooze | Per-recipient snooze rules | Cut | Per-thread is enough |
| Filters | Regex matching | Cut for v1 | Sieve has it; tabard's UI exposes substring + wildcard |
| Filters | OR logic between conditions | Cut for v1 | AND-only covers the dominant filter shape; OR adds UI complexity |
| Settings | Category tabs (Primary / Social / Promotions / Updates / Forums) | Cut | NG7-adjacent; user-defined Categorisation in `../requirements/05-categorisation.md` is the alternative if anything |
| Settings | Density (Comfortable / Cosy / Compact) | Single density in v1 | Pick one; revisit if capture shows the user spends time toggling |
| Settings | Themes | Dark default + light toggle | Two themes is enough |
| Identity | Send-as / send-on-behalf-of | Depends on what `Identity` herold exposes | Tabard surfaces what herold returns |

## Architectural simplifications

| Concern | Full version | Tabard | Why |
|---------|--------------|--------|-----|
| Persistence | Service worker + IndexedDB cache (offline mode) | None | NG2 |
| Push | EventSource + WebSocket + Web Push | EventSource only | One channel is enough; multi-device push is herold's problem |
| Multi-account | Account switcher | Single account | NG3 |
| State management | Time-travel debugging, undo history beyond Undo toast | Just enough to support optimistic revert | YAGNI |
| Build | Multi-bundle code splitting, route-based lazy loading | Single bundle in v1 | Bundle stays small (`01-tech-stack.md` < 200 KB target); revisit if it grows |
| i18n | Multi-language strings | English-only at v1 | One user, one language (German UI text is the open option — see `../notes/open-questions.md` if it surfaces) |
| Telemetry | Usage analytics | None | NG9 + we have gmail-logger for behavioural data, in development only |

## Things that look like cuts but aren't

- **Mailbox colour as a custom property** — not a cut from JMAP; this is a tabard requirement on herold (`../notes/server-contract.md`). Documented as a contract, not a hack.
- **Snooze as `$snoozed` keyword + extension property** — same. Server contract, not a fallback.
- **Categorisation as client-side `localStorage`** — that *is* a simplification, but explicit in `../requirements/05-categorisation.md`. Tagged as TBD-promotable to server-side if cross-device sync becomes a need.
