package middleware

import (
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/tusharui/payment-gateway/backend/internal/api"
	"github.com/tusharui/payment-gateway/backend/internal/auth"
	"github.com/tusharui/payment-gateway/backend/internal/respond"
)

// RateLimiter decides whether a credential may issue another request. When it
// refuses, the returned duration is how long the caller should wait.
type RateLimiter interface {
	Allow(key uuid.UUID, limitPerMinute int) (bool, time.Duration)
}

// RateLimit throttles each authenticated credential with a token bucket. It
// must sit after APIKeyAuth so a principal is present, and before work that
// touches the database so saturated keys cost almost nothing.
//
// A key's own limit wins; zero falls back to the configured default, and a
// non-positive default means unlimited. When authentication is disabled the
// synthetic principal has no key ID, so the tenant itself is the bucket key.
func RateLimit(
	limiter RateLimiter,
	enabled bool,
	defaultPerMinute int,
	logger *slog.Logger,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !enabled || isAuthExempt(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			principal, ok := auth.PrincipalFrom(r.Context())
			if !ok {
				// Authentication always sets a principal on protected routes;
				// reaching this point means the chain was wired incorrectly.
				writeUnauthorized(w)
				return
			}

			limit := principal.RateLimitPerMinute
			if limit <= 0 {
				limit = defaultPerMinute
			}
			if limit <= 0 {
				next.ServeHTTP(w, r)
				return
			}

			bucketKey := principal.APIKeyID
			if bucketKey == uuid.Nil {
				bucketKey = principal.MerchantID
			}

			allowed, retryAfter := limiter.Allow(bucketKey, limit)
			if !allowed {
				w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(retryAfter)))
				logger.Warn("api key rate limit exceeded",
					"path", r.URL.Path,
					"key_id", principal.APIKeyID,
					"merchant_id", principal.MerchantID,
					"limit_per_minute", limit,
					"request_id", RequestIDFromContext(r.Context()),
				)
				respond.Error(w, http.StatusTooManyRequests,
					string(api.ErrorCodeRateLimited),
					"too many requests; retry later")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// retryAfterSeconds rounds a wait up to whole seconds and never reports zero,
// which would invite an immediate retry.
func retryAfterSeconds(wait time.Duration) int {
	seconds := int(math.Ceil(wait.Seconds()))
	if seconds < 1 {
		return 1
	}

	return seconds
}
