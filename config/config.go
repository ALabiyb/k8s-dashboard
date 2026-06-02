package config

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

	return &cfg, nil
}
