package browser

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hbaldwin98/control-center/internal/core/policy"
)

type readerFunc func(context.Context, string) (string, error)

func (f readerFunc) Extract(ctx context.Context, html string) (string, error) {
	return f(ctx, html)
}

func TestReadRendersThenExtractsWithoutGivingReaderURL(t *testing.T) {
	h := newHarness(t, map[string]http.Handler{
		"shop.test": HTMLHandler(`<html><body><h1>Model X</h1><p>Sold for $129.</p></body></html>`),
	})
	h.svc.opts.Reader = readerFunc(func(_ context.Context, html string) (string, error) {
		if !strings.Contains(html, "Sold for $129") {
			t.Fatalf("reader received %q", html)
		}
		return "Model X sold for $129.", nil
	})
	doc, err := h.svc.Read(h.ctxHello(), OpenOptions{AllowedHosts: []string{"shop.test"}}, "https://shop.test/item")
	if err != nil {
		t.Fatal(err)
	}
	if doc.URL != "https://shop.test/item" || doc.Content != "Model X sold for $129." {
		t.Fatalf("document = %+v", doc)
	}
}

func TestReadChecksPolicyBeforeReaderAvailability(t *testing.T) {
	h := newHarness(t, map[string]http.Handler{"shop.test": HTMLHandler("<p>x</p>")})
	if err := h.pol.Disable(h.ctx, "hello", "test", "stop"); err != nil {
		t.Fatal(err)
	}
	_, err := h.svc.Read(h.ctxHello(), OpenOptions{AllowedHosts: []string{"shop.test"}}, "https://shop.test/item")
	if !errors.Is(err, policy.ErrPluginDisabled) {
		t.Fatalf("read after disable = %v", err)
	}
}

func TestReadRequiresConfiguredReader(t *testing.T) {
	h := newHarness(t, map[string]http.Handler{"shop.test": HTMLHandler("<p>x</p>")})
	_, err := h.svc.Read(h.ctxHello(), OpenOptions{AllowedHosts: []string{"shop.test"}}, "https://shop.test/item")
	if !errors.Is(err, ErrReader) {
		t.Fatalf("Read() = %v", err)
	}
}

func TestReadPreservesCancellationFromReader(t *testing.T) {
	h := newHarness(t, map[string]http.Handler{"shop.test": HTMLHandler("<p>x</p>")})
	ctx, cancel := context.WithCancel(h.ctxHello())
	h.svc.opts.Reader = readerFunc(func(context.Context, string) (string, error) {
		cancel()
		return "", errors.New("transform interrupted")
	})
	_, err := h.svc.Read(ctx, OpenOptions{AllowedHosts: []string{"shop.test"}}, "https://shop.test/item")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Read() = %v", err)
	}
}

func TestJinaReaderPostsRawHTMLOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["html"] != "<p>Model X $129</p>" || body["url"] != nil || body["maxTokens"] != float64(2048) {
			t.Fatalf("request = %#v", body)
		}
		_, _ = io.WriteString(w, "  Model X $129  ")
	}))
	defer server.Close()
	got, err := (JinaReader{BaseURL: server.URL}).Extract(context.Background(), "<p>Model X $129</p>")
	if err != nil || got != "Model X $129" {
		t.Fatalf("Extract() = %q, %v", got, err)
	}
}

func TestJinaReaderRejectsFailedAndOversizedResponses(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "failed", status: http.StatusBadGateway, body: "upstream failed"},
		{name: "oversized", status: http.StatusOK, body: strings.Repeat("x", maxReaderResponseBytes+1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			if _, err := (JinaReader{BaseURL: server.URL, Client: server.Client()}).Extract(context.Background(), "<p>x</p>"); err == nil {
				t.Fatal("Extract() succeeded")
			}
		})
	}
}

func TestReadMapsReaderFailure(t *testing.T) {
	h := newHarness(t, map[string]http.Handler{"shop.test": HTMLHandler("<p>x</p>")})
	h.svc.opts.Reader = readerFunc(func(context.Context, string) (string, error) {
		return "", errors.New("transform failed")
	})
	_, err := h.svc.Read(h.ctxHello(), OpenOptions{AllowedHosts: []string{"shop.test"}}, "https://shop.test/item")
	if !errors.Is(err, ErrReader) {
		t.Fatalf("Read() = %v", err)
	}
}
