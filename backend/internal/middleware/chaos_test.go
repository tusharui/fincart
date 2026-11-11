package middleware

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tusharui/payment-gateway/backend/internal/config"
)

func TestFailureInjection_DisabledIsPassThrough(t *testing.T) {
	logger, _ := newBufferedLogger(slog.LevelInfo)
	cfg := &config.AppConfig{
		ChaosEnabled: false,
		FailureRate:  1,
		MinLatencyMS: 100,
		MaxLatencyMS: 100,
	}

	called := false
	handler := FailureInjection(cfg, logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	start := time.Now()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/authorizations", nil))

	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Less(t, time.Since(start), 50*time.Millisecond, "disabled chaos must not add latency")
}

func TestFailureInjection_DisabledWarnsAboutStaleSettings(t *testing.T) {
	logger, buf := newBufferedLogger(slog.LevelInfo)
	cfg := &config.AppConfig{FailureRate: 0.05, MinLatencyMS: 100}

	FailureInjection(cfg, logger)

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	assert.Equal(t, "WARN", entry["level"])
	assert.Contains(t, entry["msg"], "ignored because chaos is disabled")
}

func TestFailureInjection_EnabledInjectsFailures(t *testing.T) {
	logger, buf := newBufferedLogger(slog.LevelInfo)
	cfg := &config.AppConfig{ChaosEnabled: true, FailureRate: 1}

	called := false
	handler := FailureInjection(cfg, logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/captures", nil))

	assert.False(t, called)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	var body struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "internal_error", body.Error)

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	assert.Equal(t, "WARN", entry["level"])
	assert.Contains(t, entry["msg"], "failure injection enabled")
}

func TestFailureInjection_EnabledSkipsProbesAndMetrics(t *testing.T) {
	logger, _ := newBufferedLogger(slog.LevelInfo)
	cfg := &config.AppConfig{ChaosEnabled: true, FailureRate: 1}

	handler := FailureInjection(cfg, logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, path := range []string{"/health", "/ready", "/metrics", "/docs"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			assert.Equal(t, http.StatusOK, rec.Code)
		})
	}
}

func TestFailureInjection_EnabledAddsLatency(t *testing.T) {
	logger, _ := newBufferedLogger(slog.LevelInfo)
	cfg := &config.AppConfig{ChaosEnabled: true, MinLatencyMS: 20, MaxLatencyMS: 20}

	handler := FailureInjection(cfg, logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	start := time.Now()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/refunds", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.GreaterOrEqual(t, time.Since(start), 15*time.Millisecond)
}
