// Package security provides lightweight API key helpers.
package security

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
)

// APIKeyPrefix marks generated credentials so they are recognizable in logs and
// secret scanners.
const APIKeyPrefix = "bk_"

// apiKeyRandomBytes is the entropy of the secret portion of a generated key.
const apiKeyRandomBytes = 32

// GeneratedAPIKey holds a newly minted credential and the non-secret display
// fragments that are safe to persist.
type GeneratedAPIKey struct {
	Raw    string
	Prefix string
	Last4  string
}

// GenerateAPIKey creates a cryptographically random API key. The raw value is
// returned to the caller once; only its SHA-256 hash should be stored.
func GenerateAPIKey() (GeneratedAPIKey, error) {
	random := make([]byte, apiKeyRandomBytes)
	if _, err := rand.Read(random); err != nil {
		return GeneratedAPIKey{}, fmt.Errorf("failed to generate api key: %w", err)
	}

	raw := APIKeyPrefix + hex.EncodeToString(random)

	return GeneratedAPIKey{
		Raw:    raw,
		Prefix: raw[:11],
		Last4:  raw[len(raw)-4:],
	}, nil
}

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
