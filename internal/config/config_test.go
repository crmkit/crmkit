package config

import (
	"path/filepath"
	"testing"
)

func TestEnvOverride(t *testing.T) {
	// Point the default config path at an absent file so Load uses env+defaults.
	t.Setenv("CRMKIT_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
	t.Setenv("CRMKIT_SERVER_LISTEN_ADDR", ":9999")
	t.Setenv("CRMKIT_SERVER_LOCAL", "true")
	t.Setenv("CRMKIT_STORAGE_BACKEND", "postgres")
	t.Setenv("CRMKIT_STORAGE_DSN", "postgres://u:p@h:5432/db")
	t.Setenv("CRMKIT_STORAGE_MAX_OPEN_CONNS", "3")
	t.Setenv("CRMKIT_RATELIMIT_RPS", "5")
	t.Setenv("CRMKIT_EMAIL_PROVIDER", "resend")
	t.Setenv("CRMKIT_EMAIL_RESEND_API_KEY", "re_test")

	cfg, err := Load("") // no file -> env + defaults
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Server.ListenAddr != ":9999" {
		t.Errorf("listen_addr = %q", cfg.Server.ListenAddr)
	}
	if !cfg.Server.Local {
		t.Error("local should be true")
	}
	if cfg.Storage.Backend != "postgres" || cfg.Storage.DSN != "postgres://u:p@h:5432/db" {
		t.Errorf("storage = %+v", cfg.Storage)
	}
	if cfg.Storage.MaxOpenConns != 3 {
		t.Errorf("max_open_conns = %d", cfg.Storage.MaxOpenConns)
	}
	if cfg.RateLimit.RPS != 5 {
		t.Errorf("rps = %v", cfg.RateLimit.RPS)
	}
	if cfg.Email.EffectiveProvider() != "resend" {
		t.Errorf("provider = %s", cfg.Email.EffectiveProvider())
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("validate: %v", err)
	}
}

func TestEnvBadValue(t *testing.T) {
	t.Setenv("CRMKIT_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
	t.Setenv("CRMKIT_RATELIMIT_RPS", "not-a-number")
	if _, err := Load(""); err == nil {
		t.Fatal("expected an error for a non-numeric CRMKIT_RATELIMIT_RPS")
	}
}

func TestEffectiveBackend(t *testing.T) {
	cases := []struct {
		name    string
		backend string
		dsn     string
		want    string
	}{
		{"empty defaults to sqlite", "", "", "sqlite"},
		{"sqlite file path", "", "/var/lib/crmkit/crmkit.db", "sqlite"},
		{"memory", "", ":memory:", "sqlite"},
		{"postgres url inferred", "", "postgres://u:p@h:5432/db?sslmode=require", "postgres"},
		{"postgresql url inferred", "", "postgresql://u:p@h/db", "postgres"},
		{"explicit override wins", "postgres", "host=h dbname=db", "postgres"},
		{"explicit sqlite with pg-ish path", "sqlite", "postgres-data.db", "sqlite"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c Config
			c.Storage.Backend = tc.backend
			c.Storage.DSN = tc.dsn
			if got := c.EffectiveBackend(); got != tc.want {
				t.Errorf("EffectiveBackend() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPlanDefaults(t *testing.T) {
	c := Default()
	if c.Plans.Default != "basic" {
		t.Errorf("default plan = %q, want basic", c.Plans.Default)
	}
	l, ok := c.Plans.Catalogue["basic"]
	if !ok {
		t.Fatal("basic plan missing from the catalogue")
	}
	if l.MaxContacts <= 0 || l.MaxWorkspaces <= 0 || l.MaxMembers <= 0 {
		t.Errorf("basic limits look unset: %+v", l)
	}
	// Unknown plan falls back to the default plan's limits.
	if c.Plans.LimitsFor("does-not-exist").MaxContacts != l.MaxContacts {
		t.Error("LimitsFor(unknown) should fall back to the default plan")
	}
	// Resource name mapping; unknown resource is unlimited.
	if l.For("contacts") != l.MaxContacts || l.For("workspaces") != l.MaxWorkspaces || l.For("nope") != -1 {
		t.Errorf("For() mapping wrong: %+v", l)
	}
}

func TestCheckMailDelivery(t *testing.T) {
	// Log mode (no provider configured) outside dev must be refused - codes
	// would be written to logs.
	var c Config
	if err := c.CheckMailDelivery(); err == nil {
		t.Fatal("log mode without dev should be refused")
	}

	// Log mode is fine in local mode.
	c.Server.Local = true
	if err := c.CheckMailDelivery(); err != nil {
		t.Fatalf("log mode + local should be allowed: %v", err)
	}

	// A real provider is fine outside dev.
	var c2 Config
	c2.Email.Provider = "smtp"
	c2.Email.SMTPHost = "smtp.example.com"
	c2.Email.SMTPPort = 587
	if err := c2.CheckMailDelivery(); err != nil {
		t.Fatalf("smtp provider should be allowed: %v", err)
	}
}
