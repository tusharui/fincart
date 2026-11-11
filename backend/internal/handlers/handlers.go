// Package handlers implements HTTP handlers for the bank API.
package handlers

import (
	"log/slog"

	"github.com/tusharui/payment-gateway/backend/internal/service"
)

// Handler implements the api.StrictServerInterface for all endpoints
type Handler struct {
	authService    service.Authorizer
	captureService service.Capturer
	voidService    service.Voider
	refundService  service.Refunder
	apiKeyService  service.APIKeyManager
	healthChecker  service.HealthChecker
	logger         *slog.Logger
}

// HandlerOption configures optional handler dependencies. Required payment
// services stay positional so existing call sites remain explicit.
type HandlerOption func(*Handler)

// WithAPIKeyManager enables the API key management endpoints.
func WithAPIKeyManager(manager service.APIKeyManager) HandlerOption {
	return func(h *Handler) {
		h.apiKeyService = manager
	}
}

// NewHandler creates a new Handler with injected service dependencies.
func NewHandler(
	authService service.Authorizer,
	captureService service.Capturer,
	voidService service.Voider,
	refundService service.Refunder,
	healthChecker service.HealthChecker,
	logger *slog.Logger,
	options ...HandlerOption,
) *Handler {
	handler := &Handler{
		authService:    authService,
		captureService: captureService,
		voidService:    voidService,
		refundService:  refundService,
		healthChecker:  healthChecker,
		logger:         logger,
	}

	for _, option := range options {
		option(handler)
	}

	return handler
}
