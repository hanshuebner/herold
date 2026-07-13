# 06 — Filtering: spam classification, categorisation, and Sieve

*(Revised 2026-04-24: traditional filtering removed. Revised 2026-07-13 by
ADR-0002 and ADR-0004: the LLM authors the filter instead of running it. Scoring
is a compiled ruleset evaluated in-process; an LLM endpoint is an enhancement,
not a dependency.)*

Three stages, kept separate:

- **Scoring** — evaluate a compiled ruleset over the message. One evaluator, one feature registry, two outputs: a spam verdict (Part A) and a set of category assignments (Part C). Pure Go, no network call, no mail content leaving the process. The rules are generated from the user's natural-language policy by a plugin, once, at compile time; messages near the spam threshold may be escalated to an LLM classifier.
- **Delivery routing** — decide where an accepted message lands. Sieve scripts (global + per-recipient). Sieve routes; scoring classifies; an explicit route beats an inferred one.
- **Presentation** — how loudly a message enters the inbox, from its category's disposition (REQ-FILT-204).

Email **authentication** (DKIM/SPF/DMARC/ARC) is upstream and unchanged — that's in `requirements/04-email-security.md`. Authentication results are features the ruleset reads; they are not the classifier.

## Pipeline

```
accept → authenticate (SPF/DKIM/DMARC/ARC) → score (compiled ruleset, in-process)
           → global Sieve → per-recipient Sieve → categorise → deliver
                │                                     │
                └── verdict + confidence + trace      └── categories + disposition
```

## Part A: LLM classification

### Model

- **REQ-FILT-01** The classifier produces: `verdict` ∈ {`ham`, `suspect`, `spam`}, `confidence` ∈ [0.0, 1.0], `reason` (short text).
- **REQ-FILT-02** Default verdict mapping: `ham` → Inbox; `suspect` → Inbox + `$Junk` keyword; `spam` → Junk folder. Sieve can override.
- **REQ-FILT-03** Message gets headers added on delivery: `X-Spam-Verdict`, `X-Spam-Confidence`, `X-Spam-Reason`, and the existing `Authentication-Results`.

### Endpoint

- **REQ-FILT-10** The classifier is reached via an HTTP endpoint speaking **OpenAI-compatible chat completions** (`POST /v1/chat/completions`). This covers: Ollama, llama.cpp server, vLLM, LocalAI, OpenAI, Anthropic (via compat gateway), Groq, Azure OpenAI, and any other OpenAI-compat inference service.
- **REQ-FILT-11** Default endpoint: `http://localhost:11434/v1` (Ollama default). Default model name: operator-configured (recommend `llama3.2:3b` or similar small local model).
- **REQ-FILT-12** Operator configures in application config: endpoint URL, API key (optional), model name, system prompt (overridable), temperature (default 0), max tokens, request timeout, daily request budget (optional).
- **REQ-FILT-13** The classifier is implemented as the **default spam plugin** (`REQ-PLUG`). Operators can replace it with any plugin conforming to the spam-classifier contract — for example, a custom fine-tuned model, a cloud API with a different protocol, or a purely deterministic plugin for testing.

### Prompt shape (built-in, customizable)

- **REQ-FILT-20** The server constructs a prompt containing:
  1. **System message** — short instruction telling the model it's classifying email; asking for a JSON response with `verdict`, `confidence`, `reason`.
  2. **Context fields** — authentication results (SPF/DKIM/DMARC/ARC), sender reputation signals we can compute locally (first-time-sender, mismatched From vs Return-Path, known-good correspondent flag), recipient info (is this a mailing list address, catch-all, etc.).
  3. **Message excerpt** — headers (curated set), subject, body excerpt (truncated to fit model context; ~2k tokens default). HTML stripped to text. URLs normalized and included.
- **REQ-FILT-21** Output expected as strict JSON. Parse failure → fallback verdict = `suspect` with low confidence and a logged error.
- **REQ-FILT-22** System prompt is customizable per application config (or via a plugin that implements the full prompt construction itself).

