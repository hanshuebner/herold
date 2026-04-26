# Administering Herold

Application-administrator runbook. Distinct from
[./operate.md](./operate.md) (which covers the operator's surface:
system.toml, TLS, signals, observability) - this document is for the
administrator who manages the *application* layer: domains,
principals, mailboxes, aliases, API keys, Sieve scripts, OIDC
linkages, and the audit log.

The line between operator and admin is the configuration split
(REQ-OPS / REQ-ADM):

- The **operator** owns `/etc/herold/system.toml` and the host.
- The **administrator** owns the application state - domains and
  principals and aliases and the rest. Application state lives in the
  herold DB and is mutated through the admin REST API and CLI.

A single human typically wears both hats on a personal mail server.
On a larger deployment, the same person may own the host and let one
or more delegates have admin REST credentials.

## The three admin surfaces

Herold exposes three admin surfaces (REQ-ADM-01..05):

1. **CLI (`herold ...`).** Thin wrapper around the REST API. Best for
   shell-script automation, ad-hoc terminal sessions, and the
   bootstrap.
2. **REST (`POST /api/v1/...`).** Bearer-token-authenticated; the
   complete admin surface. JSON in / JSON out. The OpenAPI 3.1 spec
   is published at `/api/openapi.json` (REQ-ADM-05).
3. **Minimal Web UI** (Phase 2). Mounts at the configured
   `[server.ui] path_prefix` (default `/ui`) and consumes the REST
   API. Suitable for browser-only quick changes; everything the UI
   does, the CLI does, and one REST call does.

The full REST API reference lives in
`docs/design/requirements/08-admin-and-management.md`. The CLI
matches the REST shape.

### Authentication

CLI commands resolve credentials in this order:

1. `--api-key` flag.
2. `HEROLD_API_KEY` environment variable.
3. `~/.herold/credentials.toml` (written by `herold bootstrap`).

The base URL similarly resolves:

1. `--server-url` flag.
2. `server_url` in `~/.herold/credentials.toml`.

Without either, CLI commands that need the REST surface error out
with a clear message.

## Domains

Hosted domains are application config - they live in the DB and are
mutated through the admin surface (REQ-OPS-21).

### Add a domain

```bash
herold domain add example.com
```

Behavior on add (REQ-OPS-60):

- DKIM keys generated for the domain (per-domain, per-selector).
- The set of DNS records the domain needs is built (DKIM TXT,
  `_mta-sts` TXT, `_smtp._tls` TXT, `_dmarc` TXT, SPF TXT).
- If a DNS-provider plugin is bound, the records are published via
  the plugin. Otherwise, herold prints them and the operator pastes
  them into the zone (REQ-OPS-63).

### List

```bash
herold domain list
```

### Remove

```bash
herold domain remove example.com
```

Removal is destructive: it cascades to principals on that domain.
Take a backup first.

### DKIM key generation and rotation

DKIM keys are generated automatically on `domain add`. Per-domain
DKIM rotation (`herold domain dkim rotate`, `herold domain dkim
show`) is planned per REQ-ADM-11 and REQ-ADM-310 - TODO(operator-doc):
dkim-cli-not-yet-wired in `internal/admin/cmd_domain.go`. Until then,
the REST surface (`/api/v1/domains/{name}/dkim`) is the path; check
`docs/design/requirements/08-admin-and-management.md` for the full
shape.

## Principals (users)

A principal is a user, group, or service account. Every mailbox
belongs to a principal; every API key belongs to a principal.

### Create

```bash
herold principal create user@example.com --password 'pw1'
```

Flags:

- `--password <str>` - explicit password. If omitted, the server
  generates a random 20-character password and prints it once.
- `--random-password` - force a server-generated random password
  even if `--password` is set (useful in scripts that always rotate).

The server stores the password Argon2id-hashed (STANDARDS section 9).
Plain passwords are not recoverable.

### List

```bash
herold principal list
herold principal list --limit 50
herold principal list --after <cursor>     # keyset pagination.
```

