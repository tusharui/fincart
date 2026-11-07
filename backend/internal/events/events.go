// Package events defines the payment domain events emitted by the gateway.
package events

import (
	"time"

	"github.com/google/uuid"
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

// Event represents a payment lifecycle domain event.
type Event struct {
	ID         uuid.UUID `json:"id"`
	Type       string    `json:"type"`
	PaymentID  uuid.UUID `json:"payment_id"`
	OccurredAt time.Time `json:"occurred_at"`
}
