# PostfixAdmin vs herold admin surface — feature parity

## Purpose

This document compares the feature set of PostfixAdmin (the long-running
PHP web GUI for managing a Postfix + Dovecot deployment with SQL-backed
virtual users) against herold's admin surface — the CLI shipped today
plus the planned admin SPA and REST API. The intended reader is the
herold maintainer: the table flags features a PostfixAdmin operator
would expect to find, which of those herold already covers, which it
covers under a different shape, and which it has deliberately ruled
out. It is intentionally scoped to operator/admin and end-user
self-service surfaces; mail engine internals are out of scope.

Source material: PostfixAdmin commit on master as cloned 2026-05-12
(`/tmp/postfixadmin-research`), specifically the model handlers under
`model/*.php`, the page list under `public/*.php` and `templates/*.tpl`,
the menu wiring in `configs/menu.conf`, and the developer notes in
`DOCUMENTS/`. Herold source material: `docs/design/00-scope.md`, the
`docs/design/server/requirements/*.md` and
`docs/design/web/requirements/*.md` trees, and the cobra-based CLI
under `internal/admin/cmd_*.go`.

## At a glance

| Concern | PostfixAdmin | herold today | herold planned |
|---|---|---|---|
| Top-level UI | PHP web app | CLI (`herold admin ...`) + minimal HTMX `protoui` at `/ui/` | Svelte SPA at `/admin/` (ADR-0001), replaces protoui in one PR |
| Hosted domains | `DomainHandler`: create, quotas, transport, backupmx, password-expiry default | `herold domain {add,remove,list}` (REQ-ADM-11) | REST `/api/v1/domains` + SPA |
| Mailboxes / principals | `MailboxHandler`: per-mailbox quota, password, "smtp_active", welcome mail | `herold principal {create,delete,list,show,disable,enable,quota,grant-admin,revoke-admin,set-password,totp}` (REQ-ADM-10) | REST `/api/v1/principals` + SPA |
| Aliases | `AliasHandler` + `AliasdomainHandler`: address→address list, catch-all, domain→domain rewrites | `herold alias {add,list,delete}` (REQ-AUTH-02..04) | Same as today; alias subresource on `/api/v1/principals` |
| Vacation / autoresponder | `VacationHandler`: subject, body, active-from / active-until | Sieve `vacation` + JMAP `VacationResponse` (REQ-PROTO-46, REQ-SET-09) | Suite Settings UI exposes it (REQ-SET-09) |
| Fetchmail (pull from remote POP3/IMAP) | `FetchmailHandler`: full pull-from-remote configuration | Not present | **Out of scope by design** — only one-shot Gmail Takeout import (REQ-IMPORT-*) and external SMTP submission per Identity (REQ-AUTH-EXT-SUBMIT-*); no IMAP/POP3 pull worker |
| DKIM key management | `DkimHandler` + `DkimsigningHandler`: per-domain key + per-author signing-table | `herold dkim {generate,show}`; DNS-published via DNS plugin | DKIM auto-rotate UI; planned DNS-publish via plugin (G6, REQ-PLUG-*) |
| Domain-scoped admin (delegated) | `AdminHandler`: superadmin grants a non-superadmin admin to N domains | Not implemented. Three flat roles only: `user` / `admin` / `superadmin` (REQ-AUTH-60) | Phase 3 candidate (REQ-ADM "Out of scope" list); REQ-AUTH-80..82 sketch a domain-owner model but no delegated admin |
| App passwords | `app-passwords.php`: per-mailbox named tokens | REQ-AUTH-30..32 (CLI / REST surface; bypasses 2FA for IMAP/SMTP) | Suite settings exposes management |
| TOTP 2FA | `totp.php` + `totp-exceptions.tpl` (per-IP exemptions) | TOTP at admin / JMAP login (REQ-AUTH-40..43); admin step-up (REQ-AUTH-SCOPE-03); `herold principal totp {enroll,disable}` | WebAuthn deferred (REQ-AUTH-41 phase 2); per-IP exemption — no equivalent |
| Broadcast email | `broadcast-message.php`: send one message to every mailbox | Not implemented | Not designed; falls under "send via HTTP API + group" |
| Admin "send to user" | `sendmail.php`: web form to send a single message | Not implemented; HTTP send API (REQ-SEND-30, G7) is the equivalent shape | Same |
| Audit / log viewer | `viewlog.php` reads a `log` table populated by `db_log` calls | `herold audit list`; REST `/api/v1/audit-log` (REQ-ADM-19, REQ-ADM-300..303) | Admin SPA viewer planned (REQ-ADM-202) |
| Backup (config + state export) | `backup.php` MySQL-only `mysqldump`-style export | `herold app-config {dump,load}` for application config (REQ-OPS-24); `herold diag {backup,restore}` (REQ-ADM-78) | Same |
| Update check | `update-check.php` calls home | NG13 ("no phone-home, no license gates, ever") | **Forbidden** |
| Password recovery (user) | `password-recover.php`: token by email or SMS | OIDC federation + verified Identity flow (REQ-IDENT-30..45); no self-serve password reset for the *local password* | **Missing-and-undecided** for principals with no linked IdP |
| Password expiry | Per-domain default + per-mailbox `password_expiry` | Not present | Not designed |
| Per-mailbox / per-domain quota | `quota` (MB), `maxquota`, `mailboxes`, `aliases` counts | Per-principal byte quota via `herold principal quota`; per-domain quota — not present as a first-class limit | Per-domain quotas — open question |
| Backup MX | Per-domain `backupmx` flag → Postfix `relay_domains` lookup | Not designed; SMTP smart-host (REQ-FLOW-SMARTHOST-01..08) is for outbound only; no inbound "accept and forward to primary MX" feature | **Out of scope** (single-node target — NG2; if needed, run a second herold or another MTA) |
| Per-domain transport map | `transport` field on Domain → Postfix transport lookup | Not present; per-domain SMTP routing is monolithic (single inbound, single outbound queue) | **Out of scope** for v1 (single-node, single-transport) |
| OIDC / social login | Not supported | REQ-AUTH-50..58: per-user federation, multiple providers, opt-in auto-provisioning | `herold oidc provider {add,list,remove,show,update,link-list,link-delete}` already shipped |
| Webhooks / event integration | XMLRPC API (`xmlrpc.php`), generally stagnant | First-class webhooks (REQ-ADM "webhook.publish" scope; G7); `herold hook {list,show,create,update,delete}` | REQ-HOOK-* suite |
| Push notifications | None | Web Push (REQ-PROTO-120..127, REQ-OPS-180..184); user-facing prefs in suite | Suite settings (REQ-PUSH-80..84) |

