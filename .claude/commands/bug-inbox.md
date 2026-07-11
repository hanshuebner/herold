---
description: Triage local bug-reporter drops in ~/herold-bugs and file them as Forgejo tickets
argument-hint: (none) | <drop-id>
---

Process bug reports captured by the in-browser bug reporter. The extension writes
each report to disk with no server: a self-contained bundle lands in
`~/Downloads/herold-bugs/<id>.heroldbug.json`. This command expands each bundle
into a drop directory under `~/herold-bugs/<id>/`, then files it as a ticket.
(If the optional `herold bug-sink` server is used instead, drops appear directly
under `~/herold-bugs/` already expanded and this expansion step is a no-op.)

## Hard rule: the private zone never enters a ticket

Every drop has a `private/` subdirectory holding repro-only secrets (session
cookies, app state). It exists so you can REPRODUCE the bug locally. It must
NEVER be read into a ticket body, a comment, or any Forgejo API call. Only
`report.md`, `report.json`, `logs.txt`, and the `screenshot-*.png` files are
ticket-eligible. Do not open `private/` unless the maintainer explicitly asks
you to reproduce the bug.

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

For each drop, read `report.json`. Report the work-list to the maintainer before
filing: drop id, kind (bug/feature), the first line of the sketch, the app +
principal, and the screenshot count.

## 2. Per drop: dedup, then file

For each drop, in order:

1. **Dedup.** Search open issues (`mcp__forgejo__issue_list`, and keyword search)
   for an existing ticket describing the same problem. If one clearly matches,
   do NOT open a duplicate: note the match, and instead add the new screenshots
   and any new detail as a single comment on that issue. Then go to step 3.

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
   drop — the `private/` bundle stays on disk for repro.

## 3. Report

Relay, per drop: the drop id, the issue number + URL filed or commented on, the
labels applied, and whether screenshots were attached. If any drop failed to
file, leave its `STATUS` as `new` and say which one and why.

## Reproducing (only on explicit request)

If the maintainer asks you to reproduce a specific drop, THEN you may read its
`private/private.json` to recover the session cookies and app state, and drive
puppeteer against an instance with that session. This data stays local; it does
not go into the issue.
