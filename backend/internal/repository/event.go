package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// EventRepository defines data access for the transactional outbox and the
// webhook delivery log.
type EventRepository interface {
	Create(ctx context.Context, event *models.PaymentEvent) error
	ClaimBatch(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]models.PaymentEvent, error)
	MarkDelivered(ctx context.Context, id uuid.UUID, at time.Time) error
	MarkRetry(ctx context.Context, id uuid.UUID, attempts int, nextAttempt time.Time, lastError string) error
	MarkDead(ctx context.Context, id uuid.UUID, attempts int, lastError string) error
	RecordDelivery(ctx context.Context, delivery *models.WebhookDelivery) error
	GetByID(ctx context.Context, id uuid.UUID) (*models.PaymentEvent, error)
	CountPending(ctx context.Context) (int64, error)
	Requeue(ctx context.Context, id uuid.UUID, now time.Time) error
}

type eventRepository struct {
	db *gorm.DB
}

// NewEventRepository creates an EventRepository. Passing a transactional
// *gorm.DB lets events be written in the same transaction as ledger changes.
func NewEventRepository(db *gorm.DB) EventRepository {
	return &eventRepository{db: db}
}

// Create inserts an outbox event, applying defaults for an unprepared event.
func (r *eventRepository) Create(ctx context.Context, event *models.PaymentEvent) error {
	if event.ID == uuid.Nil {
		event.ID = uuid.New()
	}
	if event.Status == "" {
		event.Status = models.EventStatusPending
	}
	if event.NextAttemptAt.IsZero() {
		event.NextAttemptAt = time.Now()
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now()
	}
	if event.AggregateType == "" {
		event.AggregateType = "payment"
	}

	if err := r.db.WithContext(ctx).Create(event).Error; err != nil {
		return fmt.Errorf("failed to create payment event: %w", err)
	}

	return nil
}

// ClaimBatch atomically selects pending events that are due, leases them by
// pushing next_attempt_at forward, and returns them for delivery. The row lock
// with SKIP LOCKED lets multiple dispatchers run concurrently without
// delivering the same row at the same time. If a worker crashes, the lease
// expires and the event becomes claimable again (at-least-once delivery).
func (r *eventRepository) ClaimBatch(
	ctx context.Context,
	now time.Time,
	limit int,
	lease time.Duration,
) ([]models.PaymentEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	if lease <= 0 {
		lease = 30 * time.Second
	}

	var events []models.PaymentEvent
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.Locking{
			Strength: clause.LockingStrengthUpdate,
			Options:  clause.LockingOptionsSkipLocked,
		}).
			Where("status = ? AND next_attempt_at <= ?", models.EventStatusPending, now).
			Order("next_attempt_at ASC").
			Order("created_at ASC").
			Limit(limit).
			Find(&events).Error
		if err != nil {
			return fmt.Errorf("failed to claim payment events: %w", err)
		}
		if len(events) == 0 {
			return nil
		}

		ids := make([]uuid.UUID, len(events))
		for i := range events {
			ids[i] = events[i].ID
		}

		if err := tx.Model(&models.PaymentEvent{}).
			Where("id IN ?", ids).
			Update("next_attempt_at", now.Add(lease)).Error; err != nil {
			return fmt.Errorf("failed to lease payment events: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return events, nil
}

// MarkDelivered marks an event as successfully delivered to every endpoint.
func (r *eventRepository) MarkDelivered(ctx context.Context, id uuid.UUID, at time.Time) error {
	result := r.db.WithContext(ctx).
		Model(&models.PaymentEvent{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":       models.EventStatusDelivered,
			"delivered_at": at,
			"last_error":   nil,
		})
	if result.Error != nil {
		return fmt.Errorf("failed to mark event delivered: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("payment event not found")
	}

	return nil
}

// MarkRetry schedules another delivery attempt with the accumulated attempt
// count and the next attempt time computed by the dispatcher's backoff policy.
func (r *eventRepository) MarkRetry(
	ctx context.Context,
	id uuid.UUID,
	attempts int,
	nextAttempt time.Time,
	lastError string,
) error {
	result := r.db.WithContext(ctx).
		Model(&models.PaymentEvent{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"attempts":        attempts,
			"next_attempt_at": nextAttempt,
			"last_error":      lastError,
		})
	if result.Error != nil {
		return fmt.Errorf("failed to schedule event retry: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("payment event not found")
	}

	return nil
}

// MarkDead moves an event to the dead-letter state after the retry budget is
// exhausted. Dead events are retained for inspection and explicit replay.
func (r *eventRepository) MarkDead(ctx context.Context, id uuid.UUID, attempts int, lastError string) error {
	result := r.db.WithContext(ctx).
		Model(&models.PaymentEvent{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":     models.EventStatusDead,
			"attempts":   attempts,
			"last_error": lastError,
		})
	if result.Error != nil {
		return fmt.Errorf("failed to dead-letter event: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("payment event not found")
	}

	return nil
}

// RecordDelivery appends one delivery attempt to the audit log.
func (r *eventRepository) RecordDelivery(ctx context.Context, delivery *models.WebhookDelivery) error {
	if delivery.ID == uuid.Nil {
		delivery.ID = uuid.New()
	}
	if delivery.CreatedAt.IsZero() {
		delivery.CreatedAt = time.Now()
	}

	if err := r.db.WithContext(ctx).Create(delivery).Error; err != nil {
		return fmt.Errorf("failed to record webhook delivery: %w", err)
	}

	return nil
}

// GetByID retrieves an outbox event by ID.
func (r *eventRepository) GetByID(ctx context.Context, id uuid.UUID) (*models.PaymentEvent, error) {
	var event models.PaymentEvent
	err := r.db.WithContext(ctx).First(&event, "id = ?", id).Error
	if err != nil {
		return nil, fmt.Errorf("failed to find payment event: %w", err)
	}

	return &event, nil
}

// CountPending returns the number of undelivered events, used by readiness
// checks and backlog metrics.
func (r *eventRepository) CountPending(ctx context.Context) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&models.PaymentEvent{}).
		Where("status = ?", models.EventStatusPending).
		Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("failed to count pending events: %w", err)
	}

	return count, nil
}

// Requeue returns a dead-lettered event to the pending queue so the dispatcher
// can attempt delivery again.
func (r *eventRepository) Requeue(ctx context.Context, id uuid.UUID, now time.Time) error {
	result := r.db.WithContext(ctx).
		Model(&models.PaymentEvent{}).
		Where("id = ? AND status = ?", id, models.EventStatusDead).
		Updates(map[string]any{
			"status":          models.EventStatusPending,
			"attempts":        0,
			"next_attempt_at": now,
			"last_error":      nil,
			"delivered_at":    nil,
		})
	if result.Error != nil {
		return fmt.Errorf("failed to requeue event: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("dead payment event not found")
	}

	return nil
}
