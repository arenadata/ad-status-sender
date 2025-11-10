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

func TestLoad_Validate(t *testing.T) {
	yml := []byte(`
adcm_url: "http://localhost"
host_id:  42
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
	if cfg.HostID != 42 || cfg.ADCMURL == "" || cfg.RulesPath == "" {
		t.Fatalf("bad values: %+v", cfg)
	}
}
