# Herold — working agreement for Claude Code

This file is the operating context the root agent reads first.

## Read these, in order, before any substantive work

1. `docs/design/00-scope.md` — goals, non-goals, simplification themes.
2. `STANDARDS.md` — global coding and development rules. Authoritative.
3. `AGENTS.md` — the specialist subagent roster and when to delegate.
4. The requirements file(s) for the subsystem you are about to touch, under `docs/design/server/requirements/`.
5. The matching architecture file under `docs/design/server/architecture/`.

## Delegation posture

- Any substantive implementation work in a subsystem goes to that subsystem's specialist agent (see `AGENTS.md`). The root agent does not implement SMTP parsers, IMAP state machines, or store schemas directly.
- Cross-subsystem work: the root agent sketches the interface, delegates the two sides in parallel, then integrates.
- Every substantive PR is reviewed by `reviewer`. Security-sensitive PRs also go to `security-reviewer`. Wire-parser PRs also go to `conformance-fuzz-engineer`.

## Hard rules (restated because they are frequently overlooked)

- **No emojis anywhere.** Code, commits, CLI output, logs, docs — all plain ASCII.
- **No CGO in the default build.** Pure-Go SQLite, Postgres, Bleve, NATS.
- **Both SQLite and Postgres in CI.** Code that only works on one is not mergeable.
- **Out-of-process plugins only.** JSON-RPC 2.0 on stdio. No in-process loader, no Wasm.
- **No in-process event bus.** Direct calls or store change feed. The only "bus" that exists is the `protoevents` → event-publisher-plugin dispatch.
- **System config (`/etc/herold/system.toml`) is never mutated at runtime.** Domains, principals, aliases, Sieve scripts, DKIM keys, etc. live in the DB.
- **Full test coverage is the standard** (see `STANDARDS.md` §8). Every wire parser has a fuzz target; every integration test runs on both backends; tests are deterministic; documentation examples are executable tests.
- **Requirements documents at the repo root are currently authoritative and frozen.** They will move under `docs/` in a future session; do not rearrange or edit them until then.

## Task tracking

Use `TaskCreate` / `TaskUpdate` / `TaskList` to coordinate. Agents claim unowned tasks in their surface area; `reviewer` closes them.

## Commit etiquette

- One logical change per commit.
- Commit message `subsystem: short imperative subject` on the first line, body explaining the *why*, affected REQ IDs, and the test plan run locally.

## When in doubt

Re-read `STANDARDS.md`. If the rule is not there and it should be, propose an edit in a PR against the standards document rather than working around the gap in implementation.
