package handlers

import (
	"context"
	"time"

	"github.com/tusharui/payment-gateway/backend/internal/api"
)

// readinessTimeout bounds the dependency check so a hung database cannot make
// the readiness endpoint hang with it.
const readinessTimeout = 2 * time.Second

// GetHealth handles GET /health.
//
// This is a liveness probe: it only reports that the process can serve HTTP.
// Dependency failures belong in the readiness probe so an orchestrator does not
// restart otherwise healthy instances during a database blip.
func (h *Handler) GetHealth(
	ctx context.Context,
	request api.GetHealthRequestObject,
) (api.GetHealthResponseObject, error) {
	return api.GetHealth200JSONResponse{
		Status: api.Healthy,
	}, nil
}

// GetReady handles GET /ready.
//
// Readiness fails when the database is unreachable so load balancers stop
// sending traffic until the dependency recovers.
func (h *Handler) GetReady(
	ctx context.Context,
	request api.GetReadyRequestObject,
) (api.GetReadyResponseObject, error) {
	if h.healthChecker == nil {
		h.logger.Error("readiness check failed: health checker not configured")
		return api.GetReady503JSONResponse{
			Status: api.NotReady,
		}, nil
	}

	pingCtx, cancel := context.WithTimeout(ctx, readinessTimeout)
	defer cancel()

	if err := h.healthChecker.PingContext(pingCtx); err != nil {
		h.logger.Warn("readiness check failed: database unreachable", "error", err)
		return api.GetReady503JSONResponse{
			Status: api.NotReady,
		}, nil
	}

	return api.GetReady200JSONResponse{
		Status: api.Ready,
	}, nil
}
