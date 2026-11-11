package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tusharui/payment-gateway/backend/internal/db"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"github.com/tusharui/payment-gateway/backend/internal/security"
)

// seedTenantAccount inserts one account owned by the given merchant. Each
// merchant may hold its own copy of a card, so the card token is not globally
// unique any more.
func seedTenantAccount(t *testing.T, database *db.DB, merchantID uuid.UUID, cardNumber, last4 string) *models.Account {
	t.Helper()

	account := &models.Account{
		ID:                    uuid.New(),
		MerchantID:            merchantID,
		CardToken:             security.HashToken(cardNumber),
		Last4:                 last4,
		ExpiryMonth:           12,
		ExpiryYear:            2030,
		BalanceCents:          100000,
		AvailableBalanceCents: 100000,
	}
	require.NoError(t, database.GORM().Create(account).Error, "failed to seed tenant account")

	return account
}

func TestAccountRepository_IsolatesMerchants(t *testing.T) {
	database := setupTestDB(t)
	defer cleanupTestDB(t, database)
	truncateTables(t, database)

	ctx := context.Background()
	other := newTestMerchant(t, database, "acme")

	// Both merchants hold the same physical card, which is now legal because
	// the unique index is per merchant.
	otherAccount := seedTenantAccount(t, database, other.ID, "4111111111111111", "1111")

	otherRepo := NewAccountRepository(database.GORM(), other.ID)
	found, err := otherRepo.FindByCardToken(ctx, security.HashToken("4111111111111111"))
	require.NoError(t, err)
	assert.Equal(t, otherAccount.ID, found.ID, "the other merchant's copy must win")
	assert.Equal(t, other.ID, found.MerchantID)

	// The default merchant still sees only its own seeded account.
	defaultRepo := NewAccountRepository(database.GORM(), models.DefaultMerchantID)
	own, err := defaultRepo.FindByCardToken(ctx, security.HashToken("4111111111111111"))
	require.NoError(t, err)
	assert.NotEqual(t, otherAccount.ID, own.ID)
	assert.Equal(t, models.DefaultMerchantID, own.MerchantID)

	// Direct ID lookups cannot cross tenants either.
	crossTenant, err := defaultRepo.FindByID(ctx, otherAccount.ID)
	require.Error(t, err, "a foreign account must look missing")
	assert.Nil(t, crossTenant)

	_, err = otherRepo.FindByID(ctx, own.ID)
	require.Error(t, err, "the reverse lookup must also be blocked")
}

func TestTransactionRepository_ScopesToMerchant(t *testing.T) {
	database := setupTestDB(t)
	defer cleanupTestDB(t, database)
	truncateTables(t, database)

	ctx := context.Background()
	other := newTestMerchant(t, database, "acme")
	otherAccount := seedTenantAccount(t, database, other.ID, "4242424242424242", "4242")

	otherRepo := NewTransactionRepository(database.GORM(), other.ID)
	defaultRepo := NewTransactionRepository(database.GORM(), models.DefaultMerchantID)

	txn := &models.Transaction{
		AccountID:   otherAccount.ID,
		Type:        models.TransactionTypeAuthHold,
		AmountCents: 5000,
		Currency:    "USD",
		Status:      models.TransactionStatusActive,
		ExpiresAt:   timePtr(time.Now().Add(time.Hour)),
	}
	require.NoError(t, otherRepo.Create(ctx, txn))
	assert.Equal(t, other.ID, txn.MerchantID, "ownership must be stamped from the binding")

	found, err := otherRepo.FindByID(ctx, txn.ID)
	require.NoError(t, err)
	assert.Equal(t, txn.ID, found.ID)

	crossTenant, err := defaultRepo.FindByID(ctx, txn.ID)
	require.Error(t, err, "another tenant's transaction must look missing")
	assert.Nil(t, crossTenant)

	_, err = defaultRepo.FindByIDForUpdate(ctx, txn.ID)
	require.Error(t, err, "row locks must respect tenancy")

	err = defaultRepo.UpdateStatus(ctx, txn.ID, models.TransactionStatusVoided)
	require.Error(t, err, "writes must not cross tenants")

	refID := uuid.New()
	referenced := &models.Transaction{
		AccountID:   otherAccount.ID,
		Type:        models.TransactionTypeCapture,
		AmountCents: 5000,
		Currency:    "USD",
		Status:      models.TransactionStatusCompleted,
		ReferenceID: &refID,
	}
	require.NoError(t, otherRepo.Create(ctx, referenced))
	crossRef, err := defaultRepo.FindByReferenceID(ctx, refID, models.TransactionTypeCapture)
	require.NoError(t, err)
	assert.Nil(t, crossRef, "reference lookups must be tenant scoped")
}

