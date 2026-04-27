# Admin CLI gap-fill triage -- 2026-04-26

Source: `TODO(operator-doc)` markers across `docs/user/` introduced in
Wave 3.0 Track B (commit 4e29823); reviewed against the post-Wave 3.5c
codebase (HEAD f92291f).

## Summary

The 12 `TODO(operator-doc)` markers tracked here expand into 22
individual subcommands. Triage finds:

- **Bucket 1 (CLI-surface-only): 13 commands.** REST + store both
  exist; the gap is the cobra subcommand. Includes the principal
  surface (`show`, `disable`, `enable`, `quota`, `grant-admin`),
  totp enroll/disable, alias CRUD, api-key list, audit list, and
  the categorise recategorise verb.
- **Bucket 2 (underlying-impl-missing): 6 commands.** No production
  REST surface, sometimes no store hook either. Includes Sieve admin
  CRUD (REQ-ADM-15 explicitly defers per-user scripts to ManageSieve;
  global scripts not implemented), categorise prompt mutation
  (`prompt set`, `list-categories`), DKIM `generate` and `show`, and
  mailbox ACL (Phase 2 by spec).
- **Bucket 3 (both -- partial): 3 commands.** Mailbox `add` /
  `list` (store methods exist; no admin REST surface beyond
  attachment-policy `set`); categorise prompt is store-only with no
  REST.

Recommendation: **Wave 3.4 ships all of Bucket 1 as one commit
(~250-350 LOC + tests).** Bucket 2 items split into their natural
feature waves -- Sieve admin into a Sieve-CRUD wave that has to
revisit REQ-ADM-15 first; categorise prompt mutation into the
categorise feature wave; DKIM `generate` / `show` into a small
DKIM-rotation wave that already has all the store + keymgmt scaffolding
behind it. Bucket 3 needs spec confirmation about whether admin-side
mailbox CRUD is even a v1 surface (REQ-ADM-101's enumeration does not
include `mailbox` as a noun).

## Bucket 1: CLI-surface-only

### `herold principal show <email-or-id>`

- **Spec ref**: REQ-ADM-101 (`docs/design/server/requirements/08-admin-and-management.md:55`),
  REQ-ADM-10 (`docs/design/server/requirements/08-admin-and-management.md:20`).
- **Cobra subcommand**: missing. Suggested location:
  `internal/admin/cmd_principal.go` (extend the existing tree).
- **Underlying surface**: REST endpoint `GET /api/v1/principals/{pid}`
  (`internal/protoadmin/routes.go:22` ->
  `internal/protoadmin/principals.go:127` `handleGetPrincipal`); store
  method `Metadata.GetPrincipalByID` (`internal/store/store.go:71`).
- **Effort**: ~25 LOC for the subcommand + 1 unit test (lookup-by-id
  vs lookup-by-email needs an indirection through the existing
  `/principals?...` list-and-filter path because the REST endpoint
  takes a numeric pid; either accept only id, or do a list + filter
  client-side).

### `herold principal disable <email-or-id>`

- **Spec ref**: REQ-ADM-101 (line 55), REQ-ADM-10 (line 20). The
  spec lists `disable` / `enable` as distinct verbs.
- **Cobra subcommand**: missing. Suggested location:
  `internal/admin/cmd_principal.go`.
- **Underlying surface**: `PATCH /api/v1/principals/{pid}` with
  `{"flags": ["disabled", ...prior flags]}`
  (`internal/protoadmin/routes.go:23` ->
  `internal/protoadmin/principals.go:152` `handlePatchPrincipal`);
  store method `Metadata.UpdatePrincipal`
  (`internal/store/store.go:85`); the
  `PrincipalFlagDisabled` bit is already plumbed end-to-end
  (`internal/store/types.go:79`,
  `internal/protoadmin/types.go:52,72`,
  enforced at auth time
  in `internal/directory/directory.go:313,344`).
- **Effort**: ~30 LOC. The CLI must first GET the principal to
  preserve the current `flags` set, then PATCH with `disabled`
  added. One test per add/remove transition.
