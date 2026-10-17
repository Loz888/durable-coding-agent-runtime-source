package example

import "testing"

func TestAddOne(t *testing.T) {
	if AddOne(1) != 2 {
		t.Fatal("bad result")
	}
}
