package auth

import (
	_ "embed"
	"fmt"
	"html/template"
	"strings"
)

// Email is an outbound message carrying both a plain-text and a branded HTML
// body. Providers send both (multipart/alternative over SMTP); clients that
// can't render HTML fall back to Text.
type Email struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// emailHTML is the branded email markup, embedded from emails.html (the single
// source of truth - edit that file as normal HTML). It is an html/template with
// a shared header/footer layout and one body per message type.
//
//go:embed emails.html
var emailHTML string

var emailTmpl = template.Must(template.New("emails").Parse(emailHTML))

// emailData carries the dynamic values for the email templates. Each message
// uses the subset of fields it needs.
type emailData struct {
	Heading    string
	Code       string
	TTLMinutes int
	Action     string
	To         string
	BaseURL    string
}

// renderEmail executes the named email body template (login/escalation/invite)
// and returns the HTML. A template failure yields an empty string; the caller
// still has the plain-text body, so the message is never blank.
func renderEmail(name string, data emailData) string {
	var b strings.Builder
	if err := emailTmpl.ExecuteTemplate(&b, name, data); err != nil {
		return ""
	}
	return b.String()
}

// LoginEmail is the one-time login code message.
func LoginEmail(to, code string, ttlMinutes int) Email {
	return Email{
		To:      to,
		Subject: "Your crmkit login code",
		Text: fmt.Sprintf("Your crmkit login code is: %s\n\nIt expires in %d minutes. If you did not request this, ignore this email.",
			code, ttlMinutes),
		HTML: renderEmail("login", emailData{Heading: "Your login code", Code: code, TTLMinutes: ttlMinutes}),
	}
}

// EscalationEmail is the step-up (sensitive action) confirmation code.
func EscalationEmail(to, action, code string, ttlMinutes int) Email {
	return Email{
		To:      to,
		Subject: "crmkit security code",
		Text: fmt.Sprintf("Your authorization code to %s is: %s\n\nIt expires in %d minutes. If you did not request this, ignore this email.",
			action, code, ttlMinutes),
		HTML: renderEmail("escalation", emailData{Heading: "Security code", Action: action, Code: code, TTLMinutes: ttlMinutes}),
	}
}

// InviteEmail notifies someone they've been invited to a workspace, with the
// login instruction and base URL.
func InviteEmail(to, baseURL string) Email {
	return Email{
		To:      to,
		Subject: "You've been invited to crmkit",
		Text: fmt.Sprintf("You've been invited to a crmkit workspace.\n\nTo accept, sign in at %s using this email address (%s). You'll join automatically.",
			baseURL, to),
		HTML: renderEmail("invite", emailData{Heading: "You've been invited", To: to, BaseURL: baseURL}),
	}
}
