---
description: Drain the herold open-issue queue — fix bugs, implement enhancements, park decision-blocked tickets
argument-hint: (none)
---

Work through every actionable open ticket in the herold Forgejo repo
(`herold/herold` at code.netzhansa.com).

## Comment discipline — read this first

**Tickets carry work product, never process narration.** The maintainer reads
tickets to learn what a feature is and what is left to do — not to learn what an
agent did or did not do on a given pass. So:

- **Never post a comment about the pass itself.** No "triage (bugfix-agent
  pass)", no "this is out of scope for a bugfix pass", no "no code changed", no
  "routing to `<agent>`", no "recommend the root agent picks this up". Whether an
  agent worked a ticket, and why it declined, is a fact about the *drain*, not
  about the *work* — it belongs in your final report to the maintainer and
  nowhere else.
- **Analysis that a future implementor needs goes into the DESCRIPTION**, as a
  refinement — a sharpened Work section, a named prerequisite, an Open questions
  section. A finding worth keeping is worth putting where the next person will
  read it.
- The one comment a ticket may carry is the **single substantive analysis
  comment** on a bug being fixed: the reproduction, the checklist, and the
  post-fix result, edited in place. That comment describes the *defect*, not the
  agent.

If you catch yourself writing a sentence whose subject is an agent, delete it.

## 1. Build the work-list

List all open issues with `mcp__forgejo__issue_list` (`state: "open"`). **The
tool paginates at ~30 issues per page regardless of the `limit` you pass** — a
single call silently returns only the newest page and drops every older issue.
You MUST page through the whole set: call it with `page: 1`, `page: 2`, ...
(keep `limit: 50`) until a page comes back empty, and union the results. Verify
you reached the end (last page shorter than a full page, or an empty page
follows) before you trust the list — an under-fetched work-list makes the drain
skip the oldest tickets, which are exactly the ones that have waited longest.

Drop any issue carrying `waiting-for-feedback`, `decisions-required`,
`deferred`, or `superseded` — the ball is not in your court on those. Also set
aside any pure **tracking epic** (an umbrella issue with no buildable Work
section of its own, whose real work lives in child tickets) — surface it in the
report but do not dispatch an implementor to "build the epic." Sort the
survivors ascending by number (oldest first; they have waited longest).

Report the resulting work-list to me before processing: issue number, title,
labels, and the class you assigned it in step 2. If the list is empty, say so
and stop.

## 2. Classify before you dispatch

The drain is not a bugfix queue. Read each issue in full (`issue_get` plus
`issue_comments_list` with `full: true`) and put it in exactly one class:

- **Defect** — something is broken and a reporter has observed it. Has a
  reproduction, or one can be constructed. Typically labelled `bug`.
- **Enhancement** — new behaviour that does not exist yet. Typically labelled
  `enhancement`. There is nothing to reproduce; the ticket's Work section (or the
  gap where it should be) is the target.
- **Decision-blocked** — the ticket cannot be built to a single obvious outcome
  because a call only the maintainer can make is open: a scope cut, a mechanism
  choice, a schema question, a "do we even want this". This class cuts across the
  other two — a defect whose *correct behaviour* is undecided is decision-blocked,
  not a defect.

Classify on the ticket's content, not its labels — labels are frequently missing
or wrong. If a ticket is genuinely two tickets, split it (`issue_create`, proper
type + area labels, cross-reference both) rather than forcing one class on it.

## 3. Route by class

Process the work-list **one issue at a time, sequentially** — each fix commits
and pushes to `main`, and concurrent pushes plus shared-working-tree edits race.
(If you genuinely want parallelism, every agent MUST run with `isolation:
"worktree"` — but sequential is the default.)

### 3a. Defect -> bugfix-issues

Dispatch the **bugfix-issues** agent (`subagent_type: "bugfix-issues"`): "Work
issue #N on herold/herold." Instruct it to:

- Read the issue AND every comment, and address the **latest** maintainer
  feedback (bounced tickets are the norm here — the opening description is often
  not the current target).
