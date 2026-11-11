// Package middleware provides HTTP middleware components for the bank API.
package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/tusharui/payment-gateway/backend/internal/api"
	"github.com/tusharui/payment-gateway/backend/internal/auth"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"github.com/tusharui/payment-gateway/backend/internal/respond"
)

const (
	idempotencyKeyHeader = "Idempotency-Key"

	// maxIdempotencyKeyLength matches the OpenAPI schema constraint.
	maxIdempotencyKeyLength = 255

	// maxIdempotencyBodyBytes bounds how much of a request body is buffered
	// in order to compute a stable request fingerprint.
	maxIdempotencyBodyBytes = 1 << 20 // 1 MiB
)

// idempotentPaths defines which paths require idempotency handling
//
// Only mutating operations (POST) need idempotency
var idempotentPaths = []string{
	"/api/v1/authorizations",
	"/api/v1/captures",
	"/api/v1/voids",
	"/api/v1/refunds",
}

// IdempotencyRepository defines the storage operations required by the
// idempotency middleware.
type IdempotencyRepository interface {
	Get(ctx context.Context, key, requestPath string) (*models.IdempotencyKey, error)
	Claim(ctx context.Context, key, requestPath, requestHash string) (bool, error)
	Complete(ctx context.Context, key, requestPath, requestHash string, responseStatus int, responseBody string) error
	Release(ctx context.Context, key, requestPath, requestHash string) error
}

type responseCapture struct {
	http.ResponseWriter
	body       bytes.Buffer
	statusCode int
}

func newResponseCapture(w http.ResponseWriter) *responseCapture {
	return &responseCapture{
		ResponseWriter: w,
		statusCode:     http.StatusOK, // Default if WriteHeader not called
	}
}

func (rc *responseCapture) WriteHeader(code int) {
	rc.statusCode = code
	rc.ResponseWriter.WriteHeader(code)
}

func (rc *responseCapture) Write(b []byte) (int, error) {
	rc.body.Write(b) // Capture for caching
	return rc.ResponseWriter.Write(b)
}

// IdempotencyRepositoryFactory returns an idempotency repository bound to a
// merchant. A request-scoped repository keeps client-chosen keys isolated
// between tenants without threading the merchant through every call.
type IdempotencyRepositoryFactory func(merchantID uuid.UUID) IdempotencyRepository

