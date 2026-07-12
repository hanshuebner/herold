---
description: Triage local herold-triage drops in ~/herold-bugs -- file reports as Forgejo tickets and apply review decisions
argument-hint: (none) | <drop-id>
---

Process drops captured by the in-browser herold-triage panel. The extension
writes each drop to disk with no server: a self-contained bundle lands in
`~/Downloads/herold-bugs/<id>.heroldbug.json`. A drop is one of two kinds:

- a **report** (`meta.kind` is `bug` or `feature`) -- file it as a new ticket;
- a **review hand-back** (`meta.kind` is `review`) -- comment on the named
  ticket with the captured screenshots, dropping it back into the fix loop. The
  panel already strips `waiting-for-feedback` against Forgejo at hand-back time,
  so the drop exists to carry the comment and screenshots the panel cannot
  attach. (A verified close is likewise applied in the panel directly against
  Forgejo and never reaches a drop.)

This command expands each bundle into a drop directory under `~/herold-bugs/<id>/`,
then processes it by kind. (If the optional `herold bug-sink` server is used
instead, drops appear directly under `~/herold-bugs/` already expanded and this
expansion step is a no-op.)

## Hard rule: the private zone never enters a ticket

Every drop has a `private/` subdirectory holding repro-only secrets (session
cookies, app state). It exists so you can REPRODUCE the bug locally. It must
NEVER be read into a ticket body, a comment, or any Forgejo API call. Only
`report.md`, `report.json`, `logs.txt`, and the `screenshot-*.png` files are
ticket-eligible. Do not open `private/` unless the maintainer explicitly asks
you to reproduce the bug. This applies to review drops exactly as to reports.

## 0. Expand downloaded bundles

For each `~/Downloads/herold-bugs/*.heroldbug.json` (a JSON object with `id`,
`meta`, `private`, and `screenshots[]` where each screenshot is `{name, dataUrl}`),
if `~/herold-bugs/<id>/` does not already exist, expand it:

- create `~/herold-bugs/<id>/` (mode 0700),
- write `meta` to `report.json`,
- write `private` to `private/private.json` (create `private/` mode 0700),
- for each screenshot, strip the `data:image/png;base64,` prefix and base64-decode
  the remainder into `<name>` (e.g. `screenshot-1.png`),
- write `new` to `STATUS`,
- move the source `.heroldbug.json` to `~/Downloads/herold-bugs/processed/` so it
  is not expanded again.

Use small shell/`jq`/`base64 -d` steps; do not read the private cookie values
into your reasoning or output.

## 1. Build the work-list

List `~/herold-bugs/*/` whose `STATUS` file contains `new`, oldest first by
directory name. If `$ARGUMENTS` names a specific drop id, process only that one.
If nothing is `new`, say so and stop.

For each drop, read `report.json` and note `meta.kind`. Report the work-list to
the maintainer before acting:

- reports: drop id, kind (bug/feature), the first line of `meta.sketch`, the app
  + principal, and the screenshot count;
- reviews: drop id, `kind=review`, the target issue `#meta.review.issue`, and
  the screenshot count. These are hand-backs; the reviewer's note is
  `meta.review.comment`.

## 2a. Report drops: dedup, then file

For each report drop (`meta.kind` in `bug` / `feature`), in order:

1. **Dedup.** Search open issues (`mcp__forgejo__issue_list`, and keyword search)
   for an existing ticket describing the same problem. If one clearly matches,
   do NOT open a duplicate: note the match, and instead add the new screenshots
   and any new detail as a single comment on that issue. Then record the outcome.

2. **File via ticket-clerk.** Dispatch the **ticket-clerk** agent
   (`subagent_type: "ticket-clerk"`) with: the sketch, the `context` and `logs`
   from `report.json`, the page URL, and the absolute paths of the
   `screenshot-*.png` files. Instruct it to:
   - write a lean house-style ticket from the report,
   - apply exactly one TYPE label (`bug` for kind=bug, `enhancement` for
     kind=feature) and one AREA label,
   - **embed every screenshot** by uploading each to the Forgejo issue-asset
     endpoint with `$FORGEJO_TOKEN` via curl (the MCP/fj paths cannot attach
     binaries), then reference the returned attachment URLs in the body,
   - include the page URL and a compact "Environment" line (app name+version,
     principal label) from the report,
   - NOT read or reference anything under `private/`.

3. **Record the outcome.** Write the filed issue number back to the drop's
   `STATUS` file as `filed:#<N>` (or `comment:#<N>` when you commented on an
   existing issue). This prevents re-filing on the next run. Never delete the
   drop -- the `private/` bundle stays on disk for repro.

## 2b. Review hand-backs: comment (the label is already stripped)

For each review drop (`meta.kind` is `review`), in order. The target is
`meta.review.issue`. These are hand-backs -- tickets the reviewer sent back as
still-broken. No dedup step -- a review names its ticket.

The panel strips `waiting-for-feedback` directly against Forgejo the moment the
reviewer hands a ticket back, so a review drop normally arrives with the label
already gone. That is the expected state, not an anomaly: this command's job is
to post the reviewer's comment and screenshots (which the panel cannot attach).

1. **Sanity-check the target.** Read the issue (`mcp__forgejo__issue_get`). A
   missing `waiting-for-feedback` label is expected (the panel already stripped
   it) -- do NOT halt on that. Only halt if the issue is already **closed** or
   the target number does not exist / does not match the reviewer's note: note
   the mismatch, leave `STATUS` as `new`, and report it. Otherwise continue.

2. **Compose the comment.** Body = the reviewer's `meta.review.comment`, followed
   by the embedded screenshots. Upload each `screenshot-*.png` to the Forgejo
   issue-asset endpoint with `$FORGEJO_TOKEN` via curl (the MCP/fj paths cannot
   attach binaries) and reference the returned attachment URLs. Prefix the line
   "Still failing -- handed back." Post it with
   `mcp__forgejo__issue_comment_create`. Do NOT read or reference anything under
   `private/`.

3. **Ensure the label is off.** The panel normally strips `waiting-for-feedback`
   at hand-back time, so this is usually a no-op. If the issue still carries it
   (an older panel, or a drop that predates that behaviour), remove it
   (`mcp__forgejo__issue_labels_remove`) so the ticket re-enters the bugfix
   queue. If it is already gone, do nothing.

4. **Record the outcome.** Write the drop's `STATUS` as `reviewed:#<N>`. Never
   delete the drop.

If a drop unexpectedly carries `meta.review.action: "close"` (a legacy drop --
the panel now closes directly), additionally set the issue closed
(`mcp__forgejo__issue_set_state` to `closed`) and record `STATUS` as
`closed:#<N>`.

## 3. Report

Relay, per drop:

- reports: the drop id, the issue number + URL filed or commented on, the labels
  applied, and whether screenshots were attached;
- reviews: the drop id, the target issue, that the hand-back comment was posted
  and the label stripped, and whether screenshots were attached.

If any drop failed, leave its `STATUS` as `new` and say which one and why.

## Reproducing (only on explicit request)

If the maintainer asks you to reproduce a specific drop, THEN you may read its
`private/private.json` to recover the session cookies, app state, and the
captured browser console (`private.console`, un-redacted -- useful for
diagnosing without re-reproducing), and drive puppeteer against an instance with
that session. This data stays local; it does not go into the issue.
