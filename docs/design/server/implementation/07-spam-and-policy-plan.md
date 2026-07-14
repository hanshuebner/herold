# Wave 4.x -- Spam and categories: fix the default, then build the classifier plugin

**Drafted:** 2026-07-14. **Predecessor:** ADR-0002 (revised on measurement),
ADR-0003 (plugin extensions), ADR-0004 (unified categories). **Driver:** the
measured evaluation in `imap-cleaner/eval/` -- 3,631 human-labelled messages from a
production mailbox. **Goal:** ship a spam filter that works, on evidence, and stop
shipping one that does not.

This file is the planning artefact. It does not edit the requirements or the
architecture; it points at the REQ IDs that exist, names the ones that must change,
and identifies the implementation work each wave owes.

## What the measurement changed

| Classifier | recall @ FPR <= 0.5% (hard subset) | false positives / 560 ham |
|---|---|---|
| Claude Haiku 4.5 | 65.7% | 0 |
| GLM-4.7-flash | 43.0% | 0 |
| Nova Lite | 25.0% | 2 |
| **`llama3.2:3b` -- today's default** | **4.4%** | **29.8% FPR at threshold 80** |

The LLM stays the classifier, because nothing else measured works -- not because an
LLM is what we wanted. A mechanism that classified and categorised without a model
call would be better on cost, latency, determinism and privacy, and we would take it.
The learned filter proposed in the first draft of ADR-0002 was measured at 52% and is
not built. **So the LLM is a fallback, and the job of this plan is to keep it
replaceable.**

Which is why the shape is now: **one classifier plugin, one call, both answers, and
the plugin owns the rules.** Spam and category are the same question. The server asks
it once -- after SMTP DATA, and on IMAP pull -- and gets back a verdict and a category.
What a rule *is*, how it is written, how it is edited, how it is evaluated: all of that
is inside the plugin. The server stores rules at three scopes and hands them over.

**Wave 4.1 exists because the shipped default misfiles one real message in three. That
is a bug, not a design debate.**

The measured configuration is **Claude Haiku 4.5 via the Anthropic API**, operator-
configured. Local models are deferred: nothing that runs on a CPU-only mail server was
measured to work, and we are not looking further for now.

## Wave order and dependency graph

```
4.1 stop the harm            (withdraw default model, pin temperature, off-by-default)
  |
  |  ---- independent of the rest; ship first, ship alone ----
  |
4.2 scoped rule store        (ADR-0003 state, extended: principal | domain | server)
  |
4.3 the classifier contract  (mail.classify -> verdict + category; one plugin type)
  |
4.4 delivery integration     (SMTP DATA and IMAP pull; Sieve seam; categories)
  |
4.5 first-party plugin       (Haiku; rules, NL compilation, and the editor live here)
  |
4.6 suite settings panel     (ADR-0003 tier-1 view tree, supplied by the plugin)
  |
4.7 evaluator                (operator points it at their own mailbox)
```

**What is not here any more:** the server-side CEL rule engine and the policy compiler,
which were waves of their own in the previous draft. Rules moved into the plugin, and
they took `cel-go` and `antlr` with them -- the server gains no dependencies from this
plan. A plugin may still use CEL; that is now its business, not the server's ABI.

---

## Wave 4.1 -- Stop the harm

Independent of everything else in this plan and shippable on its own.

**Work**

- **Withdraw the default model.** REQ-FILT-11 currently recommends `llama3.2:3b`.
  Delete the recommendation. There is no default model.
- **Off unless configured.** An operator with no configured classifier gets spam
  filtering **disabled**, and a startup log line and an admin-visible status saying
  so. A filter that does nothing is a nuisance; one that misfiles a third of real
  mail is a catastrophe, and today's default is the second kind.
- **Pin `temperature = 0`** in `herold-spam-llm` and in the classifier contract. The
  incumbent samples at the provider default, so the same message can be filed two ways
  on two runs. A classifier's verdict must be reproducible.
- **Publish the evidence** in the operator docs: the table above, and what it costs to
  run each option. An operator choosing an endpoint is making a quality decision and
  a privacy decision at the same time, and currently has data for neither.

**REQ changes:** REQ-FILT-11 (default model withdrawn), REQ-FILT-12 (temperature
pinned, not merely configurable), new REQ for the disabled-when-unconfigured state.

**Acceptance:** a fresh install with no classifier configured delivers mail with
`X-Spam-Verdict: unknown`, files nothing into Junk, and says why in the admin status
endpoint. `herold admin plugin test <name>` reports the model's verdict on a canned
message, and refuses a plugin whose manifest does not pin temperature.

