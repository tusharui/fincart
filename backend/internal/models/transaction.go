package models

import (
	"time"

	"github.com/google/uuid"
)

// TransactionType represents the type of transaction
type TransactionType string

// Transaction type constants
const (
	TransactionTypeAuthHold TransactionType = "AUTH_HOLD" // Authorization hold (funds reserved)
	TransactionTypeCapture  TransactionType = "CAPTURE"   // Capture authorized funds
	TransactionTypeVoid     TransactionType = "VOID"      // Void/cancel authorization
	TransactionTypeRefund   TransactionType = "REFUND"    // Refund captured funds
)

// TransactionStatus represents the status of a transaction
type TransactionStatus string

// Transaction status constants
const (
	TransactionStatusActive    TransactionStatus = "ACTIVE"    // Transaction is active (auth holds)
	TransactionStatusCompleted TransactionStatus = "COMPLETED" // Transaction completed successfully (captured)
	TransactionStatusExpired   TransactionStatus = "EXPIRED"   // Transaction expired (auth timeout)
	TransactionStatusVoided    TransactionStatus = "VOIDED"    // Authorization was voided before capture
	TransactionStatusRefunded  TransactionStatus = "REFUNDED"  // Capture was fully refunded
)

// Transaction represents a ledger entry for account activity
type Transaction struct {
	ID          uuid.UUID         `db:"id" gorm:"column:id;primaryKey"`
	AccountID   uuid.UUID         `db:"account_id" gorm:"column:account_id"`
	OrderID     *uuid.UUID        `db:"order_id" gorm:"column:order_id"`
	CustomerID  *uuid.UUID        `db:"customer_id" gorm:"column:customer_id"`
	Type        TransactionType   `db:"type" gorm:"column:type"`
	AmountCents int64             `db:"amount_cents" gorm:"column:amount_cents"`
	Currency    string            `db:"currency" gorm:"column:currency"`
	ReferenceID *uuid.UUID        `db:"reference_id" gorm:"column:reference_id"`
	Status      TransactionStatus `db:"status" gorm:"column:status"`
	ExpiresAt   *time.Time        `db:"expires_at" gorm:"column:expires_at"`
	Metadata    map[string]any    `db:"metadata" gorm:"column:metadata;serializer:json"`
	CreatedAt   time.Time         `db:"created_at" gorm:"column:created_at"`
}

// IdempotencyStatusPending marks a key whose request is currently in flight.
// Completed entries carry the HTTP status code that was cached.
const IdempotencyStatusPending = 0

// IdempotencyKey tracks processed requests to prevent duplicate transactions
type IdempotencyKey struct {
	Key            string    `db:"key" gorm:"column:key"`
	RequestPath    string    `db:"request_path" gorm:"column:request_path"`
	RequestHash    string    `db:"request_hash" gorm:"column:request_hash"`
	ResponseBody   string    `db:"response_body" gorm:"column:response_body"`
	ResponseStatus int       `db:"response_status" gorm:"column:response_status"`
	CreatedAt      time.Time `db:"created_at" gorm:"column:created_at"`
}
