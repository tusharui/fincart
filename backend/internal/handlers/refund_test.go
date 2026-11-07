package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/tusharui/payment-gateway/backend/internal/api"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"github.com/tusharui/payment-gateway/backend/internal/service"
	"github.com/tusharui/payment-gateway/backend/internal/service/mocks"
)

func TestCreateRefund_Success(t *testing.T) {
	mockRefund := mocks.NewMockRefunder(t)
	handler := NewHandler(nil, nil, nil, mockRefund, nil, testLogger())

	captureID := uuid.New()
	refundID := uuid.New()

	mockRefund.On("Refund", mock.Anything, captureID, int64(5000)).
		Return(&models.Transaction{
			ID:          refundID,
			ReferenceID: &captureID,
			AmountCents: 5000,
			Currency:    "USD",
			CreatedAt:   time.Now(),
		}, nil)

	req := api.CreateRefundRequestObject{
		Body: &api.CreateRefundJSONRequestBody{
			CaptureId: "cap_" + captureID.String(),
			Amount:    5000,
		},
	}

	resp, err := handler.CreateRefund(context.Background(), req)

	require.NoError(t, err)
	successResp, ok := resp.(api.CreateRefund200JSONResponse)
	require.True(t, ok)
	assert.Equal(t, api.Refunded, successResp.Status)
}

func TestCreateRefund_ServiceErrors(t *testing.T) {
	tests := []struct {
		name           string
		serviceErr     *service.ServiceError
		expectedCode   api.ErrorCode
		expectedStatus int
	}{
		{"capture not found", &service.ServiceError{Code: service.ErrCodeCaptureNotFound}, api.ErrorCodeCaptureNotFound, 404},
		{"capture not completed", &service.ServiceError{Code: service.ErrCodeCaptureNotCompleted}, api.ErrorCodeCaptureNotCompleted, 409},
		{"already refunded", &service.ServiceError{Code: service.ErrCodeAlreadyRefunded}, api.ErrorCodeAlreadyRefunded, 409},
		{"amount mismatch", &service.ServiceError{Code: service.ErrCodeAmountMismatch}, api.ErrorCodeAmountMismatch, 400},
		{"internal error", &service.ServiceError{Code: service.ErrCodeInternalError}, api.ErrorCodeInternalError, 500},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockRefund := mocks.NewMockRefunder(t)
			handler := NewHandler(nil, nil, nil, mockRefund, nil, testLogger())

			mockRefund.On("Refund", mock.Anything, mock.Anything, mock.Anything).
				Return(nil, tt.serviceErr)

			req := api.CreateRefundRequestObject{
				Body: &api.CreateRefundJSONRequestBody{
					CaptureId: "cap_" + uuid.New().String(),
					Amount:    5000,
				},
			}

			resp, err := handler.CreateRefund(context.Background(), req)
			require.NoError(t, err)

			switch tt.expectedStatus {
			case 404:
				typed, ok := resp.(api.CreateRefund404JSONResponse)
				require.True(t, ok, "expected 404 response, got %T", resp)
				assert.Equal(t, tt.expectedCode, typed.Error)
			case 409:
				typed, ok := resp.(api.CreateRefund409JSONResponse)
				require.True(t, ok, "expected 409 response, got %T", resp)
				assert.Equal(t, tt.expectedCode, typed.Error)
			case 500:
				typed, ok := resp.(api.CreateRefund500JSONResponse)
				require.True(t, ok, "expected 500 response, got %T", resp)
				assert.Equal(t, tt.expectedCode, typed.Error)
			default:
				typed, ok := resp.(api.CreateRefund400JSONResponse)
				require.True(t, ok, "expected 400 response, got %T", resp)
				assert.Equal(t, tt.expectedCode, typed.Error)
			}
		})
	}
}

func TestCreateRefund_InvalidIDFormat(t *testing.T) {
	handler := NewHandler(nil, nil, nil, nil, nil, testLogger())

	req := api.CreateRefundRequestObject{
		Body: &api.CreateRefundJSONRequestBody{CaptureId: "invalid", Amount: 5000},
	}

	resp, err := handler.CreateRefund(context.Background(), req)

	require.NoError(t, err)
	badResp, ok := resp.(api.CreateRefund400JSONResponse)
	require.True(t, ok)
	assert.Equal(t, api.ErrorCodeCaptureNotFound, badResp.Error)
}

func TestGetRefund_Success(t *testing.T) {
	mockRefund := mocks.NewMockRefunder(t)
	handler := NewHandler(nil, nil, nil, mockRefund, nil, testLogger())

	captureID := uuid.New()
	refundID := uuid.New()

	mockRefund.On("GetRefund", mock.Anything, refundID).
		Return(&models.Transaction{
			ID:          refundID,
			ReferenceID: &captureID,
			AmountCents: 5000,
			Currency:    "USD",
			CreatedAt:   time.Now(),
		}, nil)

	req := api.GetRefundRequestObject{RefundId: "ref_" + refundID.String()}
	resp, err := handler.GetRefund(context.Background(), req)

	require.NoError(t, err)
	_, ok := resp.(api.GetRefund200JSONResponse)
	require.True(t, ok)
}

func TestGetRefund_NotFound(t *testing.T) {
	mockRefund := mocks.NewMockRefunder(t)
	handler := NewHandler(nil, nil, nil, mockRefund, nil, testLogger())

	refundID := uuid.New()
	mockRefund.On("GetRefund", mock.Anything, refundID).
		Return(nil, &service.ServiceError{Code: service.ErrCodeCaptureNotFound})

	req := api.GetRefundRequestObject{RefundId: "ref_" + refundID.String()}
	resp, err := handler.GetRefund(context.Background(), req)

	require.NoError(t, err)
	_, ok := resp.(api.GetRefund404JSONResponse)
	require.True(t, ok)
}
