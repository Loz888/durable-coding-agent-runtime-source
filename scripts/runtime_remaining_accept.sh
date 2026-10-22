#!/usr/bin/env bash
# Offline/real-container acceptance only. Live calls require a separate explicit budget.
set -euo pipefail
cd "$(dirname "$0")/.."
unset DEEPSEEK_API_KEY
out="${1:-}"
image="${RUNTIME_SANDBOX_IMAGE:-}"
if [[ -z "$out" || "$out" != /* || -e "$out" ]]; then
    echo 'Supply a new absolute private output directory outside the repository.' >&2
    exit 2
fi
repo="$(pwd -P)"
resolved="$(realpath -m -- "$out")"
if [[ "$resolved" == "$repo" || "$resolved" == "$repo/"* ]]; then
    echo 'Acceptance output must be outside the repository.' >&2
    exit 2
fi
source scripts/lib/test_env.sh
require_test_databases
mkdir -m 700 -- "$resolved"
bash scripts/setup_test_db.sh >"$resolved/migrations.log" 2>&1
bash scripts/test.sh -p 1 -timeout 360s ./... >"$resolved/full.log" 2>&1
bash scripts/test.sh -race -p 1 -timeout 360s ./... >"$resolved/race.log" 2>&1
go vet ./... >"$resolved/vet.log" 2>&1
go build ./... >"$resolved/build.log" 2>&1
if [[ -n "$image" ]]; then
    go run ./cmd/sandbox-accept -image "$image" -output "$resolved/sandbox.json" >"$resolved/sandbox.log" 2>&1
    go test -json -tags dockeraccept -p 1 -count=1 -timeout 150s ./internal/integration20 -run '^TestPortableIsolatedContainerHosts$' >"$resolved/portable.jsonl"
    python3 scripts/check_test_results.py "$resolved/portable.jsonl"
else
    echo 'Real Docker acceptance NOT RUN: set RUNTIME_SANDBOX_IMAGE to a preloaded pinned digest.' | tee "$resolved/docker-not-run.txt"
fi
go run ./cmd/durable-benchmark -tasks 200 -workers 1,2,4,8 -repetitions 3 -output "$resolved/burst.json" >"$resolved/burst.log"
go run ./cmd/durable-benchmark -tasks 100 -workers 2,4 -repetitions 3 -arrival-every 10ms -output "$resolved/stream.json" >"$resolved/stream.log"
go run ./cmd/durable-benchmark -tasks 12 -workers 1,2,4 -repetitions 3 -workload go-test -output "$resolved/test-workload.json" >"$resolved/test-workload.log"
echo "Acceptance completed; inspect scope and per-case evidence in $resolved. Paid calls: 0."