## Feature-by-feature comparison

### 1. Domain management

PostfixAdmin: `DomainHandler` exposes a domain row with fields
`domain, description, aliases (cap), mailboxes (cap), maxquota,
quota, transport, backupmx, active, default_aliases, password_expiry`.
The list view shows current alias/mailbox counts and aggregate quota
usage. Adding a domain is one form; `default_aliases` populates
postmaster/abuse forwards.

Herold today: `herold domain {add,remove,list}` (see
`internal/admin/cmd_domain.go`). REQ-ADM-310 says domain creation
emits the exact DNS records to publish, and the DNS-provider plugin
(REQ-PLUG-DNS-*) can auto-publish them. No per-domain `aliases`/
`mailboxes` cap counter; no per-domain `transport` field; no
`backupmx` flag; no `default_aliases` toggle (operator creates
postmaster/abuse aliases manually).

Herold planned: REST `/api/v1/domains` per REQ-ADM-11 with subresources
`/dkim`, `/mta-sts`, `/tls-rpt`, `/dmarc-records`. The admin SPA
(REQ-ADM-202) exposes domain CRUD + DKIM rotate + DNS record help.
The DNS-record copy/paste step from PostfixAdmin's per-domain page
has a direct herold equivalent: `herold diag dns-check <domain>`
(REQ-ADM-311) verifies and reports mismatches.

- Domain creation: **equivalent** (different shape: copy-paste DNS
  records vs auto-publish via plugin).
- Per-domain alias/mailbox/quota *caps*: **missing-and-undecided**.
  Herold's scale model (1k mailboxes total per node, single tenant)
  is small enough that hard per-domain caps are not load-bearing,
  but operators migrating multi-domain setups may expect them.
- `default_aliases` (auto-create postmaster/abuse): **missing-and-undecided**.
  RFC 2142 requires these aliases; herold's bootstrap does not
  create them automatically and they are not enumerated as
  required-at-create.
- `transport` per domain: **out-of-scope-by-design** (single-node
  topology; smart-host is global per REQ-FLOW-SMARTHOST-*).
- `backupmx`: **out-of-scope-by-design** (NG2).

### 2. Mailbox / principal management

PostfixAdmin: `MailboxHandler` row is `username (email), local_part,
domain, maildir, password, name, quota (MB), active, smtp_active,
welcome_mail, phone, email_other, token, token_validity, created,
modified, password_expiry`. `welcome_mail` triggers a one-shot
welcome message on create. `smtp_active` is a separate kill-switch
for outbound. `email_other` is the recovery email. `phone` enables
SMS-based password recovery.

