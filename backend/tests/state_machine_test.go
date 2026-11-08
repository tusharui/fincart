package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"github.com/tusharui/payment-gateway/backend/internal/service"
)

// errorCodeFromResponse decodes the error code from an error response and
// closes the body.
func errorCodeFromResponse(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	code, _ := body["error"].(string)
	return code
}

func TestStateMachine_CaptureThenRefund(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	const cardNumber = "4111111111111111"

	authResp := ts.Authorize(t, cardNumber, "123", 10000, "sm-capture-auth")
	require.Equal(t, http.StatusOK, authResp.StatusCode)
	var authBody map[string]any
	require.NoError(t, json.NewDecoder(authResp.Body).Decode(&authBody))
	authResp.Body.Close()
	authID := authBody["authorization_id"].(string)

	// A capture amount must match the authorization exactly.
	wrongAmount := ts.Capture(t, authID, 9000, "sm-capture-wrong-amount")
	require.Equal(t, http.StatusBadRequest, wrongAmount.StatusCode)
	assert.Equal(t, "amount_mismatch", errorCodeFromResponse(t, wrongAmount))

	captureResp := ts.Capture(t, authID, 10000, "sm-capture")
	require.Equal(t, http.StatusOK, captureResp.StatusCode)
	var captureBody map[string]any
	require.NoError(t, json.NewDecoder(captureResp.Body).Decode(&captureBody))
	captureResp.Body.Close()
	captureID := captureBody["capture_id"].(string)

	// Captured authorizations are terminal: no second capture and no void.
	secondCapture := ts.Capture(t, authID, 10000, "sm-capture-twice")
	require.Equal(t, http.StatusConflict, secondCapture.StatusCode)
	assert.Equal(t, "authorization_already_used", errorCodeFromResponse(t, secondCapture))

	voidAfterCapture := ts.Void(t, authID, "sm-void-after-capture")
	require.Equal(t, http.StatusConflict, voidAfterCapture.StatusCode)
	assert.Equal(t, "authorization_already_used", errorCodeFromResponse(t, voidAfterCapture))

	// Refund amount must match the capture, and a capture can only be refunded once.
	wrongRefund := ts.Refund(t, captureID, 9000, "sm-refund-wrong-amount")
	require.Equal(t, http.StatusBadRequest, wrongRefund.StatusCode)
	assert.Equal(t, "amount_mismatch", errorCodeFromResponse(t, wrongRefund))

	refundResp := ts.Refund(t, captureID, 10000, "sm-refund")
	require.Equal(t, http.StatusOK, refundResp.StatusCode)
	refundResp.Body.Close()

	secondRefund := ts.Refund(t, captureID, 10000, "sm-refund-twice")
	require.Equal(t, http.StatusConflict, secondRefund.StatusCode)
	assert.Equal(t, "already_refunded", errorCodeFromResponse(t, secondRefund))

	ts.assertLedgerReconciled(t)
	assertAccountBalances(t, ts, cardNumber, 1000000, 1000000)
}

func TestStateMachine_VoidLifecycle(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	const cardNumber = "4111111111111111"

	authResp := ts.Authorize(t, cardNumber, "123", 20000, "sm-void-auth")
	require.Equal(t, http.StatusOK, authResp.StatusCode)
	var authBody map[string]any
	require.NoError(t, json.NewDecoder(authResp.Body).Decode(&authBody))
	authResp.Body.Close()
	authID := authBody["authorization_id"].(string)

	voidResp := ts.Void(t, authID, "sm-void")
	require.Equal(t, http.StatusOK, voidResp.StatusCode)
	voidResp.Body.Close()

	secondVoid := ts.Void(t, authID, "sm-void-twice")
	require.Equal(t, http.StatusConflict, secondVoid.StatusCode)
	assert.Equal(t, "already_voided", errorCodeFromResponse(t, secondVoid))

	captureAfterVoid := ts.Capture(t, authID, 20000, "sm-capture-after-void")
	require.Equal(t, http.StatusConflict, captureAfterVoid.StatusCode)
	assert.Equal(t, "already_voided", errorCodeFromResponse(t, captureAfterVoid))

	ts.assertLedgerReconciled(t)
	assertAccountBalances(t, ts, cardNumber, 1000000, 1000000)
}

