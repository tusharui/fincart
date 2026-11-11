package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/tusharui/payment-gateway/backend/internal/events"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"github.com/tusharui/payment-gateway/backend/internal/repository"
	"gorm.io/gorm"
)

// emitPaymentEvent appends a domain event to the transactional outbox using the
// caller's transaction. Because the event and the ledger mutation share one
// transaction, a committed state transition always has exactly one matching
// event, and a rollback leaves neither behind.
func emitPaymentEvent(
	ctx context.Context,
	tx *gorm.DB,
	eventType string,
	txn *models.Transaction,
) error {
	return emitPaymentEventAt(ctx, tx, eventType, txn, txn.CreatedAt)
}

// emitPaymentEventAt is emitPaymentEvent with an explicit occurrence time.
func emitPaymentEventAt(
	ctx context.Context,
	tx *gorm.DB,
	eventType string,
	txn *models.Transaction,
	occurredAt time.Time,
) error {
	event := events.ForTransactionAt(eventType, txn, occurredAt)

	payload, err := json.Marshal(event)
	if err != nil {
		return &ServiceError{
			Code:    ErrCodeInternalError,
			Message: fmt.Sprintf("failed to encode %s event: %v", eventType, err),
		}
	}

	repo := repository.NewEventRepository(tx)
	if err := repo.Create(ctx, &models.PaymentEvent{
		ID:            event.ID,
		MerchantID:    event.MerchantID,
		Type:          event.Type,
		AggregateType: "payment",
		AggregateID:   event.TransactionID,
		Payload:       string(payload),
		Status:        models.EventStatusPending,
		NextAttemptAt: event.OccurredAt,
		CreatedAt:     event.OccurredAt,
	}); err != nil {
		return &ServiceError{
			Code:    ErrCodeInternalError,
			Message: fmt.Sprintf("failed to record %s event: %v", eventType, err),
		}
	}

	return nil
}
