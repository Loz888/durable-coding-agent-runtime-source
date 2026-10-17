#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

FIXTURE="evaldata/fixtures/divide-assert"
LAB="evaldata/day17-lab"
SRC="$LAB/source"
BASE="$LAB/worktrees"

for file in go.mod math.go math_test.go; do
    test -f "$FIXTURE/$file" || {
        echo "Missing fixture: $FIXTURE/$file" >&2
        exit 1
    }
done

# Never overwrite a previous source or worktree.
if [ -e "$SRC" ]; then
    echo "Lab source already exists: $SRC" >&2
    echo "Keep it for review; use a new lab directory for another demo." >&2
    exit 1
fi

mkdir -p "$SRC" "$BASE"

cp \
    "$FIXTURE/go.mod" \
    "$FIXTURE/math.go" \
    "$FIXTURE/math_test.go" \
    "$SRC/"

gofmt -w \
    "$SRC/math.go" \
    "$SRC/math_test.go"

# Create a standalone Git source repository.
git -C "$SRC" init -q
git -C "$SRC" add .

git -C "$SRC" \
    -c user.name='Day17 Lab' \
    -c user.email='day17@example.invalid' \
    commit -qm 'baseline: failing fixture'

mkdir -p bin

go build \
    -o bin/day17-patch \
    ./cmd/day17-patch

SRC_ABS="$(cd "$SRC" && pwd)"
BASE_ABS="$(cd "$BASE" && pwd)"

RUN_ID="day17-divide-$(date +%s%N)"

WT="$BASE_ABS/$RUN_ID"

echo '==== 1. Prepare isolated worktree ===='

./bin/day17-patch \
    -action prepare \
    -source "$SRC_ABS" \
    -base "$BASE_ABS" \
    -run "$RUN_ID"

echo '==== 2. Confirm baseline go test FAIL ===='

./bin/day17-patch \
    -action verify \
    -worktree "$WT" \
    -timeout 30s

echo '==== 3. Create a SHA-checked patch ===='

SHA="$(
    sha256sum "$WT/math.go" |
        awk '{print $1}'
)"

PATCH_JSON="$LAB/patch-$RUN_ID.json"

printf \
    '{"path":"math.go","expected_sha256":"%s","old":"return a * b","new":"return a / b"}\n' \
    "$SHA" > "$PATCH_JSON"

./bin/day17-patch \
    -action apply \
    -worktree "$WT" \
    -patch "$PATCH_JSON"

echo '==== 4. Verify patched worktree ===='

./bin/day17-patch \
    -action verify \
    -worktree "$WT" \
    -timeout 30s

echo '==== 5. Inspect Git Diff ===='

./bin/day17-patch \
    -action diff \
    -worktree "$WT"

echo '==== 6. Confirm original source unchanged ===='

git -C "$SRC_ABS" \
    status --porcelain --untracked-files=all

grep -q \
    'return a \* b' \
    "$SRC_ABS/math.go"

echo "PASS: original untouched; patch applied only in $WT"

echo "RUN_ID=$RUN_ID"
echo "WORKTREE=$WT"
echo "PATCH_FILE=$PATCH_JSON"