### Show

```bash
herold principal list --limit 1000 | grep user@example.com
```

(A dedicated `show` subcommand is on the CLI map per REQ-ADM-101 but
TODO(operator-doc): principal-show-not-yet-wired. The REST endpoint
`GET /api/v1/principals/{id}` exists; CLI surfacing pending.)

### Disable / enable

Disable a principal without deleting (lock-out for vacation, off-boarding,
or compromise response). The CLI surface for this is planned per
REQ-ADM-101 (`disable` / `enable` verbs). TODO(operator-doc):
principal-disable-cli-not-yet-wired. Until the CLI lands, use the
REST surface: `PATCH /api/v1/principals/{id}` with `{"disabled":
true}`.

### Delete (with cascade)

Cascade implications: deleting a principal removes their mailboxes,
their messages, their Sieve scripts, their API keys, their OIDC
links. The audit log entry survives (REQ-ADM-301 - append-only). Take
a backup first.

```bash
herold principal delete user@example.com
```

### Reset password

```bash
herold principal set-password user@example.com --password 'newpw'
herold principal set-password user@example.com         # interactive prompt.
```

### Set quota

CLI flag planned per REQ-ADM-10 sub-resource `/quota`.
TODO(operator-doc): principal-quota-cli-not-yet-wired. REST:
`PUT /api/v1/principals/{id}/quota`.

### Set TOTP

User-driven via the Web UI / `/settings` self-service surface
(REQ-ADM-203). Operator override: REST
`POST /api/v1/principals/{id}/2fa/totp`. TODO(operator-doc):
totp-cli-not-yet-wired.

### Link OIDC provider

