# Phase 2.5 punch list -- 2026-04-27

Source of truth: `docs/design/server/implementation/02-phasing.md` § Phase 2.5
(lines 102-125). Cross-references:

- `docs/design/server/notes/review-wave4-standards.md` (2026-04-24)
- `docs/design/server/notes/review-wave4-security.md` (2026-04-24)
- `docs/design/server/notes/admin-cli-triage-2026-04-26.md`
- `docs/design/server/implementation/03-testing-strategy.md`

This is an audit, not an implementation plan. Effort estimates assume
one engineer at 100% per `02-phasing.md:148`.

## Summary table

| # | Deliverable | Status | Owning agent | Estimate |
|---|---|---|---|---|
| 1 | Conformance test suites passing | IN-PROGRESS | conformance-fuzz-engineer | 2 weeks |
| 2 | Fuzz targets on every wire parser + week-long campaign | IN-PROGRESS | conformance-fuzz-engineer | 0.5 week |
| 3 | Load testing at REQ-NFR-01 (100 msg/s, 1 k IDLE) | NOT-STARTED | conformance-fuzz-engineer | 1.5 weeks |
| 4 | 1 TB mailbox benchmark | NOT-STARTED | storage-implementor + conformance-fuzz-engineer | 1 week |
| 5 | Operator manual (`operate.md`) | DONE | doc-author | 0 |
| 6 | Admin reference (`administer.md`) | IN-PROGRESS | doc-author | 0.25 week |
| 7 | Config reference | DONE | doc-author | 0 |
| 8 | DNS-setup guide | NOT-STARTED | doc-author | 0.5 week |
| 9 | Plugin developer guide | IN-PROGRESS | plugin-platform-implementor + doc-author | 0.5 week |
| 10 | Migration guide (SQLite ↔ Postgres) | IN-PROGRESS | storage-implementor + doc-author | 0.25 week |
| 11 | Troubleshooting guide | NOT-STARTED | doc-author | 0.5 week |
| 12 | SES porting guide | NOT-STARTED | doc-author | 0.5 week |
| 13 | Packaging: `.deb` / `.rpm` | NOT-STARTED | release-engineer (TBD) | 1 week |
| 14 | Packaging: Docker image | DONE | release-engineer | 0 |
| 15 | Packaging: K8s example manifests | NOT-STARTED | release-engineer (TBD) | 0.5 week |
| 16 | Plugin SDK examples (DNS / webhook events / spam) | IN-PROGRESS | plugin-platform-implementor | 0.25 week |
| 17 | Performance characterization + tuning guide | IN-PROGRESS | doc-author | 0.5 week |
| 18 | External / community security review | NOT-STARTED | security-reviewer + external party | calendar-bound |

Estimated remaining engineering: **~9 person-weeks**, aligned with the
phasing doc's 10-week budget at line 123. The external security
review is calendar-bound and cannot be compressed by adding effort.

## Per-item detail

### 1. Conformance test suites passing — IN-PROGRESS

Phasing-doc cross-cut at `02-phasing.md:142`: imaptest, SMTP interop,
JMAP corpus, Pigeonhole, DKIM/DMARC vectors.

**Evidence:** `test/interop/run.sh` + docker-compose harness
(`test/interop/docker-compose.yml`, `runner/scenarios/`); imaptest
scenario landed in `cba8bff` (`test/interop/run-imaptest.sh`); DKIM /
DMARC vectors via `internal/maildkim` + `internal/maildmarc` unit + fuzz
suites; CI job at `ci.yml:132-155`.

**Gap:** no Pigeonhole-driven Sieve run; no JMAP / Fastmail corpus run;
CI conformance step still guards on "no Go packages → skip"
(`ci.yml:151-155`), so green CI does not imply conformance passing.
REQ-PROTO-10..14 (DSN / ENHANCEDSTATUSCODES) marked partial in the
wave-4 REQ trace.

**Estimate:** 2 weeks. **Owner:** `conformance-fuzz-engineer`.

### 2. Fuzz targets on every wire parser + week-long campaign — IN-PROGRESS

**Evidence:** 32 `Fuzz*` entrypoints. Wave-4 gaps for SMTP/IMAP/SASL
closed: `protosmtp/parser_fuzz_test.go` (5), `protoimap/parser_fuzz_test.go`
(4), `sasl/sasl_fuzz_test.go` (6). Phase-2.9 parsers also covered:
`protochat/frames_fuzz_test.go`, `protocall/lifecycle_fuzz_test.go`,
JMAP calendars + contacts. Nightly at 45m/target (`nightly.yml:21-26`).

