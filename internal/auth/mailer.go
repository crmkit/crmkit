package auth

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/smtp"
	"strings"
	"time"

	"github.com/crmkit/crmkit/internal/config"
)

// Mailer sends outbound email (login codes, step-up codes, invites). Implement
// this interface and add a case to NewMailer to support a new delivery provider
// (e.g. Postmark, SendGrid). Messages carry a plain-text and a branded HTML body
// (see email.go); providers should send both.
type Mailer interface {
	Send(Email) error
}

// NewMailer selects a delivery backend from the email config's provider
// ("smtp", "resend", or "log"). Log mode prints messages to stderr instead of
// sending them, which is handy for local development.
func NewMailer(cfg config.EmailConfig) Mailer {
	switch cfg.EffectiveProvider() {
	case "smtp":
		return &SMTPMailer{cfg: cfg}
	case "resend":
		return &ResendMailer{
			from:   cfg.From,
			apiKey: cfg.ResendAPIKey,
			client: &http.Client{Timeout: 10 * time.Second},
		}
	case "ses":
		return NewSESMailer(cfg.SESRegion, cfg.SESAccessKeyID, cfg.SESSecretAccessKey, cfg.SESSessionToken, cfg.From)
	case "cloudflare":
		return &CloudflareMailer{
			accountID: cfg.CloudflareAccountID,
			apiToken:  cfg.CloudflareAPIToken,
			from:      cfg.From,
			client:    &http.Client{Timeout: 10 * time.Second},
		}
	default:
		return &LogMailer{from: cfg.From}
	}
}

// LogMailer writes messages to the process log instead of sending them.
type LogMailer struct {
	from string
}

// Send logs the email (plain-text body) to stderr.
func (m *LogMailer) Send(e Email) error {
	log.Printf("[email:log] from=%q to=%q subject=%q\n%s", m.from, e.To, e.Subject, e.Text)
	return nil
}

// SMTPMailer sends email through a plain SMTP relay.
type SMTPMailer struct {
	cfg config.EmailConfig
}

// Send delivers a multipart/alternative email (text + HTML) over SMTP with
// optional PLAIN auth.
func (m *SMTPMailer) Send(e Email) error {
	addr := fmt.Sprintf("%s:%d", m.cfg.SMTPHost, m.cfg.SMTPPort)

	var auth smtp.Auth
	if m.cfg.SMTPUser != "" {
		auth = smtp.PlainAuth("", m.cfg.SMTPUser, m.cfg.SMTPPass, m.cfg.SMTPHost)
	}

	// A random per-message boundary (not a fixed string) so it cannot be
	// predicted and embedded in a body field to break out of a MIME part.
	boundary := randomBoundary()
	msg := strings.Builder{}
	fmt.Fprintf(&msg, "From: %s\r\n", m.cfg.From)
	fmt.Fprintf(&msg, "To: %s\r\n", e.To)
	fmt.Fprintf(&msg, "Subject: %s\r\n", e.Subject)
	msg.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&msg, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)
	fmt.Fprintf(&msg, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n\r\n", boundary, e.Text)
	fmt.Fprintf(&msg, "--%s\r\nContent-Type: text/html; charset=utf-8\r\n\r\n%s\r\n\r\n", boundary, e.HTML)
	fmt.Fprintf(&msg, "--%s--\r\n", boundary)

	return smtp.SendMail(addr, auth, m.cfg.From, []string{e.To}, []byte(msg.String()))
}

// randomBoundary returns a unique multipart boundary with 128 bits of entropy.
// Unpredictability is the property that matters: a body field cannot be crafted
// to contain a boundary the sender will choose. On the effectively unreachable
// crypto/rand failure path it falls back to a static string - the message still
// sends, it just loses the unpredictability guarantee.
func randomBoundary() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "crmkit_alt_b0undary"
	}
	return "crmkit_" + hex.EncodeToString(buf)
}

// ResendMailer sends email through the Resend HTTP API (https://resend.com).
// It needs no SDK - a single JSON POST with a bearer key.
type ResendMailer struct {
	from   string
	apiKey string
	client *http.Client
}

// Send delivers an email via the Resend API.
func (m *ResendMailer) Send(e Email) error {
	payload, err := json.Marshal(map[string]any{
		"from":    m.from,
		"to":      []string{e.To},
		"subject": e.Subject,
		"text":    e.Text,
		"html":    e.HTML,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, "https://api.resend.com/emails", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("resend request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("resend returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// CloudflareMailer sends email through the Cloudflare Email Service REST API.
// Like Resend, it needs no SDK - a single JSON POST with a bearer API token to
// the account's send endpoint. The From domain must be verified for sending in
// the Cloudflare dashboard (DKIM/SPF records, auto-managed when DNS is on
// Cloudflare).
type CloudflareMailer struct {
	accountID string
	apiToken  string
	from      string
	client    *http.Client
}

// Send delivers a text + HTML email via the Cloudflare Email Service API.
func (m *CloudflareMailer) Send(e Email) error {
	payload, err := json.Marshal(map[string]any{
		"from":    cloudflareAddress(m.from),
		"to":      e.To,
		"subject": e.Subject,
		"text":    e.Text,
		"html":    e.HTML,
	})
	if err != nil {
		return err
	}

	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/accounts/%s/email/sending/send", m.accountID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare email request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("cloudflare email returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// cloudflareAddress renders a From value for the Cloudflare Email API. Cloudflare
// wants a bare email string or a {address, name} object - it rejects the RFC 5322
// "Name <addr>" display-name string that other providers accept. So split that
// form into the object; pass a bare address through as a string.
func cloudflareAddress(from string) any {
	from = strings.TrimSpace(from)
	if i := strings.LastIndexByte(from, '<'); i >= 0 && strings.HasSuffix(from, ">") {
		addr := strings.TrimSpace(from[i+1 : len(from)-1])
		name := strings.Trim(strings.TrimSpace(from[:i]), `"`)
		if name != "" {
			return map[string]any{"address": addr, "name": name}
		}
		return addr
	}
	return from
}
