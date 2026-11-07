package repository

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/tusharui/payment-gateway/backend/internal/config"
	"github.com/tusharui/payment-gateway/backend/internal/db"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"github.com/tusharui/payment-gateway/backend/internal/security"
)

func setupTestDB(t *testing.T) *db.DB {
	t.Helper()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	logger := cfg.Logger.NewLogger()

	database, err := db.Connect(context.Background(), &cfg.Database, logger)
	if err != nil {
		t.Fatalf("failed to connect to test database: %v", err)
	}

	runMigrations(t, database)

	return database
}

func runMigrations(t *testing.T, database *db.DB) {
	t.Helper()

	migrationPath := filepath.Join("..", "..", "internal", "db", "migrations", "000001_init.up.sql")
	sqlBytes, err := os.ReadFile(migrationPath) // #nosec G304
	if err != nil {
		t.Fatalf("failed to read migration file: %v", err)
	}

	_, err = database.ExecContext(context.Background(), string(sqlBytes))
	if err != nil {
		if err.Error() != "pq: relation \"accounts\" already exists" {
			t.Logf("migration execution completed (tables may already exist)")
		}
	}
}

func cleanupTestDB(t *testing.T, database *db.DB) {
	t.Helper()
	if err := database.Close(); err != nil {
		log.Printf("failed to close test database: %v", err)
	}
}

func truncateTables(t *testing.T, database *db.DB) {
	t.Helper()

	_, err := database.ExecContext(context.Background(),
		"TRUNCATE TABLE transactions, idempotency_keys, accounts CASCADE")
	if err != nil {
		t.Fatalf("failed to truncate tables: %v", err)
	}

	seedAccounts(t, database)
}

// seedAccounts inserts the standard tokenized test accounts through GORM so the
// fixtures always match the current schema.
func seedAccounts(t *testing.T, database *db.DB) {
	t.Helper()

	accounts := []models.Account{
		{
			ID: uuid.New(), CardToken: security.HashToken("4111111111111111"), Last4: "1111",
			ExpiryMonth: 12, ExpiryYear: 2030, BalanceCents: 1000000, AvailableBalanceCents: 1000000,
		},
		{
			ID: uuid.New(), CardToken: security.HashToken("4242424242424242"), Last4: "4242",
			ExpiryMonth: 6, ExpiryYear: 2030, BalanceCents: 50000, AvailableBalanceCents: 50000,
		},
		{
			ID: uuid.New(), CardToken: security.HashToken("5555555555554444"), Last4: "4444",
			ExpiryMonth: 9, ExpiryYear: 2030, BalanceCents: 0, AvailableBalanceCents: 0,
		},
		{
			ID: uuid.New(), CardToken: security.HashToken("5105105105105100"), Last4: "5100",
			ExpiryMonth: 3, ExpiryYear: 2020, BalanceCents: 500000, AvailableBalanceCents: 500000,
		},
	}

	if err := database.GORM().Create(&accounts).Error; err != nil {
		t.Fatalf("failed to seed accounts: %v", err)
	}
}
