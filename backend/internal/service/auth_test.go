package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tusharui/payment-gateway/backend/internal/auth"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"github.com/tusharui/payment-gateway/backend/internal/security"
)

// stubAPIKeyRepository is a hand-written stand-in for repository.APIKeyRepository
// so authentication behaviour can be tested without a database.
type stubAPIKeyRepository struct {
	findResult  *models.APIKey
	findErr     error
	touchErr    error
	touchedAt   time.Time
	touchBefore time.Time
	touchCalls  int
	ensured     *models.APIKey
	ensureErr   error
	activeCount int64
	countErr    error
}

func (s *stubAPIKeyRepository) Create(context.Context, *models.APIKey) error {
	panic("Create is not used by AuthService tests")
}

func (s *stubAPIKeyRepository) FindByHash(context.Context, string) (*models.APIKey, error) {
	return s.findResult, s.findErr
}

func (s *stubAPIKeyRepository) FindByID(context.Context, uuid.UUID, uuid.UUID) (*models.APIKey, error) {
	panic("FindByID is not used by AuthService tests")
}

func (s *stubAPIKeyRepository) ListByMerchant(context.Context, uuid.UUID) ([]models.APIKey, error) {
	panic("ListByMerchant is not used by AuthService tests")
}

func (s *stubAPIKeyRepository) Revoke(context.Context, uuid.UUID, uuid.UUID, time.Time) (bool, error) {
	panic("Revoke is not used by AuthService tests")
}

func (s *stubAPIKeyRepository) TouchLastUsed(_ context.Context, _ uuid.UUID, usedAt, staleBefore time.Time) error {
	s.touchCalls++
	s.touchedAt = usedAt
	s.touchBefore = staleBefore

	return s.touchErr
}

func (s *stubAPIKeyRepository) EnsureBootstrap(_ context.Context, key *models.APIKey) (*models.APIKey, error) {
	s.ensured = key
	if s.ensureErr != nil {
		return nil, s.ensureErr
	}

	return key, nil
}

func (s *stubAPIKeyRepository) CountActive(context.Context, time.Time) (int64, error) {
	return s.activeCount, s.countErr
}

type stubMerchantRepository struct {
	ensured *models.Merchant
	err     error
}

func (s *stubMerchantRepository) Create(context.Context, *models.Merchant) error {
	panic("Create is not used by AuthService tests")
}

func (s *stubMerchantRepository) Ensure(_ context.Context, merchant *models.Merchant) error {
	s.ensured = merchant
	return s.err
}

func (s *stubMerchantRepository) FindByID(context.Context, uuid.UUID) (*models.Merchant, error) {
	panic("FindByID is not used by AuthService tests")
}

func testAuthService(keys *stubAPIKeyRepository, merchants *stubMerchantRepository, now time.Time) *AuthService {
	return newAuthService(
		keys,
		merchants,
		time.Minute,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		func() time.Time { return now },
	)
}

func activeTestKey(now time.Time) (*models.APIKey, string) {
	generated, err := security.GenerateAPIKey()
	if err != nil {
		panic(err)
	}

	return &models.APIKey{
		ID:                 uuid.New(),
		MerchantID:         models.DefaultMerchantID,
		Name:               "test",
		KeyPrefix:          generated.Prefix,
		KeyLast4:           generated.Last4,
		KeyHash:            security.HashToken(generated.Raw),
		Scopes:             pq.StringArray{models.ScopePayments, models.ScopeAdmin},
		RateLimitPerMinute: 120,
		CreatedAt:          now.Add(-time.Hour),
	}, generated.Raw
}

