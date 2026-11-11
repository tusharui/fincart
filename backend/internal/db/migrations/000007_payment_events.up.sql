-- Transactional outbox for payment domain events.
--
-- Events are written in the same database transaction that moves money so a
-- committed lifecycle transition always has a matching event. Background
-- workers claim pending rows, deliver them to webhook endpoints, and record
-- every attempt. A row is only marked DELIVERED after every endpoint accepts
-- it; after the configured number of attempts it is moved to DEAD for replay.

CREATE TABLE payment_events (
    id UUID PRIMARY KEY,
    type VARCHAR(64) NOT NULL,
    aggregate_type VARCHAR(32) NOT NULL DEFAULT 'payment',
    aggregate_id UUID NOT NULL,
    payload JSONB NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'PENDING',
    attempts INT NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    delivered_at TIMESTAMPTZ,
    CONSTRAINT payment_events_status_valid
        CHECK (status IN ('PENDING', 'DELIVERED', 'DEAD')),
    CONSTRAINT payment_events_attempts_nonnegative
        CHECK (attempts >= 0)
);

-- Partial index keeps the dispatcher's claim query cheap as the outbox grows.
CREATE INDEX idx_payment_events_dispatch
    ON payment_events (next_attempt_at)
    WHERE status = 'PENDING';

CREATE INDEX idx_payment_events_aggregate
    ON payment_events (aggregate_id, created_at);

CREATE INDEX idx_payment_events_type
    ON payment_events (type, created_at);

CREATE TABLE webhook_deliveries (
    id UUID PRIMARY KEY,
    event_id UUID NOT NULL REFERENCES payment_events(id) ON DELETE CASCADE,
    endpoint TEXT NOT NULL,
    attempt INT NOT NULL,
    status_code INT,
    error TEXT,
    duration_ms BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT webhook_deliveries_attempt_positive
        CHECK (attempt > 0)
);

CREATE INDEX idx_webhook_deliveries_event
    ON webhook_deliveries (event_id, created_at);
