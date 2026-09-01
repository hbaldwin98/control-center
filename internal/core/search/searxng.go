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

// SearXNG calls a private SearXNG instance's JSON API. The base URL is host config,
// never plugin-supplied.
type SearXNG struct {
	BaseURL string
	Client  *http.Client
}

func (s SearXNG) Search(ctx context.Context, query string, limit int) ([]Hit, error) {
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
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("%w: unreadable JSON", ErrUnavailable)
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
