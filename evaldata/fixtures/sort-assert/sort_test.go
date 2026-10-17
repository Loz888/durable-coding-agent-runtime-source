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