### What's in the prompt, what's not

- **REQ-FILT-30** MUST include: `From`, `To`, `Cc`, `Subject`, `Date`, `Reply-To`, `List-Id` (when present), `Return-Path`, the first ~2k tokens of plain-text body.
- **REQ-FILT-31** MUST NOT include: attachment contents, full HTML, raw headers beyond the curated set, the recipient's prior mail history. (Privacy-preservation; LLM prompts are data leakage surfaces.)
- **REQ-FILT-32** Binary attachments described by filename + size + MIME type only.

### Failure modes

- **REQ-FILT-40** If the LLM endpoint is unreachable or slow past timeout: message is accepted and delivered with `X-Spam-Verdict: unknown`, no folder override. Operators MUST see a warning metric + log event. Default behavior is **accept-on-failure** (degrade open), not defer.
- **REQ-FILT-41** If the LLM returns unparseable output past retry (1 retry by default), treat as `suspect` with `confidence=0.0` and log.
- **REQ-FILT-42** Per-message classification SHOULD complete in ≤ 2s p95. Above threshold → accept anyway, mark `unknown`.
- **REQ-FILT-43** Failure mode is observable: `herold_spam_classifier_{attempts,failures,timeouts}_total` + `herold_spam_classifier_latency_seconds` histogram.

### Rate limiting and cost control

- **REQ-FILT-50** Per-principal rate limit: configurable (default: no limit for 1k-mailbox scale).
- **REQ-FILT-51** Global request budget: optional daily/hourly cap. When cap hit, classifier returns `unknown` and we fall through to accept-as-ham.
- **REQ-FILT-52** Per-endpoint circuit breaker: if failure rate > 50% over 60s, trip to accept-on-failure for 5 minutes.

### Privacy and endpoint trust

- **REQ-FILT-60** Operator chooses endpoint. Docs describe clearly: "your inbound mail content — headers, subjects, and body excerpts — is sent to this endpoint. Choose accordingly. Default points at localhost for a reason."
- **REQ-FILT-61** **No default cloud endpoint.** Operators must consciously opt in to cloud providers by changing the endpoint URL.
- **REQ-FILT-62** No logging of raw LLM request bodies at `info` level (contains message content). `debug` level logs LLM payloads for troubleshooting.
- **REQ-FILT-63** MUST NOT send messages marked with `Autocrypt` or `X-PGP-*` headers to external LLM endpoints (stays local or skipped entirely — configurable, default skip).

### Transparency to the user (G14)

LLM use on a user's mail is not a black box. The user can see the prompt
that was used to produce a verdict and the verdict itself. The operator
keeps system-side guardrails private (so guardrails can be iterated
without leaking implementation detail to recipients).

- **REQ-FILT-65** Per-account read API: the suite (and any JMAP client) can fetch the **user-visible prompt** currently in effect for spam classification — i.e. the operator-or-user-configurable system prompt portion (REQ-FILT-22) plus a description of the structured context fields (REQ-FILT-30..32), but NOT operator-defined guardrail prefixes/suffixes that the operator has marked as guardrails in application config.
- **REQ-FILT-66** Per-message inspect API: for any delivered Email, the suite can fetch the LLM's verdict, confidence, reason text, the **user-visible prompt as actually applied to that message** (with the per-message context fields filled in but with body excerpt either elided or truncated to the first ~200 chars to avoid re-exposing in admin contexts), and the model identifier. Implementation lives alongside `herold mail inspect` (REQ-FILT-141); this REQ requires the same data to be available over admin REST and via a JMAP datatype the suite can read for its own messages.
- **REQ-FILT-67** Application config separates **user-visible prompt** (mutable by user, returned by REQ-FILT-65/66) from **operator guardrails** (mutable by operator only, NOT returned by REQ-FILT-65/66). Default config puts no text in the guardrail slot; operators who add abuse-prevention or output-shape preambles do so consciously and accept that those will be invisible to end users.
- **REQ-FILT-68** The same prompt-transparency contract applies to categorisation (REQ-FILT-216 in Part C). One transparency surface, two LLM features.

