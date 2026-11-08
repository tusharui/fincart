//nolint:errcheck // unchecked errors are acceptable in test files
package tests

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"github.com/tusharui/payment-gateway/backend/internal/security"
	"github.com/tusharui/payment-gateway/backend/internal/service"
)

func TestAuthorizeAndCapture(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	authResp := ts.Authorize(t, "4111111111111111", "123", 10000, "auth-cap-key-1")
	require.Equal(t, http.StatusOK, authResp.StatusCode)

	var authBody map[string]any
	require.NoError(t, json.NewDecoder(authResp.Body).Decode(&authBody))
	authResp.Body.Close()

	assert.Equal(t, "approved", authBody["status"])
	assert.Equal(t, float64(10000), authBody["amount"])
	authID := authBody["authorization_id"].(string)
	assert.Contains(t, authID, "auth_")

	captureResp := ts.Capture(t, authID, 10000, "cap-key-1")
	require.Equal(t, http.StatusOK, captureResp.StatusCode)

	var captureBody map[string]any
	require.NoError(t, json.NewDecoder(captureResp.Body).Decode(&captureBody))
	captureResp.Body.Close()

	assert.Equal(t, "captured", captureBody["status"])
	assert.Equal(t, authID, captureBody["authorization_id"])
	assert.Contains(t, captureBody["capture_id"].(string), "cap_")
}

func TestAuthorizeAndVoid(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	authResp := ts.Authorize(t, "4111111111111111", "123", 20000, "auth-void-key-1")
	require.Equal(t, http.StatusOK, authResp.StatusCode)

	var authBody map[string]any
	require.NoError(t, json.NewDecoder(authResp.Body).Decode(&authBody))
	authResp.Body.Close()
	authID := authBody["authorization_id"].(string)

	voidResp := ts.Void(t, authID, "void-key-1")
	require.Equal(t, http.StatusOK, voidResp.StatusCode)

	var voidBody map[string]any
	require.NoError(t, json.NewDecoder(voidResp.Body).Decode(&voidBody))
	voidResp.Body.Close()

	assert.Equal(t, "voided", voidBody["status"])
	assert.Equal(t, authID, voidBody["authorization_id"])
}

func TestFullFlow_AuthorizeCaptureRefund(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	authResp := ts.Authorize(t, "4111111111111111", "123", 15000, "full-flow-auth-1")
	require.Equal(t, http.StatusOK, authResp.StatusCode)

	var authBody map[string]any
	require.NoError(t, json.NewDecoder(authResp.Body).Decode(&authBody))
	authResp.Body.Close()
	authID := authBody["authorization_id"].(string)

	captureResp := ts.Capture(t, authID, 15000, "full-flow-cap-1")
	require.Equal(t, http.StatusOK, captureResp.StatusCode)

	var captureBody map[string]any
	require.NoError(t, json.NewDecoder(captureResp.Body).Decode(&captureBody))
	captureResp.Body.Close()
	captureID := captureBody["capture_id"].(string)

	refundResp := ts.Refund(t, captureID, 15000, "full-flow-refund-1")
	require.Equal(t, http.StatusOK, refundResp.StatusCode)

	var refundBody map[string]any
	require.NoError(t, json.NewDecoder(refundResp.Body).Decode(&refundBody))
	refundResp.Body.Close()

	assert.Equal(t, "refunded", refundBody["status"])
	assert.Equal(t, captureID, refundBody["capture_id"])
	assert.Contains(t, refundBody["refund_id"].(string), "ref_")
}

