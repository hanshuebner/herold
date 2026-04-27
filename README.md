# Herold

Herold is a single-node mail server in Go. Substrate beneath the tabard
suite (mail, calendar, contacts, chat).

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

The 3-5 minute path. You will need:

- Docker, or Go 1.25+ for a source build.
- An IMAP client (Thunderbird, Apple Mail, mutt, etc.).

The quickstart binds herold to loopback ports only and uses SQLite for
storage. No public DNS, no ACME, no smart host. For the real-domain
walkthrough (DNS records, ACME, DKIM publication, MTA-STS) see
[docs/user/quickstart-extended.md](./docs/user/quickstart-extended.md).

Every `herold` CLI command looks for the system config at
`/etc/herold/system.toml` unless told otherwise. You can override the
path with the `--system-config` flag or by setting the
`HEROLD_SYSTEM_CONFIG` environment variable. The steps below pass
`--system-config system.toml` on every CLI call so they work from the
repo root regardless of your shell environment.

### 1. Clone

```bash
git clone https://github.com/hanshuebner/herold.git
cd herold
```

### 2. Copy the quickstart config

```bash
cp docs/user/examples/system.toml.quickstart system.toml
```

Open `system.toml` in an editor. The template ships with
`hostname = "mail.example.local"` and a placeholder admin TLS cert
path; for the local-only quickstart the hostname does not need to
resolve. Adjust paths if you want the data directory somewhere other
than `./data`.

### 3. Generate the TLS cert/key pair

The IMAP, IMAPS, and SMTP submission listeners reference
`./data/admin.crt` and `./data/admin.key`. Generate them before
starting the server:

```bash
./scripts/make-self-signed-cert.sh data mail.example.local
```

This writes `data/admin.key` and `data/admin.crt`. These are suitable
for the loopback quickstart only.

### 4. Install the tabard web client

The quickstart `system.toml` mounts the tabard consumer SPA on the
public HTTP listener at `/` so you can log in via a web client in
addition to (or instead of) connecting an IMAP client. The release
tarball is published as a GitHub release asset; the helper script
downloads and extracts it into `./data/tabard/`:

```bash
./scripts/install-tabard.sh
```

`./scripts/install-tabard.sh data` is the same thing made explicit;
pass a different argument to extract somewhere else and adjust the
`asset_dir` line in `system.toml` to match. Re-run the script any
time you want to refresh to the latest tabard release; it wipes the
target directory first so older assets do not bleed through.

If you do not want the web client, comment out the `[server.tabard]`
block in `system.toml` (or set `enabled = false`) before starting the
server. The default block expects `./data/tabard/index.html` and the
server refuses to start without it.

### 5. Build and start the server

Source build:

```bash
go build ./cmd/herold
./herold server start --system-config system.toml
```

Or with Docker:

```bash
docker compose -f docs/user/examples/docker-compose.yml up -d
```

### 6. Bootstrap the first admin principal

In a second terminal:

```bash
./herold bootstrap --system-config system.toml --email admin@example.local --password 'change-me-now'
```

Passing `--password` on the command line is acceptable for this loopback-only quickstart, but for any non-throwaway installation you should omit `--password` (a random password is then generated and printed once) or supply it via stdin or the `HEROLD_BOOTSTRAP_PASSWORD` environment variable, to avoid the value appearing in shell history and process listings.

The command prints the admin email, the password, and a `hk_...` API
key. The API key is also written to `~/.herold/credentials.toml`
together with a `server_url` derived from the `kind = "admin"`
listener (here `http://127.0.0.1:9443`). Subsequent CLI calls
(`herold domain add`, `principal create`, etc.) read both values from
that file. Keep the printed credentials; the password is stored
hashed and the API key is stored as a SHA-256 hash, so neither is
recoverable from the server after this point.

If a previous bootstrap left a stale `server_url` in
`~/.herold/credentials.toml`, this run overwrites it with a warning.
If the saved URL is wrong (you will see admin commands return
`405 Method Not Allowed` because they hit the public listener instead
of the admin one), edit `server_url` in `~/.herold/credentials.toml`
to point at the `kind = "admin"` listener.

### 7. Add the local domain

```bash
./herold domain add --system-config system.toml example.local
```

### 8. Drop a test message into the inbox

Before connecting a client it helps to have something to read. The
SMTP relay listener at `127.0.0.1:1025` accepts mail for local
domains and delivers it directly into the recipient's INBOX. The
following one-liner uses only `nc` and ships a plain-text message:

```bash
{
  printf 'EHLO localhost\r\n'
  printf 'MAIL FROM:<sender@example.org>\r\n'
  printf 'RCPT TO:<admin@example.local>\r\n'
  printf 'DATA\r\n'
  printf 'From: Test Sender <sender@example.org>\r\n'
  printf 'To: admin@example.local\r\n'
  printf 'Subject: herold quickstart test\r\n'
  printf 'Date: %s\r\n' "$(date -u +'%a, %d %b %Y %H:%M:%S +0000')"
  printf '\r\n'
  printf 'Hello from the quickstart smoke test.\r\n'
  printf '.\r\n'
  printf 'QUIT\r\n'
} | nc -w 2 127.0.0.1 1025
```

