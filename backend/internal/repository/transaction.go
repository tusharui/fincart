package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/tusharui/payment-gateway/backend/internal/db"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// TransactionRepository defines the interface for transaction data access
type TransactionRepository interface {
	Create(ctx context.Context, tx *models.Transaction) error
	FindByID(ctx context.Context, id uuid.UUID) (*models.Transaction, error)
	FindByIDForUpdate(ctx context.Context, id uuid.UUID) (*models.Transaction, error)
	FindByReferenceID(ctx context.Context, refID uuid.UUID, txnType models.TransactionType) (*models.Transaction, error)
	FindExpiredAuthorizations(ctx context.Context, now time.Time, afterID uuid.UUID, limit int) ([]models.Transaction, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status models.TransactionStatus) error
}

// transactionRepository implements TransactionRepository using GORM.
type transactionRepository struct {
	db         *gorm.DB
	merchantID uuid.UUID
}

// NewTransactionRepository creates a new TransactionRepository bound to a
// merchant. Passing a transactional *gorm.DB lets the repository participate in
// an enclosing transaction without changing its API.
func NewTransactionRepository(db *gorm.DB, merchantID uuid.UUID) TransactionRepository {
	return &transactionRepository{db: db, merchantID: merchantID}
}

// scoped returns a query filtered to the repository's merchant.
func (r *transactionRepository) scoped(ctx context.Context) *gorm.DB {
	return r.db.WithContext(ctx).Where("merchant_id = ?", r.merchantID)
}

// Create inserts a new transaction into the database. Ownership is stamped from
// the repository binding so a caller cannot write a ledger entry for a
// different merchant.
func (r *transactionRepository) Create(ctx context.Context, tx *models.Transaction) error {
	if tx.ID == uuid.Nil {
		tx.ID = uuid.New()
	}
	tx.MerchantID = r.merchantID

	if err := r.db.WithContext(ctx).Create(tx).Error; err != nil {
		if db.IsUniqueViolation(err) || errors.Is(err, gorm.ErrDuplicatedKey) {
			return models.ErrDuplicateTransaction
		}
		return fmt.Errorf("failed to create transaction: %w", err)
	}

	return nil
}

// FindByID retrieves one of the merchant's transactions by its ID.
func (r *transactionRepository) FindByID(ctx context.Context, id uuid.UUID) (*models.Transaction, error) {
	var tx models.Transaction
	err := r.scoped(ctx).First(&tx, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("transaction not found: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find transaction: %w", err)
	}

	return &tx, nil
}

// FindByIDForUpdate retrieves one of the merchant's transactions by ID with a
// row-level lock. It must be called inside a transaction so the lock is held
// until commit/rollback.
func (r *transactionRepository) FindByIDForUpdate(ctx context.Context, id uuid.UUID) (*models.Transaction, error) {
	var tx models.Transaction
	err := r.scoped(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		First(&tx, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("transaction not found: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find transaction: %w", err)
	}

	return &tx, nil
}

// FindByReferenceID finds one of the merchant's transactions by its reference_id
// and type. This is used to check if a capture/void/refund already exists for an
// authorization/capture. A missing row returns (nil, nil).
func (r *transactionRepository) FindByReferenceID(ctx context.Context, refID uuid.UUID, txnType models.TransactionType) (*models.Transaction, error) {
	var tx models.Transaction
	err := r.scoped(ctx).
		Where("reference_id = ? AND type = ?", refID, txnType).
		First(&tx).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find transaction by reference: %w", err)
	}

	return &tx, nil
}

// FindExpiredAuthorizations returns active authorization holds whose expiry
// time has passed, in stable ID order. Pass a zero UUID for the first page;
// subsequent pages use the last ID from the previous page. The limit is capped
// to a sane default when non-positive.
//
// This is the entry point for the system-wide expiry sweep, so it intentionally
// ignores the repository's tenant binding and returns rows for every merchant.
// The sweep then performs each row's state change through a repository bound to
// that row's merchant.
func (r *transactionRepository) FindExpiredAuthorizations(ctx context.Context, now time.Time, afterID uuid.UUID, limit int) ([]models.Transaction, error) {
	if limit <= 0 {
		limit = 500
	}

	var txs []models.Transaction
	err := r.db.WithContext(ctx).
		Where("type = ? AND status = ? AND expires_at IS NOT NULL AND expires_at <= ? AND id > ?",
			models.TransactionTypeAuthHold, models.TransactionStatusActive, now, afterID).
		Order("id").
		Limit(limit).
		Find(&txs).Error
	if err != nil {
		return nil, fmt.Errorf("failed to find expired authorizations: %w", err)
	}

	return txs, nil
}

// UpdateStatus updates the status of one of the merchant's transactions.
func (r *transactionRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status models.TransactionStatus) error {
	result := r.scoped(ctx).
		Model(&models.Transaction{}).
		Where("id = ?", id).
		Update("status", status)
	if result.Error != nil {
		return fmt.Errorf("failed to update transaction status: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("transaction not found")
	}

	return nil
}