- **Reproduce/observe the reported failure before touching code.** If the
  environment cannot exercise the failure (e.g. a dev instance that lacks the
  flow's config), that is a cannot-reproduce condition — stop and report it, do
  not ship a speculative fix.
- Clear the verification gate for the bug's class and name anything it could not
  verify (the gates are listed in `bugfix-issues.md`; the recurring miss is a
  "verified" claim on evidence that cannot see the defect — a green unit run, a
  `getComputedStyle` read, a headless pass over an OS-integration flow).
- Post the analysis checklist as a SINGLE comment and **edit that same comment**
  with the results — do NOT post a second comment.
- Push a focused fix-commit to `main` (`re #N`, never an auto-closing keyword).
- **NOT apply the `waiting-for-feedback` label** for a shipped fix — the label is
  applied in step 4 after verification. (For a cannot-reproduce outcome there is
  nothing to verify: the agent asks the reporter in its analysis comment, labels
  `waiting-for-feedback` itself, and you skip to the next issue.)

### 3b. Enhancement -> the owning specialist

An enhancement is real work, and the drain's job is to advance it, not to
observe that it is not a bug.

Read `AGENTS.md` and dispatch the implementor that owns the subsystem the ticket
touches (`jmap-implementor`, `storage-implementor`, `web-frontend-implementor`,
`mobile`, ...). Prompt it with the issue number and instruct it to:

- Read the issue and every comment; build what the **Work** and **Acceptance**
  sections describe.
- Follow the project's convergence rules: where the feature depends on an
  external service the dev instance does not provide, **the in-tree fake plus its
  CI end-to-end test is the first work item**, not an afterthought.
- Ship a focused commit to `main` (`re #N`, never an auto-closing keyword), with
  the acceptance check green.
- **Post nothing on the ticket** unless it is refining the description.

**When the ticket spans two or more subsystems** (the AGENTS.md rule: a task
crossing one owner's boundary is coordinated from the root agent), you sketch the
interface — the enum arm, the delivery contract, the fake's shape — and dispatch
the two sides. That coordination happens in this conversation and in the ticket's
description. It does not happen in a comment.

**When the enhancement's Work section is too thin to build from**, that is not a
routing problem, it is a decision-blocked ticket: go to 3c.

### 3c. Decision-blocked -> sharpen the ticket, label, move on

Do not force work out of a ticket whose target is undecided, and do not write a
comment explaining that you didn't. Instead, leave the ticket better than you
found it:

1. **Ground your questions in the repo first.** Every question the code can
   answer, answer yourself — whether a seam exists, whether a schema column is
   already there, whether a commit landed. Only what the repo genuinely cannot
   settle is a maintainer decision. (Guessing which questions are open is how
   tickets accumulate false premises.)
2. **Edit the description** (`issue_edit`) to fold in what you established, and
   add an **Open questions** section: each question stated affirmatively, with
   the concrete options and what each one costs. Keep the description
   present-tense and free of pass history — it states the work as it now stands.
3. **Label `decisions-required`** (`issue_labels_add`, id 1105). Do NOT label
   `waiting-for-feedback` — that label means "a fix shipped, please verify", and
   using it here is what buries decision-blocked tickets where no pass ever looks
   at them again.
4. Move on. The `/unblock` command drains the `decisions-required` queue, puts
   the questions to the maintainer, and folds the answers back into the
   description — after which the ticket returns to this drain as actionable.

## 4. Verify and label (shipped work only)

When bugfix-issues or a specialist returns having shipped a commit, dispatch the
**fix-verifier** agent (`subagent_type: "fix-verifier"`) with the issue number
and the commit SHA. It independently checks the work against the grounding
discipline, the bug-class gate, and STANDARDS, and returns `VERDICT: PASS |
DEVIATIONS`. Do not skip this and do not substitute your own glance for it — the
independent pass is the point.

- **PASS** -> apply `waiting-for-feedback` (`issue_labels_add`) and move to the
  next issue.
- **DEVIATIONS** -> send the agent back **once** (via SendMessage to the same
  agent) with the deviation list, to address every item. Re-run **fix-verifier**
  on the new state.
  - Second verdict **PASS** -> apply `waiting-for-feedback`.
  - Second verdict still **DEVIATIONS** -> apply BOTH `waiting-for-feedback` AND
    `fix-failed`, so the maintainer knows this one did not clear verification.
    Record the outstanding deviations for the final report.

Only ONE retry round. A fix that fails twice gets `fix-failed` and is surfaced,
not endlessly re-attempted.

## 5. Report

The report is where everything the tickets do not carry goes. When the queue is
drained, tell me:

- Which issues **passed verification** on the first pass.
- Which needed a **retry**, and whether it passed.
- Which are labelled **`fix-failed`** — with the outstanding deviations.
- Which are now **`decisions-required`** — and, for each, the one-line summary of
  what I have to decide. This is the list I act on with `/unblock`.
- Which **could not be reproduced**, and what the reporter was asked for.
- Any ticket you **split**, with the new issue numbers.

Every processed issue now carries exactly one queue label: `waiting-for-feedback`
(work shipped or a question asked, ball in the maintainer's court) or
`decisions-required` (blocked on a decision). Confirm the queue would come back
empty on a re-run.
