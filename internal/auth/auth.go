// Package auth handles crmkit's credential primitives: one-time login codes,
// opaque API tokens, and their hashing. Secrets are only ever stored as
// SHA-256 hashes; plaintext exists just long enough to email or return it.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

// tokenEncoding is a lowercase base32 alphabet (no padding) for token bodies.
var tokenEncoding = base32.NewEncoding("abcdefghijkmnpqrstuvwxyz23456789").WithPadding(base32.NoPadding)

// GenerateCode returns a 6-digit numeric login code as a zero-padded string.
func GenerateCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		// Extremely unlikely; degrade to a fixed-width fallback.
		return "000000"
	}
	return fmt.Sprintf("%06d", n.Int64())
}

// hmacHex computes HMAC-SHA256(secret, msg) as hex. Keying with a server-side
// secret means a read-only database leak cannot brute-force the low-entropy
// (6-digit) codes offline - without the key an attacker cannot even compute
// candidate hashes.
func hmacHex(secret, msg string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(msg))
	return hex.EncodeToString(h.Sum(nil))
}

// HashCode hashes a login code bound to its email (so codes cannot be replayed
// across addresses), keyed by the server secret.
func HashCode(secret, email, code string) string {
	return hmacHex(secret, strings.ToLower(strings.TrimSpace(email))+":"+code)
}

// HashStepUp hashes an escalation (step-up) code bound to the acting user and
// the specific action+target (so a code for one operation cannot authorize
// another), keyed by the server secret.
func HashStepUp(secret, userID, action, target, code string) string {
	return hmacHex(secret, userID+"|"+action+"|"+target+"|"+code)
}

// GenerateSecret returns a random 256-bit hex secret, used to key the code
// hashers when no server.secret_key is configured.
func GenerateSecret() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return hex.EncodeToString(buf)
}

// GenerateToken returns a new opaque API token and its hash. The plaintext is
// prefixed "ck_" so it is recognizable in logs and pastes.
func GenerateToken() (plaintext, hash string) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failure is fatal-ish; return an empty token so callers error.
		return "", ""
	}
	plaintext = "ck_" + tokenEncoding.EncodeToString(buf)
	return plaintext, HashToken(plaintext)
}

// HashToken returns the SHA-256 hex digest of a token plaintext.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

// NormalizeEmail lowercases and trims an email for consistent storage.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
