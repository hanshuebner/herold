#!/usr/bin/env bash
# test-deploy-decide.sh -- exercises scripts/deploy-decide.sh (re #323).
#
# Covers the status-parsing cases (unreadable status must fail closed,
# never silently redeploy or skip) and the ordering guard (a target that
# is not a descendant of the live commit is refused, even when its
# release sorts newest by created_at or the live release was pruned).
#
# Invoked from ci.yml's "release" job so a regression in the decision
# logic fails CI before deploy.yml ever runs against it.

set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
DECIDE="$SCRIPT_DIR/deploy-decide.sh"

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

fail() {
    echo "FAIL: $*" >&2
    exit 1
}

# assert_decision <label> <expected> <exit-code> <output> <target-ref> \
#                  <target-sha> <current-sha> <git-dir>
assert_decision() {
    local label=$1 expected=$2 exit_code=$3 output=$4 target_ref=$5 target_sha=$6 current_sha=$7 git_dir=$8
    local got
    got=$("$DECIDE" "$exit_code" "$output" "$target_ref" "$target_sha" "$current_sha" "$git_dir" 2>"$WORK/${label// /_}.log")
    [ "$got" = "$expected" ] || fail "$label: expected '$expected', got '$got' (reason: $(cat "$WORK/${label// /_}.log"))"
    echo "ok: $label -> $got"
}

echo "=== status-parsing cases (no git repo needed) ==="

assert_decision "nonzero exit" fail \
    1 "" "ci-aaa" "aaa" "" ""

assert_decision "nonzero exit with stray output" fail \
    2 "garbage" "ci-aaa" "aaa" "" ""

assert_decision "empty output on exit 0" fail \
    0 "" "ci-aaa" "aaa" "" ""

assert_decision "no release live" deploy \
    0 "none" "ci-aaa" "aaa" "" ""

assert_decision "already deployed" skip \
    0 "ci-aaa" "ci-aaa" "aaa" "" ""

echo "=== ordering guard (real git repo, real ancestry) ==="

REPO="$WORK/repo"
git init -q "$REPO"
git -C "$REPO" config user.email test@example.invalid
git -C "$REPO" config user.name "deploy-decide test"

echo one > "$REPO/f"
git -C "$REPO" add f
git -C "$REPO" commit -q -m one
SHA1=$(git -C "$REPO" rev-parse HEAD)

echo two > "$REPO/f"
git -C "$REPO" commit -q -am two
SHA2=$(git -C "$REPO" rev-parse HEAD)
MAIN_BRANCH=$(git -C "$REPO" symbolic-ref --short HEAD)

# A commit on an unrelated branch, sharing no history with SHA1/SHA2 past
# nothing (orphan branch): neither is an ancestor of the other.
git -C "$REPO" checkout -q --orphan orphan-branch
git -C "$REPO" rm -rq --cached -- . >/dev/null
rm -f "$REPO/f"
echo other > "$REPO/g"
git -C "$REPO" add g
git -C "$REPO" commit -q -m orphan
SHA_ORPHAN=$(git -C "$REPO" rev-parse HEAD)
git -C "$REPO" checkout -q "$MAIN_BRANCH"

assert_decision "target newer than live" deploy \
    0 "ci-old" "ci-new" "$SHA2" "$SHA1" "$REPO"

assert_decision "target older than live: refuse to downgrade" skip \
    0 "ci-new" "ci-old" "$SHA1" "$SHA2" "$REPO"

assert_decision "target diverged from live: refuse" skip \
    0 "ci-live" "ci-target" "$SHA_ORPHAN" "$SHA1" "$REPO"

assert_decision "live release's commit unresolved: fail closed" fail \
    0 "ci-unresolved" "ci-new" "$SHA2" "" "$REPO"

assert_decision "target equals live commit: deploy (is-ancestor of self)" deploy \
    0 "ci-old" "ci-new-same-commit" "$SHA1" "$SHA1" "$REPO"

echo "all deploy-decide.sh scenarios passed"
