package ratelimit

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// fakeClock advances only when the test says so.
type fakeClock struct{ at time.Time }

func (c *fakeClock) now() time.Time { return c.at }

func (c *fakeClock) advance(d time.Duration) { c.at = c.at.Add(d) }

func TestLimiter_AllowsBurstUpToLimit(t *testing.T) {
	clock := &fakeClock{at: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	limiter := newLimiter(clock.now)
	key := uuid.New()

	for i := 0; i < 5; i++ {
		allowed, retryAfter := limiter.Allow(key, 5)
		assert.True(t, allowed, "request %d should fit in the burst", i+1)
		assert.Zero(t, retryAfter)
	}

	allowed, retryAfter := limiter.Allow(key, 5)
	assert.False(t, allowed, "the bucket should be empty")
	assert.Positive(t, retryAfter)
}

func TestLimiter_RefillsContinuously(t *testing.T) {
	clock := &fakeClock{at: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	limiter := newLimiter(clock.now)
	key := uuid.New()

	// A limit of 60 per minute refills one token per second. Drain the bucket,
	// then confirm a single token returns after one second.
	for i := 0; i < 60; i++ {
		allowed, _ := limiter.Allow(key, 60)
		assert.True(t, allowed)
	}
	allowed, _ := limiter.Allow(key, 60)
	assert.False(t, allowed)

	clock.advance(time.Second)
	allowed, _ = limiter.Allow(key, 60)
	assert.True(t, allowed, "one second should refill exactly one token")

	allowed, _ = limiter.Allow(key, 60)
	assert.False(t, allowed, "the refilled token was just spent")
}

func TestLimiter_RetryAfterIsAtLeastOneSecond(t *testing.T) {
	clock := &fakeClock{at: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	limiter := newLimiter(clock.now)
	key := uuid.New()

	for i := 0; i < 120; i++ {
		limiter.Allow(key, 120)
	}

	allowed, retryAfter := limiter.Allow(key, 120)
	assert.False(t, allowed)
	assert.GreaterOrEqual(t, retryAfter, time.Second)
	assert.LessOrEqual(t, retryAfter, time.Second, "120/min refills one token in half a second, rounded up")
}

func TestLimiter_KeysAreIndependent(t *testing.T) {
	clock := &fakeClock{at: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	limiter := newLimiter(clock.now)

	first, second := uuid.New(), uuid.New()

	allowed, _ := limiter.Allow(first, 1)
	assert.True(t, allowed)
	allowed, _ = limiter.Allow(first, 1)
	assert.False(t, allowed)

	allowed, _ = limiter.Allow(second, 1)
	assert.True(t, allowed, "another credential must have its own bucket")
}

func TestLimiter_NonPositiveLimitIsUnlimited(t *testing.T) {
	clock := &fakeClock{at: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	limiter := newLimiter(clock.now)
	key := uuid.New()

	for i := 0; i < 1000; i++ {
		allowed, retryAfter := limiter.Allow(key, 0)
		assert.True(t, allowed)
		assert.Zero(t, retryAfter)
	}
	assert.Zero(t, limiter.BucketCount(), "unlimited keys should not allocate state")
}

func TestLimiter_PrunesIdleBuckets(t *testing.T) {
	clock := &fakeClock{at: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	limiter := newLimiter(clock.now)
	stale, active := uuid.New(), uuid.New()

	limiter.Allow(stale, 10)
	limiter.Allow(active, 10)
	assert.Equal(t, 2, limiter.BucketCount())

	// Advance past the idle TTL and the sweep interval, then touch only the
	// active key so a sweep runs.
	clock.advance(pruneInterval + time.Second)
	limiter.Allow(active, 10)

	assert.Equal(t, 1, limiter.BucketCount(), "idle buckets should be reclaimed")
	allowed, _ := limiter.Allow(stale, 10)
	assert.True(t, allowed, "a pruned key simply starts with a fresh bucket")
}

func TestLimiter_FullBucketDoesNotAccumulateBeyondLimit(t *testing.T) {
	clock := &fakeClock{at: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	limiter := newLimiter(clock.now)
	key := uuid.New()

	// Let a long time pass without using the key; tokens must cap at the limit.
	clock.advance(24 * time.Hour)

	for i := 0; i < 10; i++ {
		allowed, _ := limiter.Allow(key, 10)
		assert.True(t, allowed)
	}
	allowed, _ := limiter.Allow(key, 10)
	assert.False(t, allowed)
}
