# IMAP client mode (#25) — remaining work

Status snapshot at handoff. Waves A/C/D/F are on `main` and verified on both
SQLite and Postgres (migrations 0062–0065). Wave B is on
`feat/go-imap-xgmlabels`; Wave E is on `wip/wave-e-imap-ui`. The design /
decision trail is in issue #25 (see its handoff comment) and
`docs/design/server/notes/imap-import-replan-2026-06-27.md`.

## Cold start (new machine / fresh session with no chat memory)

Everything needed is in git + issue #25 — no prior conversation required.

1. **Read, in order:** this file → issue #25 (esp. the "Handoff" comment) →
   `docs/design/server/notes/imap-import-replan-2026-06-27.md` (wave breakdown
   + validation findings) → the requirements it cites
   (`docs/design/server/requirements/19-imap-import.md`, `02-identity-and-auth.md`,
   `26-domain-cutover.md`). Plus `CLAUDE.md` + `STANDARDS.md` for the rules.
2. **Branch map:** `main` = waves A/C/D/F (done). `feat/go-imap-xgmlabels` =
   wave B — the fork/PR script (`scripts/upstream-go-imap-pr.sh`) and the patch
   (`docs/dev/go-imap-xgmlabels-*.patch/.md`) live here. `wip/wave-e-imap-ui` =
   wave E (the web UI). This doc is mirrored onto all three branches; the
   script + patch are only on `feat/go-imap-xgmlabels`.
3. **Toolchain:** Go per `go.mod` (1.26.x). For both-backend store tests, have
   Postgres reachable and set `HEROLD_PG_DSN` (e.g.
   `postgres://USER:PW@localhost:5432/DB?sslmode=disable`); SQLite needs
   nothing. Wave E needs Node + pnpm (via `corepack`) and **the puppeteer MCP
   server connected** (hard requirement; without it Wave E cannot land).
4. **Verify before landing:** `go build ./...`;
   `go test ./internal/imapimport/ -race -count=1`;
   `go test ./internal/storesqlite/`; `HEROLD_PG_DSN=... go test ./internal/storepg/`;
   on `feat/go-imap-xgmlabels` also `go test ./internal/protoimap/...` (the
   in-tree go-imap `replace` redirects the server too). Land by fast-forward
   only: `git push origin <branch>:main` — no PRs (project rule).
5. **Push note:** on the previous machine the ssh-agent was dead, so pushes
   went over an HTTPS token; on a normal dev box plain `git push` over SSH
   works — ignore the token workaround mentioned in the issue.

## Wave B — true Gmail X-GM-LABELS placement (this branch)

Code is done and locally verified (build, `imapimport -race`, `protoimap`
under the replace, both store backends, fuzz). Branch CI (dispatched) was
green on lint / web / web-e2e / IMAP-SMTP-Sieve conformance / smoke / Postgres
tests. Remaining:

1. **Open the upstream go-imap PR.** Run `scripts/upstream-go-imap-pr.sh`
   (wraps fork → `git am` → push → `gh pr create`). The patch and PR body are
   `docs/dev/go-imap-xgmlabels-upstream.patch` and `…-PR.md`.
2. **Pick the herold "meanwhile" mechanism** (until the PR merges + releases):
   - **(a) Keep the in-tree vendor** (`third_party/go-imap` + the relative
     `replace` in `go.mod`). Self-contained, CI-safe. Just land this branch.
   - **(b) Switch to the GitHub fork** via a module-version `replace`
     (`github.com/emersion/go-imap/v2 => github.com/<you>/go-imap/v2 <pseudo>`).
     Needs a *second* fork branch whose `go.mod` module path is renamed to
     `github.com/<you>/go-imap/v2` (the clean PR branch keeps the original
     path). Then `go mod edit -replace … && go get …`, drop `third_party/`.
3. **Confirm `pre-commit` is green.** The branch's earlier `pre-commit
   (run --all-files)` failure was the committed `.patch` tripping the
   `trailing-whitespace` hook (unified-diff blank lines are a single space).
   Fixed here by exempting `docs/dev/*.patch` in `.pre-commit-config.yaml` —
   re-dispatch CI on the branch to confirm.
4. **Re-check the `jmap conformance` job.** It failed on the branch run but is
   an external suite (`jmapio/jmap-test-suite`) that does not touch go-imap;
   re-run to confirm it is a flake (it passes on `main`).
5. **Land:** fast-forward `feat/go-imap-xgmlabels` → `main` once CI is green
   (`git push origin feat/go-imap-xgmlabels:main`). No PR (project rule).

Test caveat: the Gmail X-GM-LABELS end-to-end path is unit/fake-conn tested
(the in-process `imapmemserver` does not emit X-GM-LABELS); the wire parse is
covered in the fork's `fetch_gmail_test.go` and through herold's `prodConn`
against a scripted server.

## Wave E — per-identity SMTP+IMAP web UI (`wip/wave-e-imap-ui`)

Implemented per REQ-SET-IMAPIMP-01..05 / REQ-SET-IDENT-10/12; passes 1632
vitest + svelte-check. **Blocked**: not verified in a live browser because the
puppeteer MCP server was absent, and `web/CLAUDE.md` forbids shipping UI on
test-only verification. Remaining:

1. Restore the puppeteer MCP server in the session.
2. `scripts/dev-instance.sh start`; sign in as `alice@example.local` /
   `testpass123...`; verify the Identity edit dialog: the mandatory Sending
   (SMTP) section gates save until probe-verified; the optional Receiving
   (IMAP import) section appears only for external-domain identities; the
   remove/keep-or-delete-imported-mail prompt shows the message count.
   Screenshot each.
3. Land `wip/wave-e-imap-ui` → `main`.

(The branch also carries two infra fixes: `dev-instance.sh` `xxd`→`od`
fallback and `pnpm-workspace.yaml` `allowBuilds.esbuild`.)

## Known follow-ups surfaced while implementing (non-blocking)

- **Wave D / REQ-IMAP-IMP-101:** account rename does not yet rename the
  provenance label mailbox (label is created + cached but not renamed on
  update).
- **Wave D / REQ-IMAP-IMP-103 purge edge:** a message delivered *natively*
  into the same mailbox the import also targets (e.g. both INBOX) is
  indistinguishable from import-only and would be destroyed under PURGE —
  there is no native-delivery marker. Dedup-safety against *other import
  accounts* and manual labels/moves is enforced + tested. For the migration
  use case this is benign (the trial domain is not yet hosted, so no native
  delivery to it).
- **Wave A / D2-D3:** the CONDSTORE `CHANGEDSINCE` down-sync branch is
  reachable only against a real CONDSTORE-advertising upstream; tests exercise
  the non-CONDSTORE fallback (the memserver advertises neither). Correct on
  every server; incremental on CONDSTORE ones.
- **Wave C / REQ-IMAP-IMP-95 reopen:** a `migrated`→`enabled` reopen takes
  effect on the next worker (re)launch; the pool has no live worker re-spawn
  (pre-existing boot-reconnect limitation, REQ-74).

## Separate feature (not #25)

Domain cutover (provider migration) is specified in
`docs/design/server/requirements/26-domain-cutover.md` + the admin manual
chapter `docs/manual/admin/domain-cutover.mdoc`, and tracked as issue #62
(labelled `deferred`). It depends on the #25 waves above.
