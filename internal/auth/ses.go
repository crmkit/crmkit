package auth

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// sesService is the SigV4 service name for Amazon SES.
const sesService = "ses"

// SESMailer sends email via the Amazon SES v2 API. Requests are signed with
// AWS Signature Version 4 using only the standard library - no AWS SDK - to
// keep the binary small and CGO-free.
type SESMailer struct {
	region       string
	accessKey    string
	secretKey    string
	sessionToken string // optional, for temporary (STS) credentials
	from         string
	client       *http.Client
	now          func() time.Time // injectable for deterministic tests
}

// NewSESMailer constructs an SES mailer for a region and credentials.
func NewSESMailer(region, accessKey, secretKey, sessionToken, from string) *SESMailer {
	return &SESMailer{
		region:       region,
		accessKey:    accessKey,
		secretKey:    secretKey,
		sessionToken: sessionToken,
		from:         from,
		client:       &http.Client{Timeout: 10 * time.Second},
		now:          time.Now,
	}
}

// Send delivers a text + HTML email through SES v2 SendEmail.
func (m *SESMailer) Send(e Email) error {
	payload, err := json.Marshal(map[string]any{
		"FromEmailAddress": m.from,
		"Destination":      map[string]any{"ToAddresses": []string{e.To}},
		"Content": map[string]any{
			"Simple": map[string]any{
				"Subject": map[string]any{"Data": e.Subject},
				"Body": map[string]any{
					"Text": map[string]any{"Data": e.Text},
					"Html": map[string]any{"Data": e.HTML},
				},
			},
		},
	})
	if err != nil {
		return err
	}

	host := "email." + m.region + ".amazonaws.com"
	endpoint := "https://" + host + "/v2/email/outbound-emails"
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	m.sign(req, payload, host)

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("ses request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("ses returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// sign adds the SigV4 Authorization (and X-Amz-*) headers to req.
func (m *SESMailer) sign(req *http.Request, payload []byte, host string) {
	now := m.now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	req.Header.Set("X-Amz-Date", amzDate)
	if m.sessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", m.sessionToken)
	}

	// Canonical headers must be sorted by lowercased name; we include the
	// security token header only when present.
	canonicalHeaders := "content-type:application/json\n" +
		"host:" + host + "\n" +
		"x-amz-date:" + amzDate + "\n"
	signedHeaders := "content-type;host;x-amz-date"
	if m.sessionToken != "" {
		canonicalHeaders += "x-amz-security-token:" + m.sessionToken + "\n"
		signedHeaders += ";x-amz-security-token"
	}

	canonicalRequest := strings.Join([]string{
		http.MethodPost,
		"/v2/email/outbound-emails",
		"", // canonical query string
		canonicalHeaders,
		signedHeaders,
		hexSHA256(payload),
	}, "\n")

	scope := dateStamp + "/" + m.region + "/" + sesService + "/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")

	signingKey := deriveSigningKey(m.secretKey, dateStamp, m.region, sesService)
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))

	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 "+
		"Credential="+m.accessKey+"/"+scope+", "+
		"SignedHeaders="+signedHeaders+", "+
		"Signature="+signature)
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func hexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// deriveSigningKey computes the SigV4 signing key for a date/region/service.
func deriveSigningKey(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}
