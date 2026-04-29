#!/usr/bin/env bash
# check-schema-version.sh -- fail if internal/diag/backup.CurrentSchemaVersion
# does not equal max(NNNN_*.sql) in both backend migration dirs, or if the
# two backends carry different migration sets.
#
# Mirrors internal/diag/backup.TestCurrentSchemaVersionMatchesMaxMigration
# but runs in <1s without compiling Go. Suitable for pre-commit.
#
# Exit codes: 0 ok, 1 mismatch, 2 unable to determine.

set -euo pipefail

repo_root=$(git rev-parse --show-toplevel 2>/dev/null || pwd)
cd "$repo_root"

manifest=internal/diag/backup/manifest.go
sqlite_dir=internal/storesqlite/migrations
pg_dir=internal/storepg/migrations

if [[ ! -f $manifest ]]; then
    echo "check-schema-version: cannot find $manifest" >&2
    exit 2
fi
if [[ ! -d $sqlite_dir || ! -d $pg_dir ]]; then
    echo "check-schema-version: missing migration dirs" >&2
    exit 2
fi

current=$(grep -E '^const CurrentSchemaVersion = [0-9]+$' "$manifest" \
    | awk '{print $4}' || true)
if [[ -z $current ]]; then
    echo "check-schema-version: could not parse CurrentSchemaVersion in $manifest" >&2
    exit 2
fi

# Highest NNNN prefix (4-digit, zero-padded) in each backend dir.
max_in() {
    local dir=$1
    ls "$dir" 2>/dev/null \
        | grep -E '^[0-9]{4}_.+\.sql$' \
        | sed -E 's/^([0-9]{4})_.*$/\1/' \
        | sort -n \
        | tail -1 \
        | sed 's/^0*//'   # strip leading zeros so 0033 -> 33
}

sqlite_max=$(max_in "$sqlite_dir")
pg_max=$(max_in "$pg_dir")

if [[ -z $sqlite_max || -z $pg_max ]]; then
    echo "check-schema-version: no migrations found in $sqlite_dir or $pg_dir" >&2
    exit 2
fi

if [[ $sqlite_max != "$pg_max" ]]; then
    cat >&2 <<EOF
check-schema-version: backend migration sets diverge:
  sqlite max:   $sqlite_max  ($sqlite_dir)
  postgres max: $pg_max  ($pg_dir)
Every migration MUST ship to both backends in the same commit.
EOF
    exit 1
fi

if [[ $current != "$sqlite_max" ]]; then
    cat >&2 <<EOF
check-schema-version: CurrentSchemaVersion = $current but the highest migration is $sqlite_max.
When you add migration NNNN_*.sql you MUST also:
  (1) bump CurrentSchemaVersion in $manifest
  (2) add a comment block describing the migration
  (3) extend TableNames if the migration adds tables
  (4) extend rows.go, backend.go, adapter_sqlite.go, adapter_fakestore.go,
      and testharness/fakestore/diag.go with the corresponding row type +
      dispatch cases
See internal/diag/backup/manifest_test.go for the canonical CI gate.
EOF
    exit 1
fi
