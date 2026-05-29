package proxy

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// SOCKS5 constants (RFC 1928)
const (
	socks5Version = 0x05

	// Auth methods
	socks5AuthNone = 0x00
	socks5AuthFail = 0xFF

	// Commands
	socks5CmdConnect = 0x01

	// Address types
	socks5AddrIPv4   = 0x01
	socks5AddrDomain = 0x03
	socks5AddrIPv6   = 0x04

	// Reply codes
	socks5ReplySuccess         = 0x00
	socks5ReplyGeneralFailure  = 0x01
	socks5ReplyNotAllowed      = 0x02
	socks5ReplyNetUnreachable  = 0x03
	socks5ReplyHostUnreachable = 0x04
	socks5ReplyConnRefused     = 0x05
	socks5ReplyCmdNotSupported = 0x07
	socks5ReplyAddrNotSupported = 0x08
)

// handleSOCKS5 handles a SOCKS5 connection per RFC 1928.
func (s *Server) handleSOCKS5(conn net.Conn) {
	// Step 1: Version/method negotiation
	// Client sends: VER | NMETHODS | METHODS...
	// We already peeked the version byte (0x05), but the peekedConn will replay it
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil {
		s.log.Warn("SOCKS5: failed to read header", "remote", conn.RemoteAddr(), "error", err)
		return
	}

	if header[0] != socks5Version {
		s.log.Warn("SOCKS5: unexpected version", "remote", conn.RemoteAddr(), "version", header[0])
		return
	}

	nMethods := int(header[1])
	methods := make([]byte, nMethods)
	if _, err := io.ReadFull(conn, methods); err != nil {
		s.log.Warn("SOCKS5: failed to read methods", "remote", conn.RemoteAddr(), "error", err)
		return
	}

	// Check for no-auth method
	hasNoAuth := false
	for _, m := range methods {
		if m == socks5AuthNone {
			hasNoAuth = true
			break
		}
	}

	if !hasNoAuth {
		// Reject: no acceptable method
		conn.Write([]byte{socks5Version, socks5AuthFail})
		s.log.Warn("SOCKS5: no acceptable auth method", "remote", conn.RemoteAddr())
		return
	}

	// Accept no-auth
	if _, err := conn.Write([]byte{socks5Version, socks5AuthNone}); err != nil {
		return
	}

	// Step 2: Read the request
	// VER | CMD | RSV | ATYP | DST.ADDR | DST.PORT
	reqHeader := make([]byte, 4)
	if _, err := io.ReadFull(conn, reqHeader); err != nil {
		s.log.Debug("SOCKS5: failed to read request", "remote", conn.RemoteAddr(), "error", err)
		return
	}

	if reqHeader[0] != socks5Version {
		s.log.Warn("SOCKS5: unexpected version in request", "remote", conn.RemoteAddr())
		return
	}

	cmd := reqHeader[1]
	addrType := reqHeader[3]

	if cmd != socks5CmdConnect {
		s.log.Warn("SOCKS5: unsupported command", "remote", conn.RemoteAddr(), "cmd", cmd)
		s.socks5Reply(conn, socks5ReplyCmdNotSupported)
		return
	}

	// Read target address
	host, err := s.readSOCKS5Addr(conn, addrType)
	if err != nil {
		s.log.Warn("SOCKS5: failed to read address", "remote", conn.RemoteAddr(), "error", err)
		s.socks5Reply(conn, socks5ReplyAddrNotSupported)
		return
	}

	// Read target port (2 bytes, big-endian)
	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBuf); err != nil {
		s.log.Warn("SOCKS5: failed to read port", "remote", conn.RemoteAddr(), "error", err)
		return
	}
	port := strconv.Itoa(int(binary.BigEndian.Uint16(portBuf)))

	s.log.Info("SOCKS5 CONNECT", "remote", conn.RemoteAddr(), "target", net.JoinHostPort(host, port))

	// Dial the target
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	target, err := s.dialTarget(ctx, host, port)
	if err != nil {
		s.log.Warn("SOCKS5 denied or failed",
			"remote", conn.RemoteAddr(),
			"target", net.JoinHostPort(host, port),
			"error", err,
		)
		var replyCode byte
		switch {
		case errors.Is(err, ErrNotAllowed):
			replyCode = socks5ReplyNotAllowed
		case strings.Contains(err.Error(), "failed to connect"):
			replyCode = socks5ReplyConnRefused
		default:
			replyCode = socks5ReplyGeneralFailure
		}
		s.socks5Reply(conn, replyCode)
		return
	}
	defer target.Close()

	// Send success reply
	s.socks5Reply(conn, socks5ReplySuccess)

	s.log.Info("SOCKS5 established",
		"remote", conn.RemoteAddr(),
		"target", net.JoinHostPort(host, port),
	)

	bridge(conn, target)
}

// readSOCKS5Addr reads the target address based on address type.
func (s *Server) readSOCKS5Addr(conn net.Conn, addrType byte) (string, error) {
	switch addrType {
	case socks5AddrIPv4:
		addr := make([]byte, 4)
		if _, err := io.ReadFull(conn, addr); err != nil {
			return "", err
		}
		return net.IP(addr).String(), nil

	case socks5AddrDomain:
		// First byte is the length of the domain name
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			return "", err
		}
		domain := make([]byte, lenBuf[0])
		if _, err := io.ReadFull(conn, domain); err != nil {
			return "", err
		}
		return string(domain), nil

	case socks5AddrIPv6:
		addr := make([]byte, 16)
		if _, err := io.ReadFull(conn, addr); err != nil {
			return "", err
		}
		return net.IP(addr).String(), nil

	default:
		return "", fmt.Errorf("unsupported address type: 0x%02x", addrType)
	}
}

// socks5Reply sends a SOCKS5 reply with the given status code.
// Uses 0.0.0.0:0 as the bind address since we don't need to report it.
func (s *Server) socks5Reply(conn net.Conn, status byte) {
	// VER | REP | RSV | ATYP | BND.ADDR (4 bytes IPv4) | BND.PORT (2 bytes)
	reply := []byte{
		socks5Version, status, 0x00, socks5AddrIPv4,
		0, 0, 0, 0, // 0.0.0.0
		0, 0, // port 0
	}
	conn.Write(reply)
}
