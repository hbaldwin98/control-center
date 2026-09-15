package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/credentials"
)

// cloudflareChannel is the core-side adapter for the deploy/cloudflare notification
// Worker. The Worker owns Web Push encryption and subscription storage; this channel
// only authenticates requests and carries the notification contract to it.
type cloudflareChannel struct {
	id       string
	endpoint string
	creds    credentials.Runtime
	credID   string
	http     *http.Client
}

func newCloudflareChannel(cfg ChannelConfig, creds credentials.Runtime, client *http.Client) (Channel, error) {
	endpoint, err := parseCloudflareEndpoint(cfg.Settings["endpoint"])
	if err != nil {
		return nil, Permanent(err)
	}
	if strings.TrimSpace(cfg.CredentialID) == "" || creds == nil {
		return nil, Permanent(fmt.Errorf("cloudflare: credential required"))
	}
	if client == nil {
		client = http.DefaultClient
	}
	return cloudflareChannel{
		id:       cfg.ID,
		endpoint: strings.TrimRight(endpoint, "/"),
		creds:    creds,
		credID:   cfg.CredentialID,
		http:     client,
	}, nil
}

func parseCloudflareEndpoint(raw string) (string, error) {
	endpoint := strings.TrimSpace(raw)
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("cloudflare: invalid endpoint")
	}
	switch u.Scheme {
	case "https":
	case "http":
		h := u.Hostname()
		if h != "127.0.0.1" && h != "localhost" && h != "::1" {
			return "", fmt.Errorf("cloudflare: endpoint must be https")
		}
	default:
		return "", fmt.Errorf("cloudflare: endpoint must be https")
	}
	return strings.TrimRight(endpoint, "/"), nil
}

func (c cloudflareChannel) ID() string { return c.id }

func (c cloudflareChannel) Send(ctx context.Context, d Delivery) error {
	payload := struct {
		ID             string `json:"id"`
		IdempotencyKey string `json:"idempotencyKey"`
		Title          string `json:"title"`
		Body           string `json:"body"`
		URL            string `json:"url,omitempty"`
		Subject        string `json:"subject,omitempty"`
		Collapsed      int    `json:"collapsed,omitempty"`
	}{
		ID: d.Notification.ID, IdempotencyKey: d.IdempotencyKey,
		Title: d.Notification.Title, Body: d.Notification.Body,
		URL: d.Notification.URL, Subject: d.Notification.Subject,
		Collapsed: d.Notification.Collapsed,
	}
	return c.doJSON(ctx, http.MethodPost, "/notify", payload, d.IdempotencyKey, "cloudflare")
}

func (c cloudflareChannel) PublicKey(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/vapid-public-key", nil)
	if err != nil {
		return "", err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
		return "", cloudflareHTTPError(res.StatusCode)
	}
	var payload struct {
		PublicKey string `json:"publicKey"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&payload); err != nil {
		return "", Permanent(fmt.Errorf("cloudflare: invalid public-key response: %w", err))
	}
	if strings.TrimSpace(payload.PublicKey) == "" {
		return "", Permanent(fmt.Errorf("cloudflare: public-key response is empty"))
	}
	return payload.PublicKey, nil
}

func (c cloudflareChannel) RegisterSubscription(ctx context.Context, sub PushSubscription) error {
	if err := validatePushSubscription(sub); err != nil {
		return err
	}
	return c.doJSON(ctx, http.MethodPost, "/subscriptions", sub, "", "cloudflare")
}

func (c cloudflareChannel) UnregisterSubscription(ctx context.Context, endpoint string) error {
	if err := validatePushEndpoint(endpoint); err != nil {
		return err
	}
	return c.doJSON(ctx, http.MethodPost, "/subscriptions/remove", struct {
		Endpoint string `json:"endpoint"`
	}{Endpoint: strings.TrimSpace(endpoint)}, "", "cloudflare")
}

func (c cloudflareChannel) doJSON(ctx context.Context, method, path string, body any, idempotencyKey, name string) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	tok, err := c.creds.Token(ctx, c.credID)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return nil
	}
	if res.StatusCode >= 400 && res.StatusCode < 500 && res.StatusCode != http.StatusTooManyRequests {
		return Permanent(fmt.Errorf("%s: HTTP %d", name, res.StatusCode))
	}
	return fmt.Errorf("%s: HTTP %d", name, res.StatusCode)
}

func cloudflareHTTPError(status int) error {
	if status >= 400 && status < 500 && status != http.StatusTooManyRequests {
		return Permanent(fmt.Errorf("cloudflare: HTTP %d", status))
	}
	return fmt.Errorf("cloudflare: HTTP %d", status)
}
