#!/usr/bin/env bash
set -euo pipefail

: "${DAY12_DATABASE_URL:?请先设置 DAY12_DATABASE_URL}"

: "${DAY15_REPO:?Set DAY15_REPO to a trusted local Git repository}"
REPO="$DAY15_REPO"

fail() {
    echo "FAIL: $*" >&2
    exit 1
}

[[ -d "$REPO" ]] || fail "Repo 不存在: $REPO"

for bin in /tmp/day15-submit /tmp/day15-worker; do
    [[ -x "$bin" ]] || fail "请先编译 $bin"
done

# 检查真实 CLI 参数
submit_help=$(/tmp/day15-submit -h 2>&1 || true)
worker_help=$(/tmp/day15-worker -h 2>&1 || true)

for flag in id repo mode priority goal; do
    grep -Eq -- "(^|[[:space:]])-${flag}([[:space:]]|$)" \
        <<< "$submit_help" ||
        fail "Submit 不支持 -$flag，请检查 -h"
done

for flag in name workers poll ttl renew; do
    grep -Eq -- "(^|[[:space:]])-${flag}([[:space:]]|$)" \
        <<< "$worker_help" ||
        fail "Worker 不支持 -$flag，请检查 -h"
done

dbq() {
    psql "$DAY12_DATABASE_URL" \
        -X -qAt -v ON_ERROR_STOP=1 "$@"
}

# 避免 DAY12_DATABASE_URL 为空时连接错误的数据库
DB_NAME=$(dbq -c "SELECT current_database()")
[[ "$DB_NAME" == "agent_runtime" ]] ||
    fail "当前数据库为 $DB_NAME，不是 agent_runtime"

# 确认 Day15 Migration 已生效
dbq -c "
SELECT max_attempts, failure_kind,
       first_claimed_at, finished_at
FROM day12.agent_runs LIMIT 0;
" >/dev/null

PIDS=()

is_worker() {
    [[ "$(readlink -f "/proc/$1/exe" 2>/dev/null || true)" \
       == "/tmp/day15-worker" ]]
}

stop_worker() {
    local pid="$1"
    local signal="${2:-TERM}"

    if is_worker "$pid"; then
        kill -s "$signal" "$pid" 2>/dev/null || true
    fi

    wait "$pid" 2>/dev/null || true
}

cleanup() {
    for pid in "${PIDS[@]}"; do
        if is_worker "$pid"; then
            kill -TERM "$pid" 2>/dev/null || true
        fi
    done
}

trap cleanup EXIT

new_id() {
    echo "day15-$1-$(date +%Y%m%d-%H%M%S)-$$"
}

submit_run() {
    local id="$1"

    /tmp/day15-submit \
        -id "$id" \
        -repo "$REPO" \
        -mode demo \
        -priority 100 \
        -goal "Day15 reliability fault injection experiment"
}

set_budget2() {
    local id="$1"
    local updated

    updated=$(dbq -v run_id="$id" <<'SQL'
UPDATE day12.agent_runs
SET max_attempts = 2
WHERE run_id = :'run_id'
  AND status = 'pending'
  AND lease_epoch = 0
RETURNING run_id;
SQL
)

    [[ "$updated" == "$id" ]] ||
        fail "预算设置失败：Run 可能已被领取"

    echo "Budget=2 confirmed"
}

# Worker 是真实独立二进制进程
start_worker() {
    local id="$1"
    local kind="$2"
    local tag="$3"

    local log="/tmp/day15-${tag}-${id}.log"

    env \
        DAY15_FAULT_RUN_ID="$id" \
        DAY15_FAULT_KIND="$kind" \
        /tmp/day15-worker \
            -name "day15-${tag}-$$" \
            -workers 1 \
            -poll 200ms \
            -ttl 6s \
            -renew 1s \
            >"$log" 2>&1 &

    LAST_PID=$!
    PIDS+=("$LAST_PID")

    echo "Worker=$tag PID=$LAST_PID"
    echo "Log=$log"
}

# 查询 Run 的关键状态
state() {
    local id="$1"

    dbq -v run_id="$id" <<'SQL'
SELECT
    status || '|' ||
    lease_epoch::text || '|' ||
    COALESCE(retry_count::text, 'NULL') || '|' ||
    COALESCE(failure_kind, '')
FROM day12.agent_runs
WHERE run_id = :'run_id';
SQL
}

# 等待状态出现，超时则判定实验失败
wait_state() {
    local id="$1"
    local expected_status="$2"
    local expected_epoch="$3"
    local timeout="$4"

    local start=$SECONDS

    while (( SECONDS - start < timeout )); do
        local value status epoch retries kind

        value=$(state "$id")
        IFS='|' read -r status epoch retries kind <<< "$value"

        if [[ "$status" == "$expected_status" ]] &&
           { [[ "$expected_epoch" == "*" ]] ||
             [[ "$epoch" == "$expected_epoch" ]]; }; then
            echo "MATCH: $value"
            return 0
        fi

        sleep 0.2
    done

    fail "超时：期望 $expected_status epoch=$expected_epoch；实际 $(state "$id")"
}