Linking a principal to an external OIDC provider is a user-flow (the
user signs in via Google / Okta / etc., and herold matches the
external `sub` to the principal's OIDC link table).

Operator-side commands today:

```bash
herold oidc link-list user@example.com
herold oidc link-delete user@example.com google
```

The link-create path is the user-facing OIDC sign-in flow per
`docs/design/requirements/02-identity-and-auth.md`.

### Grant admin

The first principal created by `herold bootstrap` carries
`PrincipalFlagAdmin`. Subsequent principals are non-admin by default.
Granting admin is `PATCH /api/v1/principals/{id}` with
`{"flags": ["admin"]}`. TODO(operator-doc):
principal-grant-admin-cli-not-yet-wired.

## Mailboxes

A mailbox is a folder inside a principal's account: INBOX, Sent,
Trash, plus arbitrary user folders. The admin's involvement is
typically minimal - clients (IMAP, JMAP) create and rename mailboxes
on their own. Operator-driven mailbox CRUD is planned for two reasons:

- Provisioning a fresh principal with a non-default folder layout
  (Archive subfolders, project folders).
- Setting ACLs on shared mailboxes (REQ-PROTO-37, Phase 2).

### CLI surface (planned)

`herold mailbox add user@example.com INBOX/Archive` and friends are
planned per REQ-ADM-101's `principal mailbox` subresource shape.
TODO(operator-doc): mailbox-cli-not-yet-wired in
`internal/admin/`. Today: clients create mailboxes themselves over
IMAP / JMAP.

### Shared mailboxes and ACL (Phase 2)

Shared mailboxes plus IMAP ACL land in Phase 2 (per `docs/design/00-scope.md`
"Defaults in force"). The operator surface is planned but not yet
exposed.

## Aliases

An alias rewrites the recipient address before delivery
(REQ-FLOW-* / REQ-ADM-10 sub-resource `/aliases`). Single-target and
multi-target aliases are both supported; multi-target aliases fan out
to multiple principals.

### CLI surface (planned)

```bash
# Planned, Wave X.Y:
herold alias add postmaster@example.com user@example.com
herold alias add support@example.com a@example.com,b@example.com
herold alias list
herold alias delete postmaster@example.com
```

TODO(operator-doc): alias-cli-not-yet-wired in `internal/admin/`. The
REST shape (`/api/v1/principals/{id}/aliases`) is in REQ-ADM-10.

## API keys

API keys (machine accounts) authenticate against the admin REST API.
Every API key is bound to a principal; the key inherits the
principal's authorization scope.

### Create

```bash
herold api-key create user@example.com --label "ci-bot"
```

The response carries:

- `id` - the key's internal id (used for revoke).
- `key` - the **plaintext** API key, prefixed `hk_`. Shown **once**;
  the server stores only the SHA-256 hash. Capture it now or revoke
  and reissue.
- `created_at`, `last_used_at`, `principal_id`, `label`.

The bootstrap API key (created by `herold bootstrap`) follows the
same shape and is labeled `bootstrap` in the DB.

### List

```bash
# Planned, Wave X.Y:
herold api-key list
herold api-key list --principal user@example.com
```

TODO(operator-doc): api-key-list-cli-not-yet-wired. REST: `GET
/api/v1/api-keys`.

### Revoke

```bash
herold api-key revoke <key-id>
```

The hash is removed from the DB; the plaintext key (which only ever
existed in the response of `create`) is now useless.

## Sieve scripts

Sieve scripts (RFC 5228) run in the delivery pipeline; they
implement vacation autoresponders, folder routing, server-side
labels, and reject rules. Each principal has a set of scripts; one
is `active` at a time.

### CLI surface (planned)

```bash
# Planned, Wave X.Y:
herold sieve put user@example.com active < script.sieve
herold sieve validate < script.sieve
herold sieve list user@example.com
herold sieve deactivate user@example.com
```

TODO(operator-doc): sieve-cli-not-yet-wired. The user-facing
ManageSieve listener (RFC 5804) is the supported path today; clients
that speak ManageSieve (Roundcube, Thunderbird with the Sieve add-on,
Apple Mail with the Sieve plugin, the standalone `sieveshell`) edit
scripts directly.

The REST surface for global scripts (REQ-ADM-15:
`/api/v1/sieve/scripts`) is planned. Per-user scripts are
ManageSieve-only by REQ-ADM-15.

## Categorisation prompts

LLM-driven message categorisation distinct from spam - Gmail-style
Primary / Social / Promotions / Updates / Forums labels by default,
user-configurable prompt (REQ-FILT-200..220).

```bash
# Planned, Wave 3.x:
herold categorise prompt set user@example.com < prompt.txt
herold categorise list-categories user@example.com
herold categorise recategorise user@example.com --mailbox INBOX
```

TODO(operator-doc): categorise-cli-not-yet-wired. The categorise
config block in `system.toml` (`[server.categorise]`) lands with the
feature in Wave 3.x.

## Outbound submission queue

An admin can inspect any user's outbound queue (REQ-FLOW-* and
REQ-ADM-12). The same `herold queue ...` commands documented in
[./operate.md](./operate.md) accept a `--principal` filter:

```bash
herold queue list --principal 7        # principal id (numeric).
herold queue show 1234                 # any queue item by id.
```

Every inspection on another principal's queue produces an audit-log
record (REQ-ADM-300).

## Inbound webhooks (Phase 2)

A webhook subscription dispatches an HTTP POST to a receiver URL on
inbound mail (REQ-HOOK-*). Owner is either a hosted domain or a
specific principal.

### Create

```bash
herold hook create \
  --owner-kind domain \
  --owner-id example.com \
  --target-url https://hooks.example.net/incoming \
  --mode inline
```

`--mode` is `inline` (body in the POST) or `fetch_url` (signed URL
the receiver fetches; default for large bodies).

The response carries an `hmac_secret` printed **once** - the
receiver verifies the HMAC-SHA256 signature on every delivery. Store
the secret in your receiver's secret manager immediately.

### List, show, update, delete

```bash
herold hook list
herold hook list --owner-kind domain --owner-id example.com
herold hook show <id>
herold hook update <id> --target-url https://new.example.net/incoming
herold hook update <id> --rotate-secret      # rotates HMAC, prints new secret once.
herold hook delete <id>
```

