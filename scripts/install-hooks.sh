#!/usr/bin/env bash
# install-hooks.sh -- install pre-commit + pre-push git hooks for this repo.
#
# Idempotent. Run after a fresh clone, or after .pre-commit-config.yaml gains
# new hook stages.
#
# Requires: pre-commit (https://pre-commit.com/). The script fails fast with
# install instructions if it is missing.

set -euo pipefail

if ! command -v pre-commit >/dev/null 2>&1; then
    cat >&2 <<'EOF'
install-hooks: pre-commit is not installed.

Install it with one of:
    pipx install pre-commit          # recommended (isolated)
    pip install --user pre-commit
    brew install pre-commit          # macOS
    apt install pre-commit           # Debian/Ubuntu

Then re-run: make install-hooks
EOF
    exit 1
fi

repo_root=$(git rev-parse --show-toplevel 2>/dev/null || pwd)
cd "$repo_root"

# pre-commit framework manages both stages from a single config file.
# Installing both hook types at once means a contributor only runs
# `make install-hooks` once, regardless of which checks live where.
pre-commit install --hook-type pre-commit --hook-type pre-push

# Warm the hook environments so the first commit isn't slow. Best-effort:
# if the system Python or Go toolchain is missing pieces, surface that now
# rather than at commit time.
echo
echo "Warming hook environments (first run can take a minute)..."
pre-commit install-hooks || {
    echo
    echo "install-hooks: pre-commit install-hooks failed; commits will still work" >&2
    echo "but you will see the same errors on first commit. Fix the toolchain"  >&2
    echo "issues above and re-run: make install-hooks"                          >&2
    exit 1
}

cat <<'EOF'

Hooks installed.

  pre-commit -> .git/hooks/pre-commit  (gofmt, goimports, go vet, go mod tidy,
                                        staticcheck, schema-version invariant,
                                        gitleaks, generic file hygiene)
  pre-push   -> .git/hooks/pre-push    (fast invariant tests:
                                        internal/diag/backup,
                                        internal/diag/migrate)

Run `pre-commit run --all-files` to lint the whole tree, or
`make precommit` to run the same chain CI runs.
EOF
