package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// idempotencyReservationTTL is how long an in-flight claim may stay pending
// before another request is allowed to take it over. This prevents a crashed
// process from blocking an idempotency key until the cleanup job runs.
const idempotencyReservationTTL = 60 * time.Second

// IdempotencyRepository defines the interface for idempotency key data access
type IdempotencyRepository interface {
	Get(ctx context.Context, key, requestPath string) (*models.IdempotencyKey, error)
	Claim(ctx context.Context, key, requestPath, requestHash string) (bool, error)
	Complete(ctx context.Context, key, requestPath, requestHash string, responseStatus int, responseBody string) error
	Release(ctx context.Context, key, requestPath, requestHash string) error
	DeleteOlderThan(ctx context.Context, before time.Time) (int64, error)
}

// idempotencyRepository implements IdempotencyRepository using GORM.
type idempotencyRepository struct {
	db         *gorm.DB
	merchantID uuid.UUID
}

// NewIdempotencyRepository creates a new IdempotencyRepository bound to a
// merchant. Client-chosen keys are only unique inside a tenant, so every
// request-path lookup is scoped to the merchant that owns it.
func NewIdempotencyRepository(db *gorm.DB, merchantID uuid.UUID) IdempotencyRepository {
	return &idempotencyRepository{db: db, merchantID: merchantID}
}

// scoped returns a query filtered to the repository's merchant.
func (r *idempotencyRepository) scoped(ctx context.Context) *gorm.DB {
	return r.db.WithContext(ctx).Where("merchant_id = ?", r.merchantID)
}

// Get retrieves a cached idempotency key and its response.
// A missing key returns (nil, nil), meaning this is a new request.
func (r *idempotencyRepository) Get(ctx context.Context, key, requestPath string) (*models.IdempotencyKey, error) {
	var idemKey models.IdempotencyKey
	err := r.scoped(ctx).
		Where("key = ? AND request_path = ?", key, requestPath).
		First(&idemKey).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get idempotency key: %w", err)
	}

	return &idemKey, nil
}

// Claim atomically reserves an idempotency key before the request is executed.
// It returns true when this caller owns the request and must execute it, or
// false when another request already owns (or completed) the same key.
//
// A pending claim older than idempotencyReservationTTL is taken over so a
// crashed request cannot block the key forever.
func (r *idempotencyRepository) Claim(ctx context.Context, key, requestPath, requestHash string) (bool, error) {
	claim := &models.IdempotencyKey{
		MerchantID:     r.merchantID,
		Key:            key,
		RequestPath:    requestPath,
		RequestHash:    requestHash,
		ResponseStatus: models.IdempotencyStatusPending,
		ResponseBody:   "",
		CreatedAt:      time.Now(),
	}

	result := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(claim)
	if result.Error != nil {
		return false, fmt.Errorf("failed to claim idempotency key: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		return true, nil
	}

	cutoff := time.Now().Add(-idempotencyReservationTTL)
	takeover := r.scoped(ctx).
		Model(&models.IdempotencyKey{}).
		Where("key = ? AND request_path = ? AND request_hash = ? AND response_status = ? AND created_at < ?",
			key, requestPath, requestHash, models.IdempotencyStatusPending, cutoff).
		Update("created_at", time.Now())
	if takeover.Error != nil {
		return false, fmt.Errorf("failed to take over stale idempotency claim: %w", takeover.Error)
	}

	return takeover.RowsAffected == 1, nil
}

// Complete stores the response for a claim owned by requestHash.
func (r *idempotencyRepository) Complete(
	ctx context.Context,
	key, requestPath, requestHash string,
	responseStatus int,
	responseBody string,
) error {
	result := r.scoped(ctx).
		Model(&models.IdempotencyKey{}).
		Where("key = ? AND request_path = ? AND request_hash = ? AND response_status = ?",
			key, requestPath, requestHash, models.IdempotencyStatusPending).
		Updates(map[string]any{
			"response_status": responseStatus,
			"response_body":   responseBody,
		})
	if result.Error != nil {
		return fmt.Errorf("failed to complete idempotency key: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("idempotency claim is no longer held for key %q", key)
	}

	return nil
}

// Release removes a pending claim so the request can be retried.
// It only deletes the claim when it is still pending and owned by requestHash.
func (r *idempotencyRepository) Release(ctx context.Context, key, requestPath, requestHash string) error {
	result := r.scoped(ctx).
		Where("key = ? AND request_path = ? AND request_hash = ? AND response_status = ?",
			key, requestPath, requestHash, models.IdempotencyStatusPending).
		Delete(&models.IdempotencyKey{})
	if result.Error != nil {
		return fmt.Errorf("failed to release idempotency key: %w", result.Error)
	}

	return nil
}

// DeleteOlderThan removes idempotency keys created before the specified time.
// This is a maintenance operation that deliberately spans every merchant, so it
// is not constrained by the repository's tenant binding.
func (r *idempotencyRepository) DeleteOlderThan(ctx context.Context, before time.Time) (int64, error) {
	result := r.db.WithContext(ctx).
		Where("created_at < ?", before).
		Delete(&models.IdempotencyKey{})
	if result.Error != nil {
		return 0, fmt.Errorf("failed to delete old idempotency keys: %w", result.Error)
	}

	return result.RowsAffected, nil
}
