#!/usr/bin/env bash
set -euo pipefail

# Run this inside your Day10 runtime-app root (with go.mod).
ROOT="$(pwd)"
OUT="${OUT_DIR:-$ROOT/day10-experiment-a-output}"
mkdir -p "$OUT"
BASE="http://127.0.0.1:8090"
A_PID=""; B_PID=""; C_PID=""

cleanup() {
    for pid in "$A_PID" "$B_PID" "$C_PID"; do
        if [[ -n "$pid" ]]; then kill "$pid" 2>/dev/null || true; fi
    done
    for pid in "$A_PID" "$B_PID" "$C_PID"; do
        if [[ -n "$pid" ]]; then wait "$pid" 2>/dev/null || true; fi
    done
}
trap cleanup EXIT

if [[ ! -f go.mod || ! -f distributed/coordinator.go ]]; then
    echo 'ERROR: Please run inside day10/runtime-app' >&2
    exit 1
fi
if curl --noproxy '*' --max-time 1 -fsS "$BASE/workers" >/dev/null 2>&1; then
    echo 'ERROR: A Coordinator is already listening on port 8090. Stop it first.' >&2
    exit 1
fi

printf '\n[1/7] Build Coordinator and Worker binaries...\n'
go build -o "$OUT/day10-coordinator" ./cmd/day10-coordinator
go build -o "$OUT/day10-worker" ./cmd/day10-worker

"$OUT/day10-coordinator" >"$OUT/coordinator.log" 2>&1 & C_PID=$!
ready=0
for i in {1..50}; do
    if curl --noproxy '*' --max-time 1 -fsS "$BASE/workers" >/dev/null 2>&1; then
        ready=1; break
    fi
    sleep 0.1
done
if (( !ready )); then
    echo 'ERROR: Coordinator did not start. See coordinator.log' >&2
    exit 1
fi
printf '[2/7] Coordinator ready.\n'

WORKER_ID=worker-a EXEC_DELAY_MS=5000 "$OUT/day10-worker" >"$OUT/worker-a.log" 2>&1 & A_PID=$!
registered=0
for i in {1..50}; do
    if grep -q 'worker-a registered' "$OUT/worker-a.log"; then registered=1; break; fi
    sleep 0.1
done
if (( !registered )); then
    echo 'ERROR: Worker A did not register' >&2
    exit 1
fi
printf '[3/7] Slow Worker A registered. Enqueue Job 2001...\n'

status=$(curl --noproxy '*' --max-time 3 -sS -o "$OUT/create.json" -w '%{http_code}' \
    -X POST "$BASE/jobs" -H 'Content-Type: application/json' \
    -d '{"id":2001,"tool_name":"uppercase","payload":"lease test"}')
if [[ "$status" != 201 ]]; then
    echo "ERROR: Create Job returned $status" >&2
    cat "$OUT/create.json" >&2
    exit 1
fi

first=""
for i in {1..50}; do
    first=$(curl --noproxy '*' --max-time 2 -fsS "$BASE/jobs/2001")
    if [[ "$first" == *'"state":"leased"'* && "$first" == *'"worker_id":"worker-a"'* && "$first" == *'"epoch":1'* ]]; then
        break
    fi
    sleep 0.08
done
if [[ "$first" != *'"worker_id":"worker-a"'* || "$first" != *'"epoch":1'* ]]; then
    echo 'ERROR: Failed to observe first claim by Worker A' >&2
    echo "$first" >&2
    exit 1
fi
printf '[4/7] Verified Worker A owns Job 2001 Epoch=1:\n%s\n' "$first"
printf '%s\n' "$first" > "$OUT/first-claim.json"

WORKER_ID=worker-b "$OUT/day10-worker" >"$OUT/worker-b.log" 2>&1 & B_PID=$!
registered=0
for i in {1..50}; do
    if grep -q 'worker-b registered' "$OUT/worker-b.log"; then registered=1; break; fi
    sleep 0.1
done
if (( !registered )); then
    echo 'ERROR: Worker B did not register before lease expiry' >&2
    exit 1
fi
printf '[5/7] Worker B registered. Waiting for lease expiry and takeover...\n'

last=""
completed=0
for i in {1..120}; do
    last=$(curl --noproxy '*' --max-time 2 -fsS "$BASE/jobs/2001")
    if [[ "$last" == *'"state":"succeeded"'* && "$last" == *'"worker_id":"worker-b"'* && "$last" == *'"epoch":2'* && "$last" == *'"attempts":2'* && "$last" == *'"output":"LEASE TEST"'* ]]; then
        completed=1; break
    fi
    sleep 0.1
done
if (( !completed )); then
    echo 'ERROR: Expected Worker B takeover did not occur. Current job state:' >&2
    echo "$last" >&2
    exit 1
fi
printf '[6/7] Worker B took over and completed Epoch=2:\n%s\n' "$last"
printf '%s\n' "$last" > "$OUT/final-job.json"

lost=0
for i in {1..120}; do
    if grep -q 'complete task 2001: .*lease lost' "$OUT/worker-a.log"; then
        lost=1; break
    fi
    sleep 0.1
done
if (( !lost )); then
    echo 'ERROR: Worker A has not logged a rejected stale completion' >&2
    exit 1
fi
printf '[7/7] Verified Worker A stale Complete was rejected.\n'
printf '\n===== Worker A log =====\n'; cat "$OUT/worker-a.log"
printf '\n===== Worker B log =====\n'; cat "$OUT/worker-b.log"
printf '\nPASS: A Epoch=1 rejected, B Epoch=2 succeeded (same Job 2001).\n'
printf 'Logs and JSON saved in: %s\n' "$OUT"
