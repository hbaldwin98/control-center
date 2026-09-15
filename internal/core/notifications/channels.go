package notifications

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/credentials"
)

func (s *Service) buildChannel(cfg ChannelConfig) (Channel, error) {
	if s.opts.Channels != nil {
		if ctor, ok := s.opts.Channels[cfg.Kind]; ok {
			return ctor(cfg, s.creds)
		}
	}
	switch cfg.Kind {
	case "inbox":
		return inboxChannel{id: cfg.ID}, nil
	case "ntfy":
		return newNtfyChannel(cfg, s.creds, s.opts.HTTPClient)
	case "webpush":
		return newWebPushChannel(cfg, s.creds, s.opts.HTTPClient)
	case "cloudflare":
		return newCloudflareChannel(cfg, s.creds, s.opts.HTTPClient)
	case "email":
		return newEmailChannel(cfg, s.creds)
	default:
		return nil, Permanent(fmt.Errorf("notifications: unknown channel kind %q", cfg.Kind))
	}
}

type inboxChannel struct{ id string }

func (c inboxChannel) ID() string { return c.id }

func (c inboxChannel) Send(context.Context, Delivery) error { return nil }

type ntfyChannel struct {
	id     string
	server string
	topic  string
	creds  credentials.Runtime
	credID string
	http   *http.Client
}

func newNtfyChannel(cfg ChannelConfig, creds credentials.Runtime, client *http.Client) (Channel, error) {
	topic := strings.TrimSpace(cfg.Settings["topic"])
	if topic == "" {
		return nil, Permanent(fmt.Errorf("ntfy: missing topic"))
	}
	server := strings.TrimSpace(cfg.Settings["server"])
	if server == "" {
		server = "https://ntfy.sh"
	}
	u, err := url.Parse(server)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, Permanent(fmt.Errorf("ntfy: invalid server"))
	}
	if client == nil {
		client = http.DefaultClient
	}
	return ntfyChannel{id: cfg.ID, server: strings.TrimRight(server, "/"), topic: topic, creds: creds, credID: cfg.CredentialID, http: client}, nil
}

func (c ntfyChannel) ID() string { return c.id }

func (c ntfyChannel) Send(ctx context.Context, d Delivery) error {
	endpoint := c.server + "/" + url.PathEscape(c.topic)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader([]byte(d.Notification.Body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("Title", d.Notification.Title)
	if d.Notification.URL != "" {
		req.Header.Set("Click", d.Notification.URL)
	}
	if d.IdempotencyKey != "" {
		req.Header.Set("Idempotency-Key", d.IdempotencyKey)
	}
	if c.credID != "" && c.creds != nil {
		tok, err := c.creds.Token(ctx, c.credID)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
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
	if res.StatusCode >= 400 && res.StatusCode < 500 && res.StatusCode != 429 {
		return Permanent(fmt.Errorf("ntfy: HTTP %d", res.StatusCode))
	}
	return fmt.Errorf("ntfy: HTTP %d", res.StatusCode)
}

type webPushChannel struct {
	id       string
	endpoint string
	creds    credentials.Runtime
	credID   string
	http     *http.Client
}

func newWebPushChannel(cfg ChannelConfig, creds credentials.Runtime, client *http.Client) (Channel, error) {
	endpoint := strings.TrimSpace(cfg.Settings["endpoint"])
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil {
		return nil, Permanent(fmt.Errorf("webpush: invalid endpoint"))
	}
	switch u.Scheme {
	case "https":
	case "http":
		h := u.Hostname()
		if h != "127.0.0.1" && h != "localhost" && h != "::1" {
			return nil, Permanent(fmt.Errorf("webpush: endpoint must be https"))
		}
	default:
		return nil, Permanent(fmt.Errorf("webpush: endpoint must be https"))
	}
	if client == nil {
		client = http.DefaultClient
	}
	return webPushChannel{id: cfg.ID, endpoint: endpoint, creds: creds, credID: cfg.CredentialID, http: client}, nil
}

func (c webPushChannel) ID() string { return c.id }

func (c webPushChannel) Send(ctx context.Context, d Delivery) error {
	body := d.Notification.Title
	if d.Notification.Body != "" {
		body = body + "\n" + d.Notification.Body
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader([]byte(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("TTL", "86400")
	if d.IdempotencyKey != "" {
		req.Header.Set("Idempotency-Key", d.IdempotencyKey)
	}
	if c.credID != "" && c.creds != nil {
		tok, err := c.creds.Token(ctx, c.credID)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "vapid "+tok)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
	if res.StatusCode >= 200 && res.StatusCode < 300 || res.StatusCode == 201 || res.StatusCode == 204 {
		return nil
	}
	if res.StatusCode == 410 || res.StatusCode == 404 {
		return Permanent(fmt.Errorf("webpush: subscription gone (%d)", res.StatusCode))
	}
	if res.StatusCode >= 400 && res.StatusCode < 500 && res.StatusCode != 429 {
		return Permanent(fmt.Errorf("webpush: HTTP %d", res.StatusCode))
	}
	return fmt.Errorf("webpush: HTTP %d", res.StatusCode)
}
