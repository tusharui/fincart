package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tusharui/payment-gateway/backend/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// IdempotencyRepository defines the interface for idempotency key data access
type IdempotencyRepository interface {
	Get(ctx context.Context, key, requestPath string) (*models.IdempotencyKey, error)
	Store(ctx context.Context, idemKey *models.IdempotencyKey) error
	DeleteOlderThan(ctx context.Context, before time.Time) (int64, error)
}

// idempotencyRepository implements IdempotencyRepository using GORM.
type idempotencyRepository struct {
	db *gorm.DB
}

// NewIdempotencyRepository creates a new IdempotencyRepository.
func NewIdempotencyRepository(db *gorm.DB) IdempotencyRepository {
	return &idempotencyRepository{db: db}
}

// Get retrieves a cached idempotency key and its response.
// A missing key returns (nil, nil), meaning this is a new request.
func (r *idempotencyRepository) Get(ctx context.Context, key, requestPath string) (*models.IdempotencyKey, error) {
	var idemKey models.IdempotencyKey
	err := r.db.WithContext(ctx).
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

// Store saves an idempotency key with its response.
// Duplicate (key, request_path) pairs are ignored, preserving the first response.
func (r *idempotencyRepository) Store(ctx context.Context, idemKey *models.IdempotencyKey) error {
	err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(idemKey).Error
	if err != nil {
		return fmt.Errorf("failed to store idempotency key: %w", err)
	}

	return nil
}

// DeleteOlderThan removes idempotency keys created before the specified time.
// This is used for cleanup of keys older than 24 hours.
func (r *idempotencyRepository) DeleteOlderThan(ctx context.Context, before time.Time) (int64, error) {
	result := r.db.WithContext(ctx).
		Where("created_at < ?", before).
		Delete(&models.IdempotencyKey{})
	if result.Error != nil {
		return 0, fmt.Errorf("failed to delete old idempotency keys: %w", result.Error)
	}

	return result.RowsAffected, nil
}
