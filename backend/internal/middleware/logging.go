package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

// responseRecorder captures the status code and body size written by a handler
// so the access log can describe the completed response.
type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *responseRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}

	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)

	return n, err
}

// Unwrap exposes the underlying writer to http.ResponseController so handlers
// can still flush or hijack the connection through the recorder.
func (r *responseRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// AccessLog emits one structured line per request with the method, path,
// status, latency, and response size. Server errors are logged at error level,
// client errors at warn, and successful requests at info. Probe and metrics
// endpoints are logged at debug so orchestration traffic does not drown out
// real requests.
func AccessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			recorder := &responseRecorder{ResponseWriter: w}

			next.ServeHTTP(recorder, r)

			status := recorder.status
			if status == 0 {
				status = http.StatusOK
			}

			attrs := []any{
				"request_id", RequestIDFromContext(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", status,
				"duration_ms", float64(time.Since(start).Microseconds()) / 1000,
				"bytes", recorder.bytes,
				"remote_addr", r.RemoteAddr,
			}

			switch {
			case status >= http.StatusInternalServerError:
				logger.Error("http request", attrs...)
			case isProbePath(r.URL.Path):
				logger.Debug("http request", attrs...)
			case status >= http.StatusBadRequest:
				logger.Warn("http request", attrs...)
			default:
				logger.Info("http request", attrs...)
			}
		})
	}
}

// isProbePath reports whether a path is operational traffic whose successful
// requests should not be logged at info level.
func isProbePath(path string) bool {
	switch path {
	case "/health", "/ready", "/metrics":
		return true
	default:
		return false
	}
}
