---
description: Unblock a stalled herold ticket — ask the maintainer the open decisions, fold the answers into the description, drop waiting-for-feedback
argument-hint: <issue number>
---

Unblock issue **#$ARGUMENTS** in the herold Forgejo repo (`herold/herold` at
code.netzhansa.com).

A ticket lands here when it is stalled on decisions only the maintainer can
make: an agent has analysed it, left open questions, and applied
`waiting-for-feedback`. Your job is to get those questions answered and leave
the ticket in a state an implementor can pick up without asking anything.

Do not fix the ticket. Do not write code. This command ends with an actionable
ticket, not a commit.

## 1. Read the whole ticket

Fetch the issue (`mcp__forgejo__issue_get`) and **every** comment
(`mcp__forgejo__issue_comments_list` with `full: true` — the truncated bodies
hide exactly the detail you need). The opening description is often not the
current target; the latest analysis comment usually is.

## 2. Ground the questions before you ask them

**Answer, from the repo, every question the repo can answer.** A ticket's
analysis comment is a snapshot that goes stale: it will claim work is unmerged
when it landed weeks ago, name a branch that no longer exists, or list a file
that has since moved. Asking the maintainer to decide on a false premise wastes
the round-trip this command exists to avoid.

So before formulating anything, verify the ticket's factual claims:

- Commits and branches it names: are they on `main` (`git merge-base
  --is-ancestor <sha> main`), still pending, or gone?
- Files, symbols, routes, migrations it names: do they still exist as described?
- Tests it claims are green: do they exist?

State what you observed. If a claim is stale, that is a finding — the question
built on it may dissolve entirely, and the maintainer needs to know the ticket
was lying to them.

## 3. Ask the real decisions

Put the genuinely-open questions to the maintainer with **AskUserQuestion**
(up to 4 per call; ask in one batch, not one at a time).

A question earns a slot only if the answer changes what gets built and the repo
cannot settle it. Scope calls, mechanism choices, and in-scope/out-of-scope cuts
qualify. "Should I proceed?" does not.

For each question:
- 2-4 concrete, mutually exclusive options.
- Lead with your recommendation, suffixed `(Recommended)`.
- Each option's description says what it *costs* — what work it pulls in, what
  it defers, who it drags in. The maintainer is choosing between futures, not
  between labels.

## 4. Fold the answers into the ticket

Rewrite the description (`mcp__forgejo__issue_edit`) so it reads as the work
that now stands:

- **Affirmative, present-tense.** State what the ticket *is*, not how it got
  here. No "originally we planned X", no "the compiled-in registry we're
  replacing", no definition-by-negation against a rejected option — those only
  parse for someone who watched the deliberation.
- **Delivered vs. remaining.** If part of the work is already on `main`, say so
  with the commit SHAs, and give the rest its own section. An implementor must
  be able to tell in one read what is left.
- **Acceptance.** Give the ticket a green-check condition — the observable
  behaviour that proves it done. Per CLAUDE.md, "a fix was pushed" is not a
  done-state; a passing test or a driven flow is. Both SQLite and Postgres where
  the change touches the store.
- **References last.** No inline cross-ticket links in the prose; collect
  tickets, requirement IDs, and design docs into a References section, one line
  each with why it matters.

Then:

- **Answers that push work out of scope get their own ticket.** File it
  (`mcp__forgejo__issue_create`, or the ticket-clerk agent) with proper TYPE +
  AREA labels, and cross-reference it from the References section of both. An
  out-of-scope decision that lands nowhere is a decision that gets re-litigated.
- **Edit the existing analysis comment in place** (`mcp__forgejo__issue_comment_edit`)
  to record the decisions and correct anything you found stale in step 2. One
  analysis comment per ticket — do not post a second one. Fetch the current body
  and preserve it; the edit replaces the whole comment, so merge, never
  blind-overwrite.

## 5. Drop the label

**As soon as the ticket is actionable, remove `waiting-for-feedback`** (label id
9, `mcp__forgejo__issue_labels_remove`). Actionable means: every question that
blocked an implementor is answered and written into the description.

If the maintainer's answers open a *new* question, the ticket stays labelled and
you say so plainly — do not manufacture a done-state by dropping the label on a
ticket that is still blocked.

## 6. Report

Give the maintainer: what you verified against the repo (especially anything the
ticket claimed that turned out to be stale), what the description now says the
remaining work is, any follow-up tickets filed with their numbers, and whether
the label came off. Flag loose ends you deliberately did not act on.
