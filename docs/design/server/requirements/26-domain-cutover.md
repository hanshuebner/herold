# 26 — Domain cutover (provider migration to herold)

*(Added 2026-06-27. Covers the org-level adoption journey: a provider that
owns a domain trials herold with a few users, then decides to move the
whole domain onto herold and migrate every mailbox. Builds on
`19-imap-import.md` (per-identity IMAP import + complete-migration cutover)
and `02-identity-and-auth.md` (external-transport identities). The "how"
extends `architecture/12-imap-import.md`. Operator guide:
`docs/manual/admin/domain-cutover.mdoc`.)*

## Motivating use case

1. **Trial.** An organisation owns `acme.com` and runs an existing mail
   server. It stands up herold (on a trial/eval domain, e.g.
   `mail.acme-trial.com`, or any domain it already controls). A few users
   get herold principals at the trial address.
2. **Per-identity bridge.** Each trial user adds their real address
   `user@acme.com` to herold as an **external-transport identity**
   (`02-identity-and-auth.md` REQ-AUTH-EXT-SUBMIT-12): external SMTP for
   sending, optional IMAP import for receiving (`19-imap-import.md`). They
   read and send their `acme.com` mail through herold while `acme.com` MX
   still points at the legacy server.
3. **Cutover.** The org decides to move `acme.com` onto herold entirely.
   The administrator adds `acme.com` as a hosted domain, migrates **all**
   mailboxes (adopters and non-adopters alike) from the legacy server, and
   flips MX to herold. Every user ends up with a native herold account for
   `user@acme.com` holding their full history.

The hard parts this file specifies: (a) migrating non-adopter mailboxes
the admin has no per-user credential for; (b) reconciling the adopters,
whose `acme.com` mail is already partly mirrored, without duplicating or
mis-claiming addresses; (c) converting an external identity into a native
one safely.

## Decisions

1. **Re-home adopters.** When an adopter's domain is cut over, their
   existing (trial) principal **becomes** the `user@acme.com` account:
   `user@acme.com` is promoted to the principal's primary/login address;
   their mirrored mail, settings, filters, and Sieve carry over unchanged.
   The trial address is retained as a secondary alias by default (the admin
   MAY drop it). The user keeps one herold account across the transition;
   no second account is created for them. (REQ-CUTOVER-30..34.)
2. **Domain-level migration credential.** The administrator configures one
   **delegated/master credential** per legacy source (Google Workspace
   domain-wide delegation, a Dovecot/Cyrus master user, or an IMAP admin
   login) so herold can pull every user's mailbox without collecting
   per-user passwords. The bulk migration drives the `19-imap-import.md`
   worker pool, authenticating as each user via the master credential.
   (REQ-CUTOVER-10..14.)
3. **Operator-authorised, never auto-claim.** Adding `acme.com` as a hosted
   domain does NOT silently convert any trial user's self-asserted external
   `acme.com` identity into a native delivery address. Conversion is an
   explicit, reviewed administrator action. A trial user cannot pre-claim
   `ceo@acme.com` by adding it as an external identity. (REQ-CUTOVER-40..43.)

## Domain migration source

