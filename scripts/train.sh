#!/usr/bin/env bash
# train.sh -- the batch train.
#
# Agent commits land on the `train` branch. `.forgejo/workflows/ci.yml`
# runs on every push to `train`, the same jobs it runs on `main` (test,
# lint, pre-commit, conformance, fuzz, web, e2e), except the jobs that
# publish an artefact (docker, binaries, release), which run for `main`
# only. `train.sh verify` rebases the train onto origin/main, pushes, and
# watches that CI run to completion; `train.sh ship` then fast-forwards
# `main` to the verified tip. Because the train's CI run already proved
# the batch, the CI run that fires on `main` right after a ship is the
# run that produces the release and triggers the deploy -- it re-runs the
# same suite, but nothing further is gated on it passing again.
#
# Commands:
#   open              reset `train` to origin/main (refuses while unshipped
#                     commits sit on the train)
#   status            what is on the train, whether main moved, verify
#                     stamp, and the CI run for the tip when one exists
#   land <sha>...     cherry-pick commits onto the train and push them
#   verify            rebase the train onto origin/main, push, wait for
#                     the ci.yml run on the train tip to reach `success`,
#                     record the tip
#   verify --local    the old path: run `make verify-batch` in the train
#                     worktree instead of waiting on CI
#   ship              fast-forward main to the verified commit (the train
#                     may already carry newer, unverified commits)
#   abort             drop the train's unshipped commits (needs --yes)
#
# Environment:
#   TRAIN_BRANCH      branch name (default: train)
#   TRAIN_WORKTREE    worktree used by verify/land
#                     (default: <repo>/.claude/worktrees/train)
#   TRAIN_API_BASE    Forgejo API base URL (default:
#                     https://code.netzhansa.com/api/v1); override to
#                     point `verify`'s polling at a stub for testing
#   TRAIN_CI_POLL_INTERVAL
#                     seconds between CI status polls (default: 30)
#   FORGEJO_TOKEN     Forgejo API token for reading run/job status; falls
#                     back to ~/.config/cilog/token when unset
#   HEROLD_PG_DSN     Postgres DSN for `verify --local`'s batch gate; when
#                     unset, that path creates the throwaway database
#                     herold_train through `psql -U <user> -d postgres`
#   TRAIN_PG_ADMIN_USER
#                     psql superuser for creating the throwaway database
#                     used by `verify --local` (default: the current user)
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

api_base=${TRAIN_API_BASE:-https://code.netzhansa.com/api/v1}
poll_interval=${TRAIN_CI_POLL_INTERVAL:-30}
# How many consecutive run-status fetches may fail before the watch gives
# up. At the default interval that rides out six minutes of a forge or
# resolver outage while a run continues.
ci_fetch_attempts=${TRAIN_CI_FETCH_ATTEMPTS:-12}
ci_repo_owner=herold
ci_repo_name=herold
ci_workflow_id=ci.yml

usage() {
    sed -n '2,49p' "$0" | sed 's/^# \{0,1\}//'
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

# forgejo_token prints the API token on stdout and returns 0, or returns 1
# with nothing printed when none is configured. It does not die: callers
# that need the token (verify's CI wait) check explicitly and die with a
# clear message; callers for whom CI info is a nice-to-have (status) treat
# a missing token as "skip the CI line" rather than a hard failure.
forgejo_token() {
    if [ -n "${FORGEJO_TOKEN:-}" ]; then
        printf '%s' "$FORGEJO_TOKEN"
        return 0
    fi
    if [ -f "$HOME/.config/cilog/token" ]; then
        cat "$HOME/.config/cilog/token"
        return 0
    fi
    return 1
}

# api_get <path> issues an authenticated GET against $api_base and prints
# the JSON body. A generous timeout: the runs-listing endpoint embeds each
# run's full event_payload, which makes an unfiltered or large --limit
# request slow (tens of seconds for a couple dozen runs); find_ci_run
# below avoids that cost by filtering server-side instead.
api_get() {
    curl -fsS --max-time 90 -H "Authorization: token $(forgejo_token 2>/dev/null)" "${api_base}$1"
}

urlencode() {
    jq -rn --arg v "$1" '$v | @uri'
}

# find_ci_run <sha> prints the numeric run id of the ci.yml run for commit
# <sha> on the $TRAIN ref, or fails. The runs-listing endpoint supports
# server-side workflow_id/head_sha/ref filters, so this is a single small
# request rather than a client-side scan of the (event_payload-heavy)
# unfiltered listing.
find_ci_run() {
    local sha=$1 body id
    body=$(api_get "/repos/${ci_repo_owner}/${ci_repo_name}/actions/runs?workflow_id=$(urlencode "$ci_workflow_id")&head_sha=$(urlencode "$sha")&ref=$(urlencode "refs/heads/$TRAIN")") || return 1
    id=$(printf '%s' "$body" | jq -r '.workflow_runs[0]?.id // empty')
    [ -n "$id" ] || return 1
    printf '%s\n' "$id"
}

run_get() {
    api_get "/repos/${ci_repo_owner}/${ci_repo_name}/actions/runs/$1"
}

# run_get_persistent <run_id> prints the run JSON, re-trying while the
# fetch keeps failing. A CI run is watched for twenty minutes or more, so
# a transient DNS or network failure during that window is expected;
# ending the watch on one would discard a run that is still progressing
# and leave the batch unstamped even when it goes on to succeed. Only
# $ci_fetch_attempts consecutive failures end it, which distinguishes a
# blip from the forge being genuinely unreachable.
run_get_persistent() {
    local run_id=$1 body attempt=1
    while :; do
        if body=$(run_get "$run_id" 2>/dev/null); then
            printf '%s' "$body"
            return 0
        fi
        if [ "$attempt" -ge "$ci_fetch_attempts" ]; then
            return 1
        fi
        echo "train: [$(date +%H:%M:%S)] run status fetch failed (attempt $attempt/$ci_fetch_attempts); retrying in ${poll_interval}s" >&2
        attempt=$((attempt + 1))
        sleep "$poll_interval"
    done
}

run_jobs() {
    api_get "/repos/${ci_repo_owner}/${ci_repo_name}/actions/runs/$1/jobs"
}

# ci_run_in_progress <status> returns 0 (true) while the run is still
# queued or executing. Anything else -- success, failure, cancelled, or a
# status this script doesn't know about -- is treated as terminal so
# `verify` never polls forever on an unrecognised state.
ci_run_in_progress() {
    case "$1" in
        waiting | running | blocked | pending | queued) return 0 ;;
        *) return 1 ;;
    esac
}

