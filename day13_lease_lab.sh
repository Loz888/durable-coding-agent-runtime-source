#!/usr/bin/env bash
set -Eeuo pipefail

BASE="$HOME/go-learning/agent-runtime-project/day13/runtime-app"
BIN="/tmp/day13-leased-agent"
: "${DAY13_REPO:?Set DAY13_REPO to a trusted local Git repository}"
REPO="$DAY13_REPO"

# 优先使用 DAY13_DATABASE_URL，否则复用 Day12 数据库
DB_URL="${DAY13_DATABASE_URL:-${DAY12_DATABASE_URL:-}}"

MODE="${1:-abc}"

TAG="$(date +%Y%m%d-%H%M%S)-$$"
DIR="$BASE/lease-lab-logs/$TAG"

mkdir -p "$DIR"
cd "$BASE"

[[ -x "$BIN" ]] || {
    echo "ERROR: Agent binary not found: $BIN"
    exit 1
}

[[ -n "$DB_URL" ]] || {
    echo "ERROR: DAY12_DATABASE_URL / DAY13_DATABASE_URL not set"
    exit 1
}

command -v psql >/dev/null || exit 1

psqlq() {
    psql -X -v ON_ERROR_STOP=1 "$DB_URL" "$@"
}

# 确认数据库 Schema
schema_exists="$(
    psqlq -At -c \
    "SELECT to_regclass('day12.agent_runs') IS NOT NULL;"
)"

[[ "$schema_exists" == "t" ]] || {
    echo "ERROR: day12.agent_runs not found"
    exit 1
}

APID=""
CPID=""

cleanup() {
    for pid in "$APID" "$CPID"; do
        [[ -z "$pid" ]] && continue

        if kill -0 "$pid" 2>/dev/null; then
            kill -CONT "$pid" 2>/dev/null || true
            kill -KILL "$pid" 2>/dev/null || true
            wait "$pid" 2>/dev/null || true
        fi
    done
}

trap cleanup EXIT

# 等待日志出现指定内容
wait_log() {
    local pid="$1"
    local file="$2"
    local pattern="$3"
    local loops="${4:-100}"

    for ((i=0; i<loops; i++)); do
        if grep -Eq "$pattern" "$file"; then
            return 0
        fi

        if ! kill -0 "$pid" 2>/dev/null; then
            echo "Worker exited unexpectedly:"
            cat "$file"
            exit 1
        fi

        sleep 0.2
    done

    echo "TIMEOUT waiting for: $pattern"
    cat "$file"
    exit 1
}

# 等待 PostgreSQL 确认 Lease 过期
wait_expired() {
    local rid="$1"

    for ((i=0; i<100; i++)); do
        local expired

        expired="$(
            psqlq -At -c "
                SELECT
                    lease_until IS NULL
                    OR lease_until <= clock_timestamp()
                FROM day12.agent_runs
                WHERE run_id = '$rid';
            "
        )"

        if [[ "$expired" == "t" ]]; then
            echo "LEASE EXPIRED: $rid"
            return 0
        fi

        sleep 0.2
    done

    echo "ERROR: lease did not expire"
    exit 1
}

# 保存数据库证据
snapshot() {
    local rid="$1"
    local label="$2"

    {
        echo "===== $label ====="

        psqlq -P pager=off -c "
            SELECT
                run_id,
                status,
                revision,
                lease_owner,
                lease_epoch,
                lease_until,
                clock_timestamp() AS db_now
            FROM day12.agent_runs
            WHERE run_id = '$rid';
        "

        echo "===== CHECKPOINTS ====="

        psqlq -P pager=off -c "
            SELECT revision
            FROM day12.checkpoints
            WHERE run_id = '$rid'
            ORDER BY revision;
        "
    } | tee "$DIR/$label.txt"
}

# 验证最终状态
assert_final() {
    local rid="$1"
    local result

    result="$(
        psqlq -At -c "
            SELECT status || '|' || lease_epoch
            FROM day12.agent_runs
            WHERE run_id = '$rid';
        "
    )"

    if [[ "$result" != "succeeded|2" ]]; then
        echo "ERROR: unexpected final state: $result"
        exit 1
    fi

    echo "PASS: status=succeeded, epoch=2"
}

