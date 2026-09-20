package domain

import "errors"

var (
	ErrValidation       = errors.New("validation error")
	ErrNotFound         = errors.New("not found")
	ErrCommentsDisabled = errors.New("comments disabled")
	ErrForbidden        = errors.New("forbidden")
	ErrUnauthenticated  = errors.New("unauthenticated")
)
