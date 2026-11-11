package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tusharui/payment-gateway/backend/internal/db"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"github.com/tusharui/payment-gateway/backend/internal/security"
)

func newTestMerchant(t *testing.T, database *db.DB, name string) *models.Merchant {
	t.Helper()

	merchant := &models.Merchant{ID: uuid.New(), Name: name}
	require.NoError(t, NewMerchantRepository(database.GORM()).Create(context.Background(), merchant))

	return merchant
}

func newTestAPIKey(t *testing.T, merchantID uuid.UUID, name string, scopes ...string) (*models.APIKey, security.GeneratedAPIKey) {
	t.Helper()

	generated, err := security.GenerateAPIKey()
	require.NoError(t, err)

	if len(scopes) == 0 {
		scopes = []string{models.ScopePayments}
	}

	return &models.APIKey{
		ID:         uuid.New(),
		MerchantID: merchantID,
		Name:       name,
		KeyPrefix:  generated.Prefix,
		KeyLast4:   generated.Last4,
		KeyHash:    security.HashToken(generated.Raw),
		Scopes:     pq.StringArray(scopes),
		CreatedAt:  time.Now(),
	}, generated
}

func TestAPIKeyRepository_Lifecycle(t *testing.T) {
	database := setupTestDB(t)
	defer cleanupTestDB(t, database)
	truncateTables(t, database)

	ctx := context.Background()
	merchant := newTestMerchant(t, database, "acme")
	repo := NewAPIKeyRepository(database.GORM())

	key, generated := newTestAPIKey(t, merchant.ID, "primary", models.ScopePayments, models.ScopeAdmin)
	require.NoError(t, repo.Create(ctx, key))

	found, err := repo.FindByHash(ctx, security.HashToken(generated.Raw))
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, key.ID, found.ID)
	assert.Equal(t, merchant.ID, found.MerchantID)
	assert.True(t, found.HasScope(models.ScopePayments))
	assert.True(t, found.HasScope(models.ScopeAdmin))
	assert.True(t, found.IsActive(time.Now()))

	unknown, err := repo.FindByHash(ctx, security.HashToken("not-a-real-key"))
	require.NoError(t, err)
	assert.Nil(t, unknown)

	scoped, err := repo.FindByID(ctx, merchant.ID, key.ID)
	require.NoError(t, err)
	require.NotNil(t, scoped)

	crossTenant, err := repo.FindByID(ctx, uuid.New(), key.ID)
	require.NoError(t, err)
	assert.Nil(t, crossTenant, "a key must not be visible to another merchant")

	keys, err := repo.ListByMerchant(ctx, merchant.ID)
	require.NoError(t, err)
	require.Len(t, keys, 1)

	unused := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, repo.TouchLastUsed(ctx, key.ID, unused, unused.Add(time.Second)))
	stored, err := repo.FindByID(ctx, merchant.ID, key.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.LastUsedAt)
	assert.WithinDuration(t, unused, *stored.LastUsedAt, time.Second)

	// A second touch inside the throttle window must not move the timestamp.
	require.NoError(t, repo.TouchLastUsed(ctx, key.ID, unused.Add(time.Hour), unused))
	stored, err = repo.FindByID(ctx, merchant.ID, key.ID)
	require.NoError(t, err)
	assert.WithinDuration(t, unused, *stored.LastUsedAt, time.Second)

	revoked, err := repo.Revoke(ctx, merchant.ID, key.ID, time.Now())
	require.NoError(t, err)
	assert.True(t, revoked)

	revokedAgain, err := repo.Revoke(ctx, merchant.ID, key.ID, time.Now())
	require.NoError(t, err)
	assert.False(t, revokedAgain, "revocation must be idempotent")

	revokedKey, err := repo.FindByID(ctx, merchant.ID, key.ID)
	require.NoError(t, err)
	require.NotNil(t, revokedKey.RevokedAt)
	assert.False(t, revokedKey.IsActive(time.Now()))

	crossTenantRevoke, err := repo.Revoke(ctx, uuid.New(), key.ID, time.Now())
	require.NoError(t, err)
	assert.False(t, crossTenantRevoke)

	count, err := repo.CountActive(ctx, time.Now())
	require.NoError(t, err)
	assert.Zero(t, count)
}

