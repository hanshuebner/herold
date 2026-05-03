# Herold

Herold is a single-node mail server in Go. Substrate beneath the
in-tree Suite SPA (mail, calendar, contacts, chat) under `web/apps/suite`.

One binary. One system config file. One data directory. SQLite by
default; Postgres for larger deployments. No CGO. No multi-node. No
phone-home.

## What it is

Herold is a self-hostable, single-node communications server. Phase 1
ships an SMTP MTA plus IMAP / JMAP mailbox server with Sieve filtering,
DKIM / SPF / DMARC / ARC, a first-class HTTP send API, incoming
webhooks, LLM-based spam classification, and per-user external OIDC
federation. Phase 2 layers JMAP for Calendars, JMAP for Contacts, and a
chat surface (DMs, Spaces, presence, reactions, 1:1 video calls) on top
of the same store and dispatch core.

It is sized for small-to-medium self-hosters, including power users
with 1 TB+ mailboxes. Target scale per node: roughly 1,000 mailboxes,
100 hosted domains, 10,000 inbound + 10,000 outbound messages per day,
1,000 concurrent IMAP / JMAP sessions, ~10 TB total storage. See
`docs/design/00-scope.md` for the canonical scope statement and the
non-goals that frame what herold is not.

Explicit non-goals: no multi-tenancy, no multi-node, no clustering, no
hosting-provider features, no encryption at rest (operators run on
LUKS / ZFS / FileVault), no CalDAV / CardDAV / WebDAV, no bit-exact
AWS SES API compatibility, no LDAP. The list is short, deliberate, and
load-bearing.

## Status

Pre-1.0. Phase 2 work in progress; the codebase is not yet
feature-frozen. The operator-facing wire surface (SMTP / IMAP / JMAP
defaults, system.toml schema, admin REST under `/api/v1/`) is
stabilising but may still shift before 1.0.

The canonical revision history lives at `docs/design/00-scope.md` -
read its top section ("Latest scope revision") for the most recent
scope decisions.

## Quickstart

