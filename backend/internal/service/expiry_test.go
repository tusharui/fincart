package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"github.com/tusharui/payment-gateway/backend/internal/repository/mocks"
)

func TestExpiryService_ExpireAuthorization(t *testing.T) {
	now := time.Date(2026, time.November, 7, 12, 0, 0, 0, time.UTC)

	t.Run("expires an active hold and releases funds", func(t *testing.T) {
		mockTxRepo := mocks.NewMockTransactionRepository(t)
		mockAccountRepo := mocks.NewMockAccountRepository(t)
		svc := NewExpiryService(nil)
		ctx := context.Background()

		authID := uuid.New()
		accountID := uuid.New()
		const amount int64 = 10000
		expiresAt := now.Add(-time.Minute)

		auth := &models.Transaction{
			ID:          authID,
			AccountID:   accountID,
			Type:        models.TransactionTypeAuthHold,
			AmountCents: amount,
			Status:      models.TransactionStatusActive,
			ExpiresAt:   &expiresAt,
		}

		mockTxRepo.On("FindByIDForUpdate", ctx, authID).Return(auth, nil)
		mockTxRepo.On("UpdateStatus", ctx, authID, models.TransactionStatusExpired).Return(nil)
		mockAccountRepo.On("AdjustBalances", ctx, accountID, int64(0), amount).Return(nil)

		expired, err := svc.expireAuthorization(ctx, mockTxRepo, mockAccountRepo, authID, now)

		require.NoError(t, err)
		assert.True(t, expired)
		mockTxRepo.AssertExpectations(t)
		mockAccountRepo.AssertExpectations(t)
	})

	t.Run("leaves an unexpired hold untouched", func(t *testing.T) {
		mockTxRepo := mocks.NewMockTransactionRepository(t)
		mockAccountRepo := mocks.NewMockAccountRepository(t)
		svc := NewExpiryService(nil)
		ctx := context.Background()

		authID := uuid.New()
		expiresAt := now.Add(time.Minute)

		auth := &models.Transaction{
			ID:          authID,
			AccountID:   uuid.New(),
			Type:        models.TransactionTypeAuthHold,
			AmountCents: 10000,
			Status:      models.TransactionStatusActive,
			ExpiresAt:   &expiresAt,
		}

		mockTxRepo.On("FindByIDForUpdate", ctx, authID).Return(auth, nil)

		expired, err := svc.expireAuthorization(ctx, mockTxRepo, mockAccountRepo, authID, now)

		require.NoError(t, err)
		assert.False(t, expired)
		mockTxRepo.AssertNotCalled(t, "UpdateStatus")
		mockAccountRepo.AssertNotCalled(t, "AdjustBalances")
	})

	t.Run("skips a hold without an expiry time", func(t *testing.T) {
		mockTxRepo := mocks.NewMockTransactionRepository(t)
		mockAccountRepo := mocks.NewMockAccountRepository(t)
		svc := NewExpiryService(nil)
		ctx := context.Background()

		authID := uuid.New()
		auth := &models.Transaction{
			ID:          authID,
			AccountID:   uuid.New(),
			Type:        models.TransactionTypeAuthHold,
			AmountCents: 10000,
			Status:      models.TransactionStatusActive,
		}

		mockTxRepo.On("FindByIDForUpdate", ctx, authID).Return(auth, nil)

		expired, err := svc.expireAuthorization(ctx, mockTxRepo, mockAccountRepo, authID, now)

		require.NoError(t, err)
		assert.False(t, expired)
		mockTxRepo.AssertNotCalled(t, "UpdateStatus")
		mockAccountRepo.AssertNotCalled(t, "AdjustBalances")
	})

	t.Run("skips a hold that is no longer active", func(t *testing.T) {
		mockTxRepo := mocks.NewMockTransactionRepository(t)
		mockAccountRepo := mocks.NewMockAccountRepository(t)
		svc := NewExpiryService(nil)
		ctx := context.Background()

		authID := uuid.New()
		expiresAt := now.Add(-time.Hour)
		auth := &models.Transaction{
			ID:          authID,
			AccountID:   uuid.New(),
			Type:        models.TransactionTypeAuthHold,
			AmountCents: 10000,
			Status:      models.TransactionStatusCompleted,
			ExpiresAt:   &expiresAt,
		}

		mockTxRepo.On("FindByIDForUpdate", ctx, authID).Return(auth, nil)

		expired, err := svc.expireAuthorization(ctx, mockTxRepo, mockAccountRepo, authID, now)

		require.NoError(t, err)
		assert.False(t, expired)
		mockTxRepo.AssertNotCalled(t, "UpdateStatus")
		mockAccountRepo.AssertNotCalled(t, "AdjustBalances")
	})

	t.Run("skips a transaction that is not an authorization hold", func(t *testing.T) {
		mockTxRepo := mocks.NewMockTransactionRepository(t)
		mockAccountRepo := mocks.NewMockAccountRepository(t)
		svc := NewExpiryService(nil)
		ctx := context.Background()

		authID := uuid.New()
		expiresAt := now.Add(-time.Hour)
		auth := &models.Transaction{
			ID:          authID,
			AccountID:   uuid.New(),
			Type:        models.TransactionTypeCapture,
			AmountCents: 10000,
			Status:      models.TransactionStatusActive,
			ExpiresAt:   &expiresAt,
		}

		mockTxRepo.On("FindByIDForUpdate", ctx, authID).Return(auth, nil)

		expired, err := svc.expireAuthorization(ctx, mockTxRepo, mockAccountRepo, authID, now)

		require.NoError(t, err)
		assert.False(t, expired)
		mockTxRepo.AssertNotCalled(t, "UpdateStatus")
		mockAccountRepo.AssertNotCalled(t, "AdjustBalances")
	})

	t.Run("returns an error when the status update fails", func(t *testing.T) {
		mockTxRepo := mocks.NewMockTransactionRepository(t)
		mockAccountRepo := mocks.NewMockAccountRepository(t)
		svc := NewExpiryService(nil)
		ctx := context.Background()

		authID := uuid.New()
		expiresAt := now.Add(-time.Hour)
		auth := &models.Transaction{
			ID:          authID,
			AccountID:   uuid.New(),
			Type:        models.TransactionTypeAuthHold,
			AmountCents: 10000,
			Status:      models.TransactionStatusActive,
			ExpiresAt:   &expiresAt,
		}

		mockTxRepo.On("FindByIDForUpdate", ctx, authID).Return(auth, nil)
		mockTxRepo.On("UpdateStatus", ctx, authID, models.TransactionStatusExpired).Return(assert.AnError)

		expired, err := svc.expireAuthorization(ctx, mockTxRepo, mockAccountRepo, authID, now)

		require.Error(t, err)
		assert.False(t, expired)
		mockTxRepo.AssertExpectations(t)
		mockAccountRepo.AssertNotCalled(t, "AdjustBalances")
	})

	t.Run("returns an error when releasing funds fails", func(t *testing.T) {
		mockTxRepo := mocks.NewMockTransactionRepository(t)
		mockAccountRepo := mocks.NewMockAccountRepository(t)
		svc := NewExpiryService(nil)
		ctx := context.Background()

		authID := uuid.New()
		accountID := uuid.New()
		expiresAt := now.Add(-time.Hour)
		auth := &models.Transaction{
			ID:          authID,
			AccountID:   accountID,
			Type:        models.TransactionTypeAuthHold,
			AmountCents: 10000,
			Status:      models.TransactionStatusActive,
			ExpiresAt:   &expiresAt,
		}

		mockTxRepo.On("FindByIDForUpdate", ctx, authID).Return(auth, nil)
		mockTxRepo.On("UpdateStatus", ctx, authID, models.TransactionStatusExpired).Return(nil)
		mockAccountRepo.On("AdjustBalances", ctx, accountID, int64(0), int64(10000)).Return(assert.AnError)

		expired, err := svc.expireAuthorization(ctx, mockTxRepo, mockAccountRepo, authID, now)

		require.Error(t, err)
		assert.False(t, expired)
		mockTxRepo.AssertExpectations(t)
		mockAccountRepo.AssertExpectations(t)
	})

	t.Run("returns an error when the authorization cannot be loaded", func(t *testing.T) {
		mockTxRepo := mocks.NewMockTransactionRepository(t)
		mockAccountRepo := mocks.NewMockAccountRepository(t)
		svc := NewExpiryService(nil)
		ctx := context.Background()

		authID := uuid.New()
		mockTxRepo.On("FindByIDForUpdate", ctx, authID).Return(nil, assert.AnError)

		expired, err := svc.expireAuthorization(ctx, mockTxRepo, mockAccountRepo, authID, now)

		require.Error(t, err)
		assert.False(t, expired)
		mockTxRepo.AssertExpectations(t)
	})
}
