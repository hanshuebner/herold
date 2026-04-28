# Waves 3.11–3.16 — Gmail-parity push (post 2026-04-28)

**Drafted:** 2026-04-28. **Predecessor:** Wave 3.10 (ShortcutCoachStat,
commit 2e33ef8). **Driver:** UI requirements under
`docs/design/web/requirements/` together with `notes/gmail-feature-map.md`.
**Goal:** close the largest remaining visible gaps between the suite and
Gmail in one coherent six-wave chunk, plus the LLM test scaffolding the
LLM-touching waves require.

This file is the planning artefact for the next round; it does not edit
the requirements, the architecture, or the phasing doc. It points at the
REQ IDs that already exist and identifies the implementation-time work
each wave owes. Cross-reference to `02-phasing.md` Phase 2.5 — the items
below are post-v1.0 features (Phase 3 candidates) that we are choosing
to land before the v1.0 cut because they are user-visible and cheap
relative to the parity payoff.

## Wave order and dependency graph

```
3.11 multi-mailbox membership (server only)
   |
   +--> 3.13 categorisation Inbox tabs (depends on $category-* keyword
   |        already shipped + Mailbox.color extension already shipped;
   |        does NOT depend on multi-mailbox but reads cleaner once
   |        labels behave like labels)
   |
   +--> 3.14 email reactions suite UI (Wave 3.9 server-side ships; this
   |        is the missing client side)
   |
   +--> 3.15 structured filter editor + mute thread + block sender +
            report spam (writes Sieve scripts; no LLM dependency for
            the editor itself, but report-spam wires the spam-classifier
            feedback signal that Wave 3.12's harness covers)

3.12 LLM test infrastructure (mock LLM + capture-mode harness)
   |
   +--> Tests added in 3.12 are kept failing/skipped until 3.16.

3.13, 3.15, plus existing categorise + spam
   |
   +--> 3.16 LLM capture run + response baseline + un-skip the
            recorded-response tests
```

3.11 is the only wave that touches storage primitives; the others sit on
top of existing scaffolding. 3.12 lands first or in parallel with 3.11
so that any new LLM-touching code in 3.13/3.15 can land with tests that
exercise the mock from day one.

## Wave 3.11 — Multi-mailbox membership (REQ-STORE-36..39)

**Driver:** RFC 8621 §1.6.1 + the four upstream JMAP conformance tests
the design commit (23b0f72) catalogued as deferred:

- `email/get-mailbox-ids`
- `email/set-update-add-mailbox`
- `email/set-update-remove-mailbox`
- `email/set-destroy-removes-from-all-mailboxes`

This is the foundational wave. Without it Gmail-style labels (a thread
in Inbox AND a label simultaneously) do not work. Every subsequent UI
parity push assumes labels-as-mailboxes-with-multi-membership.

**Owners:** `storage-implementor` (lead), `imap-implementor`,
`jmap-implementor`. `reviewer` final pass. `conformance-fuzz-engineer`
re-runs the JMAP test corpus and the imaptest suite.

**Deliverables**

