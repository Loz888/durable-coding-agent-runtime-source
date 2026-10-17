#!/usr/bin/env bash
set -euo pipefail
trap 'echo "FAIL: demo_mock (line $LINENO)" >&2' ERR
cd "$(dirname "$0")/.."
source scripts/lib/test_env.sh
require_test_databases
unset DEEPSEEK_API_KEY
log="$(mktemp "${TMPDIR:-/tmp}/agent-runtime-demo.XXXXXX.jsonl")"
echo "real ClaimBatch -> existing Scheduler.execute -> CodingHandler -> Mock patch -> Go Test -> fenced Checkpoint"
echo "Evidence: $log"
# The integration harness owns temporary Git repositories, Worktrees and test rows.
# It calls the existing production modules; no alternate queue or repair loop.
go test -json -count=1 -timeout 180s ./durable -run '^TestDay20ExistingSchedulerMockRepair$' | tee "$log"
python3 scripts/check_test_results.py "$log" --require TestDay20ExistingSchedulerMockRepair
echo 'PASS: demo_mock'
