# IMAP client mode (#25) — re-plan + as-built validation (2026-06-27)

Re-plan and validation pass triggered by the maintainer after the issue
description was edited to add the **"important use case"** (trial in IMAP
client mode -> complete migration off the upstream, preserving the state
built up in herold) and to record inline answers to the open questions.

Requirements and architecture were updated in the same pass:
`requirements/19-imap-import.md` (decisions 7-9, new REQ-IMAP-IMP-53 and
REQ-IMAP-IMP-90..96, three doc-defect fixes) and
`architecture/12-imap-import.md` (migration number 0058, cutover section,
X-GM-LABELS upgrade section).

## Decisions taken

1. **Conflict model stands; the issue's "latest wins" answer is corrected.**
   The build is upstream-authoritative (decision 5), not timestamp-based.
   The issue description's open-question answer is amended to match rather
   than reworking the code. (REQ-IMAP-IMP-42 unchanged.)
2. **Complete-migration cutover is first-class** (REQ-IMAP-IMP-90..96):
   `enabled` -> `migrating` (one-shot complete backfill, authority already
   transferred to herold) -> `migrated` (loops stopped, upstream retired).
3. **True Gmail per-message labels via X-GM-LABELS is planned**
   (REQ-IMAP-IMP-53), not permanently deferred. Requires a `go-imap` bump.

## Validation: as-built vs requirements

Full audit of `internal/imapimport/*` + JMAP/admin surfaces + the `0058`
migration. Faithful in the large: credential sealing (REQ-02/04/70),
TLS-required (REQ-06), allow_password (REQ-05), horizon resolve-at-enable
+ `UID SEARCH SINCE` + low-water mark + lowered-horizon re-scan
(REQ-15..19), as-synced ingest with no pipeline (REQ-31 ingest path),
byte-fidelity BODY.PEEK (REQ-32/33), UIDVALIDITY rollover (REQ-35),
M-failures->errored + backoff (REQ-25), IDLE + NOOP-poll (REQ-20/23),
pool sizing (REQ-26), durable cursors + boot reconnect (REQ-74), live
snapshot (REQ-65), CLI + admin status (REQ-62), CRUD + write-only
credential (REQ-60/61). The upstream-authoritative three-way compare
(REQ-42) is implemented exactly (`writeback.go:194-266`).

### Divergences found (code is wrong; requirements stand) — fix backlog

| # | Severity | Finding | Evidence | REQ |
|---|----------|---------|----------|-----|
| D1 | **High** | **Categoriser runs over the initial INBOX backfill.** Initial sync passes `isForward=true` for the whole in-horizon set, so a 1y INBOX backfill is LLM-categorised — the exact "ruinous over 100k messages" case the spec forbids. **Blocks safe cutover** (a complete migration would categorise the entire history). | `sync.go:184` vs gate `sync.go:371-379` | REQ-31 |
| D2 | **High** | **Upstream-only flag changes are never applied down to herold.** Write-back is driven solely by the herold-side change feed; a pure-upstream mark-read/star generates no herold change-feed entry, so the "apply upstream->herold" branch never runs for it. The "down" direction of REQ-40 is effectively dead. | `writeback.go:90-125, 249-253`; download path only fetches UIDs above HWM / below LWM | REQ-40 |
| D3 | Med | **CONDSTORE/QRESYNC not implemented.** `HighestModSeq` is stored but never used; re-sync is a full `UID SEARCH ALL` enumeration, not an incremental modseq fetch. (This is also the mechanism that would fix D2.) | `sync.go:248-249, 279-291` | REQ-24 |
| D4 | Med | **`blob_hash` fallback dedup not implemented.** No-`Message-ID` messages are not content-hash deduped; they can re-insert a new `messages` row on each pass. | `sync.go:461-463` | REQ-30 |
| D5 | Med | **Gmail All-Mail dedup-skip writes a dead `message_state` row** with `herold_message_id=0` (no lookup done despite the comment). Unaddressable by write-back; latent correctness hazard. | `gmail.go:537-543` | REQ-34/50b |
| D6 | Low | **`backfill_remaining` gauge hard-coded to 0** — inert. **Needed by cutover** (REQ-91 says progress is observed here). | `sync.go:266`, `gmail.go:593` | REQ-63/91 |
| D7 | Low | `conflicts_total{kind}` only ever `"flag"`; move/membership conflicts uncounted. | `writeback.go:264` | REQ-63 |

