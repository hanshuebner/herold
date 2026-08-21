#!/usr/bin/env bash
# scripts/herold-agents.sh
#
# Bring up the herold triage/fix crew as one tmux session so the pieces start
# together, in the right places, with the right environment.
#
# Three targets, one tmux window each:
#
#   sink          `herold bug-sink` on $HEROLD_SINK_ADDR, writing expanded
#                 drops under $HEROLD_DROP_ROOT. The triage extension posts
#                 here; when it is down the extension falls back to a browser
#                 download and /bug-inbox expands the bundle itself.
#   bug-inbox     a Claude session running /watch-drops: it watches the drop
#                 inbox and drains each arrival through /bug-inbox.
#   work-tickets  a Claude session running /work-tickets: it drains the open
#                 ticket queue.
#
# The Claude windows run interactively, so `attach` lets you watch a run or
# answer a question and then detach again.
#
# Subcommands:
#   start [target]     Start everything, or one target. Already-running
#                      targets are left alone, so it is safe to re-run.
#   stop [target]      Stop everything (kills the session), or one target.
#   restart [target]   stop + start.
#   status             One line per target.
#   attach [target]    Attach to the session, selecting target's window.
#   doctor             Run the preflight checks and report, starting nothing.
#   dry-run [target]   Print the command and environment each window would get.
#                      Starts nothing and touches nothing.
#
# Environment:
#   HEROLD_AGENTS_SESSION   tmux session name           (herold-agents)
#   HEROLD_SINK_ADDR        bug-sink listen address     (127.0.0.1:7777)
#   HEROLD_DROP_ROOT        expanded drop root          ($HOME/herold-bugs)
#   HEROLD_DROPS_DIR        download bundle inbox       ($HOME/Downloads/herold-bugs)
#   BUG_REPORTER_DIR        bug-reporter checkout       ($HOME/Development/privat/bug-reporter)
#   HEROLD_AGENT_PERMISSION_MODE
#                           --permission-mode for the Claude windows
#                           (bypassPermissions). The crew exists to run
#                           unattended, so the windows do not stop for tool-call
#                           approval: they commit code and write to Forgejo on
#                           their own. Set acceptEdits to be asked instead.
#
# FORGEJO_TOKEN and the HEROLD_* settings are passed into each window with
# `tmux -e` rather than left to inheritance: a window opened on an already-
# running tmux server inherits that server's environment, which is whatever the
# shell that first started tmux happened to hold. Passing them explicitly makes
# the window environment the one `doctor` just checked. The token is visible in
# this script's argv while a window starts.
set -uo pipefail

SESSION="${HEROLD_AGENTS_SESSION:-herold-agents}"
SINK_ADDR="${HEROLD_SINK_ADDR:-127.0.0.1:7777}"
DROP_ROOT="${HEROLD_DROP_ROOT:-$HOME/herold-bugs}"
DROPS_DIR="${HEROLD_DROPS_DIR:-$HOME/Downloads/herold-bugs}"
BUG_REPORTER_DIR="${BUG_REPORTER_DIR:-$HOME/Development/privat/bug-reporter}"
PERM_MODE="${HEROLD_AGENT_PERMISSION_MODE:-bypassPermissions}"

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HEROLD_BIN="$REPO/bin/herold"
WATCHER="$BUG_REPORTER_DIR/scripts/watch-drops.sh"
TARGETS=(sink bug-inbox work-tickets)

die() { printf 'herold-agents: %s\n' "$*" >&2; exit 1; }
say() { printf '  %-13s %s\n' "$1" "$2"; }

is_target() {
  local t
  for t in "${TARGETS[@]}"; do [ "$t" = "$1" ] && return 0; done
  return 1
}

# --- health probes -----------------------------------------------------------

sink_listening() {
  local host="${SINK_ADDR%:*}" port="${SINK_ADDR##*:}"
  (exec 3<>"/dev/tcp/$host/$port") 2>/dev/null && { exec 3>&-; return 0; }
  return 1
}

session_exists() { tmux has-session -t "$SESSION" 2>/dev/null; }

window_exists() {
  session_exists || return 1
  tmux list-windows -t "$SESSION" -F '#{window_name}' 2>/dev/null | grep -qx "$1"
}

