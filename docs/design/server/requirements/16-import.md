# 16 — Import from Gmail (Google Takeout)

*(Added 2026-05-08: Gmail Takeout import promoted to phase-2 scope after the
maintainer's own takeout drove the requirement. A non-trivial fraction of
plausible herold adopters are migrating from Gmail; without an importer the
"start using herold" path is a forwarding rule and a slow drip of new mail.
The importer accepts the artefacts Google produces — a tar/zip of an mbox
plus a small set of JSON setting files — and lands the user in a state
indistinguishable, modulo Gmail-specific features that have no herold
equivalent, from where they were on Google's side.)*

## Scope

Herold ingests **Google Takeout** archives produced by Google's data-export
tool when the user requests "Mail" data. A single archive contains:

- An **mbox** file with every message in the user's account (Inbox, All Mail,
  Sent, Drafts, Spam, Trash). Format is mboxrd with Gmail-specific headers
  (`X-Gmail-Labels`, `X-GM-THRID`).
- A **settings** directory with a small set of JSON files: filters, blocked
  addresses, forwarding addresses, delegated send-as addresses. Schemas are
  stable across locales; the *file names* and label *values* inside are not.

The importer does **not** cover Calendar or Contacts takeouts. JMAP for
Calendars / Contacts (NG3, phase 2) gets its own importer when those features
land; the file is reserved for the phase that ships them.

## Inputs

| ID | Requirement |
|----|-------------|
| REQ-IMPORT-01 | Accepted input forms: a single `.tgz` / `.tar.gz` / `.zip` archive as produced by Google Takeout, **or** a directory tree with the same layout (for operators who pre-extracted). The importer auto-detects by sniffing the first bytes; no `--format` flag. |
| REQ-IMPORT-02 | The archive layout produced by Takeout is `Takeout/Gmail/<localized-mbox-name>.mbox` plus `Takeout/Gmail/<localized-settings-dir>/*.json`. The importer locates these by **content shape, not file name** (the names are localized — see REQ-IMPORT-10). |
| REQ-IMPORT-03 | Maximum supported archive size: at least 50 GiB on disk. The importer streams the mbox; it MUST NOT load the full mbox into memory or require 50 GiB of free temp space. |
| REQ-IMPORT-04 | The importer is **resumable**. State (last-imported `Message-ID` + byte offset within the mbox) is checkpointed every N messages (default 200) into a per-job row in the metadata store. Re-running the same job ID continues; re-running with a different ID starts over. |
| REQ-IMPORT-05 | The importer is **idempotent at the message level**. Messages already present in the target mailbox (matched by `Message-ID` + RFC 822 content hash) are skipped and counted, never duplicated. A second run of the same archive is a no-op. |
| REQ-IMPORT-06 | The importer targets a **single principal**. Multi-principal imports are N independent jobs; there is no batch mode. The CLI / REST surface accepts `--principal <email>` (REQ-ADM-15x). |
| REQ-IMPORT-07 | The importer never writes to `system.toml`. Domains / principals must already exist; if the target principal is missing the job fails fast with a clear error pointing the operator at `herold admin principals create`. |
| REQ-IMPORT-08 | The archive may contain a partial export (Gmail allows date-range and label-filtered takeouts). The importer accepts whatever subset it finds; missing settings files are not an error, missing mbox is not an error (settings-only import is supported). |

## Localization (load-bearing)

Gmail Takeout is **fully localized in the user's Google UI language**. For
the user whose takeout drove this requirement (`de-DE`) the archive contains:

- Settings directory `Nutzereinstellungen/`, not `User Settings/`.
- Settings files `Blockierte Adressen.json`, `Filter.json`,
  `Weiterleitungsadressen.json`, `Delegierte Absenderadressen.json`.
- Mbox named e.g. `Alle E-Mails einschliesslich Spam-Nachrichten und E.mbox`
  (truncated by Google at ~50 chars).
- `X-Gmail-Labels` values like `Posteingang` (Inbox), `Wichtig` (Important),
  `Geoeffnet` (Opened), `Markiert` (Starred), `Ungelesen` (Unread), `Spam`,
  `Papierkorb` (Trash), `Entwurf` (Draft), `Gesendet` (Sent),
  `Kategorie: "Persoenlich"` (Category: Personal),
  `Kategorie: "Neuigkeiten"` (Category: Updates),
  `Kategorie: "Soziale Medien"` (Category: Social).

