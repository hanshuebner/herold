# Implementation backlog

*(Created 2026-05-09.)*

A forward-looking list of implementation work that has design coverage
(requirements + architecture) but has **not yet been scheduled into a
wave plan**. Items graduate from this backlog into a numbered wave plan
under `docs/design/server/implementation/` when they get an owner, an
acceptance contract, and an estimate.

This file is not a sprint board. It is the answer to "what's designed
but not yet on a wave" so that scheduling is a deliberate decision and
nothing slips between "documented" and "implemented" by being invisible.

## Conventions

- One entry per coherent unit of implementation work.
- Each entry cites the authoritative requirements file(s) by REQ ID
  range and any architecture notes.
- Size is a coarse t-shirt: **S** (~0.5–1 wk), **M** (~1–2 wk),
  **L** (~3–5 wk), **XL** (>5 wk; needs decomposition before scheduling).
- Status is one of `backlog` (not started), `scheduled` (graduated to a
  wave plan; entry stays here as a back-pointer until the wave ships),
  or `done` (delete after one release cycle).
- Items move *out* by being struck through with a wave-plan link, not
  silently deleted.

## Entries

### Partial mailbox replica (edge + home)

- Status: `backlog`
- Requirements: `docs/design/server/requirements/18-partial-replica.md`
  (REQ-REPL-01..99).
- Size: **XL**. The largest single item on this list. Touches: a new
  `replica` package, a new edge-initiated tunnel transport (JSON-RPC 2.0
  over HTTP/2 inside the operator's WireGuard / Tailscale tunnel), HLC
  bookkeeping in `metadata`, a windowed-by-`received_at` selector in
  both store backends, identifier reconciliation on
  `(message_id, blob_hash)`, bidirectional flag/keyword/membership
  sync with last-writer-wins conflict resolution, an outbound
  forwarding flow from edge to home, and operator commands.
- Dependencies: blob-store CAS already shipped; `state_changes` feed
  already shipped; HLC scaffolding does not yet exist anywhere in the
  tree.
- Decomposition before scheduling: split into (1) HLC + manifest plumbing
  in `metadata`, (2) tunnel transport + RPC surface, (3) initial sync +
  windowed selector, (4) bidirectional flag/keyword sync, (5) outbound
  edge→home forwarding, (6) operator surface + observability. Each piece
  is a wave on its own; the full feature is a multi-wave arc, not a
  single wave plan.
- Risk: **high**. Conflict resolution semantics are subtle; an off-by-one
  on the HLC ordering rule silently corrupts state. Dedicated
  `security-reviewer` and `conformance-fuzz-engineer` gates required at
  the end of each component wave, not just the final one.

### IMAP polling import (long-running per-user IDLE worker)

- Status: `backlog`
- Requirements: `docs/design/server/requirements/19-imap-import.md`
  (REQ-IMAP-IMP-01..84).
- Size: **L**. New `internal/import/imap` package: connection manager
  (IDLE + FETCH connection pair per remote account), OAuth /
  app-password / plain auth, folder-mapping table reused from the Gmail
  Takeout work, mirror semantics (one-way external → herold by default),
  bidirectional `\Seen` / `\Flagged` sync, custom keywords stay
  herold-local, inbound pipeline (spam, sieve, attpol, webhooks, extimg)
  runs on imported mail, 10 s p95 notification-to-store target, Gmail
  All-Mail-as-canonical-source quirk.
- Dependencies: REQ-EXTIMG-* (external-image internalisation, just shipped)
  is reused on the imported messages; the Gmail locale-label table from
  `internal/import/gmail/labels.go` is reused for the Gmail-specific
  branch; `protoevents` already ships so per-account state changes can
  publish.
- Decomposition: (1) connection manager + IDLE + FETCH for a single
  account with plain auth; (2) OAuth + app-password auth modes;
  (3) bidirectional flag sync; (4) Gmail-specific All-Mail handling;
  (5) operator surface + per-account observability.
- Risk: **medium**. The connection-manager state machine is the hard
  part. `imap-implementor` owns the protocol wire; the worker / scheduler
  side wants a fresh agent (`import-implementor` if the role exists,
  otherwise a Gmail-takeout extension).

### Direct archive ingestion for Gmail Takeout (.tgz / .zip / multipart .zip)

- Status: `backlog`
- Requirements: `docs/design/server/requirements/16-import.md`
  REQ-IMPORT-90..99 (added 2026-05-09 alongside the cross-references in
  REQ-IMPORT-01 / REQ-IMPORT-04 / REQ-IMPORT-60 / REQ-IMPORT-62 /
  REQ-IMPORT-71).
- Size: **S** (single archive forms) + **S** (multipart aggregation) =
  **M** combined. New code lives entirely under
  `internal/import/gmail/` plus its testdata tree.
- Dependencies: existing mboxrd parser; existing settings translators;
  existing job manifest / resume cursor (REQ-IMPORT-04) generalised to
  the `(part_index, offset_within_part_entry, last_message_id)` tuple
  per REQ-IMPORT-95; `archive/zip` from the standard library (Zip64
  supported since Go 1.0 era); no third-party dependency needed for
  multipart since Google's "split" output is N independent zips.
- Decomposition: (1) `.tgz` streaming reader (the simplest, validates
  the parser-feed seam); (2) single `.zip` random-access reader with
  Zip64 + spanned-zip refusal; (3) multipart aggregator + per-part
  manifest hashes for resume safety; (4) Suite UI uploader for the
  multipart case.
- Risk: **low**. The streaming readers are well-trodden; the multipart
  aggregator is the only new shape, and the requirement explicitly
  refuses spanned-zip rather than supporting it.
