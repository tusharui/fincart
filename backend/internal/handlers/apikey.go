package handlers

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/tusharui/payment-gateway/backend/internal/api"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"github.com/tusharui/payment-gateway/backend/internal/service"
)

// ListAPIKeys handles GET /api/v1/api-keys
func (h *Handler) ListAPIKeys(
	ctx context.Context,
	_ api.ListAPIKeysRequestObject,
) (api.ListAPIKeysResponseObject, error) {
	if h.apiKeyService == nil {
		return api.ListAPIKeys500JSONResponse{InternalErrorJSONResponse: internalErrorResponse()}, nil
	}

	keys, err := h.apiKeyService.List(ctx)
	if err != nil {
		h.logger.Error("failed to list api keys", "error", err)
		return api.ListAPIKeys500JSONResponse{InternalErrorJSONResponse: internalErrorResponse()}, nil
	}

	response := api.APIKeyListResponse{ApiKeys: make([]api.APIKey, 0, len(keys))}
	for i := range keys {
		response.ApiKeys = append(response.ApiKeys, toAPIKey(keys[i]))
	}

	return api.ListAPIKeys200JSONResponse(response), nil
}

// CreateAPIKey handles POST /api/v1/api-keys
func (h *Handler) CreateAPIKey(
	ctx context.Context,
	request api.CreateAPIKeyRequestObject,
) (api.CreateAPIKeyResponseObject, error) {
	if h.apiKeyService == nil {
		return api.CreateAPIKey500JSONResponse{InternalErrorJSONResponse: internalErrorResponse()}, nil
	}

	if request.Body == nil {
		return api.CreateAPIKey400JSONResponse{
			BadRequestJSONResponse: api.BadRequestJSONResponse{
				Error:   api.ErrorCodeInvalidRequest,
				Message: "request body is required",
			},
		}, nil
	}

	var expiresAt *time.Time
	if !request.Body.ExpiresAt.IsZero() {
		expires := request.Body.ExpiresAt
		expiresAt = &expires
	}

	scopes := make([]string, 0, len(request.Body.Scopes))
	for _, scope := range request.Body.Scopes {
		scopes = append(scopes, string(scope))
	}

	issued, err := h.apiKeyService.Create(ctx, service.APIKeyIssueRequest{
		Name:               request.Body.Name,
		Scopes:             scopes,
		RateLimitPerMinute: request.Body.RateLimitPerMinute,
		ExpiresAt:          expiresAt,
	})
	if err != nil {
		code, message, clientError := mapAPIKeyError(err)
		if !clientError {
			h.logger.Error("failed to create api key", "error", err)
			return api.CreateAPIKey500JSONResponse{InternalErrorJSONResponse: internalErrorResponse()}, nil
		}

		return api.CreateAPIKey400JSONResponse{
			BadRequestJSONResponse: api.BadRequestJSONResponse{Error: code, Message: message},
		}, nil
	}

	return api.CreateAPIKey201JSONResponse{
		ApiKey: toAPIKey(*issued.Key),
		Secret: issued.Secret,
	}, nil
}

// RotateAPIKey handles POST /api/v1/api-keys/{apiKeyId}/rotate
func (h *Handler) RotateAPIKey(
	ctx context.Context,
	request api.RotateAPIKeyRequestObject,
) (api.RotateAPIKeyResponseObject, error) {
	if h.apiKeyService == nil {
		return api.RotateAPIKey500JSONResponse{InternalErrorJSONResponse: internalErrorResponse()}, nil
	}

	issued, err := h.apiKeyService.Rotate(ctx, uuid.UUID(request.ApiKeyId))
	if err != nil {
		code, message, clientError := mapAPIKeyError(err)
		if !clientError {
			h.logger.Error("failed to rotate api key", "error", err, "key_id", request.ApiKeyId)
			return api.RotateAPIKey500JSONResponse{InternalErrorJSONResponse: internalErrorResponse()}, nil
		}

		if code == api.ErrorCodeNotFound {
			return api.RotateAPIKey404JSONResponse{
				NotFoundJSONResponse: api.NotFoundJSONResponse{Error: code, Message: message},
			}, nil
		}

		return api.RotateAPIKey500JSONResponse{InternalErrorJSONResponse: internalErrorResponse()}, nil
	}

	return api.RotateAPIKey200JSONResponse{
		ApiKey: toAPIKey(*issued.Key),
		Secret: issued.Secret,
	}, nil
}

// RevokeAPIKey handles DELETE /api/v1/api-keys/{apiKeyId}
func (h *Handler) RevokeAPIKey(
	ctx context.Context,
	request api.RevokeAPIKeyRequestObject,
) (api.RevokeAPIKeyResponseObject, error) {
	if h.apiKeyService == nil {
		return api.RevokeAPIKey500JSONResponse{InternalErrorJSONResponse: internalErrorResponse()}, nil
	}

	if err := h.apiKeyService.Revoke(ctx, uuid.UUID(request.ApiKeyId)); err != nil {
		code, message, clientError := mapAPIKeyError(err)
		if !clientError {
			h.logger.Error("failed to revoke api key", "error", err, "key_id", request.ApiKeyId)
			return api.RevokeAPIKey500JSONResponse{InternalErrorJSONResponse: internalErrorResponse()}, nil
		}

		if code == api.ErrorCodeNotFound {
			return api.RevokeAPIKey404JSONResponse{
				NotFoundJSONResponse: api.NotFoundJSONResponse{Error: code, Message: message},
			}, nil
		}

		return api.RevokeAPIKey500JSONResponse{InternalErrorJSONResponse: internalErrorResponse()}, nil
	}

	return api.RevokeAPIKey204Response{}, nil
}

func internalErrorResponse() api.InternalErrorJSONResponse {
	return api.InternalErrorJSONResponse{
		Error:   api.ErrorCodeInternalError,
		Message: "internal error",
	}
}

// mapAPIKeyError translates key management failures into public error codes.
// The final value reports whether the error is safe to expose to the client.
func mapAPIKeyError(err error) (api.ErrorCode, string, bool) {
	svcErr := extractServiceError(err)
	if svcErr == nil {
		return api.ErrorCodeInternalError, "internal error", false
	}

	switch svcErr.Code {
	case service.ErrCodeInvalidRequest:
		return api.ErrorCodeInvalidRequest, svcErr.Message, true
	case service.ErrCodeNotFound:
		return api.ErrorCodeNotFound, svcErr.Message, true
	default:
		return api.ErrorCodeInternalError, "internal error", false
	}
}

// toAPIKey converts persisted metadata into the public representation. Secrets
// are intentionally absent because only the hash is stored.
func toAPIKey(key models.APIKey) api.APIKey {
	result := api.APIKey{
		Id:                 key.ID,
		Name:               key.Name,
		KeyPrefix:          key.KeyPrefix,
		KeyLast4:           key.KeyLast4,
		Scopes:             make([]api.APIKeyScope, 0, len(key.Scopes)),
		RateLimitPerMinute: key.RateLimitPerMinute,
		Bootstrap:          key.Bootstrap,
		CreatedAt:          key.CreatedAt,
	}

	for _, scope := range key.Scopes {
		result.Scopes = append(result.Scopes, api.APIKeyScope(scope))
	}

	if key.LastUsedAt != nil {
		result.LastUsedAt = *key.LastUsedAt
	}
	if key.ExpiresAt != nil {
		result.ExpiresAt = *key.ExpiresAt
	}
	if key.RevokedAt != nil {
		result.RevokedAt = *key.RevokedAt
	}

	return result
}