**Gap:** two wave-4-flagged surfaces still uncovered: `internal/protoadmin`
(REST JSON request bodies) and `internal/plugin/codec.go` (JSON-RPC frame
parser) — `grep "fuzz"` on either is empty. No week-long pre-release
campaign scheduled — nightly tops out at ~24h cumulative.

**Estimate:** 0.5 week (two `Fuzz*` files + a release-branch fuzz
workflow). **Owner:** `conformance-fuzz-engineer`.

### 3. Load testing at REQ-NFR-01 (100 msg/s, 1 k IDLE) — NOT-STARTED

**Evidence:** `test/load/` does not exist; nightly job bails
(`nightly.yml:38-44`: "test/load not yet populated"). Scenarios
specified in `03-testing-strategy.md:80-93`. REQ-NFR-01 marked **N** in
wave-4 REQ trace (`review-wave4-standards.md:190`).
`internal/storesqlite/bench_spike_test.go` covers storage benchmarks
only.

**Gap:** the entire harness — concurrent SMTP client pool, IDLE pool,
FETCH driver, mixed-workload runner, pprof capture, pass/fail gates.

**Estimate:** 1.5 weeks. **Owner:** `conformance-fuzz-engineer` with
input from each proto-implementor.

### 4. 1 TB mailbox benchmark — NOT-STARTED

**Evidence:** no 1 TB harness (`find -name "*1tb*"` empty).
`internal/storesqlite/bench_spike_test.go` runs at 1M-row scale
(`BenchmarkInsertSustained` line 108, `BenchmarkLargeScan` line 265).
`operate.md:1086-1089` mentions the 1 TB scale qualitatively but cites
no measurement.

**Gap:** no harness, no latency profile, no measurement.

**Estimate:** 1 week. **Owner:** `storage-implementor` (import shape) +
`conformance-fuzz-engineer` (driver).

### 5. Operator manual (`operate.md`) — DONE

`docs/user/operate.md` exists (performance §1082-1133, queue knobs,
log overrides, plugin section §922-930). Recent commits `5db9846`,
`e1a929e`, `bee8aa2` covered REQ-OPS-82 trace levels, SQLite pragmas,
queue concurrency. Remaining `TODO(operator-doc)` markers
(admin-cli-triage:512-528) are config-shape pointers tied to feature
waves. **Estimate:** 0. **Owner:** `doc-author`.

### 6. Admin reference (`administer.md`) — IN-PROGRESS

`docs/user/administer.md` exists; Wave 3.4 wired the 13 Bucket-1 cobra
subcommands per `admin-cli-triage-2026-04-26.md`. Bucket-2/3 items
(DKIM generate/show, Sieve admin CRUD, categorise prompt mutation,
mailbox CRUD) are Phase-2.5-adjacent and need REST + CLI surfaces.
`administer.md:493` carries the `chat-cal-contacts-admin-cli-not-yet-wired`
TODO (Phase 2 material).

**Estimate:** 0.25 week (prose only; Bucket-2/3 work tracked under
separate waves). **Owner:** `doc-author`.

### 7. Config reference — DONE

Embedded in `operate.md` (system.toml reference ~§234, `[[plugin]]`
§234-269, ACME §428, queue §841-897). Phasing doc does not require a
separate file. **Estimate:** 0. **Owner:** `doc-author`.

### 8. DNS-setup guide — NOT-STARTED (standalone)

`install.md:540-580` covers the seven records + plugin-publish path
(§573-578); `quickstart-extended.md` walks one provider. No standalone
`docs/user/dns-setup.md`.

**Gap:** a dedicated guide walking "manual records → first ACME success
→ publish DKIM" plus per-plugin records-to-paste cheatsheets.

**Estimate:** 0.5 week. **Owner:** `doc-author`.

### 9. Plugin developer guide — IN-PROGRESS

`plugins/sdk/doc.go:1-30+` has a minimal DNS-plugin example;
`plugins/herold-echo/main.go` is the SDK exerciser (78 LOC);
`docs/design/server/architecture/07-plugin-architecture.md` exists
(designer-facing); four DNS provider plugins serve as exemplars.

**Gap:** no user-facing `docs/user/plugins.md`; no "write your own
plugin" walk-through; no per-plugin `README.md`.

**Estimate:** 0.5 week. **Owner:** `plugin-platform-implementor`
(technical) + `doc-author` (prose).

### 10. Migration guide (SQLite ↔ Postgres) — IN-PROGRESS

`internal/diag/migrate/migrate.go` + `migrate_test.go` ship the tool;
`manual-test-runbook.md:138-157` validates the SQLite → Postgres path;
`install.md:330-332` cross-references `operate.md`.

