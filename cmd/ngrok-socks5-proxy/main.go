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

	"golang.ngrok.com/ngrok/v2"
	"gopkg.in/yaml.v3"

	"github.com/ngrok/ngrok-socks5-proxy/allowlist"
	"github.com/ngrok/ngrok-socks5-proxy/proxy"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

type config struct {
	Authtoken string   `yaml:"authtoken"`
	URL       string   `yaml:"url"`
	Listen    string   `yaml:"listen"`
	Name      string   `yaml:"name"`
	Bindings  []string `yaml:"bindings"`
	DNS       string   `yaml:"dns"`
	LogLevel  string   `yaml:"log_level"`
	Allow     []string `yaml:"allow"`
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
		}
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

func run() error {
	var (
		configFile string
		authtoken  string
		urlFlag    string
		listen     string
		name       string
		bindings   allowFlag
		dns        string
		logLevel   string
		allows     allowFlag
		showVer    bool
	)

	flag.BoolVar(&showVer, "version", false, "print version and exit")
	flag.StringVar(&configFile, "config", "", "path to YAML config file")
	flag.StringVar(&authtoken, "authtoken", "", "ngrok authtoken (or set NGROK_AUTHTOKEN)")
	flag.StringVar(&urlFlag, "url", "", "endpoint URL (e.g., tcp://1.tcp.ngrok.io:12345 or tcp://my-proxy.internal:8080)")
	flag.StringVar(&listen, "listen", "", "local address to listen on without ngrok (e.g., localhost:8080)")
	flag.StringVar(&name, "name", "", "label for the endpoint in the ngrok dashboard")
	flag.Var(&bindings, "bindings", "endpoint bindings (e.g., internal, k8s/my-cluster)")
	flag.StringVar(&dns, "dns", "", "custom DNS server (e.g., 10.0.0.53:53)")
	flag.StringVar(&logLevel, "log-level", "", "log level: debug, info, warn, error")
	flag.Var(&allows, "allow", "hostname pattern to allow (repeatable or comma-separated)")
	flag.Parse()

	if showVer {
		fmt.Println(version)
		return nil
	}

	// Resolve config file path
	if configFile == "" {
		configFile = defaultConfigPath()
	}

	// Load config file, or create default if it doesn't exist
	cfg := config{LogLevel: "info"}
	if data, err := os.ReadFile(configFile); err == nil {
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("parsing config file %s: %w", configFile, err)
		}
	} else if os.IsNotExist(err) && configFile == defaultConfigPath() {
		// Auto-create default config on first run
		if err := createDefaultConfig(configFile); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not create default config: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "Created default config at: %s\n", configFile)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("reading config file %s: %w", configFile, err)
	}

	// CLI flags override config file values
	if urlFlag != "" && listen != "" {
		return fmt.Errorf("--listen and --url are mutually exclusive")
	}
	if authtoken != "" {
		cfg.Authtoken = authtoken
	}
	if urlFlag != "" {
		cfg.URL = urlFlag
		cfg.Listen = "" // --url and --listen are mutually exclusive
	}
	if listen != "" {
		cfg.Listen = listen
		cfg.URL = "" // --listen and --url are mutually exclusive
	}
	if name != "" {
		cfg.Name = name
	}
	if len(bindings) > 0 {
		cfg.Bindings = bindings
	}
	if dns != "" {
		cfg.DNS = dns
	}
	if logLevel != "" {
		cfg.LogLevel = logLevel
	}
	// Merge allow flags with config file allows
	cfg.Allow = append(cfg.Allow, allows...)

	// Validate authtoken requirement (not needed in local listen mode)
	if cfg.Listen == "" {
		if cfg.Authtoken == "" {
			cfg.Authtoken = os.Getenv("NGROK_AUTHTOKEN")
		}
		if cfg.Authtoken == "" {
			return fmt.Errorf("authtoken is required (use --authtoken, config file, or NGROK_AUTHTOKEN env var)")
		}
	}

	if cfg.Listen != "" && cfg.URL != "" {
		return fmt.Errorf("--listen and --url are mutually exclusive")
	}

	if len(cfg.Allow) == 0 {
		return fmt.Errorf("at least one --allow pattern is required")
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

	// Set up custom DNS resolver if specified
	var resolver *net.Resolver
	if cfg.DNS != "" {
		resolver = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{}
				return d.DialContext(ctx, "udp", cfg.DNS)
			},
		}
		logger.Info("using custom DNS server", "dns", cfg.DNS)
	}

	// Set up context with signal handling for graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Create listener — either local or via ngrok
	var listener net.Listener
	if cfg.Listen != "" {
		// Local mode: listen directly on a local address
		listener, err = net.Listen("tcp", cfg.Listen)
		if err != nil {
			return fmt.Errorf("listening on %s: %w", cfg.Listen, err)
		}
		logger.Info("proxy listening locally", "addr", cfg.Listen)
		fmt.Fprintf(os.Stderr, "\n  Forward proxy available at: %s\n\n", cfg.Listen)
	} else {
		// ngrok mode: create a TCP endpoint via the ngrok SDK
		agent, err := ngrok.NewAgent(ngrok.WithAuthtoken(cfg.Authtoken))
		if err != nil {
			return fmt.Errorf("creating ngrok agent: %w", err)
		}

		logger.Info("connecting to ngrok...")
		if err := agent.Connect(ctx); err != nil {
			return fmt.Errorf("connecting to ngrok: %w", err)
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
			return fmt.Errorf("creating TCP endpoint: %w", err)
		}
		listener = ln
		logger.Info("proxy endpoint online", "url", ln.URL(), "name", cfg.Name)
		fmt.Fprintf(os.Stderr, "\n  Forward proxy available at: %s\n\n", ln.URL())
	}

	// Create and start proxy server
	srv := proxy.New(proxy.Config{
		Allowlist: al,
		Logger:    logger,
		Resolver:  resolver,
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
