#!/usr/bin/env bash
# test-release-notes.sh -- exercises scripts/release-notes.sh against a
# throwaway git repository and fixture issues (no network).
#
# Covers grouping by referenced issue, the feature / fix / other-ticket
# split by label, the awaiting-check marker, commits without an issue,
# an issue the API cannot return, and the range boundaries (the from
# commit is excluded, the to commit included).
#
# Invoked from ci.yml's "release" job so a regression fails CI before
# deploy.yml mails the notes.

set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
NOTES="$SCRIPT_DIR/release-notes.sh"

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

fail() {
    echo "FAIL: $*" >&2
    echo "--- output ---" >&2
    cat "$WORK/out" >&2
    exit 1
}

expect() {
    grep -qxF -- "$1" "$WORK/out" || fail "missing line: $1"
}

expect_not() {
    if grep -qF -- "$1" "$WORK/out"; then
        fail "unexpected text: $1"
    fi
}

repo="$WORK/repo"
git init -q "$repo"
git -C "$repo" config user.email test@example.test
git -C "$repo" config user.name test
commit() {
    git -C "$repo" commit -q --allow-empty -m "$1"
    git -C "$repo" rev-parse HEAD
}

commit "root" >/dev/null
base=$(commit "base: before the range (re #1)")
commit "suite: first half of the feature" >/dev/null
# The issue reference sits in the commit body.
git -C "$repo" commit -q --allow-empty -m "store: second half of the feature" -m "re #10" >/dev/null
commit "jmap: fix the crash (re #20)" >/dev/null
commit "ci: tidy the workflow" >/dev/null
commit "docs: note on the chore (refs #30)" >/dev/null
tip=$(commit "imap: fix with a ticket the API lacks (re #40)")

mkdir "$WORK/issues"
cat >"$WORK/issues/10.json" <<'EOF'
{"number":10,"title":"A new feature","labels":[{"name":"enhancement"},{"name":"waiting-for-feedback"}]}
EOF
cat >"$WORK/issues/20.json" <<'EOF'
{"number":20,"title":"A crash","labels":[{"name":"bug"}]}
EOF
cat >"$WORK/issues/30.json" <<'EOF'
{"number":30,"title":"A chore","labels":[{"name":"docs"}]}
EOF

RELEASE_NOTES_ISSUES_DIR="$WORK/issues" ISSUE_BASE_URL="https://forge.test/issues" \
    "$NOTES" "$repo" "$base" "$tip" ci-test >"$WORK/out"

[ "$(head -n 1 "$WORK/out")" = "herold ci-test deployed: 6 changes" ] \
    || fail "subject line"
[ -z "$(sed -n 2p "$WORK/out")" ] || fail "blank line after the subject"

expect "New features"
expect "#10 A new feature"
expect "  Awaiting your check."
expect "  https://forge.test/issues/10"
expect "  - store: second half of the feature"
expect "Fixes"
expect "#20 A crash"
expect "  - jmap: fix the crash (re #20)"
expect "Other tickets"
expect "#30 A chore"
expect "  - docs: note on the chore (refs #30)"
expect "#40"
expect "  - imap: fix with a ticket the API lacks (re #40)"
expect "Other changes"
expect "- suite: first half of the feature"
expect "- ci: tidy the workflow"
expect_not "base: before the range"

# Only #10 carries waiting-for-feedback.
[ "$(grep -c 'Awaiting your check.' "$WORK/out")" -eq 1 ] || fail "awaiting marker count"

# Section order: features, fixes, other tickets, other changes.
order=$(grep -nE '^(New features|Fixes|Other tickets|Other changes)$' "$WORK/out" | cut -d: -f2 | paste -sd, -)
[ "$order" = "New features,Fixes,Other tickets,Other changes" ] || fail "section order: $order"

echo "ok: release-notes.sh"
