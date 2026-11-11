package service

import (
	"context"

	"github.com/google/uuid"
	"github.com/tusharui/payment-gateway/backend/internal/models"
)

// HealthChecker validates system health.
type HealthChecker interface {
	PingContext(ctx context.Context) error
}

// Authorizer handles payment authorization operations
type Authorizer interface {
	Authorize(ctx context.Context, request AuthorizationRequest) (*models.Transaction, error)
	GetAuthorization(ctx context.Context, authID uuid.UUID) (*models.Transaction, error)
}

// Capturer handles payment capture operations
type Capturer interface {
	Capture(ctx context.Context, authorizationID uuid.UUID, amount int64) (*models.Transaction, error)
	GetCapture(ctx context.Context, captureID uuid.UUID) (*models.Transaction, error)
}

// Voider handles authorization void operations
type Voider interface {
	Void(ctx context.Context, authorizationID uuid.UUID) (*models.Transaction, error)
}

// Refunder handles refund operations
type Refunder interface {
	Refund(ctx context.Context, captureID uuid.UUID, amount int64) (*models.Transaction, error)
	GetRefund(ctx context.Context, refundID uuid.UUID) (*models.Transaction, error)
}

// APIKeyManager manages API credentials for the authenticated merchant.
type APIKeyManager interface {
	Create(ctx context.Context, request APIKeyIssueRequest) (*IssuedAPIKey, error)
	List(ctx context.Context) ([]models.APIKey, error)
	Rotate(ctx context.Context, id uuid.UUID) (*IssuedAPIKey, error)
	Revoke(ctx context.Context, id uuid.UUID) error
}

// Ensure concrete types implement interfaces
var (
	_ Authorizer = (*AuthorizationService)(nil)
	_ Capturer   = (*CaptureService)(nil)
	_ Voider     = (*VoidService)(nil)
	_ Refunder   = (*RefundService)(nil)
)
