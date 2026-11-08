#!/usr/bin/env bash
# release-notes.sh -- composes the release-notes mail that
# .forgejo/workflows/deploy.yml sends after a deploy is confirmed.
#
# usage:
#   release-notes.sh <git-dir> <from-sha> <to-sha> <release-ref>
#
# Lists the commits in <from-sha>..<to-sha> (the release that was live
# before the deploy, exclusive, up to the deployed one), grouped by the
# issue each commit references with `re #N` / `refs #N` / `see #N` /
# `addresses #N` / `part of #N`. Issues labelled `enhancement` go under
# "New features", `bug` under "Fixes", the rest under "Other tickets";
# commits that reference no issue go under "Other changes". An issue
# labelled `waiting-for-feedback` is marked as awaiting the maintainer's
# check.
#
# Issue titles and labels come from the Forgejo API
# (${FORGEJO_API}/repos/herold/herold/issues/<N>, authenticated with
# ${FORGEJO_TOKEN}). With RELEASE_NOTES_ISSUES_DIR set, they come from
# <dir>/<N>.json instead (the shape the API returns), which is how
# test-release-notes.sh runs without the network. An issue that cannot
# be fetched is listed by number with its commits.
#
# Output: the subject on the first line, a blank line, then the plain
# text body.
set -euo pipefail

if [ "$#" -ne 4 ]; then
  echo "usage: $0 <git-dir> <from-sha> <to-sha> <release-ref>" >&2
  exit 2
fi

git_dir=$1
from=$2
to=$3
ref=$4
issue_base="${ISSUE_BASE_URL:-https://code.netzhansa.com/herold/herold/issues}"

work=$(mktemp -d)
trap 'rm -rf "${work}"' EXIT

issue_json() {
  local n=$1
  if [ -n "${RELEASE_NOTES_ISSUES_DIR:-}" ]; then
    cat "${RELEASE_NOTES_ISSUES_DIR}/${n}.json" 2>/dev/null || true
  else
    curl -fsS -H "Authorization: token ${FORGEJO_TOKEN:-}" \
      "${FORGEJO_API}/repos/herold/herold/issues/${n}" 2>/dev/null || true
  fi
}

# One line per commit, oldest first: <issue-or-empty> TAB <subject>.
# A commit referencing several issues is listed under the first.
count=0
: >"${work}/commits"
while IFS= read -r sha; do
  [ -n "${sha}" ] || continue
  count=$((count + 1))
  subject=$(git -C "${git_dir}" log -1 --format=%s "${sha}")
  issue=$(git -C "${git_dir}" log -1 --format=%B "${sha}" \
    | grep -oiE '(^|[^[:alnum:]])(re|refs|see|addresses|part of) #[0-9]+' \
    | grep -oE '[0-9]+$' | head -n 1 || true)
  printf '%s\t%s\n' "${issue}" "${subject}" >>"${work}/commits"
done < <(git -C "${git_dir}" rev-list --reverse --no-merges "${from}..${to}")

: >"${work}/features"
: >"${work}/fixes"
: >"${work}/tickets"
for n in $(cut -f1 "${work}/commits" | grep -v '^$' | awk '!seen[$0]++'); do
  json=$(issue_json "${n}")
  title=$(printf '%s' "${json}" | jq -r '.title // empty' 2>/dev/null || true)
  labels=$(printf '%s' "${json}" | jq -r '[.labels[]?.name] | join(" ")' 2>/dev/null || true)
  case " ${labels} " in
    *" enhancement "*) section=features ;;
    *" bug "*) section=fixes ;;
    *) section=tickets ;;
  esac
  {
    if [ -n "${title}" ]; then
      printf '#%s %s\n' "${n}" "${title}"
    else
      printf '#%s\n' "${n}"
    fi
    case " ${labels} " in
      *" waiting-for-feedback "*) printf '  Awaiting your check.\n' ;;
    esac
    printf '  %s/%s\n' "${issue_base}" "${n}"
    awk -F '\t' -v n="${n}" '$1 == n { print "  - " $2 }' "${work}/commits"
    printf '\n'
  } >>"${work}/${section}"
done
awk -F '\t' '$1 == "" { print "- " $2 }' "${work}/commits" >"${work}/other"

if [ "${count}" -eq 1 ]; then
  noun=change
else
  noun=changes
fi
printf 'herold %s deployed: %d %s\n\n' "${ref}" "${count}" "${noun}"
printf 'Release %s is live (commits %s..%s).\n\n' "${ref}" "${from:0:8}" "${to:0:8}"

print_section() {
  local heading=$1 file=$2
  [ -s "${file}" ] || return 0
  printf '%s\n' "${heading}"
  printf '%s\n' "${heading}" | tr '[:print:]' '-'
  cat "${file}"
}
print_section "New features" "${work}/features"
print_section "Fixes" "${work}/fixes"
print_section "Other tickets" "${work}/tickets"
if [ -s "${work}/other" ]; then
  printf 'Other changes\n-------------\n'
  cat "${work}/other"
fi
