package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tusharui/payment-gateway/backend/internal/models"
)

func TestIdempotencyRepository_ClaimCompleteAndGet(t *testing.T) {
	database := setupTestDB(t)
	defer cleanupTestDB(t, database)
	truncateTables(t, database)

	repo := NewIdempotencyRepository(database.GORM())
	ctx := context.Background()

	tests := []struct {
		name        string
		key         string
		requestPath string
		hash        string
		body        string
		status      int
	}{
		{
			name:        "complete and retrieve simple key",
			key:         "test-key-1",
			requestPath: "/api/v1/authorizations",
			hash:        "hash-1",
			status:      200,
			body:        `{"status":"success"}`,
		},
		{
			name:        "complete and retrieve different path",
			key:         "test-key-2",
			requestPath: "/api/v1/captures",
			hash:        "hash-2",
			status:      201,
			body:        `{"capture_id":"cap_123"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acquired, err := repo.Claim(ctx, tt.key, tt.requestPath, tt.hash)
			require.NoError(t, err, "failed to claim idempotency key")
			require.True(t, acquired, "first claim should be acquired")

			pending, err := repo.Get(ctx, tt.key, tt.requestPath)
			require.NoError(t, err, "failed to get pending key")
			require.NotNil(t, pending, "expected pending idempotency key")
			assert.Equal(t, models.IdempotencyStatusPending, pending.ResponseStatus)
			assert.Equal(t, tt.hash, pending.RequestHash)

			err = repo.Complete(ctx, tt.key, tt.requestPath, tt.hash, tt.status, tt.body)
			require.NoError(t, err, "failed to complete idempotency key")

			retrieved, err := repo.Get(ctx, tt.key, tt.requestPath)
			require.NoError(t, err, "failed to get idempotency key")
			require.NotNil(t, retrieved, "expected idempotency key")

			assert.Equal(t, tt.key, retrieved.Key, "key mismatch")
			assert.Equal(t, tt.requestPath, retrieved.RequestPath, "request path mismatch")
			assert.Equal(t, tt.hash, retrieved.RequestHash, "request hash mismatch")
			assert.Equal(t, tt.status, retrieved.ResponseStatus, "status mismatch")
			assert.Equal(t, tt.body, retrieved.ResponseBody, "body mismatch")
		})
	}
}

func TestIdempotencyRepository_Get_NotFound(t *testing.T) {
	database := setupTestDB(t)
	defer cleanupTestDB(t, database)
	truncateTables(t, database)

	repo := NewIdempotencyRepository(database.GORM())

	result, err := repo.Get(context.Background(), "non-existent-key", "/api/v1/test")
	require.NoError(t, err, "unexpected error")
	assert.Nil(t, result, "expected nil for non-existent key")
}

func TestIdempotencyRepository_Claim_Conflict(t *testing.T) {
	database := setupTestDB(t)
	defer cleanupTestDB(t, database)
	truncateTables(t, database)

	repo := NewIdempotencyRepository(database.GORM())
	ctx := context.Background()

	key := "duplicate-key"
	path := "/api/v1/authorizations"

	acquired, err := repo.Claim(ctx, key, path, "first-hash")
	require.NoError(t, err)
	require.True(t, acquired)

	acquired, err = repo.Claim(ctx, key, path, "second-hash")
	require.NoError(t, err)
	assert.False(t, acquired, "second concurrent claim must not be acquired")

	retrieved, err := repo.Get(ctx, key, path)
	require.NoError(t, err)
	require.NotNil(t, retrieved)
	assert.Equal(t, "first-hash", retrieved.RequestHash, "first claim should win")
}

func TestIdempotencyRepository_Complete_OnlyOwnerCanComplete(t *testing.T) {
	database := setupTestDB(t)
	defer cleanupTestDB(t, database)
	truncateTables(t, database)

	repo := NewIdempotencyRepository(database.GORM())
	ctx := context.Background()

	key := "owner-key"
	path := "/api/v1/captures"

	acquired, err := repo.Claim(ctx, key, path, "owner-hash")
	require.NoError(t, err)
	require.True(t, acquired)

	err = repo.Complete(ctx, key, path, "other-hash", 200, `{"other":true}`)
	require.Error(t, err, "a non-owner must not complete the claim")

	err = repo.Complete(ctx, key, path, "owner-hash", 200, `{"owner":true}`)
	require.NoError(t, err)

	retrieved, err := repo.Get(ctx, key, path)
	require.NoError(t, err)
	require.NotNil(t, retrieved)
	assert.Equal(t, 200, retrieved.ResponseStatus)
	assert.Equal(t, `{"owner":true}`, retrieved.ResponseBody)
}

func TestIdempotencyRepository_Release(t *testing.T) {
	database := setupTestDB(t)
	defer cleanupTestDB(t, database)
	truncateTables(t, database)

	repo := NewIdempotencyRepository(database.GORM())
	ctx := context.Background()

	key := "release-key"
	path := "/api/v1/voids"

	acquired, err := repo.Claim(ctx, key, path, "release-hash")
	require.NoError(t, err)
	require.True(t, acquired)

	err = repo.Release(ctx, key, path, "release-hash")
	require.NoError(t, err)

	retrieved, err := repo.Get(ctx, key, path)
	require.NoError(t, err)
	assert.Nil(t, retrieved, "released claim should be removed")

	acquired, err = repo.Claim(ctx, key, path, "release-hash")
	require.NoError(t, err)
	assert.True(t, acquired, "released key should be claimable again")
}

func TestIdempotencyRepository_Release_DoesNotRemoveCompleted(t *testing.T) {
	database := setupTestDB(t)
	defer cleanupTestDB(t, database)
	truncateTables(t, database)

	repo := NewIdempotencyRepository(database.GORM())
	ctx := context.Background()

	key := "completed-key"
	path := "/api/v1/refunds"

	acquired, err := repo.Claim(ctx, key, path, "completed-hash")
	require.NoError(t, err)
	require.True(t, acquired)

	err = repo.Complete(ctx, key, path, "completed-hash", 200, `{"refunded":true}`)
	require.NoError(t, err)

	err = repo.Release(ctx, key, path, "completed-hash")
	require.NoError(t, err)

	retrieved, err := repo.Get(ctx, key, path)
	require.NoError(t, err)
	require.NotNil(t, retrieved, "completed entry must not be released")
	assert.Equal(t, 200, retrieved.ResponseStatus)
}

func TestIdempotencyRepository_Claim_TakesOverStalePending(t *testing.T) {
	database := setupTestDB(t)
	defer cleanupTestDB(t, database)
	truncateTables(t, database)

	repo := NewIdempotencyRepository(database.GORM())
	ctx := context.Background()

	key := "stale-key"
	path := "/api/v1/authorizations"

	acquired, err := repo.Claim(ctx, key, path, "stale-hash")
	require.NoError(t, err)
	require.True(t, acquired)

	staleCreatedAt := time.Now().Add(-2 * idempotencyReservationTTL)
	err = database.GORM().
		Model(&models.IdempotencyKey{}).
		Where("key = ? AND request_path = ?", key, path).
		Update("created_at", staleCreatedAt).Error
	require.NoError(t, err, "failed to age the pending claim")

	acquired, err = repo.Claim(ctx, key, path, "stale-hash")
	require.NoError(t, err)
	assert.True(t, acquired, "stale pending claim should be taken over")

	acquired, err = repo.Claim(ctx, key, path, "different-hash")
	require.NoError(t, err)
	assert.False(t, acquired, "a fresh pending claim must not be taken over")
}

func TestIdempotencyRepository_SameKey_DifferentPath(t *testing.T) {
	database := setupTestDB(t)
	defer cleanupTestDB(t, database)
	truncateTables(t, database)

	repo := NewIdempotencyRepository(database.GORM())
	ctx := context.Background()

	key := "same-key"

	acquired, err := repo.Claim(ctx, key, "/api/v1/authorizations", "auth-hash")
	require.NoError(t, err)
	require.True(t, acquired)
	require.NoError(t, repo.Complete(ctx, key, "/api/v1/authorizations", "auth-hash", 200, `{"auth":"response"}`))

	acquired, err = repo.Claim(ctx, key, "/api/v1/captures", "capture-hash")
	require.NoError(t, err)
	require.True(t, acquired)
	require.NoError(t, repo.Complete(ctx, key, "/api/v1/captures", "capture-hash", 201, `{"capture":"response"}`))

	retrieved1, err := repo.Get(ctx, key, "/api/v1/authorizations")
	require.NoError(t, err, "failed to get first")
	assert.Equal(t, `{"auth":"response"}`, retrieved1.ResponseBody, "first path body mismatch")

	retrieved2, err := repo.Get(ctx, key, "/api/v1/captures")
	require.NoError(t, err, "failed to get second")
	assert.Equal(t, `{"capture":"response"}`, retrieved2.ResponseBody, "second path body mismatch")
}

func TestIdempotencyRepository_DeleteOlderThan(t *testing.T) {
	database := setupTestDB(t)
	defer cleanupTestDB(t, database)
	truncateTables(t, database)

	repo := NewIdempotencyRepository(database.GORM())
	ctx := context.Background()

	now := time.Now()
	yesterday := now.Add(-24 * time.Hour)

	acquired, err := repo.Claim(ctx, "old-key", "/api/v1/test", "old-hash")
	require.NoError(t, err)
	require.True(t, acquired)
	require.NoError(t, repo.Complete(ctx, "old-key", "/api/v1/test", "old-hash", 200, "old"))

	err = database.GORM().
		Model(&models.IdempotencyKey{}).
		Where("key = ? AND request_path = ?", "old-key", "/api/v1/test").
		Update("created_at", yesterday.Add(-1*time.Hour)).Error // 25 hours ago
	require.NoError(t, err)

	acquired, err = repo.Claim(ctx, "recent-key", "/api/v1/test", "recent-hash")
	require.NoError(t, err)
	require.True(t, acquired)
	require.NoError(t, repo.Complete(ctx, "recent-key", "/api/v1/test", "recent-hash", 200, "recent"))

	deletedCount, err := repo.DeleteOlderThan(ctx, yesterday)
	require.NoError(t, err, "failed to delete old keys")
	assert.Equal(t, int64(1), deletedCount, "deleted count mismatch")

	oldResult, err := repo.Get(ctx, "old-key", "/api/v1/test")
	require.NoError(t, err, "unexpected error checking old key")
	assert.Nil(t, oldResult, "old key should have been deleted")

	recentResult, err := repo.Get(ctx, "recent-key", "/api/v1/test")
	require.NoError(t, err, "unexpected error checking recent key")
	assert.NotNil(t, recentResult, "recent key should still exist")
}

func TestIdempotencyRepository_DeleteOlderThan_NoneDeleted(t *testing.T) {
	database := setupTestDB(t)
	defer cleanupTestDB(t, database)
	truncateTables(t, database)

	repo := NewIdempotencyRepository(database.GORM())
	ctx := context.Background()

	acquired, err := repo.Claim(ctx, "recent-key", "/api/v1/test", "recent-hash")
	require.NoError(t, err)
	require.True(t, acquired)
	require.NoError(t, repo.Complete(ctx, "recent-key", "/api/v1/test", "recent-hash", 200, "recent"))

	veryOld := time.Now().Add(-365 * 24 * time.Hour)
	deletedCount, err := repo.DeleteOlderThan(ctx, veryOld)
	require.NoError(t, err, "unexpected error")
	assert.Equal(t, int64(0), deletedCount, "deleted count should be 0")
}
