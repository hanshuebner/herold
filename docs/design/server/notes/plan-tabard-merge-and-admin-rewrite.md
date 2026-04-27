# Plan: fold tabard into herold, retire the tabard brand, replace HTMX admin UI with a Svelte SPA

Date: 2026-04-27. Author: root agent + hans. Status: approved, awaiting execution in a future session.

This plan supersedes R35 in `docs/design/server/notes/open-questions.md` ("Web UI framework -> HTMX + Go templates + Alpine.js / vanilla JS"). Ratify the supersession by editing R35 in the Phase 1 PR; do not delete it.

## 1. Motivation

The product is a single binary with two web surfaces:

- The end-user mail/chat/calendar suite (tabard), today living in a separate repo at `/Users/hans/tabard` and embedded into herold at build time via `scripts/embed-tabard.sh` -> `internal/tabardspa/dist/`.
- The operator admin UI (`internal/protoui`), today implemented as Go templates + HTMX 1.9 + Alpine 3.13, mounted at `/ui/`.

Three problems with the status quo:

- Two UI toolkits in one product. Look, feel, components, and accessibility behaviour will diverge by default. There is no shared design system between protoui's hand-rolled `ui.css` and tabard's Carbon-inspired Svelte design system.
- Two repos for one binary. Cross-cutting changes (a new admin REST endpoint and the form that posts to it; a new JMAP capability and the suite UI that consumes it) take a coordination dance: ship one repo, bump a SHA, ship the other. Pre-launch, with NG2 ("multi-node never") and a single-binary distribution model, the polyrepo costs buy nothing.
- Two product names. The server is "herold"; the bundled web client is "tabard". One product, one brand makes more sense before launch than two; afterwards the cost of renaming compounds.

Because herold is not yet live, no migration bridge, no backwards-compat layer, and no dual-brand window are required. The HTMX admin UI is replaced wholesale by a Svelte SPA, and the tabard name is retired in the same import that brings the code in. This is a rewrite plus a rename, not a migration.

## 2. Decisions locked

These were settled in conversation 2026-04-27 and are inputs to execution, not open items.