| # | Item | Owner |
|---|---|---|
| A | `messages` table reshape: drop `mailbox_id`, `uid`, `modseq`, `flags_bitmap`, `flag_keywords_json`. Add `principal_id` (denormalised for query speed). Both SQLite and Postgres migrations in lock-step. | `storage-implementor` |
| B | New `message_mailboxes(message_id, mailbox_id, uid, modseq, flags_bitmap, flag_keywords_json)` join table. Composite PK; secondary index on `(mailbox_id, uid)` for IMAP UID lookup; secondary index on `(message_id)` for JMAP `mailboxIds` reads. | `storage-implementor` |
| C | `store.Metadata` interface update: every method that today returns or accepts a `mailbox_id` argument becomes per-(message, mailbox) where it should be, or per-message where it should be. The seam is the typed repository — read every call site. | `storage-implementor` |
| D | Delivery-transaction update: insert one `messages` row + N `message_mailboxes` rows for a multi-target delivery (Sieve `fileinto` cascades, future copy-to-multiple). Blob refcount stays per-`messages` row, not per-membership. | `storage-implementor` + `smtp-implementor` (delivery seam) |
| E | IMAP semantics under M:N: `EXPUNGE` removes the per-(message, mailbox) row only — the message stays visible in any other mailbox memberships; the blob stays referenced. `COPY` allocates a fresh `message_mailboxes` row in the target with a fresh UID. `MOVE` is `COPY` + `EXPUNGE` of the source row. UID remains per-mailbox. | `imap-implementor` |
| F | JMAP semantics under M:N: `Email.mailboxIds` is multi-valued. `Email/set { update: { id: { mailboxIds: { a:true, b:true } } } }` adds memberships; `Email/set { update: { id: { mailboxIds: { a:null } } } }` removes one. `Email/set { destroy }` removes from every mailbox in one transaction (REQ-STORE-33). | `jmap-implementor` |
| G | New REQ-PROTO-128 added in the design commit — JMAP capability statement that `Email.mailboxIds` is honoured as a set. No new capability URI; the existing `urn:ietf:params:jmap:mail` advertises this. | `jmap-implementor` |
| H | Migration command `herold diag migrate-messages-mailboxes` — single offline pass per REQ-STORE-38; integrity check that asserts `count(message_mailboxes) == count(messages)` pre-drop of the old `messages.mailbox_id` column. Operator runs once after upgrade. | `ops-observability-implementor` + `storage-implementor` |
| I | `internal/sieve` `fileinto` action update: when the script targets multiple mailboxes (cascade or explicit), the delivery resolves to N memberships, not N copies. Backward-compatible — the existing single-target case is the N=1 case. | `sieve-implementor` |

**Acceptance**

- `make jmaptest` passes the four previously-deferred tests (the
  upstream conformance suite reports zero failures on the membership
  surface).
- `imaptest` 30 s baseline still green (existing `EXPUNGE` /
  `COPY` / `MOVE` paths regression-clean under the new join).
- Both SQLite and Postgres migrations run cleanly on a fresh DB and on
  a non-trivial dump captured pre-migration. Round-trip verified by
  `herold diag fsck`.
- Bench: `Email/get` for an account with 100 k messages × 1.5 mailbox
  memberships (15 % multi-membership) returns within 5 % of the
  pre-migration latency. Single-membership accounts unaffected.

**Estimate:** 1.5 weeks. **Risk:** medium — the join touches every
hot path. `reviewer` is a hard gate.

## Wave 3.12 — LLM test infrastructure (mock + capture mode)

**Driver:** the next two feature waves (3.13 categorisation,
3.15 report-spam) call into the LLM substrate. Without a deterministic
mock, those tests can't run in CI; without a capture mechanism, the
mock can't be kept honest against the real LLM. This wave lands the
plumbing only — the fixtures themselves are recorded in Wave 3.16.

**Owners:** `plugin-platform-implementor` (lead, owns the spam plugin
and the LLM contract), `conformance-fuzz-engineer` (test-harness
shape), `ops-observability-implementor` (config knob).

**Deliverables**

