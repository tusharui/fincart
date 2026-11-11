package webhook

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSignAndVerify(t *testing.T) {
	const secret = "whsec_test"
	payload := []byte(`{"id":"evt_1","type":"payment.captured"}`)
	now := time.Now()

	signature := Sign(secret, payload, now)
	assert.True(t, Verify(secret, signature, string(payload), time.Minute))

	assert.False(t, Verify("wrong-secret", signature, string(payload), time.Minute))
	assert.False(t, Verify(secret, signature, string(payload)+" ", time.Minute))
	assert.False(t, Verify(secret, "v1:not-a-timestamp:abc", string(payload), time.Minute))
}

func TestVerifyRejectsStaleSignature(t *testing.T) {
	payload := []byte(`{}`)
	signature := Sign("whsec_test", payload, time.Now().Add(-10*time.Minute))

	assert.False(t, Verify("whsec_test", signature, string(payload), time.Minute))
}

func TestDispatcherBackoff(t *testing.T) {
	d := &Dispatcher{
		baseBackoff: 5 * time.Second,
		maxBackoff:  time.Minute,
	}

	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 0, want: 5 * time.Second},
		{attempt: 1, want: 5 * time.Second},
		{attempt: 2, want: 10 * time.Second},
		{attempt: 3, want: 20 * time.Second},
		{attempt: 4, want: 40 * time.Second},
		{attempt: 5, want: time.Minute},
		{attempt: 50, want: time.Minute},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.want, d.backoff(tt.attempt), "attempt %d", tt.attempt)
	}
}
