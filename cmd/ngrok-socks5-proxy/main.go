package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/kardianos/service"
	"golang.ngrok.com/ngrok/v2"
	"gopkg.in/yaml.v3"

	"github.com/ngrok/ngrok-socks5-proxy/allowlist"
	"github.com/ngrok/ngrok-socks5-proxy/proxy"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

type config struct {
	Authtoken   string   `yaml:"authtoken"`
	URL         string   `yaml:"url"`
	Listen      string   `yaml:"listen"`
	Name        string   `yaml:"name"`
	Bindings    []string `yaml:"bindings"`
	DNS         string   `yaml:"dns"`
	LogLevel    string   `yaml:"log_level"`
	Allow       []string `yaml:"allow"`
	DialTimeout string   `yaml:"dial_timeout"`
}

// allowFlag implements flag.Value to support repeated and comma-separated --allow flags.
type allowFlag []string

func (a *allowFlag) String() string { return strings.Join(*a, ",") }
func (a *allowFlag) Set(val string) error {
	for _, v := range strings.Split(val, ",") {
		v = strings.TrimSpace(v)
		if v != "" {
			*a = append(*a, v)
		}
	}
	return nil
}

func main() {
	// Handle subcommands before flag parsing
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			fmt.Println(version)
			return
		case "config":
			if err := handleConfigCmd(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "pac":
			if err := handlePacCmd(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		case "service":
			if err := handleServiceCmd(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			return
		}
	}

	if !service.Interactive() {
		// Launched by the OS service manager (systemd/launchd/Windows SCM)
		// rather than a terminal. Route through the service.Interface so
		// Windows's control-callback protocol is satisfied; systemd/launchd
		// just fork/exec and stop via SIGTERM, which proxyService.Stop
		// handles the same way run() already does.
		runAsService()
		return
	}

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func handleConfigCmd(args []string) error {
	path := defaultConfigPath()

	// Ensure config exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := createDefaultConfig(path); err != nil {
			return fmt.Errorf("creating config: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Created default config at: %s\n", path)
	}

	sub := "edit"
	if len(args) > 0 {
		sub = args[0]
	}

	switch sub {
	case "edit":
		return openInEditor(path)
	case "path":
		fmt.Println(path)
		return nil
	default:
		return fmt.Errorf("unknown config command %q (use: edit, path)", sub)
	}
}

func handlePacCmd(args []string) error {
	fs := flag.NewFlagSet("pac", flag.ExitOnError)
	configFile := fs.String("config", "", "path to YAML config file")
	proxyAddr := fs.String("proxy", "", "proxy address for the PAC file (e.g., proxy.ishan.test:443)")
	fs.Parse(args)

	// Load config to get allow patterns
	cfgPath := *configFile
	if cfgPath == "" {
		cfgPath = defaultConfigPath()
	}

	cfg := config{}
	if data, err := os.ReadFile(cfgPath); err == nil {
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("parsing config: %w", err)
		}
	}

	if len(cfg.Allow) == 0 {
		return fmt.Errorf("no allow patterns found in config")
	}

	// Determine proxy address
	proxy := *proxyAddr
	if proxy == "" {
		if cfg.URL != "" {
			proxy = strings.TrimPrefix(cfg.URL, "tcp://")
		} else if cfg.Listen != "" {
			proxy = cfg.Listen
		} else {
			return fmt.Errorf("proxy address required: use --proxy or set url/listen in config")
		}
	}

	fmt.Print(generatePAC(cfg.Allow, proxy))
	return nil
}

func generatePAC(patterns []string, proxyAddr string) string {
	var conditions []string
	for _, p := range patterns {
		// Strip port from pattern — PAC matches on hostname only
		host := p
		if h, _, err := net.SplitHostPort(p); err == nil {
			host = h
		}

		if strings.HasPrefix(host, "*.") {
			conditions = append(conditions, fmt.Sprintf(`    if (shExpMatch(host, %q)) return proxy;`, host))
		} else {
			conditions = append(conditions, fmt.Sprintf(`    if (host === %q) return proxy;`, host))
		}
	}

	return fmt.Sprintf(`function FindProxyForURL(url, host) {
  var proxy = "SOCKS5 %s";
%s
  return "DIRECT";
}
`, proxyAddr, strings.Join(conditions, "\n"))
}

