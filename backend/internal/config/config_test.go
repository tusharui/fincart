package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDatabaseConfig_DSN(t *testing.T) {
	t.Run("uses DATABASE_URL when set", func(t *testing.T) {
		cfg := &DatabaseConfig{
			URL:    "postgresql://user:pass@example.com:5432/db?sslmode=require",
			Host:   "ignored",
			DBName: "ignored",
		}

		assert.Equal(t, "postgresql://user:pass@example.com:5432/db?sslmode=require", cfg.DSN())
	})

	t.Run("strips channel_binding for lib/pq", func(t *testing.T) {
		cfg := &DatabaseConfig{
			URL: "postgresql://user:pass@example.com:5432/db?sslmode=require&channel_binding=require",
		}

		dsn := cfg.DSN()
		assert.NotContains(t, dsn, "channel_binding")
		assert.Contains(t, dsn, "sslmode=require")
	})

	t.Run("falls back to individual fields", func(t *testing.T) {
		cfg := &DatabaseConfig{
			Host:     "localhost",
			Port:     "5432",
			User:     "postgres",
			Password: "secret",
			DBName:   "mockbank",
			SSLMode:  "disable",
		}

		assert.Equal(t,
			"host=localhost port=5432 user=postgres password=secret dbname=mockbank sslmode=disable",
			cfg.DSN(),
		)
	})
}

func TestWebhookConfig_Validate(t *testing.T) {
	valid := func() WebhookConfig {
		return WebhookConfig{
			Enabled:       true,
			Endpoints:     []string{"https://example.test/webhooks"},
			Secret:        "whsec_test",
			MaxAttempts:   5,
			Timeout:       2 * time.Second,
			BatchSize:     25,
			PollInterval:  time.Second,
			LeaseDuration: 30 * time.Second,
			BaseBackoff:   time.Second,
			MaxBackoff:    time.Minute,
		}
	}

	t.Run("disabled webhooks need no configuration", func(t *testing.T) {
		cfg := WebhookConfig{}
		require.NoError(t, cfg.validate())
	})

	t.Run("accepts a complete configuration", func(t *testing.T) {
		cfg := valid()
		require.NoError(t, cfg.validate())
	})

	tests := []struct {
		name   string
		mutate func(*WebhookConfig)
	}{
		{"requires an endpoint", func(c *WebhookConfig) { c.Endpoints = nil }},
		{"requires a secret", func(c *WebhookConfig) { c.Secret = "" }},
		{"rejects a relative endpoint", func(c *WebhookConfig) { c.Endpoints = []string{"/webhooks"} }},
		{"rejects a non-http scheme", func(c *WebhookConfig) { c.Endpoints = []string{"ftp://example.test/hook"} }},
		{"requires an attempt budget", func(c *WebhookConfig) { c.MaxAttempts = 0 }},
		{"requires a positive timeout", func(c *WebhookConfig) { c.Timeout = 0 }},
		{"lease must exceed timeout", func(c *WebhookConfig) { c.LeaseDuration = c.Timeout }},
		{"max backoff must cover base backoff", func(c *WebhookConfig) { c.MaxBackoff = c.BaseBackoff / 2 }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid()
			tt.mutate(&cfg)
			assert.Error(t, cfg.validate())
		})
	}
}

func TestConfig_Validate(t *testing.T) {
	t.Run("accepts DATABASE_URL without host or database name", func(t *testing.T) {
		cfg := &Config{
			Server:   ServerConfig{Port: "8080"},
			Database: DatabaseConfig{URL: "postgresql://user:pass@example.com/db"},
			App:      AppConfig{MinLatencyMS: 0, MaxLatencyMS: 0},
			Logger:   LoggerConfig{Level: "info"},
		}

		require.NoError(t, cfg.Validate())
	})

	t.Run("requires host and database name without DATABASE_URL", func(t *testing.T) {
		cfg := &Config{
			Server:   ServerConfig{Port: "8080"},
			Database: DatabaseConfig{},
			App:      AppConfig{MinLatencyMS: 0, MaxLatencyMS: 0},
			Logger:   LoggerConfig{Level: "info"},
		}

		require.Error(t, cfg.Validate())
	})
}