Herold today: principals are not mailboxes; they are auth subjects
that own one canonical address + N aliases (REQ-AUTH-02). The CLI
exposes `herold principal {create,delete,list,show,disable,enable,
quota,grant-admin,revoke-admin,set-password,totp enroll,totp disable}`.
`disable` toggles the principal off (single kill-switch covering
both inbound and outbound). Per-principal byte quota is settable
(`herold principal quota <addr> <bytes>`). No separate
`smtp_active`. No welcome-mail dispatch on create. No SMS recovery.
Recovery email is the `Identity` mechanism (REQ-IDENT-*) plus
OIDC link.

Herold planned: REST `/api/v1/principals` with subresources
`/passwords`, `/app-passwords`, `/2fa`, `/aliases`, `/quota` per
REQ-ADM-10. SPA principal list / edit / quota per REQ-ADM-202.

- Create / delete / list / show: **equivalent**.
- Set password: **equivalent**.
- Disable / enable: **partial** — single kill-switch vs PostfixAdmin's
  separate `active` (login) and `smtp_active` (outbound) flags.
  Worth a knob? If a compromised mailbox needs to stop sending
  while still letting the owner log in to investigate, herold
  cannot do that today.
- Quota: **equivalent** (PostfixAdmin in MB, herold in bytes; one
  per-principal limit either way).
- Welcome mail on create: **missing-and-undecided**. The verified-
  Identity email (REQ-IDENT-30..33) is the closest existing template
  but it is not the same flow.
- Password expiry: **missing-and-undecided** (see § 7).
- `email_other` recovery address: **partial** — covered by the
  verified Identity model (REQ-IDENT-*) plus optional OIDC link
  (REQ-AUTH-50..58), but not as a dedicated "recovery email" field.
- Phone / SMS recovery: **out-of-scope-by-design** (no SMS gateway;
  no integration designed).
- Token / token_validity (password reset token storage): **missing-and-undecided**.
  Herold has no general password-reset endpoint; an admin running
  `herold principal set-password` is the only path.

### 3. Aliases

PostfixAdmin: `AliasHandler` defines `address, goto (list of targets),
is_mailbox (denormalised), on_vacation, active`. A row can fan out to
multiple addresses, including local mailboxes (`goto_mailbox` keeps
the message in the mailbox AND forwards). Catch-all is a row with
`address = @domain.tld`. `AliasdomainHandler` rewrites an entire
domain (`alias_domain → target_domain`).

