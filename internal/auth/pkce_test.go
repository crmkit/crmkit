package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

func TestVerifyPKCE(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	if !VerifyPKCE(verifier, challenge, "S256") {
		t.Fatal("valid S256 verifier should pass")
	}
	if !VerifyPKCE(verifier, challenge, "") {
		t.Fatal("empty method should default to S256 and pass")
	}
	if VerifyPKCE("wrong-verifier", challenge, "S256") {
		t.Fatal("mismatched verifier should fail")
	}
	if VerifyPKCE(verifier, challenge, "plain") {
		t.Fatal("plain method must be rejected")
	}
	if VerifyPKCE("", challenge, "S256") || VerifyPKCE(verifier, "", "S256") {
		t.Fatal("empty verifier/challenge should fail")
	}
}

func TestGenerateOAuthCodeUnique(t *testing.T) {
	p1, h1 := GenerateOAuthCode()
	p2, h2 := GenerateOAuthCode()
	if p1 == "" || p2 == "" {
		t.Fatal("empty code generated")
	}
	if p1 == p2 || h1 == h2 {
		t.Fatal("codes should be unique")
	}
	if h1 != HashToken(p1) {
		t.Fatal("returned hash must match HashToken(plaintext)")
	}
	if got := p1[:4]; got != "cka_" {
		t.Fatalf("expected cka_ prefix, got %q", got)
	}
}
