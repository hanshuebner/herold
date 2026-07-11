# 15 — Access control

How the grant model (`../requirements/07-access-control.md`, `REQ-AC-`) is
stored, resolved, and enforced. Authentication (sessions, elevation, OIDC login)
is `02-identity-and-auth.md` and `03-protocol-architecture.md`; this doc is the
authorization layer that sits behind the existing scope gate.

## Where it sits

The scope-and-elevation gate already runs on every request
(`03-protocol-architecture.md`, REQ-AUTH-SCOPE-*). Access control adds one step
inside it: after the request is authenticated and its end-user scopes checked,
the handler asks an **authorizer** for the acting principal's effective level on
the specific resource it is about to touch.

```
  request ─► authenticate ─► scope check (end-user) ─► handler
                                                          │
                                                   authz.Resolve(principal, resource)
                                                          │
                                              level >= required?  ── no ─► 403 (RFC 7807)
                                                          │ yes
                                              mutating + admin level? ── yes ─► elevation record present?
                                                          │                          │ no ─► 403 step_up_required
                                                          ▼                          ▼ yes
                                                       proceed  ◄────────────────────┘
```

The authorizer is a single package (`internal/authz`) consulted by SMTP, IMAP,
JMAP, the admin REST API, and the mailing-list code — no protocol grows its own
permission logic.

## Storage

One grant table in the metadata store (SQLite + Postgres parity):

```
  grant(
    id            u64 primary,
    principal_id  fk,
    resource_kind text,        -- server | domain | list | mailbox
    resource_id   text,        -- '' for server; domain name; list id; mailbox id
    level         text,        -- per-kind enum (07-access-control.md)
    provenance    text,        -- 'local' | 'idp:<provider>'
    granted_by    fk,          -- actor principal, or null for idp-derived
    granted_at    ts,
    last_asserted_at ts null,  -- idp-derived only; drives the staleness sweep
    unique(principal_id, resource_kind, resource_id, provenance)
  )
  index (resource_kind, resource_id)      -- "who can touch this resource"
  index (principal_id)                    -- "what can this principal touch" (whoami)
```

`local` and `idp:<provider>` grants for the same (principal, resource) are
distinct rows (the unique key includes provenance), so reconciliation of IdP
grants never disturbs a local grant (REQ-AC-61). Implicit ownership
(REQ-AC-02) is not stored — a principal's own mailboxes and a domain/list's sole
owner are resolved structurally, not via grant rows.

## Resolution

`authz.Resolve(principal, resource) -> level` computes the effective level as the
max over:

1. **`server:superadmin`?** -> implies top level on every resource. Short-circuit.
2. **Implicit ownership** — resource is principal-owned (own mailbox; sole
   `domain:owner`/`list:owner`) -> owner/admin level.
3. **Containment** — a `domain:operator`/`owner` implies `list:owner` on that
   domain's lists and the relevant management level on the domain's mailboxes
   (REQ-AC-42). Containment is domain -> {its lists, its principals' admin
   surfaces}, not a general cross-kind inheritance.
4. **Explicit grants** — union of `local` + `idp:*` grant rows for
   (principal, resource), taking the highest `level` (REQ-AC-03 total order).

Resolution is a small number of indexed lookups; results are cached per request
(an authorized session touches the same few resources repeatedly). Grant writes
bump a per-principal authz epoch so long-lived IMAP/JMAP sessions re-resolve
rather than serve a stale cache.

## IMAP RFC 4314 mapping

`SETACL`/`GETACL`/`MYRIGHTS`/`LISTRIGHTS` (REQ-PROTO-33) are a wire front-end over
mailbox grants (REQ-AC-50). The RFC 4314 rights string collapses onto the three
tiers:

```
  read  (l r s)         -> mailbox:read
  write (i k x t e w p)  -> mailbox:write   (implies read)
  admin (a)              -> mailbox:admin   (implies write)
```

`GETACL` renders grant rows back as rights strings; `SETACL` writes a grant row
at the tier the requested rights imply. herold does not store the full
per-letter bitmask — the three-tier collapse is the stored truth, and the wire
representation is derived. (Fidelity note: clients that set an unusual letter
subset get the enclosing tier; this is the documented simplification.)

## IdP claim reconciliation

On successful OIDC login, `internal/directoryoidc` hands the validated token's
claims to `authz.ReconcileIdP(principal, provider, claims)`:

1. Load the provider's claim-mapping rules (DB-backed admin state, REQ-AC-60).
2. Evaluate each rule against the claims to produce the **desired** `idp:<provider>`
   grant set, filtered through the safety rails (REQ-AC-64: drop any rule
   targeting `server:superadmin` or a resource outside the rule author's
   authority).
3. Diff against the principal's current `idp:<provider>` rows: insert new,
   delete no-longer-matched, refresh `last_asserted_at` on survivors. `local`
   rows untouched.
4. Emit audit events with actor `idp:<provider>` (REQ-AC-65).

A periodic **staleness sweep** (a maintenance worker) deletes `idp:*` grants
whose `last_asserted_at` is older than the configured window (REQ-AC-63), closing
the "revoked at the IdP but never logs in again" gap. Unlink of a provider
association deletes that provider's `idp:*` rows synchronously.

Reconciliation changes *what grants exist*; it never changes the elevation gate.
An IdP-derived `domain:operator` still cannot perform a mutating admin action
without a live TOTP elevation record (REQ-AC-11 / REQ-AC-64).

## Interaction with elevation

The elevation record (REQ-AUTH-74) is orthogonal to grants: grants answer "is
this principal permitted this resource+level at all"; elevation answers "has this
principal proven a second factor recently enough to *exercise* administrative
authority in a mutating way". Both must pass for a mutating admin operation; a
read of an administrative surface needs the grant but not (per REQ-AUTH-SCOPE-03's
read/mutate split) fresh elevation.

## Cross-references

- `../requirements/07-access-control.md` — the requirements realised here.
- `02-identity-and-auth.md` — REQ-AUTH-SCOPE-* gate, elevation, OIDC login.
- `01-protocols.md` — REQ-PROTO-33 IMAP ACL surface.
- `03-protocol-architecture.md` — the listener/session/scope pipeline this hooks into.
- `14-mailing-lists.md` — the list archive read-grant consumer.
