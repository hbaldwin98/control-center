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
			Purpose:      "Reads the last 30 days against two years of history and the daily temperature.",
		}},
		Events: []host.EventSpec{
			{
				Type:    "synced",
				Purpose: "A portal collection finished. Fires even when no new days were inserted.",
				Fields: host.Fields(synced{}, purposes(dayPurposes("Readings written this run."), map[string]string{
					"body": "One-line reading for a notification, such as 2026-09-02: 8.0 kWh ($2.00).",
				})),
			},
			{
				Type:    "insight",
				Purpose: "A model read the last 30 days against the seasonal history and that day's temperature. Needs three days of history and cheap-chat assigned.",
				Fields: host.Fields(insightPayload{}, map[string]string{
					"at":             "RFC3339Nano time of the insight.",
					"summary":        "Where usage stands versus last month and the same period last year, and whether temperature explains it.",
					"recommendation": "What to do about it.",
					"anomalies":      "Days that used far more or less than their outdoor temperature predicts.",
					"body":           "Summary plus recommendation, ready to send.",
				}),
			},
			{
				Type:    "alert",
				Purpose: "New calendar days were inserted. Matched by the default plugin-alert rule.",
				Fields: host.Fields(alerted{}, purposes(dayPurposes("How many new days were inserted."), map[string]string{
					"title": "Short headline.",
					"body":  "The latest reading, or a count of new days.",
				})),
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

// purposes merges the shared day descriptions with the ones an event adds of its
// own. Later maps win, so an event can sharpen a shared line.
func purposes(maps ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range maps {
		for key, value := range m {
			out[key] = value
		}
	}
	return out
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
