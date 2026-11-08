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

// CaptureService handles payment capture operations
type CaptureService struct {
	db *db.DB
}

// NewCaptureService creates a new CaptureService
func NewCaptureService(database *db.DB) *CaptureService {
	return &CaptureService{
		db: database,
	}
}

// Capture captures an authorized payment
func (s *CaptureService) Capture(ctx context.Context, authorizationID uuid.UUID, amount int64) (*models.Transaction, error) {
	var captureTxn *models.Transaction
	err := s.db.GORM().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		transactionRepo := repository.NewTransactionRepository(tx)
		accountRepo := repository.NewAccountRepository(tx)

		result, err := s.performCapture(ctx, transactionRepo, accountRepo, authorizationID, amount)
		if err != nil {
			return err
		}

		captureTxn = result
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}

	return captureTxn, nil
}

// performCapture contains the core capture business logic
func (s *CaptureService) performCapture(
	ctx context.Context,
	transactionRepo repository.TransactionRepository,
	accountRepo repository.AccountRepository,
	authorizationID uuid.UUID,
	amount int64,
) (*models.Transaction, error) {
	authTxn, err := transactionRepo.FindByIDForUpdate(ctx, authorizationID)
	if err != nil || authTxn.Type != models.TransactionTypeAuthHold {
		return nil, &ServiceError{
			Code:    ErrCodeAuthNotFound,
			Message: "authorization not found",
		}
	}

	if authTxn.Status != models.TransactionStatusActive {
		return nil, authHoldStateError(authTxn.Status)
	}

	if authTxn.ExpiresAt != nil && time.Now().After(*authTxn.ExpiresAt) {
		return nil, &ServiceError{
			Code:    ErrCodeAuthExpired,
			Message: "authorization has expired",
		}
	}

	if amount != authTxn.AmountCents {
		return nil, &ServiceError{
			Code:    ErrCodeAmountMismatch,
			Message: "capture amount does not match authorized amount",
		}
	}

	captureID := uuid.New()
	capturedAt := time.Now()

	captureTxn := &models.Transaction{
		ID:          captureID,
		AccountID:   authTxn.AccountID,
		Type:        models.TransactionTypeCapture,
		AmountCents: amount,
		Currency:    authTxn.Currency,
		ReferenceID: &authorizationID,
		Status:      models.TransactionStatusCompleted,
		CreatedAt:   capturedAt,
	}

	if err := transactionRepo.Create(ctx, captureTxn); err != nil {
		if errors.Is(err, models.ErrDuplicateTransaction) {
			return nil, &ServiceError{
				Code:    ErrCodeAlreadyCaptured,
				Message: "authorization has already been captured",
			}
		}
		return nil, fmt.Errorf("failed to create capture: %w", err)
	}

	if err := transactionRepo.UpdateStatus(ctx, authorizationID, models.TransactionStatusCompleted); err != nil {
		return nil, &ServiceError{
			Code:    ErrCodeInternalError,
			Message: fmt.Sprintf("failed to update authorization: %v", err),
		}
	}

	if err := accountRepo.AdjustBalances(ctx, authTxn.AccountID, -amount, 0); err != nil {
		if errors.Is(err, models.ErrInsufficientFunds) {
			return nil, &ServiceError{
				Code:    ErrCodeInsufficientFunds,
				Message: "insufficient funds",
			}
		}
		return nil, &ServiceError{
			Code:    ErrCodeInternalError,
			Message: fmt.Sprintf("failed to adjust balance: %v", err),
		}
	}

	return captureTxn, nil
}

// GetCapture retrieves a capture by ID
func (s *CaptureService) GetCapture(ctx context.Context, captureID uuid.UUID) (*models.Transaction, error) {
	repo := repository.NewTransactionRepository(s.db.GORM())
	txn, err := repo.FindByID(ctx, captureID)
	if err != nil || txn.Type != models.TransactionTypeCapture {
		return nil, &ServiceError{
			Code:    ErrCodeCaptureNotFound,
			Message: "capture not found",
		}
	}

	return txn, nil
}