# 实验 A + B，也用于实验 D
run_ab() {
    local mode="$1"
    local rid="$2"
    local goal="$3"
    local label="$4"

    local args=(
        -id "$rid"
        -repo "$REPO"
        -mode "$mode"
        -goal "$goal"
        -ttl 6s
        -renew 1s
    )

    echo "======================================"
    echo "$label"
    echo "RunID=$rid"
    echo "======================================"

    # Worker A 获取租约并保持 Renew
    "$BIN" \
        -worker worker-a \
        "${args[@]}" \
        -pause-after 1 \
        -pause 120s \
        > "$DIR/${label}_A.log" 2>&1 &

    APID=$!

    local limit=100
    [[ "$mode" == "deepseek" ]] && limit=900

    wait_log "$APID" \
        "$DIR/${label}_A.log" \
        'CLAIMED.*epoch=1' "$limit"

    wait_log "$APID" \
        "$DIR/${label}_A.log" \
        'RENEWED.*epoch=1' 150

    echo "Worker A owns Lease, PID=$APID"

    snapshot "$rid" "${label}_before_busy"

    # 实验 A：Worker B 抢占尚未过期的 Lease
    echo "Testing Worker B Claim while A is renewing"

    set +e
    "$BIN" \
        -worker worker-b \
        "${args[@]}" \
        > "$DIR/${label}_B_busy.log" 2>&1
    busy_rc=$?
    set -e

    if ! grep -Eiq \
        'ErrLeaseBusy|lease.*(held|busy)|another session' \
        "$DIR/${label}_B_busy.log"; then

        echo "ERROR: expected Lease Busy rejection"
        cat "$DIR/${label}_B_busy.log"
        exit 1
    fi

    echo "PASS A: Worker B rejected (exit=$busy_rc)"
    snapshot "$rid" "${label}_after_busy"

    # 实验 B：强制杀死 A
    echo "Sending SIGKILL to Worker A: $APID"

    kill -KILL "$APID"
    wait "$APID" 2>/dev/null || true
    APID=""

    # 使用 PostgreSQL 时钟等待真正到期
    wait_expired "$rid"

    snapshot "$rid" "${label}_expired"

    # Worker B 使用相同 RunID 接管
    echo "Worker B reclaiming Run"

    "$BIN" \
        -worker worker-b \
        "${args[@]}" \
        > "$DIR/${label}_B_reclaim.log" 2>&1

    snapshot "$rid" "${label}_final"

    assert_final "$rid"

    echo "PASS B: Crash recovery completed"
}

# 实验 C：SIGSTOP / SIGCONT
run_c() {
    local rid="$1"
    local goal="检查 Go 仓库的测试结果"

    local args=(
        -id "$rid"
        -repo "$REPO"
        -mode demo
        -goal "$goal"
        -ttl 6s
        -renew 1s
    )

    echo "======================================"
    echo "EXPERIMENT C: FENCING"
    echo "RunID=$rid"
    echo "======================================"

    "$BIN" \
        -worker worker-a \
        "${args[@]}" \
        -pause-after 1 \
        -pause 12s \
        > "$DIR/C_A.log" 2>&1 &

    CPID=$!

    wait_log "$CPID" "$DIR/C_A.log" \
        'CLAIMED.*epoch=1'

    wait_log "$CPID" "$DIR/C_A.log" \
        'RENEWED.*epoch=1'

    snapshot "$rid" "C_before_stop"

    echo "SIGSTOP Worker A PID=$CPID"
    kill -STOP "$CPID"

    # A 完全暂停，不再能够 Renew
    wait_expired "$rid"

    # B 接管、完成
    echo "Worker B taking over"

    "$BIN" \
        -worker worker-b \
        "${args[@]}" \
        > "$DIR/C_B.log" 2>&1

    snapshot "$rid" "C_after_B"

    assert_final "$rid"

    # 恢复旧 Worker A
    echo "SIGCONT Worker A"
    kill -CONT "$CPID"

    # 等待 A 处理 Lease 丢失或终态
    local stopped=1

    for ((i=0; i<160; i++)); do
        if ! kill -0 "$CPID" 2>/dev/null; then
            stopped=0
            break
        fi
        sleep 0.25
    done

    if [[ "$stopped" == 1 ]]; then
        echo "ERROR: old Worker A did not exit"
        exit 1
    fi

    wait "$CPID" 2>/dev/null || true
    CPID=""

    snapshot "$rid" "C_after_A_resume"

    assert_final "$rid"

    echo "PASS C: old Worker did not overwrite B"
}

if [[ "$MODE" == "abc" ]]; then

    AB_ID="day13-ab-$TAG"
    C_ID="day13-c-$TAG"

    run_ab \
        demo \
        "$AB_ID" \
        "检查 Go 仓库的测试结果" \
        "AB"

    run_c "$C_ID"

elif [[ "$MODE" == "d" ]]; then

    [[ -n "${DEEPSEEK_API_KEY:-}" ]] || {
        echo "ERROR: DEEPSEEK_API_KEY not set"
        exit 1
    }

    D_ID="day13-deepseek-$TAG"

    run_ab \
        deepseek \
        "$D_ID" \
        "诊断这个Go仓库，先运行测试，再读取源码并分析失败原因" \
        "D"

else
    echo "Usage:"
    echo "  $0 abc   # Experiments A B C"
    echo "  $0 d     # DeepSeek experiment D"
    exit 1
fi

echo
echo "ALL REQUESTED EXPERIMENTS IN THIS RUN FINISHED"
echo "Evidence directory: $DIR"
