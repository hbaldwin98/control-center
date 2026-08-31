// Package tid pulls Turlock Irrigation District usage from My TID, stores daily
// readings, and surfaces metrics plus a model-written insight.
package tid

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/hbaldwin98/control-center/host"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
)

const pluginID = "tid"

// Plugin implements host.Plugin for TID energy usage.
type Plugin struct {
	mu sync.Mutex
	h  host.Host
}

// New returns a TID plugin. Declarations do not depend on Init.
func New() *Plugin {
	return &Plugin{}
}

func (p *Plugin) Manifest() host.Manifest {
	return host.Manifest{
		ID:          pluginID,
		Name:        "TID",
		Version:     "0.1.0",
		Description: "Pulls energy usage from My TID (Origin CX) and shows daily metrics and an insight.",
		Automated:   true,
		Config: host.ConfigSpec{
			Schema: json.RawMessage(`{
				"type":"object",
				"additionalProperties":false,
				"properties":{
					"username":{
						"type":"string",
						"title":"My TID username",
						"description":"The email or username you use at my.tid.org. The password is a credential, not this field.",
						"maxLength":200
					},
					"credential_id":{
						"type":"string",
						"title":"Password credential",
						"description":"ID of an API-key credential whose secret is your My TID password. Create it under Settings → Credentials with provider tid.",
						"maxLength":64,
						"pattern":"^[a-zA-Z0-9._-]*$"
					},
					"cents_per_kwh":{
						"type":"number",
						"title":"¢ / kWh",
						"description":"Optional. Used to estimate cost when the portal does not include dollars.",
						"minimum":0,
						"maximum":200
					},
					"usage_url":{
						"type":"string",
						"title":"Usage URL override",
						"description":"Optional HTTPS URL on my.tid.org to fetch after login. Leave blank to open Origin CX Usage Graphs and parse the SPA's usage JSON.",
						"maxLength":300
					}
				}
			}`),
			Defaults: json.RawMessage(`{"username":"","credential_id":"","cents_per_kwh":0,"usage_url":""}`),
		},
	}
}

func (p *Plugin) Jobs() []hostjobs.Def {
	return []hostjobs.Def{{
		Name:        "sync",
		Schedule:    "0 6 * * *",
		TimeZone:    "America/Los_Angeles",
		Timeout:     syncTimeout,
		MaxAttempts: 2,
		Concurrency: 1,
		Handler:     p.sync,
	}}
}

func (p *Plugin) Subscriptions() []host.Subscription {
	return []host.Subscription{
		{Pattern: pluginID + ".synced", Durable: &host.DurableSubscription{Name: "syncs", Handler: p.onSynced}},
		{Pattern: pluginID + ".insight", Durable: &host.DurableSubscription{Name: "insights", Handler: p.onInsight}},
	}
}

func (p *Plugin) Routes() []host.Route {
	return []host.Route{
		{Pattern: "GET /summary", Handler: http.HandlerFunc(p.handleGetSummary)},
		{Pattern: "POST /sync", Handler: http.HandlerFunc(p.handlePostSync)},
		{Pattern: "POST /upload", Handler: http.HandlerFunc(p.handlePostUpload)},
	}
}

func (p *Plugin) Init(_ context.Context, h host.Host) error {
	p.mu.Lock()
	p.h = h
	p.mu.Unlock()
	h.Log().Info("tid ready")
	return nil
}

func (p *Plugin) Shutdown(context.Context) error {
	p.mu.Lock()
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
	Username     string  `json:"username"`
	CredentialID string  `json:"credential_id"`
	CentsPerKWh  float64 `json:"cents_per_kwh"`
	UsageURL     string  `json:"usage_url"`
}

func (p *Plugin) settings() settings {
	var s settings
	h, ok := p.host()
	if !ok {
		return s
	}
	_ = h.Config().Decode(&s)
	return s
}
