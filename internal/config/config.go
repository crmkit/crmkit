// Package config loads and validates the crmkit server configuration. The
// config is a small YAML file; every credential-like field may reference an
// environment variable using the "$NAME" or "${NAME}" syntax.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	defaultListenAddr    = ":8080"
	defaultBaseURL       = "http://localhost:8080"
	defaultOTPTTL        = 600     // seconds a login code stays valid
	defaultEscalationTTL = 600     // seconds a step-up code stays valid
	defaultTokenIdleTTL  = 2592000 // 30 days of inactivity before an access token expires
	defaultTokenName     = "default"
)

// Config is the top-level crmkit server configuration.
type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Storage   StorageConfig   `yaml:"storage"`
	RateLimit RateLimitConfig `yaml:"ratelimit"`
	Email     EmailConfig     `yaml:"email"`
	Plans     PlansConfig     `yaml:"plans"`
}

// DefaultPlan is the name of the built-in plan assigned to new users and
// workspaces when none is configured. Kept deliberately neutral ("basic")
// rather than commercial-sounding (free/pro).
const DefaultPlan = "basic"

// PlansConfig defines named resource plans and the default plan assigned to new
// users and workspaces. Each user/workspace stores a plan name; changing it
// changes the limits that apply (an "upgrade"). The built-in "basic" plan is
// provided by default and can be overridden - or new plans added - here.
type PlansConfig struct {
	// Default is the plan name assigned to new users and workspaces.
	Default string `yaml:"default"`
	// Catalogue maps a plan name to its limits. Defined in config; the built-in
	// default plan is injected when absent (see applyDefaults). Note: being a
	// map, this is configured via the YAML file, not CRMKIT_* env vars.
	Catalogue map[string]PlanLimits `yaml:"catalogue"`
}

// PlanLimits caps how many objects may be created. A value of -1 means
// unlimited. The workspace-scoped limits (members, contacts, companies, deals)
// are governed by the workspace's plan; MaxWorkspaces (per user) is governed by
// the user's plan. MaxMembers counts a "seat" as a member plus any pending
// invite, so over-inviting beyond the cap is rejected.
type PlanLimits struct {
	MaxWorkspaces int `yaml:"max_workspaces"` // per user
	MaxMembers    int `yaml:"max_members"`    // per workspace (members + pending invites)
	MaxContacts   int `yaml:"max_contacts"`
	MaxCompanies  int `yaml:"max_companies"`
	MaxDeals      int `yaml:"max_deals"`
}

// defaultBasicLimits is the built-in "basic" plan, applied when the config does
// not define the default plan. Deliberately generous enough for real use while
// bounding runaway/abusive growth.
func defaultBasicLimits() PlanLimits {
	return PlanLimits{
		MaxWorkspaces: 3,
		MaxMembers:    5,
		MaxContacts:   1000,
		MaxCompanies:  500,
		MaxDeals:      1000,
	}
}

// LimitsFor returns the limits for a plan name, falling back to the default
// plan's limits when the name is unknown (e.g. a row on a since-removed plan).
func (p PlansConfig) LimitsFor(plan string) PlanLimits {
	if l, ok := p.Catalogue[plan]; ok {
		return l
	}
	return p.Catalogue[p.Default]
}

// For returns the limit for a named resource ("workspaces", "members",
// "contacts", "companies", "deals"); -1 (unlimited) for anything unknown.
func (l PlanLimits) For(resource string) int {
	switch resource {
	case "workspaces":
		return l.MaxWorkspaces
	case "members":
		return l.MaxMembers
	case "contacts":
		return l.MaxContacts
	case "companies":
		return l.MaxCompanies
	case "deals":
		return l.MaxDeals
	}
	return -1
}

