package service

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/tusharui/payment-gateway/backend/internal/auth"
)

// errMissingMerchantContext indicates a payment operation was invoked without
// an authenticated merchant. HTTP requests always carry one because the
// authentication middleware sets it (or attributes single-tenant traffic to the
// default merchant), so this is a wiring bug rather than a client error.
var errMissingMerchantContext = errors.New("request is not associated with a merchant")

// merchantIDFrom returns the tenant that owns the current request.
func merchantIDFrom(ctx context.Context) (uuid.UUID, error) {
	merchantID, ok := auth.MerchantIDFrom(ctx)
	if !ok {
		return uuid.Nil, errMissingMerchantContext
	}

	return merchantID, nil
}
