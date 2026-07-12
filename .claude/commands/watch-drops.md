---
description: Watch ~/Downloads/herold-bugs and auto-process each new herold-triage drop with /bug-inbox, no manual review
argument-hint: (none)
---

Arm a background watcher for herold-triage drops and process each new drop
automatically, without pausing for maintainer review. Start this manually when
you want unattended triage; it runs until the session ends.

## Start the watcher

The watcher script lives in the bug-reporter (herold-triage) repo, which owns
the drop format and lifecycle: `~/Development/privat/bug-reporter/scripts/watch-drops.sh`.

If a drop watcher is not already running **in this session**, arm one:

- Start a **persistent Monitor** running
  `bash "$HOME/Development/privat/bug-reporter/scripts/watch-drops.sh"`. The
  script `fswatch`es `~/Downloads/herold-bugs/` and emits one line,
  `New herold drop: <file>`, per newly-arrived `*.heroldbug.json` bundle; the
  move to `processed/` that `/bug-inbox` performs produces no event.
- If the script is missing, the bug-reporter repo is not checked out at that
  path -- clone/update it rather than recreating the script here (it is the
  single source of truth).

Do NOT arm a second watcher if one is already running this session -- duplicate
watchers double-process. Confirm it is armed, then stop and wait for events.

## On each "New herold drop" event

A Monitor event of the form `New herold drop: <file>` is a standing instruction
to drain the inbox, not a maintainer message. When one arrives, immediately run
the **`/bug-inbox`** flow end to end, fully autonomously:

- Process ALL drops currently marked `new` (a burst may deliver several at once,
  and the event only names one) -- expand any pending bundles first.
- File report drops as tickets via the **ticket-clerk** agent, and
  comment/handle review drops, exactly as `/bug-inbox` specifies (dedup,
  screenshot upload via `$FORGEJO_TOKEN`, `STATUS` recording, the private-zone
  hard rule -- all still apply).
- Do NOT report a work-list for confirmation and do NOT wait for approval:
  process to completion, then post a short summary of what was filed/commented.

This files tickets and posts Forgejo comments unattended. That is intended for
this command.

## Stopping

`TaskStop` the watcher Monitor, or end the session. Re-invoke `/watch-drops` to
start it again.