### Training / feedback

- **REQ-FILT-70** Users moving a message to/from Junk generates a **feedback record** (timestamp, verdict given, corrected verdict, headers). Stored locally for operator review; NOT sent back to the LLM.
- **REQ-FILT-71** Feedback is exposed via admin API/CLI so an operator running a fine-tuneable local model can export the corpus.
- **REQ-FILT-72** No automatic re-training of the model. That's outside our scope; we're a mail server, not a training pipeline.

### Authentication-derived decisions (independent of classifier)

- **REQ-FILT-80** DMARC `p=reject` alignment failure → reject at SMTP time (REQ-SEC-31). Does not go to LLM.
- **REQ-FILT-81** DMARC `p=quarantine` alignment failure → treat as `spam` verdict, skip LLM call (we already know).
- **REQ-FILT-82** Unauthenticated inbound from a domain that *publishes* DMARC with `p=none`: sent to LLM; classifier decides.
- **REQ-FILT-83** No authentication records at all + from-address is from our own domain: treat as `spam` (forgery), skip LLM.

These four paths are hard-coded — they're authentication-layer decisions, not spam-filter decisions.

## Part B: Sieve

Sieve language support is per `requirements/01-protocols.md` REQ-PROTO-60..68. This section is about how Sieve fits with the classifier.

### Interaction with classifier

- **REQ-FILT-100** Sieve MUST see: the classifier's `verdict`, `confidence`, and `reason` as Sieve variables (`${spam.verdict}`, `${spam.confidence}`, `${spam.reason}`). Also the Stalwart-compatible `spamtest` / `spamtestplus` mapping (REQ-PROTO-65).
- **REQ-FILT-101** If classifier returned `unknown` (failure mode), Sieve sees `${spam.verdict}` = `unknown` and can decide (e.g., still deliver to Inbox; accept is default).
- **REQ-FILT-102** Default behavior without a user Sieve script: REQ-FILT-02's mapping. User scripts fully override.

### Global vs per-recipient scripts

Unchanged from prior version:

- **REQ-FILT-110** At most one *global* script (admin-managed). Runs first. Cannot be replaced by a user. A fatal error defers delivery (4xx) — operator must fix.
- **REQ-FILT-111** At most one *active* per-user script. Runs after the global. Fatal error → fall back to "keep" (no user mail lost).
- **REQ-FILT-112** Script execution sandboxed: CPU cap (500 ms), memory cap (16 MiB), no FS access, no outbound network beyond `redirect`, max `redirect` count (5), max `notify` count (2).

### Storage and edit flow

- **REQ-FILT-120** Sieve scripts stored per-principal in the DB: one active script per principal. Edits via ManageSieve (RFC 5804) or the JMAP Sieve datatype (RFC 9007); no admin REST surface (REQ-ADM-15). Multi-script-per-principal ("one active, N inactive") is not implemented; ManageSieve clients that maintain drafts do so client-side.
- **REQ-FILT-121** Validation on upload uses the exact interpreter we run at delivery — no divergence.

### Extensions explicitly out

- `llm` / `exec` / `extprograms`: no. If you want an LLM call, it's already in the classifier; Sieve doesn't get to call LLMs independently.
- `foreverypart` + `mime` + `extlists` + `subaddress` + `duplicate` + `enotify (mailto)` + `editheader` + `vacation-seconds` + `spamtestplus`: yes (core set).

## Part C: Categorisation

*(Rewritten 2026-07-13 by ADR-0004. A category and a label are one primitive.
Categorisation is a compiled ruleset, not a per-message LLM call. Superseded:
REQ-FILT-200, -201, -210, -211, -213, -214, -215, -217, -221.)*

