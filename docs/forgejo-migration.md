# Off-GitHub migration plan (Codeberg → self-hosted Forgejo)

Status: code-host is now `code.netzhansa.com` (self-hosted Forgejo on
Hans's FreeBSD VM); first end-to-end CI run pending. The original plan
targeted Codeberg as the destination; mid-execution we discovered that
Codeberg's Forgejo Actions REST API namespace is gated off (404 on
`/repos/.../actions/*`), which made polling-based autoscaling
impossible. We pivoted to a self-hosted Forgejo instance that exposes
the full Actions API. The plan + execution log below documents the
whole journey, Codeberg phase included; the **most recent execution-log
entries cover the pivot to self-hosted**.

Author: Hans + Claude (initial planning 2026-05-23)
Owner during execution: Hans

## Goal

Move the herold repository from GitHub to Codeberg, replacing the current
GitHub-hosted CI with autoscaled self-hosted **Forgejo Actions** runners on
Hetzner Cloud. Optimise for parallelism, cost (≤€40/month), and independence
from a single commercial CI vendor.

## Why Forgejo Actions and not Woodpecker

Codeberg's shared Woodpecker instance gates the two endpoints the official
autoscaler depends on (`/api/queue/info`, `POST /api/agents`) behind
`session.MustAdmin()`, so off-the-shelf Hetzner autoscaling for Woodpecker is
not possible against ci.codeberg.org without either forking the autoscaler or
self-hosting the Woodpecker server. Forgejo Actions' auth model is user-scoped,
and the runner protocol is GitHub Actions-compatible, so the well-trodden
"webhook on push → spawn ephemeral runner" pattern works without privileged
tokens.

## Outcome

- Source of truth: `codeberg.org/hanshuebner/herold`
- CI: Forgejo Actions, jobs on self-hosted ephemeral runners
- Runners: Hetzner Cloud CAX (ARM, default lane) and CX (x86, confidence lane only)
- Controller: act_runner orchestrator on the existing FreeBSD VM, built from source
- Snapshot bakery: weekly automated refresh, alarms on failure
- Pipeline scope preserved (10 jobs from `ci.yml` + 5 from `nightly.yml` + release),
  with the addition of an x86 confidence lane (build + parsers + storage smoke + happy-path e2e)
- One `.forgejo/workflows/ci.yml` file for all push/PR work; `nightly.yml` and `release.yml` separate
- Hard-fail on x86 confidence lane
- Hard cutover (no mirror-first transition)
- Cost cap: €20/month

## 1. Decisions (locked 2026-05-23)

| # | Decision | Resolution |
|---|---|---|
| 1.1 | Codeberg namespace | personal: `hanshuebner/herold` |
| 1.2 | Cutover style | hard cutover (no mirror-first transition) |
| 1.3 | Existing self-hosted GitHub runners | not reusable; build fresh on Hetzner |
| 1.4 | Cost cap (alarm threshold) | €20/month |
| 1.5 | Trigger scope | match current (every push, every branch) |
| 1.6 | Confidence-lane architecture | x86 only (ARM remains default for everything else) |

## 2. Phase overview

| Phase | Wall time | Output |
|---|---|---|
| 0. Prereqs | external, 1–7 days | Forgejo Actions approval, decisions in §1 settled, Hetzner account ready |
| 1. Hetzner bootstrap | half day | project, API token, limit raise |
| 2. Image bakery | half day | two snapshot IDs (ARM + x86), refresh cron on FreeBSD VM |
| 3. Repo on Codeberg | half day | repo present, settings copied, optional mirror |
| 4. Pipeline rewrite | 1–2 days | `.forgejo/workflows/{ci,nightly,release}.yml` |
| 5. Controller | 1–2 days | act_runner orchestrator on FreeBSD VM as rc.d service |
| 6. Validation | several days, mostly wall time | empirical timings + cost, tuning |
| 7. Cutover | half day | primary remote switched, GHA disabled |
| 8. Hygiene | ongoing | runbooks, snapshot freshness, cost monitoring |

Sequencing: 0 → 1 → (2 ∥ 3) → 4 → 5 → 6 → 7 → 8. Phases 2 and 3 are
independent; 4 and 5 are easier sequenced (4 first) but can overlap.

