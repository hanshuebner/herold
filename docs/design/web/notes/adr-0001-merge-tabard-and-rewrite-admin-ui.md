# ADR-0001: Fold tabard into herold; replace HTMX admin UI with a Svelte SPA; retire the tabard brand

- Status: Accepted
- Date: 2026-04-27
- Supersedes: R35 in `docs/design/notes/open-questions.md`
- Source plan: `docs/design/notes/plan-tabard-merge-and-admin-rewrite.md`
- Reviewers: hans (operator)

## Context

The product is a single self-hosted communications server. It exposes
two web surfaces today:

- The end-user mail/calendar/contacts/chat suite lived in a separate
  repository (`/Users/hans/tabard`, GitHub `hanshuebner/tabard`),
  built as a Svelte 5 + Vite SPA, and embedded into herold at build
  time via `scripts/embed-tabard.sh` -> `internal/tabardspa/dist/`.
- The operator admin UI lives in `internal/protoui` and is
  implemented as Go templates plus HTMX 1.9 plus Alpine 3.13, mounted
  at `/ui/`.

Three consequences of this status quo are unsatisfactory pre-launch:

1. **Two UI toolkits in one product.** Look, feel, components, and
   accessibility behaviour will diverge by default. There is no shared
   design system between protoui's hand-rolled `ui.css` and tabard's
   Carbon-inspired Svelte design system.
2. **Two repositories for one binary.** Cross-cutting changes (a new
   admin REST endpoint and the form that posts to it; a new JMAP
   capability and the suite UI that consumes it) require a multi-step
   coordination dance: ship one repo, bump a SHA, ship the other.
   With non-goal NG2 ("multi-node never") and a single-binary
   distribution model, the polyrepo costs buy nothing.
3. **Two product names.** "herold" for the server, "tabard" for the
   bundled web client. One product, one brand makes more sense before
   launch than two; afterwards the cost of renaming compounds.

Because herold is not yet released, no migration bridge, no
backwards-compat layer, and no dual-brand window are required.

## Decision

The full set of locked decisions lives in section 2 of the source
plan. The headline:

1. **Tabard becomes a component of herold.** The standalone
   `hanshuebner/tabard` repo is folded into herold via
   `git subtree add --prefix=web` (no squash; preserve history). The
   standalone repo is archived afterwards.
2. **Repo layout: `web/` workspace at the repo root.** pnpm
   workspaces inside `web/`. Apps under `web/apps/`, shared packages
   under `web/packages/`. One Go module at the repo root unchanged.
3. **Design docs split: `docs/design/server/` and `docs/design/web/`.**
   The existing server-side docs at
   `docs/design/{requirements, architecture, implementation, notes}`
   move into `docs/design/server/` as part of Phase 1; the imported
   tabard docs land at `docs/design/web/{requirements, architecture,
   implementation, notes}`. `docs/design/00-scope.md` stays at the
   top level and references both subtrees. (Option B in plan
   section 4.1 -- chosen 2026-04-27 by hans over the additive Option
   A. Trades a one-off mechanical cross-link diff for a permanently
   explicit split.)
4. **One `web-frontend-implementor` agent** owns all of `web/`.
5. **`/Users/hans/tabard/gmail-logger/` is not imported.**
6. **Build-tag opt-out for the frontend embed.** Default builds embed
   the SPA dists. Backend-only contributors and Go-only test runs use
   `-tags nofrontend` to compile a stub embed and a placeholder
   handler. `go build` and `go test ./...` must succeed under both
   tag states; CI runs both.
7. **Replacement, not migration.** No parallel-run window between
   protoui and the new admin SPA. Phase 3 is one PR that mounts the
   new admin SPA at `/admin/` and deletes `internal/protoui` in the
   same change. `/ui/*` returns a 308 redirect to `/admin/*` for one
   release, then the redirect is dropped.
8. **Admin SPA stack inherits tabard's:** Svelte 5 + Vite + pnpm +
   Bits UI + Carbon-inspired tokens + IBM Plex. History-mode routing
   under `/admin/*` (server fallback rewrites unknown `/admin/*`
   paths to `index.html`). REST consumed via the existing
   `internal/protoadmin` `/api/v1/...` surface; no new direct-store
   backdoor like protoui has today.
9. **Auth model unchanged.** Same HMAC-signed `herold_session`
   cookie, `[admin]` vs `[user]` scope flags. CSRF for the SPA shifts
   from form-token double-submit to a header-issued token at session
   creation; specified separately in the Phase 2 PR and reviewed by
   `security-reviewer`.
10. **End-user `/settings` panel (REQ-ADM-203) is out of scope** for
    this rewrite. Lands later, inside `web/apps/suite`, not in
    `web/apps/admin`.
11. **Tabard brand is retired completely.** The whole product is
    "herold". Inside the imported tree, every reference to "tabard"
    is renamed to either "herold" (when it identifies the product) or
    to a content-descriptive word -- "suite", "frontend", "web" --
    when it describes a part. The npm package namespace becomes
    `@herold/*`. User-facing brand inside the SPA is exactly
    "Herold". The directory `web/apps/suite/` keeps the name "suite"
    (content-descriptive, not branded). The rename happens inside
    the Phase 1 PR so `main` never carries the tabard brand.
    Pre-import git history (commit messages, tag names) keeps the
    tabard name -- history is not rewritten. The plan file's
    filename and this ADR's filename also keep the historical name
    deliberately; both describe the import event.

