package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cleanEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"RB_ADMIN_PASSWORD", "RB_COOKIE_SECRET", "RB_SESSION_HOST_DATA_DIR", "RB_PUBLISH_SESSION_TCP_PORTS", "RB_DOCKER_NETWORK", "RB_DEFAULT_INSTANCE_QUOTA", "RB_SESSION_IDLE_TIMEOUT", "RB_SMTP_TIMEOUT"} {
		t.Setenv(k, "")
	}
	t.Setenv("RB_DATA_DIR", t.TempDir())
	t.Setenv("RB_DEFAULT_INSTANCE_QUOTA", "5")
}
func TestLoadPersistsIndependentSigningSecret(t *testing.T) {
	cleanEnv(t)
	t.Setenv("RB_ADMIN_PASSWORD", "test-password")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.CookieSecret) != 64 {
		t.Fatal("expected random 32-byte key")
	}
	t.Setenv("RB_ADMIN_PASSWORD", "different-password")
	again, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if again.CookieSecret != cfg.CookieSecret {
		t.Fatal("password change invalidated signing key")
	}
	info, err := os.Stat(filepath.Join(cfg.DataDir, "cookie-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("secret permissions=%v", info.Mode())
	}
	if cfg.SessionHostDataDir != cfg.DataDir {
		t.Fatal("default host data directory mismatch")
	}
}
func TestConfigIgnoresIdleTimeoutAndSupportsZeroQuota(t *testing.T) {
	cleanEnv(t)
	t.Setenv("RB_SESSION_IDLE_TIMEOUT", "obsolete-invalid-value")
	t.Setenv("RB_DEFAULT_INSTANCE_QUOTA", "0")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SessionIdleTimeout != 0 || cfg.DefaultInstanceQuota != 0 {
		t.Fatal("idle timeout or zero quota incorrect")
	}
}
func TestConfigExplicitSecretAndValidation(t *testing.T) {
	cleanEnv(t)
	t.Setenv("RB_COOKIE_SECRET", strings.Repeat("x", 32))
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CookieSecret != strings.Repeat("x", 32) {
		t.Fatal("explicit secret ignored")
	}
	t.Setenv("RB_COOKIE_SECRET", "short")
	if _, err = Load(); err == nil {
		t.Fatal("accepted short secret")
	}
}
