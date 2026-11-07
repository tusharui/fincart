package middleware

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/tusharui/payment-gateway/backend/internal/api"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"github.com/tusharui/payment-gateway/backend/internal/service/mocks"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testHandler(status int, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body)) //nolint:errcheck // test helper
	})
}

func responseErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	var body struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), "response should be JSON")

	return body.Error
}

func newIdempotentRequest(t *testing.T, path, key, body string) *http.Request {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}

	return req
}

func TestIdempotency_GETRequestsBypassed(t *testing.T) {
	repo := mocks.NewMockIdempotencyRepository(t)
	middleware := Idempotency(repo, testLogger())

	handlerCalled := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/authorizations", nil)
	req.Header.Set("Idempotency-Key", "test-key")
	rec := httptest.NewRecorder()

	middleware(handler).ServeHTTP(rec, req)

	assert.True(t, handlerCalled, "handler should be called for GET requests")
	repo.AssertNotCalled(t, "Get")
	repo.AssertNotCalled(t, "Claim")
}

func TestIdempotency_NonIdempotentPathBypassed(t *testing.T) {
	repo := mocks.NewMockIdempotencyRepository(t)
	middleware := Idempotency(repo, testLogger())

	handlerCalled := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/other", nil)
	req.Header.Set("Idempotency-Key", "test-key")
	rec := httptest.NewRecorder()

	middleware(handler).ServeHTTP(rec, req)

	assert.True(t, handlerCalled, "handler should be called for non-idempotent paths")
	repo.AssertNotCalled(t, "Get")
	repo.AssertNotCalled(t, "Claim")
}

func TestIdempotency_MissingKeyPassesThrough(t *testing.T) {
	repo := mocks.NewMockIdempotencyRepository(t)
	middleware := Idempotency(repo, testLogger())

	handlerCalled := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/authorizations", nil)
	// No Idempotency-Key header: the strict server returns the structured error.
	rec := httptest.NewRecorder()

	middleware(handler).ServeHTTP(rec, req)

	assert.True(t, handlerCalled, "handler should be called without idempotency key")
	repo.AssertNotCalled(t, "Get")
	repo.AssertNotCalled(t, "Claim")
}

func TestIdempotency_KeyTooLongRejected(t *testing.T) {
	repo := mocks.NewMockIdempotencyRepository(t)
	middleware := Idempotency(repo, testLogger())

	handlerCalled := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
	})

	req := newIdempotentRequest(t, "/api/v1/authorizations", strings.Repeat("k", 256), `{}`)
	rec := httptest.NewRecorder()

	middleware(handler).ServeHTTP(rec, req)

	assert.False(t, handlerCalled, "handler should not be called for an invalid key")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, string(api.ErrorCodeInvalidIdempotencyKey), responseErrorCode(t, rec))
	repo.AssertNotCalled(t, "Get")
	repo.AssertNotCalled(t, "Claim")
}

func TestIdempotency_BodyTooLargeRejected(t *testing.T) {
	repo := mocks.NewMockIdempotencyRepository(t)
	middleware := Idempotency(repo, testLogger())

	handlerCalled := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
	})

	req := newIdempotentRequest(t, "/api/v1/authorizations", "large-body-key",
		strings.Repeat("a", maxIdempotencyBodyBytes+1))
	rec := httptest.NewRecorder()

	middleware(handler).ServeHTTP(rec, req)

	assert.False(t, handlerCalled, "handler should not be called for an oversized body")
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Equal(t, string(api.ErrorCodeRequestTooLarge), responseErrorCode(t, rec))
	repo.AssertNotCalled(t, "Get")
	repo.AssertNotCalled(t, "Claim")
}

