// Package config handles configuration loading and validation for the bank API.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all application configuration
type Config struct {
	Server   ServerConfig
	Logger   LoggerConfig
	Database DatabaseConfig
	App      AppConfig
	Webhook  WebhookConfig
}

// ServerConfig holds HTTP server configuration
type ServerConfig struct {
	Port         string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration
}

// DatabaseConfig holds database connection configuration
type DatabaseConfig struct {
	// URL is a complete PostgreSQL connection string. When set it takes
	// precedence over the individual Host/Port/... fields, which makes it
	// possible to target managed databases such as Neon.
	URL             string
	Host            string
	Port            string
	User            string
	Password        string
	DBName          string
	SSLMode         string
	ConnMaxLifetime time.Duration
	MaxOpenConns    int
	MaxIdleConns    int
}

// AppConfig holds application-specific configuration
type AppConfig struct {
	// ChaosEnabled gates failure and latency injection. It defaults to false
	// so a production deployment cannot accidentally return synthetic errors.
	ChaosEnabled       bool
	FailureRate        float64
	MinLatencyMS       int
	MaxLatencyMS       int
	AuthExpiryHours    int
	AuthExpiryDuration time.Duration
}

// LoggerConfig holds logging configuration
type LoggerConfig struct {
	Level string // debug, info, warn, error
}

// WebhookConfig controls outbound delivery of payment domain events.
type WebhookConfig struct {
	Enabled       bool
	Endpoints     []string
	Secret        string
	MaxAttempts   int
	Timeout       time.Duration
	BatchSize     int
	PollInterval  time.Duration
	LeaseDuration time.Duration
	BaseBackoff   time.Duration
	MaxBackoff    time.Duration
}

// Load loads configuration from environment variables with sensible defaults
func Load() (*Config, error) {
	authExpiryHours := getEnvAsInt("AUTH_EXPIRY_HOURS", 168) // 7 days default

	cfg := &Config{
		Server: ServerConfig{
			Port:         getEnv("PORT", "8080"),
			ReadTimeout:  getEnvAsDuration("SERVER_READ_TIMEOUT", "15s"),
			WriteTimeout: getEnvAsDuration("SERVER_WRITE_TIMEOUT", "15s"),
			IdleTimeout:  getEnvAsDuration("SERVER_IDLE_TIMEOUT", "60s"),
		},
		Database: DatabaseConfig{
			URL:             getEnv("DATABASE_URL", ""),
			Host:            getEnv("DB_HOST", "localhost"),
			Port:            getEnv("DB_PORT", "5432"),
			User:            getEnv("DB_USER", "postgres"),
			Password:        getEnv("DB_PASSWORD", "postgres"),
			DBName:          getEnv("DB_NAME", "mockbank"),
			SSLMode:         getEnv("DB_SSLMODE", "disable"),
			MaxOpenConns:    getEnvAsInt("DB_MAX_OPEN_CONNS", 25),
			MaxIdleConns:    getEnvAsInt("DB_MAX_IDLE_CONNS", 5),
			ConnMaxLifetime: getEnvAsDuration("DB_CONN_MAX_LIFETIME", "5m"),
		},
		App: AppConfig{
			ChaosEnabled:       getEnvAsBool("CHAOS_ENABLED", false),
			FailureRate:        getEnvAsFloat("FAILURE_RATE", 0),
			MinLatencyMS:       getEnvAsInt("MIN_LATENCY_MS", 0),
			MaxLatencyMS:       getEnvAsInt("MAX_LATENCY_MS", 0),
			AuthExpiryHours:    authExpiryHours,
			AuthExpiryDuration: time.Duration(authExpiryHours) * time.Hour,
		},
		Logger: LoggerConfig{
			Level: getEnv("LOG_LEVEL", "info"),
		},
		Webhook: WebhookConfig{
			Enabled:       getEnvAsBool("WEBHOOK_ENABLED", false),
			Endpoints:     getEnvAsStringSlice("WEBHOOK_ENDPOINTS"),
			Secret:        getEnv("WEBHOOK_SECRET", ""),
			MaxAttempts:   getEnvAsInt("WEBHOOK_MAX_ATTEMPTS", 8),
			Timeout:       getEnvAsDuration("WEBHOOK_TIMEOUT", "10s"),
			BatchSize:     getEnvAsInt("WEBHOOK_BATCH_SIZE", 50),
			PollInterval:  getEnvAsDuration("WEBHOOK_POLL_INTERVAL", "2s"),
			LeaseDuration: getEnvAsDuration("WEBHOOK_LEASE_DURATION", "30s"),
			BaseBackoff:   getEnvAsDuration("WEBHOOK_BASE_BACKOFF", "5s"),
			MaxBackoff:    getEnvAsDuration("WEBHOOK_MAX_BACKOFF", "1h"),
		},
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return cfg, nil
}

// Validate checks if the configuration is valid and returns an error if not.
func (c *Config) Validate() error {
	if c.Server.Port == "" {
		return fmt.Errorf("server port cannot be empty")
	}

	if c.Database.URL == "" {
		if c.Database.Host == "" {
			return fmt.Errorf("database host cannot be empty")
		}
		if c.Database.DBName == "" {
			return fmt.Errorf("database name cannot be empty")
		}
	}

	if c.App.FailureRate < 0 || c.App.FailureRate > 1 {
		return fmt.Errorf("failure rate must be between 0 and 1, got %f", c.App.FailureRate)
	}

	if c.App.MinLatencyMS < 0 {
		return fmt.Errorf("min latency cannot be negative")
	}
	if c.App.MaxLatencyMS < c.App.MinLatencyMS {
		return fmt.Errorf("max latency (%d) must be >= min latency (%d)", c.App.MaxLatencyMS, c.App.MinLatencyMS)
	}

	validLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	if !validLevels[c.Logger.Level] {
		return fmt.Errorf("invalid log level: %s (must be debug, info, warn, or error)", c.Logger.Level)
	}

	if err := c.Webhook.validate(); err != nil {
		return err
	}

	return nil
}

// validate checks the webhook delivery settings. Webhook configuration is only
// required when delivery is enabled; disabled webhooks never fail startup.
func (w *WebhookConfig) validate() error {
	if !w.Enabled {
		return nil
	}

	if len(w.Endpoints) == 0 {
		return fmt.Errorf("webhook endpoints cannot be empty when webhooks are enabled")
	}
	if w.Secret == "" {
		return fmt.Errorf("webhook secret cannot be empty when webhooks are enabled")
	}

	for _, endpoint := range w.Endpoints {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("invalid webhook endpoint %q: must be an absolute http(s) URL", endpoint)
		}
	}

	if w.MaxAttempts < 1 {
		return fmt.Errorf("webhook max attempts must be at least 1")
	}
	if w.BatchSize < 1 {
		return fmt.Errorf("webhook batch size must be at least 1")
	}
	if w.Timeout <= 0 {
		return fmt.Errorf("webhook timeout must be positive")
	}
	if w.PollInterval <= 0 {
		return fmt.Errorf("webhook poll interval must be positive")
	}
	if w.LeaseDuration <= w.Timeout {
		return fmt.Errorf("webhook lease duration must exceed the delivery timeout")
	}
	if w.BaseBackoff <= 0 {
		return fmt.Errorf("webhook base backoff must be positive")
	}
	if w.MaxBackoff < w.BaseBackoff {
		return fmt.Errorf("webhook max backoff must be greater than or equal to the base backoff")
	}

	return nil
}

