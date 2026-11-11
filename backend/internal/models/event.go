package models

import (
	"time"

	"github.com/google/uuid"
)

// Outbox event status values.
const (
	EventStatusPending   = "PENDING"
	EventStatusDelivered = "DELIVERED"
	EventStatusDead      = "DEAD"
)

// PaymentEvent is a durable domain event stored in the transactional outbox.
// It is written in the same transaction as the ledger mutation it describes.
type PaymentEvent struct {
	ID            uuid.UUID  `db:"id" gorm:"column:id;primaryKey"`
	Type          string     `db:"type" gorm:"column:type;not null"`
	AggregateType string     `db:"aggregate_type" gorm:"column:aggregate_type;not null"`
	AggregateID   uuid.UUID  `db:"aggregate_id" gorm:"column:aggregate_id;not null"`
	Payload       string     `db:"payload" gorm:"column:payload;type:jsonb;not null"`
	Status        string     `db:"status" gorm:"column:status;not null"`
	Attempts      int        `db:"attempts" gorm:"column:attempts;not null"`
	NextAttemptAt time.Time  `db:"next_attempt_at" gorm:"column:next_attempt_at;not null"`
	LastError     *string    `db:"last_error" gorm:"column:last_error"`
	CreatedAt     time.Time  `db:"created_at" gorm:"column:created_at;not null"`
	DeliveredAt   *time.Time `db:"delivered_at" gorm:"column:delivered_at"`
}

// TableName returns the outbox table name.
func (PaymentEvent) TableName() string {
	return "payment_events"
}

// WebhookDelivery records a single delivery attempt for an event endpoint.
type WebhookDelivery struct {
	ID         uuid.UUID `db:"id" gorm:"column:id;primaryKey"`
	EventID    uuid.UUID `db:"event_id" gorm:"column:event_id;not null"`
	Endpoint   string    `db:"endpoint" gorm:"column:endpoint;not null"`
	Attempt    int       `db:"attempt" gorm:"column:attempt;not null"`
	StatusCode *int      `db:"status_code" gorm:"column:status_code"`
	Error      *string   `db:"error" gorm:"column:error"`
	DurationMS int64     `db:"duration_ms" gorm:"column:duration_ms;not null"`
	CreatedAt  time.Time `db:"created_at" gorm:"column:created_at;not null"`
}

// TableName returns the delivery log table name.
func (WebhookDelivery) TableName() string {
	return "webhook_deliveries"
}
