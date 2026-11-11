package middleware

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecovery_ConvertsPanicToInternalError(t *testing.T) {
	logger, buf := newBufferedLogger(slog.LevelInfo)

	handler := RequestID(Recovery(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/captures", nil)
	req.Header.Set(RequestIDHeader, "panic-req")
	rec := httptest.NewRecorder()

	require.NotPanics(t, func() {
		handler.ServeHTTP(rec, req)
	})

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "panic-req", rec.Header().Get(RequestIDHeader))

	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "internal_error", body.Error)
	assert.Equal(t, "internal server error", body.Message)

	entry := decodeLogLine(t, buf)
	assert.Equal(t, "ERROR", entry["level"])
	assert.Equal(t, "panic-req", entry["request_id"])
	assert.Equal(t, "boom", entry["panic"])
	assert.Contains(t, entry["stack"], "middleware.TestRecovery")
}

func TestRecovery_DoesNotOverwriteStartedResponse(t *testing.T) {
	logger, _ := newBufferedLogger(slog.LevelInfo)

	handler := Recovery(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("partial")) //nolint:errcheck // test handler
		panic("after write")
	}))

	rec := httptest.NewRecorder()
	require.NotPanics(t, func() {
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/voids", nil))
	})

	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.Equal(t, "partial", rec.Body.String(), "recovery must not append an error to a committed body")
}

func TestRecovery_PropagatesErrAbortHandler(t *testing.T) {
	logger, _ := newBufferedLogger(slog.LevelInfo)
	handler := Recovery(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	assert.PanicsWithValue(t, http.ErrAbortHandler, func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/voids", nil))
	})
}
