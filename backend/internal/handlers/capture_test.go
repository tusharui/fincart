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

func TestCreateCapture_Success(t *testing.T) {
	mockCapture := mocks.NewMockCapturer(t)
	handler := NewHandler(nil, mockCapture, nil, nil, nil, testLogger())

	authID := uuid.New()
	captureID := uuid.New()

	mockCapture.On("Capture", mock.Anything, authID, int64(10000)).
		Return(&models.Transaction{
			ID:          captureID,
			ReferenceID: &authID,
			AmountCents: 10000,
			Currency:    "USD",
			CreatedAt:   time.Now(),
		}, nil)

	req := api.CreateCaptureRequestObject{
		Body: &api.CreateCaptureJSONRequestBody{
			AuthorizationId: "auth_" + authID.String(),
			Amount:          10000,
		},
	}

	resp, err := handler.CreateCapture(context.Background(), req)

	require.NoError(t, err)
	successResp, ok := resp.(api.CreateCapture200JSONResponse)
	require.True(t, ok)
	assert.Equal(t, api.CaptureResponseStatusCaptured, successResp.Status)
}

func TestCreateCapture_ServiceErrors(t *testing.T) {
	tests := []struct {
		name           string
		serviceErr     *service.ServiceError
		expectedCode   api.ErrorCode
		expectedStatus int
	}{
		{"auth not found", &service.ServiceError{Code: service.ErrCodeAuthNotFound}, api.ErrorCodeAuthorizationNotFound, 404},
		{"auth expired", &service.ServiceError{Code: service.ErrCodeAuthExpired}, api.ErrorCodeAuthorizationExpired, 409},
		{"already captured", &service.ServiceError{Code: service.ErrCodeAlreadyCaptured}, api.ErrorCodeAlreadyCaptured, 409},
		{"amount mismatch", &service.ServiceError{Code: service.ErrCodeAmountMismatch}, api.ErrorCodeAmountMismatch, 400},
		{"internal error", &service.ServiceError{Code: service.ErrCodeInternalError}, api.ErrorCodeInternalError, 500},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockCapture := mocks.NewMockCapturer(t)
			handler := NewHandler(nil, mockCapture, nil, nil, nil, testLogger())

			mockCapture.On("Capture", mock.Anything, mock.Anything, mock.Anything).
				Return(nil, tt.serviceErr)

			req := api.CreateCaptureRequestObject{
				Body: &api.CreateCaptureJSONRequestBody{
					AuthorizationId: "auth_" + uuid.New().String(),
					Amount:          10000,
				},
			}

			resp, err := handler.CreateCapture(context.Background(), req)
			require.NoError(t, err)

			switch tt.expectedStatus {
			case 404:
				typed, ok := resp.(api.CreateCapture404JSONResponse)
				require.True(t, ok, "expected 404 response, got %T", resp)
				assert.Equal(t, tt.expectedCode, typed.Error)
			case 409:
				typed, ok := resp.(api.CreateCapture409JSONResponse)
				require.True(t, ok, "expected 409 response, got %T", resp)
				assert.Equal(t, tt.expectedCode, typed.Error)
			case 500:
				typed, ok := resp.(api.CreateCapture500JSONResponse)
				require.True(t, ok, "expected 500 response, got %T", resp)
				assert.Equal(t, tt.expectedCode, typed.Error)
			default:
				typed, ok := resp.(api.CreateCapture400JSONResponse)
				require.True(t, ok, "expected 400 response, got %T", resp)
				assert.Equal(t, tt.expectedCode, typed.Error)
			}
		})
	}
}

func TestCreateCapture_InvalidIDFormat(t *testing.T) {
	handler := NewHandler(nil, nil, nil, nil, nil, testLogger())

	req := api.CreateCaptureRequestObject{
		Body: &api.CreateCaptureJSONRequestBody{
			AuthorizationId: "invalid",
			Amount:          10000,
		},
	}

	resp, err := handler.CreateCapture(context.Background(), req)

	require.NoError(t, err)
	badResp, ok := resp.(api.CreateCapture400JSONResponse)
	require.True(t, ok)
	assert.Equal(t, api.ErrorCodeAuthorizationNotFound, badResp.Error)
}

func TestGetCapture_Success(t *testing.T) {
	mockCapture := mocks.NewMockCapturer(t)
	handler := NewHandler(nil, mockCapture, nil, nil, nil, testLogger())

	authID := uuid.New()
	captureID := uuid.New()

	mockCapture.On("GetCapture", mock.Anything, captureID).
		Return(&models.Transaction{
			ID:          captureID,
			ReferenceID: &authID,
			AmountCents: 10000,
			Currency:    "USD",
			CreatedAt:   time.Now(),
		}, nil)

	req := api.GetCaptureRequestObject{CaptureId: "cap_" + captureID.String()}
	resp, err := handler.GetCapture(context.Background(), req)

	require.NoError(t, err)
	_, ok := resp.(api.GetCapture200JSONResponse)
	require.True(t, ok)
}

func TestGetCapture_StatusMapping(t *testing.T) {
	tests := []struct {
		name     string
		status   models.TransactionStatus
		expected api.CaptureResponseStatus
	}{
		{"completed maps to captured", models.TransactionStatusCompleted, api.CaptureResponseStatusCaptured},
		{"refunded maps to refunded", models.TransactionStatusRefunded, api.CaptureResponseStatusRefunded},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockCapture := mocks.NewMockCapturer(t)
			handler := NewHandler(nil, mockCapture, nil, nil, nil, testLogger())

			authID := uuid.New()
			captureID := uuid.New()
			mockCapture.On("GetCapture", mock.Anything, captureID).
				Return(&models.Transaction{
					ID:          captureID,
					ReferenceID: &authID,
					AmountCents: 10000,
					Currency:    "USD",
					Status:      tt.status,
					CreatedAt:   time.Now(),
				}, nil)

			resp, err := handler.GetCapture(context.Background(), api.GetCaptureRequestObject{
				CaptureId: "cap_" + captureID.String(),
			})

			require.NoError(t, err)
			typed, ok := resp.(api.GetCapture200JSONResponse)
			require.True(t, ok)
			assert.Equal(t, tt.expected, typed.Status)
		})
	}
}

func TestGetCapture_NotFound(t *testing.T) {
	mockCapture := mocks.NewMockCapturer(t)
	handler := NewHandler(nil, mockCapture, nil, nil, nil, testLogger())

	captureID := uuid.New()
	mockCapture.On("GetCapture", mock.Anything, captureID).
		Return(nil, &service.ServiceError{Code: service.ErrCodeCaptureNotFound})

	req := api.GetCaptureRequestObject{CaptureId: "cap_" + captureID.String()}
	resp, err := handler.GetCapture(context.Background(), req)

	require.NoError(t, err)
	_, ok := resp.(api.GetCapture404JSONResponse)
	require.True(t, ok)
}
