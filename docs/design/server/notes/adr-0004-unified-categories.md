# ADR-0004: Unified categories -- one primitive, one disposition knob

- Status: Accepted (maintainer, 2026-07-13)
- Date: 2026-07-13
- Area: server -- filtering, store, JMAP; web -- suite inbox and settings
- Related requirements: REQ-CAT-01..51 (suite categorisation), REQ-FILT-200..231
  (server categorisation), REQ-LBL-01..21 (labels), REQ-UI-13b (sidebar order),
  REQ-FILT-100..102 (Sieve seam)
- Scope statements in tension: G5 (LLM-first spam), G14 (LLM transparency),
  NG4 (traditional spam filtering)
- Depends on: ADR-0002 (spam filtering -- the LLM classifies, policy decides;
  revised on measurement 2026-07-14, but its rule language, which a category's
  `definition` compiles to, is unchanged by that revision), ADR-0003
  (plugins as first-class extensions). Both accepted 2026-07-13; the three ship as
  one project. See "Dependencies and build order".
- Prior-art survey backing this ADR: 17 products, 53 sources. Cited inline.

## Context

Herold organises mail two ways, and they overlap.

**Labels** are JMAP `Mailbox` objects (REQ-LBL, `web/requirements/03-labels.md`). A
message can be in several. Archiving removes the Inbox mailbox, so "label it and
archive it" files mail under a name and takes it out of the inbox.

**Categories** are `$category-<name>` keywords (REQ-FILT-201), assigned by one LLM
call per delivered message (REQ-FILT-213), rendered as a Gmail-style tab strip
over the inbox (REQ-CAT-10). Categorised mail stays in the inbox.

Both answer the same question -- *what kind of mail is this, and how much of my
attention does it want?* -- and the user has to learn two answers to it. That is
the tension this ADR resolves. Four defects follow from it:

**A category has no identity.** REQ-CAT-40 is withdrawn and REQ-FILT-210 removed:
the category vocabulary is not stored anywhere. It is whatever the LLM echoed back
in the `categories` array of its most recent response, persisted as
`derivedCategories` (REQ-FILT-217). A category therefore has no colour, no order,
no settings, and no stable identity across two classifier calls. There is nowhere
to hang a per-category behaviour even if we wanted one.

**The tab strip has a hard ceiling.** Five tabs is roughly what fits, and each tab
is a parallel inbox with its own unread badge and its own way to be forgotten. A
user who wants a "Hobby" category -- and who wants to say what their hobby *is* --
has nowhere to put it. Gmail caps at five and does not let you add a sixth;
Outlook's Focused Inbox caps at two.

**Correction does not stick.** REQ-CAT-21 says the suite sets the keyword and
"herold does what it does with that". REQ-FILT-221 says the correction is
"recorded for prompt-tuning feedback", with the mechanism "out of scope". Today
that means nothing happens, and the next re-categorisation run can silently undo
the user's correction, because nothing distinguishes a keyword the user set from
one the classifier set.

**Categorisation requires an LLM.** REQ-FILT-213 puts an OpenAI-compatible HTTP
call on the delivery path of every inbox-bound message, carrying From, To,
Subject and 2 KB of body (REQ-FILT-214). An operator with no endpoint gets no
categories at all.

## What the prior art actually says

Three findings did the work here; the rest is in the survey.