// DSN returns the PostgreSQL connection string
func (c *DatabaseConfig) DSN() string {
	if c.URL != "" {
		return stripUnsupportedDSNParams(c.URL)
	}

	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		c.Host, c.Port, c.User, c.Password, c.DBName, c.SSLMode,
	)
}

// stripUnsupportedDSNParams removes libpq-only connection options that the
// pure-Go driver would forward to the server as runtime parameters, where they
// are rejected. channel_binding is consumed by libpq itself, so the server has
// no such setting.
func stripUnsupportedDSNParams(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return raw
	}

	if u.Query().Has("channel_binding") {
		query := u.Query()
		query.Del("channel_binding")
		u.RawQuery = query.Encode()
	}

	return u.String()
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvAsInt(key string, defaultValue int) int {
	valueStr := os.Getenv(key)
	if valueStr == "" {
		return defaultValue
	}
	value, err := strconv.Atoi(valueStr)
	if err != nil {
		return defaultValue
	}
	return value
}

func getEnvAsFloat(key string, defaultValue float64) float64 {
	valueStr := os.Getenv(key)
	if valueStr == "" {
		return defaultValue
	}
	value, err := strconv.ParseFloat(valueStr, 64)
	if err != nil {
		return defaultValue
	}
	return value
}

func getEnvAsDuration(key, defaultValue string) time.Duration {
	valueStr := getEnv(key, defaultValue)
	duration, err := time.ParseDuration(valueStr)
	if err != nil {
		// Fallback to parsing the default if provided value is invalid
		duration, err = time.ParseDuration(defaultValue)
		if err != nil {
			return 0
		}
	}
	return duration
}

func getEnvAsBool(key string, defaultValue bool) bool {
	valueStr := os.Getenv(key)
	if valueStr == "" {
		return defaultValue
	}
	value, err := strconv.ParseBool(valueStr)
	if err != nil {
		return defaultValue
	}
	return value
}

// getEnvAsStringSlice parses a comma-separated environment variable into a
// trimmed, non-empty list. Empty entries are ignored so trailing commas are
// harmless.
func getEnvAsStringSlice(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			values = append(values, trimmed)
		}
	}

	return values
}
