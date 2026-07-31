package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/ngrok/ngrok-socks5-proxy/allowlist"
)

// ErrNotAllowed is returned when a target is not in the allowlist.
var ErrNotAllowed = errors.New("not allowed")

// Config holds the proxy server configuration.
type Config struct {
	Allowlist *allowlist.Allowlist
	Logger    *slog.Logger
	Resolver  *net.Resolver // optional custom DNS resolver
}

// Server is an HTTP/SOCKS5 proxy server with hostname allowlisting.
type Server struct {
	allowlist *allowlist.Allowlist
	log       *slog.Logger
	resolver  *net.Resolver
	wg        sync.WaitGroup
}

// New creates a new proxy server.
func New(cfg Config) *Server {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	resolver := cfg.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return &Server{
		allowlist: cfg.Allowlist,
		log:       logger,
		resolver:  resolver,
	}
}

// Serve accepts connections from the listener and handles them.
// It blocks until the listener is closed. Active connections are tracked
// via the WaitGroup so Drain can wait for them to finish.
func (s *Server) Serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(conn)
		}()
	}
}

// Drain waits for all active connections to finish.
func (s *Server) Drain() {
	s.wg.Wait()
}

// handleConn detects the protocol and dispatches to the appropriate handler.
func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()

	// Read the first byte to detect protocol
	buf := make([]byte, 1)
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	n, err := conn.Read(buf)
	conn.SetReadDeadline(time.Time{}) // clear deadline
	if err != nil || n == 0 {
		s.log.Debug("failed to read first byte", "remote", conn.RemoteAddr(), "error", err)
		return
	}

	// Wrap connection so the first byte can be re-read by the handler
	peeked := &peekedConn{Conn: conn, first: buf[0]}

	switch {
	case buf[0] == 0x05:
		s.log.Debug("detected SOCKS5", "remote", conn.RemoteAddr())
		s.handleSOCKS5(peeked)
	case isASCII(buf[0]):
		s.log.Debug("detected HTTP", "remote", conn.RemoteAddr())
		s.handleHTTP(peeked)
	default:
		s.log.Warn("unsupported protocol",
			"remote", conn.RemoteAddr(),
			"byte", fmt.Sprintf("0x%02x", buf[0]),
		)
	}
}

// dialTarget resolves and connects to the target, enforcing the allowlist.
func (s *Server) dialTarget(ctx context.Context, host, port string) (net.Conn, error) {
	if !s.allowlist.IsAllowed(host, port) {
		return nil, fmt.Errorf("target %s:%s not in allowlist: %w", host, port, ErrNotAllowed)
	}

	addr := net.JoinHostPort(host, port)
	dialer := &net.Dialer{
		Timeout:  10 * time.Second,
		Resolver: s.resolver,
	}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", addr, err)
	}

	return conn, nil
}

// closeWriter is implemented by connections that support half-close.
type closeWriter interface {
	CloseWrite() error
}

// bridge copies data bidirectionally between two connections.
func bridge(left, right net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		io.Copy(right, left)
		// Signal to right that we're done writing
		if cw, ok := right.(closeWriter); ok {
			cw.CloseWrite()
		}
	}()

	go func() {
		defer wg.Done()
		io.Copy(left, right)
		if cw, ok := left.(closeWriter); ok {
			cw.CloseWrite()
		}
	}()

	wg.Wait()
}

// peekedConn wraps a net.Conn and prepends a single byte that was already read.
type peekedConn struct {
	net.Conn
	first    byte
	firstRead bool
}

func (c *peekedConn) Read(p []byte) (int, error) {
	if !c.firstRead {
		c.firstRead = true
		p[0] = c.first
		return 1, nil
	}
	return c.Conn.Read(p)
}

func (c *peekedConn) CloseWrite() error {
	if cw, ok := c.Conn.(closeWriter); ok {
		return cw.CloseWrite()
	}
	return nil
}

func isASCII(b byte) bool {
	return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z'
}
