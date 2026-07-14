# herold-classify -- the classifier plugin herold ships with

**Drafted:** 2026-07-14. **Predecessor:** ADR-0002 (one plugin call, plugin owns the
rules), ADR-0003 (plugin lifecycle, state, data grants, UI tiers), ADR-0004 (categories).
**Status:** sketch.

This is the first-party implementation of the `classifier` plugin type. Herold's server
has no rule engine and no model client; everything about *how* mail is judged lives in
this binary. It is the reference implementation and the thing an operator installs to get
a working spam filter and working categories.

Because the server treats it as opaque, **everything below is this plugin's decision, not
herold's ABI.** A different classifier plugin may do all of it differently, and that is
the point of the boundary.

---

## Shape

A single Go binary, `herold-classify`. Out-of-process, JSON-RPC 2.0 over NDJSON on
stdio (REQ-PLUG-70..72). No CGO. Ships in the herold release, installed and enabled as
a plugin like any other -- **it is not built into the server**, because the day it is,
the boundary rots.

It answers three things:

| Method | Called by | When |
|---|---|---|
| `mail.classify` | delivery path | after SMTP DATA; on IMAP pull |
| `rules.compile` | settings panel | when a user edits their policy |
| `settings.panel` | suite | when a user opens classifier settings |

Its manifest declares plugin type `classifier`, the data grant it needs, and
`temperature = 0` pinned (Wave 4.1 refuses a classifier manifest that does not).

**Data grant** (ADR-0003 -- the operator confirms this at install, and it is the whole
of what leaves the machine):