func TestIdempotency_FirstRequestClaimedAndCached(t *testing.T) {
	const (
		key  = "unique-key-123"
		path = "/api/v1/authorizations"
		body = `{"amount":100}`
		resp = `{"status":"success"}`
	)

	repo := mocks.NewMockIdempotencyRepository(t)
	repo.On("Get", mock.Anything, key, path).Return(nil, nil)
	repo.On("Claim", mock.Anything, key, path, hashRequestBody([]byte(body))).Return(true, nil)
	repo.On("Complete", mock.Anything, key, path, hashRequestBody([]byte(body)), http.StatusOK, resp).Return(nil)

	middleware := Idempotency(repo, testLogger())
	handler := testHandler(http.StatusOK, resp)

	rec := httptest.NewRecorder()
	middleware(handler).ServeHTTP(rec, newIdempotentRequest(t, path, key, body))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, resp, rec.Body.String())
	assert.Empty(t, rec.Header().Get("X-Idempotent-Replayed"), "first request should not have replay header")

	repo.AssertCalled(t, "Claim", mock.Anything, key, path, hashRequestBody([]byte(body)))
	repo.AssertCalled(t, "Complete", mock.Anything, key, path, hashRequestBody([]byte(body)), http.StatusOK, resp)
	repo.AssertNotCalled(t, "Release")
}

func TestIdempotency_SecondRequestReturnsCached(t *testing.T) {
	const (
		key  = "duplicate-key"
		path = "/api/v1/authorizations"
		body = `{"amount":100}`
	)

	cached := &models.IdempotencyKey{
		Key:            key,
		RequestPath:    path,
		RequestHash:    hashRequestBody([]byte(body)),
		ResponseStatus: http.StatusOK,
		ResponseBody:   `{"call":1}`,
	}
	repo := mocks.NewMockIdempotencyRepository(t)
	repo.On("Get", mock.Anything, key, path).Return(cached, nil)

	middleware := Idempotency(repo, testLogger())

	callCount := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	middleware(handler).ServeHTTP(rec, newIdempotentRequest(t, path, key, body))

	assert.Equal(t, 0, callCount, "handler should not be called when cached")
	assert.Equal(t, "true", rec.Header().Get("X-Idempotent-Replayed"))
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, `{"call":1}`, rec.Body.String())
	assert.Equal(t, http.StatusOK, rec.Code)
	repo.AssertNotCalled(t, "Claim")
}

func TestIdempotency_LegacyCacheEntryWithoutHashIsReplayed(t *testing.T) {
	const (
		key  = "legacy-key"
		path = "/api/v1/authorizations"
		body = `{"amount":100}`
	)

	cached := &models.IdempotencyKey{
		Key:            key,
		RequestPath:    path,
		RequestHash:    "",
		ResponseStatus: http.StatusOK,
		ResponseBody:   `{"legacy":true}`,
	}
	repo := mocks.NewMockIdempotencyRepository(t)
	repo.On("Get", mock.Anything, key, path).Return(cached, nil)

	middleware := Idempotency(repo, testLogger())
	handler := testHandler(http.StatusInternalServerError, `{"should":"not run"}`)

	rec := httptest.NewRecorder()
	middleware(handler).ServeHTTP(rec, newIdempotentRequest(t, path, key, body))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, `{"legacy":true}`, rec.Body.String())
	assert.Equal(t, "true", rec.Header().Get("X-Idempotent-Replayed"))
}

func TestIdempotency_KeyReuseWithDifferentBodyRejected(t *testing.T) {
	const (
		key  = "reused-key"
		path = "/api/v1/authorizations"
	)

	cached := &models.IdempotencyKey{
		Key:            key,
		RequestPath:    path,
		RequestHash:    hashRequestBody([]byte(`{"amount":100}`)),
		ResponseStatus: http.StatusOK,
		ResponseBody:   `{"status":"success"}`,
	}
	repo := mocks.NewMockIdempotencyRepository(t)
	repo.On("Get", mock.Anything, key, path).Return(cached, nil)

	middleware := Idempotency(repo, testLogger())

	handlerCalled := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
	})

	rec := httptest.NewRecorder()
	middleware(handler).ServeHTTP(rec, newIdempotentRequest(t, path, key, `{"amount":200}`))

	assert.False(t, handlerCalled, "handler must not run for a conflicting payload")
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, string(api.ErrorCodeIdempotencyKeyConflict), responseErrorCode(t, rec))
	repo.AssertNotCalled(t, "Claim")
}

