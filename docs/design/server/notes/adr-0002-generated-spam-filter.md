# ADR-0002: Spam and category are one plugin call, and the plugin owns the rules

- Status: Accepted (maintainer, 2026-07-13; decision reversed on measurement 2026-07-14)
- Date: 2026-07-14
- Area: server -- spam filtering, categorisation, plugins, suite settings
- Related requirements: REQ-FILT-01..02 (verdict shape), REQ-FILT-10..13 (endpoint,
  model, plugin), REQ-FILT-20..22 (prompt), REQ-FILT-30..32 (what reaches the LLM),
  REQ-FILT-40..43 (failure modes), REQ-FILT-60..63 (endpoint trust),
  REQ-FILT-65..68 (prompt transparency), REQ-FILT-70..72 (feedback),
  REQ-FILT-100..102 (Sieve seam), REQ-FILT-200..230 (categories),
  REQ-PROTO-65 (spamtest)
- Depends on: ADR-0003 (plugins as first-class extensions) for the settings surface,
  plugin state, and data grants. This ADR extends its state API from one scope to
  three (principal, domain, server).
- Depended on by: ADR-0004 (unified categories). Categories are produced by the
  classifier plugin specified here, in the same call as the spam verdict. Everything
  ADR-0004 says about what a category *is* -- stored vocabulary, user-owned, tab strip,
  dispositions, corrections that stick -- stands. What it says about compiling a
  `definition` into a server-side rule language does not: rules live in the plugin now.

## Reversal

An earlier version of this ADR proposed taking the LLM off the delivery path: a
filter compiled from the user's policy, plus a linear model trained on their
feedback, scoring every message in pure Go. It was accepted on 2026-07-13 and
measured on 2026-07-14 against human ground truth.

It lost. The numbers are below, and they are kept rather than deleted because the
proposal is an attractive one and it will be made again.

## Evidence

Ground truth: 3,631 messages from a production mailbox, every label human-placed or
derived from the reply graph -- 1,689 spam, 1,942 ham, of which **560 are
representative**: a random INBOX draw a human judged. (Reply-graph ham -- mail the
owner answered -- is free and certain and useless for setting a threshold, being
threaded mail from real correspondents and therefore the mail least likely to be a
false positive.)

The metric is **spam recall at a false-positive rate at or below 0.5%**, on the
**hard subset** -- the spam the upstream filter did not already catch. Easy spam is
easy for everyone. A false positive silently files a member's mail in a folder
nobody reads; a false negative is one more spam in an inbox that already gets
fifteen a day. Accuracy is not the metric, and neither is AUC.

| Classifier | recall @ FPR <= 0.5% | false positives / 560 |
|---|---|---|
| **Claude Haiku 4.5** | **65.7%** | **0** |
| Learned filter: 44 header features + hashed n-grams | 52% temporal, 60% cross-validated | -- |
| GLM-4.7-flash (EU-hosted; ~19 GB to self-host) | 43.0% | 0 |
| Nova Lite | 25.0% | 2 |
| **`llama3.2:3b` -- herold's current default (REQ-FILT-11)** | **4.4%** | **29.8% FPR at threshold 80** |

**The LLM wins the metric.** 65.7% against the learned filter's 52%, with zero false
positives across 560 representative ham -- which by the rule of three bounds its true
false-positive rate below 0.54%.

**And it wins the argument that the metric does not capture.** An LLM arrives knowing
what a pharmacy scam, a forged postmaster bounce and a German invoice fraud look
like. A learned filter knows nothing, and everything it knows must be taught to it --
per mailbox, from labelled examples, forever, while spam evolves underneath it. The
corpus above cost two hours of human labelling and produced training data for exactly
one mailbox. None of it transfers: the model's strongest spam feature was `List-Id`,
which is true of a chairman's published address and precisely backwards for a private
inbox full of legitimate mailing lists.

**The LLM is the fallback, not the preference.** This is worth stating plainly,
because the decision reads otherwise. A mechanism that classified and categorised
mail without a model call would be better on every axis herold cares about -- cost,
latency, determinism, and not sending anyone's mail to a third party -- and we would
take it. The measurement says no such mechanism is available at acceptable quality:
the best non-LLM filter built here reaches 52% against the LLM's 65.7%, and needs
per-mailbox training data forever to hold even that. So herold uses an LLM because
the alternative does not work yet, and the design must say so, because the day a
usable non-LLM mechanism exists is the day this decision should be revisited without
a rewrite.