- **Spec ambiguity**: spec lists `disable` and `enable` as separate
  verbs (REQ-ADM-101). Alternative is a single `flags` verb
  (`herold principal flags add disabled`). Recommend implementing
  both `disable` and `enable` as thin wrappers that read-modify-write
  the flags array, matching the spec's verb list.

### `herold principal enable <email-or-id>`

- **Spec ref**: REQ-ADM-101 (line 55).
- **Cobra subcommand**: missing.
- **Underlying surface**: as `disable`, with the `disabled` flag
  removed from the array.
- **Effort**: ~25 LOC; pairs with the `disable` test.

### `herold principal quota <email-or-id> <bytes>`

- **Spec ref**: REQ-ADM-101 (line 55), REQ-ADM-10 (line 20)
  identifies `/quota` as a documented subresource of `/principals`.
- **Cobra subcommand**: missing. Suggested location:
  `internal/admin/cmd_principal.go`.
- **Underlying surface**: `PATCH /api/v1/principals/{pid}` with
  `{"quota_bytes": N}` (`internal/protoadmin/principals.go:170-181`
  -- the PATCH handler already accepts `QuotaBytes` and gates
  changes behind `PrincipalFlagAdmin`); the field lives on
  `Principal.QuotaBytes` (`internal/store/types.go:121`); REQ-STORE-50
  is the storage-side requirement.
- **Effort**: ~25 LOC. Accept human-readable suffixes ("10G", "500M")
  parsed client-side. One unit test for the flag plumbing + one for
  the suffix parser.
- **Spec ambiguity**: REQ-ADM-10's enumerated `/quota` subresource
  hints at a dedicated `PUT /api/v1/principals/{pid}/quota` rather
  than the PATCH-on-parent approach already shipped. Recommend the
  CLI use the PATCH path that exists today; if a dedicated
  subresource lands later it's an internal swap. Flag this in the
  CLI commit's review.

### `herold principal grant-admin <email-or-id>` and `revoke-admin`

- **Spec ref**: REQ-ADM-101 (line 55, the `flags` knob);
  flag literals at
  `internal/protoadmin/types.go:75` (`"admin"`).
- **Cobra subcommand**: missing.
- **Underlying surface**: `PATCH /api/v1/principals/{pid}` with
  `{"flags": [..., "admin"]}` (or without). Already gated to admin
  callers (`internal/protoadmin/principals.go:182-197`). Bootstrap
  already exercises this path; see
  `internal/admin/cmd_bootstrap.go:73`.
- **Effort**: ~30 LOC. Same read-modify-write approach as
  `disable` / `enable`. One unit test.

### `herold principal totp enroll <email-or-id>` and `disable`

- **Spec ref**: REQ-ADM-203
  (`docs/design/server/requirements/08-admin-and-management.md:94`)
  is the self-service angle; the operator override is not listed by
  REQ ID but the REST endpoints are wired, so the CLI is purely
  surface work.
- **Cobra subcommand**: missing. Suggested location:
  `internal/admin/cmd_principal.go` (a new `totp` subcommand group)
  or `cmd_totp.go`.
- **Underlying surface**:
  - `POST /api/v1/principals/{pid}/totp/enroll`
    (`internal/protoadmin/routes.go:26` ->
    `internal/protoadmin/totp.go:16` `handleTOTPEnroll`).
  - `POST /api/v1/principals/{pid}/totp/confirm`
    (`internal/protoadmin/routes.go:27` ->
    `internal/protoadmin/totp.go:37` `handleTOTPConfirm`).
  - `DELETE /api/v1/principals/{pid}/totp`
    (`internal/protoadmin/routes.go:28` ->
    `internal/protoadmin/totp.go:69` `handleTOTPDisable`).
- **Effort**: ~50 LOC for the trio (enroll prints the provisioning
  URI; confirm takes a code; disable takes a current password). One
  unit test per verb.