# True when the window exists and its pane still has a live process.
window_alive() {
  window_exists "$1" || return 1
  local dead
  dead=$(tmux list-panes -t "$SESSION:$1" -F '#{pane_dead}' 2>/dev/null | head -1)
  [ "$dead" = "0" ]
}

# --- preflight ---------------------------------------------------------------

doctor() {
  local fatal=0
  for tool in tmux claude fswatch; do
    if command -v "$tool" >/dev/null 2>&1; then
      say "$tool" "ok ($(command -v "$tool"))"
    else
      say "$tool" "MISSING"
      fatal=1
    fi
  done

  if [ -x "$HEROLD_BIN" ]; then
    say "herold bin" "ok ($HEROLD_BIN)"
  else
    say "herold bin" "MISSING -- run 'make build-server' in $REPO"
    fatal=1
  fi

  if [ -r "$WATCHER" ]; then
    say "watcher" "ok ($WATCHER)"
  else
    say "watcher" "MISSING -- clone bug-reporter to $BUG_REPORTER_DIR"
    fatal=1
  fi

  if mkdir -p "$DROP_ROOT" 2>/dev/null && [ -w "$DROP_ROOT" ]; then
    say "drop root" "ok ($DROP_ROOT)"
  else
    say "drop root" "NOT WRITABLE ($DROP_ROOT)"
    fatal=1
  fi

  # The download fallback only matters when it is readable; the sink path does
  # not need it, so an unreadable bundle inbox is a note, not a failure.
  if [ -d "$DROPS_DIR" ] && ls "$DROPS_DIR" >/dev/null 2>&1; then
    say "bundle inbox" "ok ($DROPS_DIR)"
  else
    say "bundle inbox" "unreadable -- sink path only ($DROPS_DIR)"
  fi

  if [ -n "${FORGEJO_TOKEN:-}" ]; then
    say "FORGEJO_TOKEN" "set"
  else
    say "FORGEJO_TOKEN" "UNSET -- screenshot upload in /bug-inbox will fail"
    fatal=1
  fi

  say "permissions" "$PERM_MODE"
  return $fatal
}

# --- window commands ---------------------------------------------------------

# Wrap a command so a crash leaves the failure on screen instead of closing the
# window out from under it.
hold() { printf '%s; printf "\\n[%s exited %%s] press enter to close\\n" "$?"; read -r' "$1" "$2"; }

# The slash command each Claude window drives.
slash_for() {
  case "$1" in
    bug-inbox) printf '/watch-drops' ;;
    work-tickets) printf '/work-tickets' ;;
  esac
}

# Environment handed to every window, so a stale tmux server cannot give the
# agents a different one than doctor validated.
tmux_env_args() {
  printf '%s\0' -e "HEROLD_DROP_ROOT=$DROP_ROOT" -e "HEROLD_DROPS_DIR=$DROPS_DIR"
  if [ -n "${FORGEJO_TOKEN:-}" ]; then
    printf '%s\0' -e "FORGEJO_TOKEN=$FORGEJO_TOKEN"
  fi
}

cmd_for() {
  case "$1" in
    sink)
      hold "cd $(printf %q "$REPO") && $(printf %q "$HEROLD_BIN") bug-sink --addr $(printf %q "$SINK_ADDR") --dir $(printf %q "$DROP_ROOT")" sink
      ;;
    bug-inbox)
      hold "cd $(printf %q "$REPO") && claude --permission-mode $(printf %q "$PERM_MODE") /watch-drops" bug-inbox
      ;;
    work-tickets)
      hold "cd $(printf %q "$REPO") && claude --permission-mode $(printf %q "$PERM_MODE") /work-tickets" work-tickets
      ;;
  esac
}

start_one() {
  local t="$1"
  if window_alive "$t"; then
    say "$t" "already running"
    return 0
  fi
  window_exists "$t" && tmux kill-window -t "$SESSION:$t" 2>/dev/null

  local envargs=()
  while IFS= read -r -d '' a; do envargs+=("$a"); done < <(tmux_env_args)

  if ! session_exists; then
    tmux new-session -d -s "$SESSION" -n "$t" -c "$REPO" "${envargs[@]}" "$(cmd_for "$t")"
  else
    tmux new-window -d -t "$SESSION" -n "$t" -c "$REPO" "${envargs[@]}" "$(cmd_for "$t")"
  fi

  if [ "$t" = "sink" ]; then
    local i
    for i in 1 2 3 4 5 6 7 8 9 10; do
      sink_listening && break
      sleep 0.3
    done
    if sink_listening; then
      say "$t" "started ($SINK_ADDR -> $DROP_ROOT)"
    else
      say "$t" "started but not listening on $SINK_ADDR -- check 'attach sink'"
    fi
  else
    say "$t" "started (claude $(slash_for "$t"))"
  fi
}

