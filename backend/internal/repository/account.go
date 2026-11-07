// Package repository provides data access layer implementations for the bank API.
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

// AccountRepository defines the interface for account data access
type AccountRepository interface {
	FindByID(ctx context.Context, id uuid.UUID) (*models.Account, error)
	FindByCardToken(ctx context.Context, cardToken string) (*models.Account, error)
	FindByCardTokenForUpdate(ctx context.Context, cardToken string) (*models.Account, error)
	AdjustBalances(ctx context.Context, accountID uuid.UUID, balanceDelta, availableBalanceDelta int64) error
}

// accountRepository implements AccountRepository using GORM.
type accountRepository struct {
	db *gorm.DB
}

// NewAccountRepository creates a new AccountRepository.
// Passing a transactional *gorm.DB lets the repository participate in an
// enclosing transaction without changing its API.
func NewAccountRepository(db *gorm.DB) AccountRepository {
	return &accountRepository{db: db}
}

// FindByID retrieves an account by its UUID.
func (r *accountRepository) FindByID(ctx context.Context, id uuid.UUID) (*models.Account, error) {
	var account models.Account
	err := r.db.WithContext(ctx).First(&account, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("account not found: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find account by id: %w", err)
	}

	return &account, nil
}

// FindByCardToken retrieves an account by its card token.
func (r *accountRepository) FindByCardToken(ctx context.Context, cardToken string) (*models.Account, error) {
	var account models.Account
	err := r.db.WithContext(ctx).Where("card_token = ?", cardToken).First(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("account not found: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find account by token: %w", err)
	}

	return &account, nil
}

// FindByCardTokenForUpdate retrieves an account by its card token with a row-level lock.
// It must be called inside a transaction so the lock is held until commit/rollback.
func (r *accountRepository) FindByCardTokenForUpdate(ctx context.Context, cardToken string) (*models.Account, error) {
	var account models.Account
	err := r.db.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("card_token = ?", cardToken).
		First(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("account not found: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find and lock account: %w", err)
	}

	return &account, nil
}

// AdjustBalances atomically adjusts the balance and available balance by the given deltas.
func (r *accountRepository) AdjustBalances(ctx context.Context, accountID uuid.UUID, balanceDelta, availableBalanceDelta int64) error {
	result := r.db.WithContext(ctx).
		Model(&models.Account{}).
		Where("id = ?", accountID).
		Updates(map[string]any{
			"balance_cents":           gorm.Expr("balance_cents + ?", balanceDelta),
			"available_balance_cents": gorm.Expr("available_balance_cents + ?", availableBalanceDelta),
			"updated_at":              time.Now(),
		})
	if result.Error != nil {
		return fmt.Errorf("failed to adjust account balances: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("account not found")
	}

	return nil
}
