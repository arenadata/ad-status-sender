package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMustDuration(t *testing.T) {
	if got := MustDuration("", 123*time.Millisecond); got != 123*time.Millisecond {
		t.Fatalf("def dur: want 123ms, got %v", got)
	}
	if got := MustDuration("250ms", 0); got != 250*time.Millisecond {
		t.Fatalf("parse: want 250ms, got %v", got)
	}
}

func TestLoadToken_Priority(t *testing.T) {
	// prepare temp dir for systemd credentials
	dir := t.TempDir()
	credFile := filepath.Join(dir, "adcm_token")
	if err := os.WriteFile(credFile, []byte("CRED_TOKEN\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// token in YAML has top priority
	c := &Config{
		Token:     "INLINE_TOKEN",
		TokenFile: "", // should be ignored
	}
	tok, err := LoadToken(c)
	if err != nil || tok != "INLINE_TOKEN" {
		t.Fatalf("inline token priority failed: tok=%q err=%v", tok, err)
	}

	// then systemd credential
	t.Setenv("CREDENTIALS_DIRECTORY", dir)
	c = &Config{
		Token:     "",
		TokenFile: "",
	}
	tok, err = LoadToken(c)
	if err != nil || tok != "CRED_TOKEN" {
		t.Fatalf("credential token priority failed: tok=%q err=%v", tok, err)
	}

	// IMPORTANT: clear credentials env so token_file can win
	t.Setenv("CREDENTIALS_DIRECTORY", "")

	// then token_file
	tf := filepath.Join(t.TempDir(), "tok")
	err = os.WriteFile(tf, []byte("FILE_TOKEN\n"), 0o600) // <-- no shadowing
	if err != nil {
		t.Fatal(err)
	}
	c = &Config{
		Token:     "",
		TokenFile: tf,
	}
	tok, err = LoadToken(c)
	if err != nil || tok != "FILE_TOKEN" {
		t.Fatalf("file token failed: tok=%q err=%v", tok, err)
	}
}

func TestLoadUserPass_Priority(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "adcm_user"), []byte("cred_user\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "adcm_password"), []byte("cred_pass\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// config has priority
	c := &Config{
		ADCMUser: "cfg_user",
		ADCMPass: "cfg_pass",
	}
	user, pass, err := LoadUserPass(c)
	if err != nil || user != "cfg_user" || pass != "cfg_pass" {
		t.Fatalf("config creds failed: user=%q pass=%q err=%v", user, pass, err)
	}

	// systemd credentials
	t.Setenv("CREDENTIALS_DIRECTORY", dir)
	c = &Config{}
	user, pass, err = LoadUserPass(c)
	if err != nil || user != "cred_user" || pass != "cred_pass" {
		t.Fatalf("systemd creds failed: user=%q pass=%q err=%v", user, pass, err)
	}
}

func TestLoad_Validate(t *testing.T) {
	yml := []byte(`
adcm_url: "http://localhost"
host_id:  42
rules_db: "/tmp/rules.db"
rules_path: "/tmp/x.yaml"
`)
	fn := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(fn, yml, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(fn)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if cfg.HostID != 42 || cfg.ADCMURL == "" || cfg.RulesPath == "" || cfg.RulesDB == "" {
		t.Fatalf("bad values: %+v", cfg)
	}
}

func TestLoad_LegacyAllowsMissingHostID(t *testing.T) {
	yml := []byte(`
adcm_url: "http://localhost"
rules_source: "legacy"
rules_db: "/tmp/rules.db"
legacy_dir: "/tmp/legacy"
`)
	fn := filepath.Join(t.TempDir(), "cfg.yaml")
	if err := os.WriteFile(fn, yml, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(fn); err != nil {
		t.Fatalf("load failed: %v", err)
	}
}
