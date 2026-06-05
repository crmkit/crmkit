package auth

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCloudflareMailerSend(t *testing.T) {
	var got *http.Request
	var body []byte
	m := &CloudflareMailer{
		accountID: "acct123",
		apiToken:  "tok_secret",
		from:      "crmkit <no-reply@crmkit.ai>",
		client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			got = r
			body, _ = io.ReadAll(r.Body)
			return &http.Response{StatusCode: 202, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
		})},
	}

	if err := m.Send(Email{To: "user@example.com", Subject: "Your code", Text: "123456", HTML: "<b>123456</b>"}); err != nil {
		t.Fatalf("send: %v", err)
	}

	if got.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", got.Method)
	}
	wantURL := "https://api.cloudflare.com/client/v4/accounts/acct123/email/sending/send"
	if got.URL.String() != wantURL {
		t.Errorf("url = %s, want %s", got.URL.String(), wantURL)
	}
	if h := got.Header.Get("Authorization"); h != "Bearer tok_secret" {
		t.Errorf("authorization = %q", h)
	}
	if ct := got.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("body not json: %v", err)
	}
	// A "Name <addr>" from must be sent as Cloudflare's {address, name} object,
	// not the display-name string (which Cloudflare rejects).
	from, ok := payload["from"].(map[string]any)
	if !ok || from["address"] != "no-reply@crmkit.ai" || from["name"] != "crmkit" {
		t.Errorf("from = %v, want {address:no-reply@crmkit.ai, name:crmkit}", payload["from"])
	}
	if payload["to"] != "user@example.com" || payload["subject"] != "Your code" ||
		payload["text"] != "123456" || payload["html"] != "<b>123456</b>" {
		t.Errorf("payload = %v", payload)
	}
}

func TestCloudflareAddress(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{"no-reply@crmkit.ai", "no-reply@crmkit.ai"},
		{"crmkit <no-reply@crmkit.ai>", map[string]any{"address": "no-reply@crmkit.ai", "name": "crmkit"}},
		{`"crmkit team" <no-reply@crmkit.ai>`, map[string]any{"address": "no-reply@crmkit.ai", "name": "crmkit team"}},
		{"<no-reply@crmkit.ai>", "no-reply@crmkit.ai"},
	}
	for _, c := range cases {
		got := cloudflareAddress(c.in)
		if gm, ok := c.want.(map[string]any); ok {
			am, ok := got.(map[string]any)
			if !ok || am["address"] != gm["address"] || am["name"] != gm["name"] {
				t.Errorf("cloudflareAddress(%q) = %v, want %v", c.in, got, c.want)
			}
		} else if got != c.want {
			t.Errorf("cloudflareAddress(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestCloudflareMailerErrorStatus(t *testing.T) {
	m := &CloudflareMailer{
		accountID: "a", apiToken: "t", from: "f@x.com",
		client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(`{"errors":["forbidden"]}`)), Header: make(http.Header)}, nil
		})},
	}
	if err := m.Send(Email{To: "u@x.com", Subject: "s", Text: "b", HTML: "<b>b</b>"}); err == nil {
		t.Fatal("expected an error on a 403 response")
	}
}
