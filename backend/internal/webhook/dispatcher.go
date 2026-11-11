package webhook

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/tusharui/payment-gateway/backend/internal/config"
	"github.com/tusharui/payment-gateway/backend/internal/models"
	"github.com/tusharui/payment-gateway/backend/internal/repository"
	"gorm.io/gorm"
)

// Header names sent with every webhook delivery.
const (
	HeaderEventID   = "X-Webhook-Id"
	HeaderEventType = "X-Webhook-Event"
	HeaderTimestamp = "X-Webhook-Timestamp"
	HeaderSignature = "X-Webhook-Signature"
	HeaderAttempt   = "X-Webhook-Attempt"
	HeaderDelivery  = "X-Webhook-Delivery"
)

const (
	// userAgent identifies this gateway to receivers.
	userAgent = "ficmart-webhook/1.0"

	// maxResponseBody bounds how much of a receiver response is read so a slow
	// or hostile endpoint cannot exhaust memory.
	maxResponseBody = 64 << 10

	// maxStoredError bounds the error text persisted in the outbox.
	maxStoredError = 1000
)

// Dispatcher delivers pending outbox events to configured webhook endpoints.
// It is safe to run multiple instances: events are claimed with row locks and
// a lease, so an event is not delivered concurrently while a worker is active.
type Dispatcher struct {
	repo        repository.EventRepository
	endpoints   []string
	secret      string
	client      *http.Client
	logger      *slog.Logger
	maxAttempts int
	batchSize   int
	pollEvery   time.Duration
	lease       time.Duration
	baseBackoff time.Duration
	maxBackoff  time.Duration
}

// NewDispatcher builds a dispatcher from webhook configuration.
func NewDispatcher(database *gorm.DB, cfg config.WebhookConfig, logger *slog.Logger) *Dispatcher {
	return &Dispatcher{
		repo:      repository.NewEventRepository(database),
		endpoints: cfg.Endpoints,
		secret:    cfg.Secret,
		client: &http.Client{
			Timeout: cfg.Timeout,
			// A redirect could leak a signed payload to an unintended host.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		logger:      logger,
		maxAttempts: cfg.MaxAttempts,
		batchSize:   cfg.BatchSize,
		pollEvery:   cfg.PollInterval,
		lease:       cfg.LeaseDuration,
		baseBackoff: cfg.BaseBackoff,
		maxBackoff:  cfg.MaxBackoff,
	}
}

// Run polls the outbox until the context is cancelled.
func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(d.pollEvery)
	defer ticker.Stop()

	d.logger.Info("webhook dispatcher started",
		"endpoints", len(d.endpoints),
		"poll_interval", d.pollEvery,
		"batch_size", d.batchSize,
	)

	for {
		select {
		case <-ctx.Done():
			d.logger.Info("webhook dispatcher stopped")
			return
		case <-ticker.C:
			processed, err := d.Drain(ctx)
			if err != nil {
				d.logger.Warn("webhook dispatch completed with errors", "error", err, "processed", processed)
			} else if processed > 0 {
				d.logger.Info("delivered payment events", "count", processed)
			}
		}
	}
}

// Drain claims and processes up to one batch of due events. It is exported so
// tests and operational tooling can run delivery synchronously.
func (d *Dispatcher) Drain(ctx context.Context) (int, error) {
	events, err := d.repo.ClaimBatch(ctx, time.Now(), d.batchSize, d.lease)
	if err != nil {
		return 0, err
	}

	var errs []error
	for i := range events {
		if err := d.processEvent(ctx, &events[i]); err != nil {
			errs = append(errs, err)
		}
	}

	return len(events), errors.Join(errs...)
}

