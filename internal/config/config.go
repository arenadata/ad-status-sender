package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

type TLS struct {
	CAFile             string `yaml:"ca_file"`
	CertFile           string `yaml:"cert_file"`
	KeyFile            string `yaml:"key_file"`
	ServerName         string `yaml:"server_name"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify"`
}

type Config struct {
	ADCMURL        string `yaml:"adcm_url"`
	HostID         int    `yaml:"host_id"`
	Token          string `yaml:"token"`
	TokenFile      string `yaml:"token_file"`
	ADCMUser       string `yaml:"adcm_user"`
	ADCMPass       string `yaml:"adcm_password"`
	RulesSource    string `yaml:"rules_source"` // "yaml" | "legacy" | "adcm"
	RulesPath      string `yaml:"rules_path"`
	RulesDB        string `yaml:"rules_db"`
	LegacyDir      string `yaml:"legacy_dir"`
	RulesRefresh   string `yaml:"rules_refresh_interval"`
	Interval       string `yaml:"interval"`
	HTTPTimeout    string `yaml:"http_timeout"`
	Concurrency    int    `yaml:"concurrency"`
	LogBodies      bool   `yaml:"log_bodies"`
	ForceSendAfter string `yaml:"force_send_after"`
	LogLevel       string `yaml:"log_level"`
	LogFormat      string `yaml:"log_format"` // "text" or "json"
	// File logging with rotation. When LogFile is empty the agent logs to
	// stdout (captured by journald); when set it writes to that file and
	// rotates it in-process.
	LogFile       string `yaml:"log_file"`
	LogMaxSizeMB  int    `yaml:"log_max_size_mb"`  // rotate past this size (default 100)
	LogMaxBackups int    `yaml:"log_max_backups"`  // rotated files to keep (default 7)
	LogMaxAgeDays int    `yaml:"log_max_age_days"` // delete rotated older than N days (default 28)
	LogCompress   *bool  `yaml:"log_compress"`     // gzip rotated files (default true)
	TLS           TLS    `yaml:"tls"`
}

// Log rotation defaults, applied when log_file is set.
const (
	defaultLogMaxSizeMB  = 100
	defaultLogMaxBackups = 7
	defaultLogMaxAgeDays = 28
)

func MustDuration(s string, def time.Duration) time.Duration {
	if strings.TrimSpace(s) == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		panic(err)
	}
	return d
}

func Load(path string) (Config, error) {
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		return Config{}, readErr
	}
	var c Config
	if unErr := yaml.Unmarshal(data, &c); unErr != nil {
		return Config{}, unErr
	}
	c.RulesSource = strings.ToLower(strings.TrimSpace(c.RulesSource))
	if c.RulesSource == "" {
		c.RulesSource = "yaml"
	}
	if c.ADCMURL == "" || c.RulesDB == "" {
		return Config{}, errors.New("adcm_url and rules_db are required")
	}
	if c.HostID == 0 && c.RulesSource != "legacy" {
		return Config{}, errors.New("host_id is required for non-legacy rules_source")
	}
	switch c.RulesSource {
	case "yaml":
		if c.RulesPath == "" {
			return Config{}, errors.New("rules_path is required for rules_source: yaml")
		}
	case "legacy":
		if c.LegacyDir == "" {
			return Config{}, errors.New("legacy_dir is required for rules_source: legacy")
		}
	case "adcm":
		// no extra fields required
	default:
		return Config{}, errors.New("rules_source must be one of: yaml, legacy, adcm")
	}
	if c.Concurrency <= 0 {
		c.Concurrency = runtime.NumCPU()
	}
	applyLogDefaults(&c)
	if err := validateDurations(&c); err != nil {
		return Config{}, err
	}
	return c, nil
}

// applyLogDefaults fills rotation defaults so retention is always bounded when
// file logging is enabled (lumberjack's own zero-values mean "keep forever").
func applyLogDefaults(c *Config) {
	if c.LogFile == "" {
		return
	}
	if c.LogMaxSizeMB <= 0 {
		c.LogMaxSizeMB = defaultLogMaxSizeMB
	}
	if c.LogMaxBackups <= 0 {
		c.LogMaxBackups = defaultLogMaxBackups
	}
	if c.LogMaxAgeDays <= 0 {
		c.LogMaxAgeDays = defaultLogMaxAgeDays
	}
}

// LogCompressEnabled reports whether rotated files should be gzipped; defaults
// to true when log_compress is omitted.
func (c *Config) LogCompressEnabled() bool {
	return c.LogCompress == nil || *c.LogCompress
}

// validateDurations rejects malformed duration strings at load time so runtime
// MustDuration calls (in the ticker loop, rules syncer and reload path) cannot panic.
func validateDurations(c *Config) error {
	fields := []struct{ name, val string }{
		{"interval", c.Interval},
		{"http_timeout", c.HTTPTimeout},
		{"force_send_after", c.ForceSendAfter},
		{"rules_refresh_interval", c.RulesRefresh},
	}
	for _, f := range fields {
		if strings.TrimSpace(f.val) == "" {
			continue
		}
		if _, err := time.ParseDuration(f.val); err != nil {
			return fmt.Errorf("invalid %s %q: %w", f.name, f.val, err)
		}
	}
	return nil
}

func LoadToken(c *Config) (string, error) {
	if t := strings.TrimSpace(c.Token); t != "" {
		return t, nil
	}
	if dir := os.Getenv("CREDENTIALS_DIRECTORY"); dir != "" {
		//nolint:gosec // G703: literal filename under systemd CREDENTIALS_DIRECTORY, no traversal
		if b, err := os.ReadFile(filepath.Join(dir, "adcm_token")); err == nil {
			return strings.TrimSpace(string(b)), nil
		}
	}
	if c.TokenFile != "" {
		b, err := os.ReadFile(c.TokenFile)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	return "", errors.New("no token provided")
}

func LoadUserPass(c *Config) (string, string, error) {
	user := strings.TrimSpace(c.ADCMUser)
	pass := strings.TrimSpace(c.ADCMPass)
	if user != "" || pass != "" {
		if user == "" || pass == "" {
			return "", "", errors.New("adcm_user and adcm_password must be set together")
		}
		return user, pass, nil
	}
	if dir := os.Getenv("CREDENTIALS_DIRECTORY"); dir != "" {
		//nolint:gosec // G703: literal filename under systemd CREDENTIALS_DIRECTORY
		u, uErr := os.ReadFile(filepath.Join(dir, "adcm_user"))
		//nolint:gosec // G703: literal filename under systemd CREDENTIALS_DIRECTORY
		p, pErr := os.ReadFile(filepath.Join(dir, "adcm_password"))
		if uErr == nil && pErr == nil {
			user = strings.TrimSpace(string(u))
			pass = strings.TrimSpace(string(p))
			if user == "" || pass == "" {
				return "", "", errors.New("adcm_user or adcm_password is empty")
			}
			return user, pass, nil
		}
	}
	return "", "", errors.New("no adcm credentials provided")
}

func ParseSlogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
