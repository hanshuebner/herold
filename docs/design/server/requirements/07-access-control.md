# 07 — Access control (authorization)

herold's authorization model. This document is authoritative for **who may do
what to which resource**. It reconciles three overlapping mechanisms that grew
up separately — the fixed `user`/`admin`/`superadmin` roles (REQ-AUTH-60), the
domain "owner" (REQ-AUTH-80), and the domain-scoped operator (REQ-ADM-307) — into
one model, and it is the substrate the mailing-list archive sharing
(`28-mailing-lists.md`) and shared mailboxes (REQ-PROTO-33) build on.

`02-identity-and-auth.md` remains authoritative for **authentication** (who you
are: credentials, 2FA, sessions, OIDC login, elevation). This document is
authoritative for **authorization** (what your identity is permitted to do). The
`REQ-AUTH-SCOPE-*` scope-and-elevation mechanism is retained unchanged as the
enforcement gate; what changes is what the gate consults — resource **grants**
instead of a global role attribute.

Prefix `REQ-AC-`. herold deliberately stays coarse-grained: a small fixed set of
access levels per resource kind, not a per-operation permission matrix
(`02-identity-and-auth.md`: "Stalwart has ~80 permissions with role inheritance.
We simplify").

## The model

Authorization is expressed as **grants**. A grant binds a **subject** to an
access **level** on a **resource**:

```
  grant { subject, resource_kind, resource_id, level, provenance, ... }
```

The subject is a principal or an **authorization group** (a named set of
principals — REQ-AC-80). Most grants name a principal; a group subject lets one
authority set apply to many principals and be changed in one place. A group is a
set of subjects, not a permission bundle — permissions are still grants — so it
does not reintroduce the stored role attribute REQ-AC-01 forbids.

There are four resource kinds, each with a small fixed level set:

| Resource kind | Levels (low -> high) | What the top level controls |
|---|---|---|
| **server** | `superadmin` | The whole node: server config reload, TLS certs, shutdown, directory config, creating/removing other server grants. |
| **domain** | `operator`, `owner` | A domain: aliases, catch-all, DKIM rotation, principals and lists on the domain, and the domain-scoped observability of REQ-ADM-307. `owner` additionally transfers/deletes the domain and assigns its operators. |
| **list** | `moderator`, `owner` | A mailing list: `moderator` approves held posts; `owner` edits config and roster and assigns moderators. |
| **mailbox** | `read`, `write`, `admin` | A mailbox: `read` (select/fetch/search), `write` (append/store/expunge), `admin` (set ACLs). The RFC 4314 rights map onto these three tiers. |

| ID | Requirement |
|----|-------------|
| REQ-AC-01 | Authorization is the union of the acting principal's grants and the grants of any authorization group it belongs to (REQ-AC-80). A principal with no grants beyond its own resources is an ordinary end user; there is no separate stored "role" attribute. "Being an admin" is emergent: a principal is an administrator of something iff it holds an `operator`/`owner`/`admin`/`superadmin` grant on some resource, directly or via a group. |
| REQ-AC-02 | Every resource has exactly one implicit `owner`-equivalent by construction: a principal owns its own principal-scoped resources (its mailboxes, its identities). A domain has one `owner` (REQ-AUTH-80 preserved). A list has one `owner`. `server:superadmin` is the site root. These implicit owners need no explicit grant row; grants express access **granted to other principals**. |
| REQ-AC-03 | Access levels within a kind are totally ordered; a higher level implies every lower level's capabilities on that resource (`domain owner` implies `operator`; `mailbox admin` implies `write` implies `read`). There is no cross-kind inheritance. |
| REQ-AC-04 | The `server:superadmin` grant is the only site-wide authority. The bootstrap principal receives it (REQ-AUTH-61 preserved: at least one `superadmin` always exists; removing the last is rejected). `server:superadmin` implies the ability to act on every domain, list, and mailbox on the node. |
| REQ-AC-05 | Grants are stored in the metadata store (SQLite + Postgres parity), not in `system.toml` (the never-mutated-at-runtime rule holds). Grant create/revoke is an admin operation, audited per REQ-ADM-300, and is itself authorized: you may grant on a resource only at or below a level you hold with authority to delegate (`owner`/`superadmin`). |

## Enforcement

The `REQ-AUTH-SCOPE-*` gate is retained. Grants change what it evaluates, not how
the request is authenticated.

| ID | Requirement |
|----|-------------|
| REQ-AC-10 | Every handler that touches a resource MUST resolve the acting principal's effective level on that specific resource and reject with 403 (RFC 7807 problem detail) when it is insufficient — the per-handler check of REQ-AUTH-SCOPE-02, generalised from "required scope" to "required (resource, level)". |
| REQ-AC-11 | The session cookie carries end-user scopes only and **never** carries administrative authority (REQ-AUTH-SCOPE-01 preserved). Exercising any grant above the acting principal's own end-user resources for a **mutating** operation requires an active elevation record obtained via TOTP step-up (REQ-AUTH-SCOPE-03 / REQ-AUTH-74). An IdP-asserted grant (below) does not bypass this: the local step-up gate still applies. |
| REQ-AC-12 | Domain/list-scoped observability and management surfaces MUST fail closed — show nothing rather than leak — when the caller's grant set for the resource cannot be resolved (REQ-ADM-307 preserved and generalised from domains to all resource kinds). |
| REQ-AC-13 | API keys carry an explicit grant subset chosen at creation from the creating principal's own grants, immutable thereafter (generalising REQ-AUTH-SCOPE-04; a key can never exceed its creator's authority). The `--allow-admin-scope` affordance becomes "attach an administrative grant to the key" and requires the creator to hold that grant. |
| REQ-AC-14 | The client derives which administrative surfaces to show from the principal's grant summary returned by `whoami` (generalising the `roles` field of REQ-AUTH-75 to a `grants` summary). Visibility is a client hint; the server-side per-resource check (REQ-AC-10) is the authority. |