**Risk:** none. This wave only removes a behaviour that is doing damage.

---

## Wave 4.2 -- The scoped rule store

The server's entire contribution to rules: keep them, scope them, and say who may
write them. It never parses one.

**Work**

- **Extend ADR-0003's plugin state from one scope to three.** Today it is
  per-principal (`state.get` / `state.put` / `state.list`). It gains `server`,
  `domain`, and `principal`:

  ```
  state.get(scope, key)           scope: "server" | "domain:<name>" | "principal:<id>"
  state.put(scope, key, value)
  state.list(scope, prefix)
  ```

- **The values are opaque bytes.** Herold stores, versions, and backs them up. It does
  not know a rule from a cache entry, and must not grow an opinion about the format.
- **Authorisation is the server's job and is not delegable.** A principal may write
  only its own scope. A domain administrator may write its domain's, and no other's.
  The operator may write `server`. A plugin asking for a scope its caller may not write
  gets an error, not a silent no-op.
- **The classifier reads all three on every call** and combines them itself -- whether a
  principal may relax a server rule, whether the strictest wins, whether they
  concatenate, is classifier semantics and belongs on the classifier side of the
  boundary.
- Reads on the delivery path must not hit the database per message. The plugin caches;
  the server publishes an invalidation event on write.

**REQ changes:** the ADR-0003 state requirements gain the scope parameter. New REQs for
the write-authorisation rule.

**Acceptance:** table tests for the authorisation matrix -- every (caller, scope) pair,
allowed or denied, with the denials proving a principal cannot write `server` or another
principal's scope even by naming it directly. State survives a plugin upgrade and is
purged on plugin removal (ADR-0003 lifecycle, unchanged).

---

## Wave 4.3 -- The classifier contract

One plugin type, `classifier`. One method.

**Work**

- **`mail.classify(message, context) -> { verdict, confidence, reason, category }`.**
  Replaces `spam.classify` (REQ-FILT-13) and the categoriser both. There is no second
  classification mechanism in the server.
- `context` carries what the plugin needs and the operator granted: the principal, the
  recipient domain, and **the principal's stored category set** (REQ-FILT-210) -- so the
  plugin names a category the user owns rather than inventing vocabulary, which is the
  defect ADR-0004 removed when it killed `derivedCategories`. A plugin naming a category
  outside the set is ignored and logged.
- `category` is optional. Empty means uncategorised, which is legal (REQ-FILT-230).
- **How the plugin answers is opaque.** One model call, two, rules only, no model at
  all. The server must not encode an assumption either way -- that indifference is what
  makes a future non-LLM classifier a plugin swap rather than a redesign.
- One data grant (ADR-0003) covers the call, because there is one payload. The operator
  confirms once, at install, and reads one line about what the plugin sees.
- Failure is degrade-open, unchanged (REQ-FILT-40): a timeout or a crash delivers the
  mail unjudged and uncategorised. A classifier must never be able to lose mail.

### The structural fallback categoriser

The one piece of classification the server keeps for itself, and it stays small on
purpose.

- `List-Id` or `List-Unsubscribe` present -> `forums`; `Precedence: bulk` ->
  `promotions`; `Auto-Submitted` -> `updates`. Nothing else. **No rule language, no
  configuration, no LLM.**
- It runs only where the plugin is silent -- an empty `category` in the response, or no
  classifier plugin installed at all. **The plugin's category always wins.** The fallback
  fills silence; it does not compete.
- It exists so the tab strip works on a herold with no model configured (REQ-FILT-214).
  It works at all only because categorisation is not adversarial -- nobody forges a
  `List-Id` to look more like a mailing list -- which is exactly why there is no
  equivalent fallback for the spam verdict.
- **If it ever grows a rule language, it has become the second classification mechanism
  this plan removed.** That is the line.

**REQ changes:** REQ-FILT-13 rewritten against the new method. REQ-FILT-213 withdrawn
(no server-side compilation of category definitions). REQ-FILT-214 retained in reduced
form: the structural fallback above, not a compiled ruleset. REQ-FILT-215 (categories
share the spam rule language) withdrawn -- there is no server-side rule language to
share.

**Acceptance:** a deterministic test classifier plugin returning a fixed
`{verdict, category}` drives the integration tests on both store backends -- something
the direct HTTP call in `internal/categorise` cannot do today. A plugin naming a
category outside the principal's set is ignored and logged. A plugin returning an empty
category gets the structural fallback's answer; a plugin returning a category overrides
it. With no plugin installed, a list message still lands in `forums`. A plugin timeout
delivers the mail.

