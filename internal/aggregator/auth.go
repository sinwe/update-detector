package aggregator

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

// hashToken derives a fixed-length, comparison-safe digest of an agent's
// token. Tokens themselves are never persisted or logged.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// tokensMatch compares two token hashes in constant time.
func tokensMatch(candidateHash, storedHash string) bool {
	return subtle.ConstantTimeCompare([]byte(candidateHash), []byte(storedHash)) == 1
}
