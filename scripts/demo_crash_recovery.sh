#!/usr/bin/env bash
set -euo pipefail
trap 'echo "FAIL: demo_crash_recovery (line $LINENO)" >&2' ERR
cd "$(dirname "$0")/.."
source scripts/lib/test_env.sh
require_test_databases
unset DEEPSEEK_API_KEY
log="$(mktemp "${TMPDIR:-/tmp}/agent-runtime-demo.XXXXXX.jsonl")"
echo "real child SIGKILL -> durable Patch recovery -> real Epoch takeover -> final Checkpoint"
echo "Evidence: $log"
# The integration harness owns temporary Git repositories, Worktrees and test rows.
# It calls the existing production modules; no alternate queue or repair loop.
go test -json -count=1 -timeout 180s ./durable ./internal/integration20 -run '^(TestDay20RealSIGKILLBeforeCheckpoint|TestPre20SIGKILLRecoveryMatrix)$' | tee "$log"
python3 scripts/check_test_results.py "$log" --require TestDay20RealSIGKILLBeforeCheckpoint --require TestPre20SIGKILLRecoveryMatrix/prepared --require TestPre20SIGKILLRecoveryMatrix/applied --require TestPre20SIGKILLRecoveryMatrix/verified_pass --require TestPre20SIGKILLRecoveryMatrix/verified_fail
echo 'PASS: demo_crash_recovery'
