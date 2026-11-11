package tests

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tusharui/payment-gateway/backend/internal/config"
	"github.com/tusharui/payment-gateway/backend/internal/events"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"github.com/tusharui/payment-gateway/backend/internal/repository"
	"github.com/tusharui/payment-gateway/backend/internal/service"
	"github.com/tusharui/payment-gateway/backend/internal/webhook"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func decodeJSONBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	return body
}

func listStoredEvents(t *testing.T, ts *TestServer) []models.PaymentEvent {
	t.Helper()

	var stored []models.PaymentEvent
	require.NoError(t, ts.Database.GORM().Order("created_at ASC").Find(&stored).Error)
	return stored
}

func eventTypes(stored []models.PaymentEvent) []string {
	types := make([]string, len(stored))
	for i := range stored {
		types[i] = stored[i].Type
	}
	sort.Strings(types)
	return types
}

func TestPaymentEvents_EmittedWithLedgerTransitions(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	authResp := ts.Authorize(t, "4111111111111111", "123", 10000, "evt-auth")
	require.Equal(t, http.StatusOK, authResp.StatusCode)
	authBody := decodeJSONBody(t, authResp)
	authID := authBody["authorization_id"].(string)

	captureResp := ts.Capture(t, authID, 10000, "evt-capture")
	require.Equal(t, http.StatusOK, captureResp.StatusCode)
	captureBody := decodeJSONBody(t, captureResp)
	captureID := captureBody["capture_id"].(string)

	refundResp := ts.Refund(t, captureID, 10000, "evt-refund")
	require.Equal(t, http.StatusOK, refundResp.StatusCode)
	refundBody := decodeJSONBody(t, refundResp)
	refundID := refundBody["refund_id"].(string)

	voidAuthResp := ts.Authorize(t, "4111111111111111", "123", 5000, "evt-void-auth")
	require.Equal(t, http.StatusOK, voidAuthResp.StatusCode)
	voidAuthID := decodeJSONBody(t, voidAuthResp)["authorization_id"].(string)

	voidResp := ts.Void(t, voidAuthID, "evt-void")
	require.Equal(t, http.StatusOK, voidResp.StatusCode)
	voidID := decodeJSONBody(t, voidResp)["void_id"].(string)

	stored := listStoredEvents(t, ts)
	require.Len(t, stored, 5)
	assert.Equal(t, []string{
		events.EventPaymentAuthorized,
		events.EventPaymentAuthorized,
		events.EventPaymentCaptured,
		events.EventPaymentRefunded,
		events.EventPaymentVoided,
	}, eventTypes(stored))

	byType := make(map[string]events.Event)
	for _, row := range stored {
		var payload events.Event
		require.NoError(t, json.Unmarshal([]byte(row.Payload), &payload))
		assert.Equal(t, row.ID, payload.ID)
		assert.Equal(t, row.Type, payload.Type)
		assert.Equal(t, events.SchemaVersion, payload.SchemaVersion)
		byType[payload.TransactionID.String()] = payload
	}
	assert.Len(t, byType, len(stored), "each event must have a unique transaction id")

	var captured, refunded, voided events.Event
	for _, row := range stored {
		var payload events.Event
		require.NoError(t, json.Unmarshal([]byte(row.Payload), &payload))
		switch payload.Type {
		case events.EventPaymentCaptured:
			captured = payload
		case events.EventPaymentRefunded:
			refunded = payload
		case events.EventPaymentVoided:
			voided = payload
		}
	}

	require.NotNil(t, captured.Data.AuthorizationID)
	assert.Equal(t, authID, "auth_"+captured.Data.AuthorizationID.String())
	require.NotNil(t, captured.Data.CaptureID)
	assert.Equal(t, captureID, "cap_"+captured.Data.CaptureID.String())
	assert.Equal(t, int64(10000), captured.Data.AmountCents)
	assert.Equal(t, string(models.TransactionStatusCompleted), captured.Data.Status)

	require.NotNil(t, refunded.Data.CaptureID)
	assert.Equal(t, captureID, "cap_"+refunded.Data.CaptureID.String())
	require.NotNil(t, refunded.Data.RefundID)
	assert.Equal(t, refundID, "ref_"+refunded.Data.RefundID.String())

	require.NotNil(t, voided.Data.AuthorizationID)
	assert.Equal(t, voidAuthID, "auth_"+voided.Data.AuthorizationID.String())
	require.NotNil(t, voided.Data.VoidID)
	assert.Equal(t, voidID, "void_"+voided.Data.VoidID.String())
}

