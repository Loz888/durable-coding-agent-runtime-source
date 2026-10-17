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