The importer MUST handle this without operator intervention.

| ID | Requirement |
|----|-------------|
| REQ-IMPORT-10 | The importer MUST process Takeout archives in **every Gmail UI locale**. The set of supported locales is the set Google ships at the time of a given herold release (currently 40+ languages); a takeout in any of them imports without an operator-supplied hint. |
| REQ-IMPORT-11 | The importer maps Gmail **system labels** to herold's canonical concepts via a built-in **locale label table** (`internal/import/gmail/labels.go` — name TBD by `storage-implementor` / new `import-implementor`). The table is a closed enum keyed by `(locale, gmail-system-concept) -> localized-string`, where `gmail-system-concept` covers at least: Inbox, Sent, Drafts, Spam, Trash, All Mail, Starred, Important, Unread, Opened/Read, Snoozed, Chats, Scheduled, the five default categories (Primary/Social/Promotions/Updates/Forums), and the user-defined "archived" pseudo-state (i.e. "in All Mail but not in Inbox"). |
| REQ-IMPORT-12 | Lookup direction is **localized -> canonical**. Given an `X-Gmail-Labels` value, the importer normalises by stripping ASCII transliteration / case differences, looking up against the table, and falling through to "treat as user-defined label" if no match. False-positive collisions with user-defined labels are explicitly accepted (a user who created a label named exactly `Posteingang` in an English Gmail account is an edge case we do not detect; the canonical mapping wins). |
| REQ-IMPORT-13 | The locale table covers **at minimum**: `en` (US/UK), `de` (DE/AT/CH), `fr` (FR/BE/CA/CH), `es` (ES/MX/AR), `it`, `nl`, `pt` (PT/BR), `pl`, `cs`, `sv`, `da`, `no`, `fi`, `ja`, `ko`, `zh-CN`, `zh-TW`, `ru`, `tr`, `el`, `he`, `ar`, `hi`. Additional locales added without breaking existing mappings — the table is data, not code paths. |
| REQ-IMPORT-14 | Locale **detection** is automatic. Heuristic: (a) sniff `<settings-dir>` and mbox file basename against the per-locale strings table; (b) fall back to scanning the first ~1000 `X-Gmail-Labels` values and picking the locale whose system-label set has the highest match count; (c) fall back to `en` and treat unknown labels as user-defined. The CLI accepts `--locale <tag>` to override; the REST surface accepts `locale` in the job body. |
| REQ-IMPORT-15 | The locale label table is **tested with real Takeout fixtures** — at minimum one fixture per top-tier locale (`en-US`, `de-DE`, `fr-FR`, `ja-JP`, `zh-CN`) under `internal/import/gmail/testdata/`. Fixtures are minimal mboxes (a handful of messages exercising each system label); they are checked in. Adding a new locale to REQ-IMPORT-13 requires adding a fixture. |
| REQ-IMPORT-16 | Settings-file detection is **content-shape-based**, not name-based. A JSON file containing `{"filter": [...]}` at the top level is the filters file regardless of name; `{"addresses": [...]}` paired with a sibling directory containing the marker is identified by the heuristics in REQ-IMPORT-50/51. The locale-string list in REQ-IMPORT-13 is a tiebreaker, not the primary key. |
| REQ-IMPORT-17 | The mbox file is identified by extension (`.mbox`) and content (the `From ` envelope-from line at offset 0). Localized basenames are not consulted. Multiple `.mbox` files in the archive are concatenated in lexical order with a warning. |

## Mail import (mbox -> store)

