#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/lib/test_env.sh
require_test_databases
for migration in migrations/[0-9][0-9][0-9]_*.sql; do
    psql "$DAY12_TEST_DATABASE_URL" -X -v ON_ERROR_STOP=1 -f "$migration"
done
echo 'PASS: migrations 001-006 applied to the dedicated queue test database'
