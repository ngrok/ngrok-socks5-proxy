package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// handleHTTP handles HTTP proxy requests (CONNECT for HTTPS, plain proxy for HTTP).
func (s *Server) handleHTTP(conn net.Conn) {
	br := bufio.NewReader(conn)

	for {
		// Set a read deadline for the next request
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		req, err := http.ReadRequest(br)
		conn.SetReadDeadline(time.Time{})
		if err != nil {
			if err != io.EOF {
				s.log.Debug("error reading HTTP request", "remote", conn.RemoteAddr(), "error", err)
			}
			return
		}

		if req.Method == http.MethodConnect {
			s.handleHTTPConnect(conn, req)
			return // CONNECT takes over the connection
		}

		s.handleHTTPPlain(conn, req)
	}
}

// handleHTTPConnect handles the CONNECT method for HTTPS tunneling.
func (s *Server) handleHTTPConnect(conn net.Conn, req *http.Request) {
	host, port, err := parseHostPort(req.Host, "443")
	if err != nil {
		s.log.Warn("invalid CONNECT target", "target", req.Host, "error", err)
		writeHTTPError(conn, http.StatusBadRequest, "invalid target")
		return
	}

	s.log.Info("HTTP CONNECT", "remote", conn.RemoteAddr(), "target", net.JoinHostPort(host, port))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	target, err := s.dialTarget(ctx, host, port)
	if err != nil {
		s.log.Warn("CONNECT denied or failed",
			"remote", conn.RemoteAddr(),
			"target", net.JoinHostPort(host, port),
			"error", err,
		)
		writeHTTPError(conn, http.StatusForbidden, err.Error())
		return
	}
	defer target.Close()

	// Send 200 Connection Established
	_, err = fmt.Fprintf(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
	if err != nil {
		s.log.Warn("failed to send CONNECT response", "error", err)
		return
	}

	s.log.Info("CONNECT established",
		"remote", conn.RemoteAddr(),
		"target", net.JoinHostPort(host, port),
	)

	bridge(conn, target)
}

// handleHTTPPlain handles plain HTTP proxy requests (GET http://host/path).
func (s *Server) handleHTTPPlain(conn net.Conn, req *http.Request) {
	if req.URL.Host == "" {
		writeHTTPError(conn, http.StatusBadRequest, "missing host in proxy request")
		return
	}

	host, port, err := parseHostPort(req.URL.Host, "80")
	if err != nil {
		writeHTTPError(conn, http.StatusBadRequest, "invalid target host")
		return
	}

	s.log.Info("HTTP proxy", "remote", conn.RemoteAddr(), "method", req.Method, "target", req.URL.String())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	target, err := s.dialTarget(ctx, host, port)
	if err != nil {
		s.log.Warn("HTTP proxy denied or failed",
			"remote", conn.RemoteAddr(),
			"target", req.URL.Host,
			"error", err,
		)
		writeHTTPError(conn, http.StatusForbidden, err.Error())
		return
	}
	defer target.Close()

	// Remove proxy-specific headers
	req.Header.Del("Proxy-Connection")
	req.Header.Del("Proxy-Authorization")

	// Rewrite to relative URL for the upstream server
	req.RequestURI = req.URL.RequestURI()

	// Forward the request
	if err := req.Write(target); err != nil {
		s.log.Warn("failed to forward request", "error", err)
		return
	}

	// Read and forward the response
	resp, err := http.ReadResponse(bufio.NewReader(target), req)
	if err != nil {
		s.log.Warn("failed to read upstream response", "error", err)
		return
	}
	defer resp.Body.Close()

	if err := resp.Write(conn); err != nil {
		s.log.Debug("failed to write response to client", "error", err)
	}
}

// parseHostPort splits a host:port string, applying a default port if missing.
func parseHostPort(addr, defaultPort string) (string, string, error) {
	if addr == "" {
		return "", "", fmt.Errorf("empty address")
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// No port specified, use default
		if strings.Contains(addr, ":") {
			return "", "", err
		}
		return addr, defaultPort, nil
	}

	return host, port, nil
}

func writeHTTPError(conn net.Conn, status int, msg string) {
	resp := fmt.Sprintf("HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nConnection: close\r\n\r\n%s\n",
		status, http.StatusText(status), msg)
	conn.Write([]byte(resp))
}
