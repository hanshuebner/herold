# ADR-0002: Generated spam filter -- the LLM as compiler, not as classifier

- Status: Accepted (maintainer, 2026-07-13)
- Date: 2026-07-13
- Area: server -- spam filtering, plugins, suite settings
- Related requirements: REQ-FILT-01..02 (verdict shape), REQ-FILT-13 (classifier
  is a replaceable plugin), REQ-FILT-22 / REQ-FILT-65..68 (prompt transparency),
  REQ-FILT-30..32 (what reaches the LLM), REQ-FILT-70..72 (feedback, no
  retraining), REQ-FILT-100..102 (Sieve seam), REQ-PLUG-30..34 (JSON-RPC wire),
  REQ-PLUG-40..44 (plugin isolation), REQ-PLUG-70..72 (ABI is a hard contract),
  REQ-PROTO-65 (spamtest)
- Scope statements in tension: G5 (LLM-first spam), NG4 (traditional spam
  filtering)
- Depends on: ADR-0003 (plugins as first-class extensions). This filter is a
  plugin, and it needs all three capabilities that ADR proposes -- lifecycle,
  per-principal state, and a UI surface. Without them it cannot be one.

## Context

Today every inbound message costs one LLM call (REQ-FILT-10, `herold-spam-llm`).
That buys good judgement at a price that is paid per message, forever: a 5s
timeout budget (REQ-PLUG-32) on the delivery path, a nondeterministic verdict, an
endpoint the operator must trust with mail content (REQ-FILT-60), and a reason
string that is a plausible narrative rather than an auditable decision.

This ADR proposes moving the LLM off the delivery path and into a **compile
step**: it authors the filter once, from a natural-language policy the user
writes, and the filter it emits is then evaluated in pure Go, per message, in
microseconds, with no network call and no mail content leaving the process.

### Evidence

