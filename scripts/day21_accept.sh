#!/usr/bin/env bash
set -euo pipefail
trap 'echo "FAIL: day21_accept (line $LINENO)" >&2' ERR
cd "$(dirname "$0")/.."
source scripts/lib/test_env.sh
require_test_databases
unset DEEPSEEK_API_KEY DAY21_RUNS_OUT
# Each capture owns a new output directory; committed observations stay untouched.
if [[ -n "${DAY21_OUTPUT_DIR:-}" ]]; then
    output="$(realpath -m "$DAY21_OUTPUT_DIR")"
    case "$output/" in "$PWD/"*) echo 'REFUSING capture output inside the repository' >&2; exit 2;; esac
    mkdir -m 700 "$output"
else
    output="$(mktemp -d "${TMPDIR:-/tmp}/agent-runtime-day21.XXXXXX")"
fi
echo "Evaluation evidence: $output"
./scripts/check_fmt.sh

go test -json -count=1 -timeout 360s ./eval21 ./cmd/day21-eval | tee "$output/unit-tests.jsonl"
python3 scripts/check_test_results.py "$output/unit-tests.jsonl"     --require TestDay21ScoreDenominators --require TestDay21CLIReportAndStrictMode

DAY21_RUNS_OUT="$output/runs.jsonl" go test -json -count=1 -timeout 360s     ./durable -run '^TestDay21Capture$' | tee "$output/capture-tests.jsonl"
required=(--require TestDay21Capture)
for scenario in divide-repair baseline-pass two-round-exhaust verified-pass-takeover; do
    for trial in 1 2 3; do
        required+=(--require "TestDay21Capture/$scenario/$trial")
    done
done
python3 scripts/check_test_results.py "$output/capture-tests.jsonl" "${required[@]}"

# Recompute the committed evidence, then score this new capture independently.
go run ./cmd/day21-eval -cases evaldata/day21/cases.json     -runs evaldata/day21/runs.jsonl -report "$output/recorded-report.md"     -summary "$output/recorded-summary.json" -require-all
cmp evaldata/day21/summary.json "$output/recorded-summary.json"
cmp evaldata/day21/report.md "$output/recorded-report.md"
go run ./cmd/day21-eval -cases evaldata/day21/cases.json     -runs "$output/runs.jsonl" -report "$output/report.md"     -summary "$output/summary.json" -require-all

# Full regression/race are separate test.sh and CI entrypoints. This captures one dataset.
echo 'PASS: day21_accept (12 real deterministic trials; no live model API)'