stop_one() {
  local t="$1"
  if window_exists "$t"; then
    tmux kill-window -t "$SESSION:$t" 2>/dev/null
    say "$t" "stopped"
  else
    say "$t" "not running"
  fi
}

status_one() {
  local t="$1"
  if [ "$t" = "sink" ]; then
    if sink_listening; then
      window_alive sink && say sink "running   ($SINK_ADDR -> $DROP_ROOT)" \
                        || say sink "listening on $SINK_ADDR, but not in this session"
    else
      say sink "stopped"
    fi
    return
  fi
  if window_alive "$t"; then
    say "$t" "running"
  elif window_exists "$t"; then
    say "$t" "exited (window kept; attach to read it)"
  else
    say "$t" "stopped"
  fi
}

# --- entry point -------------------------------------------------------------

# Reject a bad target in the parent shell: resolve_targets runs inside a
# process substitution, where exiting would only end the subshell.
validate_targets() {
  [ $# -eq 0 ] && return 0
  is_target "$1" || die "unknown target '$1' (want: ${TARGETS[*]})"
}

resolve_targets() {
  if [ $# -eq 0 ]; then
    printf '%s\n' "${TARGETS[@]}"
  else
    is_target "$1" || die "unknown target '$1' (want: ${TARGETS[*]})"
    printf '%s\n' "$1"
  fi
}

case "${1:-}" in
  start)
    shift
    validate_targets "$@"
    doctor || die "preflight failed; fix the items above (or run 'doctor')"
    echo
    while read -r t; do start_one "$t"; done < <(resolve_targets "$@")
    echo
    echo "  attach: tmux attach -t $SESSION"
    ;;
  stop)
    shift
    validate_targets "$@"
    if [ $# -eq 0 ]; then
      if session_exists; then
        tmux kill-session -t "$SESSION" 2>/dev/null
        say "$SESSION" "stopped"
      else
        say "$SESSION" "not running"
      fi
    else
      while read -r t; do stop_one "$t"; done < <(resolve_targets "$@")
    fi
    ;;
  restart)
    shift
    validate_targets "$@"
    while read -r t; do stop_one "$t"; done < <(resolve_targets "$@")
    while read -r t; do start_one "$t"; done < <(resolve_targets "$@")
    ;;
  status)
    if session_exists; then
      say "session" "$SESSION"
    else
      say "session" "$SESSION (not running)"
    fi
    while read -r t; do status_one "$t"; done < <(resolve_targets)
    ;;
  attach)
    shift
    session_exists || die "session '$SESSION' is not running; start it first"
    if [ $# -gt 0 ]; then
      is_target "$1" || die "unknown target '$1' (want: ${TARGETS[*]})"
      tmux select-window -t "$SESSION:$1" 2>/dev/null
    fi
    tmux attach -t "$SESSION"
    ;;
  dry-run)
    shift
    validate_targets "$@"
    say "session" "$SESSION"
    say "repo" "$REPO"
    say "permissions" "$PERM_MODE"
    echo
    echo "  window environment:"
    while IFS= read -r -d '' a; do
      [ "$a" = "-e" ] && continue
      case "$a" in
        FORGEJO_TOKEN=*) echo "    FORGEJO_TOKEN=<set, ${#a} chars incl. name>" ;;
        *) echo "    $a" ;;
      esac
    done < <(tmux_env_args)
    while read -r t; do
      echo
      echo "  [$t] would run:"
      echo "    $(cmd_for "$t")"
    done < <(resolve_targets "$@")
    ;;
  doctor)
    doctor && echo && echo "  preflight ok" || { echo; die "preflight failed"; }
    ;;
  ""|-h|--help|help)
    sed -n '2,45p' "${BASH_SOURCE[0]}" | sed 's|^# \{0,1\}||'
    ;;
  *)
    die "unknown command '${1}' (want: start stop restart status attach doctor dry-run)"
    ;;
esac
