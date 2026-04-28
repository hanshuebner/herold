# 06 — Filtering: LLM spam classification and Sieve

*(Revised 2026-04-24: traditional filtering removed. LLM is the spam default.)*

Two stages, kept separate:

- **Classification** — decide whether a message is spam. Delegated to an LLM via an OpenAI-compatible chat-completions endpoint. Default endpoint: local Ollama. No rule engine, no Bayesian, no RBL/URIBL.
- **Delivery routing** — decide where an accepted message lands. Sieve scripts (global + per-recipient).

Email **authentication** (DKIM/SPF/DMARC/ARC) is upstream and unchanged — that's in `requirements/04-email-security.md`. Authentication results feed into the classifier prompt, they're not the classifier.

## Pipeline

```
accept → authenticate (SPF/DKIM/DMARC/ARC) → classify (LLM) → global Sieve → per-recipient Sieve → deliver
                                               └── verdict + confidence + reason
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

## Part C: Automatic categorisation (LLM)

Distinct from spam classification. Spam decides "deliver / spam / quarantine"; categorisation decides "Primary / Social / Promotions / Updates / Forums" (or whatever the user has configured). Both run on inbound mail; categorisation runs *after* spam (only mail that lands in inbox gets categorised — no point categorising spam).

Used by the suite's category tabs (`docs/design/web/requirements/05-categorisation.md`). Phase 2 — runs alongside the JMAP-suite work.

### Pipeline placement

- **REQ-FILT-200** Categorisation runs after Sieve `fileinto` decisions and after spam classification. Only messages whose final destination is the user's inbox (no Sieve fileinto, not classified spam, not auto-archived by user filter) are categorised. Mail that ends up in `\Junk`, `\Trash`, or a non-inbox label is NOT categorised.
- **REQ-FILT-201** The classifier output is at most one `$category-<name>` keyword applied to the `Email`. Names are lowercase ASCII, dash-separated. Default set: `$category-primary`, `$category-social`, `$category-promotions`, `$category-updates`, `$category-forums`.
- **REQ-FILT-202** Messages that don't match any category fall through with no `$category-*` keyword set. The suite's UI treats absence as "Primary".
- **REQ-FILT-203** Categorisation runs once at delivery; subsequent edits to the message do not re-trigger it. Re-classification is explicit (REQ-FILT-220).

### Configuration

*(Revised 2026-04-28: the prompt is the single source of truth. The
manually-editable category list is gone; categories are derived from
the LLM's response. Old REQ-FILT-210/212 references to a separate
category-set editor are obsolete.)*

- **REQ-FILT-210** **Removed.** The per-account category set is no longer separately configured. The prompt (REQ-FILT-211) defines the categories; the LLM enumerates them in every response (REQ-FILT-215); the server persists the latest enumeration as `derivedCategories` (REQ-FILT-217). There is no manually-editable category list.
- **REQ-FILT-211** Per-account classifier prompt: free text. This is the single source of truth for categorisation. The default prompt enumerates Gmail-style categories (Primary, Social, Promotions, Updates, Forums) and instructs the LLM to return JSON of the shape REQ-FILT-215 specifies. Mutable.
- **REQ-FILT-212** A "reset to default" control reverts the prompt only.

### Classifier endpoint

- **REQ-FILT-213** Categorisation calls the same kind of OpenAI-compatible HTTP endpoint as the spam classifier (REQ-FILT-15..23) but is its own per-account configuration. Operators can point them at the same endpoint or different ones; the spam classifier may run on a tighter, faster model than categorisation.
- **REQ-FILT-214** The categorisation call carries: the prompt, the message envelope summary (From, To, Subject), the first ~2 KB of the plain-text body. Same privacy posture as the spam classifier (REQ-FILT-30..33). Headers like `List-ID`, `Authentication-Results`, and `List-Unsubscribe` are included as features.
- **REQ-FILT-215** The classifier returns JSON of the shape `{ "categories": [<name>, ...], "assigned": <name> | null }`. `categories` enumerates every category the prompt defines (the LLM's interpretation of the prompt); `assigned` is the chosen category for this message, or `null` if no category fits. The server applies `$category-<assigned>` as a keyword (REQ-FILT-201) when `assigned` is non-null; `null` falls through with no keyword (REQ-FILT-202). Names in either field are lowercase ASCII, dash-separated; the server lowercases and slug-normalises any name the LLM returns. Unparseable JSON or a missing `assigned` field after one retry → no keyword applied, log a warning. Failures (timeout, 5xx) → no keyword applied, log a warning, mail is delivered uncategorised.
- **REQ-FILT-216** Transparency (G14): the categoriser honours the same contract as REQ-FILT-65..67. Per-account read returns the user-visible prompt (the editable text from REQ-FILT-211) plus the latest `derivedCategories` (REQ-FILT-217). Per-message read returns the assigned category and the user-visible prompt as applied to that message; operator guardrails are excluded.
- **REQ-FILT-217** **Derived category set.** The server persists a per-account `derivedCategories` list, sourced from the most recent successful classifier response's `categories` field. On prompt change (REQ-FILT-211 mutation), `derivedCategories` is invalidated and refilled from the next successful classifier call. The list is exposed via JMAP `CategorySettings/get` and is **read-only** to the user (the prompt is the lever; the list is a consequence of it). The suite's inbox tab strip reads `derivedCategories` to know what tabs to render. If the list is empty (no classifier call has succeeded since the most recent prompt change), the suite shows no category tabs and treats every message as Primary.

### Re-classification

- **REQ-FILT-220** Operator + the suite expose "re-categorise inbox" as an admin action: re-run the classifier on the user's recent inbox (last N messages, configurable; default 1000) under the current prompt and category set. Slow operation; runs as a background job with progress reporting.
- **REQ-FILT-221** When the user manually changes the `$category-*` keyword on an `Email/set` (e.g. moves a message from Promotions to Primary), the change is persisted and the action is recorded for prompt-tuning feedback. Mechanism for using that feedback to refine the prompt is operator-side (out of scope here; a future "feedback-driven prompt update" workflow lives in the suite's settings or admin tooling).

### Failure isolation

- **REQ-FILT-230** Categorisation failures NEVER block delivery. A failed classifier call leaves the message uncategorised; the message lands in inbox normally.
- **REQ-FILT-231** Categorisation MUST NOT modify any message header or body. The only persistent effect is the `$category-*` keyword.

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
