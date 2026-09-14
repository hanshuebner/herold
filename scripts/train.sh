#!/usr/bin/env bash
# train.sh -- the batch train.
#
# Agent commits land on the `train` branch. The orchestrator verifies the
# whole batch once with `train.sh verify` (the CI test lanes, the heavy
# pre-commit hooks and the web checks, run in a dedicated worktree on a
# host that is otherwise idle) and fast-forwards `main` with
# `train.sh ship`. One CI run and one deploy then cover the batch.
#
# Commands:
#   open              reset `train` to origin/main (refuses while unshipped
#                     commits sit on the train)
#   status            what is on the train, whether main moved, verify stamp
#   land <sha>...     cherry-pick commits onto the train and push them
#   verify            rebase the train onto origin/main, run `make
#                     verify-batch` in the train worktree, record the tip
#   ship              fast-forward main to the verified train tip
#   abort             drop the train's unshipped commits (needs --yes)
#
# Environment:
#   TRAIN_BRANCH      branch name (default: train)
#   TRAIN_WORKTREE    worktree used by verify/land
#                     (default: <repo>/.claude/worktrees/train)
#   HEROLD_PG_DSN     Postgres DSN for the batch gate; when unset, verify
#                     creates the throwaway database herold_train through
#                     `psql -U <user> -d postgres` and uses that
#   TRAIN_PG_ADMIN_USER
#                     psql superuser for creating the throwaway database
#                     (default: the current user)
#
# Contributors push with:
#   git fetch origin && git rebase origin/train && git push origin HEAD:train

set -euo pipefail

TRAIN=${TRAIN_BRANCH:-train}
REMOTE=origin

