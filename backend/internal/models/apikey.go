package models

import (
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// API key scopes. A key may hold both scopes; payment endpoints require
// ScopePayments and API key management endpoints require ScopeAdmin.
const (
	ScopePayments = "payments"
	ScopeAdmin    = "admin"
)

// APIKey is a hashed credential that authenticates a merchant. The raw key is
// returned to the caller exactly once, at issue time, and is never persisted.
type APIKey struct {
	ID                 uuid.UUID      `db:"id" gorm:"column:id;primaryKey"`
	MerchantID         uuid.UUID      `db:"merchant_id" gorm:"column:merchant_id;not null"`
	Name               string         `db:"name" gorm:"column:name;not null"`
	KeyPrefix          string         `db:"key_prefix" gorm:"column:key_prefix;not null"`
	KeyLast4           string         `db:"key_last4" gorm:"column:key_last4;not null"`
	KeyHash            string         `db:"key_hash" gorm:"column:key_hash;not null"`
	Scopes             pq.StringArray `db:"scopes" gorm:"column:scopes;type:text[];not null"`
	RateLimitPerMinute int            `db:"rate_limit_per_minute" gorm:"column:rate_limit_per_minute;not null"`
	// Bootstrap marks the single key that can be reconciled from environment
	// configuration at startup. A partial unique index allows at most one per
	// merchant.
	Bootstrap  bool       `db:"bootstrap" gorm:"column:bootstrap;not null"`
	CreatedAt  time.Time  `db:"created_at" gorm:"column:created_at;not null"`
	LastUsedAt *time.Time `db:"last_used_at" gorm:"column:last_used_at"`
	ExpiresAt  *time.Time `db:"expires_at" gorm:"column:expires_at"`
	RevokedAt  *time.Time `db:"revoked_at" gorm:"column:revoked_at"`
}

// TableName returns the api_keys table name.
func (APIKey) TableName() string {
	return "api_keys"
}

// HasScope reports whether the key grants the requested scope.
func (k *APIKey) HasScope(scope string) bool {
	for _, granted := range k.Scopes {
		if granted == scope {
			return true
		}
	}

	return false
}

// IsActive reports whether the key may authenticate at the given time.
func (k *APIKey) IsActive(now time.Time) bool {
	if k.RevokedAt != nil {
		return false
	}
	if k.ExpiresAt != nil && !k.ExpiresAt.After(now) {
		return false
	}

	return true
}
