package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
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

// stubAPIKeyStore is a hand-written repository stand-in for key management
// tests. Authentication has its own stub in auth_test.go.
type stubAPIKeyStore struct {
	created   *models.APIKey
	createErr error

	listed  []models.APIKey
	listErr error

	rotateResult bool
	rotateErr    error
	rotateHash   string
	rotatePrefix string
	rotateLast4  string

	found   *models.APIKey
	findErr error

	revokeResult bool
	revokeErr    error
	revokedAt    time.Time
}

func (s *stubAPIKeyStore) Create(_ context.Context, key *models.APIKey) error {
	s.created = key
	return s.createErr
}

func (s *stubAPIKeyStore) FindByHash(context.Context, string) (*models.APIKey, error) {
	panic("FindByHash is not used by key management tests")
}

func (s *stubAPIKeyStore) FindByID(context.Context, uuid.UUID, uuid.UUID) (*models.APIKey, error) {
	return s.found, s.findErr
}

func (s *stubAPIKeyStore) ListByMerchant(context.Context, uuid.UUID) ([]models.APIKey, error) {
	return s.listed, s.listErr
}

func (s *stubAPIKeyStore) Revoke(_ context.Context, _ uuid.UUID, _ uuid.UUID, revokedAt time.Time) (bool, error) {
	s.revokedAt = revokedAt
	return s.revokeResult, s.revokeErr
}

func (s *stubAPIKeyStore) Rotate(
	_ context.Context,
	_ uuid.UUID,
	_ uuid.UUID,
	keyHash, keyPrefix, keyLast4 string,
) (bool, error) {
	s.rotateHash = keyHash
	s.rotatePrefix = keyPrefix
	s.rotateLast4 = keyLast4

	return s.rotateResult, s.rotateErr
}

func (s *stubAPIKeyStore) TouchLastUsed(context.Context, uuid.UUID, time.Time, time.Time) error {
	panic("TouchLastUsed is not used by key management tests")
}

func (s *stubAPIKeyStore) EnsureBootstrap(context.Context, *models.APIKey) (*models.APIKey, error) {
	panic("EnsureBootstrap is not used by key management tests")
}

func (s *stubAPIKeyStore) CountActive(context.Context, time.Time) (int64, error) {
	panic("CountActive is not used by key management tests")
}

func testAPIKeyService(store *stubAPIKeyStore, now time.Time) *APIKeyService {
	return newAPIKeyService(store, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return now })
}

func merchantContext(merchantID uuid.UUID) context.Context {
	return auth.WithPrincipal(context.Background(), auth.Principal{
		MerchantID: merchantID,
		APIKeyID:   uuid.New(),
	})
}

func TestAPIKeyService_Create_DefaultsAndHashesSecret(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	merchantID := uuid.New()
	store := &stubAPIKeyStore{}
	service := testAPIKeyService(store, now)

	issued, err := service.Create(merchantContext(merchantID), APIKeyIssueRequest{Name: "  checkout  "})
	require.NoError(t, err)
	require.NotNil(t, issued)
	require.NotNil(t, store.created)

	assert.Equal(t, merchantID, store.created.MerchantID)
	assert.Equal(t, "checkout", store.created.Name)
	assert.Equal(t, []string{models.ScopePayments}, []string(store.created.Scopes))
	assert.Equal(t, now, store.created.CreatedAt)
	assert.Zero(t, store.created.RateLimitPerMinute)
	assert.False(t, store.created.Bootstrap)

	assert.NotEmpty(t, issued.Secret)
	assert.NotEqual(t, issued.Secret, store.created.KeyHash, "the raw secret must never be persisted")
	assert.Equal(t, security.HashToken(issued.Secret), store.created.KeyHash)

	prefix, last4 := security.DescribeAPIKey(issued.Secret)
	assert.Equal(t, prefix, store.created.KeyPrefix)
	assert.Equal(t, last4, store.created.KeyLast4)
}

func TestAPIKeyService_Create_HonoursScopesLimitAndExpiry(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	expires := now.Add(24 * time.Hour)
	store := &stubAPIKeyStore{}
	service := testAPIKeyService(store, now)

	issued, err := service.Create(merchantContext(uuid.New()), APIKeyIssueRequest{
		Name:               "admin",
		Scopes:             []string{models.ScopeAdmin, models.ScopePayments, models.ScopeAdmin},
		RateLimitPerMinute: 240,
		ExpiresAt:          &expires,
	})
	require.NoError(t, err)
	require.NotNil(t, issued)
	assert.Equal(t, 240, store.created.RateLimitPerMinute)
	assert.Equal(t, []string{models.ScopeAdmin, models.ScopePayments}, []string(store.created.Scopes),
		"duplicate scopes are removed while order is preserved")
	require.NotNil(t, store.created.ExpiresAt)
	assert.Equal(t, expires, *store.created.ExpiresAt)
}

