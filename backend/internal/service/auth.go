package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/tusharui/payment-gateway/backend/internal/auth"
	"github.com/tusharui/payment-gateway/backend/internal/db"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"github.com/tusharui/payment-gateway/backend/internal/repository"
	"github.com/tusharui/payment-gateway/backend/internal/security"
)

// AuthService authenticates API keys and provisions the bootstrap credential.
type AuthService struct {
	keys      repository.APIKeyRepository
	merchants repository.MerchantRepository
	logger    *slog.Logger
	throttle  time.Duration
	now       func() time.Time
}

// NewAuthService creates an AuthService backed by the application database.
func NewAuthService(database *db.DB, lastUsedThrottle time.Duration, logger *slog.Logger) *AuthService {
	return newAuthService(
		repository.NewAPIKeyRepository(database.GORM()),
		repository.NewMerchantRepository(database.GORM()),
		lastUsedThrottle,
		logger,
		time.Now,
	)
}

func newAuthService(
	keys repository.APIKeyRepository,
	merchants repository.MerchantRepository,
	lastUsedThrottle time.Duration,
	logger *slog.Logger,
	now func() time.Time,
) *AuthService {
	return &AuthService{
		keys:      keys,
		merchants: merchants,
		logger:    logger,
		throttle:  lastUsedThrottle,
		now:       now,
	}
}

// Authenticate resolves a raw Bearer credential to a merchant principal. Every
// credential failure is reported as auth.ErrInvalidAPIKey so callers do not
// leak whether a key exists, was revoked, or expired.
func (s *AuthService) Authenticate(ctx context.Context, rawKey string) (*auth.Principal, error) {
	if rawKey == "" {
		return nil, auth.ErrInvalidAPIKey
	}

	key, err := s.keys.FindByHash(ctx, security.HashToken(rawKey))
	if err != nil {
		return nil, fmt.Errorf("failed to look up api key: %w", err)
	}

	now := s.now()
	if key == nil || !key.IsActive(now) {
		return nil, auth.ErrInvalidAPIKey
	}

	// Usage tracking is best effort: a failed update must not reject an
	// otherwise valid request. WithoutCancel keeps the write alive when the
	// client disconnects.
	touchCtx := context.WithoutCancel(ctx)
	if err := s.keys.TouchLastUsed(touchCtx, key.ID, now, now.Add(-s.throttle)); err != nil {
		s.logger.Warn("failed to record api key usage", "key_id", key.ID, "error", err)
	}

	return &auth.Principal{
		MerchantID:         key.MerchantID,
		APIKeyID:           key.ID,
		KeyName:            key.Name,
		Scopes:             append([]string(nil), key.Scopes...),
		RateLimitPerMinute: key.RateLimitPerMinute,
	}, nil
}

// EnsureBootstrap creates or replaces the single bootstrap key for the default
// merchant. Rotating the configured raw key updates the stored hash in place.
func (s *AuthService) EnsureBootstrap(ctx context.Context, rawKey, merchantName string) (*models.APIKey, error) {
	if rawKey == "" {
		return nil, errors.New("bootstrap api key cannot be empty")
	}

	now := s.now()
	merchant := &models.Merchant{
		ID:        models.DefaultMerchantID,
		Name:      merchantName,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.merchants.Ensure(ctx, merchant); err != nil {
		return nil, err
	}

	prefix, last4 := security.DescribeAPIKey(rawKey)
	key := &models.APIKey{
		ID:         uuid.New(),
		MerchantID: models.DefaultMerchantID,
		Name:       "bootstrap",
		KeyPrefix:  prefix,
		KeyLast4:   last4,
		KeyHash:    security.HashToken(rawKey),
		Scopes:     []string{models.ScopePayments, models.ScopeAdmin},
		Bootstrap:  true,
		CreatedAt:  now,
	}

	return s.keys.EnsureBootstrap(ctx, key)
}

// ActiveKeyCount reports how many credentials can currently authenticate. It is
// used at startup to decide whether a generated bootstrap key is required.
func (s *AuthService) ActiveKeyCount(ctx context.Context) (int64, error) {
	return s.keys.CountActive(ctx, s.now())
}