- **Spec ambiguity**: the user-doc `TODO` says `POST /api/v1/principals/{id}/2fa/totp`
  but the wired surface is `/totp/enroll` + `/totp/confirm` +
  `/totp` (DELETE). The user doc is stale; the wired surface is
  correct. Update the user doc as part of this commit.

### `herold alias add <addr> <target>`

- **Spec ref**: REQ-ADM-10 (line 20, `/aliases` subresource).
- **Cobra subcommand**: missing. Suggested location:
  `internal/admin/cmd_alias.go` (NEW file).
- **Underlying surface**: `POST /api/v1/aliases`
  (`internal/protoadmin/routes.go:46` ->
  `internal/protoadmin/aliases.go:37` `handleCreateAlias`); store
  method `Metadata.InsertAlias`
  (`internal/store/store.go:170`). DTO at
  `internal/protoadmin/aliases.go:12-17`.
- **Effort**: ~40 LOC. The `<addr>` arg parses into `local@domain`
  client-side; `<target>` is an email -> the CLI looks up the target
  principal id via the existing `GET /principals?...` list (or a
  `?email=` lookup if added). Two unit tests (single-target,
  invalid-target).
- **Spec ambiguity**: the user doc claims multi-target aliases
  (fanout) but the wired REST `createAliasRequest` only supports
  one `target_principal_id`. Multi-target fanout is a separate
  feature that needs spec text + store schema; for now scope the
  CLI to single-target and note "multi-target: not yet wired" in
  the help text.

### `herold alias list [--domain <name>]`

- **Spec ref**: REQ-ADM-10 (line 20).
- **Cobra subcommand**: missing.
- **Underlying surface**: `GET /api/v1/aliases?domain=...`
  (`internal/protoadmin/routes.go:45` ->
  `internal/protoadmin/aliases.go:19` `handleListAliases`); store
  method `Metadata.ListAliases` (`internal/store/store.go:180`).
- **Effort**: ~30 LOC + 1 unit test.

### `herold alias delete <id>`

- **Spec ref**: REQ-ADM-10 (line 20).
- **Cobra subcommand**: missing.
- **Underlying surface**: `DELETE /api/v1/aliases/{id}`
  (`internal/protoadmin/routes.go:47` ->
  `internal/protoadmin/aliases.go:71` `handleDeleteAlias`); store
  method `Metadata.DeleteAlias` (`internal/store/store.go:184`).
- **Effort**: ~25 LOC + 1 unit test.
- **Spec ambiguity**: the user doc spells `delete <addr>` but the
  REST surface keys on numeric `id`. Either resolve `<addr>` to id
  client-side via a list-and-filter, or document the id-only verb.
  Recommend list-and-filter for ergonomics.

### `herold api-key list [--principal <email>]`

- **Spec ref**: REQ-ADM-101 (line 55).
- **Cobra subcommand**: missing. Suggested location:
  `internal/admin/cmd_apikey.go` (extend the existing tree).
- **Underlying surface**: two endpoints already wired:
  - `GET /api/v1/api-keys` (caller's own keys;
    `internal/protoadmin/routes.go:50` ->
    `internal/protoadmin/apikeys.go:62` `handleListOwnAPIKeys`).
  - `GET /api/v1/principals/{pid}/api-keys` (admin-only path for
    arbitrary principal; `internal/protoadmin/routes.go:31` ->
    `internal/protoadmin/apikeys.go:39`
    `handleListPrincipalAPIKeys`).
  - Store method `Metadata.ListAPIKeysByPrincipal`
    (`internal/store/store.go:249`).
- **Effort**: ~35 LOC. With `--principal`, resolve email -> pid via
  list-and-filter, then call the per-principal endpoint; without,
  call the own-keys endpoint. Two unit tests.

### `herold audit list`

- **Spec ref**: REQ-ADM-19
  (`docs/design/server/requirements/08-admin-and-management.md:29`),
  REQ-ADM-300 (line 99), REQ-ADM-302 (line 101).
- **Cobra subcommand**: missing. Suggested location:
  `internal/admin/cmd_audit.go` (NEW file).