- envelope sender and recipient
- `From`, `To`, `Cc`, `Subject`, `Reply-To`, `Return-Path`, `Date`
- `List-Id`, `List-Unsubscribe`, `Precedence`, `Auto-Submitted`
- `Authentication-Results` (the server's own SPF/DKIM/DMARC verdict)
- the first 2 KB of the text body

No `message.raw`, no attachments, no full body. The grant is a projection the server
builds, not a promise the plugin makes.

---

## The pipeline

```
message ---> rules (local, no model call) ---> ham | spam | category  ---> done
                       |
                       `-- no opinion --------> one model call --------> verdict + category
```

**Rules run first, and settle what they can.** This is where the bill gets reduced and
where correctness that matters is guaranteed: "a one-time code from my bank is never
spam" is a floor, not a probability. Most mail has no rule and falls through.

**One model call decides the rest, and it answers both questions at once** -- verdict and
category in a single response. There is never a second call.

**The whole pipeline runs inside the SMTP transaction, before `250 OK`** -- so the
verdict exists before the message is stored, Sieve can test it, and nothing ever appears
in the inbox and then moves under the user's cursor. **The sending MTA is waiting**,
which is the constraint everything below is shaped by:

- The server gives the call a hard budget (default 5s) and cuts it off. That is a
  delivery guarantee and it is the server's, not the plugin's -- but the plugin must be
  built as though it were its own: its own model timeout sits *inside* the server's, so
  the plugin returns `unknown` deliberately rather than being killed mid-flight.
- **A message a rule settles never waits for a model at all.** With latency now on the
  delivery path, the rule layer stops being a cost optimisation and starts being a
  latency one.
- No retries on the delivery path. A rate-limited or 5xx-ing endpoint yields `unknown`
  immediately; retrying inside a held-open SMTP transaction turns one slow provider into
  a queue-wide stall.

**A verdict is always produced.** A model timeout, a rate limit, or a crash yields
`unknown`, and the server delivers the mail (REQ-FILT-40, degrade-open). A classifier
must never be able to lose mail, and it must never be able to stall a queue.

---

## Rules

TOML on disk and in the settings panel, JSON in plugin state. Conditions are **CEL**
(`google/cel-go`) -- non-Turing-complete, so termination is a property of the language;
a real type checker, so a model that invents a field fails to compile; and models emit it
reliably, their training data being saturated with it. The dependency is this plugin's to
carry, not the server's.

```toml
[[rule]]
id      = "two-factor-code"
effect  = "ham"
because = "policy line 7: one-time codes are never spam"
when    = '''auth.dkim == "pass" &&
             subject.matches("(?i)\\b(one[- ]time|verification|2fa)\\b")'''

[[rule]]
id       = "club-mailing-list"
effect   = "ham"
category = "forums"
because  = "policy line 2: the kayak club list is never spam and belongs in Forums"
when     = 'header("List-Id").contains("kajak.example.org")'

[[rule]]
id      = "forged-member-domain"
effect  = "spam"
because = "policy line 3: a forged From: using a member domain is spam"
when    = 'from.registrable in lists.members && auth.dmarc != "pass"'
```

`effect` is `ham`, `spam` or `defer` (the default). `category` is optional and may appear
with any effect except `spam` -- `\Junk` is not categorised (ADR-0004). A rule may set a
category and still `defer` the spam question to the model.

Rules are evaluated in order and **the first with an opinion wins**. There is no score,
no weight, and no threshold anywhere in the rule layer. A rule has an opinion or it
defers.

### The environment a rule sees

| Field | Type | Meaning |
|---|---|---|
| `header("Subject")` | `string` | First occurrence, unfolded; `""` when absent |
| `headers("Received")` | `list<string>` | All occurrences |
| `has_header("List-Id")` | `bool` | Present at all |
| `from`, `to`, `reply_to`, `return_path` | `Address` | `.addr`, `.domain`, `.registrable`, `.tld`, `.local` |
| `auth.spf`, `auth.dkim`, `auth.dmarc` | `string` | `pass` / `fail` / `softfail` / `none` |
| `lists.<name>` | `list<string>` | A named list defined alongside the rules |
| `subject`, `body_excerpt` | `string` | Body only if the data grant allows it |

`.matches()` is RE2 -- linear time, no backtracking. No I/O, no clock, no DNS: a rule is
a pure function of the message and the lists.

**Named lists are where day-to-day tuning actually happens** (`members`, trusted
domains), and they need no model call at all. Adding a sender to a list is the cheapest
correction in the system, and the settings panel makes it a chip.

`registrable` uses a real public-suffix table. Taking the last two labels turns
`foo.co.uk` into `co.uk`, and one British correspondent then vouches for every sender in
Britain -- observed in the eval corpus as 16 spam messages arriving from a "known"
domain.

### Rules the compiler refuses to emit

**Forgeable headers may not be evidence of ham.** `User-Agent`, `In-Reply-To`,
`References` and a `Re:` subject are strings the *sender* types. `Re:` appears in 1% of
spam and 37% of real ham and costs three characters to fake. A `ham` rule resting on them
sells a ham verdict for one header line. This is a compile error with a message naming
the reason, not a lint warning.

SPF, DKIM and DMARC are different in kind: they are the receiving server's own verdict
and cannot be typed by the sender. **Gating the forgeable headers on authentication does
not rescue them, and it was tried** -- spam authenticates perfectly, because the spammer
owns the domain and signs it, while a genuine correspondent on a small server may not
authenticate at all.

### Scopes

The plugin reads all three of ADR-0002's scopes on every classification and merges them
itself:

```
enforced server + domain rules      operator guardrails; the user cannot override these
principal rules                     the user's own opinions about their own mail
advisory server + domain rules      defaults the user may override
```

Evaluated top to bottom, first opinion wins. **The user's opinion about their own mail
beats an administrator's default, and loses to an administrator's guardrail** -- which is
the only ordering that lets an operator blocklist a known-bad sender without also
preventing a user from rescuing a correspondent the operator has never heard of. A
server or domain rule opts into the top band with `enforced = true`.

Rules live in ADR-0003 plugin state, cached in-process and invalidated on the server's
write event. There is no database read per message.

---

## The model call

One call, both answers, `temperature = 0`, structured output.

**Claude Haiku 4.5 via the Anthropic API** is the shipped configuration -- the only thing
measured that meets the target: **65.7% recall on the hard subset at zero false positives
across 560 representative ham**. Nothing is hard-coded to Anthropic; the endpoint,
model and key are plugin settings, and the plugin speaks the Anthropic API and the
OpenAI-compatible one. But there is **no default model**, because the last one misfiled
one real message in three.

The prompt carries: the user's spam policy in their own words, the principal's category
set with each category's `definition`, and the granted message projection. The response
schema is fixed:

```json
{
  "verdict":    "ham" | "spam",
  "confidence": 0-100,
  "reason":     "one sentence, shown to the user",
  "category":   "<name from the supplied set>" | ""
}
```

**The plugin will only accept a category from the set it sent.** A model naming anything
else is ignored and logged -- the vocabulary stays the user's, which is exactly the defect
ADR-0004 removed when it killed `derivedCategories`, and this call is the one place it
could come back.

If the plugin returns an empty `category`, the server's structural fallback (ADR-0002 --
`List-Id` to `forums`, and two more like it) answers instead. The plugin's category always
wins when it gives one.

### The threshold is calibrated, not constant

Spam is filed above a per-principal confidence threshold, chosen as the lowest value that
holds a **0.5% false-positive rate on that principal's own judged ham**. A false positive
files a real message in a folder nobody reads; a false negative is one more spam in an
inbox that already gets fifteen a day. The budget is set on the expensive error, and the
recall falls where it falls.

**Until a mailbox has enough judged ham, the threshold is the constant 80** -- the
incumbent's value, and the measured best operating point on the eval corpus was 79.
"Enough" is roughly **600 judged ham**, by the rule of three: zero false positives across
N ham bounds the true rate at about 3/N. No mailbox arrives with 600, so in practice every
new mailbox runs at 80 and calibrates later, if ever. **The plugin must say which of the
two it is doing.** A number presented as measured when it was assumed is worse than no
number.

Reply-graph ham -- mail the user answered -- does not count toward the 600. It is free and
certain and it is the easiest ham there is: calibrating against it reported 93% where the
honest number was 56%.

**Cost and latency:** roughly 700 input tokens per message, and one to two seconds inside
a held-open SMTP transaction. At 17 messages a day, the volume of the mailbox this was
measured on, neither matters. At 10,000 a day both do, which is what the rule layer is
for.

---

## Corrections write rules

When a user moves a message out of Junk, or recategorises it, the plugin offers to make
it stick -- by adding the sender, the domain, or the `List-Id` to a named list. Accepting
writes a deterministic rule that needs no model call and that a later re-classification
cannot undo. (ADR-0004: "training doesn't stick" is the second-most-common complaint in
the survey, and feeding a prompt does not fix it.)

The offer is one click and the result is visible as a chip in the settings panel. This is
the feedback loop, and it is made of rules, not of embeddings.

---

## Settings panel

An ADR-0003 tier-1 view tree. The plugin supplies the tree; the suite renders it; **no
plugin JavaScript executes in the suite's origin.**

- **Policy** -- a `textarea`. The user's own prose. This is REQ-FILT-65's "prompt
  currently in effect", made editable.
- **Apply** -- compile, preview, accept. Nothing changes until accept.
- **Preview diff** -- "this would reclassify 3 messages", listed, backtested against the
  user's own recent mail. The backtest is blind to herold's own prior output (`$Junk`,
  `$category-*`, `X-Spam-*`) or it measures its own reflection.
- **Active rules** -- a `table`: rule, effect, category, and the `because` sentence
  naming the policy line that produced it. A rule can be disabled without editing the
  policy. Enforced operator rules are shown as such and are not editable (REQ-FILT-67
  excludes operator guardrails from the user's view; showing that they *exist* without
  showing their contents is the compromise).
- **Lists** -- `chips`. Where the tuning actually happens.
- **Why was this filed?** -- the rule that fired, or the model's reason, and which of the
  two it was. A rule that fired *is* the decision; a model's reason is a plausible
  narrative that cannot be checked against what the model did. The user is entitled to
  know which they are reading.

## `rules.compile`

The user's prose in, a ruleset out, **and no message data in the payload** -- their own
words and a schema, nothing else. This is what makes natural-language configuration free
of the privacy question entirely, and it must not be quietly given up later for a
"compile with examples" feature.

The compile prompt carries the CEL environment, the effect vocabulary, the
forgeable-header prohibition, and a worked example. Output is type-checked by cel-go
before it goes anywhere near a message, and rejected with a message the user can act on:
unknown field, wrong arity, type mismatch, forgeable-ham rule, rule-count cap (256), cost
estimator over budget.

---

## Testing

- **The eval corpus is the acceptance gate.** `imap-cleaner/eval/` -- 3,631
  human-labelled messages, 560 of them representative ham a human judged. The plugin must
  reproduce the Haiku row: 65.7% recall on the hard subset, zero false positives.
- **Recall is measured on the hard subset only** -- the spam an upstream filter did not
  already catch. Easy spam is easy for everyone.
- **The false-positive rate is measured on representative ham only.** Reply-graph ham --
  mail the owner answered -- is free, certain, and useless: it reported 93% where the
  honest number was 56%.
- Fuzz target on the ruleset decoder, seed corpus under `testdata/fuzz/`, clean at 30s
  (STANDARDS section 8). Not on the CEL expression parser: that is cel-go's, and fuzzing
  someone else's maintained parser is not our job.
- A golden set of policy prose -> expected ruleset, so a model upgrade cannot silently
  change what a user's policy compiles to.

## Open questions

- **Where do 600 judged ham come from?** The calibrated threshold needs them and no
  mailbox arrives with any. Candidates: Junk corrections accumulating over months (slow,
  but free and honest), an onboarding pass where the user judges a sample (fast, but it is
  an hour of a stranger's time), or the reply graph (instant, and 37 points optimistic --
  so not on its own). Unanswered, and it decides whether calibration is a real feature or
  a permanently-deferred one.
- **The synchronous call is a hard dependency on a third party during SMTP.** If Anthropic
  is down or slow, every message takes the full 5s budget and is delivered unjudged. That
  is correct behaviour and it is also a bad afternoon. A short-lived circuit breaker --
  after N consecutive timeouts, skip the model entirely for M minutes -- would turn a
  degraded hour into a fast one. Worth building; not yet specified.
- **Is the model call worth making for mail a rule already categorised but did not
  judge?** A rule that sets a category and defers the spam question still needs the call.
  Probably yes, and the prompt can then skip the category question and save the tokens.
