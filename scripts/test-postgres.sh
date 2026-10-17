#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."

GO_BIN="$(command -v go || true)"
if [[ -z "$GO_BIN" ]]; then GO_BIN=/usr/local/go/bin/go; fi
# Always use the dedicated database, never the server's DATABASE_URL.
export TEST_DATABASE_URL="host=/var/run/postgresql dbname=agent_runtime_test user=$(id -un) sslmode=disable"
# Day12-15 tests use their own dedicated database, validated by reliability15.
export DAY12_TEST_DATABASE_URL="host=/var/run/postgresql dbname=agent_runtime_day14_test user=$(id -un) sslmode=disable"
psql "$DAY12_TEST_DATABASE_URL" -c 'SELECT current_database(), current_user;'


pg_lsclusters
psql "$TEST_DATABASE_URL" -c 'SELECT current_database(), current_user;'
connected_database="$(psql "$TEST_DATABASE_URL" -tAXc 'SELECT current_database();')"
[[ "$connected_database" == "agent_runtime_test" ]] || { echo "Unexpected test database"; exit 1; }

"$GO_BIN" test -v -count=1 -timeout 60s ./persistence
"$GO_BIN" test -race -count=1 -timeout 60s ./persistence
"$GO_BIN" test -count=1 -coverprofile=persistence.cover -timeout 60s ./persistence
"$GO_BIN" tool cover -func=persistence.cover | tee persistence-coverage.txt
"$GO_BIN" test -v -count=1 -timeout 120s ./...
"$GO_BIN" test -race -count=1 -timeout 120s ./...
"$GO_BIN" vet ./...
psql "$TEST_DATABASE_URL" -c "SELECT count(*) AS remaining_test_schemas FROM pg_namespace WHERE nspname LIKE 'runtime_test_%';"
