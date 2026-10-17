#!/usr/bin/env bash
set -euo pipefail

# Run from: ~/go-learning/agent-runtime-project/day10/runtime-app
# This test audits coverage and skip conditions without changing database records.
ROOT="$(pwd)"
OUT="${OUT_DIR:-$ROOT/day10-experiment-b-output}"
mkdir -p "$OUT"

if [[ ! -f go.mod || ! -d persistence ]]; then
    echo 'ERROR: Run this inside runtime-app (go.mod and persistence/ required).' >&2
    exit 1
fi

echo '[1/5] List persistence tests:'
go test -list '^Test' ./persistence | tee "$OUT/test-list.log"

echo
echo '[2/5] Execute persistence tests verbosely and uncached:'
go test -v -count=1 ./persistence | tee "$OUT/persistence-tests.log"
if grep -q -- '--- SKIP:' "$OUT/persistence-tests.log"; then
    echo
    echo 'WARNING: Some tests were skipped:'
    grep -- '--- SKIP:' "$OUT/persistence-tests.log" || true
fi

echo
echo '[3/5] Persistence package coverage (uncached):'
go test -count=1 -covermode=atomic \
    -coverprofile="$OUT/persistence-only.cover" ./persistence | tee "$OUT/persistence-coverage.log"
go tool cover -func="$OUT/persistence-only.cover" | tee "$OUT/persistence-functions.log"

echo
echo '[4/5] Cross-package coverage (e.g. HTTP API -> persistence):'
go test -count=1 -covermode=atomic -coverpkg=./persistence \
    -coverprofile="$OUT/persistence-all-tests.cover" ./... | tee "$OUT/all-package-tests.log"
go tool cover -func="$OUT/persistence-all-tests.cover" | tee "$OUT/cross-package-functions.log"

echo
echo '[5/5] Search for DB test prerequisites / skip reasons:'
if [[ -d internal/testdb ]]; then
    grep -RnE 't\.Skip|t\.Skipf|LookupEnv|Getenv|DATABASE_URL|TEST_DATABASE_URL|PGHOST|PGDATABASE' \
        persistence internal/testdb --include='*.go' || true
else
    grep -RnE 't\.Skip|t\.Skipf|LookupEnv|Getenv|DATABASE_URL|TEST_DATABASE_URL|PGHOST|PGDATABASE' \
        persistence --include='*.go' || true
fi

echo
printf 'Artifacts in: %s\n' "$OUT"
echo 'Interpretation:'
echo '  * A skipped integration test means PostgreSQL behavior has NOT been verified by that test.'
echo '  * A package can report ok while its executed production statements have 0% coverage.'
echo '  * If cross-package coverage is higher, other packages exercise persistence code.'
echo '  * Coverage does not prove query correctness; inspect assertions and DB rows.'
