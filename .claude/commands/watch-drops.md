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

## Poll the "Bug reports" mailbox

Phone bug reports arrive as mail, not as files, so `fswatch` never sees them.
Alongside the file watcher, arm a **second persistent Monitor** that polls the
mailbox on an interval by running `herold bug-fetch` (see `/bug-inbox` step 0),
which writes each unread report into `~/herold-bugs/mail-<id>/` and marks the
mail read:

```sh
while :; do
  bin/herold bug-fetch --label "Bug reports" --out "${HEROLD_DROP_ROOT:-$HOME/herold-bugs}" 2>&1 \
    | grep -Ev 'no unread messages|no mailbox named|already present' \
    | sed 's/^bug-fetch: wrote /New herold drop: /'
  sleep 300
done
```

Skip arming the poller (and say so) when neither `~/.herold/credentials.toml`
nor `HEROLD_API_KEY` is present; do not arm a second poller if one already runs
in this session. Each `New herold drop: <dir>` line it emits is the same
standing instruction as a file-watcher event. A line naming a failure means the
mailbox could not be read on that pass: report it once and keep the poller
running.

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
- A phone drop without a description (`descriptionEntered: false` in its
  `report.json`) is never filed unattended: set its `STATUS` to
  `needs-description`, name it in the summary, and leave the description to
  the next interactive `/bug-inbox` run (its step 1b asks the maintainer).

This files tickets and posts Forgejo comments unattended. That is intended for
this command.

## Stopping

`TaskStop` the watcher Monitor and the mailbox poller, or end the session.
Re-invoke `/watch-drops` to start them again.