func TestOrderCustomerLinkage_PropagatesThroughLifecycle(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	orderID := uuid.New()
	customerID := uuid.New()

	authResp := ts.AuthorizeLinked(t, "4111111111111111", "123", 10000, orderID.String(), customerID.String(), "linkage-auth")
	require.Equal(t, http.StatusOK, authResp.StatusCode)

	var authBody map[string]any
	require.NoError(t, json.NewDecoder(authResp.Body).Decode(&authBody))
	authResp.Body.Close()

	assert.Equal(t, orderID.String(), authBody["order_id"])
	assert.Equal(t, customerID.String(), authBody["customer_id"])
	authID := authBody["authorization_id"].(string)

	getAuth, err := http.Get(ts.URL("/api/v1/authorizations/" + authID))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getAuth.StatusCode)
	var getAuthBody map[string]any
	require.NoError(t, json.NewDecoder(getAuth.Body).Decode(&getAuthBody))
	getAuth.Body.Close()
	assert.Equal(t, orderID.String(), getAuthBody["order_id"])
	assert.Equal(t, customerID.String(), getAuthBody["customer_id"])

	captureResp := ts.Capture(t, authID, 10000, "linkage-cap")
	require.Equal(t, http.StatusOK, captureResp.StatusCode)
	var captureBody map[string]any
	require.NoError(t, json.NewDecoder(captureResp.Body).Decode(&captureBody))
	captureResp.Body.Close()
	assert.Equal(t, orderID.String(), captureBody["order_id"])
	assert.Equal(t, customerID.String(), captureBody["customer_id"])
	captureID := captureBody["capture_id"].(string)

	getCapture, err := http.Get(ts.URL("/api/v1/captures/" + captureID))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getCapture.StatusCode)
	var getCaptureBody map[string]any
	require.NoError(t, json.NewDecoder(getCapture.Body).Decode(&getCaptureBody))
	getCapture.Body.Close()
	assert.Equal(t, orderID.String(), getCaptureBody["order_id"])
	assert.Equal(t, customerID.String(), getCaptureBody["customer_id"])

	refundResp := ts.Refund(t, captureID, 10000, "linkage-refund")
	require.Equal(t, http.StatusOK, refundResp.StatusCode)
	var refundBody map[string]any
	require.NoError(t, json.NewDecoder(refundResp.Body).Decode(&refundBody))
	refundResp.Body.Close()
	assert.Equal(t, orderID.String(), refundBody["order_id"])
	assert.Equal(t, customerID.String(), refundBody["customer_id"])
	refundID := refundBody["refund_id"].(string)

	getRefund, err := http.Get(ts.URL("/api/v1/refunds/" + refundID))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getRefund.StatusCode)
	var getRefundBody map[string]any
	require.NoError(t, json.NewDecoder(getRefund.Body).Decode(&getRefundBody))
	getRefund.Body.Close()
	assert.Equal(t, orderID.String(), getRefundBody["order_id"])
	assert.Equal(t, customerID.String(), getRefundBody["customer_id"])

	var persisted []models.Transaction
	require.NoError(t, ts.Database.GORM().Where("order_id = ?", orderID).Order("created_at").Find(&persisted).Error)
	require.Len(t, persisted, 3, "authorization, capture, and refund should all carry the linkage")
	for _, txn := range persisted {
		if assert.NotNil(t, txn.CustomerID) {
			assert.Equal(t, customerID, *txn.CustomerID)
		}
	}
}

func TestOrderCustomerLinkage_PropagatesToVoid(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	orderID := uuid.New()
	customerID := uuid.New()

	authResp := ts.AuthorizeLinked(t, "4111111111111111", "123", 20000, orderID.String(), customerID.String(), "linkage-void-auth")
	require.Equal(t, http.StatusOK, authResp.StatusCode)

	var authBody map[string]any
	require.NoError(t, json.NewDecoder(authResp.Body).Decode(&authBody))
	authResp.Body.Close()
	authID := authBody["authorization_id"].(string)

	voidResp := ts.Void(t, authID, "linkage-void")
	require.Equal(t, http.StatusOK, voidResp.StatusCode)

	var voidBody map[string]any
	require.NoError(t, json.NewDecoder(voidResp.Body).Decode(&voidBody))
	voidResp.Body.Close()
	assert.Equal(t, orderID.String(), voidBody["order_id"])
	assert.Equal(t, customerID.String(), voidBody["customer_id"])
}

func TestAuthorization_RejectsInvalidLinkage(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	resp := ts.AuthorizeLinked(t, "4111111111111111", "123", 10000, "not-a-uuid", "", "invalid-linkage")
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	resp.Body.Close()
	assert.Equal(t, "invalid_request", body["error"])

	var count int64
	require.NoError(t, ts.Database.GORM().Model(&models.Transaction{}).Count(&count).Error)
	assert.Zero(t, count, "invalid linkage must not create a transaction")
}

func TestAuthorization_InvalidCard(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	resp := ts.Authorize(t, "4111111111111112", "123", 10000, "invalid-card-key") // Invalid Luhn
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	resp.Body.Close()

	assert.Equal(t, "invalid_card", body["error"])
}

func TestAuthorization_InvalidCVVFormat(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	resp := ts.Authorize(t, "4111111111111111", "12", 10000, "invalid-cvv-key")
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	resp.Body.Close()

	assert.Equal(t, "invalid_cvv", body["error"])
}

func TestAuthorization_ExpiredCard(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	resp := ts.Authorize(t, "5105105105105100", "321", 10000, "expired-card-key") // Expiry 03/2020
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	resp.Body.Close()

	assert.Equal(t, "card_expired", body["error"])
}

