package search

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const searxngTimeout = 20 * time.Second
const maxSearxngBytes = 1 << 20
const searxngRetryWait = 400 * time.Millisecond

// defaultSearxngEngines are tried one at a time. Querying them together makes
// DuckDuckGo and Brave both see a burst from the same Docker IP.
var defaultSearxngEngines = []string{"brave", "duckduckgo"}

// SearXNG calls a private SearXNG instance's JSON API. The base URL is host config,
// never plugin-supplied.
type SearXNG struct {
	BaseURL string
	Client  *http.Client
	// Engines, when nonempty, overrides the default Brave-then-DuckDuckGo order.
	Engines []string
}

func (s SearXNG) Search(ctx context.Context, query string, limit int) ([]Hit, error) {
	var last error
	for _, engine := range s.engineNames() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hits, err := s.searchEngine(ctx, query, engine, limit)
		if err != nil {
			last = err
			continue
		}
		if len(hits) > 0 {
			return hits, nil
		}
	}
	if last != nil {
		return nil, last
	}
	return nil, nil
}

func (s SearXNG) engineNames() []string {
	if len(s.Engines) > 0 {
		return s.Engines
	}
	return defaultSearxngEngines
}

func (s SearXNG) searchEngine(ctx context.Context, query, engine string, limit int) ([]Hit, error) {
	hits, err := s.searchOnce(ctx, query, engine, limit)
	if err == nil {
		return hits, nil
	}
	t := time.NewTimer(searxngRetryWait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.C:
	}
	return s.searchOnce(ctx, query, engine, limit)
}

func (s SearXNG) searchOnce(ctx context.Context, query, engine string, limit int) ([]Hit, error) {
	base := strings.TrimRight(strings.TrimSpace(s.BaseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("%w: searxng url is empty", ErrUnavailable)
	}
	u, err := url.Parse(base + "/search")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("format", "json")
	q.Set("language", "en")
	if engine != "" {
		q.Set("engines", engine)
	}
	u.RawQuery = q.Encode()

	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: searxngTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	req.Header.Set("Accept", "application/json")
	// SearXNG botdetection logs an error when these are absent, even on a private
	// sidecar with no reverse proxy.
	req.Header.Set("X-Real-IP", "127.0.0.1")
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxSearxngBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: HTTP %d", ErrUnavailable, res.StatusCode)
	}
	var parsed struct {
		Results []struct {
			URL     string `json:"url"`
			Title   string `json:"title"`
			Content string `json:"content"`
		} `json:"results"`
		UnresponsiveEngines [][]string `json:"unresponsive_engines"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("%w: unreadable JSON", ErrUnavailable)
	}
	if len(parsed.Results) == 0 && len(parsed.UnresponsiveEngines) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, formatEngineFailures(parsed.UnresponsiveEngines))
	}
	out := make([]Hit, 0, len(parsed.Results))
	for _, r := range parsed.Results {
		out = append(out, Hit{URL: r.URL, Title: r.Title, Snippet: r.Content})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func formatEngineFailures(failures [][]string) string {
	parts := make([]string, 0, len(failures))
	for _, failure := range failures {
		if len(failure) < 2 {
			continue
		}
		parts = append(parts, failure[0]+": "+failure[1])
	}
	if len(parts) == 0 {
		return "all requested engines failed"
	}
	return strings.Join(parts, "; ")
}
