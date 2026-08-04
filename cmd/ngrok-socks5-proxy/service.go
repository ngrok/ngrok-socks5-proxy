package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"

	"github.com/kardianos/service"

	"github.com/ngrok/ngrok-socks5-proxy/allowlist"
	"github.com/ngrok/ngrok-socks5-proxy/proxy"
)

const serviceName = "ngrok-socks5-proxy"

// handleServiceCmd dispatches `service install|uninstall|start|stop|restart|status`.
func handleServiceCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: ngrok-socks5-proxy service <install|uninstall|start|stop|restart|status> [flags...]")
	}

	action, rest := args[0], args[1:]
	switch action {
	case "install":
		return serviceInstall(rest)
	case "uninstall", "start", "stop", "restart":
		return serviceControl(action)
	case "status":
		return serviceStatus()
	default:
		return fmt.Errorf("unknown service command %q (use: install, uninstall, start, stop, restart, status)", action)
	}
}

// serviceInstall registers the proxy as an OS service (systemd on Linux,
// launchd on macOS, Windows Service on Windows), running with the given
// flags every time the OS starts it.
func serviceInstall(args []string) error {
	if !isElevated() {
		return fmt.Errorf("service install requires elevated privileges (re-run with sudo, or as Administrator on Windows)")
	}

	// Validate the flags up front so a typo surfaces now, not on the first
	// crash-loop after the service manager starts it.
	fs := flag.NewFlagSet("service install", flag.ContinueOnError)
	f := registerProxyFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, _, err := buildConfig(f)
	if err != nil {
		return fmt.Errorf("validating flags: %w", err)
	}
	if _, err := buildLogger(cfg); err != nil {
		return fmt.Errorf("validating flags: %w", err)
	}

	svcConfig := &service.Config{
		Name:        serviceName,
		DisplayName: "ngrok SOCKS5 Proxy",
		Description: "SOCKS5/HTTP CONNECT forward proxy with ngrok integration",
		Arguments:   args,
	}

	// If --authtoken wasn't passed but NGROK_AUTHTOKEN is set in this
	// environment, forward it via EnvVars rather than baking it into
	// Arguments — the installed service won't inherit this shell's
	// environment, but putting a secret directly on the command line
	// makes it visible to anything that can list processes (ps, Task
	// Manager) and persists it verbatim in the generated unit/plist file.
	if f.authtoken == "" {
		if tok := os.Getenv("NGROK_AUTHTOKEN"); tok != "" {
			svcConfig.EnvVars = map[string]string{"NGROK_AUTHTOKEN": tok}
		}
	}

	s, err := service.New(&proxyService{}, svcConfig)
	if err != nil {
		return fmt.Errorf("creating service: %w", err)
	}
	if err := s.Install(); err != nil {
		return fmt.Errorf("installing service: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Service %q installed. Start it with: %s service start\n", serviceName, os.Args[0])
	return nil
}

// serviceControl handles uninstall/start/stop/restart against the
// already-installed service.
func serviceControl(action string) error {
	if !isElevated() {
		return fmt.Errorf("service %s requires elevated privileges (re-run with sudo, or as Administrator on Windows)", action)
	}

	s, err := service.New(&proxyService{}, &service.Config{Name: serviceName})
	if err != nil {
		return fmt.Errorf("creating service: %w", err)
	}
	if err := service.Control(s, action); err != nil {
		return fmt.Errorf("%s service: %w", action, err)
	}

	fmt.Fprintf(os.Stderr, "service %s: %s\n", serviceName, action)
	return nil
}

// serviceStatus reports whether the service is installed and running.
// Read-only, so no elevation check.
func serviceStatus() error {
	s, err := service.New(&proxyService{}, &service.Config{Name: serviceName})
	if err != nil {
		return fmt.Errorf("creating service: %w", err)
	}

	status, err := s.Status()
	if err != nil {
		return fmt.Errorf("checking status: %w", err)
	}

	switch status {
	case service.StatusRunning:
		fmt.Println("running")
	case service.StatusStopped:
		fmt.Println("stopped")
	default:
		fmt.Println("unknown (not installed, or status could not be determined)")
	}
	return nil
}

// runAsService is invoked from main() when the process detects it was
// launched by the OS service manager rather than a terminal. s.Run() blocks:
// it calls proxyService.Start, waits for the OS to signal a stop (SIGTERM
// under systemd/launchd, or the Windows SCM stop callback), then calls
// proxyService.Stop before returning.
func runAsService() {
	s, err := service.New(&proxyService{}, &service.Config{Name: serviceName})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: creating service: %v\n", err)
		os.Exit(1)
	}
	if err := s.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// proxyService implements service.Interface, wrapping the same
// startup/shutdown logic run() uses so the OS-managed and interactive
// code paths stay in sync.
type proxyService struct {
	logger   *slog.Logger
	cancel   context.CancelFunc
	listener net.Listener
	srv      *proxy.Server
	done     chan struct{}
}

// Start must not block — per service.Interface, it should return within a
// few seconds. os.Args[1:] here is exactly the Config.Arguments baked in at
// install time, since that's what the OS re-executes this binary with.
func (p *proxyService) Start(s service.Service) error {
	fs := flag.NewFlagSet("service", flag.ContinueOnError)
	f := registerProxyFlags(fs)
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}

	cfg, dialTimeout, err := buildConfig(f)
	if err != nil {
		return err
	}

	p.logger, err = buildLogger(cfg)
	if err != nil {
		return err
	}

	al, err := allowlist.Parse(cfg.Allow)
	if err != nil {
		return fmt.Errorf("invalid allowlist: %w", err)
	}
	p.logger.Info("allowlist configured", "patterns", cfg.Allow)

	resolver := buildResolver(cfg, p.logger)

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	listener, err := createListener(ctx, cfg, p.logger)
	if err != nil {
		cancel()
		return err
	}
	p.listener = listener

	p.srv = proxy.New(proxy.Config{
		Allowlist:   al,
		Logger:      p.logger,
		Resolver:    resolver,
		DialTimeout: dialTimeout,
	})

	p.done = make(chan struct{})
	go func() {
		defer close(p.done)
		if err := p.srv.Serve(p.listener); err != nil && ctx.Err() == nil {
			p.logger.Error("proxy server error", "error", err)
		}
	}()

	return nil
}

// Stop mirrors run()'s shutdown block: close the listener, drain in-flight
// connections, and wait for the serve goroutine to actually finish before
// returning (Stop should not take more than a few seconds, per the
// interface contract — Drain() only blocks on connections already in
// flight, not new ones, since the listener is closed first).
func (p *proxyService) Stop(s service.Service) error {
	if p.cancel != nil {
		p.cancel()
	}
	if p.listener != nil {
		p.listener.Close()
	}
	if p.srv != nil {
		p.srv.Drain()
	}
	if p.done != nil {
		<-p.done
	}
	if p.logger != nil {
		p.logger.Info("shutdown complete")
	}
	return nil
}
