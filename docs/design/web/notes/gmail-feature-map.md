# Gmail feature map

Reference inventory of the Gmail features this client considers, with their disposition. Analogous to herold's `notes/stalwart-feature-map.md`.

This is not a requirement doc; it is a single-page index of "did we think about that?". Definitive scope is `../00-scope.md`.

## In v1 (capture-driven enrichment expected)

| Gmail feature | Disposition | Where specified |
|---------------|-------------|-----------------|
| Inbox / Sent / Drafts / Spam / Trash / All Mail / Archive | In | `../requirements/01-data-model.md` (system mailboxes) |
| Read mail (thread view, message accordion) | In | `../requirements/02-mail-basics.md`, `../requirements/09-ui-layout.md` |
| Compose / reply / reply-all / forward | In | `../requirements/02-mail-basics.md` |
| Star / unstar | In | `../requirements/02-mail-basics.md` |
| Mark read / unread | In | `../requirements/02-mail-basics.md` |
| Archive | In | `../requirements/02-mail-basics.md` |
| Delete (move to Trash) | In | `../requirements/02-mail-basics.md` |
| Labels — CRUD, nested | In | `../requirements/03-labels.md` |
| Apply / remove labels on threads | In | `../requirements/03-labels.md` |
| Filters (rules) | In, server-gated | `../requirements/04-filters.md` |
| Snooze | In, server-gated | `../requirements/06-snooze.md` |
| Search — free text | In | `../requirements/07-search.md` |
| Search — fielded operators | In | `../requirements/07-search.md` |
| Keyboard shortcuts (Gmail-compatible set) | In | `../requirements/10-keyboard.md` |
| Multiple compose windows | In | `../requirements/02-mail-basics.md`, `../requirements/09-ui-layout.md` |
| Attachments (upload + display) | In | `../requirements/02-mail-basics.md` |
| Inline images, blocked-by-default | In | `../requirements/09-ui-layout.md`, `../requirements/13-nonfunctional.md` |
| Multiple From identities | In | `../requirements/01-data-model.md` |
| Drafts auto-save | In | `../requirements/02-mail-basics.md` |
| Undo send (5 s window) | In | `../requirements/11-optimistic-ui.md` |

## Capture-driven (verdict pending)

| Gmail feature | Disposition | Notes |
|---------------|-------------|-------|
| Categorisation (user-defined groupings of labels) | TBD | Cut if capture shows no use. `../requirements/05-categorisation.md` |
| Chat / Spaces / DMs | TBD | Cut if capture shows < 5 visits over 5 days. `../requirements/08-chat.md` |
| Mark as Important | TBD | Likely cut to "advisory display only" — `../requirements/01-data-model.md` REQ-MODEL-08 |
| Mute thread | TBD | Add as `REQ-MAIL-7x` if capture shows ≥ 5 occurrences |
| Print thread | TBD | Add only if capture shows real use |
| Report spam / not spam | TBD | Likely in if capture shows ≥ 5 occurrences |
| Saved searches | TBD | Possibly worth `REQ-SRC-30` |
| Vacation responder | TBD | Server-side feature; the suite would only need a settings UI |
| Signature (per-Identity) | TBD | Settings-panel candidate |
| Send as / send-on-behalf-of | TBD | Depends on per-`Identity` capabilities herold exposes |

## Out (`../00-scope.md` NG-x)

| Gmail feature | Why out |
|---------------|---------|
| Calendar | NG6 |
| Meet / video | NG6 |
| Drive previews / attachment-from-Drive | NG6 |
| Tasks | NG6 |
| Smart Compose, Smart Reply, "Help me write", AI summaries | NG7 |
| Confidential Mode | NG8 |
| Scheduled send | NG8 (server-side; herold may grow it independently) |
| Multi-account | NG3 |
| Delegated / shared inbox | NG4 |
| S/MIME, PGP, Confidential Mode | NG5 / NG8 |
| Mobile-native iOS / Android | NG1 |
| Offline mode | NG2 |
