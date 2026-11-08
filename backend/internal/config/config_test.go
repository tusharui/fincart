package config

import (
	"testing"

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