- **Underlying surface**: `GET /api/v1/audit`
  (`internal/protoadmin/routes.go:63` ->
  `internal/protoadmin/server_endpoints.go:189` `handleAuditLog`);
  filter struct at `store.AuditLogFilter`; store method
  `Metadata.ListAuditLog` (`internal/store/store.go:298`). Filter
  flags supported today: `action`, `principal_id`, `since`, `until`,
  `limit`, `after_id`. The user doc also wants `--actor` and
  `--resource`.
- **Effort**: ~50 LOC + 2 unit tests.
- **Spec ambiguity**: user doc names filters
  `--actor`, `--action`, `--resource`; REST today exposes
  `principal_id` (not actor email), no `resource` filter. The user
  doc's `--actor admin@example.com` form requires either an
  email-to-pid client-side lookup, or an actor-email index/filter
  REST-side. The CLI in Wave 3.4 should ship `--principal-id`
  (matches REST), `--action`, `--since`, `--until`, `--limit`; the
  email-form `--actor` and the `--resource` filter are Bucket 2 work
  if the user doc's exact spelling is required. Spec REQ-ADM-19
  literally names the filters as "since, actor, action, resource",
  so the actor-email and resource filters arguably belong on the
  REST surface; flag this gap explicitly in the commit.

### `herold categorise recategorise <email-or-id> [--limit N]`

- **Spec ref**: REQ-FILT-220
  (`docs/design/server/requirements/06-filtering.md:139`).
- **Cobra subcommand**: missing. Suggested location:
  `internal/admin/cmd_categorise.go` (NEW file).
- **Underlying surface**:
  - `POST /api/v1/principals/{pid}/recategorise`
    (`internal/protoadmin/routes.go:86` ->
    `internal/protoadmin/categorise.go:76`
    `handleRecategorisePrincipal`).
  - `GET /api/v1/jobs/{id}` (poll progress;
    `internal/protoadmin/routes.go:87` ->
    `internal/protoadmin/categorise.go:154` `handleGetJob`).
- **Effort**: ~60 LOC. The CLI POSTs, then polls `/jobs/{id}` until
  state in {`done`, `failed`}. Print progress as `done/total` lines
  while polling. Two unit tests (success + failure).

## Bucket 2: Underlying-impl-missing

### `herold dkim generate <domain>`

- **Spec ref**: REQ-ADM-11
  (`docs/design/server/requirements/08-admin-and-management.md:21`)
  identifies `/dkim` as a documented subresource of `/domains`.
  REQ-OPS-60 (`docs/design/server/requirements/09-operations.md:132`)
  says key generation happens automatically on `domain add`, so
  this CLI is the explicit re-generation / rotation knob.
- **Cobra subcommand**: missing.
- **Underlying surface**: NO REST endpoint for DKIM. Store methods
  for DKIM keys exist
  (`UpsertDKIMKey`/`GetActiveDKIMKey`/`ListDKIMKeys`/`RotateDKIMKey`
  at `internal/store/store.go:402,407,411,416`); key-generation logic
  exists in `internal/mailauth/keymgmt/keymgmt.go:72`
  (`Manager.GenerateKey`) and is wired by the domain-add path. What
  is missing is an admin-facing REST handler that calls
  `keymgmt.Manager.GenerateKey`.
- **Effort**: ~120 LOC. Add `POST /api/v1/domains/{name}/dkim` ->
  `Manager.GenerateKey` (rotation semantics already in
  keymgmt); add `GET /api/v1/domains/{name}/dkim` to list keys; CLI
  on top. Unit + integration tests.
