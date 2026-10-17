#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

MODE="${1:-mock}"

if [[ "$MODE" != "mock" &&
      "$MODE" != "deepseek" ]]; then
    echo 'Usage: ./scripts/day18_demo.sh [mock|deepseek]' >&2
    exit 2
fi

if [[ "$MODE" == "deepseek" &&
      -z "${DEEPSEEK_API_KEY:-}" ]]; then
    echo 'DEEPSEEK_API_KEY missing (do not print the key)' >&2
    exit 2
fi

FIXTURE=evaldata/fixtures/divide-assert

# 每次运行独立目录，不覆盖上一次实验。
LAB="evaldata/day18-lab/$(date +%Y%m%d-%H%M%S)-$$"

SRC="$LAB/source"
BASE="$LAB/worktrees"

mkdir -p "$SRC" "$BASE"

cp \
    "$FIXTURE/go.mod" \
    "$FIXTURE/math.go" \
    "$FIXTURE/math_test.go" \
    "$SRC/"

gofmt -w \
    "$SRC/math.go" \
    "$SRC/math_test.go"

# 创建独立 Git 基线。
git -C "$SRC" init -q
git -C "$SRC" add .

git -C "$SRC" \
    -c user.name='Day18 Lab' \
    -c user.email='day18@example.invalid' \
    commit -qm 'failing baseline'

mkdir -p bin

go build \
    -o bin/day18-repair \
    ./cmd/day18-repair

SRC_ABS="$(cd "$SRC" && pwd)"
BASE_ABS="$(cd "$BASE" && pwd)"

RUN="day18-$MODE-$(date +%s%N)"

OUT="$LAB/result.json"

echo "MODE=$MODE RUN=$RUN"

./bin/day18-repair \
    -source "$SRC_ABS" \
    -base "$BASE_ABS" \
    -run "$RUN" \
    -mode "$MODE" \
    -file math.go \
    -max-rounds 2 \
    -test-timeout 30s \
    -out "$OUT"

echo '=== Original source remains unchanged ==='

grep -n \
    'return a \* b' \
    "$SRC/math.go"

echo "RESULT=$OUT"
echo "WORKTREE=$BASE_ABS/$RUN"
