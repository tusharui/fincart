// Package repository provides data access layer implementations for the bank API.
package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/tusharui/payment-gateway/backend/internal/db"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"github.com/google/uuid"
)

// AccountRepository defines the interface for account data access
type AccountRepository interface {
	FindByID(ctx context.Context, id uuid.UUID) (*models.Account, error)
	FindByAccountNumber(ctx context.Context, accountNumber string) (*models.Account, error)
	FindByAccountNumberForUpdate(ctx context.Context, accountNumber string) (*models.Account, error)
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
		SELECT id, account_number, cvv, expiry_month, expiry_year,
		       balance_cents, available_balance_cents, created_at, updated_at
		FROM accounts
		WHERE id = $1
	`

	var account models.Account
	err := r.exec.QueryRowContext(ctx, query, id).Scan(
		&account.ID,
		&account.AccountNumber,
		&account.CVV,
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

// FindByAccountNumber retrieves an account by its account number (card number)
func (r *accountRepository) FindByAccountNumber(ctx context.Context, accountNumber string) (*models.Account, error) {
	query := `
		SELECT id, account_number, cvv, expiry_month, expiry_year,
		       balance_cents, available_balance_cents, created_at, updated_at
		FROM accounts
		WHERE account_number = $1
	`

	var account models.Account
	err := r.exec.QueryRowContext(ctx, query, accountNumber).Scan(
		&account.ID,
		&account.AccountNumber,
		&account.CVV,
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
		return nil, fmt.Errorf("failed to find account by account number: %w", err)
	}

	return &account, nil
}

// FindByAccountNumberForUpdate retrieves an account by its account number with row-level lock
func (r *accountRepository) FindByAccountNumberForUpdate(ctx context.Context, accountNumber string) (*models.Account, error) {
	query := `
		SELECT id, account_number, cvv, expiry_month, expiry_year,
		       balance_cents, available_balance_cents, created_at, updated_at
		FROM accounts
		WHERE account_number = $1
		FOR UPDATE
	`

	var account models.Account
	err := r.exec.QueryRowContext(ctx, query, accountNumber).Scan(
		&account.ID,
		&account.AccountNumber,
		&account.CVV,
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