# SQL 必须返回 PASS
verify() {
    local id="$1"
    local result

    result=$(dbq -v run_id="$id")

    [[ "$result" == "PASS" ]] ||
        fail "SQL 验收失败: Run=$id, result=$result"

    echo "SQL verification PASS"
}

report() {
    local id="$1"

    psql "$DAY12_DATABASE_URL" \
        -X -v ON_ERROR_STOP=1 \
        -v run_id="$id" <<'SQL'
SELECT
    run_id,
    status,
    lease_epoch,
    max_attempts,
    retry_count,
    failure_kind,
    first_claimed_at,
    finished_at
FROM day12.agent_runs
WHERE run_id = :'run_id';

SELECT
    event_id,
    lease_epoch,
    revision,
    event_type,
    occurred_at
FROM day12.run_events
WHERE run_id = :'run_id'
ORDER BY event_id;
SQL
}

# ---------- A：正常成功 ----------
experiment_a() {
    local id
    id=$(new_id A)

    echo "Experiment A: $id"

    submit_run "$id"
    start_worker "$id" off A

    wait_state "$id" succeeded "*" 60

    verify "$id" <<'SQL'
SELECT CASE WHEN
    status = 'succeeded'
    AND lease_epoch >= 1
    AND first_claimed_at IS NOT NULL
    AND finished_at IS NOT NULL
    AND failure_kind IS NULL
THEN 'PASS' ELSE 'FAIL' END
FROM day12.agent_runs
WHERE run_id = :'run_id';
SQL

    report "$id"
}

# ---------- B：永久错误 ----------
experiment_b() {
    local id
    id=$(new_id B)

    echo "Experiment B: $id"

    submit_run "$id"
    start_worker "$id" permanent B

    wait_state "$id" failed "*" 45

    # 再观察 8 秒，检查是否错误地重新领取
    sleep 8

    verify "$id" <<'SQL'
SELECT CASE WHEN
    status = 'failed'
    AND lease_epoch = 1
    AND retry_count = 0
    AND failure_kind = 'permanent'
    AND finished_at IS NOT NULL
THEN 'PASS' ELSE 'FAIL' END
FROM day12.agent_runs
WHERE run_id = :'run_id';
SQL

    report "$id"
}

# ---------- C：连续 SIGKILL ----------
experiment_c() {
    local id a b
    id=$(new_id C)

    echo "Experiment C: $id"

    submit_run "$id"
    set_budget2 "$id"

    echo "Phase 1: Worker A"

    start_worker "$id" hold C-A
    a=$LAST_PID

    wait_state "$id" running 1 30
    is_worker "$a" || fail "Worker A 已退出"

    stop_worker "$a" KILL
    echo "Worker A SIGKILL completed"

    echo "Phase 2: Worker B"

    start_worker "$id" hold C-B
    b=$LAST_PID

    # 等待 A 的租约过期，B 自动接管
    wait_state "$id" running 2 45
    is_worker "$b" || fail "Worker B 已退出"

    stop_worker "$b" KILL
    echo "Worker B SIGKILL completed"

    echo "Phase 3: Worker C starts Sweep"

    start_worker "$id" off C-C

    wait_state "$id" failed 2 45

    # 观察是否出现违规的第三次领取
    sleep 8

    verify "$id" <<'SQL'
SELECT CASE WHEN
    status = 'failed'
    AND max_attempts = 2
    AND lease_epoch = 2
    AND failure_kind = 'retry_exhausted'
    AND finished_at IS NOT NULL
THEN 'PASS' ELSE 'FAIL' END
FROM day12.agent_runs
WHERE run_id = :'run_id';
SQL

    report "$id"
}

# ---------- D：可重试错误耗尽预算 ----------
experiment_d() {
    local id
    id=$(new_id D)

    echo "Experiment D: $id"

    submit_run "$id"
    set_budget2 "$id"

    start_worker "$id" transient D

    wait_state "$id" failed 2 60

    verify "$id" <<'SQL'
SELECT CASE WHEN
    status = 'failed'
    AND max_attempts = 2
    AND lease_epoch = 2
    AND retry_count = 1
    AND failure_kind = 'retry_exhausted'
    AND finished_at IS NOT NULL
THEN 'PASS' ELSE 'FAIL' END
FROM day12.agent_runs
WHERE run_id = :'run_id';
SQL

    report "$id"
}

case "${1:-}" in
    A) experiment_a ;;
    B) experiment_b ;;
    C) experiment_c ;;
    D) experiment_d ;;
    *)
        echo "Usage: $0 {A|B|C|D}"
        exit 1
        ;;
esac