repo_root=$(git rev-parse --show-toplevel)
common_dir=$(git rev-parse --git-common-dir)
case "$common_dir" in
    /*) ;;
    *) common_dir="$repo_root/$common_dir" ;;
esac
stamp_file="$common_dir/train-verified"
worktree=${TRAIN_WORKTREE:-$repo_root/.claude/worktrees/train}

usage() {
    sed -n '2,32p' "$0" | sed 's/^# \{0,1\}//'
    exit 2
}

die() {
    echo "train: $*" >&2
    exit 1
}

fetch() {
    git fetch -q "$REMOTE" --prune
}

remote_train_exists() {
    git rev-parse -q --verify "refs/remotes/$REMOTE/$TRAIN" >/dev/null 2>&1
}

unshipped() {
    # Commits on the train that main does not have.
    git rev-list "$REMOTE/main..$REMOTE/$TRAIN"
}

main_ahead() {
    # Commits on main that the train does not have.
    git rev-list "$REMOTE/$TRAIN..$REMOTE/main"
}

cmd_open() {
    fetch
    if remote_train_exists && [ -n "$(unshipped)" ]; then
        die "$(unshipped | wc -l | tr -d ' ') unshipped commit(s) on $REMOTE/$TRAIN; run 'train.sh status', then ship or abort"
    fi
    git push -q --force-with-lease="refs/heads/$TRAIN" "$REMOTE" "$REMOTE/main:refs/heads/$TRAIN"
    rm -f "$stamp_file"
    echo "train: $TRAIN opened at $(git rev-parse --short "$REMOTE/main")"
}

cmd_status() {
    fetch
    remote_train_exists || die "$REMOTE/$TRAIN does not exist; run 'train.sh open'"
    local tip
    tip=$(git rev-parse "$REMOTE/$TRAIN")
    echo "train tip:  $(git rev-parse --short "$tip")"
    echo "main tip:   $(git rev-parse --short "$REMOTE/main")"
    local n
    n=$(unshipped | wc -l | tr -d ' ')
    echo "unshipped:  $n commit(s)"
    if [ "$n" -gt 0 ]; then
        git log --oneline "$REMOTE/main..$REMOTE/$TRAIN"
    fi
    if [ -n "$(main_ahead)" ]; then
        echo "main moved: $(main_ahead | wc -l | tr -d ' ') commit(s) on main are not on the train; verify rebases"
    fi
    if [ -f "$stamp_file" ]; then
        local stamped
        stamped=$(cat "$stamp_file")
        if [ "$stamped" = "$tip" ]; then
            echo "verified:   yes ($(git rev-parse --short "$stamped")), ready to ship"
        else
            echo "verified:   stale ($(git rev-parse --short "$stamped") is not the tip)"
        fi
    else
        echo "verified:   no"
    fi
}

ensure_worktree() {
    fetch
    remote_train_exists || die "$REMOTE/$TRAIN does not exist; run 'train.sh open'"
    if [ ! -d "$worktree" ]; then
        git worktree add -q "$worktree" "$REMOTE/$TRAIN"
    fi
    git -C "$worktree" checkout -q -B "$TRAIN" "$REMOTE/$TRAIN"
    if [ -n "$(git -C "$worktree" status --porcelain)" ]; then
        die "train worktree $worktree is dirty"
    fi
}

cmd_land() {
    [ $# -ge 1 ] || usage
    ensure_worktree
    git -C "$worktree" cherry-pick -x "$@"
    git -C "$worktree" push -q "$REMOTE" "$TRAIN:$TRAIN"
    echo "train: landed $# commit(s); tip $(git -C "$worktree" rev-parse --short HEAD)"
}

rebase_train() {
    if [ -n "$(main_ahead)" ]; then
        echo "train: rebasing onto $REMOTE/main ($(main_ahead | wc -l | tr -d ' ') new commit(s) on main)"
        git -C "$worktree" rebase -q "$REMOTE/main"
        git -C "$worktree" push -q --force-with-lease="refs/heads/$TRAIN:refs/remotes/$REMOTE/$TRAIN" "$REMOTE" "$TRAIN:$TRAIN"
        fetch
    fi
}

ensure_pg_dsn() {
    if [ -n "${HEROLD_PG_DSN:-}" ]; then
        return
    fi
    local admin=${TRAIN_PG_ADMIN_USER:-$(id -un)}
    if ! command -v psql >/dev/null 2>&1; then
        die "HEROLD_PG_DSN is unset and psql is not installed; the batch gate needs both backends"
    fi
    if ! psql -U "$admin" -d postgres -tAc "select 1" >/dev/null 2>&1; then
        die "HEROLD_PG_DSN is unset and psql -U $admin cannot reach the local server; the batch gate needs both backends"
    fi
    if [ "$(psql -U "$admin" -d postgres -tAc "select 1 from pg_database where datname='herold_train'")" != "1" ]; then
        psql -U "$admin" -d postgres -qc "CREATE DATABASE herold_train OWNER herold"
    fi
    export HEROLD_PG_DSN="postgres://herold:herold@127.0.0.1:5432/herold_train?sslmode=disable"
    echo "train: postgres leg uses herold_train"
}

cmd_verify() {
    ensure_worktree
    rebase_train
    local tip
    tip=$(git -C "$worktree" rev-parse HEAD)
    [ "$tip" = "$(git rev-parse "$REMOTE/$TRAIN")" ] || die "train worktree is not at $REMOTE/$TRAIN"
    if [ -z "$(unshipped)" ]; then
        echo "train: nothing to verify, train equals main"
        exit 0
    fi
    ensure_pg_dsn
    rm -f "$stamp_file"
    echo "train: verifying $(unshipped | wc -l | tr -d ' ') commit(s), tip $(git rev-parse --short "$tip")"
    git log --oneline "$REMOTE/main..$tip"
    local started
    started=$(date +%s)
    if (cd "$worktree" && make verify-batch); then
        echo "$tip" >"$stamp_file"
        echo "train: verified $(git rev-parse --short "$tip") in $(( $(date +%s) - started ))s; run 'train.sh ship'"
    else
        echo "train: batch gate failed for $(git rev-parse --short "$tip")" >&2
        echo "train: find the culprit with: git -C $worktree bisect start $tip $REMOTE/main; git bisect run make verify-batch" >&2
        exit 1
    fi
}

cmd_ship() {
    fetch
    remote_train_exists || die "$REMOTE/$TRAIN does not exist"
    local tip
    tip=$(git rev-parse "$REMOTE/$TRAIN")
    [ -f "$stamp_file" ] || die "no verified tip recorded; run 'train.sh verify'"
    [ "$(cat "$stamp_file")" = "$tip" ] || die "verified tip $(git rev-parse --short "$(cat "$stamp_file")") is not the train tip $(git rev-parse --short "$tip"); run 'train.sh verify'"
    git merge-base --is-ancestor "$REMOTE/main" "$tip" || die "main moved since verification; run 'train.sh verify'"
    local n
    n=$(unshipped | wc -l | tr -d ' ')
    git push -q "$REMOTE" "$tip:main"
    rm -f "$stamp_file"
    echo "train: shipped $n commit(s); main is at $(git rev-parse --short "$tip")"
}

cmd_abort() {
    [ "${1:-}" = "--yes" ] || die "abort drops every unshipped commit on $REMOTE/$TRAIN; re-run with --yes"
    fetch
    git push -q --force-with-lease="refs/heads/$TRAIN" "$REMOTE" "$REMOTE/main:refs/heads/$TRAIN"
    rm -f "$stamp_file"
    echo "train: $TRAIN reset to $(git rev-parse --short "$REMOTE/main")"
}

[ $# -ge 1 ] || usage
cmd=$1
shift
case "$cmd" in
    open) cmd_open "$@" ;;
    status) cmd_status "$@" ;;
    land) cmd_land "$@" ;;
    verify) cmd_verify "$@" ;;
    ship) cmd_ship "$@" ;;
    abort) cmd_abort "$@" ;;
    *) usage ;;
esac