func openInEditor(path string) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		switch runtime.GOOS {
		case "darwin":
			return exec.Command("open", "-t", path).Run()
		case "windows":
			return exec.Command("notepad", path).Run()
		default:
			editor = "vi"
		}
	}
	cmd := exec.Command(editor, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// proxyFlags holds the values of every flag shared between the normal `run()`
// entrypoint and `service install` (which validates the same flags up front
// and bakes the raw argv into the installed service's arguments).
type proxyFlags struct {
	configFile  string
	authtoken   string
	url         string
	listen      string
	name        string
	bindings    allowFlag
	dns         string
	logLevel    string
	allow       allowFlag
	showVersion bool
	dialTimeout string
}

// registerProxyFlags registers the proxy's flags against fs, so the same
// definitions can be reused by both flag.CommandLine (run()) and a scratch
// FlagSet (service install).
func registerProxyFlags(fs *flag.FlagSet) *proxyFlags {
	f := &proxyFlags{}
	fs.BoolVar(&f.showVersion, "version", false, "print version and exit")
	fs.StringVar(&f.configFile, "config", "", "path to YAML config file")
	fs.StringVar(&f.authtoken, "authtoken", "", "ngrok authtoken (or set NGROK_AUTHTOKEN)")
	fs.StringVar(&f.url, "url", "", "endpoint URL (e.g., tcp://1.tcp.ngrok.io:12345 or tcp://my-proxy.internal:8080)")
	fs.StringVar(&f.listen, "listen", "", "local address to listen on without ngrok (e.g., localhost:8080)")
	fs.StringVar(&f.name, "name", "", "label for the endpoint in the ngrok dashboard")
	fs.Var(&f.bindings, "bindings", "endpoint bindings (e.g., internal, k8s/my-cluster)")
	fs.StringVar(&f.dns, "dns", "", "custom DNS server (e.g., 10.0.0.53:53)")
	fs.StringVar(&f.logLevel, "log-level", "", "log level: debug, info, warn, error")
	fs.Var(&f.allow, "allow", "hostname pattern to allow (repeatable or comma-separated)")
	fs.StringVar(&f.dialTimeout, "dial-timeout", "", "timeout for connecting to targets (default 10s, e.g. 15s, 500ms)")
	return f
}

// buildConfig merges CLI flags over the config file and validates the
// result, mirroring exactly what `run()` did inline before this was
// extracted so it could also be used by `service install` (to validate
// up front) and proxyService.Start (to build the config when launched by
// the OS service manager).
func buildConfig(f *proxyFlags) (cfg config, dialTimeoutDuration time.Duration, err error) {
	configFile := f.configFile
	if configFile == "" {
		configFile = defaultConfigPath()
	}

	// Load config file, or create default if it doesn't exist
	cfg = config{LogLevel: "info"}
	if data, readErr := os.ReadFile(configFile); readErr == nil {
		if unmarshalErr := yaml.Unmarshal(data, &cfg); unmarshalErr != nil {
			return config{}, 0, fmt.Errorf("parsing config file %s: %w", configFile, unmarshalErr)
		}
	} else if os.IsNotExist(readErr) && configFile == defaultConfigPath() {
		// Auto-create default config on first run
		if createErr := createDefaultConfig(configFile); createErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not create default config: %v\n", createErr)
		} else {
			fmt.Fprintf(os.Stderr, "Created default config at: %s\n", configFile)
		}
	} else if !os.IsNotExist(readErr) {
		return config{}, 0, fmt.Errorf("reading config file %s: %w", configFile, readErr)
	}

	// CLI flags override config file values
	if f.url != "" && f.listen != "" {
		return config{}, 0, fmt.Errorf("--listen and --url are mutually exclusive")
	}
	if f.authtoken != "" {
		cfg.Authtoken = f.authtoken
	}
	if f.url != "" {
		cfg.URL = f.url
		cfg.Listen = "" // --url and --listen are mutually exclusive
	}
	if f.listen != "" {
		cfg.Listen = f.listen
		cfg.URL = "" // --listen and --url are mutually exclusive
	}
	if f.name != "" {
		cfg.Name = f.name
	}
	if len(f.bindings) > 0 {
		cfg.Bindings = f.bindings
	}
	if f.dns != "" {
		cfg.DNS = f.dns
	}
	if f.logLevel != "" {
		cfg.LogLevel = f.logLevel
	}
	if f.dialTimeout != "" {
		cfg.DialTimeout = f.dialTimeout
	}
	// Merge allow flags with config file allows
	cfg.Allow = append(cfg.Allow, f.allow...)

	dialTimeoutDuration = 0 // 0 means "use proxy's default"
	if cfg.DialTimeout != "" {
		d, parseErr := time.ParseDuration(cfg.DialTimeout)
		if parseErr != nil {
			return config{}, 0, fmt.Errorf("invalid dial-timeout %q: %w", cfg.DialTimeout, parseErr)
		}
		dialTimeoutDuration = d
	}

	// Validate authtoken requirement (not needed in local listen mode)
	if cfg.Listen == "" {
		if cfg.Authtoken == "" {
			cfg.Authtoken = os.Getenv("NGROK_AUTHTOKEN")
		}
		if cfg.Authtoken == "" {
			return config{}, 0, fmt.Errorf("authtoken is required (use --authtoken, config file, or NGROK_AUTHTOKEN env var)")
		}
	}

	if cfg.Listen != "" && cfg.URL != "" {
		return config{}, 0, fmt.Errorf("--listen and --url are mutually exclusive")
	}

	if len(cfg.Allow) == 0 {
		return config{}, 0, fmt.Errorf("at least one --allow pattern is required")
	}

	return cfg, dialTimeoutDuration, nil
}

