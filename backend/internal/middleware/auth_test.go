package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tusharui/payment-gateway/backend/internal/api"
	"github.com/tusharui/payment-gateway/backend/internal/auth"
	"github.com/tusharui/payment-gateway/backend/internal/models"
)

type stubAuthenticator struct {
	principal *auth.Principal
	err       error
	called    bool
	received  string
}

func (s *stubAuthenticator) Authenticate(_ context.Context, rawKey string) (*auth.Principal, error) {
	s.called = true
	s.received = rawKey
	return s.principal, s.err
}

func TestAPIKeyAuth_DisabledUsesDefaultMerchant(t *testing.T) {
	authenticator := &stubAuthenticator{}
	called := false

	handler := APIKeyAuth(authenticator, false, testLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		principal, ok := auth.PrincipalFrom(r.Context())
		require.True(t, ok)
		assert.Equal(t, models.DefaultMerchantID, principal.MerchantID)
		assert.True(t, principal.HasScope(models.ScopePayments))
		assert.True(t, principal.HasScope(models.ScopeAdmin))
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/authorizations", nil))

	assert.True(t, called)
	assert.False(t, authenticator.called)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAPIKeyAuth_ExemptPathsBypassAuthentication(t *testing.T) {
	authenticator := &stubAuthenticator{}

	handler := APIKeyAuth(authenticator, true, testLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, path := range []string{"/", "/health", "/ready", "/metrics", "/docs", "/docs/openapi"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			assert.Equal(t, http.StatusOK, rec.Code)
		})
	}

	assert.False(t, authenticator.called)
}

func TestAPIKeyAuth_RequiresBearerCredential(t *testing.T) {
	tests := []struct {
		name   string
		header string
	}{
		{name: "missing", header: ""},
		{name: "wrong scheme", header: "Basic abc123"},
		{name: "empty token", header: "Bearer "},
		{name: "no separator", header: "Bearertoken"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authenticator := &stubAuthenticator{}
			handler := APIKeyAuth(authenticator, true, testLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatal("handler must not run without a valid credential")
			}))

			req := httptest.NewRequest(http.MethodGet, "/api/v1/authorizations", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.Contains(t, rec.Header().Get("WWW-Authenticate"), "Bearer")
			assert.Equal(t, string(api.ErrorCodeUnauthorized), responseErrorCode(t, rec))
			assert.False(t, authenticator.called)
		})
	}
}

func TestAPIKeyAuth_InvalidKeyIsUnauthorized(t *testing.T) {
	authenticator := &stubAuthenticator{err: auth.ErrInvalidAPIKey}

	handler := APIKeyAuth(authenticator, true, testLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run for an invalid key")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/refunds", nil)
	req.Header.Set("Authorization", "Bearer bk_invalid")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, string(api.ErrorCodeUnauthorized), responseErrorCode(t, rec))
	assert.Equal(t, "bk_invalid", authenticator.received)
}

func TestAPIKeyAuth_RepositoryFailureIsUnavailable(t *testing.T) {
	authenticator := &stubAuthenticator{err: errors.New("database down")}
	logger, _ := newBufferedLogger(slog.LevelError)

	handler := APIKeyAuth(authenticator, true, logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run when authentication is unavailable")
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/captures", nil)
	req.Header.Set("Authorization", "Bearer bk_valid")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, string(api.ErrorCodeServiceUnavailable), responseErrorCode(t, rec))
}

func TestAPIKeyAuth_ValidKeySetsPrincipal(t *testing.T) {
	principal := &auth.Principal{
		MerchantID: models.DefaultMerchantID,
		Scopes:     []string{models.ScopePayments},
	}
	authenticator := &stubAuthenticator{principal: principal}

	handler := APIKeyAuth(authenticator, true, testLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stored, ok := auth.PrincipalFrom(r.Context())
		require.True(t, ok)
		assert.Equal(t, principal.MerchantID, stored.MerchantID)
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/authorizations", nil)
	req.Header.Set("Authorization", "bearer bk_valid")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "bk_valid", authenticator.received)
}

func TestAPIKeyAuth_EnforcesScopes(t *testing.T) {
	payments := &auth.Principal{MerchantID: models.DefaultMerchantID, Scopes: []string{models.ScopePayments}}
	admin := &auth.Principal{MerchantID: models.DefaultMerchantID, Scopes: []string{models.ScopeAdmin}}

	tests := []struct {
		name       string
		principal  *auth.Principal
		path       string
		wantStatus int
	}{
		{
			name:       "payments key can call payment endpoints",
			principal:  payments,
			path:       "/api/v1/authorizations",
			wantStatus: http.StatusOK,
		},
		{
			name:       "admin key cannot call payment endpoints without payments scope",
			principal:  admin,
			path:       "/api/v1/authorizations",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "payments key cannot manage api keys",
			principal:  payments,
			path:       "/api/v1/api-keys",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "admin key can manage api keys",
			principal:  admin,
			path:       "/api/v1/api-keys",
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authenticator := &stubAuthenticator{principal: tt.principal}
			handler := APIKeyAuth(authenticator, true, testLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Header.Set("Authorization", "Bearer bk_valid")

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
			if tt.wantStatus == http.StatusForbidden {
				assert.Equal(t, string(api.ErrorCodeForbidden), responseErrorCode(t, rec))
			}
		})
	}
}

func TestBearerToken(t *testing.T) {
	tests := []struct {
		name   string
		header string
		token  string
		ok     bool
	}{
		{name: "valid", header: "Bearer abc", token: "abc", ok: true},
		{name: "case insensitive scheme", header: "bearer abc", token: "abc", ok: true},
		{name: "trims token", header: "Bearer  abc  ", token: "abc", ok: true},
		{name: "missing", header: "", ok: false},
		{name: "basic", header: "Basic abc", ok: false},
		{name: "empty", header: "Bearer ", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", tt.header)

			token, ok := bearerToken(req)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.token, token)
		})
	}
}

func TestRequiredScope(t *testing.T) {
	assert.Equal(t, models.ScopePayments, requiredScope("/api/v1/authorizations"))
	assert.Equal(t, models.ScopeAdmin, requiredScope("/api/v1/api-keys"))
	assert.Equal(t, models.ScopeAdmin, requiredScope("/api/v1/api-keys/123/rotate"))
	assert.Empty(t, requiredScope("/health"))
}
