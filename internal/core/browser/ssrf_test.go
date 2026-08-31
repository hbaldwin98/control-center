package browser

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSSRFProxyDeniesPrivateConnect(t *testing.T) {
	p, err := startSSRFProxy()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	p.lookup = func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}
	p.dial = func(context.Context, net.IP, string) (net.Conn, error) {
		t.Fatal("must not dial a private address")
		return nil, errors.New("unreachable")
	}

	resp := connect(t, p.addr, "hello.test:443")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestSSRFProxyDeniesNon443(t *testing.T) {
	p, err := startSSRFProxy()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	p.lookup = func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("8.8.8.8")}, nil
	}
	p.dial = func(context.Context, net.IP, string) (net.Conn, error) {
		t.Fatal("must not dial a non-443 port")
		return nil, errors.New("unreachable")
	}

	resp := connect(t, p.addr, "hello.test:8443")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestSSRFProxyDeniesIPLiteralHost(t *testing.T) {
	p, err := startSSRFProxy()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	p.lookup = func(context.Context, string) ([]net.IP, error) {
		t.Fatal("must not resolve an IP literal")
		return nil, errors.New("unreachable")
	}

	resp := connect(t, p.addr, "8.8.8.8:443")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestSSRFProxyDialsCheckedPublicIP(t *testing.T) {
	p, err := startSSRFProxy()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })

	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	go func() {
		_, _ = io.Copy(io.Discard, server)
	}()

	var gotIP net.IP
	var gotPort string
	p.lookup = func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("8.8.8.8")}, nil
	}
	p.dial = func(_ context.Context, ip net.IP, port string) (net.Conn, error) {
		gotIP = ip
		gotPort = port
		return client, nil
	}

	resp := connect(t, p.addr, "hello.test:443")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if !gotIP.Equal(net.ParseIP("8.8.8.8")) || gotPort != "443" {
		t.Fatalf("dialed %v:%s", gotIP, gotPort)
	}
}

func TestSSRFProxyRejectsMixedPrivateDNS(t *testing.T) {
	p, err := startSSRFProxy()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	p.lookup = func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("10.0.0.1")}, nil
	}
	p.dial = func(context.Context, net.IP, string) (net.Conn, error) {
		t.Fatal("must not dial when any DNS result is private")
		return nil, errors.New("unreachable")
	}

	resp := connect(t, p.addr, "hello.test:443")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestCheckHostFailsClosedOnLookupError(t *testing.T) {
	p := &ssrfProxy{
		lookup: func(context.Context, string) ([]net.IP, error) {
			return nil, errors.New("nxdomain")
		},
	}
	if err := p.checkHost(context.Background(), "hello.test"); !errors.Is(err, ErrDenied) {
		t.Fatalf("lookup error: %v", err)
	}
}

func connect(t *testing.T, addr, hostport string) *http.Response {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	req := "CONNECT " + hostport + " HTTP/1.1\r\nHost: " + hostport + "\r\n\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestParseConnectHost(t *testing.T) {
	host, port, err := parseConnectHost("example.test:443")
	if err != nil || host != "example.test" || port != "443" {
		t.Fatalf("%q %q %v", host, port, err)
	}
	if _, _, err := parseConnectHost(""); err == nil {
		t.Fatal("empty")
	}
	if _, _, err := parseConnectHost("example.test"); err == nil {
		t.Fatal("missing port")
	}
}

func TestSSRFProxyRejectsPlainHTTP(t *testing.T) {
	p, err := startSSRFProxy()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })

	req, err := http.NewRequest(http.MethodGet, "http://"+p.addr+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "connect") {
		t.Fatalf("body %q", body)
	}
}