func TestAuthorization_InsufficientFunds(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	resp := ts.Authorize(t, "5555555555554444", "789", 100, "insufficient-funds-key") // Balance: $0
	require.Equal(t, http.StatusPaymentRequired, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	resp.Body.Close()

	assert.Equal(t, "insufficient_funds", body["error"])
}

func TestCapture_AuthorizationAlreadyUsed(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	authResp := ts.Authorize(t, "4111111111111111", "123", 10000, "double-cap-auth")
	require.Equal(t, http.StatusOK, authResp.StatusCode)

	var authBody map[string]any
	require.NoError(t, json.NewDecoder(authResp.Body).Decode(&authBody))
	authResp.Body.Close()
	authID := authBody["authorization_id"].(string)

	cap1 := ts.Capture(t, authID, 10000, "double-cap-1")
	require.Equal(t, http.StatusOK, cap1.StatusCode)
	cap1.Body.Close()

	cap2 := ts.Capture(t, authID, 10000, "double-cap-2")
	require.Equal(t, http.StatusConflict, cap2.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(cap2.Body).Decode(&body))
	cap2.Body.Close()

	assert.Equal(t, "authorization_already_used", body["error"])
}

func TestVoid_AfterCapture(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	authResp := ts.Authorize(t, "4111111111111111", "123", 10000, "void-after-cap-auth")
	require.Equal(t, http.StatusOK, authResp.StatusCode)

	var authBody map[string]any
	require.NoError(t, json.NewDecoder(authResp.Body).Decode(&authBody))
	authResp.Body.Close()
	authID := authBody["authorization_id"].(string)

	capResp := ts.Capture(t, authID, 10000, "void-after-cap-cap")
	require.Equal(t, http.StatusOK, capResp.StatusCode)
	capResp.Body.Close()

	voidResp := ts.Void(t, authID, "void-after-cap-void")
	require.Equal(t, http.StatusConflict, voidResp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(voidResp.Body).Decode(&body))
	voidResp.Body.Close()

	// After capture, auth status is COMPLETED so we get "authorization_already_used"
	assert.Equal(t, "authorization_already_used", body["error"])
}

func TestIdempotency_ReplaysSameResponse(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	idempotencyKey := "replay-test-key"

	resp1 := ts.Authorize(t, "4111111111111111", "123", 10000, idempotencyKey)
	require.Equal(t, http.StatusOK, resp1.StatusCode)
	body1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()

	resp2 := ts.Authorize(t, "4111111111111111", "123", 10000, idempotencyKey)
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()

	assert.Equal(t, string(body1), string(body2))
	assert.Equal(t, "true", resp2.Header.Get("X-Idempotent-Replayed"))
}

func TestIdempotency_DifferentKeysCreateDifferentAuthorizations(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	resp1 := ts.Authorize(t, "4111111111111111", "123", 10000, "key-1")
	require.Equal(t, http.StatusOK, resp1.StatusCode)

	var body1 map[string]any
	require.NoError(t, json.NewDecoder(resp1.Body).Decode(&body1))
	resp1.Body.Close()

	resp2 := ts.Authorize(t, "4111111111111111", "123", 10000, "key-2")
	require.Equal(t, http.StatusOK, resp2.StatusCode)

	var body2 map[string]any
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&body2))
	resp2.Body.Close()

	assert.NotEqual(t, body1["authorization_id"], body2["authorization_id"])
}

func TestIdempotency_ReusedKeyWithDifferentBodyIsRejected(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	const idempotencyKey = "payload-conflict-key"

	resp1 := ts.Authorize(t, "4111111111111111", "123", 10000, idempotencyKey)
	require.Equal(t, http.StatusOK, resp1.StatusCode)
	resp1.Body.Close()

	resp2 := ts.Authorize(t, "4111111111111111", "123", 20000, idempotencyKey)
	require.Equal(t, http.StatusConflict, resp2.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&body))
	resp2.Body.Close()

	assert.Equal(t, "idempotency_key_conflict", body["error"])
}

func TestIdempotency_MissingKeyIsRejected(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	req, err := http.NewRequest(
		http.MethodPost,
		ts.URL("/api/v1/authorizations"),
		strings.NewReader(`{"card_number":"4111111111111111","cvv":"123","amount":10000}`),
	)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	// Intentionally no Idempotency-Key header.

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "missing_idempotency_key", body["error"])
}