**Gap:** Postgres-leg test currently skipped (`458a1e1`) pending the
storepg backup adapter — Postgres → SQLite cannot be honestly claimed
until that lands. No standalone `docs/user/migrate.md`.

**Estimate:** 0.25 week (doc) once the adapter ships. **Owner:**
`storage-implementor` (adapter) + `doc-author` (prose).

### 11. Troubleshooting guide — NOT-STARTED

Ad-hoc "Troubleshooting:" sub-section at `quickstart-extended.md:213`
covers cert / DNS / SMTP-bounce for the quickstart only; `operate.md`
covers individual failure modes (ACME §492, performance §1082) but not
as a unified flow.

**Gap:** a dedicated guide covering TLS-cert acquisition, SMTP delivery,
IMAP login, plugin-supervisor restarts, FTS-stuck, queue-stuck,
admin-REST 401/403, OIDC link failures.

**Estimate:** 0.5 week. **Owner:** `doc-author`.

### 12. SES porting guide — NOT-STARTED

No `docs/user/ses-porting.md`. SES-shape surfaces exist: HTTP send +
webhooks landed in Phase 2/3 (`f1e9714`, `25a175d`);
`internal/observe/metrics_ses.go` carries SES-shape metrics. Scope is
NG12 (`00-scope.md:98`, `open-questions.md:82`) — HTTP send + webhooks,
not SigV4 / SNS / receipt-rule DSL.

**Gap:** the porting guide. Side-by-side translation table:
`SendEmail` → `POST /api/v1/mail/send`; SES receipt rules → Sieve;
SES bounce/complaint/delivery events → herold webhooks.

**Estimate:** 0.5 week. **Owner:** `doc-author`.

### 13. Packaging: `.deb` / `.rpm` — NOT-STARTED

`install.md:20-21,238-239` mark `.deb`/`.rpm` as roadmap (REQ-OPS-150).
`deploy/` contains only `docker/` and the `tabard.version` SPA pin.
REQ-OPS-150 (`09-operations.md:220`) mandates `.deb` + `.rpm` + Docker
image + static-musl tarball, with first-party plugins bundled.

**Estimate:** 1 week (`nfpm.yaml`, `release.yml` tag-trigger, signing,
GitHub-Releases or hosted-repo path). **Owner:** `release-engineer`
(role does not yet exist in `AGENTS.md` — see open question 5).

### 14. Packaging: Docker image — DONE

`deploy/docker/Dockerfile` (multi-stage distroless nonroot per
`install.md:17-19`); `Dockerfile.plugin`; `docker-compose.yml`;
`make docker` (`Makefile:81`). **Estimate:** 0.
**Owner:** `release-engineer`.

### 15. Packaging: K8s example manifests — NOT-STARTED

`install.md:23-26,§244` mark K8s manifests as roadmap (REQ-OPS-152).
`find -path "*/k8s/*"` is empty. REQ-OPS-152 calls for "StatefulSet +
ConfigMap/Secret in `deploy/k8s/`. Not a Helm chart in v1."

**Estimate:** 0.5 week (~300 lines of YAML + listener services + PVC
for data dir). **Owner:** `release-engineer`.

### 16. Plugin SDK examples (DNS / webhook events / spam) — IN-PROGRESS

DNS: four (`herold-dns-{cloudflare,route53,hetzner,manual}`). Spam:
`herold-spam-llm`. Event-publisher: `herold-events-nats`. SDK exerciser:
`herold-echo`.

**Gap:** the phasing doc names a "minimal webhook event-publisher", but
webhooks are a built-in subsystem (`internal/protowebhook/`), not a
plugin shape. `herold-events-nats` is a NATS publisher, not a webhook
publisher. May be a phasing-doc artefact — see open question 1.

**Estimate:** 0.25 week if the webhook example is required; else 0.
**Owner:** `plugin-platform-implementor`.

### 17. Performance characterization + tuning guide — IN-PROGRESS

`operate.md:1082-1133` has a tuning section (FTS, SQLite WAL, queue
concurrency, plugin RPC); pprof wired (`03-testing-strategy.md:93`);
admin listener serves `/debug/pprof/*` post-`47b7f3f`; storage
benchmarks at `internal/storesqlite/bench_spike_test.go`.

**Gap:** no published characterization report. The phasing-doc requires
a latency / throughput / heap profile at v1 scale. Output of items 3 + 4.

**Estimate:** 0.5 week, sequenced after items 3 + 4. **Owner:**
`doc-author` + pprof traces from items 3 + 4 owners.

