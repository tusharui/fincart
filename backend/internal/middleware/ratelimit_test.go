package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tusharui/payment-gateway/backend/internal/api"
	"github.com/tusharui/payment-gateway/backend/internal/auth"
	"github.com/tusharui/payment-gateway/backend/internal/models"
)

// stubLimiter records the bucket key and limit it was asked about and returns a
// canned decision.
type stubLimiter struct {
	allowed    bool
	retryAfter time.Duration
	calls      int
	gotKey     uuid.UUID
	gotLimit   int
}

func (s *stubLimiter) Allow(key uuid.UUID, limitPerMinute int) (bool, time.Duration) {
	s.calls++
	s.gotKey = key
	s.gotLimit = limitPerMinute

	return s.allowed, s.retryAfter
}

func requestWithPrincipal(t *testing.T, method, path string, principal auth.Principal) *http.Request {
	t.Helper()

	req := httptest.NewRequest(method, path, nil)

	return req.WithContext(auth.WithPrincipal(req.Context(), principal))
}

func TestRateLimit_AllowsRequestUnderLimit(t *testing.T) {
	limiter := &stubLimiter{allowed: true}
	called := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	principal := auth.Principal{
		MerchantID:         models.DefaultMerchantID,
		APIKeyID:           uuid.New(),
		RateLimitPerMinute: 60,
	}
	req := requestWithPrincipal(t, http.MethodPost, "/api/v1/authorizations", principal)
	rec := httptest.NewRecorder()

	RateLimit(limiter, true, 120, testLogger())(handler).ServeHTTP(rec, req)

	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, limiter.calls)
	assert.Equal(t, principal.APIKeyID, limiter.gotKey)
	assert.Equal(t, 60, limiter.gotLimit, "the key's own limit wins over the default")
}

func TestRateLimit_FallsBackToDefaultLimit(t *testing.T) {
	limiter := &stubLimiter{allowed: true}

	principal := auth.Principal{MerchantID: models.DefaultMerchantID, APIKeyID: uuid.New()}
	req := requestWithPrincipal(t, http.MethodGet, "/api/v1/authorizations", principal)
	rec := httptest.NewRecorder()

	RateLimit(limiter, true, 45, testLogger())(testHandler(http.StatusOK, `{}`)).ServeHTTP(rec, req)

	require.Equal(t, 1, limiter.calls)
	assert.Equal(t, 45, limiter.gotLimit)
}

func TestRateLimit_ZeroDefaultIsUnlimited(t *testing.T) {
	limiter := &stubLimiter{allowed: false}
	called := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	principal := auth.Principal{MerchantID: models.DefaultMerchantID, APIKeyID: uuid.New()}
	req := requestWithPrincipal(t, http.MethodGet, "/api/v1/authorizations", principal)
	rec := httptest.NewRecorder()

	RateLimit(limiter, true, 0, testLogger())(handler).ServeHTTP(rec, req)

	assert.True(t, called, "an unlimited default must bypass the limiter")
	assert.Zero(t, limiter.calls)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRateLimit_RejectsWithRetryAfter(t *testing.T) {
	limiter := &stubLimiter{allowed: false, retryAfter: 2 * time.Second}
	called := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	principal := auth.Principal{
		MerchantID:         models.DefaultMerchantID,
		APIKeyID:           uuid.New(),
		RateLimitPerMinute: 10,
	}
	req := requestWithPrincipal(t, http.MethodPost, "/api/v1/captures", principal)
	rec := httptest.NewRecorder()

	RateLimit(limiter, true, 120, testLogger())(handler).ServeHTTP(rec, req)

	assert.False(t, called, "a throttled request must not reach the handler")
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "2", rec.Header().Get("Retry-After"))
	assert.Equal(t, string(api.ErrorCodeRateLimited), responseErrorCode(t, rec))
}

func TestRateLimit_RetryAfterNeverReportsZero(t *testing.T) {
	limiter := &stubLimiter{allowed: false, retryAfter: 0}
	principal := auth.Principal{MerchantID: models.DefaultMerchantID, APIKeyID: uuid.New()}
	req := requestWithPrincipal(t, http.MethodGet, "/api/v1/authorizations", principal)
	rec := httptest.NewRecorder()

	RateLimit(limiter, true, 120, testLogger())(testHandler(http.StatusOK, `{}`)).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "1", rec.Header().Get("Retry-After"))
}

func TestRateLimit_DisabledIsPassThrough(t *testing.T) {
	limiter := &stubLimiter{allowed: false}
	called := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	// No principal either: disabled means the limiter is not consulted at all.
	rec := httptest.NewRecorder()
	RateLimit(limiter, false, 120, testLogger())(handler).ServeHTTP(
		rec, httptest.NewRequest(http.MethodPost, "/api/v1/authorizations", nil))

	assert.True(t, called)
	assert.Zero(t, limiter.calls)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRateLimit_ExemptPathsAreNotThrottled(t *testing.T) {
	limiter := &stubLimiter{allowed: false}

	for _, path := range []string{"/health", "/ready", "/metrics", "/docs"} {
		rec := httptest.NewRecorder()
		RateLimit(limiter, true, 120, testLogger())(testHandler(http.StatusOK, `{}`)).ServeHTTP(
			rec, httptest.NewRequest(http.MethodGet, path, nil))

		assert.Equal(t, http.StatusOK, rec.Code, path)
	}

	assert.Zero(t, limiter.calls)
}

func TestRateLimit_MissingPrincipalIsUnauthorized(t *testing.T) {
	limiter := &stubLimiter{allowed: true}
	called := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	RateLimit(limiter, true, 120, testLogger())(handler).ServeHTTP(
		rec, httptest.NewRequest(http.MethodGet, "/api/v1/authorizations", nil))

	assert.False(t, called)
	assert.Zero(t, limiter.calls)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, string(api.ErrorCodeUnauthorized), responseErrorCode(t, rec))
}

func TestRateLimit_DisabledAuthBucketsByMerchant(t *testing.T) {
	limiter := &stubLimiter{allowed: true}
	principal := auth.Principal{MerchantID: models.DefaultMerchantID}
	req := requestWithPrincipal(t, http.MethodGet, "/api/v1/authorizations", principal)
	rec := httptest.NewRecorder()

	RateLimit(limiter, true, 120, testLogger())(testHandler(http.StatusOK, `{}`)).ServeHTTP(rec, req)

	require.Equal(t, 1, limiter.calls)
	assert.Equal(t, models.DefaultMerchantID, limiter.gotKey, "without a key ID the tenant owns the bucket")
}
