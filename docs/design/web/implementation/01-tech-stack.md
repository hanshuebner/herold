# 01 — Tech stack

## Language

**TypeScript, strict mode.** No looseness on `any`; opt-out is per-file and explicit. Source targets the supported browsers (`../requirements/13-nonfunctional.md` REQ-BR) — no IE, no transpilation past ES2022.

## Framework: Svelte 5

**Decision: Svelte 5** (with runes — `$state`, `$derived`, `$effect`).

Why, given the constraints in `../00-scope.md` and `../requirements/`:

- Smallest runtime of the mainstream choices (~10–15 KB gzipped). Leaves the 200 KB budget for application code, ProseMirror, DOMPurify, and the virtualised-list helper.
- Fine-grained reactivity maps cleanly onto the normalised cache pattern in `../architecture/01-system-overview.md`: each `(jmap-type, id)` cache cell is a `$state`, list views are `$derived`, optimistic writes are cell mutations that revert on failure. No reducer ceremony.
- Doesn't fight a `document`-level keydown listener. The keyboard engine in `../architecture/05-keyboard-engine.md` owns global key dispatch outright; React's synthetic-event system would make that subtly harder.
- The `srcdoc` iframe-per-message pattern in `../architecture/04-rendering.md` is just a host element with an attribute; no framework wrapping required.

**Plain Svelte + Vite, not SvelteKit.** SvelteKit's value is SSR / edge / file-system routing — none of which tabard needs (`../00-scope.md` NG2 already excludes service workers and offline; SSR for an authenticated client serves no purpose). Static SPA build is what we ship.

## Build: Vite

esbuild + Rollup under the hood. ESM output, no legacy targets, single bundle in v1 (code-splitting deferred per `04-simplifications-and-cuts.md`).

## Package manager: pnpm

**Decision: pnpm** with workspaces (resolved Q13).

The eventual monorepo layout is `apps/mail`, `apps/calendar`, `apps/contacts` plus shared `packages/design-system`, `packages/jmap-client`, `packages/jmap-types`. pnpm's content-addressed `node_modules` and strict workspace resolution suit the suite: shared deps are installed once; each app's lockfile correctness is enforced; per-package install-time hooks don't bleed across the workspace.

Workspace root files (planned, not yet present): `pnpm-workspace.yaml`, root `package.json` with shared scripts (`pnpm -r build`, `pnpm -r test`), single root `tsconfig.json` with project references for IDE.

Until the second app exists, the layout stays flat at the root; the workspace structure lands when the split happens.

## Rich-text editor: ProseMirror (direct)

**Decision: ProseMirror** (not TipTap, not Lexical, not Quill).

The compose body is rich text (formatting marks, lists, links, blockquotes, inline images, signatures). ProseMirror is the right substrate because:

- The schema is enforceable. We declare which marks and node types are valid in an email body — no nested blockquotes-of-blockquotes, no arbitrary inline styles, no script, no embedded objects. The schema doubles as the contract for what tabard sends and what tabard accepts on inbound HTML before sanitisation. See `../architecture/04-rendering.md` for the rendering contract.
- Framework-agnostic. The Svelte integration is a thin layer: a host element, an `EditorState`/`EditorView` lifecycle, and a Svelte `$state` bridge that reflects editor state into the toolbar (is-bold-active, current-block-type) without re-rendering the editor itself.
- TipTap is "ProseMirror with a friendlier React API"; with Svelte you'd write the bridge once and own it. Going one layer lower to ProseMirror is the same integration effort with more control. We don't need TipTap's plugin ecosystem (slash menus, embedded blocks, collaborative editing) for an email composer.

Bundle: ProseMirror core + commands + schema-basic + schema-list + history is ~50–70 KB gzipped. Comfortably within budget.

## HTML sanitisation: DOMPurify

For inbound HTML message bodies (`../architecture/04-rendering.md`). Configured with our allow-list (matching the ProseMirror compose schema where they overlap), `RETURN_DOM` enabled, hooks for the `<a>` rewriting and `<img>` proxy rewriting steps.

## State management: Svelte runes on a normalised cache

No external state library. The cache is a typed object keyed by `(jmapType, id)`; entries are `$state`-wrapped. Views derive their lists with `$derived`. Optimistic writes mutate the cache directly; reverts re-set the prior value. See `../architecture/01-system-overview.md` and `../architecture/03-sync-and-state.md` for the surrounding design.

## Routing: small SPA router

Tabard needs bookmarkable URLs for label and search views (`../requirements/03-labels.md` REQ-LBL-21, `../requirements/07-search.md` REQ-SRC-20). A small router (svelte-spa-router or hand-rolled hash + History API) is sufficient; we don't need a full file-system router.

Decision: hand-rolled. The route table is small (inbox, label/`<id>`, search/`<query>`, thread/`<id>`, settings) and hash-based routing keeps the static-SPA hosting story simple.

## List virtualisation

Hand-rolled. The list semantics are specific (JMAP cursor-paged, state-string-invalidated, ~10k threads max per `../requirements/13-nonfunctional.md` REQ-PERF-05). Off-the-shelf virtualisers handle the easy case but make the JMAP-cursor edge cases awkward.

The component owns: viewport observation (IntersectionObserver), row height (fixed at one density in v1 per `04-simplifications-and-cuts.md`), and the `Email/query` cursor advance trigger.

## JMAP type generation

JMAP types are derived, not hand-rolled, to avoid drift. Source order of preference:

1. Codegen from the IANA-registered JMAP schemas + RFC 8621 datatype definitions.
2. Bridge from herold's Go types via a small generator in herold (a `jmapgen` cmd that emits TypeScript).
3. Hand-typed as a last resort for capabilities we add ourselves (the snooze contract in `../notes/server-contract.md`).

Decision deferred to phase 1 implementation — pick the path with the least coupling once the bootstrap code is sketched.

## Testing

- **Unit:** Vitest (Vite-native, fast, JSDOM where DOM is unavoidable).
- **Contract:** Vitest against a JMAP test fixture (herold's `internal/testharness` once exposed, or a stubbed RFC 8621 server).
- **Acceptance / BDD:** Playwright. Scenarios in `03-testing-strategy.md`.

No visual-regression suite in v1.

## Dependencies discipline

Every runtime dependency is justified. The runtime tree should be readable on one screen:

- `svelte` — framework runtime.
- `prosemirror-{state,view,model,commands,schema-basic,schema-list,history,keymap}` — editor.
- `dompurify` — sanitiser.
- (Possibly) `eventsource` polyfill if Safari's native implementation has gaps; verify before adding.

Build-time dependencies (Vite, TypeScript, Vitest, Playwright, ESLint, Prettier) are not counted against the runtime tree.

## Versions

- Svelte 5.x (runes API stable since 2024-11).
- ProseMirror packages tracked at their current major (1.x for `prosemirror-state`/`view`/`model`).
- TypeScript 5.x.
- Node toolchain pinned via `.nvmrc` once the project is bootstrapped.
