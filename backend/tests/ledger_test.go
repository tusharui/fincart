package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"github.com/tusharui/payment-gateway/backend/internal/security"
	"github.com/tusharui/payment-gateway/backend/internal/service"
)

// assertLedgerReconciled verifies every account against the transaction ledger.
//
// Settled balance must equal the account's seeded starting balance minus
// completed captures plus completed refunds. Available balance must equal the
// settled balance minus funds reserved by active authorization holds. This
// catches drift between the authoritative ledger rows and the cached balance
// columns after any lifecycle transition.
func (ts *TestServer) assertLedgerReconciled(t *testing.T) {
	t.Helper()

	var accounts []models.Account
	require.NoError(t, ts.Database.GORM().Find(&accounts).Error)
	require.NotEmpty(t, accounts, "expected seeded accounts")

	var transactions []models.Transaction
	require.NoError(t, ts.Database.GORM().Find(&transactions).Error)

	ledger := make(map[uuid.UUID][]models.Transaction, len(accounts))
	for _, txn := range transactions {
		ledger[txn.AccountID] = append(ledger[txn.AccountID], txn)
	}

	for _, account := range accounts {
		startingBalance, ok := ts.initialBalances[account.ID]
		require.Truef(t, ok, "no starting balance recorded for account %s", account.ID)

		expectedBalance := startingBalance
		var activeHolds int64

		for _, txn := range ledger[account.ID] {
			switch txn.Type {
			case models.TransactionTypeCapture:
				if txn.Status == models.TransactionStatusCompleted || txn.Status == models.TransactionStatusRefunded {
					expectedBalance -= txn.AmountCents
				}
			case models.TransactionTypeRefund:
				if txn.Status == models.TransactionStatusCompleted {
					expectedBalance += txn.AmountCents
				}
			case models.TransactionTypeAuthHold:
				if txn.Status == models.TransactionStatusActive {
					activeHolds += txn.AmountCents
				}
			}
		}

		assert.Equalf(t, expectedBalance, account.BalanceCents,
			"settled balance drifted from the ledger for account %s", account.ID)
		assert.Equalf(t, expectedBalance-activeHolds, account.AvailableBalanceCents,
			"available balance drifted from settled balance minus active holds for account %s (settled=%d holds=%d)",
			account.ID, expectedBalance, activeHolds)
	}
}

// assertAccountBalances checks the cached balance columns for a seeded card.
func assertAccountBalances(t *testing.T, ts *TestServer, cardNumber string, balanceCents, availableCents int64) {
	t.Helper()

	var account models.Account
	require.NoError(t, ts.Database.GORM().
		Where("card_token = ?", security.HashToken(cardNumber)).
		First(&account).Error)

	assert.Equal(t, balanceCents, account.BalanceCents, "unexpected settled balance")
	assert.Equal(t, availableCents, account.AvailableBalanceCents, "unexpected available balance")
}

func TestLedgerReconciliation_AuthorizeCaptureRefund(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	const cardNumber = "4111111111111111"

	// Fresh accounts must already reconcile against their seeded balances.
	ts.assertLedgerReconciled(t)

	// Authorization reserves funds but does not settle them.
	authResp := ts.Authorize(t, cardNumber, "123", 10000, "ledger-auth")
	require.Equal(t, http.StatusOK, authResp.StatusCode)
	var authBody map[string]any
	require.NoError(t, json.NewDecoder(authResp.Body).Decode(&authBody))
	authResp.Body.Close()
	authID := authBody["authorization_id"].(string)

	ts.assertLedgerReconciled(t)
	assertAccountBalances(t, ts, cardNumber, 1000000, 990000)

	// Capture moves reserved funds into the settled balance.
	captureResp := ts.Capture(t, authID, 10000, "ledger-capture")
	require.Equal(t, http.StatusOK, captureResp.StatusCode)
	var captureBody map[string]any
	require.NoError(t, json.NewDecoder(captureResp.Body).Decode(&captureBody))
	captureResp.Body.Close()
	captureID := captureBody["capture_id"].(string)

	ts.assertLedgerReconciled(t)
	assertAccountBalances(t, ts, cardNumber, 990000, 990000)

	// Refund restores both settled and available funds.
	refundResp := ts.Refund(t, captureID, 10000, "ledger-refund")
	require.Equal(t, http.StatusOK, refundResp.StatusCode)
	refundResp.Body.Close()

	ts.assertLedgerReconciled(t)
	assertAccountBalances(t, ts, cardNumber, 1000000, 1000000)
}