### Requirements not yet implemented (gaps)

- **REQ-11** operator system-wide default folder-map per upstream-host
  pattern — not built (Gmail map is hard-coded Go; non-Gmail is
  name-equals-name only).
- **REQ-21/73** second-connection rate-limit auto-fallback + backoff-
  exponent bump on rate-limit — only partial (any failure drops to
  single-conn; no distinct rate-limit handling mid-session).
- **REQ-61** `IMAPImport/changes` — deferred (`currentState` fixed `"0"`).
- **Self-service folder-map** — folder-map mutation is admin-REST-only;
  JMAP `IMAPImport/set` has no `FolderMap` field (REQ-10/61 self-service
  gap).
- **No web SPA surface at all** — `web/apps/{suite,admin}/src` contain
  zero `imap` references. The backend (JMAP `IMAPImport/get|set` + admin
  REST + `herold imapimport status` CLI) shipped without any UI; an
  account can only be configured by calling the API directly. The
  self-service story of REQ-61 is unfulfilled. New web requirements
  REQ-SET-IMAPIMP-01..05 (`web/requirements/20-settings.md`) specify the
  missing surface.

### New requirement added 2026-06-27 — provenance label + delete-on-removal

REQ-IMAP-IMP-100..105 + REQ-SET-IMAPIMP-04. On removing an account the web
SPA must ask whether to also delete the mail imported through it; that is
only tractable if imported mail carries a per-account **provenance label**
(today provenance lives only in `imapimport_message_state`, invisible to
the user). Backend work: add the provenance-label membership at ingest;
implement dedup-safe purge on `destroy` with a `delete_imported_mail`
flag. None of this exists yet.

### Beyond spec (noted, no action)

Write-back uses a dedicated third connection (so up to 3 per account;
within the spirit of REQ-21 but worth noting for low-connection-cap
upstreams); prefers `AUTHENTICATE PLAIN` over `LOGIN`; accepts extra
horizon presets (`180d`/`365d`/`2y`).

## Implementation plan (waves)

**Wave A — correctness fixes (no new features), owner: `imap-implementor` /
worker.** Highest leverage and unblocks cutover:
- D1: gate the categoriser on "genuinely new arrival," not `isForward`,
  so the initial backfill is excluded (cursor `HighWaterUID==0` must not
  count as forward). Prerequisite for safe cutover.
- D2 + D3: implement CONDSTORE `CHANGEDSINCE highest_modseq` re-sync (and
  QRESYNC where advertised); apply upstream flag deltas down on each
  reconcile round. Fixes the dead "down" direction and makes re-sync
  incremental.
- D4: blob_hash fallback dedup for no-Message-ID mail.
- D5: look up the existing herold message id in the All-Mail skip branch.
- D6: compute `backfill_remaining` (needed by Wave C).
- D7: count move/membership conflicts.

**Wave B — `go-imap` upgrade + true Gmail labels (REQ-53), owner:
`imap-implementor` + `conformance-fuzz-engineer`.** Bump/patch
`emersion/go-imap/v2` past beta.8 so X-GM-EXT-1 FETCH items parse; add an
`X-GM-LABELS` fetch item to the Gmail path; place each message by its
label set; keep folder-based placement as the non-Gmail fallback. Wire-
surface change -> conformance review + fuzz vectors.

**Wave C — complete-migration cutover (REQ-90..96), owner: worker +
http-api-implementor (surfaces).** Depends on Wave A (D1 safety, D6
progress). Add `migrating`/`migrated` states; force horizon=`all` complete
backfill; transfer authority to herold at `migrating` (stop the
upstream-wins overwrite of already-mirrored mail); stop loops + close
connections at `migrated`; expose the transition on JMAP `IMAPImport/set`
+ admin PATCH; show phase in status; make `DELETE` of a `migrated` account
keep the mail.