The 3-5 minute path. The default route uses the published Docker image
so you do not need a Go toolchain or pnpm to try herold; a source-build
alternative is described in [Quickstart - source build](#quickstart---source-build)
further down.

You will need:

- Docker (or any OCI-compatible runtime such as Podman).
- An IMAP client (Thunderbird, Apple Mail, mutt, etc.).

The quickstart binds herold to loopback ports only and uses SQLite for
storage. No public DNS, no ACME, no smart host. For the real-domain
walkthrough (DNS records, ACME, DKIM publication, MTA-STS) see
[docs/manual/user/quickstart.mdoc](./docs/manual/user/quickstart.mdoc).

The image ships a baked-in `system.toml` at `/etc/herold/system.toml`.
Mail listeners (SMTP submission, IMAP, IMAPS) speak TLS using a
throwaway self-signed cert that is also baked into the image; the
admin and public HTTP listeners speak plain HTTP. That posture is
appropriate for trying the software on a single machine and not
appropriate for any deployment that exposes ports beyond `127.0.0.1`.
For a real deployment, mount your own `system.toml` and follow the
extended quickstart.

### 1. Start the container (ephemeral, container-private data)

```bash
docker run --rm --name herold \
  -p 127.0.0.1:1025:1025 \
  -p 127.0.0.1:1587:1587 \
  -p 127.0.0.1:1143:1143 \
  -p 127.0.0.1:1993:1993 \
  -p 127.0.0.1:8080:8080 \
  -p 127.0.0.1:9443:9443 \
  ghcr.io/hanshuebner/herold:latest
```

This pulls the latest published image and starts the server with
storage inside the container. Stopping the container (`docker stop
herold`) discards every mailbox, principal, and config change you
made; perfect for kicking the tyres, useless for anything you want to
keep. See [Persistent state](#persistent-state) for the host-volume
shape.

The image runs as a nonroot user and exposes the quickstart ports:
`1025` SMTP relay, `1587` SMTP submission (STARTTLS), `1143` IMAP
(STARTTLS), `1993` IMAPS (implicit TLS), `8080` public HTTP / Suite
SPA, `9443` admin HTTP / admin SPA. All bound to `127.0.0.1` so the
surface stays on the local host.

The image ships a throwaway self-signed cert at
`/var/lib/herold/admin.{crt,key}` for the IMAP / IMAPS / submission
TLS upgrade. Desktop clients will prompt to trust it on first
connect.

### 2. Bootstrap the first admin principal

In a second terminal:

```bash
docker exec herold /usr/local/bin/herold bootstrap \
  --email admin@example.local --password 'change-me-now'
```

Passing `--password` on the command line is acceptable for this loopback-only quickstart, but for any non-throwaway installation you should omit `--password` (a random password is then generated and printed once) or supply it via stdin or the `HEROLD_BOOTSTRAP_PASSWORD` environment variable, to avoid the value appearing in shell history and process listings.

The command prints the admin email, the password, and a `hk_...` API
key. The API key is also written to `~/.herold/credentials.toml`
*inside the container* (it lives at
`/home/nonroot/.herold/credentials.toml` under the nonroot user), with
a `server_url` derived from the `kind = "admin"` listener (here
`http://127.0.0.1:9443`). Subsequent `docker exec herold ...` CLI
calls read both values from that file. Keep the printed credentials;
the password is stored hashed and the API key is stored as a SHA-256
hash, so neither is recoverable from the server after this point.

### 3. Add the local domain

```bash
docker exec herold /usr/local/bin/herold domain add example.local
```

### 4. Drop a test message into the inbox

Before connecting a client it helps to have something to read. The
SMTP relay listener at `127.0.0.1:1025` accepts mail for local
domains and delivers it directly into the recipient's INBOX. The
following one-liner uses only `nc` and ships a plain-text message
addressed entirely within `example.local`, so reply-from-the-Suite
also stays on this host:

```bash
{
  printf 'EHLO localhost\r\n'
  printf 'MAIL FROM:<admin@example.local>\r\n'
  printf 'RCPT TO:<admin@example.local>\r\n'
  printf 'DATA\r\n'
  printf 'From: Quickstart Test <admin@example.local>\r\n'
  printf 'To: admin@example.local\r\n'
  printf 'Subject: herold quickstart test\r\n'
  printf 'Date: %s\r\n' "$(date -u +'%a, %d %b %Y %H:%M:%S +0000')"
  printf '\r\n'
  printf 'Hello from the quickstart smoke test.\r\n'
  printf '.\r\n'
  printf 'QUIT\r\n'
} | nc -w 2 127.0.0.1 1025
```

Each line should come back with a `2xx` status. Replying to the
message from the Suite also works on the loopback shape: the
outbound queue checks the recipient's domain, sees that
`example.local` is hosted by this herold instance, and ingests the
reply locally instead of attempting to MX-resolve a domain that
doesn't exist on the public internet.

### 5. Connect a client

Herold exposes three independent client surfaces on the loopback
quickstart. They live at distinct URLs and have distinct sign-in
flows; pick the one you want to use rather than expecting one URL
to land you on the right screen.

| Surface | URL | Sign-in flow | Working today? |
|---------|-----|--------------|----------------|
| Suite web client (mail, calendar, contacts, chat) | `http://localhost:8080/` | the SPA's own login form (against `/.well-known/jmap`) | yes |
| Herold operator UI (domains, principals, queue, audit) | `http://localhost:9443/admin/` (admin listener) | the SPA's JSON login form posts to `/api/v1/auth/login` | yes |
| IMAP / SMTP submission (Apple Mail, Thunderbird, mutt, ...) | `imap://localhost:1143`, `imaps://localhost:1993`, `smtp+starttls://localhost:1587` | direct AUTH against the listener with email + password | yes |

The credentials are the same across all three surfaces: the email
and password from step 2 (`admin@example.local` /
`change-me-now` if you copied the README literally).

#### Operator UI (`/admin/`)

The Svelte admin SPA. Visit `http://localhost:9443/admin/` on the
admin listener; the SPA renders its login form, you sign in with the
bootstrap credentials, and the SPA posts JSON to
`/api/v1/auth/login`. On success the server sets the
`herold_admin_session` cookie (HttpOnly, `Path=/`, scoped to the
admin listener via the cookie name; `herold_admin_csrf` is set
non-HttpOnly so the SPA's JS reads it and sends `X-CSRF-Token` on
every mutating request).

Legacy `/ui/*` URLs from before the SPA cutover (Phase 3b of the
merge plan documented at `docs/design/server/notes/plan-tabard-merge-and-admin-rewrite.md`)
308-redirect to `/admin/`; bookmarks pointing at the old HTMX UI
land on the SPA without breaking. On the quickstart loopback shape
the admin listener speaks plain HTTP; in production it is
loopback-only or fronted by ssh tunnel (see `docs/manual/admin/operate.mdoc`).

#### Suite web client (`/`)

Open `http://localhost:8080/` in a browser. The Suite SPA loads
and immediately tries to fetch `/.well-known/jmap`. With no session
cookie present the SPA renders its own inline login form (no redirect
to a server-rendered `/login` page; that flow was retired in the
Phase 3 protoui cutover).

Sign in with the bootstrap credentials. The SPA posts JSON to
`/api/v1/auth/login`; on success the server sets the
`herold_public_session` cookie (HttpOnly, `Path=/`) plus
`herold_public_csrf` (non-HttpOnly so the SPA's JS reads it and
attaches `X-CSRF-Token` on every mutating request). The SPA then
re-runs its JMAP bootstrap and lands on `/#/mail`. The cookie's
`Path=/` scope lets it accompany all subsequent JMAP, chat, and
send-API calls from the SPA.

Self-service: navigate to `/#/settings` (or press `g s`) for password
change, two-factor authentication enrolment, and API-key management.
These call the public-listener self-service REST surface (under
`/api/v1/principals/{pid}/...` and `/api/v1/api-keys`) using the
session cookie; admin-only endpoints (queue, certs, audit, domains)
are not reachable from the public listener.

Chat is on by default; the Suite shell exposes it at `/#/chat` (or
`g c`). DMs and Spaces ride the same JMAP store as mail; presence,
typing, reactions, and 1:1 video calls flow over the WebSocket at
`/chat/ws` and need no additional configuration. Operators who want
to disable chat add `[server.chat] enabled = false` to `system.toml`.
Group video calls require an external SFU and are out of scope for
v1; 1:1 calls work through any STUN-only setup but a TURN relay
greatly improves reliability behind symmetric NATs (see
`docs/design/server/requirements/15-video-calls.md`).

#### IMAP / SMTP desktop client

- Server: `localhost`
- IMAP+STARTTLS port: `1143`  (or IMAPS port `1993` for implicit TLS)
- SMTP submission port: `1587` (with STARTTLS and the same
  username/password). Do not use port 1025 for outgoing mail in a
  client - that listener is the inbound relay and does not require
  AUTH.
- Username: `admin@example.local`
- Password: the one you set in step 2

The TLS cert baked into the image is self-signed and is shared by
every container built from the same image tag. Apple Mail and
Thunderbird will prompt to accept it the first time you connect;
click through and check "always trust" so the prompt does not
reappear. The baked-in cert covers `mail.example.local`, `localhost`,
`127.0.0.1`, and `::1` in its SAN.

A note on `localhost` vs `127.0.0.1`: macOS resolves `localhost` to
the IPv6 loopback (`::1`) first, and `docker run -p 127.0.0.1:...`
only binds the IPv4 loopback on the host side. If your client cannot
connect to `localhost` but works against `127.0.0.1`, that is why -
either use the literal `127.0.0.1` in your client, or publish the
port on both stacks (`-p 127.0.0.1:1143:1143 -p [::1]:1143:1143`).

### Persistent state

The ephemeral run above stores the SQLite DB, blobs, and FTS index
inside the container's writable layer; `docker stop herold` discards
all of it. To keep state across restarts, mount a host directory (or
named volume) at `/var/lib/herold`:

```bash
mkdir -p ./herold-data
docker run -d --name herold \
  -v "$(pwd)/herold-data:/var/lib/herold" \
  -p 127.0.0.1:1025:1025 \
  -p 127.0.0.1:1587:1587 \
  -p 127.0.0.1:1143:1143 \
  -p 127.0.0.1:1993:1993 \
  -p 127.0.0.1:8080:8080 \
  -p 127.0.0.1:9443:9443 \
  ghcr.io/hanshuebner/herold:latest
```

The host directory must be writable by UID `65532` (the distroless
`nonroot` user that herold runs as):

```bash
sudo chown -R 65532:65532 ./herold-data
```

To override the baked-in `system.toml` (e.g. to switch to Postgres,
add a smart host, or bind real ports), bind-mount your own file at
`/etc/herold/system.toml`:

```bash
docker run -d --name herold \
  -v "$(pwd)/herold-data:/var/lib/herold" \
  -v "$(pwd)/system.toml:/etc/herold/system.toml:ro" \
  -p 127.0.0.1:1025:1025 \
  ... \
  ghcr.io/hanshuebner/herold:latest
```

For a real domain with public inbound, MX records, ACME-issued certs,
and DKIM publication, follow
[docs/manual/user/quickstart.mdoc](./docs/manual/user/quickstart.mdoc).
A `docker compose` example with persistent volumes lives at
[docs/manual/user/examples/docker-compose.yml](./docs/manual/user/examples/docker-compose.yml).

### (Optional) Outbound through a smart host

To deliver outbound mail through Gmail / SES / SendGrid rather than
talk SMTP to the public internet, copy the smart-host example as your
mounted `system.toml` and restart:

```bash
cp docs/manual/user/examples/system.toml.smarthost system.toml
docker restart herold
```

Edit the active `[smart_host]` block, send a message to an external
address, and verify it arrives. (Note: smart host implementation
lands in Wave 3.1 - the example documents the target config shape
per the REQ-FLOW-SMARTHOST spec.)

## Quickstart - source build

The Docker image above is the recommended path. If you want to build
from source - to hack on herold itself, run an unreleased commit, or
run on a host without Docker - the equivalent loopback shape lives in
`docs/manual/user/examples/system.toml.quickstart` and uses paths under
`./data` rather than `/var/lib/herold`.

You will need:

- Go 1.25+ for the server build.
- pnpm 10+ and Node 20+ if you want the consumer Suite SPA baked in
  (otherwise the binary serves placeholder HTML at `/`).
- An IMAP client.

```bash
git clone https://github.com/hanshuebner/herold.git
cd herold

cp docs/manual/user/examples/system.toml.quickstart system.toml
./scripts/make-self-signed-cert.sh data mail.example.local

make build                   # build-web + build-server, embeds the SPAs
./bin/herold server start --system-config system.toml
```

In a second terminal:

```bash
./bin/herold bootstrap --system-config system.toml \
  --email admin@example.local --password 'change-me-now'
./bin/herold domain add --system-config system.toml example.local
```

Connect clients exactly as documented in step 5 above; the URLs and
ports are identical because the source-build `system.toml.quickstart`
publishes the same listener shape.

If pnpm is not installed locally, `make build-server` (without
`build-web`) copies the tracked SPA placeholders so `//go:embed`
resolves and the resulting binary serves a placeholder index.html at
`/` and `/admin/`. The IMAP / SMTP client surfaces work identically.
For a backend-only binary that does not embed any web assets at all,
use `go build -tags nofrontend ./cmd/herold`.

## Documentation

User documentation (operator + admin facing):

- [docs/manual/admin/install.mdoc](./docs/manual/admin/install.mdoc) - install paths
  (source, Docker, Debian/RPM, Kubernetes), system resources, storage
  backend choice, first-run bootstrap.
- [docs/manual/admin/operate.mdoc](./docs/manual/admin/operate.mdoc) - system.toml
  reference, TLS / ACME, DNS records, smart host, backup / restore,
  upgrades, observability, queue triage, plugin lifecycle, OIDC RP,
  common operational issues, signals, performance tuning.
- [docs/manual/admin/administer.mdoc](./docs/manual/admin/administer.mdoc) - domains,
  principals, mailboxes, aliases, API keys, Sieve, categorisation
  prompts, audit log, OIDC linkage.
- [docs/manual/user/quickstart.mdoc](./docs/manual/user/quickstart.mdoc)
  - real-domain walkthrough with DNS, ACME, DKIM, DMARC, MTA-STS,
  TLS-RPT.

Design and specification (the historical record; not user-facing):

- [docs/design/00-scope.md](./docs/design/00-scope.md) - canonical
  scope, goals, non-goals.
- [docs/design/server/requirements/](./docs/design/server/requirements/) - numbered
  requirements (`REQ-XXX-nn`) per subsystem.
- [docs/design/server/architecture/](./docs/design/server/architecture/) - how the
  system is shaped: storage, protocols, queue, plugins, sync.
- [docs/design/server/implementation/](./docs/design/server/implementation/) - tech
  stack, phasing, testing strategy, simplifications and cuts.
- [docs/design/server/notes/](./docs/design/server/notes/) - reference material.

Contributor and agent context:

- [CLAUDE.md](./CLAUDE.md) - working agreement for Claude Code agents.
- [STANDARDS.md](./STANDARDS.md) - global coding and development
  standards. Authoritative.
- [AGENTS.md](./AGENTS.md) - specialist subagent partitioning.

## Project layout

Trimmed view; the full layout (and rationale) lives in
`docs/design/00-scope.md` and
`docs/design/server/implementation/01-tech-stack.md`.

```
herold/
  README.md                  this file
  CLAUDE.md                  agent working agreement
  STANDARDS.md               coding and development standards
  AGENTS.md                  specialist agent roster
  LICENSE                    MIT
  go.mod                     module: github.com/hanshuebner/herold
  Makefile                   build, test, lint, ci-local, docker

  cmd/herold/                single binary entrypoint (server + CLI)
  internal/                  non-plugin code
    store, storesqlite, storepg, storeblobfs, storefts
    protosmtp, protoimap, protojmap, protomanagesieve, protoadmin
    protosend, protowebhook, protoevents
    directory, directoryoidc
    mailparse, maildkim, mailspf, maildmarc, mailarc
    sieve, spam, queue, tls, acme, autodns
    plugin, observe, sysconfig, appconfig, admin
  plugins/                   first-party plugins, each its own main
  test/interop, test/e2e     cross-package scenarios

  deploy/docker              container image build

  docs/
    user/                    operator + admin documentation
      install.md
      operate.md
      administer.md
      quickstart-extended.md
      examples/
        system.toml.quickstart
        system.toml.smarthost
        docker-compose.yml
    design/                  design baseline (frozen requirements)
      00-scope.md
      requirements/
      architecture/
      implementation/
      notes/
```

## License

MIT. See [LICENSE](./LICENSE).
