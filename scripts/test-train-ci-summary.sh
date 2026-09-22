#!/usr/bin/env bash
# test-train-ci-summary.sh -- exercises wait_for_ci_run's failure summary
# against a run whose jobs mix failure, success, skipped and blocked
# statuses (re #466, shaped after the observed run 2530: one genuine
# `failure` job surrounded by `skipped` artefact-publishing jobs and a
# `blocked` job that never ran because of that failure).
#
# The summary must name the failed job, must not mention either skipped
# job, and must report the blocked job separately from the failures.
#
# The stub serves canned JSON, so no network is involved.

set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

fail() {
    echo "FAIL: $*" >&2
    exit 1
}

mkdir -p "$WORK/bin"
cat >"$WORK/bin/curl" <<'STUB'
#!/usr/bin/env bash
url=${*: -1}
case "$url" in
    *"/actions/runs?"*)
        printf '{"workflow_runs":[{"id":4242}]}'
        exit 0
        ;;
    *"/actions/runs/4242/jobs"*)
        cat "$JOBS_FIXTURE"
        exit 0
        ;;
    *"/actions/runs/4242"*)
        printf '{"id":4242,"index_in_repo":2530,"status":"failure","html_url":"https://stub.invalid/run/2530"}'
        exit 0
        ;;
esac
exit 1
STUB
chmod +x "$WORK/bin/curl"

cat >"$WORK/jobs.json" <<'JSON'
[
  {"status":"success","name":"pre-commit (run --all-files)"},
  {"status":"success","name":"lint"},
  {"status":"failure","name":"fuzz (short)"},
  {"status":"skipped","name":"docker (build + publish to code.netzhansa.com registry)"},
  {"status":"skipped","name":"binaries (linux/darwin x amd64/arm64)"},
  {"status":"blocked","name":"release (publish ci-<sha> to code.netzhansa.com)"}
]
JSON

cat >"$WORK/harness.sh" <<'HARNESS'
set -euo pipefail
api_base=${TRAIN_API_BASE:-https://stub.invalid/api/v1}
ci_repo_owner=herold
ci_repo_name=herold
ci_workflow_id=ci.yml
poll_interval=0
ci_fetch_attempts=1
stamp_file=$STAMP_FILE
TRAIN=train

die() { echo "train: $*" >&2; exit 1; }
forgejo_token() { printf '%s' "${FORGEJO_TOKEN:-}"; }
git() { case "$*" in "rev-parse --short "*) printf 'deadbee\n' ;; *) command git "$@" ;; esac; }

# The functions under test, taken verbatim from train.sh.
eval "$(sed -n '/^api_get() {/,/^}/p;/^find_ci_run() {/,/^}/p;/^run_get() {/,/^}/p;/^run_get_persistent() {/,/^}/p;/^run_jobs() {/,/^}/p;/^ci_run_in_progress() {/,/^}/p;/^summarize_ci_failure() {/,/^}/p;/^wait_for_ci_run() {/,/^}/p;/^urlencode() {/,/^}/p' "$TRAIN_SH")"

wait_for_ci_run "$SHA"
HARNESS

set +e
FORGEJO_TOKEN=stub-token \
STAMP_FILE="$WORK/stamp" \
TRAIN_SH="$SCRIPT_DIR/train.sh" \
SHA=deadbeefdeadbeefdeadbeefdeadbeefdeadbeef \
JOBS_FIXTURE="$WORK/jobs.json" \
PATH="$WORK/bin:$PATH" \
    bash "$WORK/harness.sh" >"$WORK/out.log" 2>&1
rc=$?
set -e

[ "$rc" -ne 0 ] || fail "a run ending with status failure must exit non-zero"

grep -q "  failed job: fuzz (short)" "$WORK/out.log" \
    || fail "the genuinely failed job was not named as a failed job"

grep -q "failed job:.*docker (build" "$WORK/out.log" \
    && fail "a skipped job (docker) was reported as a failed job"
grep -q "failed job:.*binaries (linux" "$WORK/out.log" \
    && fail "a skipped job (binaries) was reported as a failed job"
grep -q "did not run.*docker (build" "$WORK/out.log" \
    && fail "a skipped job (docker) appears in the failure summary at all"

grep -q "failed job:.*release (publish" "$WORK/out.log" \
    && fail "the blocked job (release) was reported as a failed job, not separately"
grep -q "did not run.*release (publish" "$WORK/out.log" \
    || fail "the blocked job (release) was not reported separately as a consequence"

[ -f "$WORK/stamp" ] && fail "a failed run must not stamp the batch as verified"

echo "PASS: the failure summary names the failed job, drops skipped jobs, and reports the blocked job separately"
