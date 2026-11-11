//nolint:errcheck // unchecked errors are acceptable in test files
package tests

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tusharui/payment-gateway/backend/internal/config"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"github.com/tusharui/payment-gateway/backend/internal/security"
)

// unknownAuthorizationPath is a well-formed authorization URL for an id that
// does not exist. Authenticated requests reach the handler and return 404, which
// makes it a cheap way to distinguish an accepted credential from a rejected
// one without mutating ledger state.
const unknownAuthorizationPath = "/api/v1/authorizations/auth_00000000-0000-0000-0000-000000000000"

// doJSON issues a request with an optional bearer credential, JSON body, and
// extra headers. An empty credential omits the Authorization header entirely.
func (ts *TestServer) doJSON(
	t *testing.T,
	method, path, apiKey string,
	body any,
	headers map[string]string,
) *http.Response {
	t.Helper()

	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		require.NoError(t, err)
		payload = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, ts.URL(path), payload)
	require.NoError(t, err)

	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	return resp
}

// authorizeWithKey creates an authorization on behalf of an explicit credential.
func (ts *TestServer) authorizeWithKey(
	t *testing.T,
	apiKey, cardNumber, cvv string,
	amount int64,
	idempotencyKey string,
) *http.Response {
	t.Helper()

	return ts.doJSON(t, http.MethodPost, "/api/v1/authorizations", apiKey, map[string]any{
		"card_number": cardNumber,
		"cvv":         cvv,
		"amount":      amount,
	}, map[string]string{"Idempotency-Key": idempotencyKey})
}

// captureWithKey captures an authorization on behalf of an explicit credential.
func (ts *TestServer) captureWithKey(
	t *testing.T,
	apiKey, authID string,
	amount int64,
	idempotencyKey string,
) *http.Response {
	t.Helper()

	return ts.doJSON(t, http.MethodPost, "/api/v1/captures", apiKey, map[string]any{
		"authorization_id": authID,
		"amount":           amount,
	}, map[string]string{"Idempotency-Key": idempotencyKey})
}

// authorizationIDFromResponse decodes the public authorization identifier.
func authorizationIDFromResponse(t *testing.T, resp *http.Response) string {
	t.Helper()

	body := decodeJSONBody(t, resp)
	authID, ok := body["authorization_id"].(string)
	require.True(t, ok, "response should contain authorization_id")

	return authID
}

// seedMerchant inserts a tenant that owns no payment data yet.
func seedMerchant(t *testing.T, ts *TestServer, name string) *models.Merchant {
	t.Helper()

	merchant := &models.Merchant{ID: uuid.New(), Name: name}
	require.NoError(t, ts.Database.GORM().Create(merchant).Error, "failed to seed merchant")

	return merchant
}

// seedAccount inserts a tokenized card for a tenant.
func seedAccount(
	t *testing.T,
	ts *TestServer,
	merchantID uuid.UUID,
	cardNumber, last4 string,
	balanceCents int64,
) *models.Account {
	t.Helper()

	account := &models.Account{
		ID:                    uuid.New(),
		MerchantID:            merchantID,
		CardToken:             security.HashToken(cardNumber),
		Last4:                 last4,
		ExpiryMonth:           12,
		ExpiryYear:            2030,
		BalanceCents:          balanceCents,
		AvailableBalanceCents: balanceCents,
	}
	require.NoError(t, ts.Database.GORM().Create(account).Error, "failed to seed account")

	return account
}

// seedAPIKeyRecord persists key metadata and returns the raw secret that
// authenticates as it. The caller supplies merchant, name, and any lifecycle
// timestamps; secret material is always generated here.
func seedAPIKeyRecord(t *testing.T, ts *TestServer, key *models.APIKey) string {
	t.Helper()

	generated, err := security.GenerateAPIKey()
	require.NoError(t, err)

	if key.ID == uuid.Nil {
		key.ID = uuid.New()
	}
	key.KeyPrefix = generated.Prefix
	key.KeyLast4 = generated.Last4
	key.KeyHash = security.HashToken(generated.Raw)
	if len(key.Scopes) == 0 {
		key.Scopes = pq.StringArray{models.ScopePayments}
	}

	require.NoError(t, ts.Database.GORM().Create(key).Error, "failed to seed api key")

	return generated.Raw
}

// seedAPIKey inserts an active key with the requested rate limit and scopes.
func seedAPIKey(
	t *testing.T,
	ts *TestServer,
	merchantID uuid.UUID,
	name string,
	rateLimitPerMinute int,
	scopes ...string,
) (*models.APIKey, string) {
	t.Helper()

	key := &models.APIKey{
		MerchantID:         merchantID,
		Name:               name,
		RateLimitPerMinute: rateLimitPerMinute,
	}
	if len(scopes) > 0 {
		key.Scopes = pq.StringArray(scopes)
	}

	return key, seedAPIKeyRecord(t, ts, key)
}

