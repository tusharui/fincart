// Package events defines the payment domain events emitted by the gateway.
//
// Events are persisted in the transactional outbox and delivered to webhook
// endpoints by the webhook package. The JSON shape published here is the
// integration contract, so fields are additive-only within a schema version.
package events

import (
	"time"

	"github.com/google/uuid"
	"github.com/tusharui/payment-gateway/backend/internal/models"
)

// Domain event types emitted during the payment lifecycle.
const (
	EventPaymentAuthorized = "payment.authorized"
	EventPaymentCaptured   = "payment.captured"
	EventPaymentVoided     = "payment.voided"
	EventPaymentRefunded   = "payment.refunded"
	EventPaymentFailed     = "payment.failed"
	EventPaymentExpired    = "payment.expired"
)

// SchemaVersion identifies the webhook payload contract.
const SchemaVersion = 1

// Event is the envelope delivered to webhook endpoints.
type Event struct {
	ID            uuid.UUID   `json:"id"`
	MerchantID    uuid.UUID   `json:"merchant_id"`
	Type          string      `json:"type"`
	SchemaVersion int         `json:"schema_version"`
	TransactionID uuid.UUID   `json:"transaction_id"`
	OccurredAt    time.Time   `json:"occurred_at"`
	Data          PaymentData `json:"data"`
}

// PaymentData carries the payment state at the time the event occurred.
type PaymentData struct {
	AuthorizationID *uuid.UUID `json:"authorization_id,omitempty"`
	CaptureID       *uuid.UUID `json:"capture_id,omitempty"`
	VoidID          *uuid.UUID `json:"void_id,omitempty"`
	RefundID        *uuid.UUID `json:"refund_id,omitempty"`
	AccountID       uuid.UUID  `json:"account_id"`
	AmountCents     int64      `json:"amount_cents"`
	Currency        string     `json:"currency"`
	Status          string     `json:"status"`
	OrderID         *uuid.UUID `json:"order_id,omitempty"`
	CustomerID      *uuid.UUID `json:"customer_id,omitempty"`
}

// ForTransaction builds an event envelope for a committed ledger entry. The
// reference fields are derived from the entry type so consumers can correlate
// capture/void/refund events back to their authorization or capture.
func ForTransaction(eventType string, txn *models.Transaction) Event {
	occurredAt := txn.CreatedAt
	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}

	return ForTransactionAt(eventType, txn, occurredAt)
}

// ForTransactionAt is ForTransaction with an explicit occurrence time. Expiry
// sweeps use it because the hold was created earlier than the transition.
func ForTransactionAt(eventType string, txn *models.Transaction, occurredAt time.Time) Event {
	data := PaymentData{
		AccountID:   txn.AccountID,
		AmountCents: txn.AmountCents,
		Currency:    txn.Currency,
		Status:      string(txn.Status),
		OrderID:     txn.OrderID,
		CustomerID:  txn.CustomerID,
	}

	switch txn.Type {
	case models.TransactionTypeAuthHold:
		id := txn.ID
		data.AuthorizationID = &id
	case models.TransactionTypeCapture:
		id := txn.ID
		data.CaptureID = &id
		data.AuthorizationID = txn.ReferenceID
	case models.TransactionTypeVoid:
		id := txn.ID
		data.VoidID = &id
		data.AuthorizationID = txn.ReferenceID
	case models.TransactionTypeRefund:
		id := txn.ID
		data.RefundID = &id
		data.CaptureID = txn.ReferenceID
	}

	return Event{
		ID:            uuid.New(),
		MerchantID:    txn.MerchantID,
		Type:          eventType,
		SchemaVersion: SchemaVersion,
		TransactionID: txn.ID,
		OccurredAt:    occurredAt.UTC(),
		Data:          data,
	}
}
