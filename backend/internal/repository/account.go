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
	db         *gorm.DB
	merchantID uuid.UUID
}

// NewAccountRepository creates a new AccountRepository bound to a merchant.
// Every read and write is scoped to that tenant, so callers cannot accidentally
// address another merchant's account. Passing a transactional *gorm.DB lets the
// repository participate in an enclosing transaction without changing its API.
func NewAccountRepository(db *gorm.DB, merchantID uuid.UUID) AccountRepository {
	return &accountRepository{db: db, merchantID: merchantID}
}

// scoped returns a query filtered to the repository's merchant.
func (r *accountRepository) scoped(ctx context.Context) *gorm.DB {
	return r.db.WithContext(ctx).Where("merchant_id = ?", r.merchantID)
}

// FindByID retrieves an account owned by the bound merchant.
func (r *accountRepository) FindByID(ctx context.Context, id uuid.UUID) (*models.Account, error) {
	var account models.Account
	err := r.scoped(ctx).First(&account, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("account not found: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find account by id: %w", err)
	}

	return &account, nil
}

// FindByCardToken retrieves a merchant's account by its card token.
func (r *accountRepository) FindByCardToken(ctx context.Context, cardToken string) (*models.Account, error) {
	var account models.Account
	err := r.scoped(ctx).Where("card_token = ?", cardToken).First(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("account not found: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find account by token: %w", err)
	}

	return &account, nil
}

// FindByCardTokenForUpdate retrieves a merchant's account by its card token with
// a row-level lock. It must be called inside a transaction so the lock is held
// until commit/rollback.
func (r *accountRepository) FindByCardTokenForUpdate(ctx context.Context, cardToken string) (*models.Account, error) {
	var account models.Account
	err := r.scoped(ctx).
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
//
// The money invariants (non-negative balances, available never above settled)
// are enforced in the UPDATE itself so concurrent adjustments cannot interleave
// between a read and a write. When no row is updated we check whether the
// account exists so callers can distinguish a missing account from an
// adjustment that would break an invariant.
func (r *accountRepository) AdjustBalances(ctx context.Context, accountID uuid.UUID, balanceDelta, availableBalanceDelta int64) error {
	result := r.scoped(ctx).
		Model(&models.Account{}).
		Where("id = ?", accountID).
		Where("balance_cents + ? >= 0", balanceDelta).
		Where("available_balance_cents + ? >= 0", availableBalanceDelta).
		Where("available_balance_cents + ? <= balance_cents + ?", availableBalanceDelta, balanceDelta).
		Updates(map[string]any{
			"balance_cents":           gorm.Expr("balance_cents + ?", balanceDelta),
			"available_balance_cents": gorm.Expr("available_balance_cents + ?", availableBalanceDelta),
			"updated_at":              time.Now(),
		})
	if result.Error != nil {
		return fmt.Errorf("failed to adjust account balances: %w", result.Error)
	}
	if result.RowsAffected > 0 {
		return nil
	}

	exists, err := r.accountExists(ctx, accountID)
	if err != nil {
		return fmt.Errorf("failed to check account existence: %w", err)
	}
	if !exists {
		return fmt.Errorf("account not found")
	}

	return fmt.Errorf("balance adjustment would violate money invariants: %w", models.ErrInsufficientFunds)
}

// accountExists reports whether an account row is present. It is used only to
// classify a zero-row balance update.
func (r *accountRepository) accountExists(ctx context.Context, accountID uuid.UUID) (bool, error) {
	var count int64
	err := r.scoped(ctx).
		Model(&models.Account{}).
		Where("id = ?", accountID).
		Count(&count).Error
	if err != nil {
		return false, err
	}

	return count > 0, nil
}