func TestIdempotency_PendingClaimReturnsConflict(t *testing.T) {
	const (
		key  = "pending-key"
		path = "/api/v1/authorizations"
		body = `{"amount":100}`
	)

	cached := &models.IdempotencyKey{
		Key:            key,
		RequestPath:    path,
		RequestHash:    hashRequestBody([]byte(body)),
		ResponseStatus: models.IdempotencyStatusPending,
	}
	repo := mocks.NewMockIdempotencyRepository(t)
	repo.On("Get", mock.Anything, key, path).Return(cached, nil)

	middleware := Idempotency(repo, testLogger())

	rec := httptest.NewRecorder()
	middleware(testHandler(http.StatusOK, `{}`)).ServeHTTP(rec, newIdempotentRequest(t, path, key, body))

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, string(api.ErrorCodeIdempotencyInProgress), responseErrorCode(t, rec))
	assert.Equal(t, "1", rec.Header().Get("Retry-After"))
}

func TestIdempotency_LostClaimRaceReplaysWinnerResponse(t *testing.T) {
	const (
		key  = "race-key"
		path = "/api/v1/authorizations"
		body = `{"amount":100}`
	)

	cached := &models.IdempotencyKey{
		Key:            key,
		RequestPath:    path,
		RequestHash:    hashRequestBody([]byte(body)),
		ResponseStatus: http.StatusOK,
		ResponseBody:   `{"winner":true}`,
	}
	repo := mocks.NewMockIdempotencyRepository(t)
	repo.On("Get", mock.Anything, key, path).Return(nil, nil).Once()
	repo.On("Get", mock.Anything, key, path).Return(cached, nil).Once()
	repo.On("Claim", mock.Anything, key, path, mock.Anything).Return(false, nil)

	middleware := Idempotency(repo, testLogger())

	handlerCalled := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
	})

	rec := httptest.NewRecorder()
	middleware(handler).ServeHTTP(rec, newIdempotentRequest(t, path, key, body))

	assert.False(t, handlerCalled, "losing request must not execute")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, `{"winner":true}`, rec.Body.String())
	assert.Equal(t, "true", rec.Header().Get("X-Idempotent-Replayed"))
}

func TestIdempotency_LostClaimRaceWithoutResultReturnsConflict(t *testing.T) {
	const (
		key  = "race-key-2"
		path = "/api/v1/authorizations"
		body = `{"amount":100}`
	)

	repo := mocks.NewMockIdempotencyRepository(t)
	repo.On("Get", mock.Anything, key, path).Return(nil, nil).Twice()
	repo.On("Claim", mock.Anything, key, path, mock.Anything).Return(false, nil)

	middleware := Idempotency(repo, testLogger())

	rec := httptest.NewRecorder()
	middleware(testHandler(http.StatusOK, `{}`)).ServeHTTP(rec, newIdempotentRequest(t, path, key, body))

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, string(api.ErrorCodeIdempotencyInProgress), responseErrorCode(t, rec))
}

func TestIdempotency_RepoGetErrorFailsClosed(t *testing.T) {
	const (
		key  = "db-down-key"
		path = "/api/v1/authorizations"
	)

	repo := mocks.NewMockIdempotencyRepository(t)
	repo.On("Get", mock.Anything, key, path).Return(nil, errors.New("database connection failed"))

	middleware := Idempotency(repo, testLogger())

	handlerCalled := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
	})

	rec := httptest.NewRecorder()
	middleware(handler).ServeHTTP(rec, newIdempotentRequest(t, path, key, `{"amount":100}`))

	assert.False(t, handlerCalled, "handler must not run when idempotency state is unavailable")
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, string(api.ErrorCodeIdempotencyUnavailable), responseErrorCode(t, rec))
}

