package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestID_GeneratesIDWhenAbsent(t *testing.T) {
	var seen string
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/authorizations", nil))

	header := rec.Header().Get(RequestIDHeader)
	require.NotEmpty(t, header)
	assert.Equal(t, header, seen, "context and response header must match")

	_, err := uuid.Parse(header)
	require.NoError(t, err, "generated request ID must be a UUID")
}

func TestRequestID_PreservesValidInboundID(t *testing.T) {
	const inbound = "trace-abc.123:4"

	var seen string
	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestIDFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set(RequestIDHeader, inbound)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, inbound, seen)
	assert.Equal(t, inbound, rec.Header().Get(RequestIDHeader))
}

func TestRequestID_ReplacesUnsafeInboundID(t *testing.T) {
	tests := []struct {
		name    string
		inbound string
	}{
		{"log injection", "abc\nlevel=ERROR"},
		{"whitespace", "abc def"},
		{"too long", strings.Repeat("a", 129)},
		{"empty-ish", "   "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.NotEqual(t, tt.inbound, RequestIDFromContext(r.Context()))
			}))

			req := httptest.NewRequest(http.MethodGet, "/health", nil)
			req.Header.Set(RequestIDHeader, tt.inbound)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			generated := rec.Header().Get(RequestIDHeader)
			require.NotEmpty(t, generated)
			assert.NotEqual(t, tt.inbound, generated)
			_, err := uuid.Parse(generated)
			require.NoError(t, err)
		})
	}
}

func TestRequestIDFromContext_EmptyWithoutMiddleware(t *testing.T) {
	assert.Empty(t, RequestIDFromContext(context.Background()))
}