| # | Item | Owner |
|---|---|---|
| A | New package `internal/llmtest` exposing two implementations of the LLM client interface that `internal/categorise` and `internal/spam` both consume: `Recorder` (forwards every call to a real upstream and writes prompt+response pairs to a JSON fixture file) and `Replayer` (reads the same fixtures and serves them deterministically; unknown prompts return a sentinel error that the test surfaces as a missing-fixture failure). | `plugin-platform-implementor` |
| B | The categorise + spam packages refactored so that the LLM client is an injectable interface (it already is in spirit; this wave makes it explicit and threads it through `StartServer` so test harnesses can swap the implementation cleanly). | `plugin-platform-implementor` |
| C | Fixture file format: line-delimited JSON, one entry per call. Schema: `{ "v":1, "kind": "categorise" | "spam-classify", "prompt_hash": "<sha256-hex>", "prompt": "<full-prompt-text-for-debug>", "response": { ... }, "model": "<id>", "captured_at": "<utc>" }`. Lookup key is `(kind, prompt_hash)`. The full prompt is stored for debugging only; the hash is the matching key so prompt churn invalidates fixtures intentionally rather than silently. | `plugin-platform-implementor` |
| D | Capture-mode wiring in the test harness: env var `HEROLD_LLM_CAPTURE=1` flips the harness from `Replayer` to `Recorder` and routes calls to the real upstream named in `HEROLD_LLM_CAPTURE_ENDPOINT`. Default-off; CI never sets it. | `conformance-fuzz-engineer` |
| E | `scripts/llm-capture.sh` — the runner that points the test harness at a real LLM (default: `http://localhost:11434/v1` per Phase-1 default), executes the full LLM-touching test set with `HEROLD_LLM_CAPTURE=1`, writes fixtures into `internal/llmtest/fixtures/<kind>/`, and prints a summary of new / updated / unchanged fixtures. **Run only by the maintainer; never in CI.** | `release-ci-engineer` |
| F | Tests for the categorisation pipeline and the spam-feedback pipeline added with `t.Skip("LLM fixtures not yet captured — run scripts/llm-capture.sh; see Wave 3.16")`. They reference the (initially missing) fixture set and will switch to the regular path once Wave 3.16 records baseline responses. The skip message is a contract — `reviewer` checks that the only skipped tests are the LLM ones. | `conformance-fuzz-engineer` |
| G | New REQ entry in `docs/design/server/requirements/06-filtering.md` Part D (or an Appendix) — REQ-FILT-300..303 — documenting the LLM-test contract: deterministic Replayer in CI, capture-mode is operator-tooling, prompts are versioned via the hash. | `docs-writer` (after the implementor lands the code) |

**Acceptance**

- `go test ./internal/categorise/... ./internal/spam/...` runs green
  with the LLM-touching tests skipped, all other tests passing.
- `HEROLD_LLM_CAPTURE=1 scripts/llm-capture.sh` against a running
  Ollama writes fixtures for every skipped test and exits 0.
- `Replayer` returns the recorded response for every `(kind, hash)`
  pair after capture, and returns `ErrFixtureMissing` for any
  unknown hash (test surfaces this as a clear "regenerate fixtures"
  message, never a silent pass).
- The fixture files are checked into the repo and small (a few KB
  each); the schema is stable enough that capture-driven churn
  produces readable diffs in PRs.

**Estimate:** 0.5 week. **Risk:** low — the surface is small; the
real risk is "did we get the prompt-versioning right" which the hash
strategy mitigates.

## Wave 3.13 — Categorisation Inbox tabs + prompt/category-set config

**Driver:** `docs/design/web/requirements/05-categorisation.md`
(REQ-CAT-01..51). The server already applies `$category-<name>`
keywords on delivery (`internal/protosmtp/deliver.go:310`,
`internal/categorise/`). The suite has no Inbox tabs yet, no settings
UI for category set or prompt, and no bulk re-categorisation API.

This is the most visible single Gmail-parity feature still missing.

**Owners:** `web-frontend-implementor` (lead, suite UI),
`jmap-implementor` (categorise capability + prompt + bulk
re-categorise), `plugin-platform-implementor` (uses Wave 3.12's mock
in tests).

**Deliverables**

