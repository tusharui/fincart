//nolint:errcheck // unchecked errors are acceptable in test files
package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tusharui/payment-gateway/backend/internal/config"
	"github.com/tusharui/payment-gateway/backend/internal/db"
	"github.com/tusharui/payment-gateway/backend/internal/handlers"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"github.com/tusharui/payment-gateway/backend/internal/security"
)

// TestServer wraps the HTTP test server and database for integration tests.
type TestServer struct {
	Server   *httptest.Server
	Database *db.DB
	t        *testing.T

	// initialBalances records the settled balance each seeded account started
	// with so ledger reconciliation can be checked against a known baseline.
	initialBalances map[uuid.UUID]int64
}

// SetupTest creates a new test server with a clean database state.
func SetupTest(t *testing.T) *TestServer {
	t.Helper()

	cfg, err := config.Load()
	require.NoError(t, err, "failed to load config")

	// Disable chaos for integration tests
	cfg.App.FailureRate = 0
	cfg.App.MinLatencyMS = 0
	cfg.App.MaxLatencyMS = 0

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	database, err := db.Connect(context.Background(), &cfg.Database, logger)
	require.NoError(t, err, "failed to connect to database")

	initialBalances := resetTestData(t, database)

	router := handlers.NewRouter(database, cfg, logger)
	server := httptest.NewServer(router)

	return &TestServer{
		Server:          server,
		Database:        database,
		t:               t,
		initialBalances: initialBalances,
	}
}

// Close shuts down the test server and database connection.
func (ts *TestServer) Close() {
	ts.Server.Close()
	_ = ts.Database.Close()
}

// URL returns the full URL for a given path.
func (ts *TestServer) URL(path string) string {
	return ts.Server.URL + path
}

func resetTestData(t *testing.T, database *db.DB) map[uuid.UUID]int64 {
	t.Helper()

	_, err := database.ExecContext(context.Background(),
		"TRUNCATE TABLE webhook_deliveries, payment_events, transactions, idempotency_keys, accounts CASCADE")
	require.NoError(t, err, "failed to truncate tables")

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

	require.NoError(t, database.GORM().Create(&accounts).Error, "failed to seed accounts")

	initialBalances := make(map[uuid.UUID]int64, len(accounts))
	for _, account := range accounts {
		initialBalances[account.ID] = account.BalanceCents
	}

	return initialBalances
}

// Authorize sends a POST request to create an authorization.
func (ts *TestServer) Authorize(t *testing.T, cardNumber, cvv string, amount int64, idempotencyKey string) *http.Response {
	t.Helper()

	return ts.AuthorizeLinked(t, cardNumber, cvv, amount, "", "", idempotencyKey)
}

// AuthorizeLinked sends a POST request to create an authorization with optional
// order and customer linkage supplied as raw strings (empty means omitted).
func (ts *TestServer) AuthorizeLinked(
	t *testing.T,
	cardNumber, cvv string,
	amount int64,
	orderID, customerID, idempotencyKey string,
) *http.Response {
	t.Helper()

	body := map[string]any{
		"card_number": cardNumber,
		"cvv":         cvv,
		"amount":      amount,
	}
	if orderID != "" {
		body["order_id"] = orderID
	}
	if customerID != "" {
		body["customer_id"] = customerID
	}
	jsonBody, _ := json.Marshal(body)

	req, err := http.NewRequest(http.MethodPost, ts.URL("/api/v1/authorizations"), bytes.NewReader(jsonBody))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idempotencyKey)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	return resp
}

// Capture sends a POST request to capture an authorization.
func (ts *TestServer) Capture(t *testing.T, authID string, amount int64, idempotencyKey string) *http.Response {
	t.Helper()

	body := map[string]any{
		"authorization_id": authID,
		"amount":           amount,
	}
	jsonBody, _ := json.Marshal(body)

	req, err := http.NewRequest(http.MethodPost, ts.URL("/api/v1/captures"), bytes.NewReader(jsonBody))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idempotencyKey)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	return resp
}

// Void sends a POST request to void an authorization.
func (ts *TestServer) Void(t *testing.T, authID string, idempotencyKey string) *http.Response {
	t.Helper()

	body := map[string]any{
		"authorization_id": authID,
	}
	jsonBody, _ := json.Marshal(body)

	req, err := http.NewRequest(http.MethodPost, ts.URL("/api/v1/voids"), bytes.NewReader(jsonBody))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idempotencyKey)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	return resp
}

// Refund sends a POST request to refund a capture.
func (ts *TestServer) Refund(t *testing.T, captureID string, amount int64, idempotencyKey string) *http.Response {
	t.Helper()

	body := map[string]any{
		"capture_id": captureID,
		"amount":     amount,
	}
	jsonBody, _ := json.Marshal(body)

	req, err := http.NewRequest(http.MethodPost, ts.URL("/api/v1/refunds"), bytes.NewReader(jsonBody))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idempotencyKey)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	return resp
}
