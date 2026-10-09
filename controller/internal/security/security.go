// Package security generates and verifies enrollment tokens and device credentials.
package security

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
)

// GenerateToken returns a cryptographically random, URL-safe token.
func GenerateToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failing is unrecoverable
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// HashSecret is plain SHA-256, not bcrypt/argon2, deliberately: every secret
// hashed here is GenerateToken output (256 bits of entropy, never a
// human-chosen password), so a slow KDF adds no security margin while costing
// real CPU per MQTT auth check. A 1,000-device load test
// (docs/load-test-report.md) showed bcrypt dominating controller CPU.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// VerifySecret compares in constant time to avoid a timing side-channel.
func VerifySecret(secret, secretHash string) bool {
	return subtle.ConstantTimeCompare([]byte(HashSecret(secret)), []byte(secretHash)) == 1
}