func TestStateMachine_ExpiredAuthorization(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	const cardNumber = "4111111111111111"

	// authorizeAndAge creates a hold and backdates its expiry so it looks
	// stale to the capture path and the sweep.
	authorizeAndAge := func(idempotencyKey string) (string, uuid.UUID) {
		authResp := ts.Authorize(t, cardNumber, "123", 15000, idempotencyKey)
		require.Equal(t, http.StatusOK, authResp.StatusCode)
		var authBody map[string]any
		require.NoError(t, json.NewDecoder(authResp.Body).Decode(&authBody))
		authResp.Body.Close()

		formattedID := authBody["authorization_id"].(string)
		authID, err := uuid.Parse(strings.TrimPrefix(formattedID, "auth_"))
		require.NoError(t, err)

		require.NoError(t, ts.Database.GORM().
			Model(&models.Transaction{}).
			Where("id = ?", authID).
			Update("expires_at", time.Now().Add(-time.Hour)).Error)

		return formattedID, authID
	}

	// Before the sweep, capture refuses the aged hold while void is still
	// allowed because it only releases the reservation.
	voidTargetID, _ := authorizeAndAge("sm-expired-void-auth")

	expiredCapture := ts.Capture(t, voidTargetID, 15000, "sm-expired-capture")
	require.Equal(t, http.StatusConflict, expiredCapture.StatusCode)
	assert.Equal(t, "authorization_expired", errorCodeFromResponse(t, expiredCapture))

	expiredVoid := ts.Void(t, voidTargetID, "sm-expired-void")
	require.Equal(t, http.StatusOK, expiredVoid.StatusCode)
	expiredVoid.Body.Close()

	// The sweep expires the remaining aged hold and releases the reserved funds.
	sweepTargetID, sweepAuthID := authorizeAndAge("sm-expired-sweep-auth")

	expired, err := service.NewExpiryService(ts.Database).ExpireAuthorizations(context.Background(), time.Now())
	require.NoError(t, err)
	require.Equal(t, 1, expired)

	var authTxn models.Transaction
	require.NoError(t, ts.Database.GORM().First(&authTxn, "id = ?", sweepAuthID).Error)
	assert.Equal(t, models.TransactionStatusExpired, authTxn.Status)

	// The terminal expired state remains non-capturable and non-voidable.
	lateCapture := ts.Capture(t, sweepTargetID, 15000, "sm-expired-late-capture")
	require.Equal(t, http.StatusConflict, lateCapture.StatusCode)
	assert.Equal(t, "authorization_expired", errorCodeFromResponse(t, lateCapture))

	lateVoid := ts.Void(t, sweepTargetID, "sm-expired-late-void")
	require.Equal(t, http.StatusConflict, lateVoid.StatusCode)
	assert.Equal(t, "authorization_expired", errorCodeFromResponse(t, lateVoid))

	ts.assertLedgerReconciled(t)
	assertAccountBalances(t, ts, cardNumber, 1000000, 1000000)
}

func TestStateMachine_UnknownResources(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	unknown := uuid.New()
	unknownAuthID := "auth_" + unknown.String()
	unknownCaptureID := "cap_" + unknown.String()

	captureResp := ts.Capture(t, unknownAuthID, 10000, "sm-unknown-capture")
	require.Equal(t, http.StatusNotFound, captureResp.StatusCode)
	assert.Equal(t, "authorization_not_found", errorCodeFromResponse(t, captureResp))

	voidResp := ts.Void(t, unknownAuthID, "sm-unknown-void")
	require.Equal(t, http.StatusNotFound, voidResp.StatusCode)
	assert.Equal(t, "authorization_not_found", errorCodeFromResponse(t, voidResp))

	refundResp := ts.Refund(t, unknownCaptureID, 10000, "sm-unknown-refund")
	require.Equal(t, http.StatusNotFound, refundResp.StatusCode)
	assert.Equal(t, "capture_not_found", errorCodeFromResponse(t, refundResp))

	// A refund pointed at an authorization ID is not a capture.
	authResp := ts.Authorize(t, "4111111111111111", "123", 10000, "sm-refund-auth-id-auth")
	require.Equal(t, http.StatusOK, authResp.StatusCode)
	var authBody map[string]any
	require.NoError(t, json.NewDecoder(authResp.Body).Decode(&authBody))
	authResp.Body.Close()

	authUUID, err := uuid.Parse(strings.TrimPrefix(authBody["authorization_id"].(string), "auth_"))
	require.NoError(t, err)

	captureRefund := ts.Refund(t, "cap_"+authUUID.String(), 10000, "sm-refund-auth-id")
	require.Equal(t, http.StatusNotFound, captureRefund.StatusCode)
	assert.Equal(t, "capture_not_found", errorCodeFromResponse(t, captureRefund))

	ts.assertLedgerReconciled(t)
}