- **Spec ambiguity**: the spec text REQ-ADM-11 enumerates the `/dkim`
  subresource but does not specify the verbs. Recommend POST = create
  (rotate-or-generate; idempotent in the keymgmt layer's terms), GET
  = list with active flag, DELETE = retire (rare). Document in the
  REST handler's doc.go.
- **Wave routing**: belongs in a small DKIM-rotation wave (call it
  Wave 3.7?). All store + crypto scaffolding exists; this is purely
  REST + CLI surface work, but it's bigger than a one-commit
  Bucket 1 add and benefits from its own design pass on the verb
  semantics.

### `herold dkim show <domain>`

- **Spec ref**: REQ-ADM-11 (line 21), REQ-ADM-310
  (`docs/design/server/requirements/08-admin-and-management.md:108`).
- **Cobra subcommand**: missing.
- **Underlying surface**: NO REST endpoint. Store has
  `GetActiveDKIMKey` (`internal/store/store.go:407`). The DKIM TXT
  record body (the user-visible artefact) is built by
  `autodns.BuildDKIMRecord` (`internal/autodns/publisher.go:169,257`).
- **Effort**: ~60 LOC for the REST handler + ~30 LOC for the CLI
  + tests. Lower-effort than `generate` because there's nothing to
  mutate.
- **Wave routing**: pair with `dkim generate` in the same DKIM
  wave; they share the REST surface design.

### `herold sieve put <email> active < script.sieve`

- **Spec ref**: REQ-ADM-15
  (`docs/design/server/requirements/08-admin-and-management.md:25`),
  REQ-FILT-120 (`docs/design/server/requirements/06-filtering.md:104`).
- **Cobra subcommand**: missing.
- **Underlying surface**: NO REST endpoint for Sieve admin. Store
  has `GetSieveScript` / `SetSieveScript`
  (`internal/store/store.go:334,341`). ManageSieve listener exists at
  `internal/protomanagesieve/`; the JMAP Sieve datatype is at
  `internal/protojmap/mail/sieve/`.
- **Effort**: TBD. The wire-format depends on whether REQ-ADM-15
  means "global script" (a single shared admin script that runs
  before user scripts, per REQ-PROTO-67) or "per-user script CRUD"
  (the user doc's reading). The spec is unambiguous: REQ-ADM-15
  says global only, "Per-user scripts only via ManageSieve for the
  user." The user doc text ("`herold sieve put user@example.com
  active`") is therefore aspirational beyond the spec.
- **Spec ambiguity**: large -- the user doc's per-user-write CLI
  is explicitly out of scope per REQ-ADM-15. Either:
  (a) Implement REQ-ADM-15 as written: a single global script CRUD
  (`GET/PUT /api/v1/sieve/scripts`), wire `herold sieve get` /
  `herold sieve put` (no principal arg), and update the user doc
  to match.
  (b) Amend REQ-ADM-15 to allow per-principal admin overrides
  before implementation.
- **Wave routing**: Wave 3.x dedicated to Sieve admin. Block on a
  spec-side decision between (a) and (b). Recommend (a) -- aligns
  with existing surface intent; per-user editing already has
  ManageSieve and JMAP Sieve.

### `herold sieve validate < script.sieve`

- **Spec ref**: REQ-PROTO-53
  (`docs/design/server/requirements/01-protocols.md:130`) -- JMAP Sieve
  validate is a Phase 1 datatype. ManageSieve VALIDATE is in
  REQ-PROTO-MGSV.
- **Cobra subcommand**: missing.
- **Underlying surface**: NO REST endpoint. The Sieve interpreter
  (and presumably its compile-only entry) lives in `internal/sieve/`.
- **Effort**: ~80 LOC -- an admin REST validate endpoint
  (`POST /api/v1/sieve/validate` reading the script body) plus
  CLI surface. The interpreter package likely has the parser handy.
- **Wave routing**: same Sieve wave as `sieve put`. Lower priority
  than the admin write path.

### `herold sieve list <email>` and `herold sieve activate <email> <script>`

- **Spec ref**: REQ-ADM-15 (line 25). The "list" + "activate"
  semantics are ManageSieve concepts (`LISTSCRIPTS`, `SETACTIVE`)
  per REQ-PROTO-67.
- **Cobra subcommand**: missing.
- **Underlying surface**: NO. Store today has a single-script-per-
  principal model (`SetSieveScript` overwrites; one active by
  construction); REQ-PROTO-67 explicitly anticipates "one active,
  N inactive" but the storage model has not yet grown that. So
  this is store schema + REST + CLI, not just CLI.
- **Effort**: substantial. Store schema migration to add an
  inactive-scripts table, REST CRUD, CLI surface. TBD until a
  design pass.
- **Spec ambiguity**: REQ-FILT-120 says "one active, N inactive"
  but the current store interface (`internal/store/store.go:329-341`)
  documents "Phase 1 is one script per principal", so the spec is
  ahead of the implementation. Both should converge before CLI
  work starts.
- **Wave routing**: same Sieve wave as `put` / `validate`, but
  blocks on the schema decision. Treat as the largest of the three
  Sieve subcommand items.

### `herold categorise prompt set <email> < prompt.txt` and `list-categories`

- **Spec ref**: REQ-FILT-211
  (`docs/design/server/requirements/06-filtering.md:128`),
  REQ-FILT-210 (line 127), REQ-FILT-212 (line 129).
- **Cobra subcommand**: missing.
- **Underlying surface**: store has `GetCategorisationConfig` and
  `UpdateCategorisationConfig` (`internal/store/store.go:633,638`),
  but **no REST handler exposes them**. (`recategorise` is exposed;
  the per-principal config is not.)
- **Effort**: ~80 LOC -- two REST endpoints
  (`GET /api/v1/principals/{pid}/categorisation` and
  `PUT /api/v1/principals/{pid}/categorisation`) plus CLI. The
  store layer is ready.
- **Spec ambiguity**: small. REQ-FILT-211 says "Mutable" but does
  not specify the REST shape. Recommend modelling the PUT body on
  `store.CategorisationConfig`'s exported fields (Prompt,
  CategorySet, Endpoint, Model, APIKeyEnv, TimeoutSec, Enabled).
- **Wave routing**: belongs with the rest of the categorise feature
  wave (separate from the recategorise verb that's already wired).

### `herold mailbox acl <addr>`

- **Spec ref**: REQ-PROTO-37 (per the user doc reference; actually
  in `docs/design/server/requirements/01-protocols.md`). User doc explicitly
  notes "Phase 2".
- **Cobra subcommand**: missing.
- **Underlying surface**: shared mailboxes + ACL is a Phase 2
  feature; admin REST shape is not yet specified.
- **Effort**: TBD. Out of scope for Wave 3.x.
- **Wave routing**: Phase 2 / shared-mailbox wave. Drop the
  `TODO(operator-doc)` marker for this one until the feature wave
  picks it up; do not block Wave 3.4 on it.

## Bucket 3: Both

### `herold mailbox add <email> <name>`

- **Spec ref**: ambiguous. REQ-ADM-101 (line 55) lists nouns
  (`principal`, `domain`, `group`, `queue`, `spam`, `cert`,
  `server`, `mail`, `diag`) and **does not include `mailbox`**.
  The user doc's claim is therefore beyond the explicit spec.
- **Cobra subcommand**: missing apart from the `set` verb wired in
  Wave 3.5c (`internal/admin/cmd_mailbox.go:14`, only carrying the
  attachment-policy knob).
- **Underlying surface**: store method `Metadata.InsertMailbox`
  exists (`internal/store/store.go:99`); IMAP and JMAP both call it
  today (`internal/protojmap/mail/mailbox/set.go:271`,
  `internal/protosmtp/deliver.go:592`). **No admin REST endpoint.**
- **Spec confirmation needed**: does v1 expose admin-side mailbox
  CRUD at all? REQ-ADM-101's enumeration does not include it; the
  user doc rationalises it as "provisioning a fresh principal with
  a non-default folder layout". Recommend amending REQ-ADM-101 to
  enumerate `mailbox` as a noun (or removing the user-doc claim and
  the `TODO`).
- **Effort if greenlit**: ~80 LOC for REST + CLI; ~20 LOC for tests.
- **Wave routing**: small dedicated mailbox-admin wave once the spec
  question is resolved. **Spec confirmation gates implementation.**

### `herold mailbox list <email>`

- **Spec ref**: same ambiguity as `mailbox add`.
- **Cobra subcommand**: missing.
- **Underlying surface**: store method `Metadata.ListMailboxes`
  exists (`internal/store/store.go:94`). **No admin REST endpoint.**
- **Effort if greenlit**: ~50 LOC.
- **Spec confirmation**: same as above.

### `herold categorise prompt set` (list form)

(Already classified in Bucket 2; included here only for cross-
reference. The prompt-set verb has store coverage but no REST or
CLI -- a partial-coverage case that fits Bucket 3 by the strictest
reading. Treated under Bucket 2 because the scope of work is
"REST + CLI" -- the same as a Bucket 2 item. The audit trail is
clear either way.)

## Recommendation

### Wave 3.4 -- ship Bucket 1 in one commit

13 cobra subcommands across `cmd_principal.go`, `cmd_apikey.go`,
plus three new files (`cmd_alias.go`, `cmd_audit.go`,
`cmd_categorise.go`).

Estimated total: **~400 LOC of Go (subcommands + parsers)**, ~200
LOC of unit tests, ~half a day of focused work.

The commit should also:

- Update the `docs/user/administer.md` and
  `docs/user/quickstart-extended.md` `TODO(operator-doc)` markers
  for the items it ships -- replace each marker with the wired
  example.
- Note in the commit body the spec gaps it does NOT address: the
  REQ-ADM-19 `--actor` (email-form) and `--resource` audit filters,
  the multi-target alias feature, the dedicated
  `PUT /principals/{pid}/quota` subresource (REQ-ADM-10 hint),
  and the user-doc's stale `2fa/totp` URL form.

### Bucket 2 -- route into feature waves

- **`dkim generate` + `dkim show`**: dedicated DKIM-rotation wave
  (Wave 3.7 candidate). Pre-requisite: design pass on the verb
  semantics for `POST /api/v1/domains/{name}/dkim`.
- **`sieve put` / `validate` / `list` / `activate`**: dedicated
  Sieve-admin wave. **Pre-requisite: spec decision on REQ-ADM-15
  (global-only vs per-user) and on REQ-FILT-120's "one active, N
  inactive" storage model.** The user-doc's per-user-script verbs
  contradict REQ-ADM-15 as written.
- **`categorise prompt set` + `list-categories`**: fold into the
  ongoing categorise feature wave. Store layer ready; needs REST
  + CLI.
- **`mailbox acl`**: Phase 2 (shared-mailbox feature wave); drop
  the `TODO` marker until then.

### Bucket 3 -- spec confirmation first

- **`mailbox add` + `mailbox list`**: REQ-ADM-101 does not list
  `mailbox` as a noun. Decide: (a) amend REQ-ADM-101 and ship the
  REST + CLI surface, or (b) remove the claim from the user doc.
  Recommend (a) because the underlying need (provisioning fresh
  principals with custom folder layouts) is real and the store
  method already exists.

### Out-of-bucket stragglers in `docs/user/`

A handful of `TODO(operator-doc)` markers fall outside the 12
listed in the prompt -- they are NOT CLI gaps but config-shape
pointers that block on the relevant feature landing:

- `install.md:179` (`packaging-publish-wave`) -- packaging.
- `install.md:197` (`cluster-template-and-tested-walkthrough`)
  -- cluster docs.
- `operate.md:149` (`managesieve-listener-shape`) -- ManageSieve.
- `operate.md:221` (`categorise-config-block`) -- pairs with the
  Bucket 2 categorise prompt work.
- `operate.md:605` (`module-log-level-config-shape`) -- log
  config.
- `operate.md:841,889,897` -- queue/retention config-block shapes.
- `administer.md:493` (`chat-cal-contacts-admin-cli-not-yet-wired`)
  -- Phase 2 chat/cal/contacts admin surface.

These are not cobra-subcommand gaps and do not belong to Wave 3.4.
Each tracks with the wave that ships the corresponding feature.
