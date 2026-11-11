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

func TestLoad_ChaosDefaultsToDisabled(t *testing.T) {
	t.Setenv("CHAOS_ENABLED", "")
	t.Setenv("FAILURE_RATE", "")
	t.Setenv("MIN_LATENCY_MS", "")
	t.Setenv("MAX_LATENCY_MS", "")

	cfg, err := Load()
	require.NoError(t, err)

	assert.False(t, cfg.App.ChaosEnabled)
	assert.Zero(t, cfg.App.FailureRate)
	assert.Zero(t, cfg.App.MinLatencyMS)
	assert.Zero(t, cfg.App.MaxLatencyMS)
}

func TestLoad_ChaosReadsExplicitSettings(t *testing.T) {
	t.Setenv("CHAOS_ENABLED", "true")
	t.Setenv("FAILURE_RATE", "0.25")
	t.Setenv("MIN_LATENCY_MS", "10")
	t.Setenv("MAX_LATENCY_MS", "50")

	cfg, err := Load()
	require.NoError(t, err)

	assert.True(t, cfg.App.ChaosEnabled)
	assert.Equal(t, 0.25, cfg.App.FailureRate)
	assert.Equal(t, 10, cfg.App.MinLatencyMS)
	assert.Equal(t, 50, cfg.App.MaxLatencyMS)
}

func TestConfig_Validate(t *testing.T) {
	t.Run("accepts DATABASE_URL without host or database name", func(t *testing.T) {
		cfg := &Config{
			Server:   ServerConfig{Port: "8080"},
			Database: DatabaseConfig{URL: "postgresql://user:pass@example.com/db"},
			App:      AppConfig{MinLatencyMS: 0, MaxLatencyMS: 0},
			Auth:     AuthConfig{BootstrapMerchant: "default", LastUsedThrottle: time.Minute},
			Logger:   LoggerConfig{Level: "info"},
		}

		require.NoError(t, cfg.Validate())
	})

	t.Run("requires host and database name without DATABASE_URL", func(t *testing.T) {
		cfg := &Config{
			Server:   ServerConfig{Port: "8080"},
			Database: DatabaseConfig{},
			App:      AppConfig{MinLatencyMS: 0, MaxLatencyMS: 0},
			Auth:     AuthConfig{BootstrapMerchant: "default", LastUsedThrottle: time.Minute},
			Logger:   LoggerConfig{Level: "info"},
		}

		require.Error(t, cfg.Validate())
	})
}

func TestLoad_AuthDefaults(t *testing.T) {
	t.Setenv("AUTH_ENABLED", "")
	t.Setenv("AUTH_BOOTSTRAP_KEY", "")
	t.Setenv("AUTH_BOOTSTRAP_MERCHANT", "")
	t.Setenv("AUTH_LAST_USED_THROTTLE", "")

	cfg, err := Load()
	require.NoError(t, err)

	assert.True(t, cfg.Auth.Enabled, "authentication must be on by default")
	assert.Empty(t, cfg.Auth.BootstrapKey)
	assert.Equal(t, "default", cfg.Auth.BootstrapMerchant)
	assert.Equal(t, time.Minute, cfg.Auth.LastUsedThrottle)
}

func TestLoad_AuthReadsExplicitSettings(t *testing.T) {
	t.Setenv("AUTH_ENABLED", "false")
	t.Setenv("AUTH_BOOTSTRAP_KEY", "bootstrap-secret-value")
	t.Setenv("AUTH_BOOTSTRAP_MERCHANT", "acme")
	t.Setenv("AUTH_LAST_USED_THROTTLE", "30s")

	cfg, err := Load()
	require.NoError(t, err)

	assert.False(t, cfg.Auth.Enabled)
	assert.Equal(t, "bootstrap-secret-value", cfg.Auth.BootstrapKey)
	assert.Equal(t, "acme", cfg.Auth.BootstrapMerchant)
	assert.Equal(t, 30*time.Second, cfg.Auth.LastUsedThrottle)
}

func TestAuthConfig_Validate(t *testing.T) {
	valid := func() AuthConfig {
		return AuthConfig{
			Enabled:           true,
			BootstrapMerchant: "default",
			LastUsedThrottle:  time.Minute,
		}
	}

	t.Run("accepts defaults without a bootstrap key", func(t *testing.T) {
		cfg := valid()
		require.NoError(t, cfg.validate())
	})

	t.Run("accepts a strong bootstrap key", func(t *testing.T) {
		cfg := valid()
		cfg.BootstrapKey = "0123456789abcdef"
		require.NoError(t, cfg.validate())
	})

	tests := []struct {
		name   string
		mutate func(*AuthConfig)
	}{
		{"rejects a short bootstrap key", func(c *AuthConfig) { c.BootstrapKey = "short" }},
		{"requires a bootstrap merchant", func(c *AuthConfig) { c.BootstrapMerchant = "" }},
		{"requires a positive last used throttle", func(c *AuthConfig) { c.LastUsedThrottle = 0 }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid()
			tt.mutate(&cfg)
			assert.Error(t, cfg.validate())
		})
	}
}
