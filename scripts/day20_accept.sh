#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

: "${DAY12_TEST_DATABASE_URL:?Set dedicated DAY12_TEST_DATABASE_URL}"
: "${TEST_DATABASE_URL:?Set dedicated TEST_DATABASE_URL}"

DB=$(
    psql "$DAY12_TEST_DATABASE_URL" \
        -X -Atc 'SELECT current_database()'
)

if [[ "$DB" != 'agent_runtime_day14_test' ]]; then
    echo "Refusing non-test database: $DB" >&2
    exit 2
fi

# All acceptance tests run without real
# DeepSeek API credentials.
unset DEEPSEEK_API_KEY

mkdir -p evaldata/day20-logs

echo "=== 1. gofmt ==="

gofmt -w \
    repair20 \
    durable/coding.go \
    durable/day20_integration_test.go

echo "=== 2. Day20 smoke tests ==="

go test \
    -v \
    -count=1 \
    -timeout 300s \
    ./repair20 ./durable \
    -run '^TestDay20' \
    | tee evaldata/day20-logs/smoke.log

if grep -q '^--- SKIP:' \
    evaldata/day20-logs/smoke.log; then

    echo 'Unexpected SKIP in Day20 acceptance' >&2
    exit 1
fi

echo "=== 3. Day20 race tests ==="

go test \
    -race \
    -count=1 \
    -timeout 300s \
    ./repair20 ./durable \
    -run '^TestDay20'

echo "=== 4. Repeat repair loop 10 times ==="

go test \
    -count=10 \
    -timeout 600s \
    ./repair20 \
    -run '^TestDay20'

echo "=== 5. Full project regression ==="

go test \
    -count=1 \
    -timeout 300s \
    ./...

echo "=== 6. Full project race detector ==="

go test \
    -race \
    -count=1 \
    -timeout 300s \
    ./...

echo "=== 7. Static analysis ==="

go vet ./...

echo "=== 8. Coverage ==="

go test \
    -cover \
    -count=1 \
    -timeout 300s \
    ./repair20 ./durable

echo "DAY20 ACCEPTANCE COMPLETE"