func TestTransactionRepository_ExpirySweepSpansTenants(t *testing.T) {
	database := setupTestDB(t)
	defer cleanupTestDB(t, database)
	truncateTables(t, database)

	ctx := context.Background()
	now := time.Now()
	other := newTestMerchant(t, database, "acme")
	otherAccount := seedTenantAccount(t, database, other.ID, "4242424242424242", "4242")

	// The sweep deliberately looks across every merchant so background work can
	// release stale holds regardless of who owns them.
	tenantRepo := NewTransactionRepository(database.GORM(), other.ID)
	require.NoError(t, tenantRepo.Create(ctx, &models.Transaction{
		AccountID:   otherAccount.ID,
		Type:        models.TransactionTypeAuthHold,
		AmountCents: 1000,
		Currency:    "USD",
		Status:      models.TransactionStatusActive,
		ExpiresAt:   timePtr(now.Add(-time.Minute)),
	}))

	defaultAccount, err := NewAccountRepository(database.GORM(), models.DefaultMerchantID).
		FindByCardToken(ctx, security.HashToken("4111111111111111"))
	require.NoError(t, err)

	require.NoError(t, NewTransactionRepository(database.GORM(), models.DefaultMerchantID).Create(ctx, &models.Transaction{
		AccountID:   defaultAccount.ID,
		Type:        models.TransactionTypeAuthHold,
		AmountCents: 2000,
		Currency:    "USD",
		Status:      models.TransactionStatusActive,
		ExpiresAt:   timePtr(now.Add(-time.Minute)),
	}))

	sweepRepo := NewTransactionRepository(database.GORM(), uuid.Nil)
	batch, err := sweepRepo.FindExpiredAuthorizations(ctx, now, uuid.Nil, 10)
	require.NoError(t, err)
	require.Len(t, batch, 2, "the sweep should see expired holds from both merchants")

	merchants := map[uuid.UUID]bool{}
	for _, auth := range batch {
		merchants[auth.MerchantID] = true
		assert.NotEqual(t, uuid.Nil, auth.MerchantID, "each row must carry its owner")
	}
	assert.True(t, merchants[models.DefaultMerchantID])
	assert.True(t, merchants[other.ID])
}

func TestIdempotencyRepository_IsolatesMerchants(t *testing.T) {
	database := setupTestDB(t)
	defer cleanupTestDB(t, database)
	truncateTables(t, database)

	ctx := context.Background()
	other := newTestMerchant(t, database, "acme")

	defaultRepo := NewIdempotencyRepository(database.GORM(), models.DefaultMerchantID)
	otherRepo := NewIdempotencyRepository(database.GORM(), other.ID)

	const (
		key      = "shared-client-key"
		path     = "/api/v1/authorizations"
		hash     = "request-hash"
		response = `{"status":"ok"}`
	)

	// Two merchants may independently choose the same key.
	claimed, err := defaultRepo.Claim(ctx, key, path, hash)
	require.NoError(t, err)
	assert.True(t, claimed)

	claimed, err = otherRepo.Claim(ctx, key, path, hash)
	require.NoError(t, err)
	assert.True(t, claimed, "keys are scoped per merchant, not globally")

	require.NoError(t, defaultRepo.Complete(ctx, key, path, hash, 200, response))

	own, err := defaultRepo.Get(ctx, key, path)
	require.NoError(t, err)
	require.NotNil(t, own)
	assert.Equal(t, response, own.ResponseBody)
	assert.Equal(t, models.DefaultMerchantID, own.MerchantID)

	foreign, err := otherRepo.Get(ctx, key, path)
	require.NoError(t, err)
	require.NotNil(t, foreign)
	assert.NotEqual(t, response, foreign.ResponseBody, "the other merchant must not see the cached response")
	assert.Equal(t, other.ID, foreign.MerchantID)

	// Releasing one tenant's claim leaves the other intact.
	require.NoError(t, otherRepo.Release(ctx, key, path, hash))
	foreign, err = otherRepo.Get(ctx, key, path)
	require.NoError(t, err)
	assert.Nil(t, foreign)

	own, err = defaultRepo.Get(ctx, key, path)
	require.NoError(t, err)
	assert.NotNil(t, own, "the first merchant's completed key must survive")
}

func TestIdempotencyRepository_DeleteOlderThanSpansTenants(t *testing.T) {
	database := setupTestDB(t)
	defer cleanupTestDB(t, database)
	truncateTables(t, database)

	ctx := context.Background()
	other := newTestMerchant(t, database, "acme")

	old := time.Now().Add(-48 * time.Hour)
	for _, merchantID := range []uuid.UUID{models.DefaultMerchantID, other.ID} {
		repo := NewIdempotencyRepository(database.GORM(), merchantID)
		claimed, err := repo.Claim(ctx, "old-"+merchantID.String(), "/api/v1/captures", "hash")
		require.NoError(t, err)
		require.True(t, claimed)

		require.NoError(t, database.GORM().Model(&models.IdempotencyKey{}).
			Where("merchant_id = ?", merchantID).
			Update("created_at", old).Error)
	}

	deleted, err := NewIdempotencyRepository(database.GORM(), uuid.Nil).DeleteOlderThan(ctx, time.Now().Add(-24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted, "maintenance must clear every tenant")

	var remaining int64
	require.NoError(t, database.GORM().Model(&models.IdempotencyKey{}).Count(&remaining).Error)
	assert.Zero(t, remaining)
}
