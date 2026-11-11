package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/tusharui/payment-gateway/backend/internal/auth"
	"github.com/tusharui/payment-gateway/backend/internal/db"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"github.com/tusharui/payment-gateway/backend/internal/repository"
	"github.com/tusharui/payment-gateway/backend/internal/security"
)

// maxAPIKeyNameLength bounds the human-readable key label.
const maxAPIKeyNameLength = 100

// APIKeyIssueRequest describes a credential to create.
type APIKeyIssueRequest struct {
	Name               string
	Scopes             []string
	RateLimitPerMinute int
	ExpiresAt          *time.Time
}

// IssuedAPIKey pairs persisted key metadata with the raw secret, which is
// returned to the caller exactly once.
type IssuedAPIKey struct {
	Key    *models.APIKey
	Secret string
}

// APIKeyService manages merchant credentials for the authenticated tenant.
type APIKeyService struct {
	keys   repository.APIKeyRepository
	logger *slog.Logger
	now    func() time.Time
}

// NewAPIKeyService creates an APIKeyService backed by the application database.
func NewAPIKeyService(database *db.DB, logger *slog.Logger) *APIKeyService {
	return newAPIKeyService(repository.NewAPIKeyRepository(database.GORM()), logger, time.Now)
}

func newAPIKeyService(
	keys repository.APIKeyRepository,
	logger *slog.Logger,
	now func() time.Time,
) *APIKeyService {
	return &APIKeyService{
		keys:   keys,
		logger: logger,
		now:    now,
	}
}

// Create issues a key for the authenticated merchant. Secrets are hashed before
// they reach the repository; only the display prefix and last four characters
// are ever stored in the clear.
func (s *APIKeyService) Create(ctx context.Context, request APIKeyIssueRequest) (*IssuedAPIKey, error) {
	merchantID, err := merchantIDFrom(ctx)
	if err != nil {
		return nil, err
	}

	name := strings.TrimSpace(request.Name)
	if name == "" || len(name) > maxAPIKeyNameLength {
		return nil, &ServiceError{
			Code:    ErrCodeInvalidRequest,
			Message: fmt.Sprintf("name must be between 1 and %d characters", maxAPIKeyNameLength),
		}
	}

	scopes, err := normalizeScopes(request.Scopes)
	if err != nil {
		return nil, err
	}

	if request.RateLimitPerMinute < 0 {
		return nil, &ServiceError{
			Code:    ErrCodeInvalidRequest,
			Message: "rate_limit_per_minute cannot be negative",
		}
	}

	now := s.now()
	if request.ExpiresAt != nil && !request.ExpiresAt.After(now) {
		return nil, &ServiceError{
			Code:    ErrCodeInvalidRequest,
			Message: "expires_at must be in the future",
		}
	}

	generated, err := security.GenerateAPIKey()
	if err != nil {
		return nil, err
	}

	key := &models.APIKey{
		ID:                 uuid.New(),
		MerchantID:         merchantID,
		Name:               name,
		KeyPrefix:          generated.Prefix,
		KeyLast4:           generated.Last4,
		KeyHash:            security.HashToken(generated.Raw),
		Scopes:             pq.StringArray(scopes),
		RateLimitPerMinute: request.RateLimitPerMinute,
		CreatedAt:          now,
		ExpiresAt:          request.ExpiresAt,
	}
	if err := s.keys.Create(ctx, key); err != nil {
		return nil, fmt.Errorf("failed to create api key: %w", err)
	}

	s.audit(ctx, "api key issued", key)

	return &IssuedAPIKey{Key: key, Secret: generated.Raw}, nil
}

// List returns every key owned by the authenticated merchant.
func (s *APIKeyService) List(ctx context.Context) ([]models.APIKey, error) {
	merchantID, err := merchantIDFrom(ctx)
	if err != nil {
		return nil, err
	}

	return s.keys.ListByMerchant(ctx, merchantID)
}

// Rotate replaces a key's secret in place. The previous secret stops
// authenticating immediately because only the hash is stored.
func (s *APIKeyService) Rotate(ctx context.Context, id uuid.UUID) (*IssuedAPIKey, error) {
	merchantID, err := merchantIDFrom(ctx)
	if err != nil {
		return nil, err
	}

	generated, err := security.GenerateAPIKey()
	if err != nil {
		return nil, err
	}

	rotated, err := s.keys.Rotate(
		ctx,
		merchantID,
		id,
		security.HashToken(generated.Raw),
		generated.Prefix,
		generated.Last4,
	)
	if err != nil {
		return nil, err
	}
	if !rotated {
		return nil, apiKeyNotFound()
	}

	key, err := s.keys.FindByID(ctx, merchantID, id)
	if err != nil {
		return nil, err
	}
	if key == nil {
		return nil, apiKeyNotFound()
	}

	s.audit(ctx, "api key rotated", key)

	return &IssuedAPIKey{Key: key, Secret: generated.Raw}, nil
}

// Revoke permanently disables a key owned by the authenticated merchant.
func (s *APIKeyService) Revoke(ctx context.Context, id uuid.UUID) error {
	merchantID, err := merchantIDFrom(ctx)
	if err != nil {
		return err
	}

	revoked, err := s.keys.Revoke(ctx, merchantID, id, s.now())
	if err != nil {
		return err
	}
	if !revoked {
		return apiKeyNotFound()
	}

	s.audit(ctx, "api key revoked", &models.APIKey{ID: id, MerchantID: merchantID})

	return nil
}

// normalizeScopes validates requested scopes, deduplicates them, and defaults to
// the least-privileged payment scope when none are supplied.
func normalizeScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return []string{models.ScopePayments}, nil
	}

	seen := make(map[string]bool, len(scopes))
	normalized := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		switch scope {
		case models.ScopePayments, models.ScopeAdmin:
		default:
			return nil, &ServiceError{
				Code:    ErrCodeInvalidRequest,
				Message: fmt.Sprintf("unsupported scope %q", scope),
			}
		}

		if !seen[scope] {
			seen[scope] = true
			normalized = append(normalized, scope)
		}
	}

	return normalized, nil
}

func apiKeyNotFound() *ServiceError {
	return &ServiceError{Code: ErrCodeNotFound, Message: "api key not found"}
}

// audit records credential lifecycle changes. The acting key is included when
// the request was authenticated so key management is attributable.
func (s *APIKeyService) audit(ctx context.Context, message string, key *models.APIKey) {
	attrs := []any{
		"merchant_id", key.MerchantID,
		"key_id", key.ID,
		"key_name", key.Name,
		"scopes", []string(key.Scopes),
	}
	if principal, ok := auth.PrincipalFrom(ctx); ok {
		attrs = append(attrs, "actor_key_id", principal.APIKeyID)
	}

	s.logger.Info(message, attrs...)
}

// Ensure APIKeyService satisfies the manager contract.
var _ APIKeyManager = (*APIKeyService)(nil)
