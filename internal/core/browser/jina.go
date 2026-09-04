package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxReaderResponseBytes = 64 << 10

// JinaReader uses Jina Reader only as an HTML-to-markdown transformer. Control
// Center fetches the page, so the sidecar cannot follow a caller-supplied URL.
type JinaReader struct {
	BaseURL string
	Client  *http.Client
}

func (r JinaReader) Extract(ctx context.Context, html string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"html": html, "respondWith": "markdown", "maxTokens": 2048,
		"retainImages": "none", "retainLinks": "none",
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(r.BaseURL, "/")+"/", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/plain")
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("reader returned %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReaderResponseBytes+1))
	if err != nil {
		return "", err
	}
	if len(raw) > maxReaderResponseBytes {
		return "", fmt.Errorf("reader response exceeds %d bytes", maxReaderResponseBytes)
	}
	return strings.TrimSpace(string(raw)), nil
}