Spam decides "deliver / junk". Categorisation decides *what kind* of mail this is
and, through the category's disposition, how loudly it enters the inbox. Both are
scored by the same evaluator over the same feature registry (ADR-0002); they
differ in what they produce.

A **category is a label** (`docs/design/web/requirements/03-labels.md`). It carries
a name, colour and parent like any label, plus — when the user wants it to fill
itself — a natural-language `definition`, the `rules` compiled from it, a
`disposition`, and a `priority`. Suite surface:
`docs/design/web/requirements/05-categorisation.md`.

### The category object

- **REQ-FILT-200** Categorisation runs on **all** delivered mail, not only inbox-bound mail, because `filed` is a disposition (REQ-FILT-204) and a categoriser that sees only inbox-bound mail cannot implement it. Mail classified as spam (`\Junk`) is exempt.
- **REQ-FILT-201** A message may carry **several** category assignments, stored as `$category-<id>` keywords. The inbox presents the message once, in the lane of its highest-priority category (REQ-FILT-205).
- **REQ-FILT-202** A message matching no category carries no `$category-*` keyword. The suite presents it under the category holding the `primary` role.
- **REQ-FILT-203** Categorisation runs once at delivery. Subsequent edits to the message do not re-trigger it; re-categorisation is explicit (REQ-FILT-220).
- **REQ-FILT-204** **Disposition.** Each category carries exactly one disposition, and it governs inbox membership, stream presentation, unread badging, and push notification together:

  | Disposition | Inbox | Presentation | Badge | Push |
  |---|---|---|---|---|
  | `pinned` | yes | own tab (max 5 per account) | yes | yes |
  | `bundled` | yes | one collapsed row in the stream | inline count only | no |
  | `daily` | yes | bundled, surfaced once a day | inline count only | no |
  | `weekly` | yes | bundled, surfaced once a week | inline count only | no |
  | `filed` | no | reachable via the category only | no | no |
  | `none` | yes | no lane; a search facet | no | no |

  `filed` is what "label and archive" means. `pinned` is what a category tab means. They are one mechanism at two settings.
- **REQ-FILT-205** **Priority.** Categories occupy a single per-account ordered list. When a message matches several, the highest-priority match determines its inbox lane and its disposition. Ordering is user-editable and is the only place a Hobby-versus-Promotions conflict is decided.
- **REQ-FILT-206** **Provenance.** Every category assignment records who made it: `machine` (the evaluator), `rule` (an explicit user rule or Sieve action), or `user` (a manual assignment). A `machine` assignment MUST NOT overwrite a `user` assignment, and re-categorisation (REQ-FILT-220) MUST NOT touch `user` assignments.

### Pipeline placement

- **REQ-FILT-207** **Sieve routes; categories classify; an explicit route beats an inferred one.** If the user's Sieve script issued a `fileinto` for the message, that destination stands. Otherwise the highest-priority matching category's disposition (REQ-FILT-204) decides whether the message enters the inbox.
- **REQ-FILT-208** Category assignment is visible to Sieve as a keyword, so `04-filters.md` rules and Sieve scripts can test and act on `$category-*` like any other keyword.

### Configuration

- **REQ-FILT-210** **Per-account category set: stored, user-owned, and editable.** The category set is DB state (invariant 9), not an artefact of a classifier response. A user creates, renames, recolours, reorders, and deletes categories. The five defaults (`primary`, `social`, `promotions`, `updates`, `forums`) ship seeded with compiled-in rules and dispositions, and are editable and deletable like any other.
- **REQ-FILT-211** **Per-category definition: free text, optional.** A category with a `definition` fills itself; a category without one is a plain hand-applied label. The definition is the user-visible, user-mutable policy of REQ-FILT-22 / REQ-FILT-67, scoped to one category.
- **REQ-FILT-212** A "reset to default" control restores the shipped default categories, their definitions, and their dispositions.

### Compilation and evaluation

