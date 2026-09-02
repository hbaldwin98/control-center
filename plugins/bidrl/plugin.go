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
	// Rows the overview shows in each of its two short lists.
	overviewRows = 6
)

// Plugin implements host.Plugin for BIDRL lot scoring.
type Plugin struct {
	mu sync.Mutex
	h  host.Host

	// live keeps BidRL's feed joined to the lots the host says someone is watching.
	live liveFeed

	// unwatch stops live delivery when this runtime generation ends.
	unwatch func()
}

// New returns a BIDRL plugin. Declarations do not depend on Init.
func New() *Plugin { return &Plugin{} }

func (p *Plugin) Manifest() host.Manifest {
	return host.Manifest{
		ID:          pluginID,
		Name:        "BIDRL",
		Version:     "0.1.0",
		Description: "Scores BIDRL lots from photographs, searches SITES auctions first, and prices only when a model or barcode is cited.",
		// Scheduled work exists, so this is true even while automation.enabled is
		// false: the honest reading is that this plugin can start work without a
		// person, and the budget that forces is the point.
		Automated: true,
		Models: []host.ModelNeed{
			{
				Name:         "cheap-vision",
				Capabilities: []string{"chat", "vision"},
				Purpose:      "Identify what the lot photographs actually show.",
			},
			{
				Name:         "grounded-price",
				Capabilities: []string{"chat"},
				Purpose:      "Pick a dollar amount already written on an eBay, retail, or marketplace search hit.",
			},
			{
				Name:         "intent-expand",
				Capabilities: []string{"chat"},
				Purpose:      "Turn a stated intent into related auction-title words (tent, headlamp, lantern — not only the word camping).",
			},
			{
				Name:         "intent-match",
				Capabilities: []string{"embed"},
				Purpose:      "Embed collected lot titles so intent search can match by meaning.",
			},
			{
				Name:         "watch-judge",
				Capabilities: []string{"chat"},
				Purpose:      "Decide whether a ranked candidate is really the thing a watchlist asked for, from its text alone.",
			},
		},
		Config: host.ConfigSpec{
			Schema: json.RawMessage(`{
				"type":"object",
				"additionalProperties":false,
				"properties":{
					"preferredAffiliateIds":{
						"type":"array",
						"title":"Preferred SITES locations",
						"description":"Numeric BidRL affiliate ids (19 is Turlock). Empty uses every location on BidRL's SITES menu.",
						"items":{"type":"string","pattern":"^[0-9]{1,8}$","maxLength":8},
						"maxItems":20
					},
					"searchScope":{
						"type":"string",
						"title":"Search scope",
						"description":"prefer ranks SITES lots first, only hides the rest, all ignores location.",
						"enum":["prefer","only","all"]
					},
					"automation":{
						"type":"object",
						"title":"Automation",
						"description":"Scheduled collection and watchlist matching. Off by default; an empty location list also means nothing runs.",
						"additionalProperties":false,
						"properties":{
							"enabled":{
								"type":"boolean",
								"title":"Run on a schedule",
								"description":"When off, both scheduled ticks return without touching BidRL or spending anything."
							},
							"affiliateIds":{
								"type":"array",
								"title":"Locations to sweep",
								"description":"Numeric BidRL affiliate ids. Empty means the sweep does nothing: an unscoped scheduled crawl is not something this plugin will do.",
								"items":{"type":"string","pattern":"^[0-9]{1,8}$","maxLength":8},
								"maxItems":20
							},
							"maxAuctionsPerSweep":{"type":"integer","title":"Auctions per tick","minimum":1,"maximum":20},
							"maxNewLotsPerSweep":{"type":"integer","title":"New lots per tick","minimum":1,"maximum":2000}
						}
					}
				}
			}`),
			Defaults: json.RawMessage(`{"preferredAffiliateIds":[],"searchScope":"prefer","automation":{"enabled":false,"affiliateIds":[],"maxAuctionsPerSweep":5,"maxNewLotsPerSweep":400}}`),
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
		{Name: "enrich", Timeout: jobTO, MaxAttempts: 2, Concurrency: 1, Backoff: backoff, Handler: p.enrichJob},
		{Name: "search", Timeout: jobTO, MaxAttempts: 2, Concurrency: 1, Backoff: backoff, Handler: p.searchJob},
		{Name: "intent", Timeout: jobTO, MaxAttempts: 2, Concurrency: 1, Backoff: backoff, Handler: p.intentJob},
		{Name: "discover", Timeout: jobTO, MaxAttempts: 2, Concurrency: 1, Backoff: backoff, Handler: p.discoverJob},
		{Name: "watch", Timeout: jobTO, MaxAttempts: 2, Concurrency: 1, Backoff: backoff, Handler: p.watchJob},
		// The only two scheduled jobs. Both check automation.enabled first and do
		// nothing when it is off, which is the default.
		{Name: "sweep", Timeout: jobTO, MaxAttempts: 1, Concurrency: 1, Backoff: backoff,
			Schedule: sweepSchedule, TimeZone: cronTimeZone, Handler: p.sweepJob},
		{Name: "match", Timeout: jobTO, MaxAttempts: 1, Concurrency: 1, Backoff: backoff,
			Schedule: matchSchedule, TimeZone: cronTimeZone, Handler: p.matchJob},
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
		{Pattern: "GET /overview", Handler: http.HandlerFunc(p.handleGetOverview)},
		{Pattern: "GET /feed", Handler: http.HandlerFunc(p.handleGetFeed)},
		{Pattern: "GET /auctions", Handler: http.HandlerFunc(p.handleListAuctions)},
		{Pattern: "POST /auctions", Handler: http.HandlerFunc(p.handleAddAuction)},
		{Pattern: "GET /auctions/{id}", Handler: http.HandlerFunc(p.handleGetAuction)},
		{Pattern: "GET /auctions/{id}/index", Handler: http.HandlerFunc(p.handleGetAuctionIndex)},
		{Pattern: "DELETE /auctions/{id}", Handler: http.HandlerFunc(p.handleDeleteAuction)},
		{Pattern: "POST /cleanup", Handler: http.HandlerFunc(p.handleCleanupExpired)},
		{Pattern: "POST /auctions/{id}/scan", Handler: http.HandlerFunc(p.handleScan)},
		{Pattern: "POST /auctions/{id}/refresh", Handler: http.HandlerFunc(p.handleRefresh)},
		{Pattern: "GET /lots", Handler: http.HandlerFunc(p.handleListLots)},
		{Pattern: "GET /locations", Handler: http.HandlerFunc(p.handleListLocations)},
		{Pattern: "GET /lots/{id}", Handler: http.HandlerFunc(p.handleGetLot)},
		{Pattern: "POST /lots/{id}/reprice", Handler: http.HandlerFunc(p.handleReprice)},
		{Pattern: "POST /lots/{id}/enrich", Handler: http.HandlerFunc(p.handleEnrich)},
		{Pattern: "POST /lots/{id}/favorite", Handler: http.HandlerFunc(p.handleFavorite)},
		{Pattern: "DELETE /lots/{id}/favorite", Handler: http.HandlerFunc(p.handleUnfavorite)},
		{Pattern: "GET /favorites", Handler: http.HandlerFunc(p.handleListFavorites)},
		{Pattern: "GET /watchlists", Handler: http.HandlerFunc(p.handleListWatchlists)},
		{Pattern: "POST /watchlists", Handler: http.HandlerFunc(p.handleCreateWatchlist)},
		{Pattern: "PATCH /watchlists/{id}", Handler: http.HandlerFunc(p.handleUpdateWatchlist)},
		{Pattern: "DELETE /watchlists/{id}", Handler: http.HandlerFunc(p.handleDeleteWatchlist)},
		{Pattern: "POST /watchlists/{id}/run", Handler: http.HandlerFunc(p.handleRunWatchlist)},
		{Pattern: "GET /findings", Handler: http.HandlerFunc(p.handleListFindings)},
		{Pattern: "POST /findings/{id}/accept", Handler: http.HandlerFunc(p.handleAcceptFinding)},
		{Pattern: "POST /findings/{id}/reject", Handler: http.HandlerFunc(p.handleRejectFinding)},
		{Pattern: "GET /automation", Handler: http.HandlerFunc(p.handleGetAutomation)},
		{Pattern: "POST /automation/resume", Handler: http.HandlerFunc(p.handleResumeAutomation)},
		{Pattern: "POST /search", Handler: http.HandlerFunc(p.handlePostSearch)},
		{Pattern: "GET /search", Handler: http.HandlerFunc(p.handleGetSearch)},
		{Pattern: "POST /intent", Handler: http.HandlerFunc(p.handlePostIntent)},
		{Pattern: "GET /intent", Handler: http.HandlerFunc(p.handleGetIntent)},
		{Pattern: "GET /sites/auctions", Handler: http.HandlerFunc(p.handleListSites)},
		{Pattern: "POST /sites/refresh", Handler: http.HandlerFunc(p.handleRefreshSites)},
	}
}

func (p *Plugin) Init(_ context.Context, h host.Host) error {
	p.mu.Lock()
	p.h = h
	p.live.p = p
	// The host holds the connections and refcounts the viewers; all this registers is
	// what a lot topic means to BidRL.
	p.unwatch = h.Push().Watch(&p.live)
	p.mu.Unlock()
	h.Log().Info("bidrl ready")
	return nil
}

func (p *Plugin) Shutdown(context.Context) error {
	p.mu.Lock()
	unwatch := p.unwatch
	p.unwatch = nil
	p.h = nil
	p.mu.Unlock()
	if unwatch != nil {
		unwatch()
	}
	// The feed outlives a request by design, so it has to be ended explicitly.
	p.live.close()
	return nil
}

func (p *Plugin) host() (host.Host, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.h, p.h != nil
}
