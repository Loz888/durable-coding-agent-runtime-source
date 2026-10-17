#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

B=evaldata/fixtures

mkdir -p \
    "$B/divide-assert" \
    "$B/compile-error" \
    "$B/nil-panic" \
    "$B/sort-assert" \
    "$B/clean-pass"

for d in "$B"/*; do
    name=$(basename "$d")

    printf \
      'module example.com/day16-%s\n\ngo 1.23\n' \
      "$name" > "$d/go.mod"
done

# Case A: Division bug
cat > "$B/divide-assert/math.go" <<'GO'
package example

func Divide(a, b int) int {
    return a * b
}
GO

cat > "$B/divide-assert/math_test.go" <<'GO'
package example

import "testing"

func TestDivide(t *testing.T) {
    if got := Divide(12, 3); got != 4 {
        t.Fatalf(
            "Divide(12,3)=%d, want 4",
            got,
        )
    }
}
GO

# Case B: Compilation failure
cat > "$B/compile-error/calc.go" <<'GO'
package example

func AddOne(x int) int {
    return x + missingNumber
}
GO

cat > "$B/compile-error/calc_test.go" <<'GO'
package example

import "testing"

func TestAddOne(t *testing.T) {
    if AddOne(1) != 2 {
        t.Fatal("bad result")
    }
}
GO

# Case C: Nil pointer panic
cat > "$B/nil-panic/person.go" <<'GO'
package example

type Person struct {
    Name string
}

func NameLen(p *Person) int {
    return len(p.Name)
}
GO

cat > "$B/nil-panic/person_test.go" <<'GO'
package example

import "testing"

func TestNameLen(t *testing.T) {
    NameLen(nil)
}
GO

# Case D: Wrong sorting direction
cat > "$B/sort-assert/sort.go" <<'GO'
package example

import "sort"

func SortAsc(x []int) {
    sort.Sort(
        sort.Reverse(sort.IntSlice(x)),
    )
}
GO

cat > "$B/sort-assert/sort_test.go" <<'GO'
package example

import (
    "reflect"
    "testing"
)

func TestSortAsc(t *testing.T) {
    x := []int{3, 1, 2}
    SortAsc(x)

    if !reflect.DeepEqual(
        x,
        []int{1, 2, 3},
    ) {
        t.Fatalf(
            "got=%v, want [1 2 3]",
            x,
        )
    }
}
GO

# Case E: Clean repository
cat > "$B/clean-pass/sum.go" <<'GO'
package example

func Sum(a, b int) int {
    return a + b
}
GO

cat > "$B/clean-pass/sum_test.go" <<'GO'
package example

import "testing"

func TestSum(t *testing.T) {
    if Sum(2, 3) != 5 {
        t.Fatal("sum should be 5")
    }
}
GO

echo "Generated five Go Repository fixtures."
