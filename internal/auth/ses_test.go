package auth

import (
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestDeriveSigningKey checks our SigV4 key derivation against the worked
// example published in the AWS documentation, so we know the crypto is correct.
func TestDeriveSigningKey(t *testing.T) {
	const (
		secret    = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
		dateStamp = "20150830"
		region    = "us-east-1"
		service   = "iam"
		want      = "c4afb1cc5771d871763a393e44b703571b55cc28424d1a5e86da6ed3c154a4b9"
	)
	got := hex.EncodeToString(deriveSigningKey(secret, dateStamp, region, service))
	if got != want {
		t.Fatalf("signing key mismatch:\n got %s\nwant %s", got, want)
	}
}

// TestSESSignDeterministic verifies the produced Authorization header has the
// right structure and is stable for a fixed time + inputs.
func TestSESSignDeterministic(t *testing.T) {
	fixed := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	m := NewSESMailer("us-east-1", "AKIAEXAMPLE", "secretkeyexample", "", "crmkit <no-reply@crmkit.ai>")
	m.now = func() time.Time { return fixed }

	host := "email.us-east-1.amazonaws.com"
	req, _ := http.NewRequest(http.MethodPost, "https://"+host+"/v2/email/outbound-emails", nil)
	req.Header.Set("Content-Type", "application/json")
	m.sign(req, []byte(`{"hello":"world"}`), host)

	auth := req.Header.Get("Authorization")
	for _, want := range []string{
		"AWS4-HMAC-SHA256 ",
		"Credential=AKIAEXAMPLE/20260604/us-east-1/ses/aws4_request",
		"SignedHeaders=content-type;host;x-amz-date",
		"Signature=",
	} {
		if !strings.Contains(auth, want) {
			t.Fatalf("authorization header missing %q:\n%s", want, auth)
		}
	}
	if req.Header.Get("X-Amz-Date") != "20260604T120000Z" {
		t.Fatalf("unexpected x-amz-date: %q", req.Header.Get("X-Amz-Date"))
	}

	// Signing again with identical inputs must yield the identical signature.
	req2, _ := http.NewRequest(http.MethodPost, "https://"+host+"/v2/email/outbound-emails", nil)
	req2.Header.Set("Content-Type", "application/json")
	m.sign(req2, []byte(`{"hello":"world"}`), host)
	if req2.Header.Get("Authorization") != auth {
		t.Fatal("signature should be deterministic for identical inputs")
	}
}

// TestSESSignIncludesSessionToken ensures temporary credentials add the token
// to both the headers and the signed-headers list.
func TestSESSignIncludesSessionToken(t *testing.T) {
	m := NewSESMailer("eu-west-1", "AKIA", "secret", "session-token-xyz", "from@x.com")
	host := "email.eu-west-1.amazonaws.com"
	req, _ := http.NewRequest(http.MethodPost, "https://"+host+"/v2/email/outbound-emails", nil)
	req.Header.Set("Content-Type", "application/json")
	m.sign(req, []byte(`{}`), host)

	if req.Header.Get("X-Amz-Security-Token") != "session-token-xyz" {
		t.Fatal("expected security token header")
	}
	if !strings.Contains(req.Header.Get("Authorization"), "x-amz-security-token") {
		t.Fatal("expected security token in signed headers")
	}
}