func TestAuth_RejectsMissingAndInvalidCredentials(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	expiresAt := time.Now().Add(-time.Hour)
	expiredSecret := seedAPIKeyRecord(t, ts, &models.APIKey{
		MerchantID: ts.MerchantID,
		Name:       "expired",
		ExpiresAt:  &expiresAt,
	})

	revokedAt := time.Now().Add(-time.Hour)
	revokedSecret := seedAPIKeyRecord(t, ts, &models.APIKey{
		MerchantID: ts.MerchantID,
		Name:       "revoked",
		RevokedAt:  &revokedAt,
	})

	unknown, err := security.GenerateAPIKey()
	require.NoError(t, err)

	tests := []struct {
		name string
		key  string
	}{
		{name: "missing credential", key: ""},
		{name: "unknown key", key: unknown.Raw},
		{name: "revoked key", key: revokedSecret},
		{name: "expired key", key: expiredSecret},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := ts.doJSON(t, http.MethodGet, unknownAuthorizationPath, tt.key, nil, nil)

			assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
			assert.Equal(t, `Bearer realm="bank-api"`, resp.Header.Get("WWW-Authenticate"))
			assert.Equal(t, "unauthorized", errorCodeFromResponse(t, resp))
		})
	}
}

func TestAuth_EnforcesAPIKeyScopes(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	_, paymentsOnly := seedAPIKey(t, ts, ts.MerchantID, "payments-only", 0, models.ScopePayments)
	_, adminOnly := seedAPIKey(t, ts, ts.MerchantID, "admin-only", 0, models.ScopeAdmin)

	t.Run("payments scope can read payment data", func(t *testing.T) {
		resp := ts.doJSON(t, http.MethodGet, unknownAuthorizationPath, paymentsOnly, nil, nil)

		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		assert.Equal(t, "not_found", errorCodeFromResponse(t, resp))
	})

	t.Run("payments scope cannot manage keys", func(t *testing.T) {
		resp := ts.doJSON(t, http.MethodGet, "/api/v1/api-keys", paymentsOnly, nil, nil)

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		assert.Equal(t, "forbidden", errorCodeFromResponse(t, resp))
	})

	t.Run("admin scope can list keys", func(t *testing.T) {
		resp := ts.doJSON(t, http.MethodGet, "/api/v1/api-keys", adminOnly, nil, nil)

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		resp.Body.Close()
	})

	t.Run("admin scope cannot read payment data", func(t *testing.T) {
		resp := ts.doJSON(t, http.MethodGet, unknownAuthorizationPath, adminOnly, nil, nil)

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		assert.Equal(t, "forbidden", errorCodeFromResponse(t, resp))
	})
}

func TestAuth_DisabledFallsBackToDefaultMerchant(t *testing.T) {
	ts := SetupTestWithConfig(t, func(cfg *config.Config) {
		cfg.Auth.Enabled = false
	})
	defer ts.Close()

	resp := ts.doJSON(t, http.MethodPost, "/api/v1/authorizations", "", map[string]any{
		"card_number": "4111111111111111",
		"cvv":         "123",
		"amount":      1000,
	}, map[string]string{"Idempotency-Key": "auth-disabled"})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body := decodeJSONBody(t, resp)
	assert.Equal(t, "approved", body["status"])

	var stored models.Transaction
	require.NoError(t, ts.Database.GORM().Order("created_at DESC").First(&stored).Error)
	assert.Equal(t, models.DefaultMerchantID, stored.MerchantID,
		"authentication-disabled traffic must still be owned by the default merchant")
}

func TestTenancy_ScopesPaymentDataToMerchant(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	merchantB := seedMerchant(t, ts, "tenant-b")
	_, keyB := seedAPIKey(t, ts, merchantB.ID, "tenant-b", 0, models.ScopePayments, models.ScopeAdmin)
	seedAccount(t, ts, merchantB.ID, "4000056655665556", "5556", 250000)

	resp := ts.authorizeWithKey(t, keyB, "4000056655665556", "321", 10000, "tenant-b-auth")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	authID := authorizationIDFromResponse(t, resp)

	// The owning tenant can read its own authorization.
	resp = ts.doJSON(t, http.MethodGet, "/api/v1/authorizations/"+authID, keyB, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Another tenant cannot observe it.
	resp = ts.doJSON(t, http.MethodGet, "/api/v1/authorizations/"+authID, ts.APIKey, nil, nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "not_found", errorCodeFromResponse(t, resp))

	// Another tenant cannot capture it.
	resp = ts.captureWithKey(t, ts.APIKey, authID, 5000, "cross-tenant-capture")
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "authorization_not_found", errorCodeFromResponse(t, resp))

	// Another tenant cannot authorize against its card: the account lookup is
	// scoped to the caller, so the card is indistinguishable from an unknown one.
	resp = ts.authorizeWithKey(t, ts.APIKey, "4000056655665556", "321", 1000, "cross-tenant-card")
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, "invalid_card", errorCodeFromResponse(t, resp))
}

