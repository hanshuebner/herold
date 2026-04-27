# Installing Herold

This document walks through the install paths herold supports, the
system resources to budget, the storage backend choice, and the
first-run bootstrap. It is operator-facing; for the day-2 runbook see
[./operate.md](./operate.md), for application-administration see
[./administer.md](./administer.md), and for the real-domain
walkthrough see [./quickstart-extended.md](./quickstart-extended.md).

## Choose a deployment shape

Herold is a single static binary plus optional first-party plugins.
The supported deployment shapes are:

- **From source.** `go build ./cmd/herold` produces a single binary
  the operator drops into `/usr/local/bin` (or anywhere in `$PATH`).
- **Docker.** A multi-stage `Dockerfile` under `deploy/docker/`
  produces a distroless `nonroot` runtime image. Suitable for
  Compose, Nomad, plain `docker run`.
- **Debian / RPM packages.** Skeletons under `deploy/debian/` and
  `deploy/rpm/`. The packages install the binary, a systemd unit, and
  a placeholder `/etc/herold/system.toml`. Plugins are bundled.
  (Status: package skeletons are present in-tree but the publish
  pipeline is not yet wired - operators currently build their own
  packages or use the source / Docker paths.)
- **Kubernetes.** Manifests under `deploy/k8s/` (StatefulSet plus
  ConfigMap / Secret). Not a Helm chart in v1; document with plain
  manifests. (Status: sketch only.)

Pick whichever matches your existing platform discipline. Herold has
no preference.

## From source

### Prerequisites

- Go 1.25 or newer (the floor at planning time; bumped to current
  stable at each phase kickoff per `STANDARDS.md` section 2).
- git. Required to clone the repository and invoked by `go build`
  for VCS stamping when building from a checkout.
- A POSIX system. Linux is the primary target; macOS works for
  development. Windows is build-only - it is not a supported runtime
  target (REQ-OPS-153).
- No CGO toolchain. Herold's default build is pure Go. The SQLite
  driver is `modernc.org/sqlite`; Postgres is `pgx/v5`; FTS is
  `blevesearch/bleve`; events go via pure-Go NATS. `CGO_ENABLED=0`
  is the supported configuration; a `cgo` build tag may exist for
  benchmarking but is not shipped (`STANDARDS.md` section 1.12).

### Build

```bash
git clone https://github.com/hanshuebner/herold.git
cd herold
go build -trimpath -o ./herold ./cmd/herold
```

The release build flags match what CI ships:

```bash
CGO_ENABLED=0 go build \
  -trimpath -buildvcs=true \
  -ldflags "-s -w" \
  -o ./herold ./cmd/herold
```

For the local CI lane (vet, staticcheck, race-tested unit + integration
tests against both SQLite and Postgres):

```bash
make ci-local
```

### Tabard SPA

Herold embeds the tabard consumer SPA (HTML/JS/CSS) and serves it on
the public HTTP listener at `/` (REQ-DEPLOY-COLOC-01..05). The default
source build embeds a placeholder index.html that documents the embed
step; release builds bake the matching tabard build into the binary
via:

```bash
TABARD_DIST=/path/to/tabard/apps/suite/dist make embed-tabard
make build
```

Or directly:

```bash
TABARD_DIST=/path/to/tabard/apps/suite/dist scripts/embed-tabard.sh
go build -trimpath -buildvcs=true -o ./herold ./cmd/herold
```

The default `TABARD_DIST` is `/Users/hans/tabard/apps/suite/dist` (the
sibling-repo layout used during development); override the env var to
point at any tabard build output.

For development hot-reload, leave the binary alone and point the
running server at a freshly built tabard dist via system.toml:

```toml
[server.tabard]
asset_dir = "/abs/path/to/tabard/dist"
```

The override reads from disk on every request, so a `pnpm build` in
the tabard tree appears at the next browser refresh without
rebuilding herold. The path MUST be absolute and contain `index.html`
at startup; otherwise the server refuses to start with a clear error.

The pinned tabard SHA the current herold release expects lives in
`deploy/tabard.version`; see `deploy/tabard.version.README`.

### Where the binary expects things

By default:

