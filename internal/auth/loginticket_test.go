package auth

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLoginTicketRoundTrip(t *testing.T) {
	tk := NewLoginTicket("secret", "user-1", "u@x.com")
	userID, email, ok := VerifyLoginTicket("secret", tk)
	if !ok {
		t.Fatal("a freshly issued ticket must verify")
	}
	if userID != "user-1" || email != "u@x.com" {
		t.Fatalf("round-trip lost data: got (%q, %q)", userID, email)
	}
}

func TestLoginTicketWrongSecret(t *testing.T) {
	tk := NewLoginTicket("secret", "user-1", "u@x.com")
	if _, _, ok := VerifyLoginTicket("other-secret", tk); ok {
		t.Fatal("a ticket must not verify under a different secret (forgeable otherwise)")
	}
}

func TestLoginTicketTampered(t *testing.T) {
	tk := NewLoginTicket("secret", "user-1", "u@x.com")
	dot := strings.LastIndexByte(tk, '.')

	// Re-encode a payload claiming a different user, but keep the original MAC.
	forgedPayload := base64.RawURLEncoding.EncodeToString([]byte("admin|u@x.com|" +
		strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10)))
	if _, _, ok := VerifyLoginTicket("secret", forgedPayload+tk[dot:]); ok {
		t.Fatal("a tampered payload must fail the MAC check")
	}

	// Flip a byte in the signature.
	bad := []byte(tk)
	bad[len(bad)-1] ^= 0x01
	if _, _, ok := VerifyLoginTicket("secret", string(bad)); ok {
		t.Fatal("a corrupted MAC must not verify")
	}
}

func TestLoginTicketExpired(t *testing.T) {
	// Forge a validly-signed ticket whose expiry is in the past.
	past := time.Now().Add(-time.Minute).Unix()
	payload := "user-1|u@x.com|" + strconv.FormatInt(past, 10)
	tk := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + ticketMAC("secret", payload)

	if _, _, ok := VerifyLoginTicket("secret", tk); ok {
		t.Fatal("an expired ticket must not verify even with a valid signature")
	}
}

func TestLoginTicketMalformed(t *testing.T) {
	cases := map[string]string{
		"empty":           "",
		"no separator":    "abc",
		"bad base64":      "!!!." + ticketMAC("secret", "x"),
		"too few fields":  base64.RawURLEncoding.EncodeToString([]byte("only-one")) + "." + ticketMAC("secret", "only-one"),
		"non-numeric exp": mustForge("secret", "user-1|u@x.com|notanumber"),
	}
	for name, tk := range cases {
		if _, _, ok := VerifyLoginTicket("secret", tk); ok {
			t.Fatalf("%s: malformed ticket must not verify", name)
		}
	}
}

// mustForge builds a validly-signed ticket for an arbitrary payload, used to
// exercise the post-MAC parsing checks.
func mustForge(secret, payload string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + ticketMAC(secret, payload)
}
