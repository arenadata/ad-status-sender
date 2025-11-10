package config

import (
	"errors"
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
	RulesPath      string `yaml:"rules_path"`
	Interval       string `yaml:"interval"`
	HTTPTimeout    string `yaml:"http_timeout"`
	Concurrency    int    `yaml:"concurrency"`
	LogBodies      bool   `yaml:"log_bodies"`
	ForceSendAfter string `yaml:"force_send_after"`
	LogLevel       string `yaml:"log_level"`
	LogFormat      string `yaml:"log_format"` // "text" or "json"
	TLS            TLS    `yaml:"tls"`
}

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
	if c.ADCMURL == "" || c.HostID == 0 || c.RulesPath == "" {
		return Config{}, errors.New("adcm_url, host_id, rules_path are required")
	}
	if c.Concurrency <= 0 {
		c.Concurrency = runtime.NumCPU()
	}
	return c, nil
}

func LoadToken(c *Config) (string, error) {
	if t := strings.TrimSpace(c.Token); t != "" {
		return t, nil
	}
	if dir := os.Getenv("CREDENTIALS_DIRECTORY"); dir != "" {
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
