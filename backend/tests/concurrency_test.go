package tests

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tusharui/payment-gateway/backend/internal/models"
)

func TestConcurrentAuthorizations_RespectAvailableBalance(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	const (
		cardNumber = "4242424242424242" // settled balance 50,000
		amount     = int64(10000)
		attempts   = 10
	)

	type result struct {
		status  int
		errCode string
	}

	results := make(chan result, attempts)
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start

			resp := ts.Authorize(t, cardNumber, "123", amount, fmt.Sprintf("concurrent-auth-%d", idx))
			defer resp.Body.Close()

			var body map[string]any
			_ = json.NewDecoder(resp.Body).Decode(&body)
			code, _ := body["error"].(string)

			results <- result{status: resp.StatusCode, errCode: code}
		}(i)
	}

	close(start)
	wg.Wait()
	close(results)

	approved := 0
	declined := 0
	for res := range results {
		switch res.status {
		case http.StatusOK:
			approved++
		case http.StatusPaymentRequired:
			declined++
			assert.Equal(t, "insufficient_funds", res.errCode)
		default:
			assert.Failf(t, "unexpected authorization response", "status=%d error=%s", res.status, res.errCode)
		}
	}

	assert.Equal(t, 5, approved, "only five 10,000 holds fit in the 50,000 balance")
	assert.Equal(t, attempts-5, declined, "the remaining holds must be declined")

	ts.assertLedgerReconciled(t)
	assertAccountBalances(t, ts, cardNumber, 50000, 0)

	var activeHolds int64
	require.NoError(t, ts.Database.GORM().
		Model(&models.Transaction{}).
		Where("type = ? AND status = ?", models.TransactionTypeAuthHold, models.TransactionStatusActive).
		Count(&activeHolds).Error)
	assert.Equal(t, int64(5), activeHolds)
}

func TestConcurrentRefunds_OnlyOneSucceeds(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	const (
		cardNumber = "4111111111111111"
		attempts   = 10
	)

	authResp := ts.Authorize(t, cardNumber, "123", 10000, "concurrent-refund-auth")
	require.Equal(t, http.StatusOK, authResp.StatusCode)
	var authBody map[string]any
	require.NoError(t, json.NewDecoder(authResp.Body).Decode(&authBody))
	authResp.Body.Close()
	authID := authBody["authorization_id"].(string)

	captureResp := ts.Capture(t, authID, 10000, "concurrent-refund-capture")
	require.Equal(t, http.StatusOK, captureResp.StatusCode)
	var captureBody map[string]any
	require.NoError(t, json.NewDecoder(captureResp.Body).Decode(&captureBody))
	captureResp.Body.Close()
	captureID := captureBody["capture_id"].(string)

	results := make(chan int, attempts)
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start

			resp := ts.Refund(t, captureID, 10000, fmt.Sprintf("concurrent-refund-%d", idx))
			results <- resp.StatusCode
			resp.Body.Close()
		}(i)
	}

	close(start)
	wg.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for status := range results {
		switch status {
		case http.StatusOK:
			successes++
		case http.StatusConflict:
			conflicts++
		default:
			assert.Failf(t, "unexpected refund response", "status=%d", status)
		}
	}

	assert.Equal(t, 1, successes, "exactly one refund should succeed")
	assert.Equal(t, attempts-1, conflicts, "all other refunds should conflict")

	ts.assertLedgerReconciled(t)
	assertAccountBalances(t, ts, cardNumber, 1000000, 1000000)

	var refunds int64
	require.NoError(t, ts.Database.GORM().
		Model(&models.Transaction{}).
		Where("type = ?", models.TransactionTypeRefund).
		Count(&refunds).Error)
	assert.Equal(t, int64(1), refunds, "only one refund row may be written")

	captureUUID, err := uuid.Parse(strings.TrimPrefix(captureID, "cap_"))
	require.NoError(t, err)

	var capture models.Transaction
	require.NoError(t, ts.Database.GORM().First(&capture, "id = ?", captureUUID).Error)
	assert.Equal(t, models.TransactionStatusRefunded, capture.Status)
}

