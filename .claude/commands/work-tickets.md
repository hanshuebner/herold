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

For each issue in the work-list, in order, dispatch the **bugfix-issues** agent
via the Agent tool (`subagent_type: "bugfix-issues"`) with a prompt naming the
specific issue number, e.g. "Work issue #N on herold/herold." The agent
reproduces the bug, posts an analysis checklist, ships a focused fix-commit
pushed to `main`, comments the result, and labels the issue
`waiting-for-feedback`. It never closes issues.

**Hold each fix to its verification gate.** The recurring cause of rework is a
fix declared "verified" on evidence that cannot see the defect — a green unit
run, a computed-style read, or a headless pass that never exercised the real
failure. In the prompt, instruct the agent to classify the bug and clear the
matching gate before reporting done, and to name anything it could not verify:

- **Visual / perceptual** (font, contrast, spacing, alignment, overflow): a
  real-browser screenshot is mandatory and must be attached to the post-fix
  comment; a `getComputedStyle` read is not sufficient.
- **Real-device / OS-integration** (notifications, service-worker lifecycle,
  clipboard, pointer/scroll, external OAuth round-trips): often not reproducible
  in headless puppeteer. The agent must add a definitive test or instrumented
  logging and state plainly what it verified and what needs your device.
- **Derived / precomputed / cached value** (badge counts, hasAttachment, thread
  membership, FTS): fix the live path AND backfill the persisted value; verify
  on a pre-existing row, not just a freshly created one.
- **Multi-site / i18n / shared-helper**: grep every call site; "applied
  everywhere" is a grep-backed claim, not an assumption.
- **Auth / session / CSRF** (especially the `/admin/` public-listener seam):
  confirm the correct cookie set and the `X-CSRF-Token` path.

When an agent returns, sanity-check that its post-fix comment carries evidence
proportional to the bug class. If a visual fix has no attached screenshot, or an
OS-integration fix claims a headless verification, treat it as not done and send
the agent back (via SendMessage) to clear the gate before moving on.

Run the agents **sequentially**, not in parallel: each one commits and pushes to
`main`, and concurrent pushes plus shared-working-tree edits race. (If you do
want parallelism, every agent MUST run with `isolation: "worktree"` — but
sequential is the default here.)

Some tickets are enhancements or deep subsystem work rather than focused
bug-fixes. If `bugfix-issues` reports that an issue is out of its scope (needs a
specialist or a design call), note it and move on — do not force a fix. Surface
those for routing at the end.

## 3. Report

When the queue is drained, summarize: which issues got a pushed fix, which were
routed/deferred and why, and which (if any) the agent could not reproduce and
asked the reporter about. Each processed issue should now carry
`waiting-for-feedback`; confirm the queue would come back empty on a re-run.