func TestConcurrentCaptures_OnlyOneSucceeds(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	authResp := ts.Authorize(t, "4111111111111111", "123", 10000, "concurrent-cap-auth")
	require.Equal(t, http.StatusOK, authResp.StatusCode)

	var authBody map[string]any
	require.NoError(t, json.NewDecoder(authResp.Body).Decode(&authBody))
	authResp.Body.Close()
	authID := authBody["authorization_id"].(string)

	const numGoroutines = 10
	var wg sync.WaitGroup
	results := make(chan int, numGoroutines)

	for i := range numGoroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			resp := ts.Capture(t, authID, 10000, "concurrent-cap-"+string(rune('a'+idx)))
			results <- resp.StatusCode
			resp.Body.Close()
		}(i)
	}

	wg.Wait()
	close(results)

	successCount := 0
	conflictCount := 0
	for code := range results {
		switch code {
		case http.StatusOK:
			successCount++
		case http.StatusConflict:
			conflictCount++
		}
	}

	assert.Equal(t, 1, successCount, "exactly one capture should succeed")
	assert.Equal(t, numGoroutines-1, conflictCount, "all others should conflict")
}

func TestAuthorizationExpirySweepReleasesFunds(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	authResp := ts.Authorize(t, "4111111111111111", "123", 10000, "expiry-sweep-key")
	require.Equal(t, http.StatusOK, authResp.StatusCode)

	var authBody map[string]any
	require.NoError(t, json.NewDecoder(authResp.Body).Decode(&authBody))
	authResp.Body.Close()

	formattedID := authBody["authorization_id"].(string)
	authID, err := uuid.Parse(strings.TrimPrefix(formattedID, "auth_"))
	require.NoError(t, err)

	// Age the authorization so the sweep treats it as expired.
	past := time.Now().Add(-time.Hour)
	require.NoError(t, ts.Database.GORM().
		Model(&models.Transaction{}).
		Where("id = ?", authID).
		Update("expires_at", past).Error)

	expiryService := service.NewExpiryService(ts.Database)
	expiredCount, err := expiryService.ExpireAuthorizations(context.Background(), time.Now())
	require.NoError(t, err)
	assert.Equal(t, 1, expiredCount, "one authorization should be expired")

	var authTxn models.Transaction
	require.NoError(t, ts.Database.GORM().First(&authTxn, "id = ?", authID).Error)
	assert.Equal(t, models.TransactionStatusExpired, authTxn.Status)

	var account models.Account
	require.NoError(t, ts.Database.GORM().First(&account, "id = ?", authTxn.AccountID).Error)
	assert.Equal(t, int64(1000000), account.AvailableBalanceCents,
		"reserved funds should be returned to the available balance")

	// A second sweep must be a no-op.
	expiredCount, err = expiryService.ExpireAuthorizations(context.Background(), time.Now())
	require.NoError(t, err)
	assert.Equal(t, 0, expiredCount)

	// An expired authorization can no longer be captured.
	captureResp := ts.Capture(t, formattedID, 10000, "expiry-sweep-capture")
	assert.Equal(t, http.StatusConflict, captureResp.StatusCode)
	captureResp.Body.Close()
}

func TestGetAuthorization(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	authResp := ts.Authorize(t, "4111111111111111", "123", 10000, "get-auth-key")
	require.Equal(t, http.StatusOK, authResp.StatusCode)

	var authBody map[string]any
	require.NoError(t, json.NewDecoder(authResp.Body).Decode(&authBody))
	authResp.Body.Close()
	authID := authBody["authorization_id"].(string)

	resp, err := http.Get(ts.URL("/api/v1/authorizations/" + authID))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var getBody map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&getBody))
	resp.Body.Close()

	assert.Equal(t, authID, getBody["authorization_id"])
	assert.Equal(t, float64(10000), getBody["amount"])
	assert.Equal(t, "approved", getBody["status"])
}

func TestGetAuthorization_AfterCapture(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	authResp := ts.Authorize(t, "4111111111111111", "123", 10000, "get-auth-captured-key")
	require.Equal(t, http.StatusOK, authResp.StatusCode)

	var authBody map[string]any
	require.NoError(t, json.NewDecoder(authResp.Body).Decode(&authBody))
	authResp.Body.Close()
	authID := authBody["authorization_id"].(string)

	captureResp := ts.Capture(t, authID, 10000, "get-auth-captured-cap")
	require.Equal(t, http.StatusOK, captureResp.StatusCode)
	captureResp.Body.Close()

	resp, err := http.Get(ts.URL("/api/v1/authorizations/" + authID))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var getBody map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&getBody))
	resp.Body.Close()

	assert.Equal(t, "captured", getBody["status"])
}