Herold today: aliases are an attribute of a principal (REQ-AUTH-02:
"An individual principal MAY have multiple addresses: one canonical
+ N aliases"). The CLI is `herold alias {add,list,delete}` with
`<addr> <target>`. Catch-all is the per-domain rule REQ-AUTH-03
("A principal MAY have a catch-all address per domain, but only if
the principal also owns the domain. Limit one catch-all per
domain."). There is no domain-to-domain rewrite ("aliasdomain")
analog; that would require operator-side DNS + a herold-side
multi-receiver setup.

Herold planned: subresources under `/api/v1/principals/{id}/aliases`.

- Plain address alias: **equivalent**.
- Catch-all: **equivalent** (constrained to domain owner per
  REQ-AUTH-03).
- Multi-target forward (one address → list of addresses): **partial**.
  PostfixAdmin's `goto` field is a list; herold's alias maps an
  address to a single principal. Multi-recipient fan-out is modelled
  as a **group** (Kind = Group, REQ-AUTH-* table). Migrating a
  PostfixAdmin alias with three `goto` entries requires creating a
  herold group with those three members.
- Forward + keep in mailbox (`goto_mailbox`): **partial** — done
  via Sieve `redirect` + implicit `keep` in the user's script
  (REQ-PROTO-61), not as an admin-managed alias flag. The tagged-
  addresses feature (REQ-TAG-01..91) handles a related case for
  `+suffix` routing.
- Per-alias `on_vacation` flag: **out-of-scope** — vacation in
  herold is a JMAP `VacationResponse` on the principal, not toggled
  per alias.
- `aliasdomain` (rewrite domain A to domain B): **missing-and-undecided**.
  Useful for "merging" two domains into one mailbox set. Herold
  operators would today need to add every address as a separate
  alias.

### 4. Vacation / autoresponder

PostfixAdmin: `VacationHandler` stores `email, subject, body,
activefrom, activeuntil, active`. The vacation responder is wired
into a separate `VIRTUAL_VACATION/` Perl script that Postfix calls.
Per-alias `on_vacation` flag on `AliasHandler` indicates which
aliases should respond.

Herold: JMAP `VacationResponse` per REQ-PROTO-46, which the suite's
Settings panel exposes (REQ-SET-09). Server-side mapped to a Sieve
`vacation` rule (RFC 5230, REQ-PROTO-61). REQ-FLOW-90 honours
`Auto-Submitted:` to avoid auto-reply loops.

- **equivalent** in functionality (subject/body/date range/active);
  herold's lives in user-facing JMAP/Suite rather than the admin
  surface. Admin can view via Sieve script inspection if needed.

### 5. Fetchmail (pull mail from remote POP3/IMAP)

PostfixAdmin: `FetchmailHandler` is a full configuration for the
`fetchmail` daemon — source server / port / protocol / SSL settings /
poll interval / src user + password / mailbox to deliver into.
Operator opts in and a cron-driven `fetchmail` runs against the table.

Herold:

- Inbound from external accounts via IMAP/POP3 pull worker — **not
  implemented and not designed**. A pull worker would run as an
  independent daemon process (similar to fetchmail's model) and
  belongs to nobody in the current architecture.
- Inbound from external accounts via forwarding rules at the
  external provider — **the documented path**. Operators arrange
  Gmail "Forwarding and POP/IMAP" or M365 mailbox forwarding so
  mail arrives over SMTP at herold and flows through the normal
  REQ-FLOW-\* pipeline.
- One-shot import from Gmail Takeout — **implemented**
  (REQ-IMPORT-01..86; `herold import gmail`).
- External SMTP submission per Identity (outbound only, no IMAP
  mirror) — **implemented** (REQ-AUTH-EXT-SUBMIT-01..10).
- The deferred broader "external mail accounts" spec (referenced
  inside REQ-AUTH-EXT-SUBMIT-*) MAY add bidirectional IMAP mirror;
  not in v1.

Gap classification: **out-of-scope-by-design** for an ongoing pull
loop. The Takeout importer is a one-shot equivalent for the migration
case; for ongoing pull, herold expects operator-side forwarding.

### 6. DKIM key management

PostfixAdmin: `DkimHandler` owns the per-domain private key + selector
+ public key text. `DkimsigningHandler` maps "author" (a header-From
domain) → DKIM key id, populating a Postfix-style signing table for
OpenDKIM.

Herold: DKIM keys per-domain per-selector (REQ-OPS-21, the application-
config inventory). `herold dkim {generate,show}` is the CLI; the
domain row owns the active selector. Auto-rotate is sketched in
REQ-ADM (subresource `/dkim` on `/api/v1/domains/{domain}`). DNS
publication is automated via the DNS-provider plugin (G6); operator
copy-paste is the fallback.

- Generate / show key: **equivalent**.
- Rotate (key roll): **partial** today (no `herold dkim rotate`
  subcommand), **planned** via the REST subresource per REQ-ADM-11
  and the SPA (REQ-ADM-202).
- Per-author signing table (one domain's mail signed with another
  domain's key): **missing-and-undecided**. Useful when domain A is
  a "send-as" alias for domain B's principal and the operator wants
  outbound from A signed with A's key but the storage lives under
  B's principal. With Identity-level external SMTP submission
  (REQ-AUTH-EXT-SUBMIT-06), herold *skips* DKIM signing when an
  external relay is used; for inbound-to-A-then-resigned scenarios
  there is no design.

### 7. Password expiry

PostfixAdmin: timestamp on each mailbox; per-domain default duration.
Notification emails at 30 / 14 / 7 days before expiry. Forces a
password change before login.

Herold: no concept. Argon2id rehash on login if a stale hash is
found (REQ-AUTH-21), but no expiry, no notification, no forced
rotation.

- **missing-and-undecided.** The maintainer's stance on rotation
  policies vs the modern "don't rotate strong passwords" guidance
  is not documented. Sees use mostly in enterprise compliance
  contexts which are explicitly outside herold's target (NG1, NG7).

### 8. Application-specific passwords

PostfixAdmin: `app-passwords.php` lets a mailbox owner create
multiple per-device passwords (e.g. "iPhone", "desktop"). Bypasses
TOTP for IMAP/SMTP. Each is named, revocable, has a last-used
timestamp.

Herold: REQ-AUTH-30..32. App passwords are revocable, listed with
last-used timestamp + IP, MAY be scoped to a protocol (IMAP only /
SMTP only) in a future revision. The CLI surface today is exposed
via the principal subresource; the Suite settings panel will show
the user-facing management.

- **equivalent**.

### 9. Two-factor authentication

PostfixAdmin: TOTP at the login flow, with per-IP exemptions
(`totp-exceptions.tpl`, `TotpexceptionHandler`). The exemption
table lets a user pin known-good IPs (e.g. home router public IP)
so they don't have to TOTP every login.

Herold: TOTP for admin UI and JMAP HTTP (REQ-AUTH-40..43). Admin
scope step-up via TOTP (REQ-AUTH-SCOPE-03). Recovery codes
(REQ-AUTH-43). WebAuthn deferred (REQ-AUTH-41). No per-IP TOTP
exemption.

- TOTP enrollment / verify: **equivalent**.
- Recovery codes: **equivalent** (10 one-time codes vs PostfixAdmin's
  similar surface).
- Per-IP exemption: **missing-and-undecided**. Trade-off is
  obvious: usability vs the "what if the IP is wrong" failure mode.
  PostfixAdmin's design treats it as user-managed; herold could
  ship the same as a per-user setting if user feedback demands it.
- WebAuthn / FIDO2: **missing-but-planned** (REQ-AUTH-41 phase 2).

### 10. Domain-scoped (delegated) admin

PostfixAdmin: `AdminHandler` lets a superadmin grant non-superadmin
admin rights to a specific list of domains. The granted admin sees
only those domains in `list-virtual.tpl` and cannot edit anything
outside their scope.

Herold: REQ-AUTH-60 defines exactly three flat roles
(`user`, `admin`, `superadmin`); `admin` is global. REQ-AUTH-80 sets
a per-domain "owner" principal who can manage their own aliases /
catch-alls / DKIM rotation — that is a *user-domain* relation, not
a delegated-admin relation. There is no notion of "admin scoped to
domains X, Y" today.

Herold planned: REQ-ADM "Out of scope" lists "Delegated admin with
scoped permissions (phase 3)". The admin SPA does not currently
sketch a UI for it.

- **missing-but-planned** (phase 3, no design). Worth confirming
  whether single-tenant herold deployments actually need it; the
  PostfixAdmin shape (a multi-tenant hosting provider with per-
  customer admins) is precisely NG1.

### 11. Broadcast email to all users

PostfixAdmin: `broadcast-message.php` is a superadmin-only page that
sends one message to every mailbox on the server. Used for "system
maintenance window tomorrow" announcements.

Herold: no equivalent designed. The closest path is
`herold` — a `mail` operator account with a "send to a group that
contains every principal" pattern, but no group is auto-maintained.

- **missing-and-undecided.** A one-off send via the HTTP send API
  (G7) is straightforward; an admin-UI shortcut for "every active
  principal" is not designed.

### 12. Admin "send mail to one user"

PostfixAdmin: `sendmail.php` is a form to compose and send one
message from the admin user to a target.

Herold: the HTTP send API (REQ-SEND-30, G7) is the equivalent shape;
no SPA form is designed.

- **partial** (REST path exists; no admin SPA helper). Trivial to
  add to the SPA if the workflow recurs.

### 13. Audit / log viewer

PostfixAdmin: `viewlog.php` reads the `log` table populated by
`db_log` calls in handler `postSave`. Per-domain filtering. Shows
the actor (admin username), action ("create_mailbox", "edit_alias",
etc), target, and timestamp.

Herold: REQ-ADM-300..303 mandate an audit record for every admin
action; REQ-ADM-19 exposes `/api/v1/audit-log` (filters: since,
actor, action, resource; cursor pagination). `herold audit list`
is the CLI. The admin SPA will host an audit-log viewer
(REQ-ADM-202).

Failed auth attempts go to a separate "auth events" stream
(REQ-ADM-303) for SIEM/fail2ban integration — PostfixAdmin does
not split the streams.

- **equivalent**, with herold's surface arguably richer
  (cursor pagination, separate auth-failure stream, ETag/before/after
  state capture on mutations).

### 14. Backup / export

PostfixAdmin: `backup.php` is a MySQL-only dump (literally a wrapper
around `mysqldump`). PostgreSQL deployments do not get this page.

Herold: `herold app-config dump` / `herold app-config load`
(REQ-OPS-24..25) export/import application config. `herold diag
backup` and `herold diag restore` handle the data side. Both work
on both SQLite and Postgres (STANDARDS.md hard rule).

- **equivalent** — herold's coverage is wider (both backends,
  separate config-only export, integrity check via
  `herold diag verify`).

### 15. Update check / phone-home

PostfixAdmin: `update-check.php` polls postfixadmin.org for the
latest version and surfaces an in-admin notice.

Herold: G13 forbids phone-home and license gates "ever". The admin
SPA's About section (REQ-SET-22 analog for the admin app) shows
the local version only.

- **forbidden by goal.**

### 16. Password recovery (user self-serve)

PostfixAdmin: `password-recover.php` accepts an email, generates a
token, mails it (or SMS via Phone field), and serves a reset form.

Herold: there is no built-in self-serve local-password recovery.
The verified-Identity flow (REQ-IDENT-30..45) handles *Identity*
verification — not *principal password reset*. For principals
linked to an OIDC provider (REQ-AUTH-50..58) the IdP owns recovery.
For principals without a linked IdP the only path is operator
intervention (`herold principal set-password`).

- **missing-and-undecided.** The maintainer has not stated whether
  self-serve local-password reset is in scope, out of scope, or
  pending. The pre-launch operator might reasonably expect it.

### 17. Quotas (per mailbox + per domain)

PostfixAdmin: per-mailbox quota (MB), per-domain `maxquota` (default
cap when a domain admin creates a mailbox), per-domain `quota`
(aggregate cap across the whole domain).

Herold:

- Per-principal byte quota — **equivalent** (`herold principal
  quota <addr> <bytes>`).
- Per-domain aggregate quota — **missing-and-undecided**.
- Per-domain *default-new-mailbox-quota* — **missing-and-undecided**.

The herold scale target (REQ-OPS scale; 1k mailboxes, 100 domains)
is small enough that aggregate quotas have limited utility, but a
herold deployment with one "personal" domain at 100 GB and another
"family" domain at 1 TB might want to express that as policy.

### 18. Backup MX

PostfixAdmin: `backupmx` flag on domain → Postfix relay_domains lookup.
Used when the herold-equivalent box should accept and spool mail
for a domain that lives elsewhere when that elsewhere is down.

Herold: NG2 ("multi-node, never") implies the backup-MX role is
served by a different machine — either a second herold or any
other MTA. The smart-host requirement (REQ-FLOW-SMARTHOST-01..08)
is the *inverse* (herold sends outbound through someone else).

- **out-of-scope-by-design.**

### 19. Per-domain transport map

PostfixAdmin: `transport` field per domain feeds Postfix's
transport_maps. Used to route domain X via SMTP and domain Y via
LMTP to dovecot, or to relay one domain to a smart host while
others go direct.

Herold: no per-domain transport selector. The smart-host
(REQ-FLOW-SMARTHOST-*) is global. Inbound delivery is in-process
(no LMTP — explicitly out of scope per "no LMTP ingress" in the
00-scope.md "Out of scope" list).

- **out-of-scope-by-design.**

### 20. OIDC / social login

PostfixAdmin: no OIDC support in core. Some third-party plugins
exist for LDAP/AD via PHP modules; none mainline.

Herold: REQ-AUTH-50..58 mandate external OIDC federation per user,
across any number of providers, with email-need-not-match-local
semantics. `herold oidc provider {add,list,remove,show,update}`
manages providers; `herold oidc provider link-list / link-delete`
manages per-principal associations.

- **herold-only** — herold has a feature PostfixAdmin lacks.
  Not part of postfixadmin's surface; recorded here for
  completeness.

### 21. Webhooks / event integration

PostfixAdmin: an XMLRPC API exists (`xmlrpc.php`) but is
unmaintained and lightly used. No event push.

Herold: webhooks are first-class (G7, REQ-HOOK-* family — see
`docs/design/server/requirements/13-events.md` and
`internal/admin/cmd_hook.go`). `herold hook {list,show,create,
update,delete}` manages subscriptions. Web Push (REQ-PROTO-120..127)
is end-user-facing notification, distinct from operator-side
webhooks.

- **herold-only** with no PostfixAdmin equivalent.

### 22. Push notifications (user)

PostfixAdmin: none.

Herold: REQ-PROTO-120..127 + REQ-PUSH-* family. Web Push gateway,
suite-side prefs, per-event-type rules.

- **herold-only.**

### 23. Spam policy

PostfixAdmin: out of scope; PostfixAdmin manages routing, not
spam. Operators wire Rspamd / SpamAssassin into Postfix's
content-filter chain separately.

Herold: `herold spam {policy-show,policy-set}` manages the LLM
classifier policy. G5 ("LLM-first spam, no rule engine, no
Bayesian, no RBL/URIBL") locks the shape; NG4 ("no traditional
spam filtering, ever") rules out alternatives.

- **herold-only.** Not a parity gap; a posture difference.

### 24. TLS / ACME certificates

PostfixAdmin: out of scope; certbot/Let's Encrypt configured
out-of-band, Postfix and Dovecot pointed at the files.

Herold: `herold cert {list,show,renew,add-manual}` (REQ-OPS-40..,
G10). ACME-by-default; file-based fallback; embedded self-signed
for dev. REQ-ADM-16 exposes `/api/v1/tls/certificates`.

- **herold-only.**

### 25. DMARC / TLS-RPT aggregate report ingestion

PostfixAdmin: none.

Herold: REQ-ADM-17 (`/api/v1/reports/dmarc`) and REQ-ADM-18
(`/api/v1/reports/tlsrpt`). The admin SPA will host a viewer
(REQ-ADM-202).

- **herold-only.**

### 26. Mail import (one-shot migration)

PostfixAdmin: none.

Herold: `herold import gmail` (REQ-IMPORT-01..86; admin REST at
REQ-ADM-22a and Suite self-service at REQ-SET-IMPORT-1..6).

- **herold-only.**

### 27. Tagged addresses

PostfixAdmin: sub-addressing is sender-side ("+suffix"); PostfixAdmin
does not record or route on suffixes.

Herold: REQ-TAG-01..91 — managed table of
`(identity, suffix) → action` filters, with a per-message banner
prompt for first encounter and dismissal lifecycle. JMAP-level
`TaggedAddressFilter` object, admin REST endpoints, Suite settings
section (REQ-SET-TAG-01..21).

- **herold-only.**

### 28. UI shape & access model

PostfixAdmin: PHP per-page render. Admin and user share the same
authn (one session table); user-mode shows a narrow set of pages
(`public/users/`), admin-mode unlocks everything. `setup.php` is
the bootstrap surface, locked behind a separate `setup_password`
hash.

Herold today: HTMX-driven `protoui` at `/ui/` plus the Svelte
Suite SPA at `/mail/`. CLI is the canonical operator surface.
Bootstrap is `herold admin bootstrap` (REQ-AUTH-90..91 — one-time
token printed to stdout/log; no web setup page).

Herold planned (ADR-0001): replace HTMX `protoui` with a Svelte
admin SPA at `/admin/`. Same auth model — HMAC-signed session
cookie with closed-enum scope set per REQ-AUTH-SCOPE-01..04;
admin step-up via TOTP per REQ-AUTH-SCOPE-03. End-user
self-service (REQ-ADM-203) lives in `web/apps/suite`, not in
`web/apps/admin`.

The deployment-topology split (REQ-OPS-ADMIN-LISTENER-01..03) — a
public listener on :443 vs an admin listener on 127.0.0.1:9443 —
has no PostfixAdmin analog. PostfixAdmin admin and user UIs are
on the same vhost.

- Bootstrap: **partial equivalence** (different shape — CLI token
  vs setup.php). Herold's CLI shape is friendlier to automated
  provisioning (REQ-AUTH-91, `--password-stdin`).
- Admin scope step-up via TOTP: **herold-only** (PostfixAdmin
  has TOTP but does not step-up admin separately from end-user).
- Listener split: **herold-only**.

## Migration story

A PostfixAdmin operator looking at herold today can do:

- Create the equivalent set of **hosted domains** (`herold domain add`).
- Bootstrap the **superadmin** with the printed bootstrap token
  (REQ-AUTH-90).
- Create **principals** (mailboxes) with the same primary email
  + canonical password (`herold principal create / set-password`).
- Express **aliases** that map address → principal (1:1) directly.
  PostfixAdmin aliases that fan out to multiple addresses migrate
  by creating a herold **group** with those members (REQ-AUTH-*).
- Move **DKIM keys** if they want to preserve selectors (paste
  the existing private key in via the application-config path), or
  generate fresh ones (`herold dkim generate <domain>`) and update
  DNS — the DNS-provider plugin automates this end of the dance.
- Set **vacation responders** — the user does this via the Suite
  Settings panel; the operator does not need to migrate the
  PostfixAdmin `vacation` table directly.
- Audit-log past actions: not migrateable. PostfixAdmin's `log`
  table and herold's audit log are different schemas with no
  conversion path. Operator-side decision: archive the
  PostfixAdmin DB and start herold's audit log fresh.

What they cannot do today:

- **Delegated per-domain admin** — flat roles only.
- **Per-IP TOTP exemption** — TOTP applies uniformly.
- **fetchmail-style ongoing pull** — operators must arrange
  forwarding at the external provider or wait for the deferred
  "external mail accounts" feature.
- **Password expiry policy** — not designed.
- **Self-serve local-password reset** — only OIDC-linked users
  recover without operator intervention.
- **`aliasdomain` (rewrite domain A → domain B)** — no equivalent.
- **Per-domain aggregate quota** — not designed.
- **Postfix `transport` map shape** — not designed.
- **Backup MX role** — single-node-by-design.
- **`broadcast-message` (mail to every user)** — not designed.

What they'd need to wait for:

- The **admin SPA** (ADR-0001 phase 3) — REST is already there;
  the form-driven UI lands when web/apps/admin replaces
  internal/protoui.
- **DKIM rotate** as a one-shot subresource — CLI works today
  (regenerate + show); SPA shape is planned.
- The **deferred external mail accounts feature** for inbound
  IMAP mirroring of external mailboxes.

## Open questions

1. **Delegated admin scoping.** REQ-AUTH-60 freezes three flat
   roles and REQ-ADM "Out of scope" defers delegated admin to
   phase 3 without a design. Should we close that door (NG1
   "no multi-tenant" implies most operators don't need it) or
   keep the phase-3 placeholder live? PostfixAdmin's
   `AdminHandler.domains` field is the obvious shape if we ever
   want it.

2. **Self-serve local-password reset.** Today only OIDC-linked
   principals recover without operator intervention. Pre-launch
   users will probably want either (a) "forgot password" email
   round-trip backed by the verified-Identity flow's plumbing
   (REQ-IDENT-30..45 ships SHA-256-hashed tokens, click-link or
   6-digit code, expiry, rate-limit) or (b) explicit "no self-
   serve reset, talk to your operator" stance. Either is fine;
   the silence is the problem.

3. **Per-domain aggregate quota.** Herold's scale (1k mailboxes,
   100 domains) tolerates absence; do we want to ship it anyway
   for operator visibility ("domain example.com is 80 % full of
   its 100 GB allocation")? If yes, the principal-quota
   accounting machinery already covers most of the work.

4. **`smtp_active` split kill-switch.** Should `herold principal
   disable` keep its current global behaviour, or split into
   `disable-login` / `disable-outbound` to match PostfixAdmin's
   shape? Compromised-mailbox incident response is the obvious
   use case.

5. **Per-IP TOTP exemption.** PostfixAdmin lets users mark
   trusted IPs to skip TOTP. Herold's admin-step-up posture
   (REQ-AUTH-SCOPE-03) is "always TOTP for admin scope". Want a
   user-facing exemption for end-user scope only?

6. **Welcome mail on principal create.** PostfixAdmin's
   `welcome_mail` is a one-shot dispatched at mailbox creation.
   Useful for handing the new user a "your address is X, the
   server is mail.example.com, IMAP is …" greeting. The
   verified-Identity email (REQ-IDENT-33) is a different beast.
   Do we want a separately-templated welcome message, or do we
   route this through the existing operator path
   ("send a manual welcome via the HTTP send API")?

7. **`aliasdomain` (domain rewrite).** Useful when merging two
   domains or running `oldname.com` as a permanent forward to
   `newname.com`. PostfixAdmin's data shape is trivial. Add to
   herold as a domain attribute (`forward_to: domain_id`)?

8. **`default_aliases` (RFC 2142).** Should `herold domain add`
   auto-create postmaster / abuse aliases? Today these have to be
   added by hand; an opt-out flag would default-correct most
   deployments.

9. **Password expiry.** Want it / don't want it / want it but
   only as a per-principal opt-in? Current best-practice swings
   away from periodic rotation, but compliance contexts demand
   it. Herold targets self-hosters and small groups (00-scope.md
   "Target scale"); compliance probably is not a driver.

10. **Broadcast-to-all-principals admin shortcut.** PostfixAdmin's
    `broadcast-message.php` is one of the few features users
    actively miss when moving to other systems. Tractable as an
    operator-only HTTP send API helper that auto-iterates
    principals + handles bounce suppression. Should the admin SPA
    expose it?

## References

- Postfixadmin clone: `/tmp/postfixadmin-research/` (master,
  2026-05-12). Key files: `model/{Domain,Mailbox,Alias,Aliasdomain,
  Vacation,Fetchmail,Admin,Dkim,Dkimsigning,Totpexception}Handler.php`,
  `public/{broadcast-message,sendmail,viewlog,backup,
  update-check,password-recover}.php`, `configs/menu.conf`,
  `DOCUMENTS/{POSTFIXADMIN,SUPERADMIN,BACKUP_MX,Password_Expiration}.{txt,md}`.
- herold scope: `docs/design/00-scope.md`.
- herold admin & operations: `docs/design/server/requirements/{08-admin-and-management,09-operations,02-identity-and-auth}.md`.
- herold suite settings: `docs/design/web/requirements/20-settings.md`.
- herold admin CLI: `internal/admin/cmd_*.go`.
- ADR for the upcoming admin SPA: `docs/design/web/notes/adr-0001-merge-tabard-and-rewrite-admin-ui.md`.
- Related cross-product comparisons (different reference points):
  `docs/design/server/notes/stalwart-feature-map.md`,
  `docs/design/web/notes/gmail-feature-map.md`.
