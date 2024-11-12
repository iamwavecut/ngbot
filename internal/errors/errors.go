package errors

import (
	"errors"
)

// Common error types
var (
	ErrInvalidInput  = errors.New("invalid input")
	ErrDatabaseError = errors.New("database error")
	ErrNotFound      = errors.New("not found")
	ErrUnauthorized  = errors.New("unauthorized")
	ErrInternal      = errors.New("internal error")
)
