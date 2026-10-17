package executor

import "errors"

var ErrTemporary = errors.New(
	"temporary executor failure",
)
