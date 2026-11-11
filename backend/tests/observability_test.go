package tests

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestObservability_RequestIDIsGeneratedAndEchoed(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	t.Run("generates an ID when absent", func(t *testing.T) {
		resp, err := http.Get(ts.URL("/health"))
		require.NoError(t, err)
		defer resp.Body.Close()

		require.Equal(t, http.StatusOK, resp.StatusCode)

		requestID := resp.Header.Get("X-Request-Id")
		require.NotEmpty(t, requestID)
		_, err = uuid.Parse(requestID)
		require.NoError(t, err)
	})

	t.Run("preserves a well-formed inbound ID", func(t *testing.T) {
		const inbound = "integration-trace-123"

		req, err := http.NewRequest(http.MethodGet, ts.URL("/health"), nil)
		require.NoError(t, err)
		req.Header.Set("X-Request-Id", inbound)

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, inbound, resp.Header.Get("X-Request-Id"))
	})

	t.Run("replaces an unsafe inbound ID", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet, ts.URL("/health"), nil)
		require.NoError(t, err)
		req.Header.Set("X-Request-Id", "bad id with spaces")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()

		echoed := resp.Header.Get("X-Request-Id")
		assert.NotEqual(t, "bad id with spaces", echoed)
		_, err = uuid.Parse(echoed)
		require.NoError(t, err)
	})
}

func TestObservability_LivenessAndReadiness(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	healthResp, err := http.Get(ts.URL("/health"))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, healthResp.StatusCode)
	assert.Equal(t, "healthy", decodeJSONBody(t, healthResp)["status"])

	readyResp, err := http.Get(ts.URL("/ready"))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, readyResp.StatusCode)
	assert.Equal(t, "ready", decodeJSONBody(t, readyResp)["status"])
}

func TestObservability_MetricsCaptureTraffic(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	authResp := ts.Authorize(t, "4111111111111111", "123", 10000, "metrics-auth")
	require.Equal(t, http.StatusOK, authResp.StatusCode)
	authID := decodeJSONBody(t, authResp)["authorization_id"].(string)

	// A mismatched capture is rejected with 400 and must still be measured.
	failedCapture := ts.Capture(t, authID, 9000, "metrics-bad-capture")
	require.Equal(t, http.StatusBadRequest, failedCapture.StatusCode)
	failedCapture.Body.Close()

	notFound, err := http.Get(ts.URL("/no-such-route"))
	require.NoError(t, err)
	notFound.Body.Close()
	require.Equal(t, http.StatusNotFound, notFound.StatusCode)

	metricsResp, err := http.Get(ts.URL("/metrics"))
	require.NoError(t, err)
	defer metricsResp.Body.Close()
	require.Equal(t, http.StatusOK, metricsResp.StatusCode)

	body, err := io.ReadAll(metricsResp.Body)
	require.NoError(t, err)
	exposition := string(body)

	expected := []string{
		`http_requests_total{method="POST",route="/api/v1/authorizations",status="200"}`,
		`http_requests_total{method="POST",route="/api/v1/captures",status="400"}`,
		`http_requests_total{method="GET",route="unmatched",status="404"}`,
		"http_request_duration_seconds_bucket",
		"db_pool_open_connections",
		"go_goroutines",
	}

	for _, want := range expected {
		assert.Contains(t, exposition, want)
	}

	assert.NotContains(t, exposition, `route="/metrics"`,
		"metric scrapes must not instrument themselves")
}

func TestObservability_RequestBodyAndHeadersAreNotLeakedInMetrics(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	resp := ts.Authorize(t, "4111111111111111", "123", 4321, "metrics-leak-check")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	metricsResp, err := http.Get(ts.URL("/metrics"))
	require.NoError(t, err)
	defer metricsResp.Body.Close()

	body, err := io.ReadAll(metricsResp.Body)
	require.NoError(t, err)
	exposition := string(body)

	assert.NotContains(t, exposition, "4111111111111111")
	assert.NotContains(t, exposition, "Idempotency-Key")
	assert.NotContains(t, exposition, "metrics-leak-check")
}

func TestObservability_ErrorResponsesKeepJSONShape(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	// A missing Idempotency-Key is rejected by the strict server with the
	// shared ErrorResponse shape, independent of observability middleware.
	req, err := http.NewRequest(http.MethodPost, ts.URL("/api/v1/authorizations"),
		strings.NewReader(`{"card_number":"4111111111111111","cvv":"123","amount":100}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "missing_idempotency_key", body.Error)
	assert.NotEmpty(t, body.Message)
}