// RateLimitConfig configures request throttling. The backend is pluggable:
// "memory" (default, in-process - correct for a single instance) or "redis" (a
// shared store needed when running multiple replicas).
type RateLimitConfig struct {
	// Backend is "memory" (default) or "redis".
	Backend string `yaml:"backend"`
	// DSN is the backend connection string (redis:// URL; ignored for memory).
	DSN string `yaml:"dsn"`
	// RPS is the per-client-IP request rate (requests/second) across the whole
	// API; Burst is the bucket size. Set RPS negative to disable limiting.
	RPS   float64 `yaml:"rps"`
	Burst int     `yaml:"burst"`
	// AuthPerHour is the stricter per-email cap on login attempts
	// (/auth/request and /auth/verify). 0 disables the auth-specific limit.
	AuthPerHour int `yaml:"auth_per_hour"`
}

// StorageConfig selects the persistence backend. SQLite is the default (a
// single file, ideal for local and small deployments); point the DSN at a
// postgres:// URL for a more robust production database.
type StorageConfig struct {
	// Backend is optional: when empty it is inferred from the DSN (a
	// "postgres://" / "postgresql://" URL means postgres, anything else means
	// sqlite). Set it explicitly only to override inference - e.g. a libpq
	// keyword/value postgres DSN ("host=… dbname=…"), which has no URL scheme.
	Backend string `yaml:"backend"`
	// DSN is the connection string. For sqlite it is a file path; if empty,
	// server.db_path is used. For postgres it is a postgres:// URL.
	DSN string `yaml:"dsn"`

	// Connection-pool tuning (postgres only; sqlite is single-connection).
	MaxOpenConns           int `yaml:"max_open_conns"`
	MaxIdleConns           int `yaml:"max_idle_conns"`
	ConnMaxLifetimeSeconds int `yaml:"conn_max_lifetime_seconds"`
}

// EffectiveBackend returns the storage backend, inferring it from the DSN when
// Backend is unset. A "postgres://" / "postgresql://" DSN is postgres; anything
// else (a file path, ":memory:", or empty) is sqlite. An explicit Backend wins,
// so an override is available for DSN forms that can't be sniffed (e.g. a libpq
// keyword/value postgres connection string).
func (c Config) EffectiveBackend() string {
	if b := strings.TrimSpace(c.Storage.Backend); b != "" {
		return b
	}
	dsn := strings.TrimSpace(c.Storage.DSN)
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		return "postgres"
	}
	return "sqlite"
}

// EffectiveDSN resolves the connection string for the chosen backend, falling
// back to the sqlite db_path when no DSN is set for the sqlite backend.
func (c Config) EffectiveDSN() string {
	if strings.TrimSpace(c.Storage.DSN) != "" {
		return c.Storage.DSN
	}
	if c.EffectiveBackend() == "sqlite" {
		return c.Server.DBPath
	}
	return ""
}

// ServerConfig controls the HTTP listener and storage.
type ServerConfig struct {
	// ListenAddr is the address the HTTP API binds to (e.g. ":8080").
	ListenAddr string `yaml:"listen_addr"`
	// BaseURL is the public URL agents use to reach the API. It appears in
	// the operating manual and outbound emails.
	BaseURL string `yaml:"base_url"`
	// DBPath is the SQLite database location.
	DBPath string `yaml:"db_path"`
	// OTPTTLSeconds is how long an emailed login code remains valid.
	OTPTTLSeconds int `yaml:"otp_ttl_seconds"`
	// EscalationTTLSeconds is how long an emailed step-up (escalation) code
	// remains valid.
	EscalationTTLSeconds int `yaml:"escalation_ttl_seconds"`
	// TokenIdleTTLSeconds is the sliding inactivity window for access tokens:
	// a token expires this many seconds after it was last used (each use slides
	// the window forward). Set to 0 to disable expiry (tokens live until revoked).
	TokenIdleTTLSeconds int `yaml:"token_idle_ttl_seconds"`
	// TrustProxyHeaders, when true, derives the client IP from
	// X-Forwarded-For / X-Real-IP (use only behind a trusted proxy/LB; these
	// headers are client-spoofable when exposed directly).
	TrustProxyHeaders bool `yaml:"trust_proxy_headers"`

	// LogFormat is "text" (default) or "json" for structured logs.
	LogFormat string `yaml:"log_format"`

	// SecretKey keys the HMAC used to hash low-entropy login/step-up codes, so a
	// read-only database leak cannot brute-force active codes offline. Set it
	// (and share it across instances) in production; if empty, crmkitd generates
	// an ephemeral key at startup. Accepts a "$ENV_VAR" reference.
	SecretKey string `yaml:"secret_key"`

	// Local enables single-user "local mode": no mail provider is required, and
	// login/step-up codes are logged to stderr and echoed in the /auth/request
	// response so an agent on the same machine can authenticate itself without
	// email. Intended for running crmkit next to your agent on localhost - not
	// for an internet-facing, multi-tenant deployment.
	Local bool `yaml:"local"`
}