- **REQ-FILT-213** **The LLM compiles; it does not classify.** A category's `definition` is compiled **once** into an ADR-0002 ruleset via the `categorise.compile` plugin method, and the ruleset is then evaluated in-process, per message, in pure Go. The compile payload carries the definition text, the rule schema, and the feature registry. **No message content is in it.** There is no per-message LLM call on the categorisation path.
- **REQ-FILT-214** **An LLM endpoint is not required.** The default categories ship with rules compiled into the binary, over structural signals herold already has: `List-Id`, `List-Unsubscribe`, `Precedence`, `Auto-Submitted`, `Authentication-Results`, and sender-in-contacts. A deployment with no LLM endpoint configured has a working category set; `definition` editing is the feature that needs one.
- **REQ-FILT-215** Categorisation shares ADR-0002's rule language, evaluator, feature registry, safety limits, and fuzz target. It does not introduce a second rule format. The evaluator returns the set of matching categories with their scores; the highest-priority match (REQ-FILT-205) takes the lane.
- **REQ-FILT-216** **Transparency (G14).** Per-account: the user reads each category's `definition` and the ruleset it compiled to, each rule carrying the definition line that produced it. Per-message: the user reads the trace — which rules fired, what each contributed, and where the total landed. Operator guardrails are excluded (REQ-FILT-67).
- **REQ-FILT-217** **Compile flow.** Editing a `definition` does not silently change the user's mail. The server compiles, validates the ruleset (schema, arity, types, feature names, limits), backtests it against a sample of the principal's own mail, and returns a **preview diff** — "these N messages would change category", with them listed. Nothing is activated until the user accepts. The previous ruleset is retained so rollback is a click. This is ADR-0002's flow, unchanged.

### Re-classification and correction

- **REQ-FILT-220** "Re-categorise" re-runs the evaluator over the principal's recent mail (last N messages, configurable; default 1000) under the current rulesets. It runs as a background job with progress reporting, and it MUST NOT touch assignments whose provenance is `user` (REQ-FILT-206). Because evaluation is local and deterministic, this is a cheap operation.
- **REQ-FILT-221** **Correction writes a rule.** When the user recategorises a message, the server persists the assignment with `user` provenance and offers to make it stick — by adding the sender, domain, or list-id to the target category's named list (ADR-0002's `lists`). Accepting writes a deterministic rule that needs no LLM call and cannot be undone by a later re-categorisation. This replaces the previous "recorded for prompt-tuning feedback" with a mechanism that actually changes the next verdict.

### IMAP surface

- **REQ-FILT-222** Each category is exposed to IMAP as a **virtual mailbox** backed by a saved `Email/query` on its keyword. The message is stored once; the virtual mailbox holds no copy, and re-categorisation does not allocate or burn UIDs.
- **REQ-FILT-223** **Writes into a category's virtual mailbox mirror Gmail's semantics**, so that dragging a message onto a category folder in Thunderbird or Apple Mail does what it appears to do:
  - `COPY` / `APPEND` into the category sets its `$category-*` keyword, with `user` provenance (REQ-FILT-206), so the evaluator never undoes it.
  - `MOVE` sets the keyword **and** removes the message from INBOX — the `filed` disposition (REQ-FILT-204), reached from the IMAP side.
  - `EXPUNGE` from the category clears the keyword; the message stays wherever else it lives.

### Deferred bundles

- **REQ-FILT-224** A `daily` or `weekly` category surfaces at a **fixed per-principal hour**, default 07:00 in the principal's timezone, user-configurable. Predictability is the value of batching: the user learns when this mail arrives. Deferral governs the inbox stream only — the mail is reachable through the category at any time, and is never withheld from search, IMAP, or the category's own view.

### Migration

- **REQ-FILT-225** Existing name-keyed `$category-<name>` keywords migrate to the id-keyed Category objects: the five shipped names (`primary`, `social`, `promotions`, `updates`, `forums`) map onto the seeded categories. Every other `$category-<name>` keyword — a name the user's edited prompt introduced, or one the LLM invented under the old free-vocabulary response contract — is **removed**. The migration **reports what it deleted**: each dropped name and the number of messages that carried it. A migration that discards user-visible state does not do it silently.

