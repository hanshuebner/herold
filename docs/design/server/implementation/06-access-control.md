# 06 — Access control: build ordering and Phase A substrate

Implementation plan for the unified resource-grant authorization model. The
model itself is authoritative in `../requirements/07-access-control.md`
(`REQ-AC-`) and `../architecture/15-access-control.md`; this document maps that
model onto concrete Go packages and store schema, fixes the build ordering, and
records exactly what the first pass (Phase A, epic #182) builds versus defers.

Epics: #182 (this substrate), #186 (mailbox grants / IMAP RFC 4314 ACL), #188
(external IdP claim-to-grant mapping). #186 and #188 are thin layers on the
substrate here and are sequenced to follow it.

## The one model, restated for implementers

Authorization is a set of **grants**. A grant is a tuple:

```
  grant { subject, resource_kind, resource_id, level, provenance }
```

- **subject** — a principal (Phase A) or, later, an authorization group
  (REQ-AC-80). The `subject_kind` column carries `principal` today and admits
  `group` without a further migration.
- **resource** — a typed pair `(resource_kind, resource_id)`: `server` (id `''`),
  `domain` (the domain name), `list` (the list id), `mailbox` (the mailbox id).
- **level** — a per-kind, totally-ordered capability tier; a higher tier implies
  every lower tier on the same resource (REQ-AC-03). `server:superadmin`;
  `domain:{operator,owner}`; `list:{moderator,owner}`; `mailbox:{read,write,admin}`.
- **provenance** — `local` (operator-assigned) or `idp:<provider>` (derived from a
  claim-mapping rule). `local` and `idp:` rows for one (subject, resource) are
  distinct rows and never overwrite each other (REQ-AC-61); effective access is
  their union.

A principal's authority is the union of its grants plus structural implicit
ownership (a principal owns its own mailboxes; a domain/list has one owner;
`server:superadmin` is the site root — REQ-AC-02). "Being an admin" is emergent
from holding any tier above one's own end-user resources; there is no stored role
attribute.

## The check seam

Every surface that touches a resource asks one function:

```
  authz.Resolve(ctx, meta, principal, Resource{Kind, ID}) -> (Level, error)
```

`Resolve` returns the acting principal's effective level on that specific
resource; the handler compares it against the level the operation requires and
returns an RFC 7807 403 when it is insufficient (REQ-AC-10). Resolution is
default-deny and **fail-closed**: a store error resolving the grant set yields a
deny, never an allow (REQ-AC-12). This is the single point SMTP, IMAP, JMAP, the
admin REST API, and the mailing-list code consult; no protocol grows its own
permission logic. It sits behind the existing `REQ-AUTH-SCOPE-*` scope +
TOTP-elevation gate unchanged — grants change what the gate evaluates, not how a
request is authenticated, and an administrative grant still requires a live
elevation record to be exercised for a mutating operation (REQ-AC-11).

## How #186 and #188 become thin layers

The point of landing #182 first is that the two dependent epics add data and one
narrow adapter each, not a second authorization mechanism.

**#186 — mailbox grants / IMAP RFC 4314 ACL.** A mailbox is the `mailbox`
resource kind already present in the schema. The work is:

- Resolve implicit `mailbox:admin` on a principal's own mailboxes (REQ-AC-51) —
  one structural branch added to `Resolve`, mirroring the domain-owner branch.
- Add the RFC 4314 adapter in `internal/protoimap`: `SETACL`/`GETACL`/`MYRIGHTS`/
  `LISTRIGHTS` read and write `mailbox` grant rows. The rights string collapses
  onto the three tiers on write and is re-derived on read
  (`read = l r s`, `write = i k x t e w p`, `admin = a`; architecture doc §IMAP
  RFC 4314 mapping). No new storage: the grant table is the ACL store.
- Enforce `mailbox` grants at the IMAP SELECT/STATUS/FETCH/STORE points and the
  JMAP sharing surface by calling the same `Resolve` (REQ-AC-53).

No schema migration, no new resolver: #186 is a resource-kind already modelled,
one implicit-ownership branch, and a wire adapter.

**#188 — external IdP claim-to-grant mapping.** Grants already carry
`provenance`. The work is:

- DB-backed per-provider claim-mapping rules + an authorization-claim allowlist
  (new admin state), plus a per-provider `authz_trusted` flag (REQ-AC-60/66/67).
- `authz.ReconcileIdP(ctx, principal, provider, claims)` called from
  `internal/directoryoidc` on successful login: evaluate rules to a desired
  `idp:<provider>` grant set, filter through the safety rails (never
  `server:superadmin`; author authority re-validated; no administrative grant on
  an auto-provisioning first login), then diff against the principal's current
  `idp:<provider>` rows in one transaction, refreshing `last_asserted_at` on
  survivors and leaving `local` rows untouched (REQ-AC-62/64/68/69).
- A staleness sweep worker deleting `idp:*` grants past the window (REQ-AC-63),
  and synchronous removal on provider unlink.

The grant table's `provenance` column and its `(subject, resource, provenance)`
unique key were designed for exactly this: reconciliation is an atomic diff over
`idp:` rows and never disturbs `local` authority. `server:superadmin` is
`local`-only, so an IdP compromise cannot seize the node (REQ-AC-64).

## Phase A cut (epic #182, this pass)

Built:

- **Store schema.** Migration `0079_grants.sql` (SQLite + Postgres, isomorphic):
  the `grants` table per the architecture doc, with the
  `(subject_kind, subject_id, resource_kind, resource_id, provenance)` unique key
  and the two lookup indexes. The migration back-fills existing authority so the
  grant table is a faithful projection of today's rules on upgrade:
  `server:superadmin` for every principal carrying `PrincipalFlagSuperAdmin`, and
  `domain:operator` for every `principal_managed_domains` row (#145). No operator
  is locked out and no authority changes on upgrade.
- **Repository.** `Grant` types in `internal/store` and the `Metadata` methods
  `InsertGrant` / `DeleteGrant` / `ListGrantsForPrincipal` / `ListGrantsOnResource`,
  implemented on both backends and covered by the shared `storetest` suite that
  runs on SQLite and Postgres.
- **The check seam.** `internal/authz` with `Resolve` and the `OperatorDomains`
  enumeration helper, resolving `server` and `domain` today (superadmin
  short-circuit, explicit grant rows, domain-operator equivalence), default-deny
  and fail-closed on store error.
- **One migrated call site (proof).** `protoadmin.ResolveOperatorScope` — the
  function backing the domain-scoped observability filter (audit log, system
  events, queue, message research) — now derives its domain set through
  `internal/authz` instead of reading `principal_managed_domains` directly. Its
  behaviour is preserved bit-for-bit (see below), so the substrate is exercised
  end to end with no privilege change.

Deferred to follow-on work under #182 and to #186/#188:

- Grant-admin REST + CLI (create/revoke bounded by the actor's delegable
  authority, REQ-AC-05) and the `whoami` grants summary (REQ-AC-14). The store
  can already write grants; the admin surface layers on next.
- Authorization groups (REQ-AC-80..89). `subject_kind` is in the schema; the two
  group tables and the membership escalation rails are a later step.
- The `list` and `mailbox` resolution branches (implicit mailbox ownership,
  domain->list containment). `Resolve` honours explicit grant rows for every kind
  today; the structural branches land with #186 and the mailing-list work.
- The per-request resolution cache and the per-principal authz epoch. The
  migrated call site already queries the store per request exactly as before, so
  omitting the cache is not a regression; it is an optimisation added when a
  long-lived session path consumes `Resolve`.
- IdP reconciliation, claim-mapping rules, and the staleness sweep (#188).

## Proof-site equivalence

`ResolveOperatorScope(caller)` today returns:

- not `PrincipalFlagAdmin` -> `{Domains: []}` (fail-closed empty);
- `Admin` + `SuperAdmin` -> `{SuperAdmin: true}`;
- `Admin` only -> `{Domains: ListManagedDomains(caller)}`, non-nil, fail-closed to
  empty on error.

The migrated version resolves the same three outcomes through `internal/authz`.
`authz.OperatorDomains` returns the union of the caller's `domain` grant rows and
`principal_managed_domains`. Because migration `0079` back-fills a `domain:operator`
grant for every managed-domain row, that union equals `ListManagedDomains` at
upgrade time; the superadmin outcome is decided by the same
`Admin && SuperAdmin` flag test. The union can only add domains later through the
new grant path (the intended extension), never remove one, so no caller loses
access. The compatibility leg over `principal_managed_domains` remains until the
operator-assignment write path is migrated onto grants in a follow-on step, which
keeps the existing operator-admin endpoints and their tests correct in the
interim.

## Package boundaries

- `internal/store` owns the `grants` table, the `Grant` types, and the repository
  methods (SQLite + Postgres parity, `storetest` on both).
- `internal/authz` owns `Resolve`, the resource/level vocabulary, and (with #188)
  `ReconcileIdP`. It imports `store` and sits above it; it uses the typed
  `store.PrincipalFlag*` constants directly rather than duplicating the flag
  literals the backends carry.
- Each `proto*` surface imports `internal/authz` and calls `Resolve` at its
  resource-touch points. `internal/directoryoidc` calls `ReconcileIdP` (#188).
  The bearer-token / session authentication code in `internal/directory` is not
  touched by this substrate.

## Cross-references

- `../requirements/07-access-control.md` — the authoritative grant model (`REQ-AC-`).
- `../architecture/15-access-control.md` — storage, resolution, IMAP mapping, IdP
  reconciliation.
- `02-phasing.md` — where access control sits in the overall build order.
