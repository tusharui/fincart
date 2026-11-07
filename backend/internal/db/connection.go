// Package db provides database connection and management utilities.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"github.com/lib/pq"
	"github.com/tusharui/payment-gateway/backend/internal/config"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// DB owns the database connection pool and exposes both the native *sql.DB
// (used for migrations and operational tasks) and a GORM handle used by the
// repository layer.
type DB struct {
	*sql.DB
	gorm   *gorm.DB
	logger *slog.Logger
}

// GORM returns the GORM handle used for ORM-based data access.
func (db *DB) GORM() *gorm.DB {
	return db.gorm
}

// Connect establishes a connection to the database and initializes GORM on top
// of the same connection pool.
func Connect(ctx context.Context, cfg *config.DatabaseConfig, logger *slog.Logger) (*DB, error) {
	logger.Info("connecting to database",
		"host", cfg.Host,
		"port", cfg.Port,
		"database", cfg.DBName,
	)

	sqlDB, err := sql.Open("postgres", cfg.DSN())
	if err != nil {
		logger.Error("failed to open database connection", "error", err)
		return nil, fmt.Errorf("failed to open database connection: %w", err)
	}

	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	if err := sqlDB.PingContext(ctx); err != nil {
		logger.Error("failed to ping database", "error", err)
		_ = sqlDB.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	gormDB, err := openGORM(sqlDB)
	if err != nil {
		logger.Error("failed to initialize orm", "error", err)
		_ = sqlDB.Close()
		return nil, fmt.Errorf("failed to initialize orm: %w", err)
	}

	logger.Info("successfully connected to database",
		"max_open_conns", cfg.MaxOpenConns,
		"max_idle_conns", cfg.MaxIdleConns,
		"conn_max_lifetime", cfg.ConnMaxLifetime,
	)

	return &DB{
		DB:     sqlDB,
		gorm:   gormDB,
		logger: logger,
	}, nil
}

// openGORM builds a GORM handle over an existing database/sql pool.
// Reusing the pool keeps a single set of connection limits and drivers.
func openGORM(sqlDB *sql.DB) (*gorm.DB, error) {
	return gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: sqlDB}), &gorm.Config{
		SkipDefaultTransaction: true,
		Logger: gormlogger.New(log.New(os.Stdout, "", log.LstdFlags), gormlogger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  gormlogger.Warn,
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		}),
	})
}

// Close closes the database connection and logs the closure.
func (db *DB) Close() error {
	db.logger.Info("closing database connection")
	return db.DB.Close()
}

// IsUniqueViolation checks if the error is a PostgreSQL unique constraint violation
func IsUniqueViolation(err error) bool {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		// 23505 is the PostgreSQL error code for unique_violation
		return pqErr.Code == "23505"
	}
	return false
}