// buildResolver constructs the DNS resolver used to reach allowlisted
// targets. See the PreferGo comment below for why this isn't just
// net.DefaultResolver.
func buildResolver(cfg config, logger *slog.Logger) *net.Resolver {
	// Use Go's pure-Go DNS resolver rather than the OS-native resolver. On
	// Darwin in particular, the OS-native resolver routes ".local"-suffixed
	// hostnames (a common internal-domain convention, e.g. "*.corp.local")
	// through mDNS/Bonjour, adding several seconds of latency per lookup
	// even when the name is already present in /etc/hosts. PreferGo skips
	// that path entirely and resolves such names near-instantly.
	resolver := &net.Resolver{PreferGo: true}
	if cfg.DNS != "" {
		resolver.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{}
			return d.DialContext(ctx, "udp", cfg.DNS)
		}
		logger.Info("using custom DNS server", "dns", cfg.DNS)
	}
	return resolver
}

// createListener creates the proxy's listener, either a local TCP listener
// (--listen) or an ngrok-managed TCP endpoint.
func createListener(ctx context.Context, cfg config, logger *slog.Logger) (net.Listener, error) {
	if cfg.Listen != "" {
		// Local mode: listen directly on a local address
		listener, err := net.Listen("tcp", cfg.Listen)
		if err != nil {
			return nil, fmt.Errorf("listening on %s: %w", cfg.Listen, err)
		}
		logger.Info("proxy listening locally", "addr", cfg.Listen)
		fmt.Fprintf(os.Stderr, "\n  Forward proxy available at: %s\n\n", cfg.Listen)
		return listener, nil
	}

	// ngrok mode: create a TCP endpoint via the ngrok SDK
	agent, err := ngrok.NewAgent(ngrok.WithAuthtoken(cfg.Authtoken))
	if err != nil {
		return nil, fmt.Errorf("creating ngrok agent: %w", err)
	}

	logger.Info("connecting to ngrok...")
	if err := agent.Connect(ctx); err != nil {
		return nil, fmt.Errorf("connecting to ngrok: %w", err)
	}

	endpointOpts := []ngrok.EndpointOption{}
	if cfg.URL != "" {
		endpointOpts = append(endpointOpts, ngrok.WithURL(cfg.URL))
	} else {
		endpointOpts = append(endpointOpts, ngrok.WithURL("tcp://"))
	}
	if cfg.Name != "" {
		endpointOpts = append(endpointOpts, ngrok.WithName(cfg.Name))
	}
	if len(cfg.Bindings) > 0 {
		endpointOpts = append(endpointOpts, ngrok.WithBindings(cfg.Bindings...))
	}

	ln, err := agent.Listen(ctx, endpointOpts...)
	if err != nil {
		return nil, fmt.Errorf("creating TCP endpoint: %w", err)
	}
	logger.Info("proxy endpoint online", "url", ln.URL(), "name", cfg.Name)
	fmt.Fprintf(os.Stderr, "\n  Forward proxy available at: %s\n\n", ln.URL())
	return ln, nil
}

