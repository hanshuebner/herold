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

Run herold from the published Docker image, bound to loopback ports,
in a few minutes. This posture is for trying the software on a single
machine only - it is not appropriate for any deployment that exposes
ports beyond `127.0.0.1`.

Start the container (storage is container-private; `docker stop`
discards it):

```bash
docker run --rm --name herold \
  -p 127.0.0.1:1025:1025 \
  -p 127.0.0.1:1587:1587 \
  -p 127.0.0.1:1143:1143 \
  -p 127.0.0.1:1993:1993 \
  -p 127.0.0.1:8080:8080 \
  -p 127.0.0.1:9443:9443 \
  codeberg.org/hanshuebner/herold:latest
```

In a second terminal, bootstrap the first admin principal and add a
local domain:

```bash
docker exec herold /usr/local/bin/herold bootstrap \
  --email admin@example.local --password 'change-me-now'
docker exec herold /usr/local/bin/herold domain add example.local
```

Open `http://localhost:8080/` for the Suite web client (mail,
calendar, contacts, chat) and `http://localhost:9443/admin/` for the
operator UI; sign in with the bootstrap credentials.

What next:

- Full evaluation tour (offline, all features):
  [./docs/manual/user/quickstart.mdoc](./docs/manual/user/quickstart.mdoc)
- Production deployment (real domain, DNS, ACME, DKIM, MTA-STS):
  [./docs/manual/admin/going-live.mdoc](./docs/manual/admin/going-live.mdoc)
- Build from source / other install paths:
  [./docs/manual/admin/install.mdoc](./docs/manual/admin/install.mdoc)

## Documentation

User manual (end-user facing):

- [docs/manual/user/quickstart.mdoc](./docs/manual/user/quickstart.mdoc) -
  offline Docker capabilities tour: run herold on one machine and
  exercise mail, calendar, contacts, and chat with no DNS or internet.

Admin manual (operator facing):

- [docs/manual/admin/install.mdoc](./docs/manual/admin/install.mdoc) -
  install paths (source, Docker, Debian/RPM, Kubernetes), system
  resources, storage backend choice, first-run bootstrap.
- [docs/manual/admin/going-live.mdoc](./docs/manual/admin/going-live.mdoc) -
  taking herold to a real domain: public inbound, MX records, ACME
  certificates, DKIM, DMARC, MTA-STS, TLS-RPT.
- [docs/manual/admin/reverse-proxy.mdoc](./docs/manual/admin/reverse-proxy.mdoc) -
  fronting the Suite and operator UIs with a reverse proxy and a real
  TLS certificate.
- [docs/manual/admin/configure.mdoc](./docs/manual/admin/configure.mdoc) -
  the full `system.toml` schema and every tunable.
- [docs/manual/admin/operate.mdoc](./docs/manual/admin/operate.mdoc) -
  backup / restore, upgrades, observability, queue triage, plugin
  lifecycle, signals, performance tuning, common operational issues.
- [docs/manual/admin/administer.mdoc](./docs/manual/admin/administer.mdoc) -
  domains, principals, mailboxes, aliases, API keys, Sieve,
  categorisation prompts, audit log, OIDC linkage.
- [docs/manual/admin/external-smtp-submission.mdoc](./docs/manual/admin/external-smtp-submission.mdoc) -
  accepting submission from external clients and applications.
- [docs/manual/admin/manual-test-runbook.mdoc](./docs/manual/admin/manual-test-runbook.mdoc) -
  manual verification runbook for releases.

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
    manual/                  end-user and operator manuals
      manifest.toml          chapter ordering and slugs
      user/
        index.mdoc           Welcome
        quickstart.mdoc      offline Docker capabilities tour
        examples/            sample system.toml / docker-compose
      admin/
        index.mdoc           Welcome
        install.mdoc         Installation
        going-live.mdoc      Going Live - real domain
        reverse-proxy.mdoc   Reverse Proxy and TLS
        configure.mdoc       Configuration Reference
        operate.mdoc         Operating Herold
        administer.mdoc      Administering Herold
        external-smtp-submission.mdoc
        manual-test-runbook.mdoc
    design/                  design baseline (frozen requirements)
      00-scope.md
      requirements/
      architecture/
      implementation/
      notes/
```

## License

MIT. See [LICENSE](./LICENSE).
