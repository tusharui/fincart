package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/tusharui/payment-gateway/backend/internal/api"
	"github.com/tusharui/payment-gateway/backend/internal/auth"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"github.com/tusharui/payment-gateway/backend/internal/respond"
)

// APIKeyAuthenticator resolves a raw Bearer credential to a principal.
type APIKeyAuthenticator interface {
	Authenticate(ctx context.Context, rawKey string) (*auth.Principal, error)
}

// APIKeyAuth requires a valid Bearer API key on every non-operational route. It
// also enforces the scope appropriate to the path: payment traffic needs the
// payments scope and key management needs the admin scope.
func APIKeyAuth(
	authenticator APIKeyAuthenticator,
	enabled bool,
	logger *slog.Logger,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !enabled {
				// Single-tenant deployments run without credentials. Downstream
				// tenant scoping still needs an owner, so attribute the request
				// to the default merchant instead of leaving it anonymous.
				if !isAuthExempt(r.URL.Path) {
					r = r.WithContext(auth.WithPrincipal(r.Context(), disabledAuthPrincipal()))
				}

				next.ServeHTTP(w, r)
				return
			}

			if isAuthExempt(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			rawKey, ok := bearerToken(r)
			if !ok {
				writeUnauthorized(w)
				return
			}

			principal, err := authenticator.Authenticate(r.Context(), rawKey)
			if err != nil {
				if errors.Is(err, auth.ErrInvalidAPIKey) {
					logger.Warn("api key rejected", "path", r.URL.Path, "request_id", RequestIDFromContext(r.Context()))
					writeUnauthorized(w)
					return
				}

				logger.Error("api key authentication failed", "path", r.URL.Path, "error", err)
				respond.Error(w, http.StatusServiceUnavailable,
					string(api.ErrorCodeServiceUnavailable),
					"authentication temporarily unavailable")
				return
			}

			if scope := requiredScope(r.URL.Path); scope != "" && !principal.HasScope(scope) {
				logger.Warn("api key is missing required scope",
					"path", r.URL.Path,
					"key_id", principal.APIKeyID,
					"required_scope", scope,
				)
				respond.Error(w, http.StatusForbidden,
					string(api.ErrorCodeForbidden),
					"API key is not authorized for this endpoint")
				return
			}

			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), *principal)))
		})
	}
}

// disabledAuthPrincipal is the synthetic identity used when authentication is
// turned off. It grants every scope and owns data as the default merchant, which
// matches a single-tenant deployment.
func disabledAuthPrincipal() auth.Principal {
	return auth.Principal{
		MerchantID: models.DefaultMerchantID,
		KeyName:    "authentication-disabled",
		Scopes:     []string{models.ScopePayments, models.ScopeAdmin},
	}
}

// isAuthExempt keeps operational and documentation endpoints reachable without
// a credential. These routes do not expose payment data.
func isAuthExempt(path string) bool {
	switch path {
	case "/", "/health", "/ready", "/metrics", "/docs", "/docs/openapi":
		return true
	default:
		return strings.HasPrefix(path, "/docs/")
	}
}

// bearerToken extracts a well-formed Bearer credential. The scheme comparison is
// case-insensitive per RFC 7235.
func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}

	token = strings.TrimSpace(token)
	if token == "" {
		return "", false
	}

	return token, true
}

// requiredScope maps a route to the scope it needs. Payment endpoints require
// the payments scope; key management requires admin.
func requiredScope(path string) string {
	if strings.HasPrefix(path, "/api/v1/api-keys") {
		return models.ScopeAdmin
	}
	if strings.HasPrefix(path, "/api/v1/") {
		return models.ScopePayments
	}

	return ""
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="bank-api"`)
	respond.Error(w, http.StatusUnauthorized, string(api.ErrorCodeUnauthorized),
		"missing or invalid API key")
}
