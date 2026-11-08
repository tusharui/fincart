package models

import "errors"

// Domain errors that can be returned by repositories
var (
	// ErrDuplicateTransaction indicates a transaction with the same reference_id and type already exists
	ErrDuplicateTransaction = errors.New("duplicate transaction")

	// ErrNotFound indicates the requested entity was not found
	ErrNotFound = errors.New("not found")

	// ErrInsufficientFunds indicates that a balance adjustment would violate a
	// money invariant (a negative balance or available funds above the settled
	// balance).
	ErrInsufficientFunds = errors.New("insufficient funds")
)