**Wave D — provenance label + delete-on-removal (REQ-100..105) + per-identity
re-scope (decision 10), owner: worker + storage.** Create the per-account
provenance label at enable; add its membership at ingest; implement the
dedup-safe purge on `destroy` with the `delete_imported_mail` flag (keep =
default), crash-safe / resumable. **Plus the schema change:** the shipped
`imapimport_account` table is `principal_id`-scoped; decision 10 re-scopes
it to per-`Identity` (add `identity_id`, keep `principal_id` denormalised,
`ON DELETE CASCADE` from the identity). New migration (0058 already
shipped). Also: make `PUT /identities/{id}/submission` **probe-gated** so
external-SMTP setup cannot finish unverified (REQ-AUTH-EXT-SUBMIT-11) — a
small server change in the identity-submission surface.

**Wave E — per-identity transport UI (SMTP + IMAP), owner:
`web-frontend-implementor`.** The currently-absent suite surface, built
**into the Identity edit dialog** (REQ-SET-IDENT-10), not a standalone
section: a mandatory **Sending (SMTP)** section (probe-verified before save,
REQ-MAIL-SUBMIT-01..06 + REQ-AUTH-EXT-SUBMIT-11/12) and an optional
**Receiving (IMAP import)** section for external-domain identities
(REQ-SET-IMAPIMP-01..05) — set-up form, edit + "Complete migration", and the
remove/keep-or-delete prompt (also fired from the identity's Remove action
when it carries imported mail). Depends on Wave D (provenance count,
`deleteImportedMail`, per-identity scope) and Wave C (migrate action).
Puppeteer-verified per `web/CLAUDE.md`.

**Wave F — deferred-gap closure (optional / lower priority).** REQ-11
host-pattern folder-map defaults; REQ-21/73 rate-limit-specific fallback;
REQ-61 `IMAPImport/changes`; self-service folder-map in JMAP `set`.

## Verification plan

- Wave A: extend the in-process `imapmemserver` tests — assert the
  categoriser is NOT invoked across an initial backfill (counter on the
  test categoriser); assert an upstream-only `\Seen` set is reflected in
  herold after a reconcile round; assert a no-Message-ID message imported
  twice yields one `messages` row.
- Wave B: conformance vectors for X-GM-LABELS FETCH parsing; a Gmail-shaped
  fixture placing one message in K labels -> K memberships via per-message
  labels (not folders).
- Wave C: trial-then-migrate integration test — mirror with a short
  horizon, mutate herold-side flags/labels, run a both-sides conflict, then
  `migrate`; assert the complete mailbox is present, herold-side state is
  preserved (not overwritten), `backfill_remaining` reaches 0, and the
  worker reaches `migrated` with connections closed. Both backends.
- Wave D: provenance label appears on imported mail (membership assertion);
  purge with a message shared by two import accounts removes only one
  account's label and keeps the message; purge of a single-source message
  destroys it and decrements the blob refcount; restart mid-purge completes.
- Wave E: puppeteer end-to-end against an ephemeral instance
  (`scripts/dev-instance.sh`) — add an account, see the provenance label
  appear, remove with each of the two choices, screenshot the prompt.
- Both SQLite and Postgres for every store-touching change; `-race` clean;
  full pre-commit chain per commit.

## Related feature beyond #25: domain cutover

`26-domain-cutover.md` (REQ-CUTOVER-\*) specifies the org-level adoption
journey — trial with per-identity bridges, then a full domain cutover where
the admin migrates every mailbox with one delegated/master credential and
early adopters are re-homed (their `user@domain` identity promoted to the
account's primary, mirror finalized, external SMTP dropped). It is a strict
superset of the per-identity machinery here (it drives the same worker pool
and complete-migration path) and depends on Waves A/C/D landing first. It is
**its own feature**, tracked as **issue #62** (labelled `deferred`), not part
of #25. Operator guide: `docs/manual/admin/domain-cutover.mdoc` (marked
planned until the tooling ships).
