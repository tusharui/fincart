package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tusharui/payment-gateway/backend/internal/api"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"github.com/tusharui/payment-gateway/backend/internal/service"
)

type stubAPIKeyManager struct {
	createResult   *service.IssuedAPIKey
	createErr      error
	createdRequest service.APIKeyIssueRequest

	listResult []models.APIKey
	listErr    error

	rotateResult *service.IssuedAPIKey
	rotateErr    error
	rotatedID    uuid.UUID

	revokeErr error
	revokedID uuid.UUID
}

func (s *stubAPIKeyManager) Create(_ context.Context, request service.APIKeyIssueRequest) (*service.IssuedAPIKey, error) {
	s.createdRequest = request
	return s.createResult, s.createErr
}

func (s *stubAPIKeyManager) List(context.Context) ([]models.APIKey, error) {
	return s.listResult, s.listErr
}

func (s *stubAPIKeyManager) Rotate(_ context.Context, id uuid.UUID) (*service.IssuedAPIKey, error) {
	s.rotatedID = id
	return s.rotateResult, s.rotateErr
}

func (s *stubAPIKeyManager) Revoke(_ context.Context, id uuid.UUID) error {
	s.revokedID = id
	return s.revokeErr
}

func apiKeyHandler(manager service.APIKeyManager) *Handler {
	return NewHandler(nil, nil, nil, nil, nil, testLogger(), WithAPIKeyManager(manager))
}

func sampleIssuedKey() *service.IssuedAPIKey {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	return &service.IssuedAPIKey{
		Key: &models.APIKey{
			ID:                 uuid.New(),
			MerchantID:         uuid.New(),
			Name:               "checkout",
			KeyPrefix:          "bk_12345678",
			KeyLast4:           "abcd",
			Scopes:             pq.StringArray{models.ScopePayments},
			RateLimitPerMinute: 60,
			CreatedAt:          now,
		},
		Secret: "bk_secret-value",
	}
}

func TestCreateAPIKey_Success(t *testing.T) {
	issued := sampleIssuedKey()
	manager := &stubAPIKeyManager{createResult: issued}
	handler := apiKeyHandler(manager)

	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	req := api.CreateAPIKeyRequestObject{
		Body: &api.CreateAPIKeyJSONRequestBody{
			Name:               "checkout",
			Scopes:             []api.APIKeyScope{api.Payments, api.Admin},
			RateLimitPerMinute: 60,
			ExpiresAt:          expires,
		},
	}

	resp, err := handler.CreateAPIKey(context.Background(), req)
	require.NoError(t, err)

	created, ok := resp.(api.CreateAPIKey201JSONResponse)
	require.True(t, ok, "expected 201 response")
	assert.Equal(t, issued.Key.ID, created.ApiKey.Id)
	assert.Equal(t, "bk_secret-value", created.Secret)
	assert.Equal(t, []api.APIKeyScope{api.Payments}, created.ApiKey.Scopes)
	assert.Equal(t, "checkout", manager.createdRequest.Name)
	assert.Equal(t, []string{models.ScopePayments, models.ScopeAdmin}, manager.createdRequest.Scopes)
	assert.Equal(t, 60, manager.createdRequest.RateLimitPerMinute)
	require.NotNil(t, manager.createdRequest.ExpiresAt)
	assert.Equal(t, expires, *manager.createdRequest.ExpiresAt)
}

func TestCreateAPIKey_OmittedExpiryIsNil(t *testing.T) {
	manager := &stubAPIKeyManager{createResult: sampleIssuedKey()}
	handler := apiKeyHandler(manager)

	_, err := handler.CreateAPIKey(context.Background(), api.CreateAPIKeyRequestObject{
		Body: &api.CreateAPIKeyJSONRequestBody{Name: "checkout"},
	})
	require.NoError(t, err)
	assert.Nil(t, manager.createdRequest.ExpiresAt)
}

func TestCreateAPIKey_NilBody(t *testing.T) {
	handler := apiKeyHandler(&stubAPIKeyManager{})

	resp, err := handler.CreateAPIKey(context.Background(), api.CreateAPIKeyRequestObject{})
	require.NoError(t, err)

	badRequest, ok := resp.(api.CreateAPIKey400JSONResponse)
	require.True(t, ok, "expected 400 response")
	assert.Equal(t, api.ErrorCodeInvalidRequest, badRequest.Error)
}

