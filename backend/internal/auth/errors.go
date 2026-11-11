package auth

import "errors"

// Authentication failures. Callers intentionally collapse every failure into a
// generic unauthorized response so a client cannot distinguish an unknown key
// from a revoked or expired one.
var (
	ErrInvalidAPIKey = errors.New("invalid api key")
	ErrMissingScope  = errors.New("api key is missing the required scope")
)