func TestLedgerReconciliation_VoidReleasesHold(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	const cardNumber = "4111111111111111"

	authResp := ts.Authorize(t, cardNumber, "123", 20000, "ledger-void-auth")
	require.Equal(t, http.StatusOK, authResp.StatusCode)
	var authBody map[string]any
	require.NoError(t, json.NewDecoder(authResp.Body).Decode(&authBody))
	authResp.Body.Close()
	authID := authBody["authorization_id"].(string)

	ts.assertLedgerReconciled(t)
	assertAccountBalances(t, ts, cardNumber, 1000000, 980000)

	voidResp := ts.Void(t, authID, "ledger-void")
	require.Equal(t, http.StatusOK, voidResp.StatusCode)
	voidResp.Body.Close()

	ts.assertLedgerReconciled(t)
	assertAccountBalances(t, ts, cardNumber, 1000000, 1000000)
}

func TestLedgerReconciliation_MultipleAccounts(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	const (
		cardA = "4111111111111111" // settled balance 1,000,000
		cardB = "4242424242424242" // settled balance 50,000
	)

	authA := ts.Authorize(t, cardA, "123", 10000, "ledger-multi-auth-a")
	require.Equal(t, http.StatusOK, authA.StatusCode)
	var authABody map[string]any
	require.NoError(t, json.NewDecoder(authA.Body).Decode(&authABody))
	authA.Body.Close()

	authB := ts.Authorize(t, cardB, "123", 5000, "ledger-multi-auth-b")
	require.Equal(t, http.StatusOK, authB.StatusCode)
	var authBBody map[string]any
	require.NoError(t, json.NewDecoder(authB.Body).Decode(&authBBody))
	authB.Body.Close()

	ts.assertLedgerReconciled(t)
	assertAccountBalances(t, ts, cardA, 1000000, 990000)
	assertAccountBalances(t, ts, cardB, 50000, 45000)

	captureA := ts.Capture(t, authABody["authorization_id"].(string), 10000, "ledger-multi-cap-a")
	require.Equal(t, http.StatusOK, captureA.StatusCode)
	var captureABody map[string]any
	require.NoError(t, json.NewDecoder(captureA.Body).Decode(&captureABody))
	captureA.Body.Close()

	voidB := ts.Void(t, authBBody["authorization_id"].(string), "ledger-multi-void-b")
	require.Equal(t, http.StatusOK, voidB.StatusCode)
	voidB.Body.Close()

	ts.assertLedgerReconciled(t)
	assertAccountBalances(t, ts, cardA, 990000, 990000)
	assertAccountBalances(t, ts, cardB, 50000, 50000)

	refundA := ts.Refund(t, captureABody["capture_id"].(string), 10000, "ledger-multi-refund-a")
	require.Equal(t, http.StatusOK, refundA.StatusCode)
	refundA.Body.Close()

	ts.assertLedgerReconciled(t)
	assertAccountBalances(t, ts, cardA, 1000000, 1000000)
	assertAccountBalances(t, ts, cardB, 50000, 50000)
}

func TestLedgerReconciliation_ExpiryReleasesHold(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	const cardNumber = "4111111111111111"

	authResp := ts.Authorize(t, cardNumber, "123", 15000, "ledger-expiry-auth")
	require.Equal(t, http.StatusOK, authResp.StatusCode)
	var authBody map[string]any
	require.NoError(t, json.NewDecoder(authResp.Body).Decode(&authBody))
	authResp.Body.Close()

	formattedID := authBody["authorization_id"].(string)
	authID, err := uuid.Parse(formattedID[len("auth_"):])
	require.NoError(t, err)

	ts.assertLedgerReconciled(t)
	assertAccountBalances(t, ts, cardNumber, 1000000, 985000)

	require.NoError(t, ts.Database.GORM().
		Model(&models.Transaction{}).
		Where("id = ?", authID).
		Update("expires_at", time.Now().Add(-time.Hour)).Error)

	expired, err := service.NewExpiryService(ts.Database).ExpireAuthorizations(context.Background(), time.Now())
	require.NoError(t, err)
	require.Equal(t, 1, expired)

	ts.assertLedgerReconciled(t)
	assertAccountBalances(t, ts, cardNumber, 1000000, 1000000)
}
