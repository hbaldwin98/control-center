// Package hello is the validating plugin: it exercises every host capability and
// is the integration test for the kill switch. It is not a product surface.
package hello

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/hbaldwin98/control-center/host"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
)

const pluginID = "hello"

// Plugin implements host.Plugin for the hello validating plugin.
type Plugin struct {
	mu      sync.Mutex
	h       host.Host
	unwatch func()
}

// New returns a hello plugin. Declarations do not depend on Init.
func New() *Plugin {
	return &Plugin{}
}

func (p *Plugin) Manifest() host.Manifest {
	return host.Manifest{
		ID:          pluginID,
		Name:        "Hello",
		Version:     "0.1.0",
		Description: "Validating plugin. Exercises every host capability; the kill-switch integration test.",
		Automated:   true,
		Models: []host.ModelNeed{{
			Name:         "cheap-chat",
			Capabilities: []string{"chat"},
			Purpose:      "A tiny chat call on every tick.",
		}},
		Events: []host.EventSpec{{
			Type:    "ticked",
			Purpose: "The cron tick finished. One row in hello_ticks.",
			Fields: []host.EventField{
				{Name: "at", Type: "string", Purpose: "RFC3339Nano time of the tick."},
				{Name: "note", Type: "string", Purpose: "The configured note recorded with the tick."},
				{Name: "blobKey", Type: "string", Purpose: "Blob written during the tick."},
				{Name: "aiText", Type: "string", Purpose: "Reply from cheap-chat, empty if the call failed."},
			},
		}},
		Config: host.ConfigSpec{
			Schema: json.RawMessage(`{
				"type":"object",
				"additionalProperties":false,
				"properties":{
					"note":{
						"type":"string",
						"description":"A label recorded with every tick.",
						"maxLength":80
					}
				}
			}`),
			Defaults: json.RawMessage(`{"note":"hello"}`),
		},
	}
}

func (p *Plugin) Jobs() []hostjobs.Def {
	return []hostjobs.Def{{
		Name:        "tick",
		Schedule:    "* * * * *",
		TimeZone:    "UTC",
		Timeout:     minuteTimeout,
		MaxAttempts: 1,
		Concurrency: 1,
		Handler:     p.tick,
	}}
}

func (p *Plugin) Subscriptions() []host.Subscription {
	return []host.Subscription{{
		Pattern: pluginID + ".ticked",
		Durable: &host.DurableSubscription{Name: "ticks", Handler: p.onTicked},
	}}
}

func (p *Plugin) Routes() []host.Route {
	return []host.Route{
		{Pattern: "GET /ticks", Handler: http.HandlerFunc(p.handleGetTicks)},
		{Pattern: "POST /tick", Handler: http.HandlerFunc(p.handlePostTick)},
		{Pattern: "POST /chat", Handler: http.HandlerFunc(p.handlePostChat)},
	}
}

func (p *Plugin) Init(_ context.Context, h host.Host) error {
	p.mu.Lock()
	p.h = h
	p.unwatch = h.Config().Watch(func(_ context.Context, raw json.RawMessage) {
		h.Log().Info("config updated", "value", string(raw))
	})
	p.mu.Unlock()
	h.Log().Info("hello ready", "at", h.Clock().Now().UTC().Format("2006-01-02T15:04:05Z"))
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
	Note string `json:"note"`
}

func (p *Plugin) settings() settings {
	var s settings
	h, ok := p.host()
	if !ok {
		return settings{Note: "hello"}
	}
	if err := h.Config().Decode(&s); err != nil || s.Note == "" {
		s.Note = "hello"
	}
	return s
}