func TestCreateAPIKey_ValidationError(t *testing.T) {
	manager := &stubAPIKeyManager{createErr: &service.ServiceError{
		Code:    service.ErrCodeInvalidRequest,
		Message: "name must be between 1 and 100 characters",
	}}
	handler := apiKeyHandler(manager)

	resp, err := handler.CreateAPIKey(context.Background(), api.CreateAPIKeyRequestObject{
		Body: &api.CreateAPIKeyJSONRequestBody{Name: ""},
	})
	require.NoError(t, err)

	badRequest, ok := resp.(api.CreateAPIKey400JSONResponse)
	require.True(t, ok, "expected 400 response")
	assert.Equal(t, api.ErrorCodeInvalidRequest, badRequest.Error)
	assert.Equal(t, "name must be between 1 and 100 characters", badRequest.Message)
}

func TestCreateAPIKey_InternalErrorDoesNotLeakDetail(t *testing.T) {
	manager := &stubAPIKeyManager{createErr: errors.New("database on fire")}
	handler := apiKeyHandler(manager)

	resp, err := handler.CreateAPIKey(context.Background(), api.CreateAPIKeyRequestObject{
		Body: &api.CreateAPIKeyJSONRequestBody{Name: "checkout"},
	})
	require.NoError(t, err)

	internal, ok := resp.(api.CreateAPIKey500JSONResponse)
	require.True(t, ok, "expected 500 response")
	assert.Equal(t, api.ErrorCodeInternalError, internal.Error)
	assert.Equal(t, "internal error", internal.Message)
}

func TestListAPIKeys_Success(t *testing.T) {
	lastUsed := time.Date(2026, 2, 1, 8, 0, 0, 0, time.UTC)
	expires := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	revoked := time.Date(2026, 2, 15, 8, 0, 0, 0, time.UTC)

	manager := &stubAPIKeyManager{listResult: []models.APIKey{
		{
			ID:                 uuid.New(),
			Name:               "active",
			KeyPrefix:          "bk_aaaaaaaa",
			KeyLast4:           "1111",
			Scopes:             pq.StringArray{models.ScopePayments, models.ScopeAdmin},
			RateLimitPerMinute: 120,
			Bootstrap:          true,
			CreatedAt:          revoked,
			LastUsedAt:         &lastUsed,
			ExpiresAt:          &expires,
		},
		{
			ID:        uuid.New(),
			Name:      "revoked",
			CreatedAt: revoked,
			RevokedAt: &revoked,
		},
	}}
	handler := apiKeyHandler(manager)

	resp, err := handler.ListAPIKeys(context.Background(), api.ListAPIKeysRequestObject{})
	require.NoError(t, err)

	list, ok := resp.(api.ListAPIKeys200JSONResponse)
	require.True(t, ok, "expected 200 response")
	require.Len(t, list.ApiKeys, 2)

	first := list.ApiKeys[0]
	assert.Equal(t, "active", first.Name)
	assert.True(t, first.Bootstrap)
	assert.Equal(t, []api.APIKeyScope{api.Payments, api.Admin}, first.Scopes)
	assert.Equal(t, lastUsed, first.LastUsedAt)
	assert.Equal(t, expires, first.ExpiresAt)
	assert.True(t, first.RevokedAt.IsZero(), "an active key has no revocation timestamp")

	second := list.ApiKeys[1]
	assert.Equal(t, revoked, second.RevokedAt)
	assert.True(t, second.LastUsedAt.IsZero())
}

func TestListAPIKeys_Error(t *testing.T) {
	handler := apiKeyHandler(&stubAPIKeyManager{listErr: errors.New("query failed")})

	resp, err := handler.ListAPIKeys(context.Background(), api.ListAPIKeysRequestObject{})
	require.NoError(t, err)

	internal, ok := resp.(api.ListAPIKeys500JSONResponse)
	require.True(t, ok, "expected 500 response")
	assert.Equal(t, api.ErrorCodeInternalError, internal.Error)
}

