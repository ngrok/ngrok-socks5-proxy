package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// noConfigFile returns a path that doesn't exist and isn't defaultConfigPath(),
// so buildConfig falls through to zero-value config without touching the
// real filesystem or auto-creating a default config.
func noConfigFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "nonexistent.yaml")
}

func parseFlags(t *testing.T, args []string) *proxyFlags {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	f := registerProxyFlags(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parsing flags: %v", err)
	}
	return f
}

func TestBuildConfig_ListenModeNoAuthtokenRequired(t *testing.T) {
	f := parseFlags(t, []string{"--listen=127.0.0.1:9080", "--allow=*.corp.local"})
	f.configFile = noConfigFile(t)

	cfg, _, err := buildConfig(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Listen != "127.0.0.1:9080" {
		t.Errorf("Listen = %q, want 127.0.0.1:9080", cfg.Listen)
	}
	if cfg.Authtoken != "" {
		t.Errorf("Authtoken = %q, want empty (not required in listen mode)", cfg.Authtoken)
	}
}

func TestBuildConfig_MissingAllowRejected(t *testing.T) {
	f := parseFlags(t, []string{"--listen=127.0.0.1:9080"})
	f.configFile = noConfigFile(t)

	if _, _, err := buildConfig(f); err == nil {
		t.Fatal("expected error for missing --allow, got nil")
	}
}

func TestBuildConfig_ListenAndURLMutuallyExclusive(t *testing.T) {
	f := parseFlags(t, []string{"--listen=127.0.0.1:9080", "--url=tcp://", "--allow=*.corp.local"})
	f.configFile = noConfigFile(t)

	if _, _, err := buildConfig(f); err == nil {
		t.Fatal("expected mutual-exclusivity error, got nil")
	}
}

func TestBuildConfig_URLModeRequiresAuthtoken(t *testing.T) {
	t.Setenv("NGROK_AUTHTOKEN", "")
	f := parseFlags(t, []string{"--url=tcp://", "--allow=*.corp.local"})
	f.configFile = noConfigFile(t)

	if _, _, err := buildConfig(f); err == nil {
		t.Fatal("expected authtoken-required error, got nil")
	}
}

func TestBuildConfig_AuthtokenFromEnv(t *testing.T) {
	t.Setenv("NGROK_AUTHTOKEN", "env-token")
	f := parseFlags(t, []string{"--url=tcp://", "--allow=*.corp.local"})
	f.configFile = noConfigFile(t)

	cfg, _, err := buildConfig(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Authtoken != "env-token" {
		t.Errorf("Authtoken = %q, want %q from NGROK_AUTHTOKEN", cfg.Authtoken, "env-token")
	}
}

func TestBuildConfig_DialTimeoutParsed(t *testing.T) {
	f := parseFlags(t, []string{"--listen=127.0.0.1:9080", "--allow=*.corp.local", "--dial-timeout=15s"})
	f.configFile = noConfigFile(t)

	_, dialTimeout, err := buildConfig(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dialTimeout != 15*time.Second {
		t.Errorf("dialTimeout = %v, want 15s", dialTimeout)
	}
}

func TestBuildConfig_InvalidDialTimeoutRejected(t *testing.T) {
	f := parseFlags(t, []string{"--listen=127.0.0.1:9080", "--allow=*.corp.local", "--dial-timeout=notaduration"})
	f.configFile = noConfigFile(t)

	if _, _, err := buildConfig(f); err == nil {
		t.Fatal("expected invalid-dial-timeout error, got nil")
	}
}

func TestBuildConfig_AllowFlagMergesWithConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("listen: \"127.0.0.1:9080\"\nallow:\n  - \"crm.test.local\"\n"), 0o600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}

	f := parseFlags(t, []string{"--allow=sso.test.local"})
	f.configFile = path

	cfg, _, err := buildConfig(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Allow) != 2 || cfg.Allow[0] != "crm.test.local" || cfg.Allow[1] != "sso.test.local" {
		t.Errorf("Allow = %v, want [crm.test.local sso.test.local]", cfg.Allow)
	}
}

func TestBuildConfig_ListenFlagOverridesConfigFileURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("url: \"tcp://1.tcp.ngrok.io:12345\"\nallow:\n  - \"*.corp.local\"\n"), 0o600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}

	f := parseFlags(t, []string{"--listen=127.0.0.1:9080"})
	f.configFile = path

	cfg, _, err := buildConfig(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Listen != "127.0.0.1:9080" {
		t.Errorf("Listen = %q, want 127.0.0.1:9080", cfg.Listen)
	}
	if cfg.URL != "" {
		t.Errorf("URL = %q, want empty (cleared by --listen override)", cfg.URL)
	}
}
