#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

: "${DAY12_DATABASE_URL:?DAY12_DATABASE_URL is empty}"
: "${DAY12_TEST_DATABASE_URL:?DAY12_TEST_DATABASE_URL is empty}"

echo "========== DATABASE =========="

psql "$DAY12_DATABASE_URL" \
  -X -v ON_ERROR_STOP=1 <<'SQL'
SELECT
    current_database() AS database_name;

SELECT
    run_id,
    status,
    lease_epoch,
    max_attempts,
    retry_count,
    lease_owner IS NOT NULL AS has_owner,
    lease_until,
    lease_until > clock_timestamp() AS lease_valid
FROM day12.agent_runs
WHERE queue_enabled = TRUE
  AND status = 'running'
ORDER BY run_id;
SQL

echo "========== DURATION METRICS =========="

psql "$DAY12_DATABASE_URL" \
  -X -v ON_ERROR_STOP=1 <<'SQL'
SELECT
    COUNT(*) AS samples,
    percentile_cont(0.5)
        WITHIN GROUP (
            ORDER BY EXTRACT(
                EPOCH FROM finished_at - first_claimed_at
            )
        ) AS p50_seconds,
    percentile_cont(0.95)
        WITHIN GROUP (
            ORDER BY EXTRACT(
                EPOCH FROM finished_at - first_claimed_at
            )
        ) AS p95_seconds
FROM day12.agent_runs
WHERE queue_enabled = TRUE
  AND status IN ('succeeded', 'failed')
  AND first_claimed_at IS NOT NULL
  AND finished_at IS NOT NULL;
SQL

echo "========== DURABLE INTEGRATION =========="

grep -RnE \
'ClaimBatch|RetryLater|FinishFailure|SweepExhausted|RecordCheckpoint|max_attempts|finished_at' \
durable cmd/day14-worker || true

echo "========== ALL TESTS =========="

go test -count=1 ./...

echo "========== ALL RACE TESTS =========="

go test -race -count=1 ./...

echo "========== VET =========="

go vet ./...

echo "========== AUDIT COMPLETE =========="
