// Package respond provides helpers for writing consistent JSON API responses.
package respond

import (
	"encoding/json"
	"net/http"
)

// errorBody mirrors the ErrorResponse schema from the OpenAPI spec.
type errorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// Error writes a JSON error response with the given HTTP status code.
func Error(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	// The header has already been sent, so a write failure cannot change the
	// response. Best-effort write is intentional.
	//nolint:errcheck // best effort response writing
	_ = json.NewEncoder(w).Encode(errorBody{Error: code, Message: message})
}