func TestConcurrentRequestsWithSameIdempotencyKey_CreateOneAuthorization(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	const (
		cardNumber = "4111111111111111"
		attempts   = 10
		key        = "concurrent-idempotency-key"
	)

	results := make(chan int, attempts)
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start

			resp := ts.Authorize(t, cardNumber, "123", 10000, key)
			results <- resp.StatusCode
			resp.Body.Close()
		}()
	}

	close(start)
	wg.Wait()
	close(results)

	successes := 0
	for status := range results {
		switch status {
		case http.StatusOK:
			successes++
		case http.StatusConflict:
			// Concurrent losers may observe the claim before it is completed.
		default:
			assert.Failf(t, "unexpected idempotent response", "status=%d", status)
		}
	}
	assert.GreaterOrEqual(t, successes, 1, "at least one request must execute")

	var authorizations int64
	require.NoError(t, ts.Database.GORM().
		Model(&models.Transaction{}).
		Where("type = ?", models.TransactionTypeAuthHold).
		Count(&authorizations).Error)
	assert.Equal(t, int64(1), authorizations, "the shared idempotency key must create a single hold")

	ts.assertLedgerReconciled(t)
	assertAccountBalances(t, ts, cardNumber, 1000000, 990000)
}

func TestConcurrentCaptureAndVoid_OnlyOneSucceeds(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	const (
		cardNumber = "4111111111111111"
		iterations = 5
	)

	authIDs := make([]uuid.UUID, 0, iterations)

	for i := 0; i < iterations; i++ {
		authResp := ts.Authorize(t, cardNumber, "123", 10000, fmt.Sprintf("race-auth-%d", i))
		require.Equal(t, http.StatusOK, authResp.StatusCode)
		var authBody map[string]any
		require.NoError(t, json.NewDecoder(authResp.Body).Decode(&authBody))
		authResp.Body.Close()

		formattedID := authBody["authorization_id"].(string)
		authID, err := uuid.Parse(strings.TrimPrefix(formattedID, "auth_"))
		require.NoError(t, err)
		authIDs = append(authIDs, authID)

		start := make(chan struct{})
		var wg sync.WaitGroup
		var captureStatus, voidStatus int

		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start

			resp := ts.Capture(t, formattedID, 10000, fmt.Sprintf("race-cap-%d", i))
			captureStatus = resp.StatusCode
			resp.Body.Close()
		}()
		go func() {
			defer wg.Done()
			<-start

			resp := ts.Void(t, formattedID, fmt.Sprintf("race-void-%d", i))
			voidStatus = resp.StatusCode
			resp.Body.Close()
		}()

		close(start)
		wg.Wait()

		switch {
		case captureStatus == http.StatusOK && voidStatus == http.StatusConflict:
		case voidStatus == http.StatusOK && captureStatus == http.StatusConflict:
		default:
			t.Errorf("iteration %d: exactly one of capture/void must win, got capture=%d void=%d",
				i, captureStatus, voidStatus)
		}
	}

	var outcomes []models.Transaction
	require.NoError(t, ts.Database.GORM().
		Where("type IN ?", []models.TransactionType{
			models.TransactionTypeCapture,
			models.TransactionTypeVoid,
		}).
		Find(&outcomes).Error)
	require.Len(t, outcomes, iterations, "each authorization must resolve to exactly one outcome")

	perAuthorization := make(map[uuid.UUID]int, len(outcomes))
	for _, txn := range outcomes {
		require.NotNil(t, txn.ReferenceID, "capture/void must reference its authorization")
		perAuthorization[*txn.ReferenceID]++
	}
	for _, authID := range authIDs {
		assert.Equal(t, 1, perAuthorization[authID], "authorization %s must have exactly one terminal row", authID)
	}

	ts.assertLedgerReconciled(t)
}
