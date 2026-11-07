package db

import (
	"database/sql"
	"io"
	"log/slog"

	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// NewTestDB creates a DB instance for testing with a no-op logger.
// This is only for use in tests where logging output is not needed.
func NewTestDB(sqlDB *sql.DB) (*DB, error) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	gormDB, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: sqlDB}), &gorm.Config{
		SkipDefaultTransaction: true,
		Logger:                 gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return nil, err
	}

	return &DB{
		DB:     sqlDB,
		gorm:   gormDB,
		logger: logger,
	}, nil
}