func TestAPIKeyService_Create_Validation(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Minute)

	tests := []struct {
		name    string
		request APIKeyIssueRequest
	}{
		{name: "blank name", request: APIKeyIssueRequest{Name: "   "}},
		{name: "name too long", request: APIKeyIssueRequest{Name: strings.Repeat("a", maxAPIKeyNameLength+1)}},
		{name: "unknown scope", request: APIKeyIssueRequest{Name: "key", Scopes: []string{"refunds"}}},
		{name: "negative rate limit", request: APIKeyIssueRequest{Name: "key", RateLimitPerMinute: -1}},
		{name: "expiry in the past", request: APIKeyIssueRequest{Name: "key", ExpiresAt: &past}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &stubAPIKeyStore{}
			service := testAPIKeyService(store, now)

			_, err := service.Create(merchantContext(uuid.New()), tt.request)
			require.Error(t, err)

			var svcErr *ServiceError
			require.ErrorAs(t, err, &svcErr)
			assert.Equal(t, ErrCodeInvalidRequest, svcErr.Code)
			assert.Nil(t, store.created, "invalid requests must not reach the repository")
		})
	}
}

func TestAPIKeyService_Create_RequiresMerchant(t *testing.T) {
	store := &stubAPIKeyStore{}
	service := testAPIKeyService(store, time.Now())

	_, err := service.Create(context.Background(), APIKeyIssueRequest{Name: "key"})
	require.ErrorIs(t, err, errMissingMerchantContext)
	assert.Nil(t, store.created)
}

func TestAPIKeyService_Create_PropagatesRepositoryError(t *testing.T) {
	store := &stubAPIKeyStore{createErr: errors.New("insert failed")}
	service := testAPIKeyService(store, time.Now())

	_, err := service.Create(merchantContext(uuid.New()), APIKeyIssueRequest{Name: "key"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "insert failed")
}

func TestAPIKeyService_List(t *testing.T) {
	merchantID := uuid.New()
	store := &stubAPIKeyStore{listed: []models.APIKey{
		{ID: uuid.New(), MerchantID: merchantID, Name: "one", Scopes: pq.StringArray{models.ScopePayments}},
	}}
	service := testAPIKeyService(store, time.Now())

	keys, err := service.List(merchantContext(merchantID))
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, "one", keys[0].Name)
}

func TestAPIKeyService_List_RequiresMerchant(t *testing.T) {
	service := testAPIKeyService(&stubAPIKeyStore{}, time.Now())

	_, err := service.List(context.Background())
	require.ErrorIs(t, err, errMissingMerchantContext)
}

func TestAPIKeyService_Rotate_ReplacesSecret(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	merchantID := uuid.New()
	keyID := uuid.New()
	store := &stubAPIKeyStore{
		rotateResult: true,
		found: &models.APIKey{
			ID:         keyID,
			MerchantID: merchantID,
			Name:       "primary",
			Scopes:     pq.StringArray{models.ScopePayments},
		},
	}
	service := testAPIKeyService(store, now)

	issued, err := service.Rotate(merchantContext(merchantID), keyID)
	require.NoError(t, err)
	require.NotNil(t, issued)
	assert.Equal(t, keyID, issued.Key.ID)
	assert.NotEmpty(t, issued.Secret)
	assert.Equal(t, security.HashToken(issued.Secret), store.rotateHash)
	assert.NotEmpty(t, store.rotatePrefix)
	assert.Len(t, store.rotateLast4, 4)
}

func TestAPIKeyService_Rotate_NotFound(t *testing.T) {
	merchantID := uuid.New()
	store := &stubAPIKeyStore{rotateResult: false}
	service := testAPIKeyService(store, time.Now())

	_, err := service.Rotate(merchantContext(merchantID), uuid.New())
	require.Error(t, err)

	var svcErr *ServiceError
	require.ErrorAs(t, err, &svcErr)
	assert.Equal(t, ErrCodeNotFound, svcErr.Code)
}

func TestAPIKeyService_Rotate_RequiresMerchant(t *testing.T) {
	service := testAPIKeyService(&stubAPIKeyStore{}, time.Now())

	_, err := service.Rotate(context.Background(), uuid.New())
	require.ErrorIs(t, err, errMissingMerchantContext)
}

func TestAPIKeyService_Revoke(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	store := &stubAPIKeyStore{revokeResult: true}
	service := testAPIKeyService(store, now)

	require.NoError(t, service.Revoke(merchantContext(uuid.New()), uuid.New()))
	assert.Equal(t, now, store.revokedAt)
}

func TestAPIKeyService_Revoke_NotFound(t *testing.T) {
	service := testAPIKeyService(&stubAPIKeyStore{revokeResult: false}, time.Now())

	err := service.Revoke(merchantContext(uuid.New()), uuid.New())
	require.Error(t, err)

	var svcErr *ServiceError
	require.ErrorAs(t, err, &svcErr)
	assert.Equal(t, ErrCodeNotFound, svcErr.Code)
}

func TestAPIKeyService_Revoke_RequiresMerchant(t *testing.T) {
	service := testAPIKeyService(&stubAPIKeyStore{}, time.Now())

	err := service.Revoke(context.Background(), uuid.New())
	require.ErrorIs(t, err, errMissingMerchantContext)
}

func TestNormalizeScopes(t *testing.T) {
	scopes, err := normalizeScopes(nil)
	require.NoError(t, err)
	assert.Equal(t, []string{models.ScopePayments}, scopes)

	scopes, err = normalizeScopes([]string{models.ScopeAdmin, models.ScopeAdmin})
	require.NoError(t, err)
	assert.Equal(t, []string{models.ScopeAdmin}, scopes)
}