// Idempotency creates middleware that handles idempotent request caching.
//
// A key is claimed before the request executes, which prevents two concurrent
// requests with the same key from both being processed. Cached responses are
// bound to a hash of the request body, so reusing a key with a different
// payload is rejected instead of silently replaying an unrelated response.
func Idempotency(factory IdempotencyRepositoryFactory, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !requiresIdempotency(r) {
				next.ServeHTTP(w, r)
				return
			}

			idempotencyKey := r.Header.Get(idempotencyKeyHeader)
			if idempotencyKey == "" {
				// The strict handler validates the required header and
				// returns a structured error response.
				next.ServeHTTP(w, r)
				return
			}

			if len(idempotencyKey) > maxIdempotencyKeyLength {
				respond.Error(w, http.StatusBadRequest,
					string(api.ErrorCodeInvalidIdempotencyKey),
					"Idempotency-Key must be at most 255 characters")
				return
			}

			body, err := readRequestBody(r)
			if err != nil {
				if errors.Is(err, errRequestBodyTooLarge) {
					respond.Error(w, http.StatusRequestEntityTooLarge,
						string(api.ErrorCodeRequestTooLarge),
						"request body must not exceed 1 MiB")
					return
				}

				logger.Warn("failed to read request body for idempotency", "error", err)
				respond.Error(w, http.StatusBadRequest,
					string(api.ErrorCodeInvalidRequest),
					"failed to read request body")
				return
			}

			merchantID, ok := auth.MerchantIDFrom(r.Context())
			if !ok {
				// Authentication middleware always sets a principal on payment
				// routes; reaching this point means the route was wired wrong.
				writeUnauthorized(w)
				return
			}
			repo := factory(merchantID)

			requestPath := normalizeRequestPath(r.URL.Path)
			requestHash := hashRequestBody(body)
			ctx := r.Context()

			cached, err := repo.Get(ctx, idempotencyKey, requestPath)
			if err != nil {
				logger.Error("failed to check idempotency cache", "error", err)
				respond.Error(w, http.StatusServiceUnavailable,
					string(api.ErrorCodeIdempotencyUnavailable),
					"idempotency service unavailable")
				return
			}

			if cached != nil {
				writeCachedResponse(w, logger, cached, idempotencyKey, requestPath, requestHash)
				return
			}

			acquired, err := repo.Claim(ctx, idempotencyKey, requestPath, requestHash)
			if err != nil {
				logger.Error("failed to claim idempotency key", "error", err)
				respond.Error(w, http.StatusServiceUnavailable,
					string(api.ErrorCodeIdempotencyUnavailable),
					"idempotency service unavailable")
				return
			}

			if !acquired {
				// Another request won the race. Re-read to replay its cached
				// response, or report that it is still in flight.
				cached, getErr := repo.Get(ctx, idempotencyKey, requestPath)
				if getErr != nil {
					logger.Error("failed to read idempotency state after claim conflict", "error", getErr)
					respond.Error(w, http.StatusServiceUnavailable,
						string(api.ErrorCodeIdempotencyUnavailable),
						"idempotency service unavailable")
					return
				}
				if cached != nil {
					writeCachedResponse(w, logger, cached, idempotencyKey, requestPath, requestHash)
					return
				}

				w.Header().Set("Retry-After", "1")
				respond.Error(w, http.StatusConflict,
					string(api.ErrorCodeIdempotencyInProgress),
					"a request with this Idempotency-Key is still in progress")
				return
			}

			capture := newResponseCapture(w)
			next.ServeHTTP(capture, r)

			// The client may disconnect before the handler returns; persist the
			// outcome anyway so retries observe a stable result.
			persistCtx := context.WithoutCancel(ctx)

			if shouldCacheResponse(capture.statusCode) {
				if err := repo.Complete(
					persistCtx,
					idempotencyKey,
					requestPath,
					requestHash,
					capture.statusCode,
					capture.body.String(),
				); err != nil {
					logger.Error("failed to store idempotency response",
						"error", err,
						"key", idempotencyKey,
						"path", requestPath,
					)
				}
				return
			}

			// Non-success responses are not cached: release the claim so the
			// client can safely retry.
			if err := repo.Release(persistCtx, idempotencyKey, requestPath, requestHash); err != nil {
				logger.Error("failed to release idempotency claim",
					"error", err,
					"key", idempotencyKey,
					"path", requestPath,
				)
			}
		})
	}
}

// writeCachedResponse replays a stored response, or reports a key conflict when
// the stored entry belongs to a different request payload or is still pending.
func writeCachedResponse(
	w http.ResponseWriter,
	logger *slog.Logger,
	cached *models.IdempotencyKey,
	idempotencyKey, requestPath, requestHash string,
) {
	// Entries written before request hashing was introduced have an empty
	// hash; treat them as compatible and replay them.
	if cached.RequestHash != "" && cached.RequestHash != requestHash {
		logger.Warn("idempotency key reused with a different request body",
			"key", idempotencyKey,
			"path", requestPath,
		)
		respond.Error(w, http.StatusConflict,
			string(api.ErrorCodeIdempotencyKeyConflict),
			"Idempotency-Key was already used with a different request payload")
		return
	}

	if cached.ResponseStatus == models.IdempotencyStatusPending {
		w.Header().Set("Retry-After", "1")
		respond.Error(w, http.StatusConflict,
			string(api.ErrorCodeIdempotencyInProgress),
			"a request with this Idempotency-Key is still in progress")
		return
	}

	logger.Debug("returning cached idempotent response",
		"key", idempotencyKey,
		"path", requestPath,
		"status", cached.ResponseStatus,
	)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Idempotent-Replayed", "true")
	w.WriteHeader(cached.ResponseStatus)
	//nolint:errcheck // Best effort response writing
	_, _ = w.Write([]byte(cached.ResponseBody))
}

// readRequestBody buffers the request body (up to maxIdempotencyBodyBytes) and
// restores it so downstream handlers can decode it normally.
func readRequestBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxIdempotencyBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxIdempotencyBodyBytes {
		return nil, errRequestBodyTooLarge
	}

	r.Body = io.NopCloser(bytes.NewReader(body))

	return body, nil
}

func hashRequestBody(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func requiresIdempotency(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}

	requestPath := normalizeRequestPath(r.URL.Path)
	for _, path := range idempotentPaths {
		if requestPath == path {
			return true
		}
	}
	return false
}

func normalizeRequestPath(urlPath string) string {
	return strings.TrimSuffix(urlPath, "/")
}

func shouldCacheResponse(statusCode int) bool {
	return statusCode >= 200 && statusCode < 300
}

var errRequestBodyTooLarge = errors.New("request body too large")