## Per-resource specifics

### Server

| ID | Requirement |
|----|-------------|
| REQ-AC-20 | `server:superadmin` grants: server config reload, TLS/ACME management, shutdown/restart, directory and OIDC-provider configuration, creation and revocation of any grant (including other `server:superadmin` grants, subject to REQ-AC-04), and unrestricted access to every domain/list/mailbox. This is the "site-wide admin" axis. |

### Domain

| ID | Requirement |
|----|-------------|
| REQ-AC-30 | `domain:operator` grants management of the domain's aliases, catch-all (REQ-AUTH-03), DKIM key rotation, the principals and lists hosted on the domain, and the domain-scoped slice of Queue / System events / Message research / Audit log (REQ-ADM-307). It does NOT include transferring or deleting the domain, or assigning further domain grants. |
| REQ-AC-31 | `domain:owner` is `domain:operator` plus: assign/revoke `domain:operator` grants on the domain, initiate domain cutover (`26-domain-cutover.md`), and delete the domain. REQ-AUTH-80's "domain belongs to exactly one principal (the owner)" is expressed as exactly one `domain:owner` per domain. |
| REQ-AC-32 | Domain ownership proof (DNS-TXT, REQ-AUTH-81) and `superadmin`-creates-without-proof (REQ-AUTH-82) are preserved: a `server:superadmin` may assign a `domain:owner` grant without the DNS challenge; self-service domain claim still requires it. |

### List

| ID | Requirement |
|----|-------------|
| REQ-AC-40 | `list:owner` grants full control of a list: config, roster CRUD, moderation, and assigning `list:moderator` grants. A list's owner is set at creation (the creating `domain:operator`/`owner` by default) and governs `REQ-MLIST-40a`/`REQ-MLIST-42`. |
| REQ-AC-41 | `list:moderator` grants approve/reject/discard of held posts (the moderation milestone, REQ-MLIST-80) and read of the list's roster, without config or roster-write authority. |
| REQ-AC-42 | List administration remains within the domain scope: a `domain:operator` implicitly holds `list:owner` on every list of its domain (REQ-AC-03 cross-kind exception is NOT invoked; this is an explicit containment rule — a domain operator manages its domain's lists by virtue of the domain grant). |

### Mailbox

| ID | Requirement |
|----|-------------|
| REQ-AC-50 | Mailbox grants are the shared-mailbox / cross-principal read substrate. `mailbox:read`/`write`/`admin` are the named tiers onto which the RFC 4314 rights (`l r s w i p k x t e a` ...) map; `SETACL`/`GETACL`/`MYRIGHTS`/`LISTRIGHTS` (REQ-PROTO-33) read and write these grant rows. This realises the phase-2 "schema carries per-mailbox ACL entries" as concrete grant storage, and supersedes the "deferred, separate dimension" framing of REQ-AUTH-63 by giving it a home in this model. |
| REQ-AC-51 | A principal always holds implicit `mailbox:admin` on its own mailboxes (REQ-AC-02); grants express access delegated to *other* principals. |
| REQ-AC-52 | The mailing-list archive (REQ-MLIST-70..73) is a mailbox owned by the list's Group principal with a `mailbox:read` grant issued to each `nomail`/reading member principal. There is no list-specific ACL type; it is an ordinary mailbox grant. |
| REQ-AC-53 | Mailbox grants are enforced uniformly across IMAP (SELECT/STATUS/FETCH/STORE), JMAP (the sharing surface), and the Suite's member read-only mode — one check, three protocols (REQ-PROTO-33's "SELECT/STATUS/fanout respect them; JMAP sharing surface aligned"). |

