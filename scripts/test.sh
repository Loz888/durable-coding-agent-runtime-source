#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/lib/test_env.sh
require_test_databases
unset DEEPSEEK_API_KEY
log="$(mktemp "${TMPDIR:-/tmp}/agent-runtime-tests.XXXXXX.jsonl")"
echo "Test evidence: $log"
if (( $# == 0 )); then set -- ./...; fi
go test -json -count=1 -timeout 300s "$@" | tee "$log"
python3 scripts/check_test_results.py "$log"
