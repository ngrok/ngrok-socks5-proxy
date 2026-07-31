package proxy

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ngrok/ngrok-socks5-proxy/allowlist"
)

func setupTestProxy(t *testing.T, patterns []string) (proxyAddr string, cleanup func()) {
	t.Helper()
	al, err := allowlist.Parse(patterns)
	if err != nil {
		t.Fatal(err)
	}

	srv := New(Config{Allowlist: al})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	go srv.Serve(ln)

	return ln.Addr().String(), func() {
		ln.Close()
		srv.Drain()
	}
}

func TestHTTPConnect_Allowed(t *testing.T) {
	// Start a target HTTP server
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello from target")
	}))
	defer target.Close()

	// Parse target host:port
	targetHost, targetPort, _ := net.SplitHostPort(target.Listener.Addr().String())

	// Start proxy allowing the target
	proxyAddr, cleanup := setupTestProxy(t, []string{targetHost})
	defer cleanup()

	// Connect to proxy and send CONNECT
	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT %s:%s HTTP/1.1\r\nHost: %s:%s\r\n\r\n", targetHost, targetPort, targetHost, targetPort)

	// Read response
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Now send an HTTP request through the tunnel
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s:%s\r\nConnection: close\r\n\r\n", targetHost, targetPort)
	resp, err = http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if string(body) != "hello from target" {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestHTTPConnect_Denied(t *testing.T) {
	proxyAddr, cleanup := setupTestProxy(t, []string{"allowed.example.com"})
	defer cleanup()

	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT denied.example.com:443 HTTP/1.1\r\nHost: denied.example.com:443\r\n\r\n")

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 403 {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestHTTPPlainProxy_Allowed(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "plain proxy works")
	}))
	defer target.Close()

	targetHost, _, _ := net.SplitHostPort(target.Listener.Addr().String())

	proxyAddr, cleanup := setupTestProxy(t, []string{targetHost})
	defer cleanup()

	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "GET %s/ HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n",
		target.URL, target.Listener.Addr().String())

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if string(body) != "plain proxy works" {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestSOCKS5_Allowed(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "socks5 works")
	}))
	defer target.Close()

	targetHost, targetPortStr, _ := net.SplitHostPort(target.Listener.Addr().String())

	proxyAddr, cleanup := setupTestProxy(t, []string{targetHost})
	defer cleanup()

	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// SOCKS5 handshake: version, 1 method, no-auth
	conn.Write([]byte{0x05, 0x01, 0x00})

	// Read method selection response
	methodResp := make([]byte, 2)
	if _, err := io.ReadFull(conn, methodResp); err != nil {
		t.Fatal(err)
	}
	if methodResp[0] != 0x05 || methodResp[1] != 0x00 {
		t.Fatalf("unexpected method response: %v", methodResp)
	}

	// Send CONNECT request with IPv4 address
	targetIP := net.ParseIP(targetHost).To4()
	var targetPort uint16
	fmt.Sscanf(targetPortStr, "%d", &targetPort)

	req := []byte{0x05, 0x01, 0x00, 0x01} // VER, CMD=CONNECT, RSV, ATYP=IPv4
	req = append(req, targetIP...)
	portBuf := make([]byte, 2)
	binary.BigEndian.PutUint16(portBuf, targetPort)
	req = append(req, portBuf...)
	conn.Write(req)

	// Read reply
	reply := make([]byte, 10) // 4 header + 4 addr + 2 port for IPv4
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	if reply[0] != 0x05 || reply[1] != 0x00 {
		t.Fatalf("SOCKS5 connect failed: reply=%v", reply)
	}

	// Send HTTP request through the SOCKS5 tunnel
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", target.Listener.Addr().String())

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if string(body) != "socks5 works" {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestSOCKS5_AllowedDomain(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "socks5 domain works")
	}))
	defer target.Close()

	targetHost, targetPortStr, _ := net.SplitHostPort(target.Listener.Addr().String())

	proxyAddr, cleanup := setupTestProxy(t, []string{targetHost})
	defer cleanup()

	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// SOCKS5 handshake
	conn.Write([]byte{0x05, 0x01, 0x00})
	methodResp := make([]byte, 2)
	if _, err := io.ReadFull(conn, methodResp); err != nil {
		t.Fatal(err)
	}
	if methodResp[0] != 0x05 || methodResp[1] != 0x00 {
		t.Fatalf("unexpected method response: %v", methodResp)
	}

	// Send CONNECT request with domain name (ATYP=0x03)
	var targetPort uint16
	fmt.Sscanf(targetPortStr, "%d", &targetPort)

	req := []byte{0x05, 0x01, 0x00, 0x03}    // VER, CMD=CONNECT, RSV, ATYP=DOMAIN
	req = append(req, byte(len(targetHost))) // domain length
	req = append(req, []byte(targetHost)...) // domain
	portBuf := make([]byte, 2)
	binary.BigEndian.PutUint16(portBuf, targetPort)
	req = append(req, portBuf...)
	conn.Write(req)

	// Read reply
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	if reply[0] != 0x05 || reply[1] != 0x00 {
		t.Fatalf("SOCKS5 connect failed: reply=%v", reply)
	}

	// Send HTTP request through the SOCKS5 tunnel
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", target.Listener.Addr().String())

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if string(body) != "socks5 domain works" {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestSOCKS5_Denied(t *testing.T) {
	proxyAddr, cleanup := setupTestProxy(t, []string{"allowed.example.com"})
	defer cleanup()

	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// SOCKS5 handshake
	conn.Write([]byte{0x05, 0x01, 0x00})
	methodResp := make([]byte, 2)
	io.ReadFull(conn, methodResp)

	// CONNECT to a domain not on the allowlist
	domain := "denied.example.com"
	req := []byte{0x05, 0x01, 0x00, 0x03} // VER, CMD=CONNECT, RSV, ATYP=DOMAIN
	req = append(req, byte(len(domain)))  // domain length
	req = append(req, []byte(domain)...)  // domain
	req = append(req, 0x01, 0xBB)         // port 443
	conn.Write(req)

	// Read reply - expect not allowed
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != socks5ReplyNotAllowed {
		t.Fatalf("expected not-allowed reply, got: 0x%02x", reply[1])
	}
}

func TestAutoDetect_UnknownProtocol(t *testing.T) {
	proxyAddr, cleanup := setupTestProxy(t, []string{"anything.local"})
	defer cleanup()

	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	// Send a byte that's neither SOCKS5 nor ASCII
	conn.Write([]byte{0xFF, 0x01, 0x02})
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	// Connection should be closed by the proxy
	buf := make([]byte, 100)
	_, err = conn.Read(buf)
	if err == nil {
		t.Fatal("expected connection to be closed")
	}
	conn.Close()
}
