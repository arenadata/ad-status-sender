package runner

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"testing"

	"github.com/arenadata/ad-status-sender/internal/config"
)

// We don't spin up TLS servers here; just validate config→tlsConfig mapping.
func TestBuildTLSConfigBasics(t *testing.T) {
	c := config.Config{}
	tlsConf := buildTLSConfig(c)
	if tlsConf.MinVersion < tls.VersionTLS12 {
		t.Fatalf("MinVersion must be TLS1.2+")
	}

	// server_name override
	c = config.Config{TLS: config.TLS{ServerName: "test.local"}}
	tlsConf = buildTLSConfig(c)
	if tlsConf.ServerName != "test.local" {
		t.Fatalf("server_name not applied")
	}

	// custom CA file (PEM may be garbage; append will just ignore)
	tmp := t.TempDir()
	pem := filepath.Join(tmp, "ca.pem")
	_ = os.WriteFile(pem, []byte("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"), 0o644)
	c = config.Config{TLS: config.TLS{CAFile: pem}}
	_ = buildTLSConfig(c) // should not panic
}