| ID | Requirement |
|----|-------------|
| REQ-IMPORT-20 | The mbox is parsed with a streaming mboxrd parser (existing `internal/mailparse` extended; or a new package `internal/import/mbox`). The parser handles `>From `-line escaping, multi-gigabyte messages, and CRLF/LF line endings. It is fuzzed (REQ standard, `STANDARDS.md` §8.2). |
| REQ-IMPORT-21 | Each message is delivered through the **same store path used by inbound SMTP** — `store.InsertMessage` — except classification and Sieve are **bypassed**. The importer is post-classification: messages already have their Gmail-decided fate, the importer respects it, and the user can re-classify selected messages later (REQ-FILT-220 / re-classify action). |
| REQ-IMPORT-22 | `X-Gmail-Labels` is split on `,` (Gmail's separator), each token decoded if RFC 2047-encoded, then mapped per REQ-IMPORT-11/12. Result is a typed structure `{folder: enum, keywords: [...], userLabels: [...], category: enum?}` consumed by the store-side translator. |
| REQ-IMPORT-23 | Mapping rules — herold equivalents for Gmail system labels: |
| | - `Inbox` -> message lands in INBOX (Mailbox role `Inbox`). |
| | - `Sent` -> Mailbox role `Sent`; keyword `\Sent` (RFC 8457). |
| | - `Drafts` -> Mailbox role `Drafts`; keyword `\Draft`. |
| | - `Spam` -> Mailbox role `Junk`; keyword `\Junk`. |
| | - `Trash` -> Mailbox role `Trash`. |
| | - `Starred` -> keyword `\Flagged`. |
| | - `Important` -> keyword `\Important` (a herold extension keyword; documented in `web/requirements/03-labels.md` if not already). |
| | - `Unread` -> keyword `\Seen` is **not** set. (Default state in Gmail's mbox is "read"; `Unread` is the explicit unread marker.) |
| | - `Opened` / locale-equivalent -> keyword `\Seen` is set. (Inverse of Unread; redundant with absence-of-Unread but emitted explicitly by Gmail.) |
| | - `Category: <Primary\|Social\|Promotions\|Updates\|Forums>` -> the matching `$category-*` keyword from REQ-FILT-201. The five Gmail categories map 1:1 to the five default categories herold ships. |
| | - User-defined Gmail labels -> herold `Mailbox` of role `Label`, created on first sight, name preserved verbatim. Nested labels (Gmail's `Parent/Child`) become parent/child mailboxes via `Mailbox.parentId`. |
| REQ-IMPORT-24 | A message with both `Inbox` and a user-defined label is **filed in INBOX** with the user-defined label *also* attached (Gmail's "labels are tags" semantics). A message with no `Inbox` and no system folder is filed in **All Mail equivalent** — herold has no separate All Mail mailbox; "in the store but not in any role mailbox or user label" is the canonical archive state. |
| REQ-IMPORT-25 | `X-GM-THRID` (Gmail thread ID) is preserved as `Email.gmailThreadId` (a herold extension property; if not yet defined in `web/architecture/02-jmap-client.md`, the importer's REQ stays in this doc and the model owner adds the property). The store's existing `threadId` is **not** overwritten by `X-GM-THRID`; threading is recomputed from `References` / `In-Reply-To` per herold's normal rules. The Gmail thread ID is preserved as a tiebreaker for users who want to query "what Gmail considered one thread" later. |
| REQ-IMPORT-26 | `Date:` of each message is preserved as the canonical `receivedAt`. The mbox `From ` envelope timestamp is **not** used (Google sets it to the message-stored time, which is identical to `Date:` for inbound and to the send time for outbound — we trust the header). |
| REQ-IMPORT-27 | Internal date for IMAP (`INTERNALDATE`) is set to the mbox `From ` envelope timestamp when present, falling back to `Date:`. This preserves the relative ordering Gmail clients saw. |
| REQ-IMPORT-28 | Quota: imported messages **do** count against the principal's quota (REQ-STORE-quota). If the import would exceed quota the job pauses with status `quota-exceeded`; the operator either raises the quota or aborts. Partial imports are not silently truncated. |
| REQ-IMPORT-29 | FTS indexing happens **asynchronously** after each batch — the importer enqueues messages onto the existing FTS worker queue rather than indexing inline. A 50 GiB import that triggers the FTS backlog is acceptable; the user gets searchable mail "soon" rather than blocking the import on indexing. |
| REQ-IMPORT-30 | Attachments are stored in the existing blob store with the existing dedup (REQ-STORE-30..). A 50 GiB takeout that contains the same attachment 200 times stores it once. |
| REQ-IMPORT-31 | The importer does **not** re-sign DKIM or rewrite headers. Imported messages keep their original headers verbatim. They are flagged `Email.imported = true` so JMAP clients and the suite can distinguish "this came from a takeout, not from current SMTP" — useful for explaining odd `Authentication-Results` (the original Gmail one is preserved). |
| REQ-IMPORT-32 | Outbound-equivalent messages (in Gmail's `Sent`) are **not** queued for re-delivery. They go straight to the principal's Sent mailbox. Resending them on import is a footgun. |
| REQ-IMPORT-33 | Drafts (`Drafts` system label) are imported into the Drafts mailbox with `\Draft` keyword. JMAP `Email.keywords` and IMAP `\Draft` flag align. |
| REQ-IMPORT-34 | Per-message progress is logged at `info` (subsystem-scoped) every N messages (default 1000). Per-batch metrics: `herold_import_messages_total{status="imported|skipped-dup|skipped-error"}`, `herold_import_bytes_total`, `herold_import_attachments_dedup_ratio`, `herold_import_duration_seconds`. |
| REQ-IMPORT-35 | A per-message error (parse failure, oversize, malformed MIME) does **not** abort the job. The message is recorded in the job's error list (with byte offset + first 200 chars of the From-line) and skipped; the job continues. The error list is exposed at the end of the job and via `/api/v1/import/jobs/{id}/errors`. |

## Settings import

| ID | Requirement |
|----|-------------|
| REQ-IMPORT-40 | Settings files are imported **after** mail (so user-label translations are stable). Settings import is its own checkpointed phase; failures here do not invalidate the mail import. |
| REQ-IMPORT-41 | `Filter.json` (Gmail filters) translates to **a single appended Sieve script** named `imported-from-gmail` on the principal's Sieve script list, marked `active = false` initially so the user reviews before activating. The translator: |
| | - Maps each Gmail filter (`{query, labelsToAdd, labelsToRemove}`) to one Sieve `if`-block. |
| | - Translates `query` from Gmail search syntax to Sieve tests using the existing search-syntax translator (or a new minimal one — supports `from:`, `to:`, `subject:`, `list:`, `has:attachment`, free-text). Untranslatable predicates emit a comment and a best-effort match (`anyof` over header tests). |
| | - `labelsToAdd` -> Sieve `addflag` for system keywords; `imapflags` / `fileinto` for folders; for user-defined labels, `fileinto :flags` into the matching herold Mailbox of role `Label`. |
| | - `labelsToRemove` -> Sieve `removeflag` or, for `Inbox`, the canonical archive idiom (`fileinto "Archive"` is wrong; herold's idiom is to file the mail wherever the user-label sends it and **not** add INBOX; the translator's job is to emit code with that effect). |
| REQ-IMPORT-42 | The translated Sieve script preserves a header comment for each rule citing the original Gmail query verbatim, so the user can audit. The full original `Filter.json` is also stored as an admin-readable artefact on the import job for forensics. |
| REQ-IMPORT-43 | Filters that reference labels not present in the mbox import are still translated; the target Mailbox is created on demand at Sieve **execution** time per herold's existing auto-create-on-fileinto rule. Translation does not need the label to exist yet. |
| REQ-IMPORT-44 | `Blockierte Adressen.json` (Gmail blocked-senders list) translates to a per-principal **block list** consumed by herold's existing block-sender mechanism (REQ-FILT-Sieve in 06-filtering.md if defined; otherwise, append to the imported Sieve script as `if address :is "from" "<addr>" { discard; stop; }`). The list also surfaces in the Suite settings UI under the existing block-sender management view. |
| REQ-IMPORT-45 | `Weiterleitungsadressen.json` (Gmail forwarding addresses) is **imported as data, not as policy**. The list is recorded on the import job for the operator's review. The importer does **not** automatically activate forwarding (REQ-IMPORT-91, principle of least surprise). The Suite settings UI (REQ-SET-IMPORT-3, see `web/requirements/20-settings.md`) offers a one-click "create forwarding rule for these" affordance backed by Sieve `redirect`. |
| REQ-IMPORT-46 | `Delegierte Absenderadressen.json` (Gmail's "send mail as" addresses) translates to additional `Identity` rows on the principal — one per address, with `name` left blank for the user to fill in. Each new identity is created with `mayDelete = true` and disabled by default (`Identity.<some-flag>`); the user activates per-identity in settings. SMTP/SASL credentials for the external account are **not** imported (Gmail does not export them); identities pointing at external accounts the user cannot send from will fail at compose time with a clear message. |
| REQ-IMPORT-47 | Imported settings files are also archived **verbatim** in the job's artefact store (under `import-jobs/<job-id>/raw/`) for 30 days, so the operator can re-translate after a translator bug fix without asking the user for a new takeout. After 30 days they are deleted; the import job summary is retained per audit-log retention (REQ-STORE-82). |

## Operator surface

| ID | Requirement |
|----|-------------|
| REQ-IMPORT-60 | CLI: `herold import gmail --principal <email> [--archive <path>] [--directory <path>] [--locale <tag>] [--dry-run] [--resume <job-id>] [--no-settings] [--no-mail]`. Either `--archive` or `--directory` required (mutually exclusive). |
| REQ-IMPORT-61 | `--dry-run` parses the archive, runs the locale detector and the translators, and prints a **plan**: number of messages by destination mailbox, number of new mailboxes that would be created, number of filters / blocked addresses / forwards / identities, plus the first 20 untranslatable filters. Writes nothing. |
| REQ-IMPORT-62 | REST: `POST /api/v1/import/jobs` with body `{principal, source: {archive_path|directory_path|upload_id}, locale?, dry_run?, no_settings?, no_mail?}` returns `{job_id, status: "queued"}`. Admin scope required (REQ-AUTH-SCOPE-*). Public listener does **not** expose this; end-user-driven imports go through the self-service surface (REQ-IMPORT-70). |
| REQ-IMPORT-63 | `GET /api/v1/import/jobs/{id}` returns the job state machine (`queued`, `analyzing`, `importing-mail`, `importing-settings`, `paused-quota-exceeded`, `failed`, `completed`) plus counters and the first / last error. `GET /api/v1/import/jobs/{id}/errors` paginates the per-message error list. `POST /api/v1/import/jobs/{id}/cancel` aborts; aborted jobs leave already-imported messages in place (no rollback). |
| REQ-IMPORT-64 | The job runs in the existing background-worker pool. Concurrency limit: one import per principal at a time (a second `POST` returns 409 `import-already-running`). Server-wide concurrency capped to a small N (default 2) so a multi-mailbox migration doesn't flatten the FTS / Sieve workers. |
| REQ-IMPORT-65 | Import jobs are recorded in the audit log (REQ-ADM-300): the actor, target principal, archive size + sha256, plus the final job summary. The raw archive is **not** retained beyond REQ-IMPORT-47's 30-day window. |

## End-user surface (Suite settings)

End-user-facing UX requirements live in `web/requirements/20-settings.md`
under "Import from Gmail" (REQ-SET-IMPORT-1..). The server contract is here:

| ID | Requirement |
|----|-------------|
| REQ-IMPORT-70 | The Suite's Settings panel exposes a self-service entry. The flow uploads the takeout archive to a per-session blob (`Blob/upload`-style), creates an import job via the **public-listener** REST surface, and polls the job state. Auth scope is `user` (REQ-AUTH-SCOPE-USER) — a user can only import into their own principal; the surface rejects `principal` mismatches with 403. |
| REQ-IMPORT-71 | Upload size limit on the public listener: 20 GiB by default, operator-configurable (`appconfig: import.max_archive_size_bytes`). Larger imports require the operator to use the CLI / admin REST. The limit is advertised in the Suite UI before the user picks a file. |
| REQ-IMPORT-72 | Uploaded archive blobs are scoped to the import job and deleted on completion / cancel / 7-day timeout. They are not addressable as message blobs and never appear on the JMAP `Blob/get` surface for normal mail traffic. |
| REQ-IMPORT-73 | The Suite UI MUST show the dry-run preview (REQ-IMPORT-61 equivalent) before the user confirms. The user reviews "X messages, Y mailboxes will be created, Z filters" and explicitly clicks "Import". |
| REQ-IMPORT-74 | Users CANNOT activate forwarding rules from the import UI directly — the imported forwarding addresses are surfaced as a list with a separate "Create forwarding rules" button that hands off to the existing forwarding settings UI. Same principle as REQ-IMPORT-45: imported forwarding is data, not auto-policy. |

## Failure modes and security

| ID | Requirement |
|----|-------------|
| REQ-IMPORT-80 | The importer parses untrusted input. The mbox parser, JSON parsers, and Sieve translator are all in-scope for `security-reviewer` review and `conformance-fuzz-engineer` fuzzing per `STANDARDS.md` §8.2 / §11. Specifically: mbox-parser fuzz target, filter-translator fuzz target, locale-detector fuzz target. |
| REQ-IMPORT-81 | Memory budget: peak resident-set during import bounded to the operator-configured `import.max_memory_bytes` (default 512 MiB). Streaming buffers, attachment temp files, and translator state all sum into this budget; exceeding it pauses the job with `failed: out-of-memory` rather than OOM-killing the server. |
| REQ-IMPORT-82 | Disk budget: the importer never uses temp space larger than (largest single message size) + (current FTS batch). 50 GiB of mbox does not require 50 GiB of free temp. |
| REQ-IMPORT-83 | Path traversal / archive-bomb defence: tar / zip extraction sanitises paths (no `..`, no absolute paths, no symlinks). A zip-bomb-style archive is detected by tracking the cumulative-extracted-size vs compressed-input-size ratio and aborting past a configurable threshold (default 100x). |
| REQ-IMPORT-84 | Secrets in headers: the importer logs first ~200 chars of header context on parse errors. The slog field allow-list (`STANDARDS.md` §7) MUST include `import_message_offset`, `import_message_id`, `import_failure_reason` and exclude raw header bodies past those 200 chars. |
| REQ-IMPORT-85 | A failed mid-import job leaves the principal in a half-imported state by design (no rollback). The job's `--resume` path picks up at the last checkpoint; an operator preferring "all or nothing" runs the import into a fresh principal first and folds it in via a follow-up tool (out of scope here). |
| REQ-IMPORT-86 | Audit log entries (REQ-ADM-300) for import jobs are retained per the audit-log retention policy. The raw upload is purged per REQ-IMPORT-72; the **summary** (counts, errors, decisions made by translators) is retained indefinitely so post-hoc questions ("did my Gmail filter X really translate to that Sieve rule?") are answerable. |

## Cut for v1, deferred

| Feature | Reason |
|---------|--------|
| Gmail Calendar takeout import | Comes with JMAP-Calendars (NG3 phase 2). |
| Gmail Contacts takeout import | Comes with JMAP-Contacts (NG3 phase 2). |
| Snoozed-state import | Gmail does not export snooze metadata in any usable form; the snooze UI starts empty. |
| Gmail Chat / Spaces takeout | Maps poorly onto herold chat (different model); deferred. Chat takeouts are silently skipped if found alongside Mail. |
| Continuous Gmail sync (IMAP-pull) | A separate feature class; the Takeout importer is a one-shot migration tool, not a sync engine. Out of scope for the Takeout doc; revisit if user demand surfaces. |
| Bidirectional sync / round-trip | Out, ever. Herold imports; Gmail does not get re-imported into. |
| Other-vendor importers (Outlook PST, Apple Mail mbox, Maildir) | Phase-2.5 hardening item. Maildir and standard mbox are trivial extensions of the same parser; vendor-specific quirks (PST, OLM) need their own design pass. |

## Out of scope (so we don't relitigate)

- **Replaying outbound from `Sent`.** Imported sent messages are stored, not re-sent.
- **Auto-activating forwarding.** Imported forwarding addresses are data; the user opts in.
- **Importing Google account password / 2FA / OAuth state.** Authentication remains herold-local (`02-identity-and-auth.md`); Google-side credentials are irrelevant.
- **Round-trip fidelity for Gmail-only features.** Snooze, Chat, "scheduled send", "confidential mode" — herold maps what it can, drops what it can't, and lists the drops in the job summary. We do not invent shadow features to mirror Gmail.
- **Live progress over WebSocket.** Polling `GET /api/v1/import/jobs/{id}` once per second is the contract; the Suite UI may use the existing JMAP push channel (REQ-PROTO-48) if/when an `ImportJob` datatype is added — that is a phase-3 nicety, not a launch requirement.