func TestPaymentEvents_NotEmittedWhenTransitionFails(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	authResp := ts.Authorize(t, "4111111111111111", "123", 10000, "evt-failed-auth")
	require.Equal(t, http.StatusOK, authResp.StatusCode)
	authID := decodeJSONBody(t, authResp)["authorization_id"].(string)

	failedCapture := ts.Capture(t, authID, 9000, "evt-failed-capture")
	require.Equal(t, http.StatusBadRequest, failedCapture.StatusCode)
	failedCapture.Body.Close()

	stored := listStoredEvents(t, ts)
	require.Len(t, stored, 1, "a rejected capture must not write an event")
	assert.Equal(t, events.EventPaymentAuthorized, stored[0].Type)
}

func TestPaymentEvents_ExpiredAuthorization(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	authResp := ts.Authorize(t, "4111111111111111", "123", 15000, "evt-expiry-auth")
	require.Equal(t, http.StatusOK, authResp.StatusCode)
	authID := decodeJSONBody(t, authResp)["authorization_id"].(string)

	require.NoError(t, ts.Database.GORM().
		Model(&models.Transaction{}).
		Where("id = ?", authID[len("auth_"):]).
		Update("expires_at", time.Now().Add(-time.Hour)).Error)

	expired, err := service.NewExpiryService(ts.Database).ExpireAuthorizations(context.Background(), time.Now())
	require.NoError(t, err)
	require.Equal(t, 1, expired)

	stored := listStoredEvents(t, ts)
	require.Len(t, stored, 2)
	assert.Equal(t, []string{
		events.EventPaymentAuthorized,
		events.EventPaymentExpired,
	}, eventTypes(stored))

	var expiredEvent events.Event
	for _, row := range stored {
		if row.Type == events.EventPaymentExpired {
			require.NoError(t, json.Unmarshal([]byte(row.Payload), &expiredEvent))
		}
	}
	assert.Equal(t, string(models.TransactionStatusExpired), expiredEvent.Data.Status)
	require.NotNil(t, expiredEvent.Data.AuthorizationID)
	assert.Equal(t, authID, "auth_"+expiredEvent.Data.AuthorizationID.String())
}

// webhookReceiver is a test endpoint that records every delivery.
type webhookReceiver struct {
	server *httptest.Server

	mu       sync.Mutex
	status   int
	requests []capturedWebhook
}

type capturedWebhook struct {
	header http.Header
	body   []byte
}

