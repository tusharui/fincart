package middleware

import (
	"context"
	"net/http"
	"regexp"

	"github.com/google/uuid"
)

// RequestIDHeader is the request/response header carrying the correlation ID.
const RequestIDHeader = "X-Request-Id"

// validRequestID accepts opaque tokens produced by clients and upstream
// proxies (UUIDs, trace IDs, load-balancer IDs). Restricting the character set
// keeps the value safe to write to structured logs and response headers.
var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type requestIDKey struct{}

// RequestID propagates a correlation ID for the whole request. An inbound ID is
// reused when it is well formed so traces span multiple services; otherwise a
// new UUID is generated. The resolved ID is always echoed in the response
// header and made available through RequestIDFromContext.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if !validRequestID.MatchString(id) {
			id = uuid.NewString()
		}

		w.Header().Set(RequestIDHeader, id)
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestIDFromContext returns the correlation ID attached by RequestID, or an
// empty string when the middleware is not installed.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}