func TestAPIKeyRepository_EnsureBootstrapReplacesCredential(t *testing.T) {
	database := setupTestDB(t)
	defer cleanupTestDB(t, database)
	truncateTables(t, database)

	ctx := context.Background()
	merchant := newTestMerchant(t, database, "bootstrap")
	repo := NewAPIKeyRepository(database.GORM())

	first, firstRaw := newTestAPIKey(t, merchant.ID, "bootstrap", models.ScopePayments, models.ScopeAdmin)
	first.Bootstrap = true
	stored, err := repo.EnsureBootstrap(ctx, first)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, first.KeyHash, stored.KeyHash)

	second, secondRaw := newTestAPIKey(t, merchant.ID, "bootstrap-rotated", models.ScopePayments, models.ScopeAdmin)
	second.Bootstrap = true
	stored, err = repo.EnsureBootstrap(ctx, second)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, first.ID, stored.ID, "the bootstrap row is replaced in place")
	assert.Equal(t, second.KeyHash, stored.KeyHash)
	assert.Equal(t, second.Name, stored.Name)

	oldLookup, err := repo.FindByHash(ctx, security.HashToken(firstRaw.Raw))
	require.NoError(t, err)
	assert.Nil(t, oldLookup, "the previous bootstrap credential must stop working")

	newLookup, err := repo.FindByHash(ctx, security.HashToken(secondRaw.Raw))
	require.NoError(t, err)
	require.NotNil(t, newLookup)

	keys, err := repo.ListByMerchant(ctx, merchant.ID)
	require.NoError(t, err)
	assert.Len(t, keys, 1, "rotating the bootstrap key must not accumulate credentials")
}

func TestAPIKeyRepository_Rotate(t *testing.T) {
	database := setupTestDB(t)
	defer cleanupTestDB(t, database)
	truncateTables(t, database)

	ctx := context.Background()
	merchant := newTestMerchant(t, database, "rotate")
	repo := NewAPIKeyRepository(database.GORM())

	key, oldCredential := newTestAPIKey(t, merchant.ID, "primary", models.ScopePayments)
	require.NoError(t, repo.Create(ctx, key))

	newCredential, err := security.GenerateAPIKey()
	require.NoError(t, err)

	rotated, err := repo.Rotate(
		ctx,
		merchant.ID,
		key.ID,
		security.HashToken(newCredential.Raw),
		newCredential.Prefix,
		newCredential.Last4,
	)
	require.NoError(t, err)
	assert.True(t, rotated)

	oldLookup, err := repo.FindByHash(ctx, security.HashToken(oldCredential.Raw))
	require.NoError(t, err)
	assert.Nil(t, oldLookup, "the previous secret must stop authenticating")

	stored, err := repo.FindByID(ctx, merchant.ID, key.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, newCredential.Prefix, stored.KeyPrefix)
	assert.Equal(t, newCredential.Last4, stored.KeyLast4)
	assert.Equal(t, key.Name, stored.Name, "rotation keeps the key identity")
	assert.Equal(t, key.Scopes, stored.Scopes, "rotation keeps the key scopes")

	newLookup, err := repo.FindByHash(ctx, security.HashToken(newCredential.Raw))
	require.NoError(t, err)
	require.NotNil(t, newLookup)
	assert.Equal(t, key.ID, newLookup.ID)

	crossTenant, err := repo.Rotate(ctx, uuid.New(), key.ID, "hash", "prefix", "last")
	require.NoError(t, err)
	assert.False(t, crossTenant, "a key must not be rotatable by another merchant")

	_, err = repo.Revoke(ctx, merchant.ID, key.ID, time.Now())
	require.NoError(t, err)

	afterRevoke, err := repo.Rotate(ctx, merchant.ID, key.ID, "hash", "prefix", "last")
	require.NoError(t, err)
	assert.False(t, afterRevoke, "a revoked key must not be rotated")
}

func TestAPIKey_IsActive(t *testing.T) {
	now := time.Now()
	expired := now.Add(-time.Minute)
	future := now.Add(time.Hour)

	tests := []struct {
		name string
		key  models.APIKey
		want bool
	}{
		{name: "active", key: models.APIKey{}, want: true},
		{name: "expired", key: models.APIKey{ExpiresAt: &expired}, want: false},
		{name: "not yet expired", key: models.APIKey{ExpiresAt: &future}, want: true},
		{name: "revoked", key: models.APIKey{RevokedAt: &expired}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.key.IsActive(now))
		})
	}
}