# print_ci_status <sha> prints a one-line "ci run: ..." summary for the
# newest ci.yml run of commit <sha>, or nothing at all when no token is
# configured or no run is found yet -- this is best-effort status
# decoration, not something `status` should fail over.
print_ci_status() {
    local sha=$1 run_id run_json
    forgejo_token >/dev/null 2>&1 || return 0
    run_id=$(find_ci_run "$sha" 2>/dev/null) || return 0
    run_json=$(run_get "$run_id" 2>/dev/null) || return 0
    echo "ci run:     #$(printf '%s' "$run_json" | jq -r '.index_in_repo') $(printf '%s' "$run_json" | jq -r '.status') ($(printf '%s' "$run_json" | jq -r '.html_url'))"
}

# summarize_ci_failure <jobs_json> prints the failure summary for a
# non-success run: one "failed job" line per job that genuinely failed,
# then one "did not run" line per job left blocked or cancelled as a
# consequence of that failure. Skipped jobs are dropped entirely -- on a
# train run, artefact-publishing jobs (docker, binaries, release) are
# skipped by design because that job's own guard condition gates them to
# main, and a skip carries no information about what went wrong. Mixing
# all three into one "failed job" list (the pre-#466 behaviour) buried
# the one real failure among five or six jobs that were behaving exactly
# as designed (see #466, run 2530: one `failure` job surrounded by four
# `skipped` and one `cancelled`, all printed as "failed job").
summarize_ci_failure() {
    local jobs_json=$1 failed blocked
    failed=$(printf '%s' "$jobs_json" | jq -r '.[] | select(.status == "failure") | "  failed job: " + .name')
    blocked=$(printf '%s' "$jobs_json" | jq -r '.[] | select(.status != "success" and .status != "failure" and .status != "skipped") | "  did not run (" + .status + "), a consequence of the failure above: " + .name')
    if [ -n "$failed" ]; then
        printf '%s\n' "$failed"
    else
        echo "  (no job reported status \"failure\"; inspect the run status itself)"
    fi
    if [ -n "$blocked" ]; then
        printf '%s\n' "$blocked"
    fi
}