- **System config:** `/etc/herold/system.toml`. Override via
  `--system-config <path>` or the `HEROLD_SYSTEM_CONFIG` environment
  variable (REQ-OPS-02).
- **Data directory:** the `[server] data_dir` value in `system.toml`.
  The server creates the directory if it does not exist; SQLite's
  `*.sqlite` file, the blob tree, the FTS index, the queue state, and
  the ACME account material all live underneath. Plugins typically
  live in `<data_dir>/plugins/` though their path is configured per
  plugin block.
- **PID file:** `/var/run/herold.pid` (override via
  `HEROLD_PID_FILE`). The PID file is what `herold server reload`
  reads to find the running server.
- **Credentials file:** `~/.herold/credentials.toml`. Written by
  `herold bootstrap` so subsequent CLI invocations from the same
  shell can find the admin REST URL and bearer token without flags.

The binary is a single static executable; there are no runtime
dependencies beyond a working filesystem and network stack.

## Docker

### Pull or build the image

The `deploy/docker/Dockerfile` is a multi-stage build with a
distroless `nonroot` runtime. From a checkout:

```bash
docker build -f deploy/docker/Dockerfile -t herold:dev .
```

(Once the publish pipeline lands, an official image will be available
on a public registry; until then, build from source.)

### One-command stack

The example compose file at
[`docs/user/examples/docker-compose.yml`](./examples/docker-compose.yml)
runs herold against SQLite, with the admin HTTP surface exposed on
loopback only and no Postgres / NATS / Ollama dependencies. It is the
shape recommended for the README quickstart:

```bash
docker compose -f docs/user/examples/docker-compose.yml up -d
```

For a development stack with Postgres, NATS, Ollama (for the LLM spam
plugin), and MailHog as a throwaway external SMTP target, see the
in-tree compose file at `deploy/docker/docker-compose.yml`. That one
is intentionally heavier and not aimed at the quickstart.

### Volume and port layout

The image expects:

- `/etc/herold/system.toml` mounted read-only - the system config.
- `/var/lib/herold/` mounted read-write - the data directory.
- Plugin binaries at `/usr/lib/herold/plugins/` (bundled in official
  packages; absent in the distroless image, so add a sidecar mount or
  bind a host directory if you need plugins).

Default exposed ports (adjust freely in the compose file):

| Port | Listener     | Purpose                                  |
|------|--------------|------------------------------------------|
| 25   | smtp-relay   | Inbound SMTP from the public internet    |
| 587  | smtp-submission | Authenticated submission              |
| 465  | smtp-submission (implicit TLS) | SMTPS submission       |
| 143  | imap         | IMAP + STARTTLS                          |
| 993  | imaps        | IMAPS (implicit TLS)                     |
| 4190 | managesieve  | ManageSieve                              |
| 8080 | admin        | Admin REST + UI + JMAP + healthz         |
| 9090 | metrics      | Prometheus `/metrics` (loopback default) |

Operator note: the metrics listener (REQ-OPS-90, STANDARDS section 7)
**defaults to loopback** and the `/metrics` handler does not perform
authentication. If you publish a non-loopback `metrics_bind`, front it
with TLS + auth at a reverse proxy.

### Container security posture

- Runs as `nonroot:nonroot`.
- Read-only root filesystem supported (REQ-OPS-151); only the data
  directory needs to be writable.
- No embedded secrets in the image. Secrets come from env vars,
  bind-mounted files, or systemd `LoadCredential` / Docker / K8s
  secrets, referenced from `system.toml` as `$VAR` or `file:/path`.

## Debian / RPM packages

Skeletons live under `deploy/debian/` and `deploy/rpm/`. When wired
they will provide:

- `/usr/local/bin/herold` - the binary.
- `/etc/herold/system.toml` - placeholder config; operator edits in
  place.
- `/var/lib/herold/` - data directory (owned by `herold:herold`).
- `/usr/lib/herold/plugins/` - first-party plugins.
- `/etc/systemd/system/herold.service` - `Type=notify` unit calling
  `herold server start`.
- `/etc/logrotate.d/herold` - log rotation when not running under a
  manager.

