package service

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"github.com/tusharui/payment-gateway/backend/internal/repository/mocks"
	"github.com/tusharui/payment-gateway/backend/internal/security"
)

func TestAuthorizationService_PerformAuthorization(t *testing.T) {
	t.Run("successful authorization", func(t *testing.T) {
		mockAccountRepo := mocks.NewMockAccountRepository(t)
		mockTxRepo := mocks.NewMockTransactionRepository(t)
		service := NewAuthorizationService(nil, 168)
		ctx := context.Background()

		accountID := uuid.New()
		cardNumber := "4111111111111111"
		cvv := "123"
		var amount int64 = 10000

		account := &models.Account{
			ID:                    accountID,
			CardToken:             security.HashToken(cardNumber),
			Last4:                 "1111",
			ExpiryMonth:           12,
			ExpiryYear:            2030,
			BalanceCents:          50000,
			AvailableBalanceCents: 50000,
		}

		mockAccountRepo.On("FindByCardTokenForUpdate", ctx, security.HashToken(cardNumber)).Return(account, nil)
		mockTxRepo.On("Create", ctx, mock.AnythingOfType("*models.Transaction")).Return(nil)
		mockAccountRepo.On("AdjustBalances", ctx, accountID, int64(0), int64(-10000)).Return(nil)

		result, err := service.performAuthorization(ctx, mockAccountRepo, mockTxRepo, cardNumber, cvv, amount)

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, accountID, result.AccountID)
		assert.Equal(t, models.TransactionTypeAuthHold, result.Type)
		assert.Equal(t, amount, result.AmountCents)
		assert.Equal(t, "USD", result.Currency)
		assert.Equal(t, models.TransactionStatusActive, result.Status)
		assert.NotNil(t, result.ExpiresAt)

		mockAccountRepo.AssertExpectations(t)
		mockTxRepo.AssertExpectations(t)
	})

	t.Run("account not found", func(t *testing.T) {
		mockAccountRepo := mocks.NewMockAccountRepository(t)
		mockTxRepo := mocks.NewMockTransactionRepository(t)
		service := NewAuthorizationService(nil, 168)
		ctx := context.Background()

		cardNumber := "4111111111111111"
		cvv := "123"
		var amount int64 = 10000

		mockAccountRepo.On("FindByCardTokenForUpdate", ctx, security.HashToken(cardNumber)).
			Return(nil, sql.ErrNoRows)

		result, err := service.performAuthorization(ctx, mockAccountRepo, mockTxRepo, cardNumber, cvv, amount)

		assert.Error(t, err)
		assert.Nil(t, result)

		var svcErr *ServiceError
		if assert.ErrorAs(t, err, &svcErr) {
			assert.Equal(t, ErrCodeInvalidCard, svcErr.Code)
		}

		mockAccountRepo.AssertExpectations(t)
	})

	t.Run("CVV mismatch", func(t *testing.T) {
		mockAccountRepo := mocks.NewMockAccountRepository(t)
		mockTxRepo := mocks.NewMockTransactionRepository(t)
		service := NewAuthorizationService(nil, 168)
		ctx := context.Background()

		accountID := uuid.New()
		cardNumber := "4111111111111111"
		cvv := "12" // Invalid CVV format (must be 3-4 digits)
		var amount int64 = 10000

		account := &models.Account{
			ID:                    accountID,
			CardToken:             security.HashToken(cardNumber),
			Last4:                 "1111", // Tokenized last4
			ExpiryMonth:           12,
			ExpiryYear:            2030,
			BalanceCents:          50000,
			AvailableBalanceCents: 50000,
		}

		mockAccountRepo.On("FindByCardTokenForUpdate", ctx, security.HashToken(cardNumber)).Return(account, nil)

		result, err := service.performAuthorization(ctx, mockAccountRepo, mockTxRepo, cardNumber, cvv, amount)

		assert.Error(t, err)
		assert.Nil(t, result)

		var svcErr *ServiceError
		if assert.ErrorAs(t, err, &svcErr) {
			assert.Equal(t, ErrCodeInvalidCVV, svcErr.Code)
		}

		mockAccountRepo.AssertExpectations(t)
	})

	t.Run("card expired", func(t *testing.T) {
		mockAccountRepo := mocks.NewMockAccountRepository(t)
		mockTxRepo := mocks.NewMockTransactionRepository(t)
		service := NewAuthorizationService(nil, 168)
		ctx := context.Background()

		accountID := uuid.New()
		cardNumber := "4111111111111111"
		cvv := "123"
		var amount int64 = 10000

		account := &models.Account{
			ID:                    accountID,
			CardToken:             security.HashToken(cardNumber),
			Last4:                 "1111",
			ExpiryMonth:           1,
			ExpiryYear:            2020, // Expired
			BalanceCents:          50000,
			AvailableBalanceCents: 50000,
		}

		mockAccountRepo.On("FindByCardTokenForUpdate", ctx, security.HashToken(cardNumber)).Return(account, nil)

		result, err := service.performAuthorization(ctx, mockAccountRepo, mockTxRepo, cardNumber, cvv, amount)

		assert.Error(t, err)
		assert.Nil(t, result)

		var svcErr *ServiceError
		if assert.ErrorAs(t, err, &svcErr) {
			assert.Equal(t, ErrCodeCardExpired, svcErr.Code)
		}

		mockAccountRepo.AssertExpectations(t)
	})

	t.Run("insufficient funds", func(t *testing.T) {
		mockAccountRepo := mocks.NewMockAccountRepository(t)
		mockTxRepo := mocks.NewMockTransactionRepository(t)
		service := NewAuthorizationService(nil, 168)
		ctx := context.Background()

		accountID := uuid.New()
		cardNumber := "4111111111111111"
		cvv := "123"
		var amount int64 = 10000

		account := &models.Account{
			ID:                    accountID,
			CardToken:             security.HashToken(cardNumber),
			Last4:                 "1111",
			ExpiryMonth:           12,
			ExpiryYear:            2030,
			BalanceCents:          5000,
			AvailableBalanceCents: 5000, // Less than requested amount
		}

		mockAccountRepo.On("FindByCardTokenForUpdate", ctx, security.HashToken(cardNumber)).Return(account, nil)

		result, err := service.performAuthorization(ctx, mockAccountRepo, mockTxRepo, cardNumber, cvv, amount)

		assert.Error(t, err)
		assert.Nil(t, result)

		var svcErr *ServiceError
		if assert.ErrorAs(t, err, &svcErr) {
			assert.Equal(t, ErrCodeInsufficientFunds, svcErr.Code)
		}

		mockAccountRepo.AssertExpectations(t)
	})

	t.Run("transaction creation fails", func(t *testing.T) {
		mockAccountRepo := mocks.NewMockAccountRepository(t)
		mockTxRepo := mocks.NewMockTransactionRepository(t)
		service := NewAuthorizationService(nil, 168)
		ctx := context.Background()

		accountID := uuid.New()
		cardNumber := "4111111111111111"
		cvv := "123"
		var amount int64 = 10000

		account := &models.Account{
			ID:                    accountID,
			CardToken:             security.HashToken(cardNumber),
			Last4:                 "1111",
			ExpiryMonth:           12,
			ExpiryYear:            2030,
			BalanceCents:          50000,
			AvailableBalanceCents: 50000,
		}

		mockAccountRepo.On("FindByCardTokenForUpdate", ctx, security.HashToken(cardNumber)).Return(account, nil)
		mockTxRepo.On("Create", ctx, mock.AnythingOfType("*models.Transaction")).
			Return(models.ErrDuplicateTransaction)

		result, err := service.performAuthorization(ctx, mockAccountRepo, mockTxRepo, cardNumber, cvv, amount)

		assert.Error(t, err)
		assert.Nil(t, result)

		var svcErr *ServiceError
		if assert.ErrorAs(t, err, &svcErr) {
			assert.Equal(t, ErrCodeInternalError, svcErr.Code)
		}

		mockAccountRepo.AssertExpectations(t)
		mockTxRepo.AssertExpectations(t)
	})

	t.Run("balance adjustment fails", func(t *testing.T) {
		mockAccountRepo := mocks.NewMockAccountRepository(t)
		mockTxRepo := mocks.NewMockTransactionRepository(t)
		service := NewAuthorizationService(nil, 168)
		ctx := context.Background()

		accountID := uuid.New()
		cardNumber := "4111111111111111"
		cvv := "123"
		var amount int64 = 10000

		account := &models.Account{
			ID:                    accountID,
			CardToken:             security.HashToken(cardNumber),
			Last4:                 "1111",
			ExpiryMonth:           12,
			ExpiryYear:            2030,
			BalanceCents:          50000,
			AvailableBalanceCents: 50000,
		}

		mockAccountRepo.On("FindByCardTokenForUpdate", ctx, security.HashToken(cardNumber)).Return(account, nil)
		mockTxRepo.On("Create", ctx, mock.AnythingOfType("*models.Transaction")).Return(nil)
		mockAccountRepo.On("AdjustBalances", ctx, accountID, int64(0), int64(-10000)).
			Return(assert.AnError)

		result, err := service.performAuthorization(ctx, mockAccountRepo, mockTxRepo, cardNumber, cvv, amount)

		assert.Error(t, err)
		assert.Nil(t, result)

		var svcErr *ServiceError
		if assert.ErrorAs(t, err, &svcErr) {
			assert.Equal(t, ErrCodeInternalError, svcErr.Code)
		}

		mockAccountRepo.AssertExpectations(t)
		mockTxRepo.AssertExpectations(t)
	})

	t.Run("balance guard rejects hold", func(t *testing.T) {
		mockAccountRepo := mocks.NewMockAccountRepository(t)
		mockTxRepo := mocks.NewMockTransactionRepository(t)
		service := NewAuthorizationService(nil, 168)
		ctx := context.Background()

		accountID := uuid.New()
		cardNumber := "4111111111111111"
		cvv := "123"
		var amount int64 = 10000

		account := &models.Account{
			ID:                    accountID,
			CardToken:             security.HashToken(cardNumber),
			Last4:                 "1111",
			ExpiryMonth:           12,
			ExpiryYear:            2030,
			BalanceCents:          50000,
			AvailableBalanceCents: 50000,
		}

		mockAccountRepo.On("FindByCardTokenForUpdate", ctx, security.HashToken(cardNumber)).Return(account, nil)
		mockTxRepo.On("Create", ctx, mock.AnythingOfType("*models.Transaction")).Return(nil)
		mockAccountRepo.On("AdjustBalances", ctx, accountID, int64(0), int64(-10000)).
			Return(fmt.Errorf("guard: %w", models.ErrInsufficientFunds))

		result, err := service.performAuthorization(ctx, mockAccountRepo, mockTxRepo, cardNumber, cvv, amount)

		assert.Error(t, err)
		assert.Nil(t, result)

		var svcErr *ServiceError
		if assert.ErrorAs(t, err, &svcErr) {
			assert.Equal(t, ErrCodeInsufficientFunds, svcErr.Code)
		}

		mockAccountRepo.AssertExpectations(t)
		mockTxRepo.AssertExpectations(t)
	})
}

func TestAuthorizationService_ValidateAuthorizationRequest(t *testing.T) {
	service := NewAuthorizationService(nil, 168)

	// Individual validators are already tested in validators_test.go
	// This test verifies that validation errors are wrapped in ServiceError with correct codes
	t.Run("wraps validation errors in ServiceError", func(t *testing.T) {
		err := service.validateAuthorizationRequest("1234567890123456", "123", 10000)
		assert.Error(t, err)

		var svcErr *ServiceError
		if assert.ErrorAs(t, err, &svcErr) {
			assert.Equal(t, ErrCodeInvalidCard, svcErr.Code)
		}
	})
}
