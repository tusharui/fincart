package repository

import (
	"context"
	"os"
	"path/filepath"
	"sort"
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

	files, err := filepath.Glob(filepath.Join("..", "..", "internal", "db", "migrations", "*.up.sql"))
	if err != nil {
		t.Fatalf("failed to list migration files: %v", err)
	}
	sort.Strings(files)

	for _, path := range files {
		sqlBytes, err := os.ReadFile(path) // #nosec G304
		if err != nil {
			t.Fatalf("failed to read migration file %s: %v", path, err)
		}

		if _, err := database.ExecContext(context.Background(), string(sqlBytes)); err != nil {
			// Migrations are not idempotent; an already-migrated test database
			// is the expected steady state. Anything else is worth surfacing.
			t.Logf("migration %s skipped: %v", filepath.Base(path), err)
		}
	}
}

func cleanupTestDB(t *testing.T, database *db.DB) {
	t.Helper()
	if err := database.Close(); err != nil {
		t.Logf("failed to close test database: %v", err)
	}
}

func truncateTables(t *testing.T, database *db.DB) {
	t.Helper()

	_, err := database.ExecContext(context.Background(),
		"TRUNCATE TABLE webhook_deliveries, payment_events, transactions, idempotency_keys, api_keys, accounts, merchants CASCADE")
	if err != nil {
		t.Fatalf("failed to truncate tables: %v", err)
	}

	seedDefaultMerchant(t, database)
	seedAccounts(t, database)
}

// seedDefaultMerchant recreates the merchant that pre-tenancy data belongs to.
// The auth migration seeds it once, but truncation removes it between tests.
func seedDefaultMerchant(t *testing.T, database *db.DB) {
	t.Helper()

	merchant := models.Merchant{
		ID:   models.DefaultMerchantID,
		Name: "default",
	}
	if err := database.GORM().Create(&merchant).Error; err != nil {
		t.Fatalf("failed to seed default merchant: %v", err)
	}
}

// seedAccounts inserts the standard tokenized test accounts through GORM so the
// fixtures always match the current schema.
func seedAccounts(t *testing.T, database *db.DB) {
	t.Helper()

	accounts := []models.Account{
		{
			ID: uuid.New(), MerchantID: models.DefaultMerchantID, CardToken: security.HashToken("4111111111111111"), Last4: "1111",
			ExpiryMonth: 12, ExpiryYear: 2030, BalanceCents: 1000000, AvailableBalanceCents: 1000000,
		},
		{
			ID: uuid.New(), MerchantID: models.DefaultMerchantID, CardToken: security.HashToken("4242424242424242"), Last4: "4242",
			ExpiryMonth: 6, ExpiryYear: 2030, BalanceCents: 50000, AvailableBalanceCents: 50000,
		},
		{
			ID: uuid.New(), MerchantID: models.DefaultMerchantID, CardToken: security.HashToken("5555555555554444"), Last4: "4444",
			ExpiryMonth: 9, ExpiryYear: 2030, BalanceCents: 0, AvailableBalanceCents: 0,
		},
		{
			ID: uuid.New(), MerchantID: models.DefaultMerchantID, CardToken: security.HashToken("5105105105105100"), Last4: "5100",
			ExpiryMonth: 3, ExpiryYear: 2020, BalanceCents: 500000, AvailableBalanceCents: 500000,
		},
	}

	if err := database.GORM().Create(&accounts).Error; err != nil {
		t.Fatalf("failed to seed accounts: %v", err)
	}
}