// processEvent delivers one event to every endpoint and updates its outbox
// state. Delivery is at-least-once: a partial success is retried for all
// endpoints, so receivers must deduplicate on the event ID.
func (d *Dispatcher) processEvent(ctx context.Context, event *models.PaymentEvent) error {
	payload := []byte(event.Payload)
	now := time.Now()
	attempt := event.Attempts + 1

	var errs []error
	for _, endpoint := range d.endpoints {
		if err := d.deliver(ctx, endpoint, event, payload, attempt, now); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", endpoint, err))
		}
	}

	if len(errs) == 0 {
		if err := d.repo.MarkDelivered(ctx, event.ID, now); err != nil {
			return fmt.Errorf("event %s delivered but not acknowledged: %w", event.ID, err)
		}
		return nil
	}

	lastError := truncate(errors.Join(errs...).Error(), maxStoredError)
	if attempt >= d.maxAttempts {
		if err := d.repo.MarkDead(ctx, event.ID, attempt, lastError); err != nil {
			return fmt.Errorf("event %s dead-lettered but not acknowledged: %w", event.ID, err)
		}
		d.logger.Error("payment event dead-lettered",
			"event_id", event.ID,
			"event_type", event.Type,
			"attempts", attempt,
			"error", lastError,
		)
		return nil
	}

	nextAttempt := now.Add(d.backoff(attempt))
	if err := d.repo.MarkRetry(ctx, event.ID, attempt, nextAttempt, lastError); err != nil {
		return fmt.Errorf("event %s retry not acknowledged: %w", event.ID, err)
	}
	d.logger.Warn("payment event delivery failed; retry scheduled",
		"event_id", event.ID,
		"event_type", event.Type,
		"attempt", attempt,
		"next_attempt_at", nextAttempt,
		"error", lastError,
	)

	return nil
}

// deliver performs one signed HTTP request and records the attempt.
func (d *Dispatcher) deliver(
	ctx context.Context,
	endpoint string,
	event *models.PaymentEvent,
	payload []byte,
	attempt int,
	now time.Time,
) error {
	timestamp := now.UTC()
	signature := Sign(d.secret, payload, timestamp)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		d.recordDelivery(ctx, event.ID, endpoint, attempt, nil, err, 0)
		return fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set(HeaderEventID, event.ID.String())
	req.Header.Set(HeaderEventType, event.Type)
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(timestamp.Unix(), 10))
	req.Header.Set(HeaderSignature, signature)
	req.Header.Set(HeaderAttempt, strconv.Itoa(attempt))
	req.Header.Set(HeaderDelivery, uuid.NewString())

	start := time.Now()
	resp, err := d.client.Do(req)
	duration := time.Since(start)
	if err != nil {
		d.recordDelivery(ctx, event.ID, endpoint, attempt, nil, err, duration)
		return err
	}
	defer func() {
		// Drain a bounded amount so the connection can be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBody))
		_ = resp.Body.Close()
	}()

	status := resp.StatusCode
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		deliveryErr := fmt.Errorf("endpoint returned status %d", status)
		d.recordDelivery(ctx, event.ID, endpoint, attempt, &status, deliveryErr, duration)
		return deliveryErr
	}

	d.recordDelivery(ctx, event.ID, endpoint, attempt, &status, nil, duration)
	return nil
}

// recordDelivery logs one attempt. Logging failures are not fatal: the outbox
// state still drives correctness, and the delivery log is an audit aid.
func (d *Dispatcher) recordDelivery(
	ctx context.Context,
	eventID uuid.UUID,
	endpoint string,
	attempt int,
	status *int,
	deliveryErr error,
	duration time.Duration,
) {
	var errText *string
	if deliveryErr != nil {
		text := truncate(deliveryErr.Error(), maxStoredError)
		errText = &text
	}

	err := d.repo.RecordDelivery(ctx, &models.WebhookDelivery{
		EventID:    eventID,
		Endpoint:   endpoint,
		Attempt:    attempt,
		StatusCode: status,
		Error:      errText,
		DurationMS: duration.Milliseconds(),
		CreatedAt:  time.Now(),
	})
	if err != nil {
		d.logger.Warn("failed to record webhook delivery",
			"event_id", eventID,
			"endpoint", endpoint,
			"error", err,
		)
	}
}

// backoff returns the delay before the next attempt using capped exponential
// growth: base, 2*base, 4*base, ... up to max.
func (d *Dispatcher) backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}

	delay := d.baseBackoff
	for i := 1; i < attempt; i++ {
		if delay >= d.maxBackoff {
			return d.maxBackoff
		}
		delay *= 2
	}
	if delay > d.maxBackoff {
		return d.maxBackoff
	}

	return delay
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
