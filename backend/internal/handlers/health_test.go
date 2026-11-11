package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tusharui/payment-gateway/backend/internal/api"
)

type stubHealthChecker struct {
	err error
}

func (s stubHealthChecker) PingContext(context.Context) error {
	return s.err
}

func TestGetHealth_AlwaysReportsHealthy(t *testing.T) {
	// Liveness must not depend on the database, so the checker is nil here.
	handler := NewHandler(nil, nil, nil, nil, nil, testLogger())

	resp, err := handler.GetHealth(context.Background(), api.GetHealthRequestObject{})
	require.NoError(t, err)

	assert.Equal(t, api.GetHealth200JSONResponse{Status: api.Healthy}, resp)
}

func TestGetReady_ReportsReadyWhenDatabaseReachable(t *testing.T) {
	handler := NewHandler(nil, nil, nil, nil, stubHealthChecker{}, testLogger())

	resp, err := handler.GetReady(context.Background(), api.GetReadyRequestObject{})
	require.NoError(t, err)

	assert.Equal(t, api.GetReady200JSONResponse{Status: api.Ready}, resp)
}

func TestGetReady_ReportsNotReadyWhenDatabaseUnreachable(t *testing.T) {
	handler := NewHandler(nil, nil, nil, nil, stubHealthChecker{
		err: errors.New("connection refused"),
	}, testLogger())

	resp, err := handler.GetReady(context.Background(), api.GetReadyRequestObject{})
	require.NoError(t, err)

	assert.Equal(t, api.GetReady503JSONResponse{Status: api.NotReady}, resp)
}

func TestGetReady_FailsClosedWithoutChecker(t *testing.T) {
	handler := NewHandler(nil, nil, nil, nil, nil, testLogger())

	resp, err := handler.GetReady(context.Background(), api.GetReadyRequestObject{})
	require.NoError(t, err)

	assert.Equal(t, api.GetReady503JSONResponse{Status: api.NotReady}, resp)
}
