package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tusharui/payment-gateway/backend/internal/models"
)

func newTestEvent(eventType string, nextAttempt time.Time) *models.PaymentEvent {
	return &models.PaymentEvent{
		Type:          eventType,
		AggregateType: "payment",
		AggregateID:   uuid.New(),
		Payload:       `{"id":"test"}`,
		Status:        models.EventStatusPending,
		NextAttemptAt: nextAttempt,
	}
}

func TestEventRepository_ClaimBatchLeasesDueEvents(t *testing.T) {
	database := setupTestDB(t)
	defer cleanupTestDB(t, database)
	truncateTables(t, database)

	repo := NewEventRepository(database.GORM())
	ctx := context.Background()
	now := time.Now()

	due := newTestEvent("payment.authorized", now.Add(-time.Minute))
	future := newTestEvent("payment.captured", now.Add(time.Hour))
	require.NoError(t, repo.Create(ctx, due))
	require.NoError(t, repo.Create(ctx, future))

	batch, err := repo.ClaimBatch(ctx, now, 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, batch, 1)
	assert.Equal(t, due.ID, batch[0].ID)

	// The row is leased, so a second dispatcher must not claim it again.
	batch, err = repo.ClaimBatch(ctx, now.Add(30*time.Second), 10, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, batch)

	// Once the lease expires an abandoned event becomes claimable again.
	batch, err = repo.ClaimBatch(ctx, now.Add(2*time.Minute), 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, batch, 1)
	assert.Equal(t, due.ID, batch[0].ID)

	pending, err := repo.CountPending(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), pending)
}

func TestEventRepository_DeliveryStateMachine(t *testing.T) {
	database := setupTestDB(t)
	defer cleanupTestDB(t, database)
	truncateTables(t, database)

	repo := NewEventRepository(database.GORM())
	ctx := context.Background()
	now := time.Now()

	event := newTestEvent("payment.refunded", now)
	require.NoError(t, repo.Create(ctx, event))

	statusCode := 200
	require.NoError(t, repo.RecordDelivery(ctx, &models.WebhookDelivery{
		EventID:    event.ID,
		Endpoint:   "https://example.test/webhook",
		Attempt:    1,
		StatusCode: &statusCode,
	}))

	retryAt := now.Add(time.Minute)
	require.NoError(t, repo.MarkRetry(ctx, event.ID, 2, retryAt, "connection refused"))
	stored, err := repo.GetByID(ctx, event.ID)
	require.NoError(t, err)
	assert.Equal(t, models.EventStatusPending, stored.Status)
	assert.Equal(t, 2, stored.Attempts)
	require.NotNil(t, stored.LastError)
	assert.Equal(t, "connection refused", *stored.LastError)
	assert.WithinDuration(t, retryAt, stored.NextAttemptAt, time.Second)

	require.NoError(t, repo.MarkDead(ctx, event.ID, 8, "exhausted"))
	stored, err = repo.GetByID(ctx, event.ID)
	require.NoError(t, err)
	assert.Equal(t, models.EventStatusDead, stored.Status)
	assert.Equal(t, 8, stored.Attempts)

	require.NoError(t, repo.Requeue(ctx, event.ID, now))
	stored, err = repo.GetByID(ctx, event.ID)
	require.NoError(t, err)
	assert.Equal(t, models.EventStatusPending, stored.Status)
	assert.Equal(t, 0, stored.Attempts)
	assert.Nil(t, stored.LastError)

	require.NoError(t, repo.MarkDelivered(ctx, event.ID, now))
	stored, err = repo.GetByID(ctx, event.ID)
	require.NoError(t, err)
	assert.Equal(t, models.EventStatusDelivered, stored.Status)
	require.NotNil(t, stored.DeliveredAt)

	// Requeue only applies to dead-lettered events.
	assert.Error(t, repo.Requeue(ctx, event.ID, now))

	var deliveries int64
	require.NoError(t, database.GORM().Model(&models.WebhookDelivery{}).Count(&deliveries).Error)
	assert.Equal(t, int64(1), deliveries)
}
