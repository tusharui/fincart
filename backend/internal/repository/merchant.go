package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MerchantRepository defines the interface for merchant data access.
type MerchantRepository interface {
	Create(ctx context.Context, merchant *models.Merchant) error
	Ensure(ctx context.Context, merchant *models.Merchant) error
	FindByID(ctx context.Context, id uuid.UUID) (*models.Merchant, error)
}

// merchantRepository implements MerchantRepository using GORM.
type merchantRepository struct {
	db *gorm.DB
}

// NewMerchantRepository creates a new MerchantRepository.
func NewMerchantRepository(db *gorm.DB) MerchantRepository {
	return &merchantRepository{db: db}
}

// Create inserts a merchant.
func (r *merchantRepository) Create(ctx context.Context, merchant *models.Merchant) error {
	if merchant.ID == uuid.Nil {
		merchant.ID = uuid.New()
	}

	if err := r.db.WithContext(ctx).Create(merchant).Error; err != nil {
		return fmt.Errorf("failed to create merchant: %w", err)
	}

	return nil
}

// Ensure inserts a merchant if it does not already exist. It makes startup
// bootstrap idempotent across restarts and replicas.
func (r *merchantRepository) Ensure(ctx context.Context, merchant *models.Merchant) error {
	if merchant.ID == uuid.Nil {
		merchant.ID = uuid.New()
	}

	result := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(merchant)
	if result.Error != nil {
		return fmt.Errorf("failed to ensure merchant: %w", result.Error)
	}

	return nil
}

// FindByID retrieves a merchant by ID.
func (r *merchantRepository) FindByID(ctx context.Context, id uuid.UUID) (*models.Merchant, error) {
	var merchant models.Merchant
	err := r.db.WithContext(ctx).First(&merchant, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("merchant not found: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find merchant: %w", err)
	}

	return &merchant, nil
}
