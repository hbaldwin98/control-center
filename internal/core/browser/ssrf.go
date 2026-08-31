package browser

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ssrfProxy is a loopback HTTP CONNECT proxy. Chromium talks to it; it resolves the
// target, rejects private/loopback/link-local/CGNAT addresses, and dials only a
// checked public IP. That is the connect-time SSRF gate the Fake engine cannot
// exercise.
type ssrfProxy struct {
	lookup func(context.Context, string) ([]net.IP, error)
	dial   func(context.Context, net.IP, string) (net.Conn, error)

	mu     sync.Mutex
	srv    *http.Server
	addr   string
	closed bool
}

func startSSRFProxy() (*ssrfProxy, error) {
	p := &ssrfProxy{
		lookup: defaultLookup,
		dial:   defaultDial,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p.addr = ln.Addr().String()
	p.srv = &http.Server{
		Handler:           p,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = p.srv.Serve(ln) }()
	return p, nil
}

func (p *ssrfProxy) URL() string {
	return "http://" + p.addr
}

func (p *ssrfProxy) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	if p.srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return p.srv.Shutdown(ctx)
}

func (p *ssrfProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		http.Error(w, "connect only", http.StatusMethodNotAllowed)
		return
	}
	host, port, err := parseConnectHost(r.Host)
	if err != nil {
		http.Error(w, "denied", http.StatusForbidden)
		return
	}
	dest, err := p.dialChecked(r.Context(), host, port)
	if err != nil {
		http.Error(w, "denied", http.StatusForbidden)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = dest.Close()
		http.Error(w, "hijack", http.StatusInternalServerError)
		return
	}
	client, bufrw, err := hj.Hijack()
	if err != nil {
		_ = dest.Close()
		return
	}
	if _, err := bufrw.WriteString("HTTP/1.1 200 Connection Established\r\nContent-Length: 0\r\n\r\n"); err != nil {
		_ = dest.Close()
		_ = client.Close()
		return
	}
	if err := bufrw.Flush(); err != nil {
		_ = dest.Close()
		_ = client.Close()
		return
	}
	if n := bufrw.Reader.Buffered(); n > 0 {
		buf, _ := bufrw.Peek(n)
		if _, err := dest.Write(buf); err != nil {
			_ = dest.Close()
			_ = client.Close()
			return
		}
	}
	tunnel(client, dest)
}

func tunnel(client, dest net.Conn) {
	defer client.Close()
	defer dest.Close()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(dest, client)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, dest)
		done <- struct{}{}
	}()
	<-done
}

func (p *ssrfProxy) checkHost(ctx context.Context, host string) error {
	if host == "" {
		return fmt.Errorf("%w: address", ErrDenied)
	}
	if ip := net.ParseIP(host); ip != nil {
		return fmt.Errorf("%w: address", ErrDenied)
	}
	ips, err := p.lookup(ctx, host)
	if err != nil || len(ips) == 0 {
		return fmt.Errorf("%w: address", ErrDenied)
	}
	for _, ip := range ips {
		if err := checkResolvedIP(ip); err != nil {
			return err
		}
	}
	return nil
}

func (p *ssrfProxy) dialChecked(ctx context.Context, host, port string) (net.Conn, error) {
	if port != "443" {
		return nil, fmt.Errorf("%w: port", ErrDenied)
	}
	if err := p.checkHost(ctx, host); err != nil {
		return nil, err
	}
	ips, err := p.lookup(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("%w: address", ErrDenied)
	}
	var last error
	for _, ip := range ips {
		c, err := p.dial(ctx, ip, port)
		if err == nil {
			return c, nil
		}
		last = err
	}
	if last == nil {
		return nil, fmt.Errorf("%w: address", ErrDenied)
	}
	return nil, last
}

func parseConnectHost(raw string) (host, port string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("empty host")
	}
	host, port, err = net.SplitHostPort(raw)
	if err != nil {
		return "", "", err
	}
	if host == "" || port == "" {
		return "", "", fmt.Errorf("empty host")
	}
	return host, port, nil
}

func defaultLookup(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ips = append(ips, a.IP)
	}
	return ips, nil
}

func defaultDial(ctx context.Context, ip net.IP, port string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), port))
}
