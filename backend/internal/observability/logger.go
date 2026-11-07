// Package observability provides structured logging helpers for the bank API.
package observability

import (
	"context"
	"log/slog"
	"os"
)

// NewJSONLogger returns a JSON structured logger that writes to stdout.
func NewJSONLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

// FromContext resolves the provided logger or falls back to the default logger.
func FromContext(ctx context.Context, logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.Default()
	}
	return logger
}