1. **Tabard becomes a component of herold.** The standalone `/Users/hans/tabard` repo is folded into this repo via `git subtree add --prefix=web` (no squash; preserve tabard's commit history). The standalone repo is archived afterward.
2. **Repo layout: `web/` workspace at the repo root.** pnpm workspaces inside `web/`, one Go module at the repo root. Apps under `web/apps/`, shared packages under `web/packages/`. See section 4.
3. **Design docs split: `docs/design/web/` and `docs/design/server/`.** Existing `docs/design/{requirements,architecture,implementation,notes}` keep their current contents (server-side); the directory structure is mirrored under `docs/design/web/{requirements,architecture,implementation,notes}` for tabard-imported docs and admin SPA docs. The `00-scope.md` at `docs/design/00-scope.md` stays at the top level and is updated to reference both subtrees.
4. **One `web-frontend-implementor` agent** owns all of `web/`. No split between admin-UI and suite-UI agents at this stage.
5. **`/Users/hans/tabard/gmail-logger/`** is not imported.
6. **Build-tag opt-out for the frontend embed.** Default builds embed the SPA dists. Backend-only contributors and Go-only test runs use `-tags nofrontend` to compile a stub embed and a placeholder handler. `go build` and `go test ./...` must succeed under both tag states; CI runs both.
7. **Replacement, not migration.** No parallel-run window. Phase 3 is a single PR that mounts the new admin SPA at `/admin/` and deletes `internal/protoui` in the same change. `/ui/*` returns a 308 redirect to `/admin/*` for one release, then the redirect is dropped.
8. **Admin SPA stack inherits tabard's**: Svelte 5 + Vite + pnpm + Bits UI + Carbon-inspired tokens + IBM Plex. History-mode routing under `/admin/*` (server fallback rewrites unknown `/admin/*` paths to `index.html`). REST consumed via existing `internal/protoadmin` `/api/v1/...` surface; no new direct-store backdoor like protoui has today.
9. **Auth model unchanged.** Same HMAC-signed `herold_session` cookie, `[admin]` vs `[user]` scope flags. CSRF for the SPA shifts from form-token double-submit to a header-issued token at session creation; specify in the ADR.
10. **End-user `/settings` panel (REQ-ADM-203) is out of scope** for this plan. It lands later, inside `web/apps/suite`, not in `web/apps/admin`.
11. **Tabard brand is retired completely.** The whole product is "herold". Inside the imported tree, every reference to "tabard" is renamed to either "herold" (when it identifies the product) or to a content-descriptive word -- "suite", "frontend", "web" -- when it describes a part. The npm package namespace becomes `@herold/*`. User-facing brand inside the SPA -- HTML `<title>`, page header, web manifest `name` and `short_name`, favicon alt text -- is exactly "Herold", with no qualifier. The directory `web/apps/suite/` keeps the name "suite" (content-descriptive, not branded). The rename happens inside the Phase 1 PR so `main` never carries the tabard brand. Pre-import git history (commit messages, tag names) keeps the tabard name -- history is not rewritten. See section 4.2 for the rename catalogue.

## 3. Pre-execution inventory

Before the Phase 1 PR is opened, an executor must read and record the following from the tabard repo while it still exists at `/Users/hans/tabard`. None of this is in herold today.

- `tabard/CLAUDE.md` — working agreement; content moves to `web/CLAUDE.md`.
- `tabard/.claude/` (if present) — agent definitions, hooks, settings; merge into herold's agent setup, renaming any tabard-specific agent to `web-frontend-implementor`.
- `tabard/docs/{requirements,architecture,implementation,notes}/` — relocate to `docs/design/web/...` preserving filenames.
- `tabard/package.json`, `tabard/pnpm-workspace.yaml`, `tabard/pnpm-lock.yaml`, `tabard/tsconfig.base.json` — relocate to `web/`.
- `tabard/apps/suite/` — relocate to `web/apps/suite/`.
- `tabard/packages/design-system/` — relocate to `web/packages/design-system/`.
- Any `tabard/scripts/` files referenced by `tabard/package.json` build scripts.
- In-flight branches: confirm with hans that no unmerged tabard branches will be lost by the import.

`tabard/gmail-logger/` and `tabard/node_modules/` are not imported.

The relocation is one mechanical step inside the subtree-import PR. Because subtree merge preserves history, post-import `git log --follow web/apps/suite/...` will reach back into pre-merge tabard commits.

## 4. Target layout

```
herold/
  cmd/                                 (unchanged)
  internal/
    protoui/                           DELETED in Phase 3
    tabardspa/                         DELETED in Phase 1, replaced by webspa
    webspa/                            NEW: serves admin and suite dists
      embed_default.go                 //go:build !nofrontend  -- embeds dist/
      embed_stub.go                    //go:build nofrontend   -- stub FS
      admin.go                         /admin/* handler, SPA fallback
      suite.go                         /* handler on public listener
      doc.go
  docs/
    design/
      00-scope.md                      (edit: reference both subtrees)
      requirements/                    (server-side, unchanged)
      architecture/                    (server-side, unchanged)
      implementation/                  (server-side, unchanged)
      notes/                           (server-side, plus this file)
      server/                          NEW empty marker, optional;
                                       see section 4.1 below
      web/                             NEW
        requirements/                  imported tabard docs + admin SPA reqs
        architecture/                  imported tabard docs + admin SPA arch
        implementation/                tech stack, build, embedding
        notes/                         imported notes, ADRs
  web/                                 NEW workspace root
    CLAUDE.md                          imported from tabard/CLAUDE.md
    package.json
    pnpm-workspace.yaml
    pnpm-lock.yaml
    tsconfig.base.json
    apps/
      admin/                           NEW Svelte app (Phase 2)
      suite/                           imported from tabard/apps/suite
    packages/
      design-system/                   imported from tabard/packages/design-system
  scripts/
    build-web.sh                       NEW; replaces embed-tabard.sh
    embed-tabard.sh                    DELETED in Phase 1
  Makefile                             EDIT
  AGENTS.md                            EDIT (add web-frontend-implementor)
  STANDARDS.md                         EDIT (add Frontend section)
  CLAUDE.md                            EDIT (point at web/CLAUDE.md)
```

### 4.1 Note on `docs/design/server/`

The user requested separate `docs/design/web/` and `docs/design/server/` directories. Two ways to interpret this:

- Option A: keep existing `docs/design/{requirements,architecture,implementation,notes}` where they are; create only `docs/design/web/` for imported content. `docs/design/server/` stays implicit.
- Option B: move the existing server-side docs under `docs/design/server/` to make the split explicit.

Option B touches every cross-link in the codebase (CLAUDE.md, agent prompts, requirements docs that reference each other) and produces a large, mostly-mechanical diff. Option A is non-disruptive and the split is still legible. Recommend Option A; flag the choice for hans at the start of execution.

### 4.2 Tabard brand removal catalogue

Scope of the brand-removal sweep performed in Phase 1 step 4 (see section 6). The executor must hit every category before opening the Phase 1 PR. Where the imported tabard repo has not been read yet, the catalogue says "if present"; the inventory step (section 3) is the moment to confirm.

**Package and workspace identity (web/):**

- `web/package.json` -- `name` field set to `herold-web` (or `@herold/web`). All scripts that reference "tabard" in commands or paths updated.
- `web/pnpm-workspace.yaml` -- no string changes likely, but verify.
- All `web/apps/*/package.json` and `web/packages/*/package.json` -- `name` fields rewritten under the `@herold/*` namespace: `@herold/suite`, `@herold/design-system`, and (Phase 2) `@herold/admin`.
- All cross-package `dependencies` / `devDependencies` referring to the old namespace updated in lockstep.
- `web/pnpm-lock.yaml` -- regenerate after the namespace rewrite (`pnpm install` from `web/`).

**Source code (web/apps/, web/packages/):**

- TypeScript / Svelte source: rename all imports, identifiers, type names, file names containing `tabard` / `Tabard` / `TABARD`. Exception: leave third-party identifiers alone (none expected).
- CSS / SCSS: rename class names, custom-property names, and selectors prefixed `tabard-` or named with the brand. The design-system token names are likely brand-neutral (Carbon-inspired); confirm during inventory.
- HTML / Svelte component template literals: rename visible "Tabard" strings to "Herold". Includes default headings, empty-state copy, and aria-labels.
- Asset filenames containing `tabard` (e.g. `tabard-logo.svg`) -- rename. Asset *contents* (logo artwork, brand wordmark images) are design work, not in scope for the rename pass; flag separately.

**HTML metadata and PWA manifest (web/apps/suite/, web/apps/admin/):**

- `<title>` defaults: "Herold".
- `<meta name="application-name">`, `<meta name="apple-mobile-web-app-title">` if present.
- Web app manifest (`manifest.webmanifest` / `site.webmanifest` / similar): `name`, `short_name`, `description`.
- `index.html` body content if it carries inline brand text.

**Documentation (docs/design/web/, web/CLAUDE.md, web/README.md if any):**

- All prose references to "tabard" rewritten. Where the doc describes the historical fact of the import ("imported from the tabard repo on 2026-MM-DD"), keep the historical name; where it names the *current* product, use "herold".
- The Phase 0 ADR explicitly records the rename so future readers understand commit-history mentions of "tabard".

**Server-side code (Go) and configuration:**

- `internal/tabardspa/` -- already deleted in Phase 1 step 5; replaced by `internal/webspa/`. Confirms no remaining `tabardspa` import path.
- Comments, log messages, error strings: grep `tabard` across `internal/`, `cmd/`, `plugins/`, `test/` and rewrite. Expected hits: any comment in `internal/webspa/` carried over from `internal/tabardspa/`, anything in `internal/protoadmin` or boot wiring that mentions tabard by name, any operator-facing log line.
- TOML config keys (`internal/sysconfig`, `docs/design/server/requirements/09-operations.md`): if any `[server.tabard]` or `tabard_*` key exists, rename to `[server.suite]` / `suite_*` (or fold into `[server.ui]` if there is no parameter divergence). Keep configuration backward compatibility off -- pre-launch.
- Metric names (`herold_tabard_*` if any) renamed; Prometheus naming uses a generic noun (e.g. `herold_suite_sessions_active`).

**Build and tooling:**

- `scripts/embed-tabard.sh` -- already deleted in Phase 1 step 6; replaced by `scripts/build-web.sh`.
- `Makefile` targets: ensure no surviving `tabard` target name.
- `.github/workflows/*.yml`: any job, step, or artifact name mentioning tabard renamed.
- `deploy/` (Dockerfiles, debian/, rpm/, k8s/): any image name, label, environment variable, service name, or comment mentioning tabard renamed.
- `.pre-commit-config.yaml`: any hook id or path filter mentioning tabard renamed.

**Repo-root surface:**

- `README.md` -- rewrite the section that introduces the bundled web client. The product is one thing, named herold.
- `CLAUDE.md` (root) -- ensure no surviving `tabard` mention; the pointer to `web/CLAUDE.md` does not need to use the brand name.
- `AGENTS.md` -- new agent is `web-frontend-implementor`, never `tabard-implementor`.
- `STANDARDS.md` -- if any text references "tabard" (unlikely), rewrite.
- `LICENSE`, `CHANGELOG`, etc. -- inspect for brand references.

**Out of scope (deliberately not renamed):**

- Pre-import git commit messages and tag names. History is preserved verbatim by the subtree merge; rewriting it would destroy the rationale for not squashing.
- This plan file's filename (`plan-tabard-merge-and-admin-rewrite.md`) and the matching ADR filename. Both describe the historical event of folding tabard in; the names remain accurate. The plan's *content* uses "tabard" only to refer to the pre-import source.
- Backups, archived branches, and the standalone `/Users/hans/tabard` repo (which is being archived).

**Execution discipline:**

- After the rename pass, run `git grep -i tabard` and account for every hit. Either it is in the deliberately-out-of-scope list above, or it is a missed rename and must be fixed before the PR opens.
- The `web-frontend-implementor` agent owns the rename sweep inside `web/`; the root agent owns it outside.

## 5. Build, embed, and dev workflow contracts

- `make build-server` runs `go build ./cmd/herold` with `-tags nofrontend`. No pnpm dependency. Produces a binary that serves a placeholder at `/` and `/admin/`. Used by Go-only contributors and by the `go` CI lane.
- `make build-web` runs `pnpm -C web install --frozen-lockfile` then `pnpm -C web build`. Output dirs: `web/apps/admin/dist/`, `web/apps/suite/dist/`. Then `scripts/build-web.sh` copies both into `internal/webspa/dist/admin/` and `internal/webspa/dist/suite/`.
- `make build` runs `make build-web && make build-server` without `-tags nofrontend`. Produces the full single-binary release artifact.
- `make test-server` runs `go test -race ./...` with `-tags nofrontend`.
- `make test-web` runs `pnpm -C web test` (vitest) and `pnpm -C web test:e2e` (playwright, against a stub server).
- `make test` runs both, plus the integration smoke (admin-user agent flow against the full binary).
- Dev loop, web-only: `pnpm -C web dev --filter @herold/admin` (or `--filter @herold/suite`) runs Vite with a proxy to a locally running herold for `/api/v1/*`, `/jmap/*`, `/chat/ws`, EventSource paths.
- Dev loop, server-only: `make build-server && ./bin/herold ...`. Hitting `/admin/` with no built dist returns HTTP 503 and a one-line "frontend not built; run `make build-web`" body.
- CI: three lanes -- `go` (tags=nofrontend, both DB backends), `web` (lint, vitest, playwright with stub), `integration` (full build, admin-user smoke, both DB backends). First two run in parallel; the third gates merge.
- Pre-commit hooks: file-scoped. gofmt/golangci-lint on `*.go`; eslint/prettier on `web/**`. No cross-talk.

## 6. Phased execution

Each phase is one PR. PRs do not overlap; later phases assume earlier phases have merged. Every PR is reviewed by `reviewer`. Phase 3 (auth-relevant CSRF model change, deletion of session-handling code) also goes to `security-reviewer`.

### Phase 0 -- ADR and inventory (small PR)

Goal: lock the decision in the repo before any code moves.

- Add `docs/design/web/notes/adr-0001-merge-tabard-and-rewrite-admin-ui.md` capturing the decisions in section 2, the rejected alternative (parallel migration), and the supersession of R35.
- Edit `docs/design/server/notes/open-questions.md` R35 to read "Web UI framework -> Svelte 5 SPA, see ADR-0001 in `docs/design/web/notes/`. The HTMX implementation in `internal/protoui` is replaced; this question is closed." Do not delete the entry.
- Edit `docs/design/server/requirements/08-admin-and-management.md` REQ-ADM-204 to name the stack instead of saying "TBD". REQ-ADM-200 ("at /admin") and REQ-ADM-201 ("UI is a SPA that consumes the REST API") already match the target and need no edit.
- This plan file (`docs/design/server/notes/plan-tabard-merge-and-admin-rewrite.md`) stays where it is.

Exit criteria: ADR merged. R35 superseded. REQ-ADM-204 names Svelte. No code changes.

### Phase 1 -- subtree import and repo bones (medium PR)

Goal: tabard lives inside herold; existing tabard suite SPA continues to be served and embedded. Admin UI (`internal/protoui`) is untouched and still serves at `/ui/`. No behavioural change for operators.

Steps:

1. `git subtree add --prefix=web https://github.com/<tabard-remote> <tabard-main-sha>` (or local path equivalent). Confirms history is preserved.
2. Relocate inside the imported subtree:
   - `web/CLAUDE.md` (was `tabard/CLAUDE.md`).
   - `web/.claude/` if any tabard agent setup existed; merge into herold's agent setup; rename any tabard-specific agent to `web-frontend-implementor`.
   - `tabard/docs/...` -> `docs/design/web/...` (subtree import places it at `web/docs/...`; move it).
3. Delete `tabard/gmail-logger/` from the imported tree.
4. **Brand-removal sweep.** Apply the rename catalogue in section 4.2 across `web/`, `docs/design/web/`, and any in-tree references to "tabard" anywhere else in the repo. End state: `git grep -i tabard` returns only the explicitly out-of-scope items listed in section 4.2 (this plan's filename, its ADR's filename, pre-import git history). Regenerate `web/pnpm-lock.yaml` after the namespace rewrite.
5. Update `web/package.json` script names if they conflict with herold's `Makefile` targets. Confirm `pnpm install` from `web/` succeeds.
6. Replace `internal/tabardspa/` with `internal/webspa/`. Initial implementation serves only the suite (admin app does not exist yet). Wire the build-tag split (`embed_default.go` / `embed_stub.go`).
7. Replace `scripts/embed-tabard.sh` with `scripts/build-web.sh`. Drop the old script.
8. Update `Makefile`: add `build-server`, `build-web`, `build`, `test-server`, `test-web`, `test`. Existing top-level `make` and `make test` targets become aliases or are redefined; preserve admin-user smoke compatibility.
9. Update CI workflow under `.github/workflows/` to add the three lanes.
10. Update `CLAUDE.md` (root): add a one-liner "If you are touching `web/`, also read `web/CLAUDE.md` and the docs under `docs/design/web/`."
11. Update `STANDARDS.md`: add a short Frontend section recording (a) Svelte 5 + Vite + pnpm as the locked stack, (b) the build-tag opt-out invariant, (c) "no runtime JS toolchain dependency; all assets embedded via `embed.FS`."
12. Update `AGENTS.md`: add `web-frontend-implementor` (owns `web/apps/*` and `web/packages/*`); note that `http-api-implementor` remains the Go-side counterpart for `/api/v1/...` consumed by the admin SPA.
13. Update `docs/design/00-scope.md` to reference `docs/design/web/` as the home for frontend design docs.
14. Archive note: open a follow-up issue or note to archive the standalone `/Users/hans/tabard` repo with a README pointer to herold. (Not required to merge Phase 1, but should not be forgotten.)

Exit criteria: clean `make build` produces a binary that serves the imported suite at `/` (now under the herold brand) and the existing HTMX admin UI at `/ui/`. `make build-server` (with `nofrontend`) produces a binary where `/` returns the placeholder. CI is green on all three lanes. `git log --follow web/apps/suite/<some-file>` reaches into pre-merge tabard history. `git grep -i tabard` returns only the explicitly out-of-scope items from section 4.2.

### Phase 2 -- build the admin SPA to parity (one large PR or a small series)

Goal: a Svelte admin app at `web/apps/admin/` that reaches feature parity with `internal/protoui`. Not yet mounted; not yet replacing protoui.

Sub-steps. Each can be its own PR or all bundled, at hans's preference.

1. Scaffold `web/apps/admin/` (package.json, vite.config.ts, tsconfig, src/, public/). Wire it into `web/pnpm-workspace.yaml`. Vite history-mode base path = `/admin/`.
2. Wire the design system: `web/apps/admin/` depends on `web/packages/design-system/` via workspace protocol.
3. Audit `internal/protoadmin` for REST coverage gaps. The current `internal/protoui` reaches into the store/directory directly for several flows (notably session login at `internal/protoui/auth_handlers.go`, OIDC begin/callback at `internal/protoui/oidc_handlers.go`, password change, 2FA enroll, API key CRUD). Each path needed by the SPA must exist as `/api/v1/...`. Land any gap-filler endpoints in `internal/protoadmin` *before* the SPA needs them. This is a Go-side prerequisite owned by `http-api-implementor`.
4. Specify and implement the SPA's CSRF model: header token issued by the session-create endpoint, sent on every mutating request as `X-CSRF-Token`. Document in the ADR; review by `security-reviewer`.
5. Build pages in dependency order:
   1. Login + session establishment.
   2. Dashboard (read-only; exercises stats endpoints).
   3. Principals list + detail (CRUD; exercises mutate + ETag).
   4. Domains list + detail + alias CRUD.
   5. Queue inspector (list, filter, retry, hold, release, delete).
   6. Audit log (read-only).
   7. Email research.
   8. OIDC link/unlink, 2FA enroll/confirm/disable, API keys, password change.
6. Write a Playwright suite that exercises each page against a stub server. Add fixtures.
7. Add the embed wiring in `internal/webspa/admin.go` but leave it gated behind a config flag (default off) until Phase 3.

Exit criteria: `make build-web` produces both `apps/admin/dist/` and `apps/suite/dist/`. With the admin SPA flag enabled in dev config, `/admin/` serves the new UI and every parity feature works against a real `internal/protoadmin`. The HTMX admin at `/ui/` is still the default and still works. Playwright suite green.

### Phase 3 -- cutover and protoui deletion (medium PR)

Goal: `/admin/` is the only admin UI. `/ui/` is gone or 308-redirects.

Steps:

1. Mount `internal/webspa/admin.go` at `/admin/` on the admin listener unconditionally. Drop the dev-only flag.
2. Replace `internal/protoui` route registration with a 308 redirect handler from `/ui/*` to `/admin/*`. (Keep the redirect for one release window; remove in a follow-up.)
3. Delete `internal/protoui/` in its entirety: handlers, templates, vendored htmx/alpine, ui.css, tests.
4. Update operator-facing docs:
   - `docs/operators/...` (or wherever quickstart admin URLs live) -> `/admin/`.
   - The admin-user agent's smoke-run script must navigate `/admin/...` not `/ui/...`.
   - `README.md` if it mentions `/ui/`.
5. Update audit-log fixtures or test data that reference protoui handler names.

Exit criteria: `internal/protoui` is gone. `/admin/` serves the Svelte SPA. The admin-user smoke-test passes against the full binary. `reviewer` and `security-reviewer` sign off.

### Phase 4 (deferred) -- end-user `/settings` panel

Out of scope for this plan. Tracked as a follow-up against REQ-ADM-203. Lands inside `web/apps/suite` as a new route, not in `web/apps/admin`.

## 7. Files this plan will touch

For executor visibility. Paths are post-import where relevant.

Created:
- `docs/design/web/notes/adr-0001-merge-tabard-and-rewrite-admin-ui.md`
- `docs/design/web/{requirements,architecture,implementation,notes}/*` (imported)
- `web/...` (imported, plus `web/apps/admin/` in Phase 2)
- `internal/webspa/{embed_default.go,embed_stub.go,admin.go,suite.go,doc.go}`
- `scripts/build-web.sh`

Edited:
- `docs/design/server/notes/open-questions.md` (supersede R35)
- `docs/design/server/requirements/08-admin-and-management.md` (REQ-ADM-204 names stack)
- `docs/design/00-scope.md` (reference docs/design/web/)
- `Makefile`
- `.github/workflows/*.yml`
- `CLAUDE.md` (root)
- `STANDARDS.md` (add Frontend section)
- `AGENTS.md` (add web-frontend-implementor)
- `README.md` (URL of admin UI; tabard brand removed)
- Every imported file under `web/` and `docs/design/web/` touched by the brand-removal sweep (section 4.2). Expected count: dozens to a few hundred lines, mostly mechanical.

Deleted:
- `internal/protoui/` (Phase 3)
- `internal/tabardspa/` (Phase 1; replaced by `internal/webspa/`)
- `scripts/embed-tabard.sh` (Phase 1; replaced by `scripts/build-web.sh`)

## 8. Risks and explicit non-decisions

- **`internal/protoadmin` REST gaps.** The size of these gaps is unknown until Phase 2 step 3 audits them. If the gap is large, Phase 2 grows. Treat the audit as a true prerequisite, not a side task.
- **Subtree-merge collisions.** If tabard's repo has a `README.md` or `LICENSE` at root, the subtree import will land them at `web/README.md` / `web/LICENSE`, not at the herold root. Verify before merging Phase 1.
- **pnpm-lock churn.** Imported lockfile may need a regeneration pass to align with whatever Node/pnpm version herold's CI standardises on. Allow time for one re-resolve.
- **`embed.FS` size.** Tabard suite + admin SPA bundles inflate the binary. Measure in Phase 1 (suite only) and Phase 2 (both). If size becomes a concern, the design-system Plex font subset is the first lever to pull; not a phase-blocking issue.
- **Build-tag drift.** Both `embed_default.go` and `embed_stub.go` must export the same symbol surface. Add a small `webspa_build_tag_test.go` that asserts both compile and that the stub returns sane status codes.
- **CSRF model change.** Switching from form-token to header-token is the only auth-shaped change here. `security-reviewer` must review the Phase 2 PR (or a dedicated CSRF-model PR carved out of Phase 2).
- **History preservation under `--prefix=web`.** Verify after import: `git log --follow web/apps/suite/<file>` should reach pre-import tabard commits. If for any reason it does not, the choice between living with truncated history and re-doing the import as `--squash` becomes an open question; default to living with it rather than redoing.
- **What if tabard wants a second life as a standalone JMAP client?** `git subtree split --prefix=web` extracts cleanly. Plan does not need to anticipate this; flag is here for future readers.
- **Brand-rename completeness.** The `git grep -i tabard` gate (section 4.2) is the primary discipline, but it cannot catch references inside binary assets (logo images, icon files), inside compiled artefacts (which we do not commit), or inside string fragments split across lines. A second sweep after Phase 1 -- a manual smoke against the running suite (`/`, settings, about page) and a visual check of any imported logo / favicon -- closes the residual gap. Logo artwork itself is design work, not part of this rename pass; if the imported tabard logo is recognisably tabard-shaped, file a follow-up rather than blocking Phase 1 on artwork.

## 9. What this plan deliberately does not do

- It does not implement REQ-ADM-203 (`/settings` self-service panel for end users).
- It does not change auth scoping, principal model, or session lifetime.
- It does not change the admin REST contract beyond filling gaps the SPA needs.
- It does not change the JMAP, IMAP, SMTP, or any other wire-protocol surface.
- It does not introduce a new plugin type or change the plugin host model.
- It does not touch the storage layer, the queue, or the ACME client.
- It does not rewrite git history. Pre-import tabard commits, tags, and authorship remain as they are.
- It does not redesign brand artwork. The rename is a string-and-identifier rewrite; logo, wordmark, and icon design are out of scope and tracked as separate follow-ups.

## 10. Handoff checklist for the executing session

Read in order:

1. This file. Pay particular attention to section 2 (decisions, including decision 11 on brand removal) and section 4.2 (rename catalogue).
2. `docs/design/server/notes/open-questions.md` -- confirm R35 still says HTMX (which means Phase 0 has not run yet) or now references the ADR (which means it has).
3. `docs/design/server/requirements/08-admin-and-management.md` lines 78-95 -- confirm REQ-ADM-200/201/204 still match section 2 of this plan.
4. `internal/protoui/` directory listing -- confirm scope estimate (8 pages, ~36 routes) still holds.
5. `internal/tabardspa/` and `scripts/embed-tabard.sh` -- confirm they still exist and how they wire together.
6. `/Users/hans/tabard/` (external) -- confirm it still exists and is the source of truth for the import. If it is gone, recover from git history before proceeding.

Then start with Phase 0.

Before merging Phase 1, the gate is: `git grep -i tabard` returns only the deliberately-out-of-scope hits enumerated in section 4.2 (this plan's filename, the matching ADR filename, pre-import commit history). Anything else is a missed rename.
