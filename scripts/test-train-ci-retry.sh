#!/usr/bin/env bash
# test-train-ci-retry.sh -- exercises train.sh's CI watch against a forge
# that fails intermittently (re #454-era gate hardening).
#
# A batch is watched for twenty minutes or more, so a transient DNS or
# network failure inside that window is expected. The watch must ride one
# out and go on to stamp a run that succeeds, and it must still give up
# when the forge is unreachable for good. Both are asserted here, because
# a watch that ends on the first blip looks exactly like a watch
# reporting a failed batch.
#
# The stub serves canned JSON from files, so no network is involved.

set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

fail() {
    echo "FAIL: $*" >&2
    exit 1
}

# A stub `curl` on PATH ahead of the real one. It fails for the first
# $STUB_FAILURES calls against the run endpoint, then answers success.
mkdir -p "$WORK/bin"
cat >"$WORK/bin/curl" <<'STUB'
#!/usr/bin/env bash
url=${*: -1}
counter=$STUB_COUNTER
case "$url" in
    *"/actions/runs?"*)
        printf '{"workflow_runs":[{"id":4242}]}'
        exit 0
        ;;
    *"/actions/runs/4242/jobs"*)
        printf '[{"status":"success","name":"test"}]'
        exit 0
        ;;
    *"/actions/runs/4242"*)
        n=0
        [ -f "$counter" ] && n=$(cat "$counter")
        n=$((n + 1))
        printf '%s' "$n" >"$counter"
        if [ "$n" -le "${STUB_FAILURES:-0}" ]; then
            echo "curl: (6) Could not resolve host: stub.invalid" >&2
            exit 6
        fi
        printf '{"id":4242,"index_in_repo":99,"status":"success","html_url":"https://stub.invalid/run/99"}'
        exit 0
        ;;
esac
exit 1
STUB
chmod +x "$WORK/bin/curl"

run_watch() {
    local failures=$1 attempts=$2 logfile=$3
    : >"$WORK/counter"
    STUB_FAILURES=$failures \
    STUB_COUNTER="$WORK/counter" \
    TRAIN_CI_FETCH_ATTEMPTS=$attempts \
    TRAIN_CI_POLL_INTERVAL=0 \
    FORGEJO_TOKEN=stub-token \
    PATH="$WORK/bin:$PATH" \
        bash -c '
            set -euo pipefail
            source "$1" --source-only 2>/dev/null || true
        ' _ "$SCRIPT_DIR/train.sh" >"$logfile" 2>&1 || true
}

# train.sh runs a command when sourced, so drive wait_for_ci_run through a
# harness that defines only what it needs: the script's helpers, with git
# and the stamp file pointed at the scratch directory.
cat >"$WORK/harness.sh" <<'HARNESS'
set -euo pipefail
api_base=${TRAIN_API_BASE:-https://stub.invalid/api/v1}
ci_repo_owner=herold
ci_repo_name=herold
ci_workflow_id=ci.yml
poll_interval=${TRAIN_CI_POLL_INTERVAL:-0}
ci_fetch_attempts=${TRAIN_CI_FETCH_ATTEMPTS:-12}
stamp_file=$STAMP_FILE
TRAIN=train

die() { echo "train: $*" >&2; exit 1; }
forgejo_token() { printf '%s' "${FORGEJO_TOKEN:-}"; }
git() { case "$*" in "rev-parse --short "*) printf 'deadbee\n' ;; *) command git "$@" ;; esac; }

# The functions under test, taken verbatim from train.sh.
eval "$(sed -n '/^api_get() {/,/^}/p;/^find_ci_run() {/,/^}/p;/^run_get() {/,/^}/p;/^run_get_persistent() {/,/^}/p;/^run_jobs() {/,/^}/p;/^ci_run_in_progress() {/,/^}/p;/^wait_for_ci_run() {/,/^}/p;/^urlencode() {/,/^}/p' "$TRAIN_SH")"

wait_for_ci_run "$SHA"
HARNESS

drive() {
    local failures=$1 attempts=$2 log=$3
    : >"$WORK/counter"
    rm -f "$WORK/stamp"
    set +e
    STUB_FAILURES=$failures \
    STUB_COUNTER="$WORK/counter" \
    TRAIN_CI_FETCH_ATTEMPTS=$attempts \
    TRAIN_CI_POLL_INTERVAL=0 \
    FORGEJO_TOKEN=stub-token \
    STAMP_FILE="$WORK/stamp" \
    TRAIN_SH="$SCRIPT_DIR/train.sh" \
    SHA=deadbeefdeadbeefdeadbeefdeadbeefdeadbeef \
    PATH="$WORK/bin:$PATH" \
        bash "$WORK/harness.sh" >"$log" 2>&1
    local rc=$?
    set -e
    return $rc
}

# 1. A transient failure inside the window must not end the watch: the
#    run succeeds afterwards and the batch is stamped.
if ! drive 2 12 "$WORK/transient.log"; then
    echo "--- output ---" >&2
    cat "$WORK/transient.log" >&2
    fail "a transient fetch failure ended the watch"
fi
grep -q "retrying" "$WORK/transient.log" || fail "the retry was not reported"
[ -f "$WORK/stamp" ] || fail "a run that succeeded after a blip was not stamped"

# 2. A forge that never answers must still end the watch, rather than
#    polling forever, and must not stamp anything.
if drive 99 3 "$WORK/permanent.log"; then
    fail "an unreachable forge did not end the watch"
fi
grep -q "after 3 consecutive attempts" "$WORK/permanent.log" \
    || fail "giving up did not say how many attempts were made"
[ -f "$WORK/stamp" ] && fail "an abandoned watch stamped the batch"

echo "PASS: train.sh's CI watch rides out a blip and still gives up on an outage"