## Audit log

Every admin mutation (and significant read) lands in the
`audit_log` table - an append-only ledger of who did what and when
(REQ-ADM-300, REQ-ADM-301).

### Query (planned)

```bash
# Planned, Wave X.Y:
herold audit list --since 1h
herold audit list --actor admin@example.com --action principal.delete
herold audit list --resource user@example.com
```

TODO(operator-doc): audit-list-cli-not-yet-wired. Today's path:

- The REST endpoint `GET /api/v1/audit?limit=1000` (also exposed via
  `herold diag collect` which dumps the recent audit log into the
  support bundle).
- Failed auth attempts land in a separate "auth events" stream
  (REQ-ADM-303) for SIEM / fail2ban integration.

The audit shape is `{timestamp, actor, actor_ip, action, resource,
outcome, before, after}` for state changes, `{timestamp, actor,
actor_ip, action, resource, outcome}` for reads. Retention follows
REQ-STORE-82.

### Export

For SIEM ingestion, the audit log exports as JSON-Lines via
`/api/v1/audit-log` (REQ-ADM-302). Treat it as immutable: the table
is append-only and downgrades are forbidden by REQ-OPS-130.

## OIDC RP federation

Per-user OIDC linkage gives a principal a "Sign in with Google /
Okta / GitHub" path on top of the local password (REQ-AUTH-*).
Herold is a **relying party only** - never an issuer (NG11).

### Register a provider

```bash
herold oidc provider add google \
  --issuer https://accounts.google.com \
  --client-id <id> \
  --client-secret <secret>
```

Once protoadmin's secret-env shape is in place, prefer:

```bash
herold oidc provider update google --client-secret-env=$GOOGLE_OIDC_SECRET
```

so the secret never lands in shell history.

### Inspect

```bash
herold oidc provider list
herold oidc provider show google                # client_secret omitted.
```

### Update / remove

```bash
herold oidc provider update google --client-secret-env=$NEW_VAR
herold oidc provider remove google
```

### Link / unlink (operator side)

The link-creation path is a user sign-in flow: the user clicks "Sign
in with Google", herold redirects to the provider, the provider
redirects back, herold matches the external `sub` to a local
principal (or claims the account on first link). Operator-side, only
list and delete are exposed:

```bash
herold oidc link-list user@example.com
herold oidc link-delete user@example.com google
```

The detailed flow lives in
`docs/design/requirements/02-identity-and-auth.md`.

## Chat / calendar / contacts admin (Phase 2)

JMAP for Calendars, JMAP for Contacts, and the chat surface (DMs,
Spaces, presence, reactions, 1:1 video calls) land in Phase 2. Most
of this is **client-managed**: the user creates Conversations, joins
Spaces, sets their presence, etc.

The admin surface, where it exists, covers:

- **Disable a Space.** Operator-side mute / suspend a noisy or
  abandoned Space.
- **Force-leave members.** Remove a member who lost their device or
  left the organisation.
- **Set retention per Conversation / Space.** REQ-CHAT-92 sweeper
  honors per-conversation retention.
- **Set the edit window per Space.** How long after send a message
  remains editable / deletable.
- **Disable video calls** (`[server.call] enabled = false`) - see
  [./operate.md](./operate.md) for the system.toml block.

The chat / cal / contacts admin REST surface is wave-2.x, not yet
fully wired. TODO(operator-doc): chat-cal-contacts-admin-cli-not-yet-wired.
Track the requirements documents under
`docs/design/requirements/14-chat.md`, `15-video-calls.md`, plus the
calendar / contacts requirements when those land.

## Where to next

- Day-2 operator runbook: [./operate.md](./operate.md).
- Real-domain walkthrough: [./quickstart-extended.md](./quickstart-extended.md).
- The historical record: `docs/design/requirements/08-admin-and-management.md`
  for the full REST + CLI shape, `docs/design/requirements/02-identity-and-auth.md`
  for the OIDC flow.
