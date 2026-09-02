package search

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFakeEngineAnswersAndHonoursTheAllowlist(t *testing.T) {
	svc := New(nil, Options{Engine: Fake{}})
	ctx := WithPlugin(context.Background(), "example")

	hits, err := svc.Query(ctx, Request{Query: "some product used price"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || !strings.HasPrefix(hits[0].URL, "https://") {
		t.Fatalf("hits = %#v", hits)
	}

	allowed, err := svc.Query(ctx, Request{
		Query: "some product used price", AllowedDomains: []string{"example-market.test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(allowed) != 1 {
		t.Fatalf("allowlisted host was filtered out: %#v", allowed)
	}

	denied, err := svc.Query(ctx, Request{
		Query: "some product used price", AllowedDomains: []string{"somewhere-else.test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(denied) != 0 {
		t.Fatalf("a host off the allowlist survived: %#v", denied)
	}
}

func TestQueryRequiresPluginAndNonEmptyQuery(t *testing.T) {
	svc := New(nil, Options{Engine: Fake{}})
	if _, err := svc.Query(context.Background(), Request{Query: "drill"}); !errors.Is(err, ErrNoPlugin) {
		t.Fatalf("no plugin: %v", err)
	}
	ctx := WithPlugin(context.Background(), "bidrl")
	if _, err := svc.Query(ctx, Request{Query: "  "}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty: %v", err)
	}
}

func TestPublicHitRejectsPrivateAndHTTP(t *testing.T) {
	if _, err := publicHit(Hit{URL: "http://example.test/x"}, nil); !errors.Is(err, ErrDenied) {
		t.Fatalf("http: %v", err)
	}
	if _, err := publicHit(Hit{URL: "https://127.0.0.1/x"}, nil); !errors.Is(err, ErrDenied) {
		t.Fatalf("loopback: %v", err)
	}
	if _, err := publicHit(Hit{URL: "https://evil.test/x"}, map[string]struct{}{"example.test": {}}); !errors.Is(err, ErrDenied) {
		t.Fatalf("allowlist: %v", err)
	}
	if _, err := publicHit(Hit{URL: "https://www.ebay.com/itm/1"}, map[string]struct{}{"ebay.com": {}}); err != nil {
		t.Fatalf("ebay subdomain: %v", err)
	}
	if _, err := publicHit(Hit{URL: "https://notebay.com/itm/1"}, map[string]struct{}{"ebay.com": {}}); !errors.Is(err, ErrDenied) {
		t.Fatalf("suffix trap: %v", err)
	}
	got, err := publicHit(Hit{URL: "https://example.test/item", Title: "Drill", Snippet: " $89 "}, nil)
	if err != nil || got.URL != "https://example.test/item" || got.Snippet != "$89" {
		t.Fatalf("got %#v %v", got, err)
	}
}

func TestSearXNGParsesJSONResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "json" || r.URL.Query().Get("q") == "" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		if r.URL.Query().Get("engines") != "brave" {
			t.Errorf("engines = %s", r.URL.Query().Get("engines"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]string{{
				"url":     "https://shop.example.test/dcd791",
				"title":   "DeWalt DCD791",
				"content": "Used DeWalt DCD791 for $89",
			}},
		})
	}))
	t.Cleanup(srv.Close)
	engine := SearXNG{BaseURL: srv.URL, Client: srv.Client()}
	hits, err := engine.Search(context.Background(), "DCD791", 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Title != "DeWalt DCD791" {
		t.Fatalf("hits = %#v", hits)
	}
}

func TestSearXNGFallsBackWhenBraveIsEmpty(t *testing.T) {
	var engines []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		engines = append(engines, r.URL.Query().Get("engines"))
		if r.URL.Query().Get("engines") == "brave" {
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]string{{
				"url":     "https://shop.example.test/dcd791",
				"title":   "DeWalt DCD791",
				"content": "Used DeWalt DCD791 for $89",
			}},
		})
	}))
	t.Cleanup(srv.Close)
	hits, err := (SearXNG{BaseURL: srv.URL, Client: srv.Client()}).Search(context.Background(), "DCD791", 4)
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits=%#v err=%v", hits, err)
	}
	if len(engines) != 2 || engines[0] != "brave" || engines[1] != "duckduckgo" {
		t.Fatalf("engines=%v", engines)
	}
}

func TestSearXNGHTTPErrorIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	_, err := (SearXNG{BaseURL: srv.URL, Client: srv.Client(), Engines: []string{"brave"}}).Search(context.Background(), "x", 1)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestPaceHonoursCancel(t *testing.T) {
	p := Pace(Fake{}, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := p.Search(ctx, "keurig", 1); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := p.Search(ctx, "keurig", 1); err == nil {
		t.Fatal("expected cancel while waiting")
	}
}
