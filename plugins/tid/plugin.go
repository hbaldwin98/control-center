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
		Models: []host.ModelNeed{{
			Name:         "cheap-chat",
			Capabilities: []string{"chat"},
			Purpose:      "A reading of the last month of usage.",
		}},
		Events: []host.EventSpec{
			{
				Type:    "synced",
				Purpose: "A portal collection finished. Fires even when no new days were inserted.",
				Fields:  append(syncedFields("Readings written this run."), host.EventField{Name: "body", Type: "string", Purpose: "One-line reading for a notification, such as 2026-09-02: 8.0 kWh ($2.00)."}),
			},
			{
				Type:    "insight",
				Purpose: "A model wrote a summary of recent usage. Needs at least three days of history and cheap-chat assigned.",
				Fields: []host.EventField{
					{Name: "at", Type: "string", Purpose: "RFC3339Nano time of the insight."},
					{Name: "summary", Type: "string", Purpose: "What the model said about usage."},
					{Name: "recommendation", Type: "string", Purpose: "What to do about it."},
					{Name: "anomalies", Type: "string[]", Purpose: "Unusual days the model called out."},
					{Name: "body", Type: "string", Purpose: "Summary plus recommendation, ready to send."},
				},
			},
			{
				Type:    "alert",
				Purpose: "New calendar days were inserted. Matched by the default plugin-alert rule.",
				Fields: append([]host.EventField{
					{Name: "title", Type: "string", Purpose: "Short headline."},
					{Name: "body", Type: "string", Purpose: "The latest reading, or a count of new days."},
				}, syncedFields("How many new days were inserted.")...),
			},
		},
		Config: host.ConfigSpec{
			Schema: json.RawMessage(`{
				"type":"object",
				"additionalProperties":false,
				"properties":{
					"tenant_id":{
						"type":"string",
						"title":"OCX tenant ID",
						"description":"Tenant ID sent with every TID API request.",
						"maxLength":200
					},
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
					}
				}
			}`),
			Defaults: json.RawMessage(`{"tenant_id":"","username":"","credential_id":"","cents_per_kwh":0}`),
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
		{Pattern: "GET /history", Handler: http.HandlerFunc(p.handleGetHistory)},
		{Pattern: "POST /history/sync", Handler: http.HandlerFunc(p.handlePostHistorySync)},
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
	TenantID     string  `json:"tenant_id"`
	Username     string  `json:"username"`
	CredentialID string  `json:"credential_id"`
	CentsPerKWh  float64 `json:"cents_per_kwh"`
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
