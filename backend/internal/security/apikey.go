// Package security provides lightweight API key helpers.
package security

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

// HashToken returns the SHA-256 hex digest of a raw API token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ConstantTimeEquals reports whether a and b are equal in constant time.
func ConstantTimeEquals(a, b string) bool {
	ab := []byte(a)
	bb := []byte(b)
	if len(ab) != len(bb) {
		return false
	}
	return subtle.ConstantTimeCompare(ab, bb) == 1
}
