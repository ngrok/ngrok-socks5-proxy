package main

import (
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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

// buildLoggerForTest mirrors buildLogger's logic but returns a close func
// so file-destination tests can release the handle before t.TempDir()'s
// cleanup runs — on Windows, an open file blocks deletion (unlike Unix,
// where an unlinked-while-open file is fine), so buildLogger (which opens
// the file internally via logWriter but only returns a *slog.Logger, with
// no way to reach the handle back) can't be used directly in these tests.
func buildLoggerForTest(t *testing.T, cfg config) (*slog.Logger, func()) {
	t.Helper()

	w, err := logWriter(cfg.LogDestination)
	if err != nil {
		t.Fatalf("logWriter: unexpected error: %v", err)
	}

	opts := &slog.HandlerOptions{Level: parseLogLevel(cfg.LogLevel)}
	var h slog.Handler
	switch strings.ToLower(cfg.LogFormat) {
	case "", "text", "logfmt":
		h = slog.NewTextHandler(w, opts)
	case "json":
		h = slog.NewJSONHandler(w, opts)
	default:
		t.Fatalf("unsupported log-format in test: %q", cfg.LogFormat)
	}

	closeFn := func() {
		if closer, ok := w.(io.Closer); ok {
			closer.Close()
		}
	}
	return slog.New(h), closeFn
}

func TestLogWriter_DefaultAndStderr(t *testing.T) {
	for _, dest := range []string{"", "stderr"} {
		w, err := logWriter(dest)
		if err != nil {
			t.Fatalf("logWriter(%q): unexpected error: %v", dest, err)
		}
		if w != os.Stderr {
			t.Errorf("logWriter(%q) = %v, want os.Stderr", dest, w)
		}
	}
}

func TestLogWriter_Stdout(t *testing.T) {
	w, err := logWriter("stdout")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != os.Stdout {
		t.Errorf("logWriter(\"stdout\") = %v, want os.Stdout", w)
	}
}

func TestLogWriter_FalseDiscards(t *testing.T) {
	w, err := logWriter("false")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != io.Discard {
		t.Errorf("logWriter(\"false\") = %v, want io.Discard", w)
	}
}

func TestLogWriter_FilePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.log")

	w, err := logWriter(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatalf("writing through returned writer: %v", err)
	}
	if closer, ok := w.(io.Closer); ok {
		closer.Close()
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading back log file: %v", err)
	}
	if string(got) != "hello\n" {
		t.Errorf("file contents = %q, want %q", got, "hello\n")
	}
}

func TestLogWriter_MissingParentDirErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nosuchdir", "proxy.log")

	if _, err := logWriter(path); err == nil {
		t.Fatal("expected error for missing parent directory, got nil")
	}
}

func TestBuildLogger_InvalidFormatRejected(t *testing.T) {
	cfg := config{LogFormat: "xml", LogDestination: "stderr"}

	if _, err := buildLogger(cfg); err == nil {
		t.Fatal("expected error for invalid log-format, got nil")
	}
}

func TestBuildLogger_JSONFormatWritesValidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.log")
	cfg := config{LogFormat: "json", LogDestination: path, LogLevel: "info"}

	logger, closeLog := buildLoggerForTest(t, cfg)
	logger.Info("test message", "key", "val")
	closeLog()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading back log file: %v", err)
	}

	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("log line is not valid JSON: %v\ncontent: %s", err, data)
	}
	if record["msg"] != "test message" {
		t.Errorf("msg = %v, want %q", record["msg"], "test message")
	}
	if record["key"] != "val" {
		t.Errorf("key = %v, want %q", record["key"], "val")
	}
}

func TestBuildLogger_TextFormatIsLogfmtStyle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.log")
	cfg := config{LogDestination: path, LogLevel: "info"} // LogFormat left unset -> default

	logger, closeLog := buildLoggerForTest(t, cfg)
	logger.Info("test message", "key", "val")
	closeLog()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading back log file: %v", err)
	}

	if strings.HasPrefix(string(data), "{") {
		t.Errorf("default format looks like JSON, want logfmt-style: %s", data)
	}
	if !strings.Contains(string(data), `msg="test message"`) {
		t.Errorf("expected logfmt-style msg=\"test message\" in output, got: %s", data)
	}
}