func run() error {
	f := registerProxyFlags(flag.CommandLine)
	flag.Parse()

	if f.showVersion {
		fmt.Println(version)
		return nil
	}

	cfg, dialTimeoutDuration, err := buildConfig(f)
	if err != nil {
		return err
	}

	// Set up logger
	level := parseLogLevel(cfg.LogLevel)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	// Parse allowlist
	al, err := allowlist.Parse(cfg.Allow)
	if err != nil {
		return fmt.Errorf("invalid allowlist: %w", err)
	}
	logger.Info("allowlist configured", "patterns", cfg.Allow)

	resolver := buildResolver(cfg, logger)

	// Set up context with signal handling for graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	listener, err := createListener(ctx, cfg, logger)
	if err != nil {
		return err
	}

	// Create and start proxy server
	srv := proxy.New(proxy.Config{
		Allowlist:   al,
		Logger:      logger,
		Resolver:    resolver,
		DialTimeout: dialTimeoutDuration,
	})

	// Run proxy in a goroutine so we can handle shutdown
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(listener)
	}()

	// Wait for shutdown signal or proxy error
	select {
	case <-ctx.Done():
		logger.Info("shutting down...")
		listener.Close()
		srv.Drain()
		logger.Info("shutdown complete")
		return nil
	case err := <-errCh:
		if ctx.Err() != nil {
			// Shutdown was requested, ignore the listener close error
			srv.Drain()
			logger.Info("shutdown complete")
			return nil
		}
		return fmt.Errorf("proxy server error: %w", err)
	}
}

const defaultConfigTemplate = `# ngrok-socks5-proxy configuration
# Documentation: see DESIGN.md

# ngrok authtoken (or set NGROK_AUTHTOKEN env var)
# authtoken: ""

# Endpoint URL (omit for ephemeral TCP)
# Public:     tcp://1.tcp.ngrok.io:12345
# Internal:   tcp://my-proxy.internal:8080
# Kubernetes: tcp://my-proxy.namespace:8080
# url: ""

# Local listen mode (no ngrok, mutually exclusive with url)
# listen: "localhost:8080"

# Label for the endpoint in the ngrok dashboard
# name: "acme-corp-proxy"

# Endpoint bindings: public, internal, or kubernetes
# bindings:
#   - "public"

# Custom DNS server for resolving internal hostnames
# dns: "10.0.0.53:53"

# Timeout for connecting to targets (e.g., "15s", "500ms")
# dial_timeout: "10s"

# Log level: debug, info, warn, error
log_level: "info"

# Hostnames the proxy is allowed to connect to (required)
# Supports exact match and wildcard subdomains (*.domain.tld)
allow:
  # - "*.corp.local"
  # - "sso.partner.com"
  # - "db.internal:5432"
`

func defaultConfigPath() string {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(configDir, "ngrok-socks5-proxy", "config.yaml")
}

func createDefaultConfig(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(defaultConfigTemplate), 0o600)
}

func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
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