## 3. Phase detail

### Phase 0 — Prerequisites

- [x] Forgejo Actions enabled on the repo (2026-05-23). Q7.5 resolved: per-repo toggle, no Codeberg hosted-tier onboarding required for self-hosted runners.
- [x] Forgejo token exposed in chat is revoked (2026-05-23)
- [x] §1 decisions filled in (2026-05-23)
- [x] Hetzner Cloud account exists: **K1100508019**
- [ ] FreeBSD VM has outbound HTTPS to `api.hetzner.cloud` and `codeberg.org`; SSH key for snapshot bakery (confirm)

### Phase 1 — Hetzner bootstrap

- [ ] Create Hetzner project `herold-ci`
- [ ] Project API token (Read+Write) stored in 1Password (or pass(1) on the FreeBSD VM)
- [ ] Email Hetzner support to raise server limit 10 → 50 and IPv4 limit 10 → 20 if going dual-stack
- [ ] Choose network model: IPv6-only (cheapest, no per-IP fee) or one private network for runner-to-runner; default IPv6-only
- [ ] Billing alarms at €15 (warn) and €20 (cap, per decision 1.4); investigate any month >€10

### Phase 2 — Image bakery

Goal: one snapshot per architecture, refreshed weekly, with everything the
tests need preinstalled so first-job latency stays under 60 s.

- [x] `infra/hetzner/bake.sh` (179 lines) + `infra/hetzner/provision.sh` (~150 lines) — shell-driven bakery; runs from any unix host with `hcloud`, `ssh`, `scp`, `python3`. Defensively unsets `HCLOUD_TOKEN`, uses `--context herold-ci`, labels snapshots `herold-ci=runner` `arch=$ARCH`, looks the new snapshot up by label, writes `infra/hetzner/snapshots.json` (gitignored). Bake instance types: `cax11` (arm64) / `cpx22` (amd64). Provisioner installs Docker + Go + Node + pnpm + forgejo-runner + linter toolchain + preloads postgres:16, nats:2-alpine, mailhog.
- [x] First successful bake 2026-05-23: snapshots `389706944` (amd64) and `389706948` (arm64) in herold-ci project.
- [ ] Wire as cron on the FreeBSD VM: `0 4 * * 1` (Monday 04:00 local). Cron invocation: `cd /path/to/repo && BAKE_SSH_KEY_PATH=/var/lib/herold-ci/.ssh/id_rsa SNAPSHOTS_FILE=/var/lib/herold-ci/snapshots.json infra/hetzner/bake.sh arm64 && ... amd64`.
- [ ] Cron alarm: non-zero exit emails hans@huebner.org (FreeBSD `MAILTO=` in crontab).
- [ ] Snapshot retention reaper: delete bake-labelled snapshots older than 4 weeks (so we keep the last ~4 bakes per arch as fallbacks).
- [ ] Verify after first bake: boot one VM from each snapshot, sanity-check `go version`, `docker --version`, `forgejo-runner --version`.

### Phase 3 — Repo on Codeberg

