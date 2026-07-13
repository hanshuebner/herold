---
description: Unblock stalled herold tickets — ask the maintainer the open decisions, fold the answers into the description, drop the label
argument-hint: [issue number] (omit to drain the whole decisions-required queue)
---

Unblock stalled tickets in the herold Forgejo repo (`herold/herold` at
code.netzhansa.com).

A ticket is stalled when it cannot be built to a single obvious outcome until the
maintainer makes a call only they can make. Your job is to get those calls made
and leave each ticket in a state an implementor picks up without asking anything.

Do not fix the tickets. Do not write code. This command ends with actionable
tickets, not a commit.

## Mode

- **`/unblock <N>`** — work issue #N, whatever its labels.
- **`/unblock`** (no argument) — **drain the queue**: list open issues
  (`mcp__forgejo__issue_list`, `state: "open"`) and take every one carrying
  `decisions-required` (label id 1105), oldest first. That label is applied by
  `/work-tickets` when a pass finds a decision it cannot make; this command is
  the other half of that loop. If the queue is empty, say so and stop.

$ARGUMENTS

## 1. Read each ticket whole

Fetch the issue (`mcp__forgejo__issue_get`) and **every** comment
(`mcp__forgejo__issue_comments_list` with `full: true` — the truncated bodies
hide exactly the detail you need). The open questions normally live in the
description's **Open questions** section; a comment may carry more.

## 2. Ground the questions before you ask them

**Answer, from the repo, every question the repo can answer.** A ticket's
analysis is a snapshot that goes stale: it will claim work is unmerged when it
landed weeks ago, name a branch that no longer exists, or list a file that has
since moved. Asking the maintainer to decide on a false premise wastes the
round-trip this command exists to avoid.

So before formulating anything, verify the ticket's factual claims:

- Commits and branches it names: on `main` (`git merge-base --is-ancestor <sha>
  main`), still pending, or gone?
- Files, symbols, routes, migrations, schema columns it names: do they still
  exist as described?
- Tests it claims are green: do they exist?

State what you observed. If a claim is stale, that is a finding — the question
built on it may dissolve entirely, and the maintainer needs to know the ticket
was lying to them.

## 3. Ask the real decisions

Put the genuinely-open questions to the maintainer with **AskUserQuestion**.

A question earns a slot only if the answer changes what gets built and the repo
cannot settle it. Scope calls, mechanism choices, and in-scope/out-of-scope cuts
qualify. "Should I proceed?" does not.

For each question:
- 2-4 concrete, mutually exclusive options.
- Lead with your recommendation, suffixed `(Recommended)`.
- Each option's description says what it *costs* — what work it pulls in, what it
  defers, who it drags in. The maintainer is choosing between futures, not
  between labels.

**In drain mode, batch across tickets.** AskUserQuestion takes up to 4 questions
per call; fill the call. Read every ticket in the queue and ground its questions
FIRST, then ask in as few rounds as the queue allows — do not walk the maintainer
through one ticket at a time when six questions across four tickets could be two
rounds. If one ticket's answer would change another ticket's question, ask that
one first and say why.

## 4. Fold the answers into the ticket

Rewrite the description (`mcp__forgejo__issue_edit`) so it reads as the work that
now stands:

- **Affirmative, present-tense.** State what the ticket *is*, not how it got
  here. No "originally we planned X", no definition-by-negation against a rejected
  option, no trace of the deliberation — those only parse for someone who watched
  it happen.
- **Delivered vs. remaining.** If part of the work is already on `main`, say so
  with the commit SHAs, and give the rest its own section. An implementor must be
  able to tell in one read what is left.
- **Acceptance.** Give the ticket a green-check condition — the observable
  behaviour that proves it done. Per CLAUDE.md, "a fix was pushed" is not a
  done-state; a passing test or a driven flow is. Both SQLite and Postgres where
  the change touches the store.
- **Remove the Open questions section** as you answer it. A decided question is
  not a question; it is a requirement, and it belongs in Scope or Work.
- **References last.** No inline cross-ticket links in the prose; collect
  tickets, requirement IDs, and design docs into a References section, one line
  each with why it matters.

Then:

- **Answers that push work out of scope get their own ticket.** File it
  (`mcp__forgejo__issue_create`, or the ticket-clerk agent) with proper TYPE +
  AREA labels, and cross-reference it from the References section of both. An
  out-of-scope decision that lands nowhere is a decision that gets re-litigated.
- **Never narrate the process on the ticket.** No "the maintainer decided X on
  <date>", no "this was blocked pending a decision", no pass history. The
  decision shows up as a requirement in the description; that it was once open is
  not information the next reader needs. If an analysis comment exists and is now
  wrong, edit it in place to match reality (fetch the body first and merge — the
  edit replaces the whole comment) or delete it if it carries nothing the
  description does not.

## 5. Drop the label

**As soon as the ticket is actionable, remove `decisions-required`** (label id
1105, `mcp__forgejo__issue_labels_remove`), so the next `/work-tickets` pass
picks it up as work. Actionable means: every question that blocked an implementor
is answered and written into the description.

If the maintainer's answers open a *new* question, the ticket keeps the label and
you say so plainly — do not manufacture a done-state by dropping the label on a
ticket that is still blocked.

## 6. Report

Give the maintainer, per ticket: what you verified against the repo (especially
anything the ticket claimed that turned out to be stale), what the description
now says the remaining work is, any follow-up tickets filed with their numbers,
and whether the label came off. Close with the list of tickets that are now
actionable by `/work-tickets`, and flag loose ends you deliberately did not act
on.
