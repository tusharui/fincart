package handlers

import (
	"context"

	"github.com/tusharui/payment-gateway/backend/internal/api"
)

// CreateRefund handles POST /api/v1/refunds
func (h *Handler) CreateRefund(
	ctx context.Context,
	request api.CreateRefundRequestObject,
) (api.CreateRefundResponseObject, error) {
	captureID, err := parseCaptureID(request.Body.CaptureId)
	if err != nil {
		//nolint:nilerr // Returning 400 response object, not propagating error
		return api.CreateRefund400JSONResponse{
			BadRequestJSONResponse: api.BadRequestJSONResponse{
				Error:   api.ErrorCodeCaptureNotFound,
				Message: "invalid capture ID format",
			},
		}, nil
	}

	txn, err := h.refundService.Refund(ctx, captureID, request.Body.Amount)
	if err != nil {
		return h.handleRefundError(err)
	}

	return api.CreateRefund200JSONResponse{
		RefundId:   formatRefundID(txn.ID),
		CaptureId:  formatCaptureID(*txn.ReferenceID),
		Status:     api.Refunded,
		Amount:     txn.AmountCents,
		Currency:   txn.Currency,
		OrderId:    formatOptionalUUID(txn.OrderID),
		CustomerId: formatOptionalUUID(txn.CustomerID),
		RefundedAt: txn.CreatedAt,
	}, nil
}

// GetRefund handles GET /api/v1/refunds/{refundId}
func (h *Handler) GetRefund(
	ctx context.Context,
	request api.GetRefundRequestObject,
) (api.GetRefundResponseObject, error) {
	refundID, err := parseRefundID(request.RefundId)
	if err != nil {
		//nolint:nilerr // Returning 404 response object, not propagating error
		return api.GetRefund404JSONResponse{
			NotFoundJSONResponse: api.NotFoundJSONResponse{
				Error:   api.ErrorCodeNotFound,
				Message: "refund not found",
			},
		}, nil
	}

	txn, err := h.refundService.GetRefund(ctx, refundID)
	if err != nil {
		//nolint:nilerr // Returning 404 response object, not propagating error
		return api.GetRefund404JSONResponse{
			NotFoundJSONResponse: api.NotFoundJSONResponse{
				Error:   api.ErrorCodeNotFound,
				Message: "refund not found",
			},
		}, nil
	}

	return api.GetRefund200JSONResponse{
		RefundId:   formatRefundID(txn.ID),
		CaptureId:  formatCaptureID(*txn.ReferenceID),
		Status:     api.Refunded,
		Amount:     txn.AmountCents,
		Currency:   txn.Currency,
		OrderId:    formatOptionalUUID(txn.OrderID),
		CustomerId: formatOptionalUUID(txn.CustomerID),
		RefundedAt: txn.CreatedAt,
	}, nil
}

// handleRefundError maps service errors to appropriate HTTP responses
func (h *Handler) handleRefundError(err error) (api.CreateRefundResponseObject, error) {
	svcErr := extractServiceError(err)
	if svcErr == nil {
		h.logger.Error("unexpected error during refund", "error", err)
		return api.CreateRefund500JSONResponse{
			InternalErrorJSONResponse: api.InternalErrorJSONResponse{
				Error:   api.ErrorCodeInternalError,
				Message: "internal error",
			},
		}, nil
	}

	errorCode := mapServiceErrorToCode(svcErr.Code)

	switch {
	case isNotFoundError(svcErr.Code):
		return api.CreateRefund404JSONResponse{
			NotFoundJSONResponse: api.NotFoundJSONResponse{
				Error:   errorCode,
				Message: svcErr.Message,
			},
		}, nil
	case isConflictError(svcErr.Code):
		return api.CreateRefund409JSONResponse{
			ConflictJSONResponse: api.ConflictJSONResponse{
				Error:   errorCode,
				Message: svcErr.Message,
			},
		}, nil
	case errorCode == api.ErrorCodeInternalError:
		h.logger.Error("unexpected error during refund", "error", err)
		return api.CreateRefund500JSONResponse{
			InternalErrorJSONResponse: api.InternalErrorJSONResponse{
				Error:   api.ErrorCodeInternalError,
				Message: "internal error",
			},
		}, nil
	}

	return api.CreateRefund400JSONResponse{
		BadRequestJSONResponse: api.BadRequestJSONResponse{
			Error:   errorCode,
			Message: svcErr.Message,
		},
	}, nil
}
