package repair19

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Typed error codes avoid a durable -> repair19 dependency during integration.
type protocolError struct{ message, code string }

func (e *protocolError) Error() string       { return e.message }
func (e *protocolError) FailureCode() string { return e.code }

var (
	ErrLeaseLost error = &protocolError{"lease owner, epoch or TTL invalid", "lease_lost"}
	ErrConflict  error = &protocolError{"patch state conflict", "patch_conflict"}
	ErrBudget    error = &protocolError{"durable repair budget exhausted", "repair_budget_exhausted"}
)

type Decision string

const (
	ApplyNow Decision = "apply"

	RecoverApplied Decision = "recover_applied"

	Conflict Decision = "conflict"
)

func Hash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Calculate the expected post-patch file Hash.
func Plan(
	source []byte,
	expected string,
	old string,
	replacement string,
) (string, error) {

	if expected == "" ||
		Hash(source) != expected {

		return "", fmt.Errorf(
			"%w: stale before SHA",
			ErrConflict,
		)
	}

	if old == "" ||
		old == replacement ||
		bytes.Count(source, []byte(old)) != 1 {

		return "", fmt.Errorf(
			"%w: old fragment must appear once",
			ErrConflict,
		)
	}

	if len(source) > 1024*1024 ||
		len(replacement) > 1024*1024 {

		return "", fmt.Errorf(
			"%w: file too large",
			ErrConflict,
		)
	}

	updated := bytes.Replace(
		source,
		[]byte(old),
		[]byte(replacement),
		1,
	)

	if len(updated) > 1024*1024 {
		return "", fmt.Errorf("%w: patched file too large", ErrConflict)
	}
	return Hash(updated), nil
}

func Decide(
	current string,
	before string,
	after string,
) Decision {

	switch current {

	case before:
		return ApplyNow

	case after:
		return RecoverApplied

	default:
		return Conflict
	}
}