## Alternatives considered

- **Parallel migration / dual-run window.** Keep the HTMX admin UI
  alive while the Svelte admin lands at a different URL, with operator
  opt-in. Rejected because (a) herold is pre-launch, no operator base
  to migrate, and (b) maintaining two admin UIs doubles the audit
  burden for every protoadmin REST contract change.
- **Dual-brand window.** Keep "tabard" as the bundled-web-client
  brand for one release, then rename. Rejected for the same
  pre-launch reason: there is no installed base whose mental model
  needs to be respected, and brand transitions are cheaper to do
  before users exist than after.
- **Keep tabard out-of-tree, fix only the admin UI.** Rewrite
  `internal/protoui` in Svelte against `internal/protoadmin` while
  tabard stays at `/Users/hans/tabard`. Rejected because (a) it
  preserves the polyrepo coordination cost forever and (b) it
  preserves two UI toolkits forever.
- **Fold tabard in but keep HTMX admin.** Rejected because the
  consistency / shared-design-system argument disappears; importing
  tabard without consolidating the admin surface buys only half the
  value.
- **Squash the tabard import.** Rejected; preserves authorship, lets
  `git log --follow web/...` reach pre-import history, costs nothing
  at import time. The cost would be paid only if we later wanted to
  excise the import, which is itself reversible via
  `git subtree split`.

## Consequences

Positive:

- One product, one brand, one repo, one CI lane spectrum (Go +
  frontend + integration), one design system.
- Cross-cutting changes that previously required two repos now ship
  in one PR.
- The `web-frontend-implementor` agent has a single coherent surface
  to own.
- Frontend-irrelevant backend work stays cheap via the
  `-tags nofrontend` opt-out: no pnpm dependency for Go-only
  contributors, no node toolchain in the Go CI lane.

Negative:

- Single-PR rewrite of the admin UI is a chunkier piece of work than
  an incremental migration. Mitigation: Phase 2 is broken into
  page-sized sub-steps that can each ship independently if hans
  prefers a stream of small PRs to a single large one.
- The binary size grows by the admin SPA bundle (in addition to the
  suite bundle which was already embedded). Measured in the Phase 1
  exit-criteria check; the design-system Plex font subset is the
  first lever to pull if size becomes an issue.
- `internal/protoadmin` REST gaps are unknown until the Phase 2 step
  3 audit. The audit is a true prerequisite, not a side task.
- CSRF model change (form-token double-submit -> header-issued token)
  is a security-shaped change that requires `security-reviewer` sign
  off on the Phase 2 PR (or a dedicated CSRF-model PR carved out of
  Phase 2).

## Phasing summary

Each phase is one PR. Phases do not overlap. Every PR is reviewed by
`reviewer`; Phase 3 also goes to `security-reviewer`.

- **Phase 0** (this ADR + R35 supersession + REQ-ADM-204 update). No
  code.
- **Phase 1** -- subtree import, repo bones, brand-removal sweep,
  `internal/webspa/` replacing `internal/tabardspa/`,
  `scripts/build-web.sh` replacing `scripts/embed-tabard.sh`. Behaviour
  unchanged for operators: HTMX admin UI still serves at `/ui/`, suite
  still serves at `/`.
- **Phase 2** -- build the admin Svelte app at `web/apps/admin/` to
  parity with `internal/protoui`. Not yet mounted; not yet replacing
  protoui. Sub-step 3 audits `internal/protoadmin` REST coverage
  gaps and lands the gap-filler endpoints before the SPA needs them.
- **Phase 3** -- cutover. `/admin/` mounts the Svelte SPA, `/ui/*`
  308-redirects to `/admin/*` for one release, `internal/protoui` is
  deleted.

The plan's section 6 carries the full step-by-step breakdown of each
phase, including the
"`git grep -i tabard` returns only deliberately-out-of-scope hits"
gate that precedes Phase 1 merge.

## Supersession of R35

R35 in `docs/design/notes/open-questions.md` previously read:

> **R35.** Web UI framework -> HTMX + Go templates + Alpine.js /
> vanilla JS

This ADR closes that decision in favour of Svelte 5 + Vite + pnpm.
The R35 entry is updated in the same PR that lands this ADR (do not
delete it; the resolved log preserves history).

## REQ-ADM-204 update

REQ-ADM-204 in `docs/design/requirements/08-admin-and-management.md`
previously said the framework was TBD with a preference for "Svelte,
SolidJS, or plain JS over React-SPA default". This ADR resolves it
to Svelte 5 + Vite + pnpm. REQ-ADM-204 is updated in the same PR.

## Risks for the executing session to track

The plan's section 8 enumerates risks. The most consequential is the
size of the `internal/protoadmin` REST gap discovered in the Phase 2
step 3 audit; the audit is mandatory before the admin SPA wires up
each page.

The Option B doc move (decision 3) is mostly-mechanical but touches
every cross-link to `docs/design/...` in CLAUDE.md, agent prompts,
requirements docs, and code REQ-ID citations. The Phase 1 PR carries
that diff in a dedicated commit so reviewers can read it
independently from the subtree import and the brand-removal sweep.