# wait_for_ci_run <sha> polls the ci.yml run for commit <sha> until it
# reaches a terminal status, printing job-level progress each poll. On
# success it records <sha> in the stamp file; on any other terminal
# status it prints the failure summary (summarize_ci_failure) and a
# cilog hint, then exits 1.
wait_for_ci_run() {
    local sha=$1 run_id run_json status jobs_json run_number
    forgejo_token >/dev/null 2>&1 || die "no Forgejo API token: set \$FORGEJO_TOKEN or ~/.config/cilog/token"
    echo "train: locating the ci.yml run for $(git rev-parse --short "$sha") on $TRAIN..."
    run_id=$(find_ci_run "$sha") || die "no ci.yml run found yet for $(git rev-parse --short "$sha"); the push may not have registered with Forgejo yet -- retry in a few seconds"
    run_json=$(run_get_persistent "$run_id") || die "could not fetch run $run_id from $api_base after $ci_fetch_attempts consecutive attempts; the watch is abandoned, not the run -- re-run 'train.sh verify' to pick it up"
    run_number=$(printf '%s' "$run_json" | jq -r '.index_in_repo')
    echo "train: watching $(printf '%s' "$run_json" | jq -r '.html_url')"
    while :; do
        status=$(printf '%s' "$run_json" | jq -r '.status')
        jobs_json=$(run_jobs "$run_id") || jobs_json='[]'
        echo "train: [$(date +%H:%M:%S)] run status: $status"
        printf '%s' "$jobs_json" | jq -r '.[] | "  " + .status + "\t" + .name'
        ci_run_in_progress "$status" || break
        sleep "$poll_interval"
        run_json=$(run_get_persistent "$run_id") || die "could not fetch run $run_id from $api_base after $ci_fetch_attempts consecutive attempts; the watch is abandoned, not the run -- re-run 'train.sh verify' to pick it up"
    done
    if [ "$status" = "success" ]; then
        echo "$sha" >"$stamp_file"
        echo "train: CI run #$run_number succeeded for $(git rev-parse --short "$sha"); run 'train.sh ship'"
    else
        echo "train: CI run #$run_number ended with status $status" >&2
        summarize_ci_failure "$jobs_json" >&2
        echo "train: inspect with: cilog herold/herold $run_number" >&2
        exit 1
    fi
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
    print_ci_status "$tip"
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
    if ! psql -X -U "$admin" -d postgres -tAc "select 1" >/dev/null 2>&1; then
        die "HEROLD_PG_DSN is unset and psql -U $admin cannot reach the local server; the batch gate needs both backends"
    fi
    if [ "$(psql -X -U "$admin" -d postgres -tAc "select 1 from pg_database where datname='herold_train'")" != "1" ]; then
        psql -X -U "$admin" -d postgres -qc "CREATE DATABASE herold_train OWNER herold"
    fi
    export HEROLD_PG_DSN="postgres://herold:herold@127.0.0.1:5432/herold_train?sslmode=disable"
    echo "train: postgres leg uses herold_train"
}

cmd_verify() {
    if [ "${1:-}" = "--local" ]; then
        cmd_verify_local
        return
    fi
    [ $# -eq 0 ] || usage
    ensure_worktree
    rebase_train
    local tip
    tip=$(git -C "$worktree" rev-parse HEAD)
    [ "$tip" = "$(git rev-parse "$REMOTE/$TRAIN")" ] || die "train worktree is not at $REMOTE/$TRAIN"
    if [ -z "$(unshipped)" ]; then
        echo "train: nothing to verify, train equals main"
        exit 0
    fi
    rm -f "$stamp_file"
    echo "train: verifying $(unshipped | wc -l | tr -d ' ') commit(s) via CI, tip $(git rev-parse --short "$tip")"
    git log --oneline "$REMOTE/main..$tip"
    wait_for_ci_run "$tip"
}

cmd_verify_local() {
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
    echo "train: verifying $(unshipped | wc -l | tr -d ' ') commit(s) locally, tip $(git rev-parse --short "$tip")"
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
    local tip verified
    tip=$(git rev-parse "$REMOTE/$TRAIN")
    [ -f "$stamp_file" ] || die "no verified tip recorded; run 'train.sh verify'"
    verified=$(cat "$stamp_file")
    # The train may have moved on since the gate ran; the verified commit
    # ships as long as it is still an ancestor of the train tip, and the
    # newer commits stay on the train for the next batch.
    git merge-base --is-ancestor "$verified" "$tip" || die "verified tip $(git rev-parse --short "$verified") is no longer on $REMOTE/$TRAIN; run 'train.sh verify'"
    git merge-base --is-ancestor "$REMOTE/main" "$verified" || die "main moved since verification; run 'train.sh verify'"
    local n rest
    n=$(git rev-list "$REMOTE/main..$verified" | wc -l | tr -d ' ')
    rest=$(git rev-list "$verified..$tip" | wc -l | tr -d ' ')
    git push -q "$REMOTE" "$verified:main"
    rm -f "$stamp_file"
    echo "train: shipped $n commit(s); main is at $(git rev-parse --short "$verified")"
    if [ "$rest" -gt 0 ]; then
        echo "train: $rest newer commit(s) stay on the train for the next batch"
    fi
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