---

## Wave 4.4 -- Delivery integration

**Work**

```
SMTP DATA ---\
              >-- mail.classify --> verdict + category --> Sieve --> store --> 250 OK
IMAP pull ---/
```

- The call happens at exactly those two points: **after the SMTP DATA phase**, and when
  a message is **pulled in over IMAP** from a remote account. Nowhere else. A message
  that enters the mailbox is classified once.
- **The call is synchronous and inside the SMTP transaction, before `250 OK`.** The
  verdict is known before the message is stored, so Sieve can test it and no message ever
  lands in the inbox and then moves under the user's cursor.
- **A hard timeout bounds the transaction.** The sending MTA is waiting, so the classify
  call gets a budget (default 5s, operator-settable); on expiry the verdict is `unknown`
  and the mail is delivered unjudged (REQ-FILT-40). A classifier must never lose mail and
  must never stall the queue. The timeout is a *delivery* guarantee and belongs to the
  server, not to the plugin -- the server enforces it even if the plugin ignores its own.
- **This makes model latency a delivery-path concern**, which is the price of the
  ordering. It is also the strongest argument for the plugin's rule layer: a message a
  rule settles never waits for a model at all.
- Verdict feeds `${spam.verdict}` / `${spam.confidence}` and the `spamtest` mapping
  exactly as today (REQ-FILT-100, REQ-PROTO-65). **Sieve is untouched.** The classifier
  decides; Sieve routes.
- **`\Junk` is exempt from categorisation** (ADR-0004): a `spam` verdict drops the
  category even if the plugin returned one.
- Routing is ADR-0004's, unchanged: **Sieve routes, categories classify, an explicit
  `fileinto` beats an inferred disposition.** Sieve gains the category as a test it
  could not see before.
- **`internal/categorise`'s direct HTTP call goes away.** Its per-account knobs move to
  the plugin; its LLM path is subsumed by `mail.classify`.
- The reason string reaches the API (REQ-FILT-66), and says whether a rule or a model
  produced it -- the plugin reports which, because only it knows.

**Acceptance:** an integration test on both store backends: a message arriving over SMTP
and the same message pulled over IMAP each invoke the classifier **exactly once** and
land with both a verdict and a category; a spam verdict files to `\Junk` with no
category even when the plugin returned one; a plugin that sleeps past the budget is cut
off, the mail is delivered unjudged, and `250 OK` is returned within the budget plus
overhead -- asserted with a clock, because "it never hangs" is not a claim a reader
should have to take on trust.

---

## Wave 4.5 -- The first-party classifier plugin

Everything the server gave up lives here: the rule format, the natural-language
compilation, the evaluation, and the editor.

**Work**

- **Claude Haiku 4.5 via the Anthropic API**, `temperature = 0`, the one configuration
  with evidence behind it (65.7% recall, zero false positives in 560 representative
  ham).
- **Rules, in a format the plugin defines.** It reads `server`, `domain` and
  `principal` scopes on every call and decides how they combine. Rules that settle a
  message do so without a model call, which is where the bill actually gets reduced.
- **Natural-language compilation**, on demand, from the user's own prose to the
  plugin's rule format. **No message data in the compile payload** -- the user's prose
  and a schema, nothing else. That is what makes natural-language configuration free of
  the privacy question, and it must not be quietly given up later for a "compile with
  examples" feature.
- **Findings the plugin must obey**, from the measurement (ADR-0002 records them; the
  server can no longer enforce them, having given up the rule language):
  - **Forgeable headers may not be evidence of ham.** `User-Agent`, `In-Reply-To`,
    `References`, a `Re:` subject -- all typed by the sender. `Re:` appears in 1% of
    spam and 37% of real ham and costs three characters to fake. The plugin's compiler
    rejects a generated `ham` rule that rests on them.
  - **Authentication does not fix that.** Spam authenticates perfectly: the spammer owns
    the domain and signs it. Tried, and it failed.
  - `registrable` needs a real public-suffix table. Taking the last two labels turns
    `foo.co.uk` into `co.uk`, and one British correspondent then vouches for every
    sender in Britain. (Observed in the eval corpus as 16 spam messages arriving from a
    "known" domain.)

**Acceptance:** the plugin scores at or above the table's Haiku row when run against the
`imap-cleaner/eval/` corpus. A rule that trusts `Re:` for ham fails to compile. Rules at
all three scopes are read, and a principal's rule cannot be made to affect another
principal's mail.

---

## Wave 4.6 -- Suite settings panel

