package config

import "testing"

func TestRedirectURIAllowed(t *testing.T) {
	cases := []struct {
		name  string
		allow []string
		uri   string
		want  bool
	}{
		{"star allows any", []string{"*"}, "https://anything.example/cb", true},
		{"exact match", []string{"https://app.example/cb"}, "https://app.example/cb", true},
		{"exact mismatch", []string{"https://app.example/cb"}, "https://app.example/other", false},
		{"prefix path match", []string{"https://app.example/*"}, "https://app.example/oauth/cb", true},
		{"prefix host match (no path)", []string{"https://app.example*"}, "https://app.example/cb", true},
		{"prefix rejects sibling host", []string{"https://app.example*"}, "https://app.example.evil.com/cb", false},
		{"prefix rejects different host", []string{"https://app.example/*"}, "https://evil.example/cb", false},
		{"prefix rejects scheme change", []string{"https://app.example/*"}, "http://app.example/cb", false},
		{"empty uri", []string{"*"}, "", false},
		{"no match in empty list", []string{}, "https://app.example/cb", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := MCPConfig{AllowedRedirectURIs: c.allow}
			if got := m.RedirectURIAllowed(c.uri); got != c.want {
				t.Fatalf("RedirectURIAllowed(%q) with %v = %v, want %v", c.uri, c.allow, got, c.want)
			}
		})
	}
}
