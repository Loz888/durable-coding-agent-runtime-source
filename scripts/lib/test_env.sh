#!/usr/bin/env bash
# Source only: shared read-only database guards for tests and demos.
require_test_databases() {
    : "${DAY12_TEST_DATABASE_URL:?Set DAY12_TEST_DATABASE_URL to the dedicated queue test database}"
    : "${TEST_DATABASE_URL:?Set TEST_DATABASE_URL to the dedicated persistence test database}"
    local actual
    actual="$(psql "$DAY12_TEST_DATABASE_URL" -X -At -v ON_ERROR_STOP=1 -c 'SELECT current_database()')"
    [[ "$actual" == agent_runtime_day14_test ]] || { echo "REFUSING non-test queue database: $actual" >&2; return 2; }
    actual="$(psql "$TEST_DATABASE_URL" -X -At -v ON_ERROR_STOP=1 -c 'SELECT current_database()')"
    [[ "$actual" == agent_runtime_test ]] || { echo "REFUSING non-test persistence database: $actual" >&2; return 2; }
}