func TestTenancy_SharedIdempotencyKeyIsIsolated(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	merchantB := seedMerchant(t, ts, "tenant-b")
	_, keyB := seedAPIKey(t, ts, merchantB.ID, "tenant-b", 0, models.ScopePayments)
	seedAccount(t, ts, merchantB.ID, "4000056655665556", "5556", 250000)

	const sharedKey = "shared-idempotency-key"

	resp := ts.authorizeWithKey(t, ts.APIKey, "4111111111111111", "123", 5000, sharedKey)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	authA := authorizationIDFromResponse(t, resp)

	resp = ts.authorizeWithKey(t, keyB, "4000056655665556", "321", 5000, sharedKey)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	authB := authorizationIDFromResponse(t, resp)

	assert.NotEqual(t, authA, authB,
		"a client-chosen idempotency key must not collide across tenants")

	resp = ts.doJSON(t, http.MethodGet, "/api/v1/authorizations/"+authB, ts.APIKey, nil, nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()

	resp = ts.doJSON(t, http.MethodGet, "/api/v1/authorizations/"+authA, keyB, nil, nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()
}

func TestTenancy_IsolatesAPIKeyManagement(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	merchantB := seedMerchant(t, ts, "tenant-b")
	keyB, keyBSecret := seedAPIKey(t, ts, merchantB.ID, "tenant-b-admin", 0,
		models.ScopePayments, models.ScopeAdmin)

	// The default merchant's listing never includes another tenant's key.
	resp := ts.doJSON(t, http.MethodGet, "/api/v1/api-keys", ts.APIKey, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := decodeJSONBody(t, resp)
	entries, ok := body["api_keys"].([]any)
	require.True(t, ok)
	for _, entry := range entries {
		item, ok := entry.(map[string]any)
		require.True(t, ok)
		assert.NotEqual(t, keyB.ID.String(), item["id"])
	}

	// Tenant B sees exactly its own key.
	resp = ts.doJSON(t, http.MethodGet, "/api/v1/api-keys", keyBSecret, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body = decodeJSONBody(t, resp)
	entries, ok = body["api_keys"].([]any)
	require.True(t, ok)
	require.Len(t, entries, 1)
	ownKey, ok := entries[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, keyB.ID.String(), ownKey["id"])

	// Cross-tenant rotation and revocation are reported as missing, not denied,
	// so a tenant cannot probe for the existence of another tenant's key.
	resp = ts.doJSON(t, http.MethodPost, "/api/v1/api-keys/"+keyB.ID.String()+"/rotate", ts.APIKey, nil, nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "not_found", errorCodeFromResponse(t, resp))

	resp = ts.doJSON(t, http.MethodDelete, "/api/v1/api-keys/"+keyB.ID.String(), ts.APIKey, nil, nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "not_found", errorCodeFromResponse(t, resp))

	// The rejected mutations left tenant B's key intact.
	resp = ts.doJSON(t, http.MethodGet, unknownAuthorizationPath, keyBSecret, nil, nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "not_found", errorCodeFromResponse(t, resp))
}

func TestRateLimit_RejectsRequestsAboveDefaultLimit(t *testing.T) {
	ts := SetupTestWithConfig(t, func(cfg *config.Config) {
		cfg.RateLimit.Enabled = true
		cfg.RateLimit.DefaultPerMinute = 2
	})
	defer ts.Close()

	for i := 0; i < 2; i++ {
		resp := ts.doJSON(t, http.MethodGet, unknownAuthorizationPath, ts.APIKey, nil, nil)
		require.Equal(t, http.StatusNotFound, resp.StatusCode)
		resp.Body.Close()
	}

	resp := ts.doJSON(t, http.MethodGet, unknownAuthorizationPath, ts.APIKey, nil, nil)
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)

	retryAfter := resp.Header.Get("Retry-After")
	require.NotEmpty(t, retryAfter)
	seconds, err := strconv.Atoi(retryAfter)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, seconds, 1)
	assert.Equal(t, "rate_limited", errorCodeFromResponse(t, resp))
}

func TestRateLimit_KeyLimitOverridesDefault(t *testing.T) {
	ts := SetupTestWithConfig(t, func(cfg *config.Config) {
		cfg.RateLimit.Enabled = true
		cfg.RateLimit.DefaultPerMinute = 100
	})
	defer ts.Close()

	_, tinyLimit := seedAPIKey(t, ts, ts.MerchantID, "tiny-limit", 1, models.ScopePayments)

	resp := ts.doJSON(t, http.MethodGet, unknownAuthorizationPath, tinyLimit, nil, nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()

	resp = ts.doJSON(t, http.MethodGet, unknownAuthorizationPath, tinyLimit, nil, nil)
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Equal(t, "rate_limited", errorCodeFromResponse(t, resp))
}

func TestRateLimit_ExemptsOperationalEndpoints(t *testing.T) {
	ts := SetupTestWithConfig(t, func(cfg *config.Config) {
		cfg.RateLimit.Enabled = true
		cfg.RateLimit.DefaultPerMinute = 1
	})
	defer ts.Close()

	for i := 0; i < 3; i++ {
		resp := ts.doJSON(t, http.MethodGet, "/health", "", nil, nil)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		resp.Body.Close()
	}
}

func TestAPIKeyManagement_LifecycleOverHTTP(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	// Unauthenticated callers cannot manage keys.
	resp := ts.doJSON(t, http.MethodGet, "/api/v1/api-keys", "", nil, nil)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, "unauthorized", errorCodeFromResponse(t, resp))

	// Invalid key definitions are rejected before persistence.
	resp = ts.doJSON(t, http.MethodPost, "/api/v1/api-keys", ts.APIKey, map[string]any{"name": "  "}, nil)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, "invalid_request", errorCodeFromResponse(t, resp))

	// Issue a key. The raw secret is returned exactly once.
	resp = ts.doJSON(t, http.MethodPost, "/api/v1/api-keys", ts.APIKey, map[string]any{
		"name": "checkout-service",
	}, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	created := decodeJSONBody(t, resp)
	secret, ok := created["secret"].(string)
	require.True(t, ok)
	require.NotEmpty(t, secret)

	keyObject, ok := created["api_key"].(map[string]any)
	require.True(t, ok)
	keyID, ok := keyObject["id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, keyID)
	assert.Equal(t, "checkout-service", keyObject["name"])
	assert.NotContains(t, keyObject, "secret")
	assert.Contains(t, keyObject["scopes"], "payments")

	// The new key authenticates payment traffic.
	resp = ts.authorizeWithKey(t, secret, "4111111111111111", "123", 2500, "created-key-auth")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Listings never expose secrets.
	resp = ts.doJSON(t, http.MethodGet, "/api/v1/api-keys", ts.APIKey, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	listed := decodeJSONBody(t, resp)
	encoded, err := json.Marshal(listed)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), secret)

	entries, ok := listed["api_keys"].([]any)
	require.True(t, ok)
	found := false
	for _, entry := range entries {
		item, ok := entry.(map[string]any)
		require.True(t, ok)
		assert.NotContains(t, item, "secret")
		if item["id"] == keyID {
			found = true
		}
	}
	assert.True(t, found, "created key should appear in the listing")

	// Rotation replaces the secret while preserving the key identity.
	resp = ts.doJSON(t, http.MethodPost, "/api/v1/api-keys/"+keyID+"/rotate", ts.APIKey, nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	rotated := decodeJSONBody(t, resp)
	rotatedSecret, ok := rotated["secret"].(string)
	require.True(t, ok)
	require.NotEmpty(t, rotatedSecret)
	assert.NotEqual(t, secret, rotatedSecret)

	rotatedKey, ok := rotated["api_key"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, keyID, rotatedKey["id"])

	// The previous secret stops working and the replacement works.
	resp = ts.doJSON(t, http.MethodGet, unknownAuthorizationPath, secret, nil, nil)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()

	resp = ts.authorizeWithKey(t, rotatedSecret, "4111111111111111", "123", 2500, "rotated-key-auth")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// Revocation blocks the key permanently and cannot be repeated.
	resp = ts.doJSON(t, http.MethodDelete, "/api/v1/api-keys/"+keyID, ts.APIKey, nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()

	resp = ts.doJSON(t, http.MethodGet, unknownAuthorizationPath, rotatedSecret, nil, nil)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()

	resp = ts.doJSON(t, http.MethodDelete, "/api/v1/api-keys/"+keyID, ts.APIKey, nil, nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "not_found", errorCodeFromResponse(t, resp))

	// Rotating an unknown key is reported as missing as well.
	resp = ts.doJSON(t, http.MethodPost, "/api/v1/api-keys/"+uuid.NewString()+"/rotate", ts.APIKey, nil, nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "not_found", errorCodeFromResponse(t, resp))
}