func TestIdempotency_ClaimErrorFailsClosed(t *testing.T) {
	const (
		key  = "claim-error-key"
		path = "/api/v1/authorizations"
	)

	repo := mocks.NewMockIdempotencyRepository(t)
	repo.On("Get", mock.Anything, key, path).Return(nil, nil)
	repo.On("Claim", mock.Anything, key, path, mock.Anything).Return(false, errors.New("insert failed"))

	middleware := Idempotency(repo, testLogger())

	rec := httptest.NewRecorder()
	middleware(testHandler(http.StatusOK, `{}`)).ServeHTTP(rec, newIdempotentRequest(t, path, key, `{"amount":100}`))

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, string(api.ErrorCodeIdempotencyUnavailable), responseErrorCode(t, rec))
}

func TestIdempotency_5xxResponsesAreReleasedNotCached(t *testing.T) {
	const (
		key  = "error-key"
		path = "/api/v1/authorizations"
		body = `{"amount":100}`
	)

	repo := mocks.NewMockIdempotencyRepository(t)
	repo.On("Get", mock.Anything, key, path).Return(nil, nil)
	repo.On("Claim", mock.Anything, key, path, hashRequestBody([]byte(body))).Return(true, nil)
	repo.On("Release", mock.Anything, key, path, hashRequestBody([]byte(body))).Return(nil)

	middleware := Idempotency(repo, testLogger())
	handler := testHandler(http.StatusInternalServerError, `{"error":"server error"}`)

	rec := httptest.NewRecorder()
	middleware(handler).ServeHTTP(rec, newIdempotentRequest(t, path, key, body))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	repo.AssertNotCalled(t, "Complete")
	repo.AssertCalled(t, "Release", mock.Anything, key, path, hashRequestBody([]byte(body)))
}

func TestIdempotency_4xxResponsesAreReleasedNotCached(t *testing.T) {
	const (
		key  = "bad-request-key"
		path = "/api/v1/authorizations"
		body = `{"amount":100}`
	)

	repo := mocks.NewMockIdempotencyRepository(t)
	repo.On("Get", mock.Anything, key, path).Return(nil, nil)
	repo.On("Claim", mock.Anything, key, path, hashRequestBody([]byte(body))).Return(true, nil)
	repo.On("Release", mock.Anything, key, path, hashRequestBody([]byte(body))).Return(nil)

	middleware := Idempotency(repo, testLogger())
	handler := testHandler(http.StatusBadRequest, `{"error":"bad request"}`)

	rec := httptest.NewRecorder()
	middleware(handler).ServeHTTP(rec, newIdempotentRequest(t, path, key, body))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	repo.AssertNotCalled(t, "Complete")
	repo.AssertCalled(t, "Release", mock.Anything, key, path, hashRequestBody([]byte(body)))
}

func TestIdempotency_CompleteErrorDoesNotAffectResponse(t *testing.T) {
	const (
		key  = "store-error-key"
		path = "/api/v1/authorizations"
		body = `{"amount":100}`
		resp = `{"status":"success"}`
	)

	repo := mocks.NewMockIdempotencyRepository(t)
	repo.On("Get", mock.Anything, key, path).Return(nil, nil)
	repo.On("Claim", mock.Anything, key, path, hashRequestBody([]byte(body))).Return(true, nil)
	repo.On("Complete", mock.Anything, key, path, hashRequestBody([]byte(body)), http.StatusOK, resp).
		Return(errors.New("failed to store"))

	middleware := Idempotency(repo, testLogger())
	handler := testHandler(http.StatusOK, resp)

	rec := httptest.NewRecorder()
	middleware(handler).ServeHTTP(rec, newIdempotentRequest(t, path, key, body))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, resp, rec.Body.String())
}

