// Package ratelimit provides an in-process token bucket limiter keyed by API
// credential.
//
// State lives in memory, so limits are enforced per process. A horizontally
// scaled deployment therefore allows each replica its own bucket; this is the
// usual trade-off for a mock bank and avoids a Redis dependency. The limiter is
// safe for concurrent use.
package ratelimit

import (
	"math"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	// pruneInterval bounds how often the map is swept for idle buckets.
	pruneInterval = 5 * time.Minute
	// idleTTL is how long a bucket may go unused before it is discarded. A
	// minute's worth of refill is enough to restore any bucket to full, so
	// dropping an idle one never grants extra capacity.
	idleTTL = 2 * time.Minute
)

// bucket tracks one credential's remaining tokens.
type bucket struct {
	tokens     float64
	lastRefill time.Time
	lastSeen   time.Time
}

// Limiter enforces per-key token buckets.
type Limiter struct {
	mu        sync.Mutex
	buckets   map[uuid.UUID]*bucket
	now       func() time.Time
	lastPrune time.Time
}

// NewLimiter creates a limiter backed by the wall clock.
func NewLimiter() *Limiter {
	return newLimiter(time.Now)
}

func newLimiter(now func() time.Time) *Limiter {
	return &Limiter{
		buckets:   make(map[uuid.UUID]*bucket),
		now:       now,
		lastPrune: now(),
	}
}

// Allow consumes one token from the key's bucket. A non-positive limit means
// unlimited. When the bucket is empty it returns false and how long the caller
// should wait before retrying.
//
// A bucket starts full, so a key may spend its whole per-minute allowance in a
// burst and then refills continuously at limit/60 tokens per second.
func (l *Limiter) Allow(key uuid.UUID, limit int) (bool, time.Duration) {
	if limit <= 0 {
		return true, 0
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.pruneLocked(now)

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(limit), lastRefill: now}
		l.buckets[key] = b
	}

	ratePerSecond := float64(limit) / 60
	if elapsed := now.Sub(b.lastRefill).Seconds(); elapsed > 0 {
		b.tokens = math.Min(float64(limit), b.tokens+elapsed*ratePerSecond)
	}
	b.lastRefill = now
	b.lastSeen = now

	if b.tokens >= 1 {
		b.tokens--

		return true, 0
	}

	wait := (1 - b.tokens) / ratePerSecond
	if wait < 1 {
		wait = 1
	}

	return false, time.Duration(math.Ceil(wait)) * time.Second
}

// BucketCount reports how many credentials currently have limiter state. It
// exists for tests and operational assertions.
func (l *Limiter) BucketCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return len(l.buckets)
}

// pruneLocked drops buckets that have been idle for longer than idleTTL. It runs
// at most once per pruneInterval so the sweep never dominates request latency.
func (l *Limiter) pruneLocked(now time.Time) {
	if now.Sub(l.lastPrune) < pruneInterval {
		return
	}

	for key, b := range l.buckets {
		if now.Sub(b.lastSeen) > idleTTL {
			delete(l.buckets, key)
		}
	}
	l.lastPrune = now
}