Which is why **classification is pluggable, and spam and category are one plugin
call.** Both are the same question -- *what kind of mail is this?* -- and the server
asks it once, of one plugin, which answers however it likes. Neither the LLM nor any
particular model is load-bearing; the *contract* is.

**herold's default does not work, and that is the urgent finding.** `llama3.2:3b`
catches 4.4% of the spam that matters, and at the threshold the incumbent runs today
it misfiles **29.8% of real mail**. An operator who installs herold, accepts the
defaults and enables spam filtering has roughly one real message in three filed into
Junk. REQ-FILT-11 recommends this model. It must not.

Nor does a bigger local model rescue it: GLM-4.7-flash needs ~19 GB and reaches 43%,
*below* the learned filter. **A small local LLM is not a working spam filter, and a
good one needs hardware a typical self-hoster does not have.**

**The local model is therefore out of scope for now.** Nothing measured runs on a
CPU-only mail server and works. Rather than ship a recommendation nobody can act on,
herold ships none, and the configuration with evidence behind it -- Claude Haiku 4.5,
via the Anthropic API, operator-configured -- is documented as the one that was
measured. This is a decision to stop looking, not a conclusion that nothing exists:
the 8-14B band is untested, and the question is worth reopening when there is a
reason to. It is not worth holding up the build for.

### Traps in the measurement, recorded because every one of them flattered

Each was found only by looking, and each produced a *better* number:

- **The corpus leaks.** The mailbox's own SpamAssassin had written its verdict into
  the headers of the mail being judged, and a `DKIM-Filter` header dated each message
  finely enough to stand in for the folder it ended up in. Both had to be stripped
  before any number meant anything. REQ-FILT-226..229 already blind the in-tree
  backtest to herold's own prior output for exactly this reason -- the same leak,
  arriving from inside the house.
- **The folder is not the label.** 422 of the sampled INBOX messages were spam; INBOX
  was 63% spam. An evaluation scored against folder priors is scored against a 30%
  error rate.
- **Cheap ham is biased ham.** Measured against reply-graph ham, the learned filter
  reported 93% where the honest number was 56%.
- **Forgeable signals are not signals.** A `Re:` prefix appears in 1% of spam and 37%
  of real ham, and costs a spammer three characters to fake. Anything -- model or rule
  -- that leans on `Re:`, `User-Agent` or `In-Reply-To` as evidence of ham is
  borrowing recall against an attack nobody has bothered to run yet.

## Decision

**The LLM remains the classifier**, behind a plugin contract, because nothing else
measured works.

### One plugin, one call, both answers

Spam and category are one question -- *what kind of mail is this?* -- and herold asks
it once, of one plugin:

    mail.classify(message, context) -> { verdict, confidence, reason, category }

One plugin type (`classifier`), one method. It is invoked at the two points where a
message enters a mailbox and nowhere else:

- after the **SMTP DATA** phase, before Sieve;
- when a message is **pulled in over IMAP** from a remote account.

**How the plugin produces the two answers is the plugin's business** -- one model call
returning both, two calls, rules, no model at all. The server does not know and must not
care. That indifference is the point: it is what lets a non-LLM implementation replace
this one without touching the delivery path, on the day one exists.

This replaces `spam.classify` (REQ-FILT-13) and the categoriser both. There is no second
classification mechanism anywhere in the server.

### The plugin owns the rules; the server owns the rule *store*

Users have opinions that no classifier should be guessing at -- "a one-time code from my
bank is never spam", "mail from the club's members is never spam". Those opinions are
rules, and **the plugin defines what a rule is**: its format, its semantics, how it is
edited, how it is compiled from natural language, and how it is evaluated. The server
does not parse a rule, does not evaluate one, and ships no rule engine.

What the server provides is **storage, scoping, and authorisation** -- the things a
plugin cannot do for itself because they are properties of the mail system, not of the
classifier:

| Scope | Owned by | Read by the plugin when classifying |
|---|---|---|
| `principal` | the user | always, for that user's mail |
| `domain` | the domain's administrator | always, for mail to that domain |
| `server` | the operator | always |

