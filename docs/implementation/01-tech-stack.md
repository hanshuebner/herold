# 01 — Tech stack

> **⚠ TBD** — framework selection is an open conversation. Candidates: Svelte (likely SvelteKit), React, plain TypeScript with hand-rolled reactivity, or Solid. The choice depends on bundle size, the keyboard-engine's reactivity needs, the iframe-rendering pattern, and the author's preference. See `../notes/open-questions.md` if this remains open at implementation start.

This file fills in once the framework decision is made. The shape it will take:

## Language

TypeScript, strict mode. No looseness on `any`; opt-out is explicit per file.

## Framework

TBD. Constraints:

- Must support the SPA model (`../architecture/01-system-overview.md`).
- Must permit a single global keyboard dispatcher without fighting framework conventions (`../architecture/05-keyboard-engine.md`).
- Must support `srcdoc` iframes for HTML mail rendering (`../architecture/04-rendering.md`) without framework hooks interfering with the sandbox.
- Bundle target: < 200 KB gzipped for the initial paint.
- Build target: ESM, no IE, no transpilation past the supported browsers (`../requirements/13-nonfunctional.md` REQ-BR).

## Build

TBD. Likely Vite. esbuild internally is fine.

## Type generation for JMAP

The JMAP types in tabard are derived from the RFC 8620 / 8621 schemas, not hand-rolled, to avoid drift. Source: TBD (some combination of the IANA-registered JMAP schemas, herold's Go types, and a generator).

## State management

In-memory, normalised by JMAP `(type, id)`. Whether this lives in a framework store (Redux / MobX / Svelte stores) or a plain typed object depends on the framework choice.

## Sanitisation

DOMPurify is the default candidate. Final pick at framework-selection time.

## Testing

Unit + contract + BDD acceptance scenarios. See `03-testing-strategy.md`.

## Dependencies discipline

Every dependency is justified. The runtime dependency tree should be readable on one screen. No transitive nightmares.
