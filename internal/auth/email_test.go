package auth

import (
	"strings"
	"testing"
)

func TestEmailRendering(t *testing.T) {
	login := LoginEmail("user@example.com", "123456", 10)
	if !strings.Contains(login.HTML, "123456") ||
		!strings.Contains(login.HTML, "Your login code") ||
		!strings.Contains(login.HTML, "https://crmkit.ai/icon-light.png") ||
		!strings.Contains(login.HTML, "Expires in 10 minutes") {
		t.Fatalf("login HTML missing expected content:\n%s", login.HTML)
	}
	if !strings.Contains(login.Text, "123456") {
		t.Fatalf("login text missing code: %s", login.Text)
	}

	esc := EscalationEmail("user@example.com", "promote a member to admin", "654321", 10)
	if !strings.Contains(esc.HTML, "654321") ||
		!strings.Contains(esc.HTML, "<strong>promote a member to admin</strong>") {
		t.Fatalf("escalation HTML missing expected content:\n%s", esc.HTML)
	}

	inv := InviteEmail("invitee@example.com", "https://api.crmkit.ai")
	if !strings.Contains(inv.HTML, "invitee@example.com") ||
		!strings.Contains(inv.HTML, `href="https://api.crmkit.ai"`) {
		t.Fatalf("invite HTML missing expected content:\n%s", inv.HTML)
	}
}

func TestValidEmail(t *testing.T) {
	valid := []string{"a@b.com", "jane.doe@acme.co", "a+tag@x.io", "user@sub.example.com"}
	for _, e := range valid {
		if !ValidEmail(e) {
			t.Errorf("ValidEmail(%q) = false, want true", e)
		}
	}

	invalid := map[string]string{
		"empty":               "",
		"whitespace":          "   ",
		"no @":                "noatsign",
		"display-name form":   "Jane <jane@acme.com>",
		"CRLF header inject":  "a@b.com\r\nBcc: evil@x.com",
		"bare LF":             "a@b.com\nX-Injected: 1",
		"MIME boundary break": "a@b.com\r\n--crmkit_boundary",
		"tab control char":    "a\t@b.com",
	}
	for name, e := range invalid {
		if ValidEmail(e) {
			t.Errorf("%s: ValidEmail(%q) = true, want false", name, e)
		}
	}
}
