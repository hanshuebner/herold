# 24 — Tagged addresses (v1)

*(Added 2026-05-11.)*

This feature lets a user hand out sub-addressed variants of one of their email addresses (`alice+amazon@example.local`, `alice+newsletters@example.local`, …) and have inbound mail to each variant routed to a label of their choice, optionally skipping the inbox and/or marked read. The user does not pre-register suffixes; the first time mail to an unfamiliar suffix is *opened*, the suite surfaces a one-shot prompt offering to create a filter or dismiss the suffix.

Web-side counterpart: `../../web/requirements/20-settings.md` § Tagged addresses (v1) for the Settings surface, and `../../web/requirements/02-mail-basics.md` for the per-message banner.

## Scope and rationale

- Sub-addressing per RFC 5233 §3.1 (`local-part+suffix@domain`) is a sender-controlled routing namespace. herold accepts it unconditionally — receipt of mail to `+suffix` does NOT depend on registration; the suffix only governs *filing*.
- The naive design ("each new suffix auto-creates a label") is a denial-of-service vector: a hostile sender can spray random suffixes and grow the user's label list unboundedly. The chosen design caps every stored set by user attention (the user must explicitly open a message and choose an action) and by hard per-principal limits.
- GUI-managed filters live in a structured table, NOT inside the user's Sieve script. The user's Sieve script remains hand-written and is never touched by the GUI. Power users who want richer matching invoke "Convert to Sieve" on a managed filter, which emits the equivalent Sieve text into their script and removes the row from the managed table.

## Suffix extraction

- **REQ-TAG-01** The suffix delimiter is the literal `+` character. The canonical recipient address (`received_to`, REQ-FLOW-33) is split at the first `@` to yield the local-part; the local-part is split at the first `+` to yield `(base_local_part, suffix)`. If no `+` is present, the message has NO suffix and the tagged-address subsystem is a no-op for that message. The base identity address is `base_local_part + "@" + domain`. Suffix bytes after the first `+` are taken verbatim — additional `+` characters are part of the suffix string, not separators.
- **REQ-TAG-02** Canonicalisation: the suffix is compared case-insensitively against stored filters and dismissals. Storage normalises to lower-case. The stored canonical form is what the SPA renders; the original-case form (if different) is dropped — the user does not see casing variance.
- **REQ-TAG-03** Empty suffix (`alice+@example.local`) is treated as "no suffix" — the user's identity received the mail at its base address with a trailing `+`, which is an edge case that does not trigger tagged-address routing.

## Storage

- **REQ-TAG-10** Schema additions (forward-only, SQLite + Postgres parity):
  - `tagged_address_filters(id, principal_id, base_identity_id, suffix, action, label_name, created_at_us, updated_at_us)`:
    - `id` opaque, primary key.
    - `(principal_id, base_identity_id, suffix)` is unique — at most one filter per (identity, suffix) combo.
    - `base_identity_id` FK to `jmap_identities.id` — the Identity row whose address (minus suffix) matches. ON DELETE CASCADE: removing the Identity drops all its tagged filters.
    - `suffix` TEXT NOT NULL, lower-cased canonical form (REQ-TAG-02).
    - `action` TEXT NOT NULL, one of `label`, `label_archive`, `label_archive_read` (REQ-TAG-30). Stored as a string for forward-compat with future actions.
    - `label_name` TEXT NOT NULL — the label/mailbox the filter files into. Existence enforced at filter-create time (REQ-TAG-32).
  - `tagged_address_dismissals(principal_id, base_identity_id, suffix, dismissed_at_us)`:
    - Primary key `(principal_id, base_identity_id, suffix)`.
    - Tracks suffixes the user has explicitly chosen to ignore.
    - CASCADE on Identity destroy.
- **REQ-TAG-11** Hard caps. A principal can hold at most **100 tagged-address filters** total across all their identities, and at most **500 dismissals** total. The caps are enforced at filter-creation / dismissal-creation time (`forbidden / too_many_filters` or `too_many_dismissals` response). The dismissal cap is generous because the user's attention naturally bounds it; the filter cap is tighter because each filter contributes per-message routing overhead.

## Inbound pipeline