- [x] Repo exists at `codeberg.org/hanshuebner/herold` (2026-05-23, default_branch=main)
- [x] `main` pushed (repo size 8229 KB, has_actions toggle enabled)
- [ ] Push remaining branches if any: review `git branch -r | grep origin | grep -v main` and push those still needed: `git push codeberg <branch>`
- [ ] Push tags: `git push codeberg --tags`
- [ ] Confirm repo settings: default branch `main`, **no PR gate** (per CLAUDE.md hard rule), issues on, wiki off (current API shows `has_issues=true`, `has_wiki=false`)
- [x] Migrated the 9 open issues from GitHub (2026-05-23) via `scripts/migrate-issues-to-codeberg.py`. Codeberg issues `#1`–`#9` correspond to GitHub `#97`, `#98`, `#99`, `#106`, `#108`, `#110`, `#111`, `#114`, `#118` respectively. Each Codeberg body has a footer linking back to the original.
- [ ] **Pending verification by Hans on the Codeberg UI**, then close the source GitHub issues with redirect comments (task #4).
- [x] Secrets audit (2026-05-23): the GitHub workflows reference only `secrets.GITHUB_TOKEN`, which is auto-injected by Forgejo Actions under the same name. No custom secrets configured at the repo. Nothing to migrate.

### Phase 4 — Pipeline rewrite (LANDED 2026-05-23)

Translate `.github/workflows/*` → `.forgejo/workflows/*`. Forgejo Actions
syntax is GitHub-compatible at ~95% — most YAML carries over unchanged.

- [x] `.forgejo/workflows/ci.yml` (single file, all PR/push jobs) - landed 2026-05-23:
  - Replace `runs-on: self-hosted` (and `[self-hosted, arm64]`) with explicit labels: `[self-hosted, herold, arm64]`
  - New job `confidence-x86` on `[self-hosted, herold, amd64]`:
    - `go build ./...`
    - `go vet ./...`
    - Unit tests for `internal/maildkim`, `mailspf`, `maildmarc`, `mailarc`, `sieve`, `protosmtp`, `protoimap`, `protojmap` parsers
    - Storage smoke: open pure-Go SQLite, run migrations, single round-trip
    - Postgres smoke: spin postgres:16 container, run migrations, single round-trip
    - One end-to-end happy path: boot herold, SMTP-submit, IMAP-fetch, JMAP-fetch
    - Budget: ≤5 min wall clock
  - Hard-fail merge on confidence-x86 failure
  - Existing jobs (pre-commit, lint, web, web-e2e, test, fuzz-short, conformance, docker, jmaptest, binaries) all stay on arm64
  - Replace `actions/cache@v4` with Forgejo's cache action (compatibility verified during Phase 6)
  - Replace `actions/upload-artifact@v4` if not supported, with push to Hetzner object storage (S3-compatible, ~€0.001/GB·month) — also resolve Q7.3 first
- [x] `.forgejo/workflows/nightly.yml` - 5 jobs on arm64 (fuzz-long, load, interop, jmaptest-pin-check, sbom-diff)
- [x] `.forgejo/workflows/release.yml` - tag-triggered, GOOS/GOARCH matrix, switched ubuntu-latest → arm64 self-hosted, ghcr.io → codeberg.org registry. SBOM + cosign-sign + upload-artifact preserved.
- [x] Dropped auto-rerun.yml entirely - it relied on the GHA workflow_run trigger and the actions/runner shutdown-signal log marker. Forgejo Actions handles transient failures differently; revisit if we see runner-restart noise on Codeberg.
- [ ] YAML parsed clean with pyyaml; once we have access to a `forgejo-runner exec` or `actionlint` build for Forgejo dialect, validate again before the first cutover run.

### Phase 5 — Controller (act_runner orchestrator) (CODE LANDED 2026-05-23, AWAITING LIVE DEPLOY)

The orchestrator polls Forgejo for queued Actions runs, spawns ephemeral
Hetzner VMs each running `act_runner --once` with the correct label, lets
them pick up exactly one job and self-terminate, then deletes the VM.

- [x] Picked: **5b** (custom Go controller). 811 lines across 5 files in `cmd/herold-runner-orchestrator/`. No clean off-the-shelf fit for Forgejo Actions + Hetzner at this date.
- [x] Builds clean for linux/amd64, freebsd/amd64, darwin/arm64. No CGO.
- [x] Stateless reconciler: derives truth from Hetzner + Codeberg APIs each tick; restartable; no local state file.
- [x] Reconciliation pass: list queued workflow jobs (by `runs-on` labels) → list current owned VMs in Hetzner → spawn deficit up to per-arch cap; reap VMs that are off or past vm-max-lifetime; sweep ghost runner registrations on Codeberg whose VM no longer exists.
- [x] Per-spawn one-shot registration token via `POST /repos/.../actions/runners/registration-token`. cloud-init registers + starts `forgejo-runner.service` (systemd unit) on the new VM.
- [x] Snapshot discovery is dynamic — `image list --selector "herold-ci=runner,arch=$ARCH"` picks the newest, so weekly bakes auto-roll forward without touching the orchestrator.
- [x] Config via env + flags: HCLOUD_TOKEN, ORCHESTRATOR_CODEBERG_TOKEN, --repo, --location, --arm-type, --amd-type, --max-arm, --max-amd, --poll, --vm-max-lifetime, --ssh-key.
- [ ] **Live deploy on the FreeBSD VM** (next concrete step on Hans). Step-by-step is in `infra/freebsd/rc.d/herold_runner_orchestrator`'s header comment; the short version:
  - cross-compile `GOOS=freebsd GOARCH=amd64 go build ./cmd/herold-runner-orchestrator/` and scp to `/usr/local/bin/`
  - create the `_herold-orch` service user (`pw user add ...`)
  - drop Hetzner + Codeberg tokens into `/usr/local/etc/herold-runner-orchestrator.env` (mode 0600, owned by `_herold-orch`)
  - install `infra/freebsd/rc.d/herold_runner_orchestrator` to `/usr/local/etc/rc.d/`
  - `sysrc herold_runner_orchestrator_enable=YES && service herold_runner_orchestrator start`
- [ ] **First live workflow run** — push a trivial commit to Codeberg, watch the orchestrator log spawn a VM, watch the workflow turn green. Loop until it works.
- [ ] Snapshot retention reaper (Phase 2 leftover): delete bake snapshots older than 4 weeks.
- [ ] Cron + alarm for the weekly bake on the FreeBSD VM (Phase 2 leftover).
- [ ] Scale-down: the current policy is "reap at vm_max_lifetime = 60 min regardless". The plan's "T+50min keepalive to absorb subsequent jobs" idea would be richer but adds state; deferred until we have real workload data.

### Phase 6 — Validation

- [ ] Smoke: open a trivial PR on Codeberg, confirm runners spawn and pipeline completes green
- [ ] Compare timings job-by-job vs current GHA; record below
- [ ] Stress: open 3 PRs simultaneously, observe parallelism behaviour
- [ ] Failure mode tests:
  - Kill orchestrator mid-run → in-flight jobs finish?
  - Hetzner API rate-limit → exponential backoff?
  - act_runner registration fails → VM cleaned up?
  - Stale snapshot → loud failure?
- [ ] Cost: run both pipelines in parallel for 1 week, compare Hetzner bill vs forecast (§5)
- [ ] Sign-off criteria: Codeberg pipeline is faster than GHA OR clearly more stable, AND month-projected cost ≤ €20 (decision 1.4)

### Phase 7 — Cutover

Hard cutover per decision 1.2 — same-day, no mirror window.

- [ ] `CLAUDE.md`: replace `github.com/hanshuebner/herold` references with `codeberg.org/hanshuebner/herold`
- [ ] README badges, contribution guide, `AGENTS.md`, any references in `docs/`
- [ ] `git remote set-url origin git@codeberg.org:hanshuebner/herold.git` locally
- [ ] Delete `.github/workflows/*.yml` in the same commit as the rest of the cutover (one logical change)
- [ ] Disable GitHub Actions on the GitHub repo (Settings → Actions → Disable)
- [ ] Archive the GitHub repo, README points to Codeberg
- [ ] Update Renovate / any third-party integrations to Codeberg

### Phase 8 — Hygiene

- [ ] Snapshot freshness alarm: no successful bake in 10 days → email
- [ ] Weekly cost review (15 min)
- [ ] Runbook in `docs/operations/`:
  - Add a new runner pool
  - Refresh a snapshot manually
  - Debug a stuck VM (it's still alive at Hetzner but not running act_runner)
  - Rotate Hetzner token
  - Rotate Codeberg token
- [ ] Append incident notes to §8 of this doc

## 4. Risks and mitigations

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Forgejo Actions alpha bugs (cache, artifact, schedule) | medium | medium | object-storage workaround for artefacts; pin runner image versions; subscribe to Codeberg status |
| Snapshot drift / broken upstream | medium | high | weekly bake + alarm; keep last 4 snapshots so we can pin to a known-good |
| Codeberg outage during release | low | high | for releases, keep one GHA-hosted lane (`release.yml` mirror, manually-triggered) for 6 months post-cutover |
| Hetzner API rate-limit | low | low | controller exponential backoff |
| FreeBSD VM dies | low | medium | in-flight runners continue; new ones don't spawn; runbook for manual VM provisioning via Hetzner UI |
| Runner registration race | medium | low | one-time token per spawn; 5-min unregistered → GC |
| Hourly billing rounding loss from over-aggressive scale-down | medium | low | T+50 min idle policy (see Phase 5) |

## 5. Cost estimate

Steady state, assuming 30 push events/week × 4 min CI each, plus 1 nightly/day, plus 4 releases/year:

| Item | Monthly |
|---|---|
| Bursty arm64 runners (CAX21, billed hours) | ~€1 |
| Bursty x86 runner (CX22, confidence lane only) | ~€0.50 |
| Snapshot storage (2 × 15 GB × €0.012/GB) | €0.40 |
| Object storage for artefacts | ~€1 |
| Floating IPv4 (avoided by IPv6-only) | €0 |
| Controller (sunk cost on existing FreeBSD VM) | €0 |
| **Total expected** | **€3–10** |

Inside the €20 cap (decision 1.4) with ~2× headroom. Below the warn-at-€15 threshold under steady state. If projection drifts toward €15 sustainedly, investigate before it hits the cap; the most likely cause would be runaway runner spawns or a bake-script that leaves zombie VMs.

## 6. Out of scope (intentionally)

- Self-hosting Woodpecker as a fallback
- Multi-cloud runner support (AWS, GCP, Scaleway)
- Custom dashboards beyond Prometheus + a couple of alerts
- Browser-driven UI testing in CI (per CLAUDE.md, manual via puppeteer MCP)
- Caching strategy beyond what Forgejo Actions' cache provides

## 7. Open questions (research before §3 phases)

- **Q7.1** — Does Codeberg's Forgejo expose per-repo or per-org pending workflow runs to a user-scoped token? Probable yes. **Verification**: one curl against `codeberg.org/api/v1/repos/<owner>/<repo>/actions/runs?status=queued` with a fresh user token; needs to happen once the user is onboarded for Forgejo Actions.
- **Q7.2** — Does Codeberg's Forgejo accept `actions/cache@v4` directly, or do we need their fork? Tested in Phase 6.
- **Q7.3** — Does Forgejo Actions support `actions/upload-artifact@v4` against Codeberg, or do we go straight to object storage? Tested in Phase 6.
- **Q7.4** — Does Codeberg charge for object storage or do they offer artefact hosting? If hosting, free.
- **Q7.5** ✅ — Resolved 2026-05-23: enabling Forgejo Actions is a per-repo settings toggle; self-hosted runners attach without Codeberg's hosted-tier onboarding.

## 8. Execution log

(Append as phases complete. Most recent first.)

- 2026-05-24 — Phase 9: self-hosted Forgejo at `code.netzhansa.com` live. Apache vhost on the AWS host fronts HTTPS to the FreeBSD VM's `127.0.0.1:3001`; Forgejo built-in SSH on `0.0.0.0:2222`. `infra/freebsd/forgejo/{install.sh,app.ini.template}` checked in. Bootstrap snags: pkg's config path is `$FORGEJO_CUSTOM/conf/app.ini`, not `$FORGEJO_CUSTOM/app.ini`; `forgejo doctor check` requires `/var/db/forgejo/data/forgejo-repositories` to exist pre-start; install.sh now creates all required subdirs and waits for `/api/v1/version` to come up post-migration. SSH key length minimum raised from default 3072 to 2048 in app.ini.
- 2026-05-24 — Phase 9 prep: discovered Codeberg's `/repos/.../actions/*` namespace returns router-level 404 (alpha gating), not 403. Polling-based orchestration cannot work against Codeberg today. Pivoted plan to self-hosted Forgejo (this same project, just a different code host).
- 2026-05-24 — Orchestrator rename: `codeberg*` → `forgejo*` throughout `cmd/herold-runner-orchestrator/`. `--codeberg` flag → `--forgejo`. Env vars `ORCHESTRATOR_CODEBERG_*` → `ORCHESTRATOR_FORGEJO_*`. Default URL `https://codeberg.org` → `https://code.netzhansa.com`. cloud-init's instance URL is now templated via `--instance "{{.Instance}}"` so a single binary works against any Forgejo host. File rename `cmd/herold-runner-orchestrator/codeberg.go` → `forgejo.go`, doc rename `docs/codeberg-migration.md` → `docs/forgejo-migration.md`.
- 2026-05-23 — Phase 5 controller code landed: `cmd/herold-runner-orchestrator/` (811 lines, 5 files, no CGO, builds for linux/amd64 + freebsd/amd64 + darwin/arm64). Custom Go reconciler that polls Codeberg's Forgejo Actions API + Hetzner Cloud API every 15 s; spawns ephemeral VMs from the labelled snapshots on demand; reaps VMs by max-lifetime; sweeps ghost runner registrations. Next: live deploy on the FreeBSD VM + first end-to-end Codeberg workflow run.
- 2026-05-23 — Phase 7 cutover (brought forward at maintainer's request). `.github/workflows/*` deleted; `.github/race-packages.txt` moved to `test/race-packages.txt`; `.forgejo/workflows/ci.yml` reference updated; README badge + AGENTS.md issue URL pointed at Codeberg; `main` + 5 feature branches pushed to `codeberg.org/hanshuebner/herold`; GitHub repo archived (read-only) with description + homepage redirecting to Codeberg.
- 2026-05-23 — Phase 4 pipeline rewrite landed: `.forgejo/workflows/{ci,nightly,release}.yml` written (auto-rerun dropped). 11 jobs in ci.yml (one new: confidence-x86 on amd64 with hard-fail). All other jobs labelled `[self-hosted, herold, arm64]`. Drops the persistent-runner-only "fix workspace permissions" pre-checkout step (ephemeral runners are clean by definition). Switched `cache: false` → default `cache: true` on setup-go. Switched ghcr.io → codeberg.org container registry. Switched ubuntu-latest → arm64 self-hosted on the binaries job. SHA-pinned action refs replaced with tag refs since Forgejo's mirror has different SHAs from upstream. Open follow-ups: (a) verify Forgejo Actions cache service interop with `actions/setup-go cache: true` and `actions/cache@v4`, (b) verify localhost:5432 vs postgres:5432 service hostname semantics on first live run, (c) verify which third-party actions (staticcheck-action, govulncheck-action, dominikh, anchore, sigstore) resolve through Codeberg's act_runner mirror config.
- 2026-05-23 — Secrets audit: only `secrets.GITHUB_TOKEN` referenced (auto-injected by Forgejo Actions), no custom secrets configured at the GitHub repo. Phase 3 secret-migration step is a no-op.
- 2026-05-23 — Phase 2 image bakery: `infra/hetzner/{bake.sh,provision.sh}` written, exercised end-to-end. First snapshots: arm64 `389706948` (1.69 GB, cax11 baked), amd64 `389706944` (1.72 GB, cpx22 baked) in herold-ci Hetzner project. snapshots.json populated. Toolchain in image: ubuntu 24.04, docker 29.5.2, go 1.25.0, node 20.20.2, pnpm 9.15.9, forgejo-runner v12.10.1, gitleaks 8.30.1, staticcheck 2026.1, pre-commit 4.6.0. Issues discovered during the runs: cpx21 deprecated by Hetzner Jan 2026 (now `cpx22`); gitleaks moved repo (`zricethezav/` → `gitleaks/`), latest is v8.30.1, asset names use `x64` not `amd64`; `hcloud server create-image` has no `--output` flag (look snapshot up by label after); bash heredoc-built JSON is fragile when values contain control chars (use python3 with control-char stripping). goimports version capture left as a cosmetic TODO — fix is in `provision.sh` now, will land on next bake (autoscaler is unaffected).
- 2026-05-23 — Issues migrated. All 9 open GitHub issues are on Codeberg as `#1`–`#9`. Number map preserved in `scripts/migrate-issues-to-codeberg.py` run output. Source GitHub issues NOT yet closed (deliberate, awaits verification). Discovered Codeberg's new-issue rate limit is layered (5 / 5 min AND 7 / 10 min sliding) — script now handles both.
- 2026-05-23 — Phase 0 essentially complete. Hetzner account K1100508019 confirmed; exposed Forgejo token revoked; Forgejo Actions enabled on the Codeberg repo; Q7.5 resolved (no hosted-tier onboarding needed for self-hosted runners); Codeberg repo exists with `main` pushed. Phase 1 unblocked. Open issue count: 9.
- 2026-05-23 — §1 decisions locked: personal namespace, hard cutover, fresh runners, €20 cap, every-push triggers, x86-only confidence lane.
- 2026-05-23 — plan drafted; Forgejo Actions access requested by Hans.
