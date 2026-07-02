---
description: Drain the herold open-issue queue, skipping waiting-for-feedback and deferred
argument-hint: (none)
---

Work through every actionable open ticket in the herold Forgejo repo
(`herold/herold` at code.netzhansa.com).

## 1. Build the work-list

List all open issues with `mcp__forgejo__issue_list` (`state: "open"`, raise
`limit` as needed). Drop any issue that carries the `waiting-for-feedback` or
`deferred` label — those are not actionable this pass. Sort the survivors
ascending by number (oldest first; they have waited longest).

Report the resulting work-list to me before processing: issue number, title, and
labels for each. If the list is empty, say so and stop.

## 2. Process each ticket

Process the work-list **one issue at a time, sequentially** — each fix commits
and pushes to `main`, and concurrent pushes plus shared-working-tree edits race.
(If you genuinely want parallelism, every agent MUST run with `isolation:
"worktree"` — but sequential is the default.)

For each issue, run this fix → verify → (retry once) → label cycle:

### 2a. Fix

Dispatch the **bugfix-issues** agent (`subagent_type: "bugfix-issues"`) with a
prompt naming the issue: "Work issue #N on herold/herold." Instruct it to:

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
- **NOT apply the `waiting-for-feedback` label** for a shipped fix — the label
  is applied in step 2c after verification. (For cannot-reproduce or routing
  outcomes there is nothing to verify: the agent labels `waiting-for-feedback`
  itself and you skip to the next issue.)

### 2b. Verify (independent)

When bugfix-issues returns having shipped a fix, dispatch the **fix-verifier**
agent (`subagent_type: "fix-verifier"`) with the issue number and the fix commit
SHA. It independently checks the fix against the grounding discipline, the
bug-class gate, and STANDARDS, and returns `VERDICT: PASS | DEVIATIONS` with a
concrete deviation list. Do not skip this step and do not substitute your own
glance for it — the independent pass is the point.

### 2c. Retry once, then label

- **PASS** → apply `waiting-for-feedback` (`mcp__forgejo__issue_labels_add`) and
  move to the next issue.
- **DEVIATIONS** → send the bugfix-issues agent back **once** (via SendMessage
  to the same agent) with the verifier's deviation list, to address every item.
  When it returns, re-run **fix-verifier** on the new state.
  - If the second verdict is **PASS** → apply `waiting-for-feedback`.
  - If the second verdict is still **DEVIATIONS** → apply BOTH
    `waiting-for-feedback` AND `fix-failed`, so the maintainer knows this one
    did not clear verification and needs investigation. Record the outstanding
    deviations for the final report.

Only ONE retry round. Do not loop indefinitely; a fix that fails twice gets
`fix-failed` and is surfaced, not endlessly re-attempted.

### Out of scope

If bugfix-issues reports an issue is out of its scope (needs a specialist or a
design call), do not force a fix. It labels `waiting-for-feedback`; note the
issue for routing in the final report and move on.

## 3. Report

When the queue is drained, summarize:

- Which issues **passed verification** on the first fix.
- Which needed a **retry**, and whether the retry passed.
- Which are labeled **`fix-failed`** (failed verification twice) — list the
  outstanding deviations so the maintainer knows where to look.
- Which were **routed/deferred** (out of scope) and why.
- Which the agent **could not reproduce** and asked the reporter about.

Every processed issue should now carry `waiting-for-feedback` (fix-failed ones
carry both). Confirm the queue would come back empty on a re-run.
