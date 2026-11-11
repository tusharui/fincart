package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"gorm.io/gorm"
)

// APIKeyRepository defines the interface for API key data access.
type APIKeyRepository interface {
	Create(ctx context.Context, key *models.APIKey) error
	FindByHash(ctx context.Context, keyHash string) (*models.APIKey, error)
	FindByID(ctx context.Context, merchantID, id uuid.UUID) (*models.APIKey, error)
	ListByMerchant(ctx context.Context, merchantID uuid.UUID) ([]models.APIKey, error)
	Revoke(ctx context.Context, merchantID, id uuid.UUID, revokedAt time.Time) (bool, error)
	TouchLastUsed(ctx context.Context, id uuid.UUID, usedAt, staleBefore time.Time) error
	EnsureBootstrap(ctx context.Context, key *models.APIKey) (*models.APIKey, error)
	CountActive(ctx context.Context, now time.Time) (int64, error)
}

// apiKeyRepository implements APIKeyRepository using GORM.
type apiKeyRepository struct {
	db *gorm.DB
}

// NewAPIKeyRepository creates a new APIKeyRepository.
func NewAPIKeyRepository(db *gorm.DB) APIKeyRepository {
	return &apiKeyRepository{db: db}
}

// Create inserts an API key. The raw credential is hashed by the caller and is
// never stored.
func (r *apiKeyRepository) Create(ctx context.Context, key *models.APIKey) error {
	if key.ID == uuid.Nil {
		key.ID = uuid.New()
	}

	if err := r.db.WithContext(ctx).Create(key).Error; err != nil {
		return fmt.Errorf("failed to create api key: %w", err)
	}

	return nil
}

// FindByHash looks up a key by its SHA-256 hash. A missing key returns
// (nil, nil) so an invalid credential is indistinguishable from an unknown one
// at the call site.
func (r *apiKeyRepository) FindByHash(ctx context.Context, keyHash string) (*models.APIKey, error) {
	var key models.APIKey
	err := r.db.WithContext(ctx).Where("key_hash = ?", keyHash).First(&key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find api key: %w", err)
	}

	return &key, nil
}

// FindByID retrieves a key owned by the given merchant. Scoping by merchant in
// the query prevents one tenant from observing another tenant's keys.
func (r *apiKeyRepository) FindByID(ctx context.Context, merchantID, id uuid.UUID) (*models.APIKey, error) {
	var key models.APIKey
	err := r.db.WithContext(ctx).
		Where("id = ? AND merchant_id = ?", id, merchantID).
		First(&key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find api key: %w", err)
	}

	return &key, nil
}

// ListByMerchant returns every key owned by a merchant, newest first.
func (r *apiKeyRepository) ListByMerchant(ctx context.Context, merchantID uuid.UUID) ([]models.APIKey, error) {
	var keys []models.APIKey
	err := r.db.WithContext(ctx).
		Where("merchant_id = ?", merchantID).
		Order("created_at DESC").
		Find(&keys).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list api keys: %w", err)
	}

	return keys, nil
}

// Revoke marks a key as revoked. It reports false when the key does not belong
// to the merchant or is already revoked.
func (r *apiKeyRepository) Revoke(ctx context.Context, merchantID, id uuid.UUID, revokedAt time.Time) (bool, error) {
	result := r.db.WithContext(ctx).
		Model(&models.APIKey{}).
		Where("id = ? AND merchant_id = ? AND revoked_at IS NULL", id, merchantID).
		Update("revoked_at", revokedAt)
	if result.Error != nil {
		return false, fmt.Errorf("failed to revoke api key: %w", result.Error)
	}

	return result.RowsAffected == 1, nil
}

// TouchLastUsed records key usage, but only when the stored timestamp is
// older than staleBefore. Throttling the write keeps authentication from
// turning every request into an UPDATE on the same row.
func (r *apiKeyRepository) TouchLastUsed(ctx context.Context, id uuid.UUID, usedAt, staleBefore time.Time) error {
	result := r.db.WithContext(ctx).
		Model(&models.APIKey{}).
		Where("id = ? AND (last_used_at IS NULL OR last_used_at < ?)", id, staleBefore).
		Update("last_used_at", usedAt)
	if result.Error != nil {
		return fmt.Errorf("failed to update api key last used time: %w", result.Error)
	}

	return nil
}

// EnsureBootstrap inserts or updates the single bootstrap key for a merchant.
// Rotating AUTH_BOOTSTRAP_KEY replaces the stored hash rather than creating a
// second credential, and clears any previous revocation.
func (r *apiKeyRepository) EnsureBootstrap(ctx context.Context, key *models.APIKey) (*models.APIKey, error) {
	if key.ID == uuid.Nil {
		key.ID = uuid.New()
	}
	key.Bootstrap = true

	var stored models.APIKey
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Where("merchant_id = ? AND bootstrap = ?", key.MerchantID, true).
			First(&stored).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if createErr := tx.Create(key).Error; createErr != nil {
				return createErr
			}
			stored = *key
			return nil
		}
		if err != nil {
			return err
		}

		updates := map[string]any{
			"key_hash":              key.KeyHash,
			"key_prefix":            key.KeyPrefix,
			"key_last4":             key.KeyLast4,
			"name":                  key.Name,
			"scopes":                key.Scopes,
			"rate_limit_per_minute": key.RateLimitPerMinute,
			"expires_at":            key.ExpiresAt,
			"revoked_at":            nil,
		}
		if updateErr := tx.Model(&stored).Updates(updates).Error; updateErr != nil {
			return updateErr
		}

		stored.KeyHash = key.KeyHash
		stored.KeyPrefix = key.KeyPrefix
		stored.KeyLast4 = key.KeyLast4
		stored.Name = key.Name
		stored.Scopes = key.Scopes
		stored.RateLimitPerMinute = key.RateLimitPerMinute
		stored.ExpiresAt = key.ExpiresAt
		stored.RevokedAt = nil

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to ensure bootstrap api key: %w", err)
	}

	return &stored, nil
}

// CountActive counts keys that can still authenticate at the given time.
func (r *apiKeyRepository) CountActive(ctx context.Context, now time.Time) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&models.APIKey{}).
		Where("revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)", now).
		Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("failed to count active api keys: %w", err)
	}

	return count, nil
}
