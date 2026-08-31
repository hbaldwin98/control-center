// Package pagewatch monitors one public page as a cheap end-to-end system canary.
package pagewatch

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
)

const (
	pluginID      = "pagewatch"
	checkSchedule = "17 */6 * * *"
	checkTimeout  = 2 * time.Minute
)

// Plugin implements the Page Watch plugin.
type Plugin struct {
	mu      sync.Mutex
	h       host.Host
	unwatch func()
}

// New returns a Page Watch plugin.
func New() *Plugin { return &Plugin{} }

func (p *Plugin) Manifest() host.Manifest {
	return host.Manifest{
		ID:          pluginID,
		Name:        "Page Watch",
		Version:     "0.1.0",
		Description: "Checks one public page through the real capability pipeline and reports drift or failure.",
		Automated:   true,
		Config: host.ConfigSpec{
			Schema: json.RawMessage(`{
				"type":"object",
				"additionalProperties":false,
				"required":["url","expectedText"],
				"properties":{
					"url":{
						"type":"string",
						"description":"Public HTTPS page to check every six hours.",
						"pattern":"^https://[^\\s]+$",
						"maxLength":2048
					},
					"expectedText":{
						"type":"string",
						"description":"Visible text that must remain on the page. Leave empty to monitor changes only.",
						"maxLength":200
					}
				}
			}`),
			Defaults: json.RawMessage(`{"url":"https://example.com/","expectedText":"Example Domain"}`),
		},
	}
}

func (p *Plugin) Jobs() []hostjobs.Def {
	return []hostjobs.Def{{
		Name:        "check",
		Schedule:    checkSchedule,
		TimeZone:    "UTC",
		Timeout:     checkTimeout,
		MaxAttempts: 3,
		Backoff: hostjobs.BackoffPolicy{
			Initial: 30 * time.Second,
			Maximum: 5 * time.Minute,
		},
		Concurrency: 1,
		Handler:     p.check,
	}}
}

func (p *Plugin) Subscriptions() []host.Subscription {
	return []host.Subscription{{
		Pattern: pluginID + ".check.completed",
		Durable: &host.DurableSubscription{Name: "checks", Handler: p.onCheckCompleted},
	}}
}

func (p *Plugin) Routes() []host.Route {
	return []host.Route{
		{Pattern: "GET /checks", Handler: http.HandlerFunc(p.handleGetChecks)},
		{Pattern: "POST /checks", Handler: http.HandlerFunc(p.handlePostCheck)},
	}
}

func (p *Plugin) Init(_ context.Context, h host.Host) error {
	p.mu.Lock()
	p.h = h
	p.unwatch = h.Config().Watch(func(_ context.Context, raw json.RawMessage) {
		h.Log().Info("page watch config updated", "value", string(raw))
	})
	p.mu.Unlock()
	h.Log().Info("page watch ready", "schedule", checkSchedule)
	return nil
}

func (p *Plugin) Shutdown(context.Context) error {
	p.mu.Lock()
	if p.unwatch != nil {
		p.unwatch()
		p.unwatch = nil
	}
	p.h = nil
	p.mu.Unlock()
	return nil
}

func (p *Plugin) host() (host.Host, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.h, p.h != nil
}

type settings struct {
	URL          string `json:"url"`
	ExpectedText string `json:"expectedText"`
}

func (p *Plugin) settings() settings {
	s := settings{URL: "https://example.com/", ExpectedText: "Example Domain"}
	h, ok := p.host()
	if ok {
		_ = h.Config().Decode(&s)
	}
	return s
}
