package middleware

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/tusharui/payment-gateway/backend/internal/respond"
)

// statusWriter tracks whether the response has started so a recovered panic
// does not try to replace an already committed status or body.
type statusWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.wroteHeader = true
	return w.ResponseWriter.Write(b)
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// Recovery converts a panic in any downstream handler into a logged 500
// response instead of a dropped connection. The panic value and stack are
// logged with the request ID so the failure can be correlated with the access
// log. If the response has already started there is nothing safe to write, so
// only the log entry is produced.
func Recovery(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writer := &statusWriter{ResponseWriter: w}

			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}

				// net/http uses ErrAbortHandler to abort a response without
				// logging. Preserve that signal for the server.
				if err, ok := recovered.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(recovered)
				}

				logger.Error("recovered panic while serving request",
					"request_id", RequestIDFromContext(r.Context()),
					"method", r.Method,
					"path", r.URL.Path,
					"panic", fmt.Sprint(recovered),
					"stack", string(debug.Stack()),
				)

				if !writer.wroteHeader {
					respond.Error(writer, http.StatusInternalServerError,
						"internal_error", "internal server error")
				}
			}()

			next.ServeHTTP(writer, r)
		})
	}
}