The plugin reads all three on every classification and decides for itself how they
combine. That is classifier semantics and it belongs on the classifier side of the
boundary. (The first-party plugin evaluates enforced operator rules, then the user's own,
then advisory operator rules -- so an operator can blocklist a known-bad sender outright,
and a user can still rescue a correspondent the operator has never heard of.)

The server enforces the one thing it must: **who may write which scope.** A principal
cannot edit domain or server rules. A domain administrator cannot edit another domain's.
This is authorisation, and it is not delegable to a plugin.

Rules are **opaque bytes to herold** -- stored, versioned, backed up, and handed over.
This is ADR-0003's plugin state, extended from one scope to three; no new mechanism.

Editing is the plugin's too. It supplies the settings surface as an ADR-0003 tier-1 view
tree, so a rule editor can look like whatever the rule format actually is, and the
suite renders it without executing plugin code.

### What this costs and what it buys

It costs herold the ability to say anything about a rule: no server-side validation, no
server-side backtest of a ruleset it cannot read, no cross-plugin rule portability.
Switching classifier plugins means the rules do not come along.

It buys the thing that made the whole exercise worth doing -- **the classifier is a
single, replaceable component.** Spam filtering is a moving target with a hostile
opponent, and every mechanism worth trying (a model, a rule engine, a trained filter,
some combination) has a different idea of what a rule is. Fixing a rule language in the
server would freeze the design around today's best guess, which the measurement above
says is already the second-best answer to a question that keeps changing.

### Ordering

**The call is synchronous, inside the SMTP transaction, before `250 OK`.** The verdict is
known before the message is stored, so Sieve can test it and no message ever appears in
the inbox and then moves under the user's cursor. The cost is that a slow or rate-limited
model holds the transaction open, which is bounded rather than avoided: **a hard timeout,
after which the verdict is `unknown` and the mail is delivered unjudged** (REQ-FILT-40,
degrade-open). A classifier must never be able to lose mail, and it must never be able to
stall a queue.

Both answers come back in that one call. `\Junk` is exempt from categorisation
(ADR-0004): a `spam` verdict drops the category even if the plugin returned one.
Otherwise ADR-0004's routing rule is untouched -- Sieve routes, categories classify, an
explicit `fileinto` beats an inferred disposition. Sieve gains the category as a test it
could not see before, alongside `${spam.verdict}`.

### The one thing the server still categorises by itself

A plugin may return an empty `category`, and a deployment may have no classifier plugin
at all. In both cases herold falls back to a **small structural categoriser built into
the server** -- `List-Id` and `List-Unsubscribe` to `forums`, `Precedence: bulk` to
`promotions`, `Auto-Submitted` to `updates`, and nothing else. This is REQ-FILT-214,
retained: **the tab strip works on a herold with no model configured and no plugin
installed.**

It is a fallback, not a competitor. **The plugin's category wins whenever it gives one**,
and the fallback fills silence. It is deliberately dumb -- a handful of structural
headers, no user-editable rules, no LLM, nothing to configure -- because the moment it
grows a rule language it becomes the second classification mechanism this ADR exists to
remove.

It works at all only because categorisation is not adversarial: nobody forges a `List-Id`
to look more like a mailing list. Spam is the opposite, the sender is *trying* to defeat
the rule, which is why there is no equivalent fallback for the spam verdict. **With no
classifier plugin, mail is delivered unjudged** -- categorised, but with no opinion about
spam, which is the honest outcome and better than a 3B model destroying the mailbox.

### What plugin authors need to know, from the measurement

Not requirements the server enforces -- it cannot, having given up the rule language --
but findings that cost real effort to obtain, and that the first-party plugin obeys:

- **Forgeable headers may not be evidence of ham.** `User-Agent`, `In-Reply-To`,
  `References` and a `Re:` subject are strings the *sender* types. A `Re:` prefix appears
  in 1% of spam and 37% of real ham and costs three characters to fake. A rule that
  accepts them as evidence of ham sells a `ham` verdict for one header line. SPF, DKIM
  and DMARC are different in kind -- they are the receiving server's own verdict and
  cannot be typed by the sender.
- **Authentication does not fix that, and it was tried.** Spam authenticates perfectly:
  the spammer owns the domain and signs it, while a genuine correspondent on a small
  server may not authenticate at all.