func TestAuthService_Authenticate(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	key, rawKey := activeTestKey(now)

	t.Run("returns a principal and records usage", func(t *testing.T) {
		keys := &stubAPIKeyRepository{findResult: key}
		service := testAuthService(keys, &stubMerchantRepository{}, now)

		principal, err := service.Authenticate(context.Background(), rawKey)
		require.NoError(t, err)
		require.NotNil(t, principal)

		assert.Equal(t, key.MerchantID, principal.MerchantID)
		assert.Equal(t, key.ID, principal.APIKeyID)
		assert.Equal(t, key.Name, principal.KeyName)
		assert.Equal(t, []string{models.ScopePayments, models.ScopeAdmin}, principal.Scopes)
		assert.Equal(t, 120, principal.RateLimitPerMinute)
		assert.True(t, principal.HasScope(models.ScopePayments))
		assert.False(t, principal.HasScope("unknown"))

		assert.Equal(t, 1, keys.touchCalls)
		assert.Equal(t, now, keys.touchedAt)
		assert.Equal(t, now.Add(-time.Minute), keys.touchBefore,
			"only keys older than the throttle window are updated")
	})

	t.Run("rejects an empty credential without a database lookup", func(t *testing.T) {
		keys := &stubAPIKeyRepository{findErr: errors.New("must not be called")}
		service := testAuthService(keys, &stubMerchantRepository{}, now)

		principal, err := service.Authenticate(context.Background(), "")
		require.ErrorIs(t, err, auth.ErrInvalidAPIKey)
		assert.Nil(t, principal)
		assert.Zero(t, keys.touchCalls)
	})

	t.Run("rejects an unknown credential", func(t *testing.T) {
		keys := &stubAPIKeyRepository{}
		service := testAuthService(keys, &stubMerchantRepository{}, now)

		principal, err := service.Authenticate(context.Background(), rawKey)
		require.ErrorIs(t, err, auth.ErrInvalidAPIKey)
		assert.Nil(t, principal)
		assert.Zero(t, keys.touchCalls)
	})

	t.Run("rejects a revoked credential", func(t *testing.T) {
		revoked := *key
		revokedAt := now.Add(-time.Minute)
		revoked.RevokedAt = &revokedAt

		keys := &stubAPIKeyRepository{findResult: &revoked}
		service := testAuthService(keys, &stubMerchantRepository{}, now)

		principal, err := service.Authenticate(context.Background(), rawKey)
		require.ErrorIs(t, err, auth.ErrInvalidAPIKey)
		assert.Nil(t, principal)
	})

	t.Run("rejects an expired credential", func(t *testing.T) {
		expired := *key
		expiresAt := now.Add(-time.Second)
		expired.ExpiresAt = &expiresAt

		keys := &stubAPIKeyRepository{findResult: &expired}
		service := testAuthService(keys, &stubMerchantRepository{}, now)

		principal, err := service.Authenticate(context.Background(), rawKey)
		require.ErrorIs(t, err, auth.ErrInvalidAPIKey)
		assert.Nil(t, principal)
	})

	t.Run("reports storage failures separately from invalid keys", func(t *testing.T) {
		keys := &stubAPIKeyRepository{findErr: errors.New("connection reset")}
		service := testAuthService(keys, &stubMerchantRepository{}, now)

		principal, err := service.Authenticate(context.Background(), rawKey)
		require.Error(t, err)
		assert.NotErrorIs(t, err, auth.ErrInvalidAPIKey, "outages must not look like bad credentials")
		assert.Nil(t, principal)
	})

	t.Run("usage tracking failures do not reject the request", func(t *testing.T) {
		keys := &stubAPIKeyRepository{findResult: key, touchErr: errors.New("write failed")}
		service := testAuthService(keys, &stubMerchantRepository{}, now)

		principal, err := service.Authenticate(context.Background(), rawKey)
		require.NoError(t, err)
		require.NotNil(t, principal)
		assert.Equal(t, 1, keys.touchCalls)
	})
}

func TestAuthService_EnsureBootstrap(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

	t.Run("creates a merchant and a hashed bootstrap key", func(t *testing.T) {
		keys := &stubAPIKeyRepository{}
		merchants := &stubMerchantRepository{}
		service := testAuthService(keys, merchants, now)

		const rawKey = "bootstrap-secret-value"
		key, err := service.EnsureBootstrap(context.Background(), rawKey, "acme")
		require.NoError(t, err)
		require.NotNil(t, key)

		require.NotNil(t, merchants.ensured)
		assert.Equal(t, models.DefaultMerchantID, merchants.ensured.ID)
		assert.Equal(t, "acme", merchants.ensured.Name)

		require.NotNil(t, keys.ensured)
		assert.Equal(t, security.HashToken(rawKey), keys.ensured.KeyHash)
		assert.NotContains(t, keys.ensured.KeyHash, rawKey)
		assert.True(t, keys.ensured.Bootstrap)
		assert.Equal(t, models.DefaultMerchantID, keys.ensured.MerchantID)
		assert.ElementsMatch(t, []string{models.ScopePayments, models.ScopeAdmin}, keys.ensured.Scopes)

		prefix, last4 := security.DescribeAPIKey(rawKey)
		assert.Equal(t, prefix, keys.ensured.KeyPrefix)
		assert.Equal(t, last4, keys.ensured.KeyLast4)
		assert.Equal(t, now, keys.ensured.CreatedAt)
	})

	t.Run("rejects an empty key", func(t *testing.T) {
		keys := &stubAPIKeyRepository{}
		merchants := &stubMerchantRepository{}
		service := testAuthService(keys, merchants, now)

		key, err := service.EnsureBootstrap(context.Background(), "", "acme")
		require.Error(t, err)
		assert.Nil(t, key)
		assert.Nil(t, keys.ensured)
	})

	t.Run("fails when the merchant cannot be ensured", func(t *testing.T) {
		keys := &stubAPIKeyRepository{}
		merchants := &stubMerchantRepository{err: errors.New("insert failed")}
		service := testAuthService(keys, merchants, now)

		key, err := service.EnsureBootstrap(context.Background(), "bootstrap-secret-value", "acme")
		require.Error(t, err)
		assert.Nil(t, key)
		assert.Nil(t, keys.ensured)
	})

	t.Run("propagates key storage failures", func(t *testing.T) {
		keys := &stubAPIKeyRepository{ensureErr: errors.New("conflict")}
		service := testAuthService(keys, &stubMerchantRepository{}, now)

		key, err := service.EnsureBootstrap(context.Background(), "bootstrap-secret-value", "acme")
		require.Error(t, err)
		assert.Nil(t, key)
	})
}

func TestAuthService_ActiveKeyCount(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

	keys := &stubAPIKeyRepository{activeCount: 3}
	service := testAuthService(keys, &stubMerchantRepository{}, now)

	count, err := service.ActiveKeyCount(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(3), count)

	failing := &stubAPIKeyRepository{countErr: errors.New("count failed")}
	service = testAuthService(failing, &stubMerchantRepository{}, now)

	_, err = service.ActiveKeyCount(context.Background())
	require.Error(t, err)
}
