package repair19

import (
	"errors"
	"testing"
)

func TestDay19PlanAndReconcile(t *testing.T) {
	before := []byte(
		"func Divide(a,b int) int { return a * b }\n",
	)

	after := []byte(
		"func Divide(a,b int) int { return a / b }\n",
	)

	result, err := Plan(
		before,
		Hash(before),
		"return a * b",
		"return a / b",
	)

	if err != nil || result != Hash(after) {
		t.Fatalf(
			"after=%q err=%v",
			result,
			err,
		)
	}

	if Decide(
		Hash(before),
		Hash(before),
		result,
	) != ApplyNow {
		t.Fatal("expected apply")
	}

	if Decide(
		Hash(after),
		Hash(before),
		result,
	) != RecoverApplied {
		t.Fatal("expected recover")
	}

	if Decide(
		Hash([]byte("unexpected")),
		Hash(before),
		result,
	) != Conflict {
		t.Fatal("expected conflict")
	}
}

func TestDay19RejectStaleAndAmbiguous(t *testing.T) {
	src := []byte("x+x")

	if _, err := Plan(
		src,
		Hash([]byte("z")),
		"x",
		"y",
	); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}

	if _, err := Plan(
		src,
		Hash(src),
		"x",
		"y",
	); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}

	if _, err := Plan(
		src,
		Hash(src),
		"",
		"y",
	); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}
