package handlers

import (
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/tusharui/payment-gateway/backend/internal/api"
	"github.com/tusharui/payment-gateway/backend/internal/config"
	"github.com/tusharui/payment-gateway/backend/internal/db"
	"github.com/tusharui/payment-gateway/backend/internal/middleware"
	"github.com/tusharui/payment-gateway/backend/internal/observability"
	"github.com/tusharui/payment-gateway/backend/internal/ratelimit"
	"github.com/tusharui/payment-gateway/backend/internal/repository"
	"github.com/tusharui/payment-gateway/backend/internal/service"
)

// NewRouter creates and configures the HTTP router with all routes and middleware.
func NewRouter(
	database *db.DB,
	cfg *config.Config,
	logger *slog.Logger,
) http.Handler {
	authService := service.NewAuthorizationService(database, cfg.App.AuthExpiryHours)
	captureService := service.NewCaptureService(database)
	voidService := service.NewVoidService(database)
	refundService := service.NewRefundService(database)
	apiKeyService := service.NewAuthService(database, cfg.Auth.LastUsedThrottle, logger)

	handler := NewHandler(authService, captureService, voidService, refundService, database, logger)
	strictHandler := api.NewStrictHandlerWithOptions(handler, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  handler.requestErrorHandler,
		ResponseErrorHandlerFunc: handler.responseErrorHandler,
	})

	mux := http.NewServeMux()
	api.RegisterDocsRoutes(mux)

	metrics := observability.NewMetrics(database.DB)
	mux.Handle("GET /metrics", metrics.Handler())

	api.HandlerWithOptions(strictHandler, api.StdHTTPServerOptions{
		BaseRouter:       mux,
		ErrorHandlerFunc: handler.requestErrorHandler,
	})

	// CaptureRoute sits directly outside the mux so the matched route pattern
	// is available to the metrics middleware for label aggregation.
	var finalHandler http.Handler = observability.CaptureRoute(mux)

	finalHandler = middleware.FailureInjection(&cfg.App, logger)(finalHandler)

	idempotencyRepo := func(merchantID uuid.UUID) middleware.IdempotencyRepository {
		return repository.NewIdempotencyRepository(database.GORM(), merchantID)
	}
	finalHandler = middleware.Idempotency(idempotencyRepo, logger)(finalHandler)
	finalHandler = middleware.RateLimit(
		ratelimit.NewLimiter(),
		cfg.RateLimit.Enabled,
		cfg.RateLimit.DefaultPerMinute,
		logger,
	)(finalHandler)
	finalHandler = middleware.APIKeyAuth(apiKeyService, cfg.Auth.Enabled, logger)(finalHandler)

	// Request ID and access logging sit outside recovery so a panic is logged
	// with its correlation ID and the resulting 500 is visible in the access
	// log. Recovery wraps every handler, including the idempotency layer.
	// Metrics are outermost (after request IDs) so short-circuited responses
	// are measured as well.
	finalHandler = middleware.Recovery(logger)(finalHandler)
	finalHandler = middleware.AccessLog(logger)(finalHandler)
	finalHandler = metrics.Middleware()(finalHandler)
	finalHandler = middleware.RequestID(finalHandler)

	return finalHandler
}
