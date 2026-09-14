#!/usr/bin/env bash
# test-lanes.sh -- run the Go test lanes the way the CI `test` job does.
#
# SQLite: the packages listed in test/race-packages.txt run under -race,
# the rest without. Postgres (when HEROLD_PG_DSN is set): the packages
# that open the DSN run serialised (-p 1, the storepg test seam terminates
# every other backend on its database), the rest in parallel.
#
# Used by `make verify-batch`; runnable standalone.

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

patterns=$(grep -v '^[[:space:]]*#' test/race-packages.txt | grep -v '^[[:space:]]*$' | sed 's|^|./|')
race=$(echo "$patterns" | xargs go list 2>/dev/null | sort -u)
[ -n "$race" ] || { echo "test-lanes: race-packages.txt produced no packages" >&2; exit 1; }
all=$(go list ./... | sort -u)
norace=$(comm -23 <(echo "$all") <(echo "$race"))

echo "test-lanes: sqlite race lane, $(echo "$race" | wc -l | tr -d ' ') packages"
# shellcheck disable=SC2086
go test -race -count=1 -timeout 30m $race

echo "test-lanes: sqlite fast lane, $(echo "$norace" | wc -l | tr -d ' ') packages"
# shellcheck disable=SC2086
go test -count=1 -timeout 15m $norace

if [ -z "${HEROLD_PG_DSN:-}" ]; then
    echo "test-lanes: HEROLD_PG_DSN unset, postgres lanes skipped" >&2
    exit 0
fi

echo "test-lanes: postgres serialised lane"
go test -count=1 -timeout 20m -p 1 ./internal/storepg/... ./internal/diag/migrate/... ./test/e2e/...

rest=$(echo "$all" | grep -vE '/(internal/storepg|internal/diag/migrate|test/e2e)($|/)')
echo "test-lanes: postgres parallel lane, $(echo "$rest" | wc -l | tr -d ' ') packages"
# shellcheck disable=SC2086
HEROLD_TEST_STORE=postgres go test -count=1 -timeout 20m $rest
