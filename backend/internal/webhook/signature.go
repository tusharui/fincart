// Package webhook provides HMAC-based webhook signature helpers.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// Sign returns a signature in the format "v1:<timestamp>:<hmacHex>".
func Sign(secret string, payload []byte, timestamp time.Time) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestamp.Unix(), 10)))
	mac.Write(payload)
	return "v1:" + strconv.FormatInt(timestamp.Unix(), 10) + ":" + hex.EncodeToString(mac.Sum(nil))
}

// Verify reports whether the signature is valid and within the tolerance.
func Verify(secret, signature, payload string, tolerance time.Duration) bool {
	parts := strings.Split(signature, ":")
	if len(parts) != 3 || parts[0] != "v1" {
		return false
	}
	ts, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte(payload))
	expected := "v1:" + parts[1] + ":" + hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) != 1 {
		return false
	}
	timestamp := time.Unix(ts, 0)
	return time.Since(timestamp) <= tolerance
}