- **REQ-TAG-20** Evaluation order at delivery time, per recipient:
  1. Determine `base_identity, suffix` from `received_to` (REQ-TAG-01). If no suffix, skip to step 4.
  2. Look up a matching row in `tagged_address_filters` for `(principal, base_identity, suffix)`. If found, apply the action's filing effect (REQ-TAG-30..32) BEFORE Sieve runs. The actions modify the implicit-keep semantic, possibly causing Sieve to see a message that is already filed and/or already flagged.
  3. If no filter matches, no immediate routing happens; the message proceeds to Sieve as-is. The dismissal table is NOT consulted at delivery time — dismissals affect the SPA banner only (REQ-TAG-40 web-side).
  4. Run the user's Sieve script (existing pipeline, REQ-FILT-*).
  5. Apply the implicit `keep` if neither stage filed the message.
- **REQ-TAG-21** A tagged-filter match is silent — no audit-log entry per matched message (the per-message cost would dwarf the queue's existing volume). Filter create / update / destroy are audited (REQ-TAG-90).
- **REQ-TAG-22** Sieve receives the message after the tag-filter has applied its filing/flag changes. A Sieve `discard` still wins (discards the message regardless of tag-filter state). A Sieve `fileinto` adds another mailbox membership; the tag-filter's filing is not undone. A Sieve `redirect` forwards the message; the tag-filter's local filing is preserved on the local mailbox copy if any.

## Actions

- **REQ-TAG-30** Three action shapes are spec'd. Each is precisely defined in terms of mailbox membership, keywords, and implicit-keep suppression:
  - `label`: ADD the message to mailbox `label_name`. The implicit `keep` semantic is NOT suppressed; if no later stage files the message elsewhere AND no Sieve `discard` runs, the message also lands in Inbox. Equivalent Sieve: `fileinto "<label_name>";` (no `stop`).
  - `label_archive`: ADD the message to mailbox `label_name`. Suppress the implicit `keep` — the message is NOT added to Inbox unless a later Sieve stage explicitly does so via `fileinto "Inbox"`. Equivalent Sieve: `fileinto "<label_name>"; stop;`.
  - `label_archive_read`: same as `label_archive`, PLUS set the `\Seen` IMAP flag (so the message lands already-read). Equivalent Sieve: `setflag "\\Seen"; fileinto "<label_name>"; stop;`.
- **REQ-TAG-31** "Suppress implicit keep" means the per-recipient delivery code does NOT add `Inbox` to the message's mailbox-membership set. If the user's Sieve script later runs `fileinto "Inbox"` explicitly, that DOES add Inbox membership — the tag-filter's suppression is one-shot, not sticky.
- **REQ-TAG-32** `label_name` resolution at filter-create time:
  - If a mailbox/label with this exact name (case-sensitive) exists on the principal's account: use it as-is.
  - Else if a mailbox/label with a case-insensitive match exists: use the existing canonical-case name; reject the request with `mailbox_case_conflict` if the user supplied a DIFFERENT case (the SPA can offer "Use existing 'Shopping'?").
  - Else: create a new mailbox/label with the supplied name as part of the same atomic operation as inserting the filter row.

## Convert to Sieve

- **REQ-TAG-50** An admin-style operation (REST: `POST /api/v1/tagged-address-filters/{id}/convert-to-sieve`, self-only) on a single filter row:
  1. Read the row.
  2. Generate the equivalent Sieve text fragment per REQ-TAG-30's mapping. The fragment is wrapped in `require ["envelope", "fileinto", "imap4flags"];` at the top (idempotently — the operation merges into the existing `require` if present) and emits the `if envelope :matches :localpart "to" "<base_local_part>+<suffix>" { ... }` block.
  3. Prepend the generated fragment to the principal's existing Sieve script (above any existing hand-written content).
  4. Validate the merged script via the existing Sieve compiler. If validation fails, the operation aborts with a clear error AND does NOT delete the filter row (rolls back).
  5. On validation success, delete the `tagged_address_filters` row in the same transaction as the script update. Active script swap happens atomically per the existing Sieve replace semantics.
- **REQ-TAG-51** Conversion is one-way. There is no "convert from Sieve back to managed" operation. A user who wants the GUI to re-manage a converted rule must delete the Sieve text by hand and create a new GUI filter.

## Dismissal lifecycle

- **REQ-TAG-60** Dismissal: `POST /api/v1/tagged-address-dismissals` with `{base_identity_id, suffix}`. Inserts a row. Idempotent on duplicate insert (returns 200 if already dismissed). Hard-capped per REQ-TAG-11.
- **REQ-TAG-61** Removal of a `tagged_address_filters` row (whether via DELETE or via Convert to Sieve) MUST also remove any matching `tagged_address_dismissals` row for the same `(principal, base_identity, suffix)`. Rationale: the user is changing their mind about how to handle this suffix; the dismiss state is stale once the filter is gone. This is per-transaction — both rows go in one statement OR cascade.
- **REQ-TAG-62** Independent dismissal removal (admin-style REST `DELETE /api/v1/tagged-address-dismissals/{base_identity_id}/{suffix}`) is provided for the case where the user wants to be prompted again about a suffix they previously dismissed. Self-only.

## JMAP wire

- **REQ-TAG-70** A new JMAP object type `TaggedAddressFilter` mirrors the storage row. Methods: `TaggedAddressFilter/get`, `TaggedAddressFilter/set` (create + update + destroy), `TaggedAddressFilter/changes`. Capability `https://netzhansa.com/jmap/tagged-addresses` advertises availability.
- **REQ-TAG-71** Dismissals do NOT appear on the JMAP wire — they are a private signalling mechanism for the SPA. Read/write via the REST endpoints above (REQ-TAG-60..62).
- **REQ-TAG-72** Per-suffix state-change events: when a tag filter is created / updated / destroyed, an `Identity/changes` push event fires (NOT a separate type) so the SPA's compose / settings caches invalidate alongside the rest of the identity model. Dismissals likewise emit `Identity/changes` (they are user-state, semantically grouped with identity).

## Configuration knobs

```toml
[server.tagged_addresses]
# Master switch. When false, the JMAP capability is not advertised
# and the inbound pipeline skips REQ-TAG-20's evaluation entirely;
# all sub-addressed mail falls through to Sieve unchanged.
enabled = true

# Per-principal hard caps.
max_filters_per_principal = 100
max_dismissals_per_principal = 500
```

## Audit and observability

- **REQ-TAG-90** Audit events: `tagged_address_filter.create`, `tagged_address_filter.update`, `tagged_address_filter.destroy`, `tagged_address_filter.convert_to_sieve`, `tagged_address_dismissal.create`, `tagged_address_dismissal.destroy`. Each carries the principal id, the base Identity id, the suffix, and the action class. Per-message filter matches are NOT audited (REQ-TAG-21).
- **REQ-TAG-91** Metrics: counters `tagged_address_filter_match_total{action}`, `tagged_address_filter_create_total`, `tagged_address_dismissal_create_total`. Histograms for filter set size (gauge per principal sampled hourly) so operators can spot users approaching the cap.

## Out of scope (v1)

- Pre-registration of suffixes without a triggering message. v1 surfaces filter creation only when the user opens a message to that suffix.
- "Generate a tagged address" affordance that creates a suffix proactively. Useful, but deferred — the model is purely reactive in v1.
- Wildcard / pattern matching beyond exact-suffix equality. Power users wanting `+shop-*` use Sieve directly via Convert to Sieve.
- Cross-principal sharing of tag filters.
- Suffix-aware From routing (sending FROM `me+amazon@`). Tagged addresses are receive-only routing.

## Interactions

- **Verification (REQ-IDENT-*):** Tag filters are scoped to the `base_identity`. The base Identity must be verified — REQ-IDENT-60's send-side gate is unaffected by tagged addresses (tag filters are receive-only). An unverified Identity cannot host tag filters (REST and JMAP both reject filter-create with `forbidden { identity_not_verified }`).
- **X-Herold-Recipient (REQ-FLOW-34):** the suite's per-message banner (web-side REQ-MAIL-12c, see `../../web/requirements/02-mail-basics.md`) reads X-Herold-Recipient to determine the (base_identity, suffix) for the current message. The header makes the banner deterministic regardless of how the message arrived (alias rewriting, BCC delivery, …).
- **Sieve script (REQ-FILT-*):** The user's hand-written Sieve script is never modified by the tagged-address subsystem, with one exception: Convert to Sieve (REQ-TAG-50) prepends a single fragment. The fragment is plain Sieve and survives any future hand-edit. There are no markers.
- **Forwarding (REQ-FLOW-160..):** External forwarding rules of the principal continue to apply after tag-filter evaluation. A tag filter that suppresses implicit keep does not suppress a Sieve `redirect` — the user's forward still fires.