func newWebhookReceiver(t *testing.T, status int) *webhookReceiver {
	t.Helper()

	receiver := &webhookReceiver{status: status}
	receiver.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		receiver.mu.Lock()
		receiver.requests = append(receiver.requests, capturedWebhook{
			header: r.Header.Clone(),
			body:   body,
		})
		status := receiver.status
		receiver.mu.Unlock()

		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"received":true}`))
	}))
	t.Cleanup(receiver.server.Close)

	return receiver
}

func (r *webhookReceiver) setStatus(status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = status
}

func (r *webhookReceiver) captured() []capturedWebhook {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]capturedWebhook, len(r.requests))
	copy(out, r.requests)
	return out
}

func newTestDispatcher(t *testing.T, ts *TestServer, endpoint, secret string, maxAttempts int) *webhook.Dispatcher {
	t.Helper()

	return webhook.NewDispatcher(ts.Database.GORM(), config.WebhookConfig{
		Enabled:       true,
		Endpoints:     []string{endpoint},
		Secret:        secret,
		MaxAttempts:   maxAttempts,
		Timeout:       2 * time.Second,
		BatchSize:     50,
		PollInterval:  10 * time.Millisecond,
		LeaseDuration: 30 * time.Second,
		BaseBackoff:   time.Millisecond,
		MaxBackoff:    time.Millisecond,
	}, testLogger())
}

func TestWebhookDispatcher_DeliversSignedEvent(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	const secret = "whsec_integration"
	receiver := newWebhookReceiver(t, http.StatusOK)
	dispatcher := newTestDispatcher(t, ts, receiver.server.URL, secret, 3)

	authResp := ts.Authorize(t, "4111111111111111", "123", 10000, "dispatcher-auth")
	require.Equal(t, http.StatusOK, authResp.StatusCode)
	authResp.Body.Close()

	processed, err := dispatcher.Drain(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, processed)

	requests := receiver.captured()
	require.Len(t, requests, 1)

	signature := requests[0].header.Get(webhook.HeaderSignature)
	require.NotEmpty(t, signature)
	assert.True(t, webhook.Verify(secret, signature, string(requests[0].body), time.Minute))

	var payload events.Event
	require.NoError(t, json.Unmarshal(requests[0].body, &payload))
	assert.Equal(t, events.EventPaymentAuthorized, payload.Type)
	assert.Equal(t, requests[0].header.Get(webhook.HeaderEventID), payload.ID.String())
	assert.Equal(t, string(models.TransactionStatusActive), payload.Data.Status)

	var stored models.PaymentEvent
	require.NoError(t, ts.Database.GORM().First(&stored, "id = ?", payload.ID).Error)
	assert.Equal(t, models.EventStatusDelivered, stored.Status)
	require.NotNil(t, stored.DeliveredAt)

	var delivery models.WebhookDelivery
	require.NoError(t, ts.Database.GORM().First(&delivery, "event_id = ?", payload.ID).Error)
	require.NotNil(t, delivery.StatusCode)
	assert.Equal(t, http.StatusOK, *delivery.StatusCode)
	assert.Equal(t, receiver.server.URL, delivery.Endpoint)
}

func TestWebhookDispatcher_RetriesThenDeadLetters(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	receiver := newWebhookReceiver(t, http.StatusInternalServerError)
	dispatcher := newTestDispatcher(t, ts, receiver.server.URL, "whsec_retry", 2)

	authResp := ts.Authorize(t, "4111111111111111", "123", 10000, "retry-auth")
	require.Equal(t, http.StatusOK, authResp.StatusCode)
	authResp.Body.Close()

	_, err := dispatcher.Drain(context.Background())
	require.NoError(t, err)

	var event models.PaymentEvent
	require.NoError(t, ts.Database.GORM().First(&event).Error)
	assert.Equal(t, models.EventStatusPending, event.Status)
	assert.Equal(t, 1, event.Attempts)
	require.NotNil(t, event.LastError)
	assert.Contains(t, *event.LastError, "500")

	time.Sleep(20 * time.Millisecond)

	_, err = dispatcher.Drain(context.Background())
	require.NoError(t, err)

	require.NoError(t, ts.Database.GORM().First(&event, "id = ?", event.ID).Error)
	assert.Equal(t, models.EventStatusDead, event.Status)
	assert.Equal(t, 2, event.Attempts)

	var deliveries int64
	require.NoError(t, ts.Database.GORM().
		Model(&models.WebhookDelivery{}).
		Where("event_id = ?", event.ID).
		Count(&deliveries).Error)
	assert.Equal(t, int64(2), deliveries)
	assert.Len(t, receiver.captured(), 2)
}

func TestWebhookDispatcher_RequeueAllowsRedelivery(t *testing.T) {
	ts := SetupTest(t)
	defer ts.Close()

	receiver := newWebhookReceiver(t, http.StatusBadGateway)
	dispatcher := newTestDispatcher(t, ts, receiver.server.URL, "whsec_requeue", 1)

	authResp := ts.Authorize(t, "4111111111111111", "123", 10000, "requeue-auth")
	require.Equal(t, http.StatusOK, authResp.StatusCode)
	authResp.Body.Close()

	_, err := dispatcher.Drain(context.Background())
	require.NoError(t, err)

	var event models.PaymentEvent
	require.NoError(t, ts.Database.GORM().First(&event).Error)
	require.Equal(t, models.EventStatusDead, event.Status)

	receiver.setStatus(http.StatusOK)

	repo := repository.NewEventRepository(ts.Database.GORM())
	require.NoError(t, repo.Requeue(context.Background(), event.ID, time.Now()))

	processed, err := dispatcher.Drain(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, processed)

	require.NoError(t, ts.Database.GORM().First(&event, "id = ?", event.ID).Error)
	assert.Equal(t, models.EventStatusDelivered, event.Status)
}
