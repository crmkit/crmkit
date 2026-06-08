package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

// loginTicketTTL bounds how long a verified-login ticket is valid - long enough
// to pick a workspace, short enough to limit replay.
const loginTicketTTL = 5 * time.Minute

// NewLoginTicket issues a signed, short-lived proof that an email completed the
// OTP login. It carries the user id (and email, for display) so a follow-up step
// - choosing a workspace on the OAuth authorize page - can establish the grant
// without re-entering the now-consumed one-time code. It is keyed by the server
// secret, so it cannot be forged.
func NewLoginTicket(secret, userID, email string) string {
	exp := time.Now().Add(loginTicketTTL).Unix()
	payload := userID + "|" + email + "|" + strconv.FormatInt(exp, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + ticketMAC(secret, payload)
}

// VerifyLoginTicket validates a ticket from NewLoginTicket, returning the bound
// user id and email when the signature checks out and the ticket has not
// expired. The MAC comparison is constant-time.
func VerifyLoginTicket(secret, ticket string) (userID, email string, ok bool) {
	dot := strings.LastIndexByte(ticket, '.')
	if dot < 0 {
		return "", "", false
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(ticket[:dot])
	if err != nil {
		return "", "", false
	}
	payload := string(payloadBytes)
	if !hmac.Equal([]byte(ticket[dot+1:]), []byte(ticketMAC(secret, payload))) {
		return "", "", false
	}
	parts := strings.Split(payload, "|")
	if len(parts) != 3 {
		return "", "", false
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func ticketMAC(secret, payload string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte("loginticket|" + payload))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