func TestGetAuthorization_AfterVoid(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	authResp := ts.Authorize(t, "4111111111111111", "123", 10000, "get-auth-voided-key")
	require.Equal(t, http.StatusOK, authResp.StatusCode)

	var authBody map[string]any
	require.NoError(t, json.NewDecoder(authResp.Body).Decode(&authBody))
	authResp.Body.Close()
	authID := authBody["authorization_id"].(string)

	voidResp := ts.Void(t, authID, "get-auth-voided-void")
	require.Equal(t, http.StatusOK, voidResp.StatusCode)
	voidResp.Body.Close()

	resp, err := http.Get(ts.URL("/api/v1/authorizations/" + authID))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var getBody map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&getBody))
	resp.Body.Close()

	assert.Equal(t, "voided", getBody["status"])
}

func TestGetCapture_AfterRefund(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	authResp := ts.Authorize(t, "4111111111111111", "123", 10000, "get-cap-refunded-auth")
	require.Equal(t, http.StatusOK, authResp.StatusCode)

	var authBody map[string]any
	require.NoError(t, json.NewDecoder(authResp.Body).Decode(&authBody))
	authResp.Body.Close()
	authID := authBody["authorization_id"].(string)

	captureResp := ts.Capture(t, authID, 10000, "get-cap-refunded-cap")
	require.Equal(t, http.StatusOK, captureResp.StatusCode)

	var captureBody map[string]any
	require.NoError(t, json.NewDecoder(captureResp.Body).Decode(&captureBody))
	captureResp.Body.Close()
	captureID := captureBody["capture_id"].(string)

	getCapture := func() map[string]any {
		resp, err := http.Get(ts.URL("/api/v1/captures/" + captureID))
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		defer resp.Body.Close()

		var body map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		return body
	}

	assert.Equal(t, "captured", getCapture()["status"])

	refundResp := ts.Refund(t, captureID, 10000, "get-cap-refunded-refund")
	require.Equal(t, http.StatusOK, refundResp.StatusCode)
	refundResp.Body.Close()

	assert.Equal(t, "refunded", getCapture()["status"])
}

func TestGetAuthorization_NotFound(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL("/api/v1/authorizations/auth_00000000-0000-0000-0000-000000000000"))
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()
}

func TestBalanceInvariantViolationRollsBackCaptureAndVoid(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	authResp := ts.Authorize(t, "4111111111111111", "123", 10000, "invariant-auth-key")
	require.Equal(t, http.StatusOK, authResp.StatusCode)

	var authBody map[string]any
	require.NoError(t, json.NewDecoder(authResp.Body).Decode(&authBody))
	authResp.Body.Close()

	formattedID := authBody["authorization_id"].(string)
	authID, err := uuid.Parse(strings.TrimPrefix(formattedID, "auth_"))
	require.NoError(t, err)

	// Simulate out-of-band tampering that leaves the account with no settled
	// funds while a hold is still active. The balance guard must refuse to move
	// either balance, and the whole transaction must roll back.
	require.NoError(t, ts.Database.GORM().
		Model(&models.Account{}).
		Where("card_token = ?", security.HashToken("4111111111111111")).
		Updates(map[string]any{
			"balance_cents":           0,
			"available_balance_cents": 0,
		}).Error)

	captureResp := ts.Capture(t, formattedID, 10000, "invariant-capture-key")
	require.Equal(t, http.StatusBadRequest, captureResp.StatusCode)

	var captureBody map[string]any
	require.NoError(t, json.NewDecoder(captureResp.Body).Decode(&captureBody))
	captureResp.Body.Close()
	assert.Equal(t, "insufficient_funds", captureBody["error"])

	voidResp := ts.Void(t, formattedID, "invariant-void-key")
	require.Equal(t, http.StatusInternalServerError, voidResp.StatusCode)
	voidResp.Body.Close()

	// Neither attempt may leave a ledger row or change the authorization.
	var authTxn models.Transaction
	require.NoError(t, ts.Database.GORM().First(&authTxn, "id = ?", authID).Error)
	assert.Equal(t, models.TransactionStatusActive, authTxn.Status)

	var ledgerCount int64
	require.NoError(t, ts.Database.GORM().
		Model(&models.Transaction{}).
		Where("reference_id = ?", authID).
		Count(&ledgerCount).Error)
	assert.Equal(t, int64(0), ledgerCount, "failed capture/void must not leave ledger rows")

	var account models.Account
	require.NoError(t, ts.Database.GORM().
		Where("card_token = ?", security.HashToken("4111111111111111")).
		First(&account).Error)
	assert.Equal(t, int64(0), account.BalanceCents)
	assert.Equal(t, int64(0), account.AvailableBalanceCents)
}