| # | Item | Owner |
|---|---|---|
| A | New JMAP capability `https://netzhansa.com/jmap/categorise` advertised by herold's session descriptor when the categoriser is configured. Capability metadata reports the active category set and a flag for whether bulk re-categorisation is permitted. | `jmap-implementor` |
| B | New JMAP datatype `CategorySettings` (singleton per principal). Properties: `categories: [{ id, name, order }]`, `prompt: string`, `defaultPrompt: string` (read-only). Methods: `CategorySettings/get`, `CategorySettings/set`. Update triggers a state-change on the type but NOT a recategorise — that is explicit. | `jmap-implementor` + `categorise` package |
| C | New JMAP method `CategorySettings/recategorise` on the same datatype. Args: `{ scope: "inbox-recent" | "inbox-all", limit: number }`. Server returns a job id; progress polled via a follow-up `CategorySettings/recategoriseStatus` (or a state-change event). Runs in the background; respects the user's existing $category-* user-corrections (REQ-CAT-21) by skipping them. | `jmap-implementor` + `categorise` package |
| D | Suite Inbox view rewritten to render category tabs at the top: Primary | Social | Promotions | Updates | Forums (in the user's configured order). Active tab filters via the corresponding `$category-<name>` keyword (or no category keyword for Primary). Unread badge per tab. URL state preserves the active tab. | `web-frontend-implementor` |
| E | Suite "Move to category" thread/message action (`m` shortcut + toolbar). Patches `$category-<name>` keyword via `Email/set`. Optimistic. Available from thread list, open thread, and the per-message context menu. | `web-frontend-implementor` |
| F | Suite settings panel section "Categories": list editor (add / rename / reorder / remove; Primary cannot be removed), advanced "Edit prompt" textarea, "Reset to default" button (REQ-CAT-42), "Re-categorise inbox" button calling the new RPC (REQ-CAT-30). Progress indicator wires to the chrome (CoachStrip-style banner). | `web-frontend-implementor` |
| G | Tests for the recategorise RPC and the prompt-update path use the Wave 3.12 mock; fixtures captured in Wave 3.16. | `conformance-fuzz-engineer` |

**Acceptance**

- A fresh principal sees five Inbox tabs with the default category set;
  inbound mail is classified and lands under the right tab without any
  user action.
- Editing the category set or the prompt persists across reload and
  across devices (server-side state).
- "Re-categorise inbox" runs in the background, the chrome shows
  progress, and the inbox tabs update as keywords change.
- "Move to category" applies the keyword optimistically, the tab
  count updates, and the change syncs to a second open tab via
  EventSource.
- LLM-touching tests run green against the recorded fixtures (after
  Wave 3.16 lands).

**Estimate:** 1 week. **Risk:** low-medium — the server piece is
mostly already there; the bulk recategorise + progress-reporting RPC
is the only new cross-cutting surface.

## Wave 3.14 — Email reactions suite UI

**Driver:** `docs/design/web/requirements/02-mail-basics.md` § Reactions
(REQ-MAIL-150..152, REQ-MAIL-170..184). Wave 3.9 (commit 190dd32)
landed the server-side `Email.reactions` extension property and the
cross-server propagation. The suite has no React button.

**Owners:** `web-frontend-implementor`. No server changes.

**Deliverables**

| # | Item | Owner |
|---|---|---|
| A | "React" pill action below each expanded message in `MessageAccordion.svelte`, next to Reply / Forward (REQ-MAIL-150..152). Opens the emoji picker; selection patches `Email.reactions/<emoji>/<my-principal-id>` true. Optimistic; Undo via toast. | `web-frontend-implementor` |
| B | Reactions display under each message: emoji-grouped chips with reactor count. Hover/tap on a chip lists reactor names. Clicking the user's own emoji removes the reaction (toggle). | `web-frontend-implementor` |
| C | Cross-server confirmation modal (REQ-MAIL-191): when the message has a `List-ID` header AND > 5 recipients, a one-time confirmation appears. Decision persisted client-side. | `web-frontend-implementor` |
| D | Vitest coverage for the optimistic patch + state-change reconciliation paths. The vitest harness landed in Wave 2.x already covers the basic shape. | `web-frontend-implementor` |

**Acceptance**

- Reacting to a message in browser A shows the reaction in browser B
  via EventSource within a state-change cycle.
- Toggling off the user's own reaction removes only their entry; other
  reactors' entries unchanged.
- Authorisation error from the server (attempt to mutate someone
  else's reaction) shows a clear inline error and reverts the
  optimistic patch.

**Estimate:** 0.5 week. **Risk:** low.

## Wave 3.15 — Structured filter editor + mute thread + block sender + report spam/phishing

**Driver:** `docs/design/web/requirements/04-filters.md` (REQ-FLT-01..31)
+ `02-mail-basics.md` REQ-MAIL-130..160 (per-message context menu:
mute thread, block sender, report spam, report phishing, filter messages
like this).

The current state: a raw Sieve script editor lives in
`web/apps/suite/src/views/settings/SieveForm.svelte` (Wave 2.x). The
Sieve runtime is fully wired. What's missing is the user-friendly
structured layer on top, the menu-action paths that auto-generate Sieve
rules without exposing Sieve, and the spam-feedback signal.

**Owners:** `sieve-implementor` (lead, server side rule abstraction),
`web-frontend-implementor` (suite UI), `plugin-platform-implementor`
(spam-feedback signal endpoint), `jmap-implementor` (mute-thread + block
list datatypes if we go that route).

**Deliverables**

| # | Item | Owner |
|---|---|---|
| A | A higher-level "ManagedRule" abstraction over Sieve. Stored as a JMAP datatype `ManagedRule` with structured fields: `conditions: [{ field, op, value }]`, `actions: [{ kind, params }]`, `enabled: bool`, `order: int`. Server compiles all enabled `ManagedRule` rows + the user's hand-written Sieve into one effective script for the principal. The two coexist: the user can edit either side; the server keeps them as separate sources concatenated in a known order. | `sieve-implementor` |
| B | New JMAP capability `https://netzhansa.com/jmap/managed-rules`. Methods: standard `ManagedRule/{get,query,set,changes}`. State-change wires through the existing feed. | `jmap-implementor` |
| C | Suite filter editor (`SettingsView` → "Filters" section): list view, create / edit / reorder / enable-disable / delete; structured form for conditions (REQ-FLT-01) and actions (REQ-FLT-10..15) per `04-filters.md`; "Test against existing mail" runs `Email/query` matching the conditions and shows N matched threads (REQ-FLT-21). | `web-frontend-implementor` |
| D | Mute thread (REQ-MAIL-160): per-message-menu / per-thread-menu action that creates a `ManagedRule` whose condition is "thread-id == X" (a new condition type added in deliverable A) and action is "skip inbox + mark read on arrival." Reversible via "Unmute" — flips the rule's `enabled` flag rather than destroying it. | `sieve-implementor` + `web-frontend-implementor` |
| E | Block sender (REQ-MAIL-134): per-message-menu action with confirmation modal. Creates a `ManagedRule` with condition "from address matches X" and action "delete." Surfaces the active block list in the settings panel under "Blocked senders" with per-row Unblock. | `sieve-implementor` + `web-frontend-implementor` |
| F | Report spam (REQ-MAIL-135): per-message-menu action; sets `$junk`, moves to Spam mailbox, and POSTs a feedback signal to the spam classifier (mechanism analogous to Wave 3.13's recategorise feedback — server-side, per principal). | `web-frontend-implementor` + `plugin-platform-implementor` |
| G | Report phishing (REQ-MAIL-136): same as spam plus a `$phishing` keyword applied to the message and the feedback signal carries `kind: "phishing"`. Operator-policy hook for upstream forwarding (REQ-MAIL-137 illegal-content reporting is gated on operator config; if disabled, the menu item is hidden — not greyed). | `web-frontend-implementor` + `http-api-implementor` |
| H | "Filter messages like this" (REQ-MAIL-138): opens the new filter editor pre-populated with conditions derived from the message: From address, Subject prefix, List-Id if present. | `web-frontend-implementor` |
| I | Tests for the report-spam feedback path use the Wave 3.12 mock; fixtures captured in Wave 3.16. The structured filter editor has Vitest coverage that exercises the editor's validation rules without the server. | `conformance-fuzz-engineer` |

**Acceptance**

- A user creates a structured filter from the settings panel without
  ever seeing Sieve syntax; inbound mail is filtered correctly.
- The user can also edit the raw Sieve script (existing affordance);
  the two sources coexist without one stomping the other.
- Mute thread on thread T removes T from inbox immediately and
  silently archives subsequent replies; "Unmute" re-enables the rule
  and the next reply lands in inbox.
- Block sender prompts a confirmation, and the next message from
  that sender lands in Trash.
- Report spam / phishing applies the right keyword, moves the
  message, and the spam classifier's feedback log records the
  signal (verified via the LLM mock fixtures).

**Estimate:** 2 weeks. **Risk:** medium — the ManagedRule abstraction
is a new datatype with a non-trivial compilation step into Sieve. The
two-source-of-truth coexistence (managed rules + hand-written Sieve)
needs clear semantics; security-reviewer must sign off on the compile
+ concatenate path so a malformed managed rule cannot escape into the
hand-written section.

## Wave 3.16 — LLM fixture capture + un-skip the recorded-response tests

**Driver:** Wave 3.12 left every LLM-touching test with `t.Skip("LLM
fixtures not yet captured")`. With Waves 3.13 and 3.15 landed, the
prompt set is stable enough to record. This wave runs the capture
script against a real Ollama (or any operator-chosen
OpenAI-compatible endpoint), commits the fixture files, and drops the
skip wrappers.

**Owners:** `conformance-fuzz-engineer` (lead — owns the capture run
and the fixture-set quality gate). Maintainer (the human) runs the
capture script on their machine; the diffs land in a PR like any
other change.

**Deliverables**

| # | Item | Owner |
|---|---|---|
| A | Run `scripts/llm-capture.sh` against the local Ollama, recording fixtures for every LLM-touching test. Spot-check responses for shape correctness (e.g., the categoriser's response is a valid category name). | `conformance-fuzz-engineer` |
| B | Drop every `t.Skip("LLM fixtures not yet captured...")` call. The replayer-driven path is now the default. CI is green end-to-end. | `conformance-fuzz-engineer` |
| C | A short note in `docs/design/server/implementation/03-testing-strategy.md` explaining the capture cadence: re-run when the prompt changes (the hash mismatch will surface as missing-fixture errors), or when the model version pins move. | `docs-writer` |

**Acceptance**

- `make ci-local` passes with no skipped tests on the LLM surface.
- A deliberate prompt-change in `internal/categorise/prompt.go`
  produces a clean missing-fixture failure that names the affected
  test and points at the capture script.

**Estimate:** 0.25 week (mostly capture wall-clock + diff review).

## Cross-cutting

- **Security review.** Wave 3.11 (storage migration) and Wave 3.15
  (managed-rule compilation into Sieve) are explicit
  `security-reviewer` gates. Wave 3.13 (LLM prompt config exposed to
  the user) needs a quick `security-reviewer` pass on the prompt
  size cap and prompt-injection containment — the categoriser already
  treats LLM output as untrusted and only accepts category names from
  a known set; that pattern must hold under user-edited prompts.
- **Conformance.** Wave 3.11 must close the four deferred JMAP tests
  before any other wave merges. The membership change touches every
  protocol surface; the conformance run is the gate.
- **Docs.** `docs-writer` updates the operator manual after each wave
  with the new capabilities + settings, in line with the Phase 2.5
  punch list (`notes/phase-2.5-punchlist-2026-04-27.md`).
- **Migration.** Wave 3.11 is the only wave with an offline migration.
  It MUST land on a release branch and be backed out cleanly if the
  fsck check fails on real-operator data; the migration tool is
  re-runnable.

## Total estimate

~5.25 person-weeks. Realistically eight calendar weeks at one engineer
with the LLM-capture wait, security reviews, and conformance reruns
factored in. This buys: Gmail-style multi-membership labels, the
five-tab Inbox, reactions, structured filters with mute / block /
report-spam, and a deterministic LLM test substrate that future
LLM-touching features inherit for free.
