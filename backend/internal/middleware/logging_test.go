package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newBufferedLogger(level slog.Level) (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level}))

	return logger, buf
}

func decodeLogLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()

	var entry map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &entry))

	return entry
}

func TestAccessLog_RecordsCompletedRequest(t *testing.T) {
	logger, buf := newBufferedLogger(slog.LevelInfo)

	handler := RequestID(AccessLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("hello")) //nolint:errcheck // test handler
	})))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/authorizations", nil)
	req.Header.Set(RequestIDHeader, "req-123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)

	entry := decodeLogLine(t, buf)
	assert.Equal(t, "INFO", entry["level"])
	assert.Equal(t, "http request", entry["msg"])
	assert.Equal(t, "req-123", entry["request_id"])
	assert.Equal(t, http.MethodPost, entry["method"])
	assert.Equal(t, "/api/v1/authorizations", entry["path"])
	assert.Equal(t, float64(http.StatusCreated), entry["status"])
	assert.Equal(t, float64(len("hello")), entry["bytes"])
	assert.Contains(t, entry, "duration_ms")
}

func TestAccessLog_LevelMatchesStatus(t *testing.T) {
	tests := []struct {
		status int
		level  string
	}{
		{http.StatusOK, "INFO"},
		{http.StatusNoContent, "INFO"},
		{http.StatusBadRequest, "WARN"},
		{http.StatusNotFound, "WARN"},
		{http.StatusInternalServerError, "ERROR"},
		{http.StatusServiceUnavailable, "ERROR"},
	}

	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			logger, buf := newBufferedLogger(slog.LevelInfo)
			handler := AccessLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
			}))

			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/captures", nil))

			entry := decodeLogLine(t, buf)
			assert.Equal(t, tt.level, entry["level"])
			assert.Equal(t, float64(tt.status), entry["status"])
		})
	}
}

func TestAccessLog_DefaultsStatusToOK(t *testing.T) {
	logger, buf := newBufferedLogger(slog.LevelInfo)
	handler := AccessLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok")) //nolint:errcheck // test handler
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/refunds", nil))

	entry := decodeLogLine(t, buf)
	assert.Equal(t, float64(http.StatusOK), entry["status"])
}

func TestAccessLog_ProbesAreDebugOnly(t *testing.T) {
	for _, path := range []string{"/health", "/ready", "/metrics"} {
		t.Run(path, func(t *testing.T) {
			logger, buf := newBufferedLogger(slog.LevelInfo)
			handler := AccessLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
			assert.Empty(t, buf.String(), "successful probes must not log at info level")

			debugLogger, debugBuf := newBufferedLogger(slog.LevelDebug)
			debugHandler := AccessLog(debugLogger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			debugHandler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))

			entry := decodeLogLine(t, debugBuf)
			assert.Equal(t, "DEBUG", entry["level"])
		})
	}
}