func TestIdempotency_RequestBodyIsRestoredForHandler(t *testing.T) {
	const (
		key  = "body-key"
		path = "/api/v1/authorizations"
		body = `{"amount":100}`
	)

	repo := mocks.NewMockIdempotencyRepository(t)
	repo.On("Get", mock.Anything, key, path).Return(nil, nil)
	repo.On("Claim", mock.Anything, key, path, hashRequestBody([]byte(body))).Return(true, nil)
	repo.On("Complete", mock.Anything, key, path, mock.Anything, http.StatusOK, body).Return(nil)

	var received string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		read, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		received = string(read)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(read) //nolint:errcheck // test helper
	})

	middleware := Idempotency(repo, testLogger())
	rec := httptest.NewRecorder()
	middleware(handler).ServeHTTP(rec, newIdempotentRequest(t, path, key, body))

	assert.Equal(t, body, received, "handler should receive the original body")
	assert.Equal(t, body, rec.Body.String())
}

func TestIdempotency_AllIdempotentPaths(t *testing.T) {
	paths := []string{
		"/api/v1/authorizations",
		"/api/v1/captures",
		"/api/v1/voids",
		"/api/v1/refunds",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			const (
				key  = "test-key"
				body = `{"amount":100}`
			)

			repo := mocks.NewMockIdempotencyRepository(t)
			repo.On("Get", mock.Anything, key, path).Return(nil, nil)
			repo.On("Claim", mock.Anything, key, path, hashRequestBody([]byte(body))).Return(true, nil)
			repo.On("Complete", mock.Anything, key, path, mock.Anything, http.StatusOK, mock.Anything).Return(nil)

			middleware := Idempotency(repo, testLogger())
			handler := testHandler(http.StatusOK, `{"status":"ok"}`)

			rec := httptest.NewRecorder()
			middleware(handler).ServeHTTP(rec, newIdempotentRequest(t, path, key, body))

			repo.AssertCalled(t, "Claim", mock.Anything, key, path, hashRequestBody([]byte(body)))
			repo.AssertCalled(t, "Complete", mock.Anything, key, path, mock.Anything, http.StatusOK, mock.Anything)
		})
	}
}

func TestIdempotency_SameKeyDifferentPathsAreSeparate(t *testing.T) {
	const (
		key  = "shared-key"
		body = `{"amount":100}`
	)

	repo := mocks.NewMockIdempotencyRepository(t)
	repo.On("Get", mock.Anything, key, mock.Anything).Return(nil, nil)
	repo.On("Claim", mock.Anything, key, mock.Anything, mock.Anything).Return(true, nil)
	repo.On("Complete", mock.Anything, key, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil)

	middleware := Idempotency(repo, testLogger())

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"path":"` + r.URL.Path + `"}`)) //nolint:errcheck // test helper
	})

	rec1 := httptest.NewRecorder()
	middleware(handler).ServeHTTP(rec1, newIdempotentRequest(t, "/api/v1/authorizations", key, body))

	rec2 := httptest.NewRecorder()
	middleware(handler).ServeHTTP(rec2, newIdempotentRequest(t, "/api/v1/captures", key, body))

	assert.Contains(t, rec1.Body.String(), "authorizations")
	assert.Contains(t, rec2.Body.String(), "captures")

	repo.AssertCalled(t, "Get", mock.Anything, key, "/api/v1/authorizations")
	repo.AssertCalled(t, "Get", mock.Anything, key, "/api/v1/captures")
}

func TestIdempotency_TrailingSlashIsNormalized(t *testing.T) {
	const (
		key  = "slash-key"
		body = `{"amount":100}`
	)

	cached := &models.IdempotencyKey{
		Key:            key,
		RequestPath:    "/api/v1/authorizations",
		RequestHash:    hashRequestBody([]byte(body)),
		ResponseStatus: http.StatusOK,
		ResponseBody:   `{"cached":true}`,
	}
	repo := mocks.NewMockIdempotencyRepository(t)
	repo.On("Get", mock.Anything, key, "/api/v1/authorizations").Return(cached, nil)

	middleware := Idempotency(repo, testLogger())
	rec := httptest.NewRecorder()
	middleware(testHandler(http.StatusOK, `{}`)).ServeHTTP(rec,
		newIdempotentRequest(t, "/api/v1/authorizations/", key, body))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, `{"cached":true}`, rec.Body.String())
}
