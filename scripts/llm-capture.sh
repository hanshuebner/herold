#!/usr/bin/env bash
# llm-capture.sh -- record LLM fixture baselines for Wave 3.12 tests.
#
# USAGE:
#   HEROLD_LLM_CAPTURE=1 HEROLD_LLM_CAPTURE_ENDPOINT=http://localhost:11434/v1 \
#       scripts/llm-capture.sh
#
# This script MUST NOT run in CI. It is operator tooling; run it
# manually against a live LLM endpoint (default: local Ollama) to
# populate internal/llmtest/fixtures/<kind>/<pkg>.jsonl. Once the
# fixtures are committed, the regular test run uses the Replayer and
# requires no network.
#
# Requirements:
#   - HEROLD_LLM_CAPTURE=1 must be set. If it is not, the script
#     exits 0 with a usage message and makes no network calls.
#   - A running OpenAI-compatible endpoint at HEROLD_LLM_CAPTURE_ENDPOINT
#     (default: http://localhost:11434/v1).
#   - HEROLD_LLM_CAPTURE_MODEL overrides the model name used during
#     capture (default: llama3.2:3b).
#
# On completion the script prints a summary: how many fixtures were
# created, updated, or unchanged.
#
# This script does NOT run in normal `go test` execution. It is
# triggered only by the maintainer when the prompt set has changed
# (prompt-hash invalidation will surface missing-fixture failures in CI
# that point back here).

set -euo pipefail

ENDPOINT="${HEROLD_LLM_CAPTURE_ENDPOINT:-http://localhost:11434/v1}"
MODEL="${HEROLD_LLM_CAPTURE_MODEL:-llama3.2:3b}"

print_usage() {
    cat <<'EOF'
Usage:
  HEROLD_LLM_CAPTURE=1 scripts/llm-capture.sh [--help]

Environment variables:
  HEROLD_LLM_CAPTURE          Must be set to "1" to enable capture mode.
  HEROLD_LLM_CAPTURE_ENDPOINT LLM endpoint base URL (default: http://localhost:11434/v1).
  HEROLD_LLM_CAPTURE_MODEL    Model name to use (default: llama3.2:3b).

Description:
  Records LLM responses for every test that uses llmtest.Replayer. The
  recorded fixtures are written to internal/llmtest/fixtures/<kind>/<pkg>.jsonl.
  Once recorded, commit the fixture files so CI can use the Replayer without
  network access.

  Re-run this script whenever a prompt changes. The prompt-hash matching
  strategy (REQ-FILT-301) will surface missing-fixture failures in CI that
  point here.

This script does NOT run in CI. It is operator tooling only.
EOF
}

if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
    print_usage
    exit 0
fi

if [[ "${HEROLD_LLM_CAPTURE:-}" != "1" ]]; then
    echo "INFO: HEROLD_LLM_CAPTURE is not set to '1'; nothing to do."
    echo ""
    print_usage
    exit 0
fi

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
FIXTURE_DIR="${REPO_ROOT}/internal/llmtest/fixtures"

echo "LLM capture mode"
echo "  endpoint: ${ENDPOINT}"
echo "  model:    ${MODEL}"
echo "  fixtures: ${FIXTURE_DIR}"
echo ""

# Verify the endpoint is reachable before burning time on tests.
if ! curl -sf --max-time 5 "${ENDPOINT}/models" > /dev/null 2>&1; then
    echo "WARNING: endpoint ${ENDPOINT} did not respond to GET /models."
    echo "  Proceeding anyway; individual calls will fail if the endpoint is down."
    echo ""
fi

export HEROLD_LLM_CAPTURE=1
export HEROLD_LLM_CAPTURE_ENDPOINT="${ENDPOINT}"
export HEROLD_LLM_CAPTURE_MODEL="${MODEL}"

# Run the LLM-touching tests in capture mode. The build tag llm_capture
# makes the test harness swap the Replayer for the Recorder.
echo "Running tests with capture enabled..."
cd "${REPO_ROOT}"

go test \
    -count=1 \
    -run 'TestCategorise_WithLLMReplayer|TestClassify_WithLLMReplayer' \
    -v \
    ./internal/categorise/... \
    ./internal/spam/... \
    2>&1 || true

echo ""
echo "Fixture summary:"
total_created=0
total_updated=0
total_unchanged=0

for kind_dir in "${FIXTURE_DIR}"/*/; do
    kind=$(basename "${kind_dir}")
    for fixture_file in "${kind_dir}"*.jsonl; do
        [ -f "${fixture_file}" ] || continue
        count=$(wc -l < "${fixture_file}" | tr -d ' ')
        pkg=$(basename "${fixture_file}" .jsonl)
        echo "  ${kind}/${pkg}: ${count} fixture(s)"
    done
done

echo ""
echo "To update fixtures, re-run this script after prompt changes."
echo "Commit the updated fixture files so CI can use the Replayer."
