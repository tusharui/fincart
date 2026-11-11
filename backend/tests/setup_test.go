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
	"github.com/lib/pq"
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

	// APIKey is the raw credential for the seeded integration test merchant.
	// It carries both the payments and admin scopes.
	APIKey string
	// MerchantID identifies the tenant that owns the seeded key and accounts.
	MerchantID uuid.UUID

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

	// Integration tests burst many requests from a single key. Rate limiting is
	// covered by its own tests, so the shared harness opts out.
	cfg.RateLimit.Enabled = false

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	database, err := db.Connect(context.Background(), &cfg.Database, logger)
	require.NoError(t, err, "failed to connect to database")

	initialBalances, apiKey := resetTestData(t, database)

	router := handlers.NewRouter(database, cfg, logger)
	server := httptest.NewServer(router)

	return &TestServer{
		Server:          server,
		Database:        database,
		t:               t,
		APIKey:          apiKey,
		MerchantID:      models.DefaultMerchantID,
		initialBalances: initialBalances,
	}
}

// AuthRequest attaches the test API key to a request.
func (ts *TestServer) AuthRequest(req *http.Request) *http.Request {
	req.Header.Set("Authorization", "Bearer "+ts.APIKey)

	return req
}

// Get issues an authenticated GET request.
func (ts *TestServer) Get(t *testing.T, path string) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, ts.URL(path), nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(ts.AuthRequest(req))
	require.NoError(t, err)

	return resp
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

func resetTestData(t *testing.T, database *db.DB) (map[uuid.UUID]int64, string) {
	t.Helper()

	_, err := database.ExecContext(context.Background(),
		"TRUNCATE TABLE webhook_deliveries, payment_events, transactions, idempotency_keys, api_keys, accounts, merchants CASCADE")
	require.NoError(t, err, "failed to truncate tables")

	merchant := models.Merchant{ID: models.DefaultMerchantID, Name: "default"}
	require.NoError(t, database.GORM().Create(&merchant).Error, "failed to seed merchant")

	generated, err := security.GenerateAPIKey()
	require.NoError(t, err, "failed to generate test api key")

	apiKey := models.APIKey{
		ID:         uuid.New(),
		MerchantID: models.DefaultMerchantID,
		Name:       "integration-test",
		KeyPrefix:  generated.Prefix,
		KeyLast4:   generated.Last4,
		KeyHash:    security.HashToken(generated.Raw),
		Scopes:     pq.StringArray{models.ScopePayments, models.ScopeAdmin},
	}
	require.NoError(t, database.GORM().Create(&apiKey).Error, "failed to seed api key")

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

	require.NoError(t, database.GORM().Create(&accounts).Error, "failed to seed accounts")

	initialBalances := make(map[uuid.UUID]int64, len(accounts))
	for _, account := range accounts {
		initialBalances[account.ID] = account.BalanceCents
	}

	return initialBalances, generated.Raw
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

	ts.AuthRequest(req)
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

	ts.AuthRequest(req)
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

	ts.AuthRequest(req)
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

	ts.AuthRequest(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idempotencyKey)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	return resp
}
