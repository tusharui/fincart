// Package repository provides data access layer implementations for the bank API.
package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
	"github.com/tusharui/payment-gateway/backend/internal/db"
	"github.com/tusharui/payment-gateway/backend/internal/models"
)

// AccountRepository defines the interface for account data access
type AccountRepository interface {
	FindByID(ctx context.Context, id uuid.UUID) (*models.Account, error)
	FindByCardToken(ctx context.Context, cardToken string) (*models.Account, error)
	FindByCardTokenForUpdate(ctx context.Context, cardToken string) (*models.Account, error)
	AdjustBalances(ctx context.Context, accountID uuid.UUID, balanceDelta, availableBalanceDelta int64) error
}

// accountRepository implements AccountRepository
type accountRepository struct {
	exec db.Executor
}

// NewAccountRepository creates a new AccountRepository
// The exec parameter can be either *db.DB or *db.Tx, allowing the repository
// to work with or without transactions
func NewAccountRepository(exec db.Executor) AccountRepository {
	return &accountRepository{exec: exec}
}

// FindByID retrieves an account by its UUID
func (r *accountRepository) FindByID(ctx context.Context, id uuid.UUID) (*models.Account, error) {
	query := `
		SELECT id, card_token, last4, expiry_month, expiry_year,
		       balance_cents, available_balance_cents, created_at, updated_at
		FROM accounts
		WHERE id = $1
	`

	var account models.Account
	err := r.exec.QueryRowContext(ctx, query, id).Scan(
		&account.ID,
		&account.CardToken,
		&account.Last4,
		&account.ExpiryMonth,
		&account.ExpiryYear,
		&account.BalanceCents,
		&account.AvailableBalanceCents,
		&account.CreatedAt,
		&account.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("account not found: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find account by id: %w", err)
	}

	return &account, nil
}

// FindByCardToken retrieves an account by its card token
func (r *accountRepository) FindByCardToken(ctx context.Context, cardToken string) (*models.Account, error) {
	query := `
		SELECT id, card_token, last4, expiry_month, expiry_year,
		       balance_cents, available_balance_cents, created_at, updated_at
		FROM accounts
		WHERE card_token = $1
	`

	var account models.Account
	err := r.exec.QueryRowContext(ctx, query, cardToken).Scan(
		&account.ID,
		&account.CardToken,
		&account.Last4,
		&account.ExpiryMonth,
		&account.ExpiryYear,
		&account.BalanceCents,
		&account.AvailableBalanceCents,
		&account.CreatedAt,
		&account.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("account not found: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find account by token: %w", err)
	}

	return &account, nil
}

// FindByCardTokenForUpdate retrieves an account by its card token with row-level lock
func (r *accountRepository) FindByCardTokenForUpdate(ctx context.Context, cardToken string) (*models.Account, error) {
	query := `
		SELECT id, card_token, last4, expiry_month, expiry_year,
		       balance_cents, available_balance_cents, created_at, updated_at
		FROM accounts
		WHERE card_token = $1
		FOR UPDATE
	`

	var account models.Account
	err := r.exec.QueryRowContext(ctx, query, cardToken).Scan(
		&account.ID,
		&account.CardToken,
		&account.Last4,
		&account.ExpiryMonth,
		&account.ExpiryYear,
		&account.BalanceCents,
		&account.AvailableBalanceCents,
		&account.CreatedAt,
		&account.UpdatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("account not found: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find and lock account: %w", err)
	}

	return &account, nil
}

// AdjustBalances atomically adjusts the balance and available balance by the given deltas
func (r *accountRepository) AdjustBalances(ctx context.Context, accountID uuid.UUID, balanceDelta, availableBalanceDelta int64) error {
	query := `
		UPDATE accounts
		SET balance_cents = balance_cents + $2,
		    available_balance_cents = available_balance_cents + $3,
		    updated_at = NOW()
		WHERE id = $1
	`

	result, err := r.exec.ExecContext(ctx, query, accountID, balanceDelta, availableBalanceDelta)
	if err != nil {
		return fmt.Errorf("failed to adjust account balances: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("account not found")
	}

	return nil
}
