package observability

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// metricSample is one labelled sample from a gathered metric family.
type metricSample struct {
	labels map[string]string
	value  float64
}

func gatherFamily(t *testing.T, registry *prometheus.Registry, name string) []metricSample {
	t.Helper()

	families, err := registry.Gather()
	require.NoError(t, err)

	for _, family := range families {
		if family.GetName() != name {
			continue
		}

		samples := make([]metricSample, 0, len(family.Metric))
		for _, metric := range family.Metric {
			labels := make(map[string]string, len(metric.Label))
			for _, pair := range metric.Label {
				labels[pair.GetName()] = pair.GetValue()
			}

			samples = append(samples, metricSample{
				labels: labels,
				value:  counterValue(metric),
			})
		}

		return samples
	}

	return nil
}

func counterValue(metric *dto.Metric) float64 {
	switch {
	case metric.Counter != nil:
		return metric.Counter.GetValue()
	case metric.Gauge != nil:
		return metric.Gauge.GetValue()
	case metric.Untyped != nil:
		return metric.Untyped.GetValue()
	case metric.Histogram != nil:
		return float64(metric.Histogram.GetSampleCount())
	default:
		return 0
	}
}

func findSample(samples []metricSample, labels map[string]string) (metricSample, bool) {
	for _, sample := range samples {
		matches := true
		for key, want := range labels {
			if sample.labels[key] != want {
				matches = false
				break
			}
		}
		if matches {
			return sample, true
		}
	}

	return metricSample{}, false
}

func TestHTTPMetrics_RecordsRequestsByRouteAndStatus(t *testing.T) {
	metrics := NewMetrics(nil)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/authorizations", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /api/v1/captures", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})

	handler := metrics.Middleware()(CaptureRoute(mux))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/authorizations", nil))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/captures", nil))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/no-such-route", nil))

	requests := gatherFamily(t, metrics.Registry(), "http_requests_total")
	require.NotEmpty(t, requests)

	sample, found := findSample(requests, map[string]string{
		"method": http.MethodGet,
		"route":  "/api/v1/authorizations",
		"status": "200",
	})
	require.True(t, found)
	assert.Equal(t, float64(1), sample.value)

	sample, found = findSample(requests, map[string]string{
		"method": http.MethodPost,
		"route":  "/api/v1/captures",
		"status": "400",
	})
	require.True(t, found)
	assert.Equal(t, float64(1), sample.value)

	sample, found = findSample(requests, map[string]string{
		"method": http.MethodGet,
		"route":  "unmatched",
		"status": "404",
	})
	require.True(t, found)
	assert.Equal(t, float64(1), sample.value)

	durations := gatherFamily(t, metrics.Registry(), "http_request_duration_seconds")
	require.NotEmpty(t, durations)
	_, found = findSample(durations, map[string]string{
		"method": http.MethodGet,
		"route":  "/api/v1/authorizations",
	})
	assert.True(t, found)
}

func TestHTTPMetrics_TracksInFlightRequests(t *testing.T) {
	metrics := NewMetrics(nil)

	entered := make(chan struct{})
	release := make(chan struct{})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusOK)
	})

	handler := metrics.Middleware()(CaptureRoute(mux))
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/slow", nil))
	}()

	<-entered
	inflight := gatherFamily(t, metrics.Registry(), "http_inflight_requests")
	sample, found := findSample(inflight, map[string]string{"method": http.MethodGet})
	require.True(t, found)
	assert.Equal(t, float64(1), sample.value)

	close(release)
	<-done

	inflight = gatherFamily(t, metrics.Registry(), "http_inflight_requests")
	sample, found = findSample(inflight, map[string]string{"method": http.MethodGet})
	require.True(t, found)
	assert.Equal(t, float64(0), sample.value)
}

func TestMetricsHandler_ServesPrometheusFormat(t *testing.T) {
	metrics := NewMetrics(nil)

	// A CounterVec does not appear in the exposition until it has a sample.
	handler := metrics.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", nil))

	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/plain")
	assert.Contains(t, rec.Body.String(), "go_goroutines")
	assert.Contains(t, rec.Body.String(), "http_requests_total")
}

func TestMetrics_DatabasePoolCollector(t *testing.T) {
	database, err := sql.Open("postgres", "host=localhost port=1 user=test dbname=test sslmode=disable")
	require.NoError(t, err)
	defer func() {
		_ = database.Close() //nolint:errcheck // test cleanup
	}()

	metrics := NewMetrics(database)
	samples := gatherFamily(t, metrics.Registry(), "db_pool_max_open_connections")
	require.Len(t, samples, 1)
	assert.GreaterOrEqual(t, samples[0].value, float64(0))
}

func TestNormalizeRoute(t *testing.T) {
	assert.Equal(t, "/api/v1/captures", normalizeRoute("POST /api/v1/captures"))
	assert.Equal(t, "/docs/{path...}", normalizeRoute("/docs/{path...}"))
	assert.Equal(t, "/health", normalizeRoute("GET /health"))
	assert.Equal(t, "unmatched", normalizeRoute(""))
}

func TestMethodLabel_BucketsUnknownMethods(t *testing.T) {
	assert.Equal(t, http.MethodGet, methodLabel(http.MethodGet))
	assert.Equal(t, "OTHER", methodLabel("MADEUP"))
}
