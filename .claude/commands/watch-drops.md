---
description: Watch ~/Downloads/herold-bugs and auto-process each new herold-triage drop with /bug-inbox, no manual review
argument-hint: (none)
---

Arm a background watcher for herold-triage drops and process each new drop
automatically, without pausing for maintainer review. Start this manually when
you want unattended triage; it runs until the session ends.

## Start the watcher

If a drop watcher is not already running **in this session**, arm one:

- Ensure the watcher script exists at `~/herold-bugs/.watch-drops.sh` (create it
  from the template at the bottom if missing; `chmod` is not required, it is run
  via `bash`).
- Start a **persistent Monitor** running `bash "$HOME/herold-bugs/.watch-drops.sh"`.
  The script `fswatch`es `~/Downloads/herold-bugs/` and emits one line,
  `New herold drop: <file>`, per newly-arrived `*.heroldbug.json` bundle; the
  move to `processed/` that `/bug-inbox` performs produces no event.

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

## Script template (write to ~/herold-bugs/.watch-drops.sh if missing)

```bash
#!/usr/bin/env bash
# Watch the herold-triage drop inbox and emit one line per newly-arrived bundle.
# fswatch supplies kernel-event immediacy; the directory diff makes the emit
# exact (one line per genuinely-new *.heroldbug.json), and file removals -- the
# move to processed/ that /bug-inbox performs -- are ignored.
set -u
dir="$HOME/Downloads/herold-bugs"
mkdir -p "$dir"
cd "$dir" || exit 1

list() { ls -1 *.heroldbug.json 2>/dev/null | sort; }
seen=$(list)

report() {
  local cur
  cur=$(list)
  comm -13 <(printf '%s\n' "$seen") <(printf '%s\n' "$cur") | sed '/^$/d' | while IFS= read -r f; do
    echo "New herold drop: $f"
  done
  seen=$cur
}

# React to any change under the inbox; report() decides what is actually new.
while IFS= read -r -d '' _; do
  report
done < <(fswatch -0 "$dir")
```