func TestRotateAPIKey_Success(t *testing.T) {
	issued := sampleIssuedKey()
	manager := &stubAPIKeyManager{rotateResult: issued}
	handler := apiKeyHandler(manager)
	keyID := uuid.New()

	resp, err := handler.RotateAPIKey(context.Background(), api.RotateAPIKeyRequestObject{ApiKeyId: keyID})
	require.NoError(t, err)

	rotated, ok := resp.(api.RotateAPIKey200JSONResponse)
	require.True(t, ok, "expected 200 response")
	assert.Equal(t, issued.Key.ID, rotated.ApiKey.Id)
	assert.Equal(t, "bk_secret-value", rotated.Secret)
	assert.Equal(t, keyID, manager.rotatedID)
}

func TestRotateAPIKey_NotFound(t *testing.T) {
	manager := &stubAPIKeyManager{rotateErr: &service.ServiceError{
		Code:    service.ErrCodeNotFound,
		Message: "api key not found",
	}}
	handler := apiKeyHandler(manager)

	resp, err := handler.RotateAPIKey(context.Background(), api.RotateAPIKeyRequestObject{ApiKeyId: uuid.New()})
	require.NoError(t, err)

	notFound, ok := resp.(api.RotateAPIKey404JSONResponse)
	require.True(t, ok, "expected 404 response")
	assert.Equal(t, api.ErrorCodeNotFound, notFound.Error)
}

func TestRotateAPIKey_InternalError(t *testing.T) {
	manager := &stubAPIKeyManager{rotateErr: errors.New("update failed")}
	handler := apiKeyHandler(manager)

	resp, err := handler.RotateAPIKey(context.Background(), api.RotateAPIKeyRequestObject{ApiKeyId: uuid.New()})
	require.NoError(t, err)

	internal, ok := resp.(api.RotateAPIKey500JSONResponse)
	require.True(t, ok, "expected 500 response")
	assert.Equal(t, api.ErrorCodeInternalError, internal.Error)
}

func TestRevokeAPIKey_Success(t *testing.T) {
	manager := &stubAPIKeyManager{}
	handler := apiKeyHandler(manager)
	keyID := uuid.New()

	resp, err := handler.RevokeAPIKey(context.Background(), api.RevokeAPIKeyRequestObject{ApiKeyId: keyID})
	require.NoError(t, err)

	_, ok := resp.(api.RevokeAPIKey204Response)
	require.True(t, ok, "expected 204 response")
	assert.Equal(t, keyID, manager.revokedID)
}

func TestRevokeAPIKey_NotFound(t *testing.T) {
	manager := &stubAPIKeyManager{revokeErr: &service.ServiceError{
		Code:    service.ErrCodeNotFound,
		Message: "api key not found",
	}}
	handler := apiKeyHandler(manager)

	resp, err := handler.RevokeAPIKey(context.Background(), api.RevokeAPIKeyRequestObject{ApiKeyId: uuid.New()})
	require.NoError(t, err)

	notFound, ok := resp.(api.RevokeAPIKey404JSONResponse)
	require.True(t, ok, "expected 404 response")
	assert.Equal(t, api.ErrorCodeNotFound, notFound.Error)
}

func TestRevokeAPIKey_InternalError(t *testing.T) {
	manager := &stubAPIKeyManager{revokeErr: errors.New("update failed")}
	handler := apiKeyHandler(manager)

	resp, err := handler.RevokeAPIKey(context.Background(), api.RevokeAPIKeyRequestObject{ApiKeyId: uuid.New()})
	require.NoError(t, err)

	internal, ok := resp.(api.RevokeAPIKey500JSONResponse)
	require.True(t, ok, "expected 500 response")
	assert.Equal(t, api.ErrorCodeInternalError, internal.Error)
}

func TestAPIKeyHandlers_FailClosedWithoutManager(t *testing.T) {
	handler := NewHandler(nil, nil, nil, nil, nil, testLogger())

	listResp, err := handler.ListAPIKeys(context.Background(), api.ListAPIKeysRequestObject{})
	require.NoError(t, err)
	_, listOK := listResp.(api.ListAPIKeys500JSONResponse)
	assert.True(t, listOK)

	revokeResp, err := handler.RevokeAPIKey(context.Background(), api.RevokeAPIKeyRequestObject{ApiKeyId: uuid.New()})
	require.NoError(t, err)
	_, revokeOK := revokeResp.(api.RevokeAPIKey500JSONResponse)
	assert.True(t, revokeOK)
}