Status: package files are checked in but the build / publish pipeline
is `TODO(operator-doc): packaging-publish-wave`. For now operators
build the binary from source and install it manually, or use the
Docker image.

## Kubernetes

Manifests under `deploy/k8s/` (REQ-OPS-152). Sketch only in v1; not a
Helm chart. Expected shape:

- A `StatefulSet` with a single replica (single-node only - herold
  does not cluster, ever; see NG2 in `docs/design/00-scope.md`).
- A `PersistentVolumeClaim` for the data directory.
- A `ConfigMap` for `system.toml`.
- `Secret`s referenced as `file:/run/secrets/...` in the config.
- A `Service` exposing the admin / IMAP / SMTP listeners as needed.

A real cluster operator should treat this as a starting point and
adapt to their ingress, cert-manager, and storage class conventions.

Cluster topology is explicitly out of scope for herold v1, which is
a single-node design (see `docs/design/00-scope.md`, non-goal NG2).
There is no supported clustering mode. Operators requiring horizontal
scale should run multiple independent herold instances with disjoint
domain assignments rather than a shared clustered topology. The
`StatefulSet` replica count above is intentionally 1 and should not
be increased.

## Required system resources

Sized for the v1 scale target (1k mailboxes, 100 domains, 10k+10k
msg/day, ~10 TB total per node - see `docs/design/00-scope.md`
"Target scale (v1)"). Recommended minimum on bare metal or a hosted
VM:

| Dimension | Minimum    | Recommended | Notes                            |
|-----------|------------|-------------|----------------------------------|
| CPU       | 2 vCPU     | 8 vCPU      | LLM spam call dominates at scale |
| RAM       | 4 GB       | 32 GB       | Bleve FTS benefits from RAM      |
| Disk      | 50 GB      | 1 TB+ NVMe  | Mail volume + FTS + ACME state   |
| Network   | 10 Mbps    | 100 Mbps    | Outbound burst + IMAP IDLE       |

The scale target assumes provisioned hardware (8 vCPU, 32 GB RAM,
NVMe). At the bottom end (2 vCPU / 4 GB / 50 GB) herold runs fine for
a personal mail server with a handful of mailboxes. Above that, scale
RAM with FTS index size and disk with mail volume; CPU rarely
saturates outside spam classification (which is a plugin call to a
local Ollama or a remote OpenAI-compatible endpoint, not on-CPU
herold work).

Encryption at rest is **not implemented** in herold (NG10). Operators
who need disk-level confidentiality run on LUKS / ZFS native
encryption / FileVault.

## TLS

Production deployments must use TLS for IMAPS, JMAP, the admin
surface, and the MTA-STS vhost. Two cert sources (REQ-OPS-40):

1. **ACME** (default for internet-facing). Configured under `[acme]`
   in `system.toml`; the herold ACME client supports HTTP-01,
   TLS-ALPN-01, and DNS-01 (the latter via DNS-provider plugin -
   first-party plugins ship for Cloudflare, Route53, Hetzner, and a
   manual / webhook generic).
2. **File-based.** Operator-supplied `cert_file` / `key_file` per
   listener. For environments with cert-manager, an internal PKI, or
   a wildcard cert from elsewhere.

ACME is live as of Wave 3.3. Configure `[acme]` in `system.toml` with
`email` and optionally `directory_url` (defaults to Let's Encrypt
production). See [./operate.md](./operate.md) for the full TLS / ACME
runbook and `herold cert list` to inspect issued certificates.

For the development quickstart on loopback only, `tls = "none"` on the
admin listener is acceptable and the example compose file uses that
shape.

## Storage backend

Pick at install (REQ-STORE / REQ-OPS-03 extension):

- **SQLite** (default, zero-dep). Pure-Go `modernc.org/sqlite`. The
  right choice for <= 200 mailboxes, single-host deployments, and
  every quickstart. The DB file lives at `<data_dir>/herold.sqlite`
  by default. WAL is enabled; concurrent reads do not block writes.
- **Postgres** (for heavier deployments). Pure-Go `pgx/v5`. The right
  choice when you have 200+ mailboxes, sustained high-concurrency
  writes (large-mailbox clients doing concurrent heavy writes), or an
  existing Postgres operations practice. Configure via
  `[server.storage] backend = "postgres"` and a `postgres.dsn`.

