package domain

import "errors"

var (
	ErrNotFound           = errors.New("not found")
	ErrAlreadyExists      = errors.New("already exists")
	ErrConflict           = errors.New("conflict")
	ErrPermissionDenied   = errors.New("permission denied")
	ErrInvalidArgument    = errors.New("invalid argument")
	ErrFailedPrecondition = errors.New("failed precondition")
	ErrUnauthenticated    = errors.New("unauthenticated")
)
