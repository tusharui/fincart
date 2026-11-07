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

func TestCreateVoid_Success(t *testing.T) {
	mockVoid := mocks.NewMockVoider(t)
	handler := NewHandler(nil, nil, mockVoid, nil, nil, testLogger())

	authID := uuid.New()
	voidID := uuid.New()

	mockVoid.On("Void", mock.Anything, authID).
		Return(&models.Transaction{
			ID:          voidID,
			ReferenceID: &authID,
			CreatedAt:   time.Now(),
		}, nil)

	req := api.CreateVoidRequestObject{
		Body: &api.CreateVoidJSONRequestBody{AuthorizationId: "auth_" + authID.String()},
	}

	resp, err := handler.CreateVoid(context.Background(), req)

	require.NoError(t, err)
	successResp, ok := resp.(api.CreateVoid200JSONResponse)
	require.True(t, ok)
	assert.Equal(t, api.Voided, successResp.Status)
}

func TestCreateVoid_ServiceErrors(t *testing.T) {
	tests := []struct {
		name           string
		serviceErr     *service.ServiceError
		expectedCode   api.ErrorCode
		expectedStatus int
	}{
		{"auth not found", &service.ServiceError{Code: service.ErrCodeAuthNotFound}, api.ErrorCodeAuthorizationNotFound, 404},
		{"already voided", &service.ServiceError{Code: service.ErrCodeAlreadyVoided}, api.ErrorCodeAlreadyVoided, 409},
		{"auth already used", &service.ServiceError{Code: service.ErrCodeAuthAlreadyUsed}, api.ErrorCodeAuthorizationAlreadyUsed, 409},
		{"already captured", &service.ServiceError{Code: service.ErrCodeAlreadyCaptured}, api.ErrorCodeAlreadyCaptured, 409},
		{"internal error", &service.ServiceError{Code: service.ErrCodeInternalError}, api.ErrorCodeInternalError, 500},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockVoid := mocks.NewMockVoider(t)
			handler := NewHandler(nil, nil, mockVoid, nil, nil, testLogger())

			mockVoid.On("Void", mock.Anything, mock.Anything).Return(nil, tt.serviceErr)

			req := api.CreateVoidRequestObject{
				Body: &api.CreateVoidJSONRequestBody{AuthorizationId: "auth_" + uuid.New().String()},
			}

			resp, err := handler.CreateVoid(context.Background(), req)
			require.NoError(t, err)

			switch tt.expectedStatus {
			case 404:
				typed, ok := resp.(api.CreateVoid404JSONResponse)
				require.True(t, ok, "expected 404 response, got %T", resp)
				assert.Equal(t, tt.expectedCode, typed.Error)
			case 409:
				typed, ok := resp.(api.CreateVoid409JSONResponse)
				require.True(t, ok, "expected 409 response, got %T", resp)
				assert.Equal(t, tt.expectedCode, typed.Error)
			case 500:
				typed, ok := resp.(api.CreateVoid500JSONResponse)
				require.True(t, ok, "expected 500 response, got %T", resp)
				assert.Equal(t, tt.expectedCode, typed.Error)
			default:
				typed, ok := resp.(api.CreateVoid400JSONResponse)
				require.True(t, ok, "expected 400 response, got %T", resp)
				assert.Equal(t, tt.expectedCode, typed.Error)
			}
		})
	}
}

func TestCreateVoid_InvalidIDFormat(t *testing.T) {
	handler := NewHandler(nil, nil, nil, nil, nil, testLogger())

	req := api.CreateVoidRequestObject{
		Body: &api.CreateVoidJSONRequestBody{AuthorizationId: "invalid"},
	}

	resp, err := handler.CreateVoid(context.Background(), req)

	require.NoError(t, err)
	badResp, ok := resp.(api.CreateVoid400JSONResponse)
	require.True(t, ok)
	assert.Equal(t, api.ErrorCodeAuthorizationNotFound, badResp.Error)
}
