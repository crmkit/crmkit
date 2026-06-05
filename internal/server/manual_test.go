package server

import (
	"slices"
	"strings"
	"testing"
)

func TestManualFrontmatterParses(t *testing.T) {
	m := Frontmatter()
	if m.Name != "crmkit" {
		t.Fatalf("expected name crmkit, got %q", m.Name)
	}
	if m.Description == "" {
		t.Fatal("expected a description")
	}
	if !slices.Contains(m.Capabilities, "contacts") || !slices.Contains(m.Capabilities, "workspaces") {
		t.Fatalf("unexpected capabilities: %v", m.Capabilities)
	}
	if !slices.Contains(m.ContentTypes, "application/json") {
		t.Fatalf("unexpected content types: %v", m.ContentTypes)
	}
	if m.Authentication.Type != "bearer" || m.Authentication.Scheme != "email-otp" {
		t.Fatalf("unexpected auth: %+v", m.Authentication)
	}
}

func TestManualBeginsWithFrontmatter(t *testing.T) {
	out := Manual("https://crm.example")
	if !strings.HasPrefix(out, "---\n") {
		t.Fatalf("manual should begin with frontmatter, got:\n%.40s", out)
	}
	// The body must still interpolate base_url.
	if !strings.Contains(out, "BASE_URL: https://crm.example") {
		t.Fatal("manual body should interpolate base_url")
	}
}

func TestSplitFrontmatter(t *testing.T) {
	front, body := splitFrontmatter("---\nname: x\n---\n\nhello body")
	if strings.TrimSpace(front) != "name: x" {
		t.Fatalf("front: %q", front)
	}
	if strings.TrimSpace(body) != "hello body" {
		t.Fatalf("body: %q", body)
	}
	// No frontmatter -> whole thing is body.
	if f, b := splitFrontmatter("no fm here"); f != "" || b != "no fm here" {
		t.Fatalf("expected passthrough, got front=%q body=%q", f, b)
	}
}
