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

# The SQLite lanes run without a Postgres DSN: with one set, every
# package's Postgres variants would run in parallel against a single
# database, and the storepg test seam terminates the other backends on
# that database (the Postgres lanes below run those packages serialised).
echo "test-lanes: sqlite race lane, $(echo "$race" | wc -l | tr -d ' ') packages"
# shellcheck disable=SC2086
# Race-instrumented packages are 5-30x heavier than plain ones; running
# ncpu of them at once on the development host oversubscribes it far
# enough that server-boot tests miss their readiness window. Four at a
# time keeps the lane within what the CI runner sees.
env -u HEROLD_PG_DSN HEROLD_TEST_STORE=sqlite CGO_ENABLED=1 go test -race -count=1 -timeout 30m -p "${TEST_LANES_RACE_P:-4}" $race

echo "test-lanes: sqlite fast lane, $(echo "$norace" | wc -l | tr -d ' ') packages"
# shellcheck disable=SC2086
env -u HEROLD_PG_DSN HEROLD_TEST_STORE=sqlite CGO_ENABLED=0 go test -count=1 -timeout 15m $norace

if [ -z "${HEROLD_PG_DSN:-}" ]; then
    echo "test-lanes: HEROLD_PG_DSN unset, postgres lanes skipped" >&2
    exit 0
fi

echo "test-lanes: postgres serialised lane"
HEROLD_TEST_STORE=postgres CGO_ENABLED=0 go test -count=1 -timeout 20m -p 1 ./internal/storepg/... ./internal/diag/migrate/... ./test/e2e/...

# The CI postgres lane runs the remaining packages without the DSN (only
# HEROLD_TEST_STORE=postgres), so their Postgres variants skip there; the
# gate does the same. Packages whose Postgres variants matter are covered
# by the serialised lane and by each ticket's own targeted run.
rest=$(echo "$all" | grep -vE '/(internal/storepg|internal/diag/migrate|test/e2e)($|/)')
echo "test-lanes: postgres parallel lane, $(echo "$rest" | wc -l | tr -d ' ') packages"
# shellcheck disable=SC2086
env -u HEROLD_PG_DSN HEROLD_TEST_STORE=postgres CGO_ENABLED=0 go test -count=1 -timeout 20m $rest
