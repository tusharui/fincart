package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/tusharui/payment-gateway/backend/internal/api"
	"github.com/tusharui/payment-gateway/backend/internal/respond"
)

// requestErrorHandler converts strict-server parameter and body binding
// failures into the same JSON error shape used by the rest of the API.
func (h *Handler) requestErrorHandler(w http.ResponseWriter, r *http.Request, err error) {
	code := api.ErrorCodeInvalidRequest
	message := "invalid request"

	var requiredHeader *api.RequiredHeaderError
	var invalidParam *api.InvalidParamFormatError

	switch {
	case errors.As(err, &requiredHeader) && strings.EqualFold(requiredHeader.ParamName, "Idempotency-Key"):
		code = api.ErrorCodeMissingIdempotencyKey
		message = "Idempotency-Key header is required"
	case errors.As(err, &requiredHeader):
		message = requiredHeader.Error()
	case errors.As(err, &invalidParam):
		message = invalidParam.Error()
	}

	h.logger.Warn("request rejected", "error", err, "path", r.URL.Path)
	respond.Error(w, http.StatusBadRequest, string(code), message)
}

// responseErrorHandler handles failures that occur while writing a response.
func (h *Handler) responseErrorHandler(w http.ResponseWriter, r *http.Request, err error) {
	h.logger.Error("failed to write response", "error", err, "path", r.URL.Path)
	respond.Error(w, http.StatusInternalServerError, string(api.ErrorCodeInternalError), "internal error")
}
