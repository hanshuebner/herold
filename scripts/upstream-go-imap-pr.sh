#!/usr/bin/env bash
# Fork emersion/go-imap on GitHub, apply the herold X-GM-LABELS patch, push the
# branch, and open the upstream pull request. One-shot wrapper around the
# multi-step gh/git dance documented in docs/dev/imap-client-mode-remaining-work.md
# (Wave B of issue #25). Run it on a machine with `gh` authenticated to GitHub.
#
# Usage:
#   scripts/upstream-go-imap-pr.sh [-b BRANCH] [-w WORKDIR] [--no-pr]
#
#   -b BRANCH    Fork branch name to create/push      (default: xgmlabels)
#   -w WORKDIR   Where to clone the fork              (default: a temp dir)
#   --no-pr      Do everything except `gh pr create`  (push only)
#
# Requires: gh (authenticated), git. Reads the patch + PR body from this repo's
# docs/dev/. Idempotent-ish: re-running reuses an existing fork and resets the
# branch.
set -euo pipefail

UPSTREAM="emersion/go-imap"
TITLE="imapclient: add X-GM-LABELS (X-GM-EXT-1) FETCH support"
BRANCH="xgmlabels"
WORKDIR=""
OPEN_PR=1

while [ $# -gt 0 ]; do
  case "$1" in
    -b) BRANCH="$2"; shift 2 ;;
    -w) WORKDIR="$2"; shift 2 ;;
    --no-pr) OPEN_PR=0; shift ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

# Resolve this herold repo and the patch/PR artifacts.
HEROLD_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PATCH="$HEROLD_ROOT/docs/dev/go-imap-xgmlabels-upstream.patch"
PRBODY="$HEROLD_ROOT/docs/dev/go-imap-xgmlabels-PR.md"
for f in "$PATCH" "$PRBODY"; do
  [ -f "$f" ] || { echo "missing artifact: $f" >&2; exit 1; }
done

command -v gh >/dev/null 2>&1 || { echo "gh (GitHub CLI) is required: https://cli.github.com" >&2; exit 1; }
gh auth status >/dev/null 2>&1 || { echo "run 'gh auth login' first" >&2; exit 1; }

GH_USER="$(gh api user --jq .login)"
echo ">> GitHub user: $GH_USER"

# Fork (no-op if it already exists) and clone it.
echo ">> forking $UPSTREAM ..."
gh repo fork "$UPSTREAM" --clone=false >/dev/null 2>&1 || true

if [ -z "$WORKDIR" ]; then WORKDIR="$(mktemp -d)/go-imap"; fi
echo ">> cloning fork into $WORKDIR ..."
rm -rf "$WORKDIR"
gh repo clone "$GH_USER/go-imap" "$WORKDIR" -- -q
cd "$WORKDIR"
git remote add upstream "https://github.com/$UPSTREAM" 2>/dev/null || true
git fetch -q upstream

# Base the PR branch on upstream's default branch.
DEFAULT_BRANCH="$(gh repo view "$UPSTREAM" --json defaultBranchRef --jq .defaultBranchRef.name)"
echo ">> basing $BRANCH on upstream/$DEFAULT_BRANCH ..."
git checkout -q -B "$BRANCH" "upstream/$DEFAULT_BRANCH"

# Apply the patch. Try a straight am first, then a 3-way, then bail with guidance.
echo ">> applying patch ..."
if ! git am "$PATCH" 2>/dev/null; then
  git am --abort 2>/dev/null || true
  if ! git am --3way "$PATCH"; then
    git am --abort 2>/dev/null || true
    echo "!! patch did not apply cleanly to upstream/$DEFAULT_BRANCH." >&2
    echo "   The patch was cut against v2.0.0-beta.8; master may have drifted." >&2
    echo "   Resolve by hand in $WORKDIR (git apply --reject \"$PATCH\"), commit, then re-run with --no-pr-skipped steps, or open the PR manually." >&2
    exit 1
  fi
fi

echo ">> pushing $GH_USER:$BRANCH ..."
git push -q -f -u origin "$BRANCH"

if [ "$OPEN_PR" -eq 1 ]; then
  echo ">> opening PR against $UPSTREAM:$DEFAULT_BRANCH ..."
  gh pr create --repo "$UPSTREAM" \
    --base "$DEFAULT_BRANCH" --head "$GH_USER:$BRANCH" \
    --title "$TITLE" --body-file "$PRBODY"
else
  echo ">> --no-pr: pushed branch only. Open the PR with:"
  echo "   gh pr create --repo $UPSTREAM --base $DEFAULT_BRANCH --head $GH_USER:$BRANCH --title \"$TITLE\" --body-file \"$PRBODY\""
fi

echo ">> done. Fork branch: https://github.com/$GH_USER/go-imap/tree/$BRANCH"