ADR-0003 `settings.panel` view tree, tier 1. **No plugin JavaScript** -- the plugin
supplies a view tree, the suite renders it.

**Work**

- The rule editor's *content* is the plugin's, because the rule format is. The suite
  provides the surface, the widgets (`textarea`, `table`, `chips`) and the save path;
  the plugin decides what goes in them.
- **Policy** -- a `textarea`. This is REQ-FILT-65's "user-visible prompt currently in
  effect", made editable.
- **Apply** -- compile, validate, preview, accept. Nothing changes until accept.
- **Preview diff** -- "this would reclassify 3 messages", listed. The backtest runs
  against the live mailbox and must be blind to herold's own prior output (`$Junk`,
  `$category-*`, `X-Spam-*`) or it measures its own reflection (REQ-FILT-226..229).
- **Scope selector** -- a user edits their own rules; an administrator sees the domain
  and server scopes, and the server refuses the write if they are not entitled to it.
  The UI must not be the only thing enforcing that (Wave 4.2 does).
- **Why was this filed?** -- the rule that fired, or the model's reason.

**Acceptance:** driven end-to-end in a real browser via puppeteer against an ephemeral
instance, per `web/CLAUDE.md`. Screenshot of the preview diff in the closing comment. A
non-administrator cannot write the domain scope through the UI *or* around it.

---

## Wave 4.7 -- The evaluator, and the per-mailbox threshold

The operator can measure their own classifier on their own mail, because nobody should
have to trust our table. And the threshold is **calibrated per mailbox** from that
measurement rather than shipped as a constant.

**Work**

- `herold admin spam evaluate` -- score the configured classifier against a labelled
  sample of the operator's mailbox and print recall at the false-positive budget.
- **Calibration:** choose each principal's spam threshold as the lowest confidence that
  holds a 0.5% false-positive rate on *that principal's own judged ham*. Until there is
  enough of it, the threshold is the constant **80** and the plugin says so rather than
  pretending to a calibration it has not got.
- **"Enough" is about 600 judged ham**, by the rule of three: zero false positives on N
  ham bounds the true rate at roughly 3/N, so 600 is what a 0.5% budget costs. **No
  mailbox arrives with that**, and where it comes from is the open question of this wave
  -- Junk corrections accumulating over months, an onboarding pass where the user judges
  a sample, or the reply graph. Not the reply graph on its own: it is free and certain
  and 37 points optimistic, and a threshold set against it reports the filter as far
  safer than it is.
- The calibrated threshold is shown to the user with the evidence behind it ("held at 0.5%
  across 640 messages you judged"), not as a bare number they cannot evaluate.
- Labels come from the folder the mail sits in *plus* the corrections in the feedback
  records (REQ-FILT-70). **The tool must say loudly that folder priors are not ground
  truth**: in the eval corpus, 422 of the sampled INBOX messages were spam, and INBOX
  was 63% spam. An operator scoring against folder priors is scoring against a 30%
  error rate and will get a confidently wrong answer.
- Report the false-positive rate against ham the *operator judged*, not ham inferred
  from the reply graph. Reply-graph ham is free, certain, and the easiest ham there
  is; a threshold set against it reports the filter as safer than it is, by 37 points
  in the measured case.

**Acceptance:** run against the imap-cleaner corpus, reproduce the table at the top of
this file within noise. If it cannot reproduce a known result, it cannot be trusted on
an unknown one.

---

## Not in this plan

- **A server-side rule engine.** Rules are the plugin's. The server stores opaque bytes
  at three scopes and enforces who may write them, and that is the whole of its
  involvement. The cost is real and accepted: herold cannot validate a ruleset, cannot
  backtest one it cannot read, and cannot carry rules from one plugin to another.
- **The learned filter.** Measured at 52% against the LLM's 65.7%, needs per-mailbox
  training data forever, and none of what it learns transfers between mailboxes. The
  corpus and harness live in `imap-cleaner/eval/` if the argument is ever reopened --
  which it should be, if the no-GPU no-cloud operator becomes someone we care about,
  because for them 52% is the only measured option that beats nothing.
- **Local models.** Deferred, and not being pursued. `llama3.2:3b` fails outright at
  4.4%; GLM-4.7-flash needs ~19 GB to reach 43%. Nothing that runs on a CPU-only mail
  server was measured to work, so herold ships no local recommendation rather than one
  nobody can act on. The 8-14B band is untested; if the question is ever reopened, the
  argument is that a small mailbox's volume tolerates slow inference -- 17 messages a
  day allows 30 seconds a message -- so an 8B model clearing ~55% would put a free,
  private, GPU-free option back on the table.