## External IdP claim-to-grant mapping

herold remains an OIDC **relying party only**, never an issuer (REQ-AUTH-58 / NG11
preserved). OIDC continues to authenticate a login to a local principal via the
`sub` claim (REQ-AUTH-52). What this section adds is **authorization derived from
IdP claims**: an operator MAY configure rules that map a provider's group/role
claims onto herold grants. This is a deliberate extension beyond the prior
authentication-only stance.

| ID | Requirement |
|----|-------------|
| REQ-AC-60 | An operator MAY define, per configured OIDC provider, **claim-mapping rules** `{claim, match, resource_kind, resource_selector, level}` — e.g. `groups contains "list-x-admins" -> list:x owner`, or `groups contains "domain-ops" -> domain:example.com operator`. Rules are DB-backed admin state (not `system.toml`), managed under the domain/server grant of the operator who writes them, and audited. |
| REQ-AC-61 | A grant carries a **provenance**: `local` (assigned by an operator) or `idp:<provider>` (derived from a claim-mapping rule). The two are independent — an `idp:` grant never overwrites or is overwritten by a `local` grant; effective access is the union (REQ-AC-01). |
| REQ-AC-62 | On each successful OIDC login, herold re-evaluates that provider's claim-mapping rules against the presented token and **reconciles** the principal's `idp:<provider>` grants: newly-matched rules add grants, no-longer-matched rules remove them, and each surviving `idp:` grant's `last_asserted_at` is refreshed. `local` grants are never touched by reconciliation. |
| REQ-AC-63 | Deprovisioning has two triggers. (a) A member who logs in without a previously-matched claim loses that `idp:` grant immediately (REQ-AC-62). (b) For members who stop logging in, a periodic sweep removes `idp:` grants whose `last_asserted_at` is older than a configurable staleness window (default bounded, e.g. 30 days), so a revoked-at-the-IdP user does not retain access indefinitely. Unlinking a provider association (REQ-AUTH-55) removes all of that provider's `idp:` grants for the principal immediately. |
| REQ-AC-64 | Safety rails (all default-deny, `server:superadmin`-only to override): a claim-mapping rule MUST NOT be able to confer `server:superadmin`; superadmin is `local`-provenance only, so an IdP compromise cannot seize the node. A rule MAY only target resources the authoring operator controls (you cannot write a rule granting access to a domain/list you do not own). IdP-asserted administrative grants still require local TOTP elevation to *exercise* for mutating operations (REQ-AC-11) — IdP group membership authenticates the assertion, it does not satisfy the local second factor. |
| REQ-AC-65 | Claim-mapping and reconciliation events (grant added/removed via `idp:`, rules created/edited, staleness sweeps) are audited (REQ-ADM-300) with actor `idp:<provider>` for the derived changes, so an operator can see why a principal holds a given grant. |
| REQ-AC-66 | **Authorization trust is separate from authentication trust.** Claim-to-grant mapping is inert for a provider unless a `server:superadmin` has explicitly flagged that provider `authz_trusted`. A provider usable for login is not thereby usable to confer grants; enabling authorization mapping is a distinct, superadmin-only, per-provider decision. |
| REQ-AC-67 | Only claims on a per-provider **authorization-claim allowlist** (operator-set, e.g. `groups`, `roles`) are consulted by mapping rules; a claim not on the allowlist can never satisfy a rule, so a provider that passes through arbitrary or user-influenced claims cannot be used to smuggle authorization. Matching is always qualified by the originating provider — a claim from one provider can never satisfy a rule scoped to another. |
| REQ-AC-68 | A mapping rule is bound to its author's authority and **re-validated at evaluation time**: if the author no longer holds a grant sufficient to confer the rule's target (`owner`/`superadmin` on the resource), the rule is inert and any `idp:` grants it previously produced are swept. An orphaned rule never keeps provisioning access. |
| REQ-AC-69 | Administrative-level mapped grants (any level above the mailbox reading tier, and any `operator`/`owner`/`admin` level) MUST NOT be applied during the same login that auto-provisions a new principal (REQ-AUTH-56). A newly self-provisioned identity receives at most end-user access on its first login; administrative mapped grants require a pre-existing principal, closing the self-provision-and-escalate window. |
| REQ-AC-70 | Reconciliation is **fail-safe and atomic**. If the mapping rules cannot be loaded or a claim cannot be evaluated, the principal's existing `idp:` grants are left unchanged (never mass-revoked on error) and the failure is audited; partial application is not permitted. A transient rules-store outage neither escalates nor locks out. |