Measured on the `imap-cleaner` evaluation corpus (1,000 real messages from a
production mailbox; see that repo's `eval/`):

- A logistic regression over 44 hand-written header features -- SPF/DKIM/DMARC
  results, From/Reply-To/Return-Path/Message-ID consistency, Received chain
  shape, List-* headers, subject typography -- reaches **AUC 0.992** in
  cross-validation and **0.998** on a temporal holdout (train on the past,
  classify the future). Hashed character n-grams over From and Subject add about
  four points.
- The features are pure string handling over the header block. No model file, no
  DNS lookup, no network, no CGO.

Two caveats that bound the claim, and they are about the corpus rather than the
method:

- **The false-positive side is not yet certified.** The metric that matters is
  spam recall at a false-positive rate at or below 0.5%, and by the rule of three
  that needs roughly 600 confirmed ham. The temporal holdout had 24. Zero false
  positives out of 24 bounds the FPR at 12%, which is not the claim we need.
  Human ground truth is being collected.
- **Corpora leak.** The mailbox's own SpamAssassin had written its verdict into
  the headers of the very mail the classifier was asked to judge, and a
  `DKIM-Filter` header dated each message finely enough to stand in for the
  folder it ended up in. Both had to be stripped before any number above meant
  anything. Any future evaluation harness must assume this class of leak exists
  and hunt for it.

### Why a compiled filter and not simply a cheaper model

The per-message spend is not the problem; at this volume it is noise. The
problems the compile step actually solves:

- **Privacy.** At compile time the model sees the user's own policy prose and a
  schema. No correspondent's address, subject or body is in the payload -- ever.
  REQ-FILT-60's warning ("your inbound mail content is sent to this endpoint")
  stops applying, and with it the whole jurisdiction question.
- **Determinism.** The same message gets the same verdict twice. Today it does
  not: a classifier sampling at a nonzero temperature can file the same mail two
  ways on two runs.
- **Latency.** Microseconds against a 5s timeout budget, with no
  accept-on-failure degradation path (REQ-FILT-40) because there is nothing to
  fail.
- **Auditability.** REQ-FILT-66 promises the user an explanation. An LLM's
  `reason` string is a post-hoc rationalisation that cannot be checked against
  what the model actually did. A rule trace -- these rules fired, they
  contributed these scores, the total crossed the threshold -- is the decision
  itself.

## Decision

Three artefacts, one of which the LLM writes.

```
  policy.md  --[ LLM, on demand, no mail content ]-->  rules.json
  feedback   --[ logistic regression, no LLM      ]-->  weights.json
  message    --[ pure Go, no network              ]-->  score -> verdict
```

    score(msg) = SUM { r.score : r.when(msg) }  +  w . phi(msg)
    verdict    = spam if score >= threshold else ham

`phi` is the compiled feature extractor (in-tree Go, fixed at release).
`rules.json` is generated from the user's natural-language policy. `weights.json`
is trained from the feedback records REQ-FILT-70 already collects. The threshold
is recalibrated after any change to either, against the user's own labelled mail,
so the false-positive budget survives a policy edit.

The verdict feeds `${spam.verdict}` / `${spam.confidence}` and the `spamtest`
mapping exactly as today (REQ-FILT-100, REQ-PROTO-65). **Sieve is untouched.**
This DSL scores; Sieve routes. They do not overlap: Sieve has tests and actions
and no notion of accumulating a weight, and this language has weights and no
notion of an action.

### Where the work runs

**All of it is one plugin**, `herold-spam-filter`: feature extraction, rule
evaluation, scoring, weight training, and the settings panel. Core gains no spam
logic at all.

| Concern | Mechanism |
|---|---|
| Scoring a message | `spam.classify`, with a payload that carries the full header block |
| Policy -> rules compilation | `spam.compile` -- a new method; the only one that talks to an LLM, and it sees no mail |
| Weight training | inside the plugin, from feedback the server forwards |
| Policy, rules, weights | ADR-0003 per-principal plugin state (`state.get` / `state.put`) |
| Settings UI, rule table, preview diff | ADR-0003 `settings.panel` view tree |
| Feedback (Junk moves) | `spam.feedback`, a new server-to-plugin notification over the records REQ-FILT-70 already collects |

A plugin is the right home for all of it, on three counts:

- **Scope.** NG4 already says operators wanting a rule engine or a Bayesian
  classifier "can write a plugin". This is the sanctioned home for exactly this.
- **Cost.** herold targets 10,000 inbound messages per day -- about 0.12 per
  second. An NDJSON round trip over stdio carrying a few kilobytes of headers is
  not measurable against a 5s classification budget (REQ-PLUG-32). A process hop
  that keeps spam logic out of core is bought cheaply.
- **Blast radius.** A scorer that evaluates model-generated expressions over
  hostile input belongs behind the process boundary REQ-PLUG-40..44 already built,
  not inside the mail server.

The one thing the plugin boundary does not currently supply is the message itself.
`spam.classify` carries `from`, `to`, `cc`, `subject`, `received_date`, three auth
booleans, `from_domain` and `body_excerpt` -- and the features this design rests on
are not in it: no Received chain, no Message-ID, no Return-Path, no Reply-To, no
`List-*`, no Content-Type, no User-Agent. The payload has to carry the raw header
block, which ADR-0003's data grants provide (see "What this contradicts", item 3).

The LLM stays out-of-process, which is what invariant 2 is protecting, and it is
called once per *policy edit* rather than once per message.

## The rule language

A rule is a score, a condition, and a sentence explaining which line of the user's
policy produced it. The condition is **CEL** -- the Common Expression Language:

```toml
[[rule]]
id      = "member-domain-authenticated"
score   = -6.0
because = "policy line 3: mail from member domains is ham"
when    = 'from.registrable in lists.members && auth.dmarc == "pass"'
```

### Why CEL

This is the one place where a wrong choice is expensive, because the language is
simultaneously written by a model, read by a user, and evaluated on untrusted
input. CEL is the only candidate that is good at all three:

- **It is the industry's answer to exactly this problem.** Kubernetes admission
  policy, Envoy RBAC, and Firebase security rules all embed CEL for the same
  reason: a user-supplied predicate over a structured object, evaluated in a
  server that must not hang. Nothing here is novel, which is the point.
- **Non-Turing-complete, by design.** There are no unbounded loops and no
  recursion, so **evaluation terminates as a property of the language** rather
  than because we wrapped it in a CPU cap and hoped. That is a materially stronger
  guarantee than a hand-rolled interpreter can offer, and it is why CEL exists.
- **It has a real type checker.** The environment declares the fields and their
  types; a ruleset referencing `from.registerable` (sic), or comparing a string to
  a number, fails to compile and is rejected before it can ever run. This is the
  primary defence against a model that invents a field, and it is enforced by a
  checker we did not write.
- **Models emit it reliably**, because their training data is saturated with it --
  every Kubernetes `ValidatingAdmissionPolicy` in the world is a worked example.
- **Users can read and hand-edit it.** `from.registrable in lists.members` needs no
  explanation, and a filter the user is asked to trust is one they must be able to
  read. Legibility is a safety property here, not a courtesy: the whole
  accountability story rests on a person being able to look at a generated rule and
  see what it does.

Dependency justification, per STANDARDS section 3: `github.com/google/cel-go`,
Apache-2.0, Google, actively maintained, pure Go and CGO-free. It pulls in
`antlr4-go/antlr` (BSD-3) for its parser; `protobuf` is already in the tree, so
that side costs nothing. Two direct dependencies against a budget of fifty, in
exchange for not writing -- and not fuzzing, and not maintaining -- a lexer, a
parser, a type checker, and a terminating evaluator.

### The environment

CEL evaluates against a typed object. This is the whole surface a rule can touch;
there is nothing else in scope, and the type checker enforces it.

| Field | Type | Meaning |
|---|---|---|
| `header("Subject")` | `string` | First occurrence, unfolded; `""` when absent |
| `headers("Received")` | `list<string>` | All occurrences |
| `has_header("List-Id")` | `bool` | Present at all |
| `from`, `to`, `reply_to`, `return_path` | `Address` | `.addr`, `.domain`, `.registrable`, `.tld`, `.local` |
| `auth.spf`, `auth.dkim`, `auth.dmarc` | `string` | `pass` / `fail` / `softfail` / `none` |
| `lists.<name>` | `list<string>` | A named list defined alongside the rules |
| `feature.<name>` | `double` | The learned model's feature vector, by name |
| `subject`, `body_excerpt` | `string` | Subject to hand; body only if granted (see below) |

CEL supplies the operators -- `&&`, `||`, `!`, `==`, `in`, `.matches()`,
`.startsWith()`, `.contains()`, `.size()`, arithmetic, comparison -- so none of
them have to be specified, implemented, or fuzzed here.

`feature` is the hinge of the design. It lets a generated rule reach the same
vector the learned model consumes, so "be harsher on subject lines that shout"
compiles to `feature.subj_upper_ratio > 0.6` rather than to a regex somebody has to
maintain forever. The registry is compiled into the plugin and versioned; a ruleset
naming a feature this build does not have fails the type check, which is what stops
a stale ruleset surviving a plugin upgrade.

`.matches()` is RE2 -- linear time, no backtracking, no catastrophic blowup. That
is a property of Go's `regexp`, not a limit we impose.

### The document

TOML, because it is what herold already speaks (`system.toml`,
`pelletier/go-toml/v2`) and because a user who opens this file should recognise it.
It is JSON on the wire and in the plugin state store; TOML is the face it presents
for reading, export, and hand-editing.

```toml
schema    = 1
threshold = 4.0

[generated]
policy_sha256 = "9f2c..."
model         = "claude-haiku-4.5"
temperature   = 0
at            = "2026-07-13T09:14:22Z"

[lists]
members    = ["classic-computing.de", "vzekc.org"]
cheap_tlds = ["top", "beauty", "shop", "homes", "icu", "xyz"]

[[rule]]
id      = "member-domain-authenticated"
score   = -6.0
because = "policy line 3: mail from member domains is ham"
when    = 'from.registrable in lists.members && auth.dmarc == "pass"'

[[rule]]
id      = "forged-member-domain"
score   = 8.0
because = "policy line 3: a forged From: using a member domain is spam"
when    = 'from.registrable in lists.members && auth.dmarc != "pass"'

[[rule]]
id      = "cheap-tld-unauthenticated"
score   = 3.5
because = "policy line 5: throwaway domains that cannot authenticate"
when    = 'from.tld in lists.cheap_tlds && auth.dkim != "pass"'

[[rule]]
id      = "two-factor-code"
effect  = "force_ham"
because = "policy line 7: one-time codes are never spam"
when    = '''auth.dkim == "pass" &&
             subject.matches("(?i)\\b(one[- ]time|verification|2fa)\\b")'''
```

`effect` defaults to `score`. `force_ham` and `force_spam` short-circuit the sum,
because some policy statements are absolute -- "a one-time code from my bank is
never spam" is not a large negative weight, it is a floor -- and expressing an
absolute as a big number is exactly the thing that quietly stops holding once the
other weights move. They are the escape hatch, and a ruleset that leans on them is
a ruleset whose policy needs rewriting.

Weights ship separately, because they change on a different clock -- feedback,
continuously -- than the rules do, which is policy edits, rarely:

```toml
schema     = 1
bias       = -1.2
trained_at = "2026-07-13T09:14:22Z"
n_examples = 1432

[weight]
"auth.spf.pass"     = -1.83
"tld.cheap"         =  1.11
"subj.upper_ratio"  =  0.94
```

### Safety limits

The evaluator takes a model's output as input, so it is bounded rather than
trusted. CEL discharges most of this for us -- termination, the type check, RE2 --
and what remains is arithmetic:

- Rule count capped (256); compiled CEL programs are cached per ruleset, so
  evaluation is O(rules) of pre-compiled programs, with no parsing on the hot path.
- CEL's own cost estimator bounds each expression at compile time; a rule whose
  estimated cost exceeds the budget is rejected at compile time rather than
  interrupted at evaluation time.
- No I/O, no clock read, no DNS. A rule is a pure function of the message and the
  lists.
- Per STANDARDS section 8, the ruleset decoder gets a fuzz target with a seed
  corpus under `testdata/fuzz/`. The *expression* parser does not need one: it is
  cel-go's, and fuzzing someone else's maintained parser is not our job.

## Regeneration is a runtime, user-level operation

The generated ruleset is **not** a build artefact and is **not** committed. The
user edits their policy in the suite and recompiles; nothing rebuilds, nothing
redeploys. That forces two consequences:

- **Rules are interpreted data, not generated Go.** There is no compiler on the
  user's machine. This is why the DSL exists at all.
- **The regression gate cannot be CI.** It is a backtest instead, and herold is
  in the rare position of having the corpus that matters already on disk: the
  user's own mail. A candidate ruleset is scored against a sample of their INBOX
  and Junk before it is ever activated, and the user is shown what would change.

The compile flow, all of it inside the plugin, brokered by `ui.action`:

1. User edits the policy and hits Apply. This is the slot REQ-FILT-22 and
   REQ-FILT-67 already define as the user-visible, user-mutable prompt.
2. Plugin calls its LLM endpoint with the policy text, the DSL schema, and the
   feature registry. **No message data is in the payload.**
3. Plugin validates what comes back: JSON schema, operator arity, types, feature
   names, limits. Invalid -> rejected, nothing changes, the user is told why.
4. Plugin backtests the candidate against its retained corpus (below) and
   recalibrates the threshold to hold the false-positive budget.
5. Plugin returns a `diff` node: "3 messages would change verdict", listed.
6. User accepts. The ruleset is written to per-principal plugin state and
   activated. The previous one is retained, so rolling back is a click.

Step 5 is the load-bearing one. It is what makes natural-language configuration a
repeatable engineering operation rather than a wish: the user sees the consequence
before it is real, against their own mail.

### Where the backtest corpus comes from

A backtest needs the user's mail, and a plugin has no mailbox access -- nor should
it acquire any. Granting plugins a "read the mailbox" RPC to serve this one flow
would be a far larger concession than the feature is worth.

It does not need one. **The plugin already sees every inbound message**, via
`spam.classify`. It can retain its own bounded corpus in plugin state:

- **Headers, last N messages** (N a few hundred) -- needed in full, because a rule
  may reference any header via `["header", "X"]`, so feature vectors alone cannot
  backtest a *new* rule. This is the backtest corpus and the reason it is bounded.
- **Feature vectors plus labels, full history** -- roughly 350 bytes a message, so
  years of it is cheap. This is what trains the weights.

Labels come from `spam.feedback`: the user moving mail into or out of Junk is the
correction signal, and REQ-FILT-70 already records it.

This keeps mail inside the two processes that already handle it and adds no new
data path -- but it does mean the plugin state store holds message headers, which
is a fact an operator deserves to know and a retention policy has to name.

## Suite integration

A `settings.panel` view tree (ADR-0003 tier 1), rendered into the suite's settings
surface. The whole panel fits the v1 component vocabulary, which is not a
coincidence -- this filter is the plugin the vocabulary was sized against:

- **Policy** -- a `textarea`. This is REQ-FILT-65's "user-visible prompt currently
  in effect", made editable. Operator guardrails (REQ-FILT-67) stay invisible and
  are not part of what gets compiled.
- **Apply** -- a `button` dispatching the compile action, whose response is a
  `diff` node: the preview. Accept or discard; nothing changes until accept.
- **Active rules** -- a `table`: rule, score, and the `because` sentence pointing
  back at the policy line that produced it. The user sees the filter, not just the
  prompt that made it. Individual rules can be disabled without editing the policy.
- **Lists** -- the named lists (`members`, and whatever the policy introduced) as
  `chips`. This is where most day-to-day tuning will actually happen, and it needs
  no LLM call at all.
- **Why was this filed?** -- a `keyvalue` on the `message.detail` surface: which
  rules fired, what each contributed, what the learned model contributed, and where
  that landed relative to the threshold. This discharges REQ-FILT-66's promise with
  a decision instead of a narrative. It is also the only part of this panel that
  needs a second ADR-0003 surface, and ADR-0003 flags `message.detail` as the one
  extension point with a single known caller -- which is this. If it is cut from
  ADR-0003 v1, the trace lives in the settings panel keyed by Message-ID until a
  second consumer appears.

State -- policy text, ruleset, weights -- lives in ADR-0003's per-principal plugin
store, not in a bespoke table. That is the point of ADR-0003: core does not learn
this plugin's schema.

## What this contradicts, and what has to change

Stated plainly, because none of it can be quietly assumed:

1. **G5 ("LLM-first spam. No rule engine, no Bayesian... One classifier call per
   message") and NG4 ("No bundled rule engine. No Bayesian.").** This filter ships
   **first-party**, in `plugins/herold-spam-filter/` and in REQ-PLUG-61's bundle,
   so both statements need amending. Decided 2026-07-13.

   G5 bought simplicity, and the bill it left is a network call, a 5s timeout, a
   nondeterministic verdict, and mail content leaving the machine on every single
   message. A filter that avoids all four should be what a new install gets, not
   something users have to go and find. NG4's escape clause ("operators who want
   these can write a plugin") was written to keep herold out of the rule-engine
   business; it reads oddly once herold is the one shipping the plugin, and the
   honest move is to amend it rather than to shelter behind it.

   The amendment is narrower than it first looks. G5's real content -- "the LLM
   decides what spam means, and the operator picks the endpoint" -- survives
   intact. What changes is *when* the model is asked: once per policy edit rather
   than once per message. The scope statements should say that, because it is a
   better statement of the intent than the one they currently carry.

2. **REQ-FILT-72 ("No automatic re-training of the model. That's outside our
   scope; we're a mail server, not a training pipeline.").** Fitting a logistic
   regression over feedback records is not a training pipeline in the sense that
   REQ was refusing -- no GPU, no model file, no fine-tune; a few hundred
   multiply-adds over records REQ-FILT-70 already stores. And it happens inside a
   plugin, so herold core is not the thing doing it. But that reading is a lawyer's
   reading, and the REQ should be amended to say what is actually meant: herold
   does not train models; plugins may fit their own.

3. **The `spam.classify` payload.** It must carry the raw header block: the
   Received chain, Message-ID, Return-Path, Reply-To and `List-*` headers are where
   most of the measured signal lives, and REQ-FILT-30..32 curate all of them away.
   This is handled by ADR-0003's **data grants** rather than by a bespoke exemption
   -- the plugin declares `headers.all` as required, and the operator confirms it at
   install. The rationale REQ-FILT-31 gives for the ban ("LLM prompts are data
   leakage surfaces") is a statement about an *egress*, and grants put the control
   at the egress: this filter never opens a socket, and the operator can see that
   in the same screen where they grant it the headers. Without ADR-0003, this is a
   hard blocker and nothing else here is buildable.

4. **A new plugin method, `spam.compile`,** and a new server-to-plugin
   notification, `spam.feedback`. Both additive. `herold-spam-llm` is the natural
   home for `spam.compile` to grow into.

## Consequences

The LLM path does not go away. It becomes the **abstain band**: messages whose
score lands close to the threshold can still be escalated to `herold-spam-llm`,
which keeps the quality of a reasoning model on the genuinely hard mail while
deciding the other ~95% locally, deterministically, and for free. An operator who
wants today's behaviour sets the band to cover everything and has it.

A user with no LLM endpoint configured at all still gets a working filter: default
rules ship compiled into the binary, and the weights train from their own Junk
moves. LLM configuration becomes an enhancement rather than a dependency, which
is a strictly better default posture than a spam filter that does nothing until an
endpoint is reachable.

The cost is a feature registry that is now a compatibility surface: `phi` is
referenced by name from stored rulesets and stored weights, so removing or
renaming a feature breaks both. It gets versioned and it gets the same
forward-only discipline as a DB migration.

## Open questions

- **Ground truth comes first.** None of the numbers above are final until the
  evaluation corpus has human labels: the deciding metric needs roughly 600
  *confirmed* ham and the corpus does not yet have them. No filter code is written
  before that is done.
- ~~Backtest corpus: how many messages, retained how long?~~ **Resolved
  2026-07-13** (maintainer, via ADR-0004): **there is no backtest corpus.** The
  scorer runs in core, so the backtest runs in core too, against the live mailbox
  in the store -- nothing is copied, nothing is retained, and the retention
  obligation this question was worried about never arises. The backtest is **full,
  bounded by a configurable limit** (default 200 000 messages, most recent first);
  when the limit binds, the preview states its coverage. Sampling was the answer
  from the era when scoring meant an LLM call per message; a compiled ruleset
  scores in microseconds, so the cost that justified sampling is gone, and with it
  the recency and class-imbalance biases that would have made the preview diff a
  confident lie. See REQ-FILT-226..229, which also blind the evaluator to its own
  prior output (`$category-*`, `$Junk`, `X-Spam-*`) during a backtest -- the same
  leakage this ADR found in the imap-cleaner corpus, arriving from inside the house.
- Cold start: a new principal has no feedback and no retained mail. Ship default
  weights trained on an aggregate corpus, or run rules-only until enough feedback
  accumulates?
- Where does the abstain band's width get configured, and by whom -- operator or
  user?