// EmailConfig configures outbound email (login codes, and later reminders).
// Delivery is provider-based and swappable: pick "smtp", "resend", or "log".
// When nothing is configured, crmkit runs in "log" mode and prints messages to
// stderr instead of sending them - convenient for local development. Add a new
// provider by implementing auth.Mailer and a case in auth.NewMailer.
type EmailConfig struct {
	// Provider selects the delivery backend: "log" | "smtp" | "resend" | "ses" |
	// "cloudflare". Empty means infer from the fields below (smtp if smtp_host is
	// set, resend if resend_api_key is set, ses if ses_access_key_id is set,
	// cloudflare if cloudflare_api_token is set, otherwise log).
	Provider string `yaml:"provider"`

	// From is the sender address used on outbound mail.
	From string `yaml:"from"`

	// SMTP relay settings (provider "smtp"). Also covers Amazon SES SMTP,
	// Postmark, Mailgun, and any standard relay - point it at their endpoint.
	SMTPHost string `yaml:"smtp_host"`
	SMTPPort int    `yaml:"smtp_port"`
	SMTPUser string `yaml:"smtp_user"`
	SMTPPass string `yaml:"smtp_pass"`

	// Resend HTTP API settings (provider "resend").
	ResendAPIKey string `yaml:"resend_api_key"`

	// Amazon SES API settings (provider "ses"), signed with SigV4 (no AWS SDK).
	// SESSessionToken is optional, for temporary (STS) credentials.
	SESRegion          string `yaml:"ses_region"`
	SESAccessKeyID     string `yaml:"ses_access_key_id"`
	SESSecretAccessKey string `yaml:"ses_secret_access_key"`
	SESSessionToken    string `yaml:"ses_session_token"`

	// Cloudflare Email Service settings (provider "cloudflare"): a single JSON
	// POST to the account's email/sending/send endpoint with a bearer API token.
	// The From domain must be verified for sending in the Cloudflare dashboard.
	CloudflareAccountID string `yaml:"cloudflare_account_id"`
	CloudflareAPIToken  string `yaml:"cloudflare_api_token"`
}

// EffectiveProvider resolves the delivery backend, inferring one from the
// configured fields when Provider is empty.
func (e EmailConfig) EffectiveProvider() string {
	if p := strings.ToLower(strings.TrimSpace(e.Provider)); p != "" {
		return p
	}
	if strings.TrimSpace(e.SMTPHost) != "" {
		return "smtp"
	}
	if strings.TrimSpace(e.ResendAPIKey) != "" {
		return "resend"
	}
	if strings.TrimSpace(e.SESAccessKeyID) != "" {
		return "ses"
	}
	if strings.TrimSpace(e.CloudflareAPIToken) != "" {
		return "cloudflare"
	}
	return "log"
}

// IsLocal reports whether email is in log mode (no real delivery) - used to
// decide whether to print login codes to the console for convenience.
func (e EmailConfig) IsLocal() bool {
	return e.EffectiveProvider() == "log"
}

// ResolveCredential expands a "$NAME" / "${NAME}" reference to the value of
// the named environment variable. Plain values are returned unchanged.
func ResolveCredential(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", errors.New("credential value cannot be empty")
	}

	if strings.HasPrefix(trimmed, "$") {
		envName := strings.TrimPrefix(trimmed, "$")
		envName = strings.TrimPrefix(envName, "{")
		envName = strings.TrimSuffix(envName, "}")
		envName = strings.TrimSpace(envName)
		if envName == "" {
			return "", errors.New("credential env reference is invalid")
		}

		resolved := strings.TrimSpace(os.Getenv(envName))
		if resolved == "" {
			return "", fmt.Errorf("environment variable %q is not set", envName)
		}

		return resolved, nil
	}

	return trimmed, nil
}

