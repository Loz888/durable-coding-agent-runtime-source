#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
unset DEEPSEEK_API_KEY DAY21_RUNS_OUT DAY12_TEST_DATABASE_URL TEST_DATABASE_URL
log="$(mktemp "${TMPDIR:-/tmp}/agent-runtime-unit.XXXXXX.jsonl")"
# These packages' entire test suites are independent of PostgreSQL.
# Mixed packages' unit tests also run in the full integration job.
go test -json -count=1 -timeout 180s     ./cmd/day21-eval ./cmd/artifact-inventory ./cmd/durable-benchmark ./internal/goinput ./eval21 ./artifactstore ./distributed ./eval16 ./executor ./internal/testdb ./internal/providererr ./internal/boundedlog ./observability     ./repair17 ./repair18 ./repair20 ./repodiag ./runtimecore | tee "$log"
python3 scripts/check_test_results.py "$log"
python3 -B -m unittest discover -s scripts -p test_clock_probe.py -v