The two backends are first-class. CI runs every integration test
against both. Code that works on only one is not mergeable
(`STANDARDS.md` section 1.8).

To switch backends after install, use `herold diag migrate`:

```bash
herold diag migrate \
  --to-backend postgres \
  --to-dsn "postgres://herold:secret@localhost:5432/herold?sslmode=disable" \
  --to-blob-dir /var/lib/herold/blobs
```

The migration is offline: stop the server, run the migrate, point
`system.toml` at the new backend, start the server. See
[./operate.md](./operate.md) "Upgrades and migration" for detail.

## First-run bootstrap

After the binary is installed and `system.toml` is in place, start the
server and bootstrap the first admin principal.

### Start the server

Source / package install:

```bash
herold server start --system-config /etc/herold/system.toml
```

Or under systemd:

```bash
systemctl start herold
```

The server binds every listener declared in `system.toml`, opens the
store, applies pending schema migrations, starts every declared
plugin, and reports `ok` on `/healthz/ready` once everything is up
(REQ-OPS-110, REQ-OPS-111).

### Validate the config without starting

```bash
herold server config-check /etc/herold/system.toml
```

Exits 0 on success. On validation failure exits 2 with an actionable
message naming the offending key (REQ-OPS-06).

### Bootstrap the admin principal

The bootstrap command creates the very first principal and prints a
one-time API key the operator stores.

```bash
herold bootstrap --email admin@example.com --password 'changeme123'
```

Flags:

- `--email <addr>` - required. The admin's canonical email.
- `--password <str>` - optional. If omitted, the server generates a
  20-character random password and prints it.
- `--save-credentials=true` - default. Writes the API key and
  `--server-url` (if given) to `~/.herold/credentials.toml`. Subsequent
  `herold` CLI calls authenticate against the admin REST without
  needing `--api-key` flags.
- `--server-url <url>` - recorded in the saved credentials file (e.g.
  `https://127.0.0.1:8080`).

Behaviour:

- Refuses to run if any principal already exists in the store. Exits
  10 (`ExitBootstrapAlreadyDone`) in that case.
- Marks the created principal as admin (`PrincipalFlagAdmin`) so the
  printed API key can run admin-gated REST mutations (domain add,
  principal create, etc.). Subsequent principals are non-admin by
  default; admin is granted via `PATCH /api/v1/principals/{pid}`.
- Stores the password Argon2id-hashed and the API key SHA-256-hashed.
  Neither value is recoverable from the server after the command
  returns. Capture them now.

### Add the first hosted domain

```bash
herold domain add example.com
```

Once the domain is registered, herold:

- Generates DKIM keys for the domain (per-domain, per-selector).
- Builds the set of DNS records the domain needs (DKIM TXT,
  `_mta-sts` TXT, `_smtp._tls` TXT, `_dmarc` TXT, SPF TXT) and, if a
  DNS plugin is configured, publishes them via the plugin
  (REQ-OPS-60). With no DNS plugin, herold falls back to "emit record
  text, operator publishes manually" mode and prints the records.

### Add the first user principal

User principal management lives in
[./administer.md](./administer.md). The minimum:

```bash
herold principal create user1@example.com --password 'pw1'
```

### Connect a client

With a domain and a principal in place, point an IMAP client at the
configured listener. For the quickstart shape (loopback ports
1143 / 1993 / 8080), see the README. For production, the standard
ports are 143 (IMAP+STARTTLS), 993 (IMAPS), 587 (submission), 465
(submission+implicit TLS), 8080 (admin REST + JMAP).

## Where to next

- Day-2 operation, system.toml reference, TLS / DNS / observability
  runbook: [./operate.md](./operate.md).
- Application administration (domains, principals, mailboxes,
  aliases, API keys, Sieve, audit log): [./administer.md](./administer.md).
- Real-domain walkthrough (DNS records, ACME, DKIM publication,
  DMARC, MTA-STS, TLS-RPT):
  [./quickstart-extended.md](./quickstart-extended.md).
- Spec / requirements / architecture (the historical record): the
  `docs/design/` tree.