## Authorization groups (grant-to-group)

A grant's subject may be a single principal or an **authorization group**: a
named, node-local set of principals. Attaching a grant to a group confers it on
every member, and membership is managed in one place — the proportionate answer
to "the same set of people repeatedly needs the same authority" without a
permission-bundle role. Groups generalise the grant *subject*; permissions
remain grants, so REQ-AC-01's "no stored role attribute" is preserved (a group
is a set of subjects, not a named set of permissions). This is the deliberately
smaller step chosen over reusable roles: it delivers assign-once-to-many and
change-in-one-place while keeping the grant as herold's single authorization
primitive.

| ID | Requirement |
|----|-------------|
| REQ-AC-80 | A grant's **subject** is either a principal or an **authorization group** — a named, node-local set of principals. A grant whose subject is a group confers its (resource, level) on every current member; effective authorization (REQ-AC-01) is the union over the principal's own grants and the grants of every group it belongs to, with the per-kind total order (REQ-AC-03) selecting the highest level. A group carries no authority except the grants attached to it. |
| REQ-AC-81 | Authorization groups are **distinct from the mailing-list Group principal** (REQ-AC-52, `28-mailing-lists.md`): the two rosters are separate and neither confers the other. Distribution-list membership never grants authorization, and authz-group membership never subscribes a principal to mail. |
| REQ-AC-82 | Groups are **flat** — members are principals only; a group cannot contain another group (the no-inheritance-trees non-goal holds). Groups, their manager, and their memberships are `local`-provenance state in the metadata store (SQLite + Postgres parity), managed via the admin REST API + CLI and audited (REQ-ADM-300), never in `system.toml`. |
| REQ-AC-83 | Two **independent** authorizations govern a group. (a) *Attaching or revoking a grant on a group* is REQ-AC-05 unchanged — the actor must hold delegable authority (`owner`/`superadmin`) on that resource; the subject being a group does not relax it. (b) *Managing membership and the group itself* (add/remove members, rename, delete, reassign the manager) requires being the group's **manager** or a `server:superadmin`. The manager is set at creation (the creator by default) and is reassignable by the current manager or a superadmin. |
| REQ-AC-84 | **Adding a member is bounded by the group's conferred authority.** To add a principal to a group, the actor MUST additionally hold, for *every* grant the group carries, the delegable authority to confer that grant directly (REQ-AC-05). A manager who could not grant one of the group's grants directly cannot add members to that group — closing the escalation in which a low-authority manager mints high authority by adding members (mirrors REQ-AC-13, "a key can never exceed its creator's authority"). Removing a member requires only manager authority (reducing access is always safe). Attaching a grant to a non-empty group requires that same resource authority and is audited as conferring the grant on every current member. |
| REQ-AC-85 | `server:superadmin` **MUST NOT** be attached to a group (parallel to REQ-AC-64's bar on IdP-derived superadmin); it is held only directly by a principal with `local` provenance. The "at least one superadmin always exists" invariant (REQ-AC-04) is evaluated over direct superadmin grants only, so no group edit can remove the last root. |
| REQ-AC-86 | Group-derived administrative grants **still require local TOTP elevation to exercise** for mutating operations, exactly as direct grants do (REQ-AC-11). A `domain:operator` held via a group determines *which* authority applies; it does not satisfy the second factor. |
| REQ-AC-87 | The `whoami` grant summary (REQ-AC-14) **labels each effective grant with its source** — `direct`, `idp:<provider>`, or `group:<name>` — so a principal and an auditing operator can see that e.g. `domain:operator on example.com` is held via group `Board`, not directly. Visibility is a client hint; REQ-AC-10's per-resource server check remains the authority. |
| REQ-AC-88 | **Lifecycle is closed.** Deleting a group removes its grants and memberships in one transaction and the access it conferred evaporates immediately; deleting a principal removes its memberships. A membership change or a change to a group's grants bumps the authz epoch of every affected member so long-lived IMAP/JMAP sessions re-resolve. Fail-closed (REQ-AC-12) applies: if a principal's memberships or a group's grants cannot be resolved, the surface shows nothing. |
| REQ-AC-89 | Group create/delete/rename, manager reassignment, membership add/remove, and grant attach/revoke on a group are **audited** (REQ-ADM-300). Because a membership change silently alters a member's effective authority without touching any grant on that principal, membership events record both the group and the affected principal, so "why does this principal hold this grant" (REQ-AC-65) resolves through the group. |

## Amendments to existing requirements

This model supersedes or amends the following. These are called out so the older
text is not read as still-current; the amendments themselves are applied as
superseding notes in the source documents.

| Existing | Disposition |
|---|---|
| REQ-AUTH-60 (fixed `user`/`admin`/`superadmin` roles) | **Superseded.** Roles become emergent from grants (REQ-AC-01). `superadmin` survives as `server:superadmin`; `admin`/`user` cease to be stored attributes. |
| `02-identity-and-auth.md` non-goal "Fine-grained permissions beyond the three roles" | **Amended.** Replaced by grant-per-resource; still bounded (no per-operation matrix — REQ-AC intro). |
| `08-admin-and-management.md:140` non-goal "Delegated admin with scoped permissions (phase 3)" | **Lifted.** Delegated scoped admin is now in scope and is the grant model; REQ-ADM-307 was already an instance of it. |
| REQ-ADM-307 (super-admin + domain-scoped operator) | **Generalised.** Domain-scoped operator = `domain:operator` grant; super-admin = `server:superadmin`. The principal-to-domain association becomes a grant row. Fail-closed behaviour preserved (REQ-AC-12). |
| REQ-AUTH-80 (one domain owner) | **Restated** as exactly one `domain:owner` per domain (REQ-AC-31); ownership grants unchanged. |
| REQ-PROTO-33 / REQ-AUTH-63 (IMAP ACL, shared mailboxes, deferred) | **Realised.** The "per-mailbox ACL entries" schema is the mailbox-grant storage (REQ-AC-50); ACL is no longer a separate deferred dimension. Timing remains phase-2. |
| REQ-OPS-ADMIN-LISTENER-01/03 (cookie carries `ScopeAdmin` from login) | **Corrected as stale.** The 2026-06-28 scope redesign (REQ-AUTH-SCOPE-01) governs: the cookie never carries administrative authority; elevation is the sole admin authorization (REQ-AC-11). |
| REQ-AUTH-50..58 (OIDC authentication-only) | **Extended.** Authentication behaviour unchanged; claim-to-grant mapping (REQ-AC-60..65) added as an opt-in authorization layer. RP-only / NG11 preserved. |

## Non-goals

- **No per-operation permission matrix.** Levels are per-resource-kind and few; herold does not model ~80 discrete rights or role inheritance trees.
- **No per-message or per-thread ACL.** The finest granularity is the mailbox.
- **No multi-tenant isolation (NG3).** Grants delegate within one node; they are not a tenancy boundary.
- **`server:superadmin` is never IdP-derivable.** (REQ-AC-64.)
- **No `deny` grants in v1.** Grants are additive; to remove access, remove the grant or the mapping rule. A local override-deny is a possible future extension, not v1.
- **No permission-bundle roles.** grant-to-group (REQ-AC-80..89) assigns one authority set to many principals by generalising the grant *subject*; it does not introduce a named bundle of permissions assigned to principals. REQ-AC-01's "no stored role attribute" stands.
- **No nested groups.** Authorization groups are flat (REQ-AC-82); a group's members are principals only.
- **`server:superadmin` is never group-derivable.** (REQ-AC-85, parallel to the IdP bar.)
- **IdP claim-mapping targets grants, not group membership (v1).** A mapping rule confers a grant directly (REQ-AC-60); mapping an IdP claim onto authz-group membership is a possible future composition, not v1.

## Cross-references

- `02-identity-and-auth.md` — authentication, sessions, elevation (REQ-AUTH-SCOPE-*, REQ-AUTH-74), OIDC login (REQ-AUTH-50..58), domain ownership proof (REQ-AUTH-81/82).
- `08-admin-and-management.md` — admin surfaces, audit (REQ-ADM-300), the domain-scoped operator origin (REQ-ADM-307).
- `01-protocols.md` — IMAP RFC 4314 ACL (REQ-PROTO-33) realised as mailbox grants.
- `28-mailing-lists.md` — list ownership/moderation and the archive read-grant that consume this model.
- `architecture/15-access-control.md` — storage, resolution, and enforcement seam.