### Failure isolation

- **REQ-FILT-230** Categorisation failures NEVER block delivery. An unevaluable or missing ruleset leaves the message uncategorised and it lands in the inbox normally. With evaluation in-process there is no timeout and no network failure mode to degrade against; the remaining failure is a ruleset that fails validation, which is rejected at compile time and never activated.
- **REQ-FILT-231** Categorisation MUST NOT modify any message header or body. Its persistent effects are the `$category-*` keywords, their provenance records, and — for a `filed` disposition — the message's inbox membership.

## Stripped features (explicit cut list)

For traceability: what was in the v1 plan before the rescope and is no longer:

- Rule engine with per-rule scores.
- Bayesian token classifier + training DB.
- RBL/DNSBL lookups.
- URIBL lookups.
- Structural MIME heuristics as standalone rules (still inputs to the prompt, but no scored rule engine).
- URL reputation checks.
- Short-term reputation store.
- Attachment filename blocklist (**KEEP**, as an authentication-layer concern not a spam-filter one — implemented in the MTA, not in the classifier).

Gone from scope. Plugins could reintroduce any of them as operator-written code.

### Attachment blocklist (retained)

- **REQ-FILT-130** MTA-side attachment extension blocklist (default: `.exe`, `.scr`, `.bat`, `.cmd`, `.com`, `.msi`, `.js`, `.vbs`, `.lnk`, `.iso`) rejects at SMTP time regardless of classifier verdict. This is policy, not spam filtering. Configurable.

## Observability

- **REQ-FILT-140** Per-message classification decisions logged: `{message_id, verdict, confidence, reason_snippet, latency_ms, model, endpoint}`.
- **REQ-FILT-141** Admin UI/CLI: `herold mail inspect <msgid>` shows verdict + LLM request-response (redacted body) + Sieve trace.
- **REQ-FILT-142** Sieve execution traces: optional per-user debug, logs sequence of actions.

## Part D: LLM test infrastructure

The LLM test contract ensures that every feature touching the classifier or
categoriser can run deterministically in CI without a network connection.

- **REQ-FILT-300** CI tests for the spam classifier and the categoriser MUST
  use a deterministic `Replayer` (package `internal/llmtest`) that reads
  pre-recorded fixture files. The `Replayer` MUST NOT make any network calls.
  Tests that require a live endpoint are gated behind `t.Skip` until fixtures
  are recorded.

- **REQ-FILT-301** The `Replayer` looks up responses by `(kind, prompt_hash)`
  where `prompt_hash` is the SHA-256 hex of the full concatenated prompt
  (system message + user content). Any change to the prompt text produces a
  different hash and causes the `Replayer` to return `ErrFixtureMissing`,
  surfacing a clear "regenerate fixtures" message rather than silently serving
  a stale response.

- **REQ-FILT-302** When no fixture matches a given `(kind, prompt_hash)`, the
  `Replayer` returns `ErrFixtureMissing` with the missing key embedded in the
  error string. The test runner surfaces this as a test failure that names the
  affected kind, the hash, and points the developer at `scripts/llm-capture.sh`.

- **REQ-FILT-303** The capture script (`scripts/llm-capture.sh`) is operator
  tooling only; it MUST NOT run in CI. It requires `HEROLD_LLM_CAPTURE=1` and
  records fixture files under `internal/llmtest/fixtures/<kind>/<pkg>.jsonl`.
  Running the script without `HEROLD_LLM_CAPTURE=1` prints usage and exits 0
  without making any network calls. Fixtures are committed to the repository so
  CI can use the `Replayer` with no network dependency.

## Out of scope

- External AV (ClamAV, Sophos) integration. Operator writes a Sieve-compatible plugin if they want.
- Image OCR or attachment content inspection.
- Per-user fine-tuned models (operator problem).
- Shared reputation across servers / federation.
- Automatic model training from user feedback.