// Load builds the configuration by layering, lowest precedence first:
//
//	defaults  <  config file  <  $VAR refs in file values  <  CRMKIT_* env vars
//
// path is the config file; when empty, the default path is used and treated as
// optional (absent is fine - env vars alone can fully configure the server). An
// explicitly-requested file that is missing or malformed is an error. Validation
// is deliberately NOT run here so callers can apply flag overrides first; call
// Config.Validate afterwards.
func Load(path string) (Config, error) {
	explicit := strings.TrimSpace(path) != ""
	if !explicit {
		path = DefaultConfigPath()
	}

	var cfg Config
	switch data, err := os.ReadFile(path); {
	case err == nil:
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		if err := decoder.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("parse yaml %s: %w", path, err)
		}
	case explicit || !errors.Is(err, os.ErrNotExist):
		// An explicitly-requested file, or any error other than "missing", is fatal.
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	default:
		// The default config path is absent - fine. Env + defaults provide config.
	}

	if err := cfg.resolve(); err != nil {
		return Config{}, err
	}
	if err := applyEnv(&cfg); err != nil {
		return Config{}, err
	}
	applyDefaults(&cfg)
	return cfg, nil
}

// Default returns a config suitable for local development with no file.
func Default() Config {
	cfg := Config{}
	applyDefaults(&cfg)
	return cfg
}

// CheckMailDelivery fails closed when there is no way to deliver codes securely:
// "log" email mode prints login and step-up codes to the logs, which is only
// acceptable in dev. In production (not dev) a real provider must be configured.
func (c Config) CheckMailDelivery() error {
	if c.Email.EffectiveProvider() == "log" && !c.Server.Local {
		return errors.New("no email provider configured: login/step-up codes would be written to logs. Configure email.* (smtp/resend/ses/cloudflare) for a served deployment, or run with --local for a single-user local instance")
	}
	return nil
}

// resolve expands environment-variable references in credential fields.
func (cfg *Config) resolve() error {
	for _, field := range []*string{
		&cfg.Server.SecretKey,
		&cfg.Email.SMTPUser, &cfg.Email.SMTPPass, &cfg.Email.ResendAPIKey,
		&cfg.Email.SESRegion, &cfg.Email.SESAccessKeyID, &cfg.Email.SESSecretAccessKey, &cfg.Email.SESSessionToken,
	} {
		if strings.TrimSpace(*field) == "" {
			continue
		}
		resolved, err := ResolveCredential(*field)
		if err != nil {
			return err
		}
		*field = resolved
	}
	return nil
}

