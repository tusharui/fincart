package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/tusharui/payment-gateway/backend/internal/db"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"github.com/tusharui/payment-gateway/backend/internal/repository"
	"gorm.io/gorm"
)

// expirySweepBatchSize bounds how many expired authorizations are loaded at a
// time so a large backlog does not hold every row in memory.
const expirySweepBatchSize = 500

// ExpiryService releases funds reserved by authorizations that were never
// captured or voided before their expiry time.
type ExpiryService struct {
	db *db.DB
}

// NewExpiryService creates a new ExpiryService.
func NewExpiryService(database *db.DB) *ExpiryService {
	return &ExpiryService{db: database}
}

// ExpireAuthorizations marks every active authorization hold that has passed
// its expiry time as EXPIRED and returns the reserved funds to the account's
// available balance.
//
// Work is processed in batches until no expired holds remain. A failure on one
// authorization is reported but does not stop the rest of the sweep.
func (s *ExpiryService) ExpireAuthorizations(ctx context.Context, now time.Time) (int, error) {
	var (
		expired int
		errs    []error
		cursor  uuid.UUID
	)

	repo := repository.NewTransactionRepository(s.db.GORM())
	for {
		batch, err := repo.FindExpiredAuthorizations(ctx, now, cursor, expirySweepBatchSize)
		if err != nil {
			errs = append(errs, err)
			break
		}
		if len(batch) == 0 {
			break
		}

		for _, auth := range batch {
			cursor = auth.ID

			didExpire, err := s.expireOne(ctx, auth.ID, now)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if didExpire {
				expired++
			}
		}

		if len(batch) < expirySweepBatchSize {
			break
		}
	}

	return expired, errors.Join(errs...)
}

// expireOne expires a single authorization inside its own transaction so the
// status update and balance release commit together.
func (s *ExpiryService) expireOne(ctx context.Context, authID uuid.UUID, now time.Time) (bool, error) {
	var expired bool

	err := s.db.GORM().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		transactionRepo := repository.NewTransactionRepository(tx)
		accountRepo := repository.NewAccountRepository(tx)

		didExpire, err := s.expireAuthorization(ctx, transactionRepo, accountRepo, authID, now)
		if err != nil {
			return err
		}

		expired = didExpire
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return false, fmt.Errorf("failed to expire authorization %s: %w", authID, err)
	}

	return expired, nil
}

// expireAuthorization contains the core expiry logic. It re-checks the
// authorization state under a row lock so a capture or void that commits first
// is never overwritten.
func (s *ExpiryService) expireAuthorization(
	ctx context.Context,
	transactionRepo repository.TransactionRepository,
	accountRepo repository.AccountRepository,
	authID uuid.UUID,
	now time.Time,
) (bool, error) {
	auth, err := transactionRepo.FindByIDForUpdate(ctx, authID)
	if err != nil {
		return false, fmt.Errorf("failed to load authorization: %w", err)
	}

	if auth.Type != models.TransactionTypeAuthHold || auth.Status != models.TransactionStatusActive {
		return false, nil
	}
	if auth.ExpiresAt == nil || auth.ExpiresAt.After(now) {
		return false, nil
	}

	if err := transactionRepo.UpdateStatus(ctx, authID, models.TransactionStatusExpired); err != nil {
		return false, fmt.Errorf("failed to mark authorization expired: %w", err)
	}
	if err := accountRepo.AdjustBalances(ctx, auth.AccountID, 0, auth.AmountCents); err != nil {
		return false, fmt.Errorf("failed to release authorization funds: %w", err)
	}

	return true, nil
}
