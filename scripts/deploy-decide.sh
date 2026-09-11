#!/usr/bin/env bash
# deploy-decide.sh -- pure decision logic for .forgejo/workflows/deploy.yml
# (re #323). Given what outpost's read-only `status` forced command
# reported and the release the workflow wants to deploy, decides whether
# to deploy, skip, or fail. Kept separate from the workflow so it is
# testable without ssh or the Forgejo API (see test-deploy-decide.sh).
#
# usage:
#   deploy-decide.sh <status-exit-code> <status-output> <target-ref> \
#                     <target-sha> <current-sha> <git-dir>
#
# <status-exit-code> / <status-output>: the exit code and stdout of
#   outpost's `status` command, exactly as observed -- this script does
#   not tolerate the caller collapsing "unreachable" and "nothing
#   deployed" into the same empty string. Only two shapes count as a
#   readable answer: exit 0 with output "none" (nothing live yet), and
#   exit 0 with output equal to a release ref (that ref is live). Any
#   other combination (nonzero exit, empty output on exit 0, garbage
#   output) is treated as unreadable and fails closed.
#
# <target-ref> / <target-sha>: the release the workflow wants to deploy
#   and the commit it was built from (release target_commitish).
#
# <current-sha> / <git-dir>: when outpost reports a live ref that is
#   NOT <target-ref>, the caller resolves that ref's commit (via the
#   Forgejo releases API) and passes it as <current-sha>, together with
#   a git working copy (<git-dir>) that has both commits available. This
#   script then refuses to deploy a target that is not a descendant of
#   the live commit (`git merge-base --is-ancestor`), so a stale release
#   -- picked because it happened to sort newest by created_at, or
#   because the live release was pruned out of the releases list -- can
#   never downgrade a host. Pass "" for both when outpost reports "none"
#   or the live ref already matches <target-ref>; this script does not
#   use them in those cases.
#
# Prints exactly one word to stdout: deploy, skip, or fail. Prints a
# human-readable reason to stderr. Always exits 0 -- the decision is the
# output, not the exit code, so callers can do decision=$(deploy-decide.sh ...).
set -euo pipefail

decide() {
  local exit_code="$1" output="$2" target_ref="$3" target_sha="$4" current_sha="$5" git_dir="$6"

  if [ "${exit_code}" -ne 0 ]; then
    echo "fail"
    echo "status query failed (exit ${exit_code}); cannot safely tell 'nothing deployed' from 'unreachable'" >&2
    return
  fi

  if [ -z "${output}" ]; then
    echo "fail"
    echo "status query returned no output on exit 0; treating as unreadable" >&2
    return
  fi

  if [ "${output}" = "none" ]; then
    echo "deploy"
    echo "outpost reports no release currently live" >&2
    return
  fi

  if [ "${output}" = "${target_ref}" ]; then
    echo "skip"
    echo "outpost already runs ${target_ref}" >&2
    return
  fi

  # outpost runs a different, named release. Only deploy if the target is
  # not older than (i.e. is a descendant of, or equal to) what's live.
  if [ -z "${current_sha}" ]; then
    echo "fail"
    echo "outpost runs ${output} but its commit could not be resolved; refusing to guess whether ${target_ref} is newer" >&2
    return
  fi

  local rc=0
  git -C "${git_dir}" merge-base --is-ancestor "${current_sha}" "${target_sha}" || rc=$?
  case "${rc}" in
    0)
      echo "deploy"
      echo "${target_ref} (${target_sha}) is a descendant of the live ${output} (${current_sha})" >&2
      ;;
    1)
      echo "skip"
      echo "${target_ref} (${target_sha}) is not a descendant of the live ${output} (${current_sha}); refusing to downgrade" >&2
      ;;
    *)
      echo "fail"
      echo "git merge-base --is-ancestor ${current_sha} ${target_sha} errored (exit ${rc})" >&2
      ;;
  esac
}

# Allow sourcing for tests without running main().
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  if [ "$#" -ne 6 ]; then
    echo "usage: deploy-decide.sh <status-exit-code> <status-output> <target-ref> <target-sha> <current-sha> <git-dir>" >&2
    exit 2
  fi
  decide "$1" "$2" "$3" "$4" "$5" "$6"
fi