- **A rule that fired is an explanation; a model's `reason` string is a narrative.**
  REQ-FILT-66 promises the user an explanation, so a plugin should say which of the two
  it is giving them.
- **Evaluate on representative ham.** Reply-graph ham -- mail the owner answered -- is
  free, certain, and useless for setting a threshold. It reported 93% where the honest
  number was 56%.

### Model requirements

REQ-FILT-11's default model is withdrawn. In its place:

- **No default cloud endpoint.** REQ-FILT-61 stands: an operator opts in to a cloud
  provider consciously, and is told what leaves the machine when they do.
- **No default model either.** An operator with no configured, validated classifier
  gets spam filtering **off**, and is told so -- rather than a 3B model quietly
  destroying their mail. A spam filter that does nothing is a nuisance; one that
  misfiles a third of real mail is a catastrophe, and the current default is the
  second kind.
- **One documented, measured configuration: Claude Haiku 4.5 via the Anthropic API.**
  It is the only thing tested that meets the target (65.7% recall, zero false
  positives in 560). It is not a default and cannot be one; it is what the operator
  docs recommend, with the table above and the cost next to it, so the choice is made
  on evidence instead of on a guess.
- **`temperature = 0`, pinned.** A classifier's verdict must be reproducible. The
  incumbent samples at the provider default and can file the same message two ways on
  two runs.
- **herold ships the harness to check.** An operator can point the evaluator at their
  own mailbox and get the table above for their own model, before trusting it. The
  in-tree backtest (REQ-FILT-226..229) is most of this machinery already.

## Consequences

The per-message cost and the third-country transfer are real and remain. A plugin can
reduce them -- its own rules can settle a message without a model call -- but that is
now the plugin's design problem, not the server's. REQ-FILT-60's disclosure obligation
stands.

**A deployment with no classifier plugin has no spam filtering.** It still has
categories, from the structural fallback above. Herold ships a first-party classifier
plugin, and installing it is a documented step.

**The operator with no GPU and no willingness to use a cloud API has no working spam
filter on this evidence.** That gap is left open deliberately rather than filled with a
3B model that does harm. The learned filter's 52% is the only thing measured that beats
nothing for them, and it is not built -- if that operator becomes the one we care about,
this ADR is where the argument restarts, and the corpus and harness in
`imap-cleaner/eval/` are what it restarts from. The plugin contract is what makes
restarting cheap: the substitution is a plugin, not a redesign.

G5 ("LLM-first spam") is **preserved**: the LLM decides what spam means and the operator
picks the endpoint. NG4 ("no bundled rule engine, no Bayesian") is now satisfied
literally rather than in spirit -- the server has no rule engine at all.

## Open questions

- **Calibration needs labels, and nobody has them.** The threshold is calibrated per
  mailbox (below), which requires ham the *user judged* -- roughly 600 of it before a
  0.5% false-positive budget means anything, by the rule of three. No mailbox arrives with
  that. Where does it come from: the user's Junk corrections accumulating over months, an
  onboarding pass where they judge a sample, or their reply graph (free, certain, and
  biased 37 points optimistic)? Until it exists the threshold is the constant 80, so this
  is a question about the second year, not the first.
- May the plugin name a category outside the principal's stored set (REQ-FILT-210)?
  Restricting it keeps the vocabulary user-owned, which is ADR-0004's central fix --
  `derivedCategories` died for exactly this reason. So the call payload should carry the
  category set and a plugin answering outside it should be ignored. This is the one way
  the merged call could quietly resurrect the defect ADR-0004 removed.
- Backup and migration of an opaque rule store: herold can back the bytes up, but cannot
  tell an operator whether a restored ruleset is still valid, or convert one plugin's
  rules to another's. Accepted for now; it is the visible cost of the boundary.

## Deferred

- **The minimum viable local model.** `llama3.2:3b` fails at 4.4%; GLM-4.7-flash
  reaches 43% at ~19 GB; the 8-14B band is untested. Not being pursued. If it is ever
  picked up, the argument is that at a small mailbox's volume even slow CPU inference
  is acceptable -- 17 messages a day tolerates 30 seconds a message -- so an 8B model
  clearing ~55% would put a free, private, GPU-free option back on the table.