### 18. External / community security review — NOT-STARTED (external)

An **internal** Wave-4 review exists
(`docs/design/server/notes/review-wave4-security.md`, 2026-04-24, 73 lines).
Several wave-4 findings have closed since: SCRAM channel binding via
`internal/sasl/cbinding.go` and `protosmtp/session.go:1267`'s
`endpointBinding`; metrics registration via `internal/observe/metrics_*.go`;
per-IP IMAP cap at `protoimap/server.go:69-70`; OIDC RP fixes under
`f21493b`.

**Gap:** no external-firm or community pass. Wave-4 internal review
reads as interim — substantial subsystems (chat, call, JMAP
calendars/contacts, reactions, webhooks, send, queue) have landed
since.

**Estimate:** calendar-bound. Internal re-pass: 1 week
(`security-reviewer`). External: 1-4 weeks scheduling + 1 week review
window. **Cannot be compressed by adding internal effort.**

## Recommended sequence

The shortest credible path to a 1.0 cut sequences items by dependency,
not by deliverable order:

1. **Foundation (weeks 1-3).** Items 3 (load harness) and 4 (1 TB
   benchmark) build first because they produce the data that items 1
   and 17 consume; item 2's two missing fuzz targets and the release-
   fuzz workflow are 0.5 week of parallel work. Build `test/load/`
   from `03-testing-strategy.md:80-93` scenarios.

2. **Documentation cluster (weeks 3-5).** Items 8, 9, 11, 12 are
   primarily prose and parallelisable across two writing passes. Item
   10 blocks on the storepg backup adapter landing in a separate wave.
   Item 17's write-up joins this cluster once items 3 and 4 produce
   data.

3. **Packaging (weeks 5-7).** Items 13 + 15 sequence after the docs
   settle, because their install paths must match the final binary
   layout.

4. **Conformance close-out (weeks 7-8).** Item 1 (Pigeonhole + JMAP
   corpus + CI green-or-red gating) sequences after load + packaging;
   conformance gaps surfaced under load testing typically need
   proto-implementor fixes, not harness fixes.

5. **Security review (weeks 8-10, calendar-bound).** Item 18 has a
   floor regardless. Schedule the external engagement at week 4 to
   land in weeks 8-10. Internal re-pass at week 7.

The smallest sequence to a defensible 1.0 cut is roughly 9 engineering
weeks plus a 2-3 week external-review window — within the phasing doc's
10-week budget if external review overlaps engineering rather than
adding to it.

## Open questions for the operator

1. **Webhook-publisher example plugin (item 16).** Webhooks are a
   built-in subsystem now, not a plugin shape. Either drop the
   webhook-plugin requirement from 1.0 or confirm and add it.

2. **Phase 3 features as 1.0 prerequisites?** Phasing-doc §127-138
   (expanded Web UI, WebAuthn, Web Push, importer, OCR, additional
   DNS plugins, SCIM, acme-dns) are flagged "post-v1.0; pick based on
   demand." Confirm none of these are pre-1.0 blockers.

3. **External security review choice — vendor or community?** Decide
   before week 4 of the schedule, otherwise the calendar floor at
   item 18 slips.

4. **Migration guide sequencing (item 10).** The Postgres-leg test
   skip in `458a1e1` is gated on the storepg backup adapter. Slot
   that work into a wave or accept a doc that omits Postgres → SQLite
   at 1.0?

5. **Release-engineer agent role.** `AGENTS.md` does not enumerate
   one. Items 13 + 15 need an owner. Either (a) extend `AGENTS.md`
   (recommended; the work spans .deb, .rpm, Docker, K8s, release.yml)
   or (b) split between existing agents.

6. **Bucket 2 admin-CLI items as 1.0 blockers?** From
   `admin-cli-triage-2026-04-26.md`: DKIM generate/show probably
   1.0 (rotation must be operable); Sieve admin CRUD probably defer
   (ManageSieve covers per-user); categorise prompt mutation defer.
   Confirm.

## Items where status could not be established without code change

- **Conformance "passing" (item 1).** CI conformance currently skips
  when `test/interop/` has no Go files. Whether the latest pytest
  harness run is fully green requires running the harness, not reading
  the tree. Audit-pass classification "IN-PROGRESS" is conservative.

- **Item 18 internal-vs-canonical ambiguity.** Whether
  `review-wave4-security.md` is "the pre-1.0 internal pass" or "a
  Wave-4 interim that needs a redo before 1.0" is a governance call.
  This audit defaults to the latter reading — substantial subsystems
  have landed since 2026-04-24.
