package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/tusharui/payment-gateway/backend/internal/db"
	"github.com/tusharui/payment-gateway/backend/internal/events"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"github.com/tusharui/payment-gateway/backend/internal/repository"
	"github.com/tusharui/payment-gateway/backend/internal/security"
	"gorm.io/gorm"
)

// AuthorizationService handles payment authorization operations
type AuthorizationService struct {
	db              *db.DB
	authExpiryHours int
}

// NewAuthorizationService creates a new AuthorizationService
func NewAuthorizationService(
	database *db.DB,
	authExpiryHours int,
) *AuthorizationService {
	return &AuthorizationService{
		db:              database,
		authExpiryHours: authExpiryHours,
	}
}

// AuthorizationRequest describes the inputs for creating an authorization hold.
type AuthorizationRequest struct {
	CardNumber string
	CVV        string
	Amount     int64
	OrderID    *uuid.UUID
	CustomerID *uuid.UUID
}

// Authorize creates an authorization hold on a customer's account
func (s *AuthorizationService) Authorize(ctx context.Context, request AuthorizationRequest) (*models.Transaction, error) {
	if err := s.validateAuthorizationRequest(request.CardNumber, request.CVV, request.Amount); err != nil {
		return nil, err
	}

	var authTx *models.Transaction
	err := s.db.GORM().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		accountRepo := repository.NewAccountRepository(tx)
		transactionRepo := repository.NewTransactionRepository(tx)

		result, err := s.performAuthorization(ctx, accountRepo, transactionRepo, request)
		if err != nil {
			return err
		}

		authTx = result
		return emitPaymentEvent(ctx, tx, events.EventPaymentAuthorized, authTx)
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}

	return authTx, nil
}

// performAuthorization contains the core authorization business logic
func (s *AuthorizationService) performAuthorization(
	ctx context.Context,
	accountRepo repository.AccountRepository,
	transactionRepo repository.TransactionRepository,
	request AuthorizationRequest,
) (*models.Transaction, error) {
	cardToken := security.HashToken(request.CardNumber)
	account, err := accountRepo.FindByCardTokenForUpdate(ctx, cardToken)
	if err != nil {
		return nil, &ServiceError{
			Code:    ErrCodeInvalidCard,
			Message: "card not found or invalid",
		}
	}

	if err := ValidateCVV(request.CVV); err != nil {
		return nil, &ServiceError{
			Code:    ErrCodeInvalidCVV,
			Message: err.Error(),
		}
	}

	if err := ValidateExpiry(account.ExpiryMonth, account.ExpiryYear); err != nil {
		return nil, &ServiceError{
			Code:    ErrCodeCardExpired,
			Message: err.Error(),
		}
	}

	if account.AvailableBalanceCents < request.Amount {
		return nil, &ServiceError{
			Code:    ErrCodeInsufficientFunds,
			Message: "insufficient funds",
		}
	}

	authID := uuid.New()
	expiresAt := time.Now().Add(time.Duration(s.authExpiryHours) * time.Hour)
	createdAt := time.Now()

	authTx := &models.Transaction{
		ID:          authID,
		AccountID:   account.ID,
		OrderID:     request.OrderID,
		CustomerID:  request.CustomerID,
		Type:        models.TransactionTypeAuthHold,
		AmountCents: request.Amount,
		Currency:    "USD",
		Status:      models.TransactionStatusActive,
		ExpiresAt:   &expiresAt,
		CreatedAt:   createdAt,
	}

	if err := transactionRepo.Create(ctx, authTx); err != nil {
		return nil, &ServiceError{
			Code:    ErrCodeInternalError,
			Message: fmt.Sprintf("failed to create authorization: %v", err),
		}
	}

	if err := accountRepo.AdjustBalances(ctx, account.ID, 0, -request.Amount); err != nil {
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

	return authTx, nil
}

// GetAuthorization retrieves an authorization by ID
func (s *AuthorizationService) GetAuthorization(ctx context.Context, authID uuid.UUID) (*models.Transaction, error) {
	repo := repository.NewTransactionRepository(s.db.GORM())
	txn, err := repo.FindByID(ctx, authID)
	if err != nil || txn.Type != models.TransactionTypeAuthHold {
		return nil, &ServiceError{
			Code:    ErrCodeAuthNotFound,
			Message: "authorization not found",
		}
	}

	return txn, nil
}

func (s *AuthorizationService) validateAuthorizationRequest(cardNumber, cvv string, amount int64) error {
	if err := ValidateLuhn(cardNumber); err != nil {
		return &ServiceError{
			Code:    ErrCodeInvalidCard,
			Message: err.Error(),
		}
	}

	if err := ValidateCVV(cvv); err != nil {
		return &ServiceError{
			Code:    ErrCodeInvalidCVV,
			Message: err.Error(),
		}
	}

	if err := ValidateAmount(amount); err != nil {
		return &ServiceError{
			Code:    ErrCodeInvalidAmount,
			Message: err.Error(),
		}
	}

	return nil
}
