#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

: "${DAY12_TEST_DATABASE_URL:?Set DAY12_TEST_DATABASE_URL}"

DB_NAME="$(
    psql "$DAY12_TEST_DATABASE_URL" \
        -X -Atc 'SELECT current_database()'
)"

if [[ "$DB_NAME" != 'agent_runtime_day14_test' ]]; then
    echo "REFUSING: expected test database, got $DB_NAME" >&2
    exit 2
fi

# LAB ONLY: synthetic Claim row, not Day14 Scheduler.
psql "$DAY12_TEST_DATABASE_URL" \
    -X -v ON_ERROR_STOP=1 <<'SQL'
CREATE SCHEMA IF NOT EXISTS day19_lab;

CREATE TABLE IF NOT EXISTS day19_lab.agent_runs (
    run_id text PRIMARY KEY,
    lease_owner text,
    lease_epoch bigint NOT NULL,
    status text NOT NULL,
    lease_until timestamptz NOT NULL,
    completion_pending boolean NOT NULL DEFAULT false
);
SQL

# Install patch tables in day19_lab, not day19.
sed \
  -e 's/day19\./day19_lab./g' \
  -e 's/EXISTS day19;/EXISTS day19_lab;/g' \
  migrations/005_durable_patch.sql \
  | psql "$DAY12_TEST_DATABASE_URL" \
      -X -v ON_ERROR_STOP=1

# Additive lab compatibility; existing rows are not restamped as verified.
psql "$DAY12_TEST_DATABASE_URL" -X -v ON_ERROR_STOP=1 -c "ALTER TABLE day19_lab.agent_runs ADD COLUMN IF NOT EXISTS completion_pending boolean NOT NULL DEFAULT false"
sed 's/day19\./day19_lab./g' migrations/010_verification_input.sql |
  psql "$DAY12_TEST_DATABASE_URL" -X -v ON_ERROR_STOP=1

# Apply the worktree identity extension in the same isolated lab schema.
sed 's/day19\./day19_lab./g' migrations/006_repair_binding.sql |
  psql "$DAY12_TEST_DATABASE_URL" -X -v ON_ERROR_STOP=1

LAB="evaldata/day19-lab/$(date +%Y%m%d-%H%M%S)-$$"

SRC="$LAB/source"
BASE="$LAB/worktrees"

mkdir -p "$SRC" "$BASE" bin

cp \
  evaldata/fixtures/divide-assert/go.mod \
  evaldata/fixtures/divide-assert/math.go \
  evaldata/fixtures/divide-assert/math_test.go \
  "$SRC/"

gofmt -w "$SRC/math.go" "$SRC/math_test.go"

git -C "$SRC" init -q
git -C "$SRC" add .

git -C "$SRC" \
    -c user.name='Day19 Lab' \
    -c user.email='day19@example.invalid' \
    commit -qm baseline

SRC_ABS="$(cd "$SRC" && pwd)"
BASE_ABS="$(cd "$BASE" && pwd)"

RUN="day19-demo-$(date +%s%N)"
WT="$BASE_ABS/$RUN"

OWNER_A=lab-owner-a
OWNER_B=lab-owner-b

go build \
    -o bin/day17-patch \
    ./cmd/day17-patch

go build \
    -o bin/day19-patch \
    ./cmd/day19-patch

bin/day17-patch \
    -action prepare \
    -source "$SRC_ABS" \
    -base "$BASE_ABS" \
    -run "$RUN"

psql "$DAY12_TEST_DATABASE_URL" \
    -X -v ON_ERROR_STOP=1 \
    -c "INSERT INTO day19_lab.agent_runs
          (run_id,lease_owner,lease_epoch,status,lease_until)
        VALUES (
          '$RUN',
          '$OWNER_A',
          1,
          'running',
          clock_timestamp()+interval '2 minutes'
        )"

run19() {
    bin/day19-patch \
        -queue-schema day19_lab \
        -patch-schema day19_lab \
        -id "$RUN" \
        "$@"
}

echo '=== 1. Persist worktree and patch intent ==='

run19 \
    -action register \
    -owner "$OWNER_A" \
    -epoch 1 \
    -worktree "$WT" \
    -max-rounds 2

run19 \
    -action prepare \
    -owner "$OWNER_A" \
    -epoch 1 \
    -worktree "$WT" \
    -file math.go \
    -patch p1 \
    -old 'return a * b' \
    -new 'return a / b'

echo '=== 2. SIGKILL after Rename ==='

set +e

run19 \
    -action apply \
    -owner "$OWNER_A" \
    -epoch 1 \
    -patch p1 \
    -inject-sigkill

CODE=$?

set -e

if [[ "$CODE" -ne 137 ]]; then
    echo "Expected SIGKILL exit=137, got $CODE" >&2
    exit 1
fi

# File was changed; DB result wasn't committed.
grep -qF 'return a / b' "$WT/math.go"

psql "$DAY12_TEST_DATABASE_URL" \
    -X -v ON_ERROR_STOP=1 \
    -c "SELECT patch_id,state,prepared_epoch,result_epoch
        FROM day19_lab.patch_ops
        WHERE run_id='$RUN'"

echo '=== 3. Synthetic Worker takeover ==='

psql "$DAY12_TEST_DATABASE_URL" \
    -X -v ON_ERROR_STOP=1 \
    -c "UPDATE day19_lab.agent_runs
        SET lease_owner='$OWNER_B',
            lease_epoch=2,
            lease_until=clock_timestamp()+interval '2 minutes'
        WHERE run_id='$RUN'"

echo '=== 4. Find pending patch ==='

run19 \
    -action pending \
    -owner "$OWNER_B" \
    -epoch 2

echo '=== 5. Old Owner must be rejected ==='

set +e

run19 \
    -action apply \
    -owner "$OWNER_A" \
    -epoch 1 \
    -patch p1

OLD_CODE=$?

set -e

if [[ "$OLD_CODE" -eq 0 ]]; then
    echo 'ERROR: old owner accepted' >&2
    exit 1
fi

echo '=== 6. Reconcile without replaying patch ==='

run19 \
    -action apply \
    -owner "$OWNER_B" \
    -epoch 2 \
    -patch p1

run19 \
    -action verify \
    -owner "$OWNER_B" \
    -epoch 2 \
    -patch p1 \
    -test-timeout 30s

run19 \
    -action summary

echo '=== 7. Confirm original untouched ==='

git -C "$SRC_ABS" \
    diff --exit-code HEAD -- math.go

grep -qF 'return a * b' "$SRC_ABS/math.go"

echo "PASS: crash recovery, fencing, budget, source isolation"
echo "RUN_ID=$RUN"
echo "WORKTREE=$WT"
