// Package auth carries the authenticated merchant principal through request and
// background contexts.
package auth

import (
	"context"

	"github.com/google/uuid"
)

// Principal is the authenticated identity behind a request.
type Principal struct {
	MerchantID         uuid.UUID
	APIKeyID           uuid.UUID
	KeyName            string
	Scopes             []string
	RateLimitPerMinute int
}

// HasScope reports whether the principal grants the requested scope.
func (p Principal) HasScope(scope string) bool {
	for _, granted := range p.Scopes {
		if granted == scope {
			return true
		}
	}

	return false
}

type principalContextKey struct{}

type systemContextKey struct{}

// WithPrincipal stores the authenticated principal in a context.
func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

// PrincipalFrom returns the authenticated principal, if any.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}

// MerchantIDFrom returns the tenant associated with the request. It is absent
// for background work and unauthenticated internal calls.
func MerchantIDFrom(ctx context.Context) (uuid.UUID, bool) {
	principal, ok := PrincipalFrom(ctx)
	if !ok || principal.MerchantID == uuid.Nil {
		return uuid.Nil, false
	}

	return principal.MerchantID, true
}

// WithSystem marks a context as trusted background work that deliberately
// operates across every tenant. Request contexts never carry this marker.
func WithSystem(ctx context.Context) context.Context {
	return context.WithValue(ctx, systemContextKey{}, true)
}

// IsSystem reports whether the context is trusted background work.
func IsSystem(ctx context.Context) bool {
	trusted, _ := ctx.Value(systemContextKey{}).(bool)
	return trusted
}
