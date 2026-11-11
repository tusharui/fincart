// Package observability provides structured logging and metrics for the bank
// API.
package observability

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics owns the Prometheus registry and the API's instrumented collectors.
// Each router gets its own registry, which keeps test servers isolated.
type Metrics struct {
	registry *prometheus.Registry

	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	inflight *prometheus.GaugeVec
}

// NewMetrics builds a registry with HTTP, Go runtime, process, and database
// pool metrics. A nil database skips the pool collector.
func NewMetrics(database *sql.DB) *Metrics {
	registry := prometheus.NewRegistry()

	metrics := &Metrics{
		registry: registry,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests by method, route, and status code.",
		}, []string{"method", "route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency in seconds by method and route.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
		inflight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "http_inflight_requests",
			Help: "Current number of in-flight HTTP requests by method.",
		}, []string{"method"}),
	}

	registry.MustRegister(
		metrics.requests,
		metrics.duration,
		metrics.inflight,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	if database != nil {
		registry.MustRegister(&dbPoolCollector{db: database})
	}

	return metrics
}

// Registry exposes the underlying registry for tests and custom collectors.
func (m *Metrics) Registry() *prometheus.Registry {
	return m.registry
}

// Handler serves the Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// Middleware records request count, latency, and in-flight requests. It must be
// installed outside handlers that short-circuit requests (idempotency,
// failure injection) so those responses are measured too.
func (m *Metrics) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Scrapes are operational traffic and would otherwise inflate
			// their own metrics on every collection.
			if r.URL.Path == "/metrics" {
				next.ServeHTTP(w, r)
				return
			}

			holder := &routeHolder{}
			ctx := context.WithValue(r.Context(), routeContextKey{}, holder)

			method := methodLabel(r.Method)
			recorder := &statusRecorder{ResponseWriter: w}

			m.inflight.WithLabelValues(method).Inc()
			start := time.Now()
			defer func() {
				m.inflight.WithLabelValues(method).Dec()

				route := normalizeRoute(holder.pattern)
				status := recorder.status
				if status == 0 {
					status = http.StatusOK
				}

				m.requests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
				m.duration.WithLabelValues(method, route).Observe(time.Since(start).Seconds())
			}()

			next.ServeHTTP(recorder, r.WithContext(ctx))
		})
	}
}

// CaptureRoute records the ServeMux pattern that matched a request. It must
// wrap the mux directly so the pattern is available after routing, including
// for requests that other middleware short-circuits before reaching the mux.
func CaptureRoute(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)

		holder, ok := r.Context().Value(routeContextKey{}).(*routeHolder)
		if ok && r.Pattern != "" {
			holder.pattern = r.Pattern
		}
	})
}

type routeContextKey struct{}

type routeHolder struct {
	pattern string
}

// normalizeRoute strips the method prefix from Go 1.22+ ServeMux patterns and
// buckets requests that never matched a route to keep label cardinality
// bounded.
func normalizeRoute(pattern string) string {
	if pattern == "" {
		return "unmatched"
	}

	if _, path, found := strings.Cut(pattern, " "); found {
		return path
	}

	return pattern
}

// methodLabel keeps the method label set bounded for non-standard methods.
func methodLabel(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodConnect:
		return method
	default:
		return "OTHER"
	}
}

// statusRecorder captures the status code written by downstream handlers.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// dbPoolCollector exposes database/sql pool saturation, which is the earliest
// signal of connection exhaustion under load.
type dbPoolCollector struct {
	db *sql.DB
}

var (
	dbOpenDesc = prometheus.NewDesc(
		"db_pool_open_connections",
		"Number of established database connections.",
		nil, nil,
	)
	dbInUseDesc = prometheus.NewDesc(
		"db_pool_in_use_connections",
		"Number of database connections currently in use.",
		nil, nil,
	)
	dbIdleDesc = prometheus.NewDesc(
		"db_pool_idle_connections",
		"Number of idle database connections.",
		nil, nil,
	)
	dbWaitCountDesc = prometheus.NewDesc(
		"db_pool_wait_count_total",
		"Total number of times a caller waited for a database connection.",
		nil, nil,
	)
	dbWaitDurationDesc = prometheus.NewDesc(
		"db_pool_wait_duration_seconds_total",
		"Total time callers spent waiting for a database connection.",
		nil, nil,
	)
	dbMaxOpenDesc = prometheus.NewDesc(
		"db_pool_max_open_connections",
		"Configured maximum number of open database connections.",
		nil, nil,
	)
)

func (c *dbPoolCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- dbOpenDesc
	ch <- dbInUseDesc
	ch <- dbIdleDesc
	ch <- dbWaitCountDesc
	ch <- dbWaitDurationDesc
	ch <- dbMaxOpenDesc
}

func (c *dbPoolCollector) Collect(ch chan<- prometheus.Metric) {
	stats := c.db.Stats()

	ch <- prometheus.MustNewConstMetric(dbOpenDesc, prometheus.GaugeValue, float64(stats.OpenConnections))
	ch <- prometheus.MustNewConstMetric(dbInUseDesc, prometheus.GaugeValue, float64(stats.InUse))
	ch <- prometheus.MustNewConstMetric(dbIdleDesc, prometheus.GaugeValue, float64(stats.Idle))
	ch <- prometheus.MustNewConstMetric(dbWaitCountDesc, prometheus.CounterValue, float64(stats.WaitCount))
	ch <- prometheus.MustNewConstMetric(dbWaitDurationDesc, prometheus.CounterValue, stats.WaitDuration.Seconds())
	ch <- prometheus.MustNewConstMetric(dbMaxOpenDesc, prometheus.GaugeValue, float64(stats.MaxOpenConnections))
}
