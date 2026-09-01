// Package bidrl scores BIDRL auction lots from their photographs rather than their titles.
package bidrl

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
	pluginID = "bidrl"
	jobTO    = 2 * time.Hour

	maxLotsPerAuction = 500
	maxImagesPerLot   = 12
	maxImageBytes     = 10 << 20
	maxLotImageBytes  = 100 << 20
	scanAIParallel    = 4

	// The bid gallery pages its item feed; 100 per page is the largest option the
	// site's own pager offers, and the cap keeps a runaway auction bounded.
	galleryPerPage  = 100
	maxGalleryPages = 20
	feedTimeout     = 30 * time.Second
)

// Plugin implements host.Plugin for BIDRL lot scoring.
type Plugin struct {
	mu sync.Mutex
	h  host.Host
}

// New returns a BIDRL plugin. Declarations do not depend on Init.
func New() *Plugin { return &Plugin{} }

func (p *Plugin) Manifest() host.Manifest {
	return host.Manifest{
		ID:          pluginID,
		Name:        "BIDRL",
		Version:     "0.1.0",
		Description: "Scores BIDRL lots from photographs, prices only when a model or barcode is cited, and ranks deals.",
		Automated:   false,
		Models: []host.ModelNeed{
			{
				Name:         "cheap-vision",
				Capabilities: []string{"chat", "vision"},
				Purpose:      "Identify what the lot photographs actually show.",
			},
			{
				Name:         "grounded-price",
				Capabilities: []string{"chat", "grounding"},
				Purpose:      "Cite a current market price from public listings.",
			},
		},
		Config: host.ConfigSpec{
			Schema:   json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
			Defaults: json.RawMessage(`{}`),
		},
	}
}

func (p *Plugin) Jobs() []hostjobs.Def {
	backoff := hostjobs.BackoffPolicy{Initial: time.Minute, Maximum: 15 * time.Minute}
	return []hostjobs.Def{
		{Name: "collect", Timeout: jobTO, MaxAttempts: 2, Concurrency: 1, Backoff: backoff, Handler: p.collectJob},
		{Name: "scan", Timeout: jobTO, MaxAttempts: 2, Concurrency: 1, Backoff: backoff, Handler: p.scanJob},
		{Name: "reprice", Timeout: jobTO, MaxAttempts: 2, Concurrency: 1, Backoff: backoff, Handler: p.repriceJob},
		{Name: "refresh", Timeout: jobTO, MaxAttempts: 2, Concurrency: 1, Backoff: backoff, Handler: p.refreshJob},
	}
}

func (p *Plugin) Subscriptions() []host.Subscription {
	return []host.Subscription{{
		Pattern: pluginID + ".**",
		Durable: &host.DurableSubscription{Name: "meta", Handler: p.onEvent},
	}}
}

func (p *Plugin) Routes() []host.Route {
	return []host.Route{
		{Pattern: "GET /feed", Handler: http.HandlerFunc(p.handleGetFeed)},
		{Pattern: "GET /auctions", Handler: http.HandlerFunc(p.handleListAuctions)},
		{Pattern: "POST /auctions", Handler: http.HandlerFunc(p.handleAddAuction)},
		{Pattern: "GET /auctions/{id}", Handler: http.HandlerFunc(p.handleGetAuction)},
		{Pattern: "DELETE /auctions/{id}", Handler: http.HandlerFunc(p.handleDeleteAuction)},
		{Pattern: "POST /auctions/{id}/scan", Handler: http.HandlerFunc(p.handleScan)},
		{Pattern: "POST /auctions/{id}/refresh", Handler: http.HandlerFunc(p.handleRefresh)},
		{Pattern: "GET /lots/{id}", Handler: http.HandlerFunc(p.handleGetLot)},
		{Pattern: "POST /lots/{id}/reprice", Handler: http.HandlerFunc(p.handleReprice)},
	}
}

func (p *Plugin) Init(_ context.Context, h host.Host) error {
	p.mu.Lock()
	p.h = h
	p.mu.Unlock()
	h.Log().Info("bidrl ready")
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
