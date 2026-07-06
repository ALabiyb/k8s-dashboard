package config

// ---------------------------------------------------------------------------
// Author: Labiyb M. Said — DevSecOps Engineer
// Contact: abdulmunimsaid82@gmail.com
// ---------------------------------------------------------------------------

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level structure that mirrors config.yaml
type Config struct {
	Server        ServerConfig        `yaml:"server"`
	ExcludedNS    []string            `yaml:"excluded_namespaces"`
	Thresholds    ThresholdConfig     `yaml:"thresholds"`
	Notifications NotificationConfig  `yaml:"notifications"`
	OIDC          OIDCConfig          `yaml:"oidc"`
}

// OIDCConfig holds Keycloak / OpenID Connect settings.
// ClientSecret must be set via the OIDC_CLIENT_SECRET env var in production
// (same pattern as SMTP_PASSWORD) — never commit a real secret to YAML.
//
// Role model: the dashboard maps Keycloak *groups* (not realm roles) to
// dashboard roles. See docs and the K8s Dashboard OIDC RBAC Migration
// runbook for the full model.
type OIDCConfig struct {
	Enabled       bool   `yaml:"enabled"`
	IssuerURL     string `yaml:"issuer_url"`      // e.g. https://keycloak.example.com/realms/myrealm ; overridden by OIDC_ISSUER_URL env var
	ClientID      string `yaml:"client_id"`
	ClientSecret  string `yaml:"client_secret"`   // overridden by OIDC_CLIENT_SECRET env var
	RedirectURL   string `yaml:"redirect_url"`    // must be registered in the Keycloak client ; overridden by OIDC_REDIRECT_URL env var
	AdminGroup    string `yaml:"admin_group"`     // Keycloak group whose members become dashboard admins (e.g. k8s-cluster-admins)
	TLSSkipVerify bool   `yaml:"tls_skip_verify"` // skip TLS cert check for internal/self-signed CAs (dev only)
}

type ServerConfig struct {
	Port         int           `yaml:"port"`
	PollInterval time.Duration `yaml:"poll_interval"`
}

type ThresholdConfig struct {
	// Percentage at or above this = Healthy (green)
	Healthy int `yaml:"healthy"`
	// Percentage at or above this (but below Healthy) = Degraded (amber)
	Degraded int `yaml:"degraded"`
	// Below Degraded = Critical (red)
}

type NotificationConfig struct {
	Email EmailConfig `yaml:"email"`
}

type EmailConfig struct {
	Enabled           bool     `yaml:"enabled"`
	SMTPHost          string   `yaml:"smtp_host"`
	SMTPPort          int      `yaml:"smtp_port"`
	SMTPUsername      string   `yaml:"smtp_username"`
	SMTPPassword      string   `yaml:"smtp_password"`
	From              string   `yaml:"from"`
	To                []string `yaml:"to"`
	OnStateChangeOnly bool     `yaml:"on_state_change_only"`
}

// Load reads config.yaml from the given file path and returns a Config struct.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	// Apply sensible defaults if not set in the YAML
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8081
	}
	if cfg.Server.PollInterval == 0 {
		cfg.Server.PollInterval = 30 * time.Second
	}
	if cfg.Thresholds.Healthy == 0 {
		cfg.Thresholds.Healthy = 100
	}
	if cfg.Thresholds.Degraded == 0 {
		cfg.Thresholds.Degraded = 70
	}

	// SMTP_PASSWORD overrides whatever is in config.yaml when set. This lets
	// the real password live in a Kubernetes Secret (mounted as an env var)
	// instead of the committed/ConfigMap'd YAML — see
	// k8s/k8s/02-deployment.yaml and docs/PRODUCTION_READINESS.md §1.3.
	if pw := os.Getenv("SMTP_PASSWORD"); pw != "" {
		cfg.Notifications.Email.SMTPPassword = pw
	}

	// OIDC_CLIENT_SECRET overrides the YAML value for the same reason —
	// the Keycloak client secret should live in a Kubernetes Secret, not in a
	// ConfigMap or committed YAML file.
	if s := os.Getenv("OIDC_CLIENT_SECRET"); s != "" {
		cfg.OIDC.ClientSecret = s
	}
	// OIDC_ISSUER_URL and OIDC_REDIRECT_URL let the deployment override the
	// values baked into the ConfigMap-shipped YAML. Useful when the same image
	// is deployed to multiple environments (dev/staging/prod) that hit
	// different Keycloak realms or dashboard URLs.
	if s := os.Getenv("OIDC_ISSUER_URL"); s != "" {
		cfg.OIDC.IssuerURL = s
	}
	if s := os.Getenv("OIDC_REDIRECT_URL"); s != "" {
		cfg.OIDC.RedirectURL = s
	}

	return &cfg, nil
}