Each line should come back with a `2xx` status. The HTTP send API
(`POST /api/v1/mail/send`) is for outbound delivery to the public
internet and is not the right path for loopback testing — it would
queue an SMTP-out attempt and fail to resolve `example.local`.

### 9. Connect a client

Herold exposes three independent client surfaces on the loopback
quickstart. They live at distinct URLs and have distinct sign-in
flows; pick the one you want to use rather than expecting one URL
to land you on the right screen.

| Surface | URL | Sign-in flow | Working today? |
|---------|-----|--------------|----------------|
| Tabard web suite (mail, calendar, contacts, chat) | `http://localhost:8080/` | the SPA's own login form (against `/.well-known/jmap`) | yes |
| Herold operator UI (domains, principals, queue, audit) | `http://localhost:8080/ui/dashboard` (public listener) or `http://localhost:9443/ui/dashboard` (admin listener) | the protoui sign-in form at `/ui/login`, then `/ui/dashboard` | yes |
| IMAP / SMTP submission (Apple Mail, Thunderbird, mutt, ...) | `imap://localhost:1143`, `imaps://localhost:1993`, `smtp+starttls://localhost:1587` | direct AUTH against the listener with email + password | yes |

The credentials are the same across all three surfaces: the email
and password from step 6 (`admin@example.local` /
`change-me-now` if you copied the README literally).

#### Operator UI (`/ui/`)

The fully working web surface. Visit
`http://localhost:8080/ui/dashboard`; you will be redirected to
`/ui/login`, sign in with the bootstrap credentials, and land back
on the dashboard. The public-listener session cookie
(`herold_public_session`) is scoped to `Path=/` and accompanies
JMAP, chat, and send-API requests made by the browser.

The admin listener at `localhost:9443` mounts the same UI but
issues a separate cookie scoped to the admin listener
(`herold_admin_session`, `Path=/ui/`) so cookie reuse across the
two listeners is mechanically impossible
(REQ-OPS-ADMIN-LISTENER-03). On the quickstart loopback shape both
ports speak plain HTTP; in production the admin listener is
loopback-only or fronted by ssh tunnel (see `docs/user/operate.md`).

#### Tabard web suite (`/`)

Open `http://localhost:8080/` in a browser. The tabard SPA loads
and immediately tries to fetch `/.well-known/jmap`. With no session
cookie present it redirects the browser to
`/login?return=%2F%23%2Fmail`.

Sign in at the login form with the bootstrap credentials. Herold
redirects back to `/#/mail` and the SPA's JMAP handshake succeeds
using the `herold_public_session` cookie now in the browser's jar.
The cookie is scoped to `Path=/` so it accompanies all subsequent
JMAP, chat, and send-API calls from the SPA.

#### IMAP / SMTP desktop client

- Server: `localhost`
- IMAP+STARTTLS port: `1143`  (or IMAPS port `1993` for implicit TLS)
- SMTP submission port: `1587` (with STARTTLS and the same
  username/password). Do not use port 1025 for outgoing mail in a
  client - that listener is the inbound relay and does not require
  AUTH.
- Username: `admin@example.local`
- Password: the one you set in step 6

The cert generated in step 3 is self-signed. Apple Mail and
Thunderbird will prompt to accept it the first time you connect;
click through and check "always trust" so the prompt does not
reappear. If the client returns a generic "Unable to verify account
name or password" without prompting, regenerate the cert with the
helper in step 3 (the SAN block must include `localhost` and
`127.0.0.1`, which the helper writes by default).

A note on `localhost` vs `127.0.0.1`: macOS's resolver returns the
IPv6 loopback (`::1`) for `localhost` first, and CFNetwork-based
clients (Apple Mail) do not always Happy-Eyeballs back to IPv4. The
quickstart `system.toml` therefore binds every listener as
`localhost:PORT`, which expands at startup into one socket on
`127.0.0.1` and one on `[::1]`; either name works in the client.

### 10. (Optional) Outbound through a smart host

To deliver outbound mail through Gmail / SES / SendGrid rather than
talk SMTP to the public internet, copy the smart-host example:

```bash
cp docs/user/examples/system.toml.smarthost system.toml
```

Edit the active `[smart_host]` block, restart the server, send a
message to an external address, and verify it arrives. (Note: smart
host implementation lands in Wave 3.1 - the example documents the
target config shape per the REQ-FLOW-SMARTHOST spec.)

For a real domain with public inbound, MX records, ACME-issued certs,
and DKIM publication, follow
[docs/user/quickstart-extended.md](./docs/user/quickstart-extended.md).

## Documentation

User documentation (operator + admin facing):

- [docs/user/install.md](./docs/user/install.md) - install paths
  (source, Docker, Debian/RPM, Kubernetes), system resources, storage
  backend choice, first-run bootstrap.
- [docs/user/operate.md](./docs/user/operate.md) - system.toml
  reference, TLS / ACME, DNS records, smart host, backup / restore,
  upgrades, observability, queue triage, plugin lifecycle, OIDC RP,
  common operational issues, signals, performance tuning.
- [docs/user/administer.md](./docs/user/administer.md) - domains,
  principals, mailboxes, aliases, API keys, Sieve, categorisation
  prompts, audit log, OIDC linkage.
- [docs/user/quickstart-extended.md](./docs/user/quickstart-extended.md)
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