func applyDefaults(cfg *Config) {
	if cfg.Server.ListenAddr == "" {
		cfg.Server.ListenAddr = defaultListenAddr
	}
	if cfg.Server.BaseURL == "" {
		cfg.Server.BaseURL = defaultBaseURL
	}
	if cfg.Server.DBPath == "" {
		cfg.Server.DBPath = DefaultDBPath()
	}
	if cfg.Server.OTPTTLSeconds <= 0 {
		cfg.Server.OTPTTLSeconds = defaultOTPTTL
	}
	if cfg.Server.EscalationTTLSeconds <= 0 {
		cfg.Server.EscalationTTLSeconds = defaultEscalationTTL
	}
	// A negative value is normalized to 0 (disabled); 0 stays 0; unset (also 0
	// from YAML) takes the default. We can't distinguish unset from explicit 0,
	// so 0 means "use default"; pass a large number to effectively disable.
	if cfg.Server.TokenIdleTTLSeconds == 0 {
		cfg.Server.TokenIdleTTLSeconds = defaultTokenIdleTTL
	}
	if cfg.Server.TokenIdleTTLSeconds < 0 {
		cfg.Server.TokenIdleTTLSeconds = 0
	}
	if cfg.Email.From == "" {
		cfg.Email.From = "crmkit <no-reply@localhost>"
	}
	// storage.backend is intentionally left unset when not provided: it is
	// inferred from the DSN (see EffectiveBackend).
	if cfg.Server.LogFormat == "" {
		cfg.Server.LogFormat = "text"
	}
	if cfg.RateLimit.Backend == "" {
		cfg.RateLimit.Backend = "memory"
	}
	if cfg.Plans.Default == "" {
		cfg.Plans.Default = DefaultPlan
	}
	if cfg.Plans.Catalogue == nil {
		cfg.Plans.Catalogue = map[string]PlanLimits{}
	}
	// Ensure the default plan always exists with usable limits, so quotas work
	// out of the box (e.g. the env-only deployment with no plans config).
	if _, ok := cfg.Plans.Catalogue[cfg.Plans.Default]; !ok {
		cfg.Plans.Catalogue[cfg.Plans.Default] = defaultBasicLimits()
	}
	// Rate-limit defaults apply only when unset (0). Set rps negative to disable
	// the general limiter; it is normalized to 0 (disabled) below.
	if cfg.RateLimit.RPS == 0 {
		cfg.RateLimit.RPS = 20
	}
	if cfg.RateLimit.RPS < 0 {
		cfg.RateLimit.RPS = 0
	}
	if cfg.RateLimit.Burst <= 0 {
		cfg.RateLimit.Burst = 40
	}
	if cfg.RateLimit.AuthPerHour == 0 {
		cfg.RateLimit.AuthPerHour = 20
	}
	if cfg.RateLimit.AuthPerHour < 0 {
		cfg.RateLimit.AuthPerHour = 0
	}
}

// Validate checks the fully-resolved config (file + env + flags + defaults).
// Callers run it after applying any flag overrides.
func (cfg Config) Validate() error {
	if !strings.Contains(cfg.Server.ListenAddr, ":") {
		return fmt.Errorf("server.listen_addr %q must include a port", cfg.Server.ListenAddr)
	}
	switch cfg.EffectiveBackend() {
	case "sqlite":
	case "postgres":
		if strings.TrimSpace(cfg.Storage.DSN) == "" {
			return errors.New(`storage.dsn (postgres:// URL) is required for backend "postgres"`)
		}
	default:
		return fmt.Errorf("unknown storage.backend %q (use sqlite or postgres)", cfg.Storage.Backend)
	}
	switch cfg.RateLimit.Backend {
	case "memory":
	case "redis":
		if strings.TrimSpace(cfg.RateLimit.DSN) == "" {
			return errors.New(`ratelimit.dsn (redis:// URL) is required for backend "redis"`)
		}
	default:
		return fmt.Errorf("unknown ratelimit.backend %q (use memory or redis)", cfg.RateLimit.Backend)
	}
	switch cfg.Email.EffectiveProvider() {
	case "log":
	case "smtp":
		if strings.TrimSpace(cfg.Email.SMTPHost) == "" {
			return errors.New(`email.smtp_host is required for provider "smtp"`)
		}
		if cfg.Email.SMTPPort == 0 {
			return errors.New(`email.smtp_port is required for provider "smtp"`)
		}
	case "resend":
		if strings.TrimSpace(cfg.Email.ResendAPIKey) == "" {
			return errors.New(`email.resend_api_key is required for provider "resend"`)
		}
	case "ses":
		if strings.TrimSpace(cfg.Email.SESRegion) == "" {
			return errors.New(`email.ses_region is required for provider "ses"`)
		}
		if strings.TrimSpace(cfg.Email.SESAccessKeyID) == "" || strings.TrimSpace(cfg.Email.SESSecretAccessKey) == "" {
			return errors.New(`email.ses_access_key_id and email.ses_secret_access_key are required for provider "ses"`)
		}
	case "cloudflare":
		if strings.TrimSpace(cfg.Email.CloudflareAccountID) == "" || strings.TrimSpace(cfg.Email.CloudflareAPIToken) == "" {
			return errors.New(`email.cloudflare_account_id and email.cloudflare_api_token are required for provider "cloudflare"`)
		}
	default:
		return fmt.Errorf("unknown email.provider %q (use log, smtp, resend, ses, or cloudflare)", cfg.Email.EffectiveProvider())
	}
	return nil
}
