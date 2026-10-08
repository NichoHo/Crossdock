// Package apperr holds sentinel errors the HTTP layer maps to status codes.
package apperr

import (
	"errors"
	"fmt"
)

// ErrInvalid marks a request that is well-formed but fails validation (422).
var ErrInvalid = errors.New("invalid request")

// Invalid returns an ErrInvalid with a message.
func Invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}