| ID | Requirement |
|----|-------------|
| REQ-CUTOVER-10 | The administrator MAY register a **migration source** for a domain being onboarded: `{domain, imap_host, imap_port, tls_mode, auth_kind, credential}` where `auth_kind` is one of `google_domain_delegation`, `imap_master_user` (authenticate as the master principal, then authorise as the target user via SASL `authzid` / proxy login), or `imap_admin_login` (a single admin account that can `SELECT` other users' mailboxes). The credential is sealed at rest with `internal/secrets` (the same data key and `*_ct` pattern as REQ-IMAP-IMP-70 / `store.IdentitySubmission`); plaintext is never persisted and is held only for the duration of a connect. |
| REQ-CUTOVER-11 | The migration source is **system/admin-scoped**, not per-principal: it is created and used only by an administrator on the admin listener, never exposed on a principal's self-service JMAP surface. It exists only for the duration of the migration and SHOULD be deleted afterward; `herold` warns if a source outlives its migration job by more than a configurable grace period. |
| REQ-CUTOVER-12 | For `google_domain_delegation`, herold exchanges the service-account JWT for a per-user access token (`https://mail.google.com/` scope) impersonating each `user@domain`, exactly as a per-identity `xoauth2` import would but with the credential supplied once at the domain level. For `imap_master_user` / `imap_admin_login`, herold connects once per user and assumes that user's mailbox per the server's master-user convention. The per-user connection reuses the entire `19-imap-import.md` download path (horizon, dedup, as-synced ingest, folder mapping). |
| REQ-CUTOVER-13 | The migration source connection MUST use TLS (REQ-IMAP-IMP-06). A source that cannot authenticate as a sample user at registration time is rejected with a categorised error, mirroring the per-identity probe gate (REQ-AUTH-EXT-SUBMIT-11). |
| REQ-CUTOVER-14 | The migration credential is never logged and never returned on read; the admin surface shows only `{auth_kind, configured: true, last_used_at, state}` (mirrors REQ-IMAP-IMP-71 / REQ-AUTH-EXT-SUBMIT-04). |

## Bulk provisioning and migration

| ID | Requirement |
|----|-------------|
| REQ-CUTOVER-20 | The administrator supplies (or herold enumerates from the source, where the protocol allows listing accounts) the set of `user@domain` mailboxes to migrate. For each, herold **provisions or reconciles** a native principal (REQ-CUTOVER-30 decides provision-vs-reconcile) and creates a migration job that runs the per-mailbox **complete migration** of REQ-IMAP-IMP-90..96 (full backfill, authority transferred to herold, upstream retired) driven by the domain migration source. |
| REQ-CUTOVER-21 | The bulk migration is a **batch with per-mailbox isolation and observability**: one slow or failing mailbox does not block the others (REQ-IMAP-IMP-26); each mailbox exposes the live worker snapshot (REQ-IMAP-IMP-65) and the per-account metrics (REQ-IMAP-IMP-63). The batch surfaces an aggregate progress view (mailboxes pending / migrating / done / errored, total `backfill_remaining`). |
| REQ-CUTOVER-22 | The batch is **resumable and idempotent**: re-running it skips mailboxes already `migrated`, resumes those mid-flight from their cursors (REQ-IMAP-IMP-94), and dedups by Message-ID / blob_hash so a re-run never duplicates mail (REQ-IMAP-IMP-30/35). A herold restart mid-batch resumes cleanly (REQ-IMAP-IMP-74). |
| REQ-CUTOVER-23 | New native principals provisioned by the batch get a default quota, a forced-password-reset / invite flow, and the standard mailbox set; their initial credential issuance follows the existing principal-provisioning surface (REQ-ADM-10). Migration does not invent a parallel account-creation path. |

## Adopter reconciliation and identity conversion

| ID | Requirement |
|----|-------------|
| REQ-CUTOVER-30 | Before provisioning, the batch **detects adopters**: principals that already own a verified external-transport `Identity` whose email is `user@domain`. For a detected adopter, the batch **reconciles in place** (re-home, decision 1) — it does NOT create a second principal for `user@domain`. For every other `user@domain`, it provisions a fresh native principal. The detection + the provision-vs-reconcile decision per address is shown in the pre-migration plan (REQ-CUTOVER-50). |
| REQ-CUTOVER-31 | **Re-home.** Reconciling an adopter promotes `user@domain` to the principal's primary/canonical address (the address its synthesised default identity derives from, REQ-IDENT-02) and demotes the former trial address to a secondary alias (REQ-ADM-10 `/aliases`), unless the admin opts to drop it. The principal's mirrored `user@domain` mail, settings, filters, Sieve, labels, and the import provenance label (REQ-IMAP-IMP-100) all carry over unchanged — it is the same principal row. |
| REQ-CUTOVER-32 | **Finalize the existing mirror.** If the adopter's identity has a running IMAP import, the reconcile drives it to `migrated` via the same complete-migration path (REQ-IMAP-IMP-90..96) rather than starting a fresh pull — the already-mirrored mail is reused and only the gap is fetched. If the adopter had no IMAP import (external SMTP only, inbound via forwarding), the reconcile starts a complete migration through the domain migration source (REQ-CUTOVER-12). |
| REQ-CUTOVER-33 | **Convert external -> native.** On reconcile the `user@domain` identity is reclassified native (its domain is now hosted, REQ-IDENT-21): its external SMTP submission config is removed (herold now sends natively and DKIM-signs for the hosted domain, REQ-CUTOVER-60), its IMAP import config is retired with the mirror (REQ-IMAP-IMP-93), and its verification state is preserved (control was already proven during the trial — no re-verification). |
| REQ-CUTOVER-34 | Imported mail keeps its provenance label across re-home (REQ-IMAP-IMP-100/104); the admin MAY offer the user the keep-or-delete choice (REQ-IMAP-IMP-102) but the cutover default is **keep** — the whole point was to bring the mail across. |

## Uniqueness and authorisation

| ID | Requirement |
|----|-------------|
| REQ-CUTOVER-40 | **Delivery uniqueness invariant.** After `domain` is hosted, each `user@domain` MUST resolve to exactly one delivery target (one principal, as its primary address or an alias). The migration tooling enforces this: it refuses to create a native principal for an address already claimed (as primary, alias, or a converted identity) by another principal, surfacing the conflict for the admin to resolve (typically: that address is an adopter -> reconcile, not provision). |
| REQ-CUTOVER-41 | A self-asserted external `Identity` for `user@domain` confers **no claim** on the native address. Until the administrator reviews and approves the conversion (REQ-CUTOVER-50), the address remains unassigned in the hosted namespace. This closes the pre-claim attack: a trial user adding `ceo@acme.com` as an external identity cannot thereby receive `ceo@acme.com` native mail. |
| REQ-CUTOVER-42 | Conversion and re-home are **admin-listener operations** (REQ-AUTH-SCOPE-\*); no principal self-service surface can trigger them. Every conversion, provision, and alias change emits an audit-log entry (REQ-ADM audit) naming the admin, the address, the affected principal(s), and the action. |
| REQ-CUTOVER-43 | If two principals both assert an external identity for the same `user@domain`, the tooling refuses to auto-reconcile and surfaces the ambiguity; the admin picks the canonical principal (or neither, provisioning fresh). herold never merges principals automatically. |

## Pre-migration plan and execution

| ID | Requirement |
|----|-------------|
| REQ-CUTOVER-50 | The migration runs **plan-then-apply**. A dry-run plan lists, per `user@domain`: provision-vs-reconcile, the detected adopter principal (if any), the trial address that would be demoted, mailbox size / message-count estimate from the source, and any uniqueness conflict (REQ-CUTOVER-40). The admin reviews and approves before any write. |
| REQ-CUTOVER-51 | **Sequencing.** The recommended order, encoded by the tooling and the operator guide: (1) add `domain` as hosted with DKIM/MTA-STS/etc. published (going-live), but keep MX on the legacy server; (2) run the migration in mirror mode (legacy stays authoritative, herold pulls continuously) so users keep working; (3) when `backfill_remaining` across the batch is ~0 and verified, flip MX to herold; (4) finalize — drive every mailbox to `migrated` (authority to herold, upstream retired). Dual delivery during the window is deduped (REQ-IMAP-IMP-30). |
| REQ-CUTOVER-52 | **Rollback safety.** Until step (4) finalize, the legacy server remains the source of truth and reachable; a `migrated` account can still be re-opened to mirroring (REQ-IMAP-IMP-95) if the cutover is aborted. The tooling does not delete or alter anything on the legacy server. |

## Send-path and DKIM transition

| ID | Requirement |
|----|-------------|
| REQ-CUTOVER-60 | Once `domain` is hosted and DKIM keys are published (going-live), outbound for `user@domain` is signed and sent natively by herold (REQ-DKIM-\*). For a reconciled adopter the external SMTP submission is removed at conversion (REQ-CUTOVER-33), so there is no window where both paths are active. Operators are advised (operator guide) to keep DKIM published and MX legacy during the mirror window so inbound DMARC still aligns at the legacy server until the MX flip. |

## Surfaces

| ID | Requirement |
|----|-------------|
| REQ-CUTOVER-70 | Admin CLI under the existing `herold admin` namespace, e.g. `herold admin domain migrate <domain> --source <id> [--plan] [--apply] [--users <file>] [--drop-trial-alias]`, plus `herold admin migration-source {add,list,show,delete}`. Migration is also driven from the admin REST surface (admin listener) so a future admin UI can render the plan and progress. |
| REQ-CUTOVER-71 | The batch reuses the IMAP-import observability (REQ-IMAP-IMP-62/63/65): `herold imapimport status` shows every per-mailbox worker; a migration-level view aggregates the batch. |

## Out of scope

- **REQ-CUTOVER-80** — Automatic discovery of the legacy user list where the
  source protocol cannot enumerate accounts (no IMAP standard for "list all
  mailboxes"). The admin supplies the user list in that case (REQ-CUTOVER-20).
- **REQ-CUTOVER-81** — Migrating non-mail state (calendar, contacts, server-side
  filters/rules) from the legacy provider. Mail only; calendar/contacts get
  their own importers when those features land.
- **REQ-CUTOVER-82** — Principal **merge** (two pre-existing herold principals
  that both deserve `user@domain`). The tooling detects and refuses (REQ-CUTOVER-43);
  a guided merge is a future iteration.
- **REQ-CUTOVER-83** — Live coexistence where `domain` is split (some mailboxes
  on herold, some on legacy) long-term. The design targets a one-way cutover,
  not a permanent split-domain topology.
