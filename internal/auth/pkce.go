package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
)

// GenerateOAuthCode returns a new opaque OAuth authorization code and its
// SHA-256 hash. Only the hash is stored; the plaintext is handed to the client
// via the redirect and exchanged once at the token endpoint. The "cka_" prefix
// distinguishes it from an access token ("ck_") in logs.
func GenerateOAuthCode() (plaintext, hash string) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", ""
	}
	plaintext = "cka_" + tokenEncoding.EncodeToString(buf)
	return plaintext, HashToken(plaintext)
}

// GenerateRefreshToken returns a new opaque OAuth refresh token and its SHA-256
// hash. Only the hash is stored. The "ckr_" prefix distinguishes it from an
// access token ("ck_") and an authorization code ("cka_") in logs.
func GenerateRefreshToken() (plaintext, hash string) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", ""
	}
	plaintext = "ckr_" + tokenEncoding.EncodeToString(buf)
	return plaintext, HashToken(plaintext)
}

// VerifyPKCE checks a PKCE code_verifier against the stored code_challenge per
// RFC 7636. Only the S256 method is supported (an empty method defaults to
// S256); "plain" and anything else are rejected. The comparison is
// constant-time.
func VerifyPKCE(verifier, challenge, method string) bool {
	if method == "" {
		method = "S256"
	}
	if method != "S256" {
		return false
	}
	if verifier == "" || challenge == "" {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) == 1
}