**Google already collapsed this axis, then abandoned the product.** Inbox by Gmail
(2014-2019) let the user choose, per bundle, *when* its mail appears: [as it
arrives, once a day, once a week, or skip the inbox
entirely](https://gmail.googleblog.com/2014/11/a-bit-about-bundles-in-inbox.html).
Same post: bundled messages do not raise a phone notification by default.

Read that list against our tension. "Skip the inbox entirely" *is* label+archive.
"As it arrives" is a plain label. "Once a day" is a digest. They are not different
mechanisms -- they are four settings of one setting on one object. Gmail's own
successor product had the unification, and the version that survived is the
fragmented one.

**A bundle costs a row, not a tab.** The tab ceiling is a property of tabs, not of
categories. A bundle occupies one collapsed row in a single stream, positioned by
its newest member, and disappears when empty. Cardinality stops binding. This is
what lets "Hobby" exist.

**Exclusivity is a property, not a type.** [Mailpile's
tags](https://github.com/mailpile/Mailpile/wiki/Tags) carry a `type`: a `folder`
is simply a tag with an exclusivity constraint -- "a message can not exist in two
'folder'-type tags simultaneously". That dissolves labels-versus-folders into one
object with a flag, and it maps directly onto JMAP.

Two findings that constrain rather than enable:

**Most users never organise anything.** Grbovic, Halawi, Karnin and Maarek, ["How
Many Folders Do You Really Need?"](https://arxiv.org/abs/1606.09296) (CIKM 2014):
*most web mail users never define a single folder*, and about six latent
categories explain most traffic. The machine axis is universal; the user axis is
empty for almost everyone. Any design that requires the user to configure their
categories first has already lost the median user.

**The protocol cannot express provenance, and that is the bug.** The IETF is
standardising 17 new keywords
([draft-ietf-mailmaint-messageflag-mailboxattribute](https://datatracker.ietf.org/doc/html/draft-ietf-mailmaint-messageflag-mailboxattribute),
co-authored by RFC 8621's Neil Jenkins) in **one flat namespace**, where
`$istrusted` is "set by the server on delivery" and `$hasmemo` is "set and cleared
by the client based on user interaction" -- and the only thing carrying that
distinction is prose in the IANA registration template. A client cannot tell from
the wire which assertion the server made and which the user made. Every product
doing machine classification worked around this by reserving a namespace users
cannot write into (Gmail's `CATEGORY_*` is reserved; creating one returns `400
Invalid label name`; `category:` and `label:` are distinct query verbs).

**Honesty about what is ours.** No source argues that machine categories and user
labels should be one primitive. Every shipping product keeps them apart. The
merge below is our synthesis, and the reserved-namespace convergence above is
evidence *against* it. We take it anyway, because the thing those products are
really buying with a reserved namespace is *provenance* -- and provenance is
cheaper to store as a field than to encode as a second namespace.

## Decision

**One primitive, called a Category. A label is a category. A category is a label.**

Eight parts.

### 1. The Category object

A Category carries what a label carries, plus what a classifier needs:

| Field | Purpose |
|---|---|
| `id`, `name`, `colour`, `parentId` | What REQ-LBL-01/04/05 already give a label |
| `definition` | Natural-language prose: what mail belongs here. Optional. |
| `rules` | The compiled ruleset generated from `definition` (ADR-0002's DSL) |
| `disposition` | The one knob. See below. |
| `priority` | Position in a single user-ordered list. Breaks ties. |

A category with no `definition` is exactly today's label: a name the user applies
by hand. A category with a `definition` also fills itself. Nothing else changes.

There is no exclusivity flag. Mailpile's framing (below) is what makes the merge
legible, but the constraint it models is not one we need: `priority` already
answers the only question exclusivity was there to answer -- which lane a
multi-matching message shows in. An unimplemented field is a lie in the schema,
so it is cut.

### 2. Disposition is the only knob, and it drives four things

```
  pinned   -- its own tab. Badges. Notifies. Max 5.
  bundled  -- one collapsed row in the inbox stream, by newest member.
              Inline count. Does NOT badge the app. Does NOT push.
  daily    -- bundled, but surfaces once a day.
  weekly   -- bundled, but surfaces once a week.
  filed    -- never enters the inbox. This is label+archive.
  none     -- applied, but no inbox effect. A search facet.
```

One setting decides inbox membership, stream presentation, unread badging, and
push notification, because those are the same question asked four times: *how much
attention does this class of mail deserve?* Four independent toggles would let the
user build incoherent states (a muted category that still pushes). One ordered
knob cannot.

This is the axis Inbox by Gmail shipped, with `pinned` added at the loud end and
`none` at the quiet one.

**"Label + archive" and "category tab" are now the same mechanism at two
settings.** That is the whole ADR in one sentence.

### 3. Membership is a keyword; a category is also a virtual mailbox

Membership stays `$category-<id>` on the message, as today. It does **not** become
`Mailbox` membership.

The reason is IMAP. Real mailbox membership would mean a message sits in INBOX
*and* in Promotions, so every IMAP client sees it twice (Gmail's behaviour), and
re-categorising is a mailbox move that burns UIDs and churns every connected
client. A "re-categorise my last 1000 messages" run would become a storm. Keywords
have neither problem: one message, one copy, one UID, and recategorisation is a
keyword flip.

To keep "a category is a label" true for IMAP clients, **each category is exposed
as a virtual mailbox** backed by a saved `Email/query`. Thunderbird and Apple Mail
see a browsable folder; the store holds no second copy. The store has no
virtual-mailbox mechanism today and gains one -- though the suite already fakes
exactly this for `$important` and `$snoozed`
(`web/apps/suite/src/lib/mail/store.svelte.ts`), so the shape is known.

### 4. Multi-membership, single lane

A message may hold several categories. A model-railway retailer's sale is
genuinely both Hobby and Promotions, and a classifier told to pick one will pick
Promotions every time.

The inbox shows each message **once**, in the lane of its highest-priority
category. Priority is the single ordered list from the Category object -- put
Hobby above Promotions and the sale surfaces under Hobby. Search still sees both.

This replaces REQ-CAT-01's partition, and it moves the Hobby-versus-Promotions
judgement from the classifier (which will get it wrong) to an ordered list the
user can read and reorder (which cannot be wrong, only unwanted).

### 5. Every assignment carries provenance

`machine` | `rule` | `user`, stored per assignment.

- A `machine` assignment never overwrites a `user` one. Re-categorisation runs
  know what they may touch.
- "Was this filed by me or by the classifier?" becomes answerable -- Gmail needs
  `has:userlabels` for this; we get it from a field.
- It is the field the IETF's flat namespace cannot express, and the reason we do
  not need a reserved namespace to get the same protection.

This is the concrete fix for REQ-CAT-21 and REQ-FILT-221, which today record the
correction and do nothing with it.

### 6. The inbox is a complete stream; tabs are an opt-in promotion

The dominant complaint about Gmail's tabs, across every source in the survey, is
"mail disappeared into a tab I never look at". The structural cause is that
Primary is *a bucket*, not *everything*: the strip partitions the inbox, so no
view shows all of it.

**Compression, not concealment.** The default inbox shows every message in date
order. Categories collapse to one row; they do not move elsewhere.

```
  [Allgemein] [Hobby]              <- pinned tabs, max 5, opt-in
  ------------------------------------------------------------
    Torsten Bulck      Re: CC 2027            10. Juli
  > Werbung (12)                              10. Juli
    Anna Meier         Rechnung Juni           9. Juli
  > Soziale Netze (5)                          8. Juli
    Sparkasse          Kontoauszug             8. Juli
```

Tabs are a scarce resource (max 5) that the user *promotes* a category into. The
rest bundle. Nothing is ever reachable only through a lane you stopped checking.

### 7. Visibility is a property of the surface, not of the category

Two predicates, and the survey shows products pick them by surface:

- **Nav surfaces show-if-unread.** Gmail's `Label` resource has a three-state
  `labelListVisibility` (`labelShow` / `labelShowIfUnread` / `labelHide`) that its
  help pages have never documented -- only [the
  API](https://developers.google.com/gmail/api/reference/rest/v1/users.labels)
  does. Fastmail and Superhuman ship the same thing.
- **Stream surfaces hide-when-empty.** Presence tied to message count, not unread
  count.

Inbox by Gmail ran both, on one label, simultaneously. So does this design: the
sidebar promotes a category when it has unread; the stream shows a bundle when it
is non-empty. No third setting.

### 8. The definition compiles to rules. The LLM is optional.

Per ADR-0002, the LLM is a compiler, not a classifier. A category's `definition`
("mail about my model railway hobby: club newsletters, these vendors, forum
digests") compiles **once** into ADR-0002's closed-vocabulary ruleset, which then
scores in pure Go, per message, with no network call and no mail content leaving
the process.

Consequences that matter here:

- **No LLM is required.** The default categories ship as rules compiled into the
  binary, over structural signals herold already has: `List-Id`,
  `List-Unsubscribe`, `Precedence: bulk`, `Auto-Submitted`,
  `Authentication-Results`, and sender-in-contacts. A box with no LLM endpoint
  gets a working, useful default set. The LLM becomes an enhancement -- which is
  the posture the maintainer asked for, and a strictly better default than a
  feature that does nothing until an endpoint is reachable.
- **The preview diff is worth more here than for spam.** "You changed Hobby's
  definition; here are the 12 messages that would move" turns natural-language
  configuration into a repeatable operation rather than a wish. ADR-0002 already
  builds the backtest; categorisation reuses it.
- **Corrections become rules.** "Not this" writes a deterministic rule or a named-
  list entry -- the same `lists` mechanism ADR-0002 defines -- rather than feeding
  a prompt. This is the direct fix for the second-most-common complaint in the
  survey ("training doesn't stick"), and it needs no LLM call at all.
- **`derivedCategories` dies.** The vocabulary is stored data the user owns, not an
  artefact of the last LLM response.

**Revised by ADR-0002 (2026-07-14).** One plugin answers `mail.classify` and returns
the spam verdict and the category in one call, made after SMTP DATA and on IMAP pull.
The rule format is the plugin's -- it compiles a category's `definition` and evaluates
the result itself, and the server ships no rule engine to do that with.

Everything above about what a category *is* stands: stored vocabulary the user owns,
per-category dispositions, the tab strip, corrections that write a rule rather than
feed a prompt, `derivedCategories` dead. What changes is where the rules live.

REQ-FILT-214 survives in reduced form. The server keeps a **small structural
categoriser** -- `List-Id` to `forums`, `Precedence: bulk` to `promotions`,
`Auto-Submitted` to `updates` -- with no rule language, nothing to configure, and no LLM.
The plugin's category wins whenever it gives one; the fallback fills silence. So the tab
strip works on a herold with no model and no plugin, which is the property this
requirement was written to protect.

## Where categorisation runs

Categorisation runs on **all** delivered mail, not only inbox-bound mail. It has
to: `filed` is a disposition, so a category can route, and a categoriser that only
sees inbox-bound mail cannot implement it.

That puts it in overlap with Sieve, and the rule is:

> **Sieve routes. Categories classify. An explicit route beats an inferred one.**

If the user's Sieve script issued a `fileinto`, that wins -- it is an explicit
instruction, and categorisation is inference. Otherwise the highest-priority
matching category's disposition decides whether the message enters the inbox.
`\Junk` is exempt: spam is not categorised.

This reverses REQ-FILT-200, which today restricts categorisation to inbox-bound
mail.

## Zero configuration is the design centre

Grbovic's finding is load-bearing. The median user will never write a
`definition`, never reorder priorities, never promote a tab. So:

- The five default categories ship with compiled-in rules and sensible
  dispositions (Primary `pinned`; Social, Promotions, Updates, Forums `bundled`).
- The inbox on day one, on a box with no LLM configured, is a complete stream with
  four bundles and one pinned tab.
- Everything in this ADR that the user *can* configure is an affordance for the
  minority who will, layered on a default that works untouched.

## What this contradicts, and what has to change

Stated plainly, because none of it can be quietly assumed.

**Scope (`docs/design/00-scope.md`)** -- amended by this ADR, on the maintainer's
instruction, in the same change:

1. **G5** ("LLM-first spam. No rule engine, no Bayesian... One classifier call per
   message") and **NG4** ("No bundled rule engine. No Bayesian.") are reversed.
   This is ADR-0002's central question, not ours; we record that the maintainer
   ruled to accept it, and amend the text. The rule engine being reintroduced is a
   closed-vocabulary evaluator of a few hundred lines, not Rspamd.
2. **G14** (LLM transparency) is *strengthened*, not weakened. Its promise today is
   "you can read the prompt". Under ADR-0002 the user can read **the rules their
   policy generated, and the trace of which ones fired on a given message** -- a
   decision rather than a narrative. The G14 text, which is written entirely in
   terms of prompts and per-message excerpts, has to be restated in those terms.
3. The scope intro ("LLM-based message categorisation", "no bundled rule-based
   spam") is restated to match.

**Server requirements (`server/requirements/06-filtering.md`, Part C)** --
REQ-FILT-200 (inbox-only), -201 (at most one keyword), -210/-211/-215/-217 (prompt
as the vocabulary, `derivedCategories`), -213/-214 (per-message LLM call carrying
mail content), and -221 (feedback goes nowhere) are all superseded. -202, -203,
-216, -220, -230, -231 are rewritten.

**Suite requirements (`web/requirements/05-categorisation.md`)** -- REQ-CAT-13
("tabs are not labels... not a separate location") is reversed outright; it is the
exact statement this ADR overturns. REQ-CAT-01 (one keyword), -10 (fixed 5-tab
strip), -40 (no editable category list) are superseded. -03, -11, -12, -20..22,
-30..31, -41..45, -50..51 are rewritten.

**Label requirements (`web/requirements/03-labels.md`)** -- REQ-LBL-07 ("Each label
shows an unread count badge when unread threads exist") is overridden by
disposition: a bundled category deliberately does not badge, which is the entire
point of bundling. REQ-LBL-01/03/06 gain the new fields.

## Dependencies and build order

This ADR rests on two others, accepted the same day:

- **ADR-0002** supplies the rule DSL, the evaluator, the feature registry, the
  backtest-and-preview flow, and the trace. Section 8 is entirely its machinery.
- **ADR-0003** supplies plugin installation, **plugin state**, and **plugin
  settings UI**. A category's compiled ruleset is per-principal state that a
  plugin must be able to write, and REQ-PLUG-44 forbids a plugin from touching
  the DB. Without ADR-0003 there is nowhere to keep it.

**These three ship as one project** (maintainer, 2026-07-13). Sections 1-7 could
technically land on top of the existing per-message LLM categoriser, deferring
section 8; that path was considered and rejected. It would put a per-message LLM
call on the delivery path of a design built to remove it, and would need glue
between the new Category object and the old categoriser that exists only to be
deleted. The order is ADR-0003 (plugin state and UI), then ADR-0002 (evaluator),
then this (Category object, virtual mailboxes, suite).

## Consequences

The store gains a virtual-mailbox mechanism (section 3), which is new machinery
and the largest single cost in this ADR. It buys IMAP visibility for categories
without duplicating a single message.

The suite's category tab strip currently filters **client-side** over the fetched
page (`MailView.svelte`). Bundles and per-lane counts need a server-side
`hasKeyword` query with its own pagination, and `$category-*` keywords are not in
the Bleve mapping (`internal/storefts/index.go`), so a category cannot presently be
combined with a text search server-side. Both are prerequisites, not follow-ups.

`internal/categorise` keyed on `strings.EqualFold(mb.Name, "INBOX")` rather than
the `MailboxAttrInbox` attribute. That check disappears with REQ-FILT-200 anyway.

The priority list is a new thing for the user to understand. It earns its place by
being the only place the Hobby-versus-Promotions question is answered, and by being
answerable by reordering a list rather than by arguing with a classifier.

## Resolved (maintainer, 2026-07-13)

The questions this ADR opened, and how they were answered.

**Exclusivity: cut.** `priority` already answers which lane a multi-matching
message shows in, which is the only thing the flag was for. Mailpile's framing
stays as the argument for the merge; the field does not ship.

**Virtual mailbox writes: mirror Gmail.** `COPY` and `APPEND` into a category's
virtual mailbox set that category's keyword, with `user` provenance so the
evaluator never undoes it. `MOVE` sets the keyword *and* removes the message from
INBOX -- which is precisely the `filed` disposition, arrived at from the other
direction. `EXPUNGE` from a category folder clears the keyword and leaves the
message where it is. Every IMAP user already knows these semantics from Gmail, so
dragging a message onto "Hobby" in Thunderbird does what it looks like it does.

**Deferral: a fixed hour the user sets.** A `daily` or `weekly` bundle
materialises at a per-principal hour, defaulting to 07:00 local. Predictability is
the entire value of batching -- the user learns when the newsletters arrive, the
way they learn when the post does. Surfacing on first-open-after-interval would
drift with the user's habits and never become a rhythm. The scheduler and the
timezone both already exist for snooze.

**Tab cap: five, matching Gmail.** Bundles are uncapped regardless, so the cap
only governs how many parallel inboxes a user may opt into. Five is what arrivals
from Gmail expect and it lets the five defaults all be tabs for a user who wants
the old look. (This ADR originally proposed three, on the argument that a pinned
tab is a place to forget mail and the scarcity is the point. Overruled.)

**Migration: map the five defaults, drop the rest, and say so.** The five shipped
names map onto the seeded Category objects. Any other `$category-<name>` keyword
-- from an edited prompt, or a name the LLM invented under REQ-FILT-215's
free-vocabulary response -- is removed. The migration **reports what it deleted**
(names and message counts); a migration that discards user-visible state does not
get to do it silently.

**Backtest: full, with a limit.** The candidate ruleset is evaluated against every
message in the mailbox, bounded by a configurable cap (default 200 000, most
recent first). Sampling was the inherited answer from the era when scoring meant
an LLM call per message; a compiled ruleset scores in microseconds, so the cost
that justified sampling is gone, and with it the recency and class-imbalance
biases that would have made the preview diff a confident lie. When the cap binds,
the preview says so (REQ-FILT-226..227).

Two things fall out of this that the requirements now nail down, because both are
easy to get wrong in the obvious way:

- **The diff shows what moves, never whether the move is better** (REQ-FILT-228).
  Outside the user's own corrections there is no ground truth for a category --
  the machine asserted it -- so a backtest compares a new ruleset against an old
  one, not against truth. Precision and recall are not computable and must not be
  displayed. Spam is the exception: Junk moves are real labels, which is what
  ADR-0002's threshold recalibration rests on.
- **The evaluator is blinded to its own prior output during a backtest**
  (REQ-FILT-229) -- no `$category-*`, no `$Junk`, no `X-Spam-*`. This is the same
  leakage ADR-0002 found in the imap-cleaner corpus, arriving from inside the
  house instead of from SpamAssassin.

## Still open

Nothing. The questions this ADR raised are answered above.
