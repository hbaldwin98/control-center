package bidrl

import "github.com/hbaldwin98/control-center/host"

// The payloads BidRL publishes. Every event is a struct rather than an inline
// map so the catalog below can be derived from the thing that actually ships:
// host.Fields reads names and types off these structs and pairs them with a
// purpose. A field with no purpose loads with an empty one, which the host
// rejects, so an undescribed payload field fails at startup instead of reaching
// the catalog as a mystery key.

type lotAnalyzed struct {
	LotID          string `json:"lotId"`
	AuctionID      string `json:"auctionId"`
	Title          string `json:"title"`
	Identification string `json:"identification"`
	Basis          string `json:"basis"`
	Bucket         string `json:"bucket"`
	Category       string `json:"category"`
}

type lotEnriched struct {
	LotID     string `json:"lotId"`
	AuctionID string `json:"auctionId"`
}

// lotPriced backs both lot.priced and deal_found: a deal is the same valuation,
// republished under a type a rule can match on its own.
type lotPriced struct {
	LotID           string `json:"lotId"`
	AuctionID       string `json:"auctionId"`
	Title           string `json:"title"`
	PriceCents      int64  `json:"priceCents"`
	BidCents        *int64 `json:"bidCents"`
	Kind            string `json:"kind"`
	SourceURL       string `json:"sourceUrl"`
	SourceClass     string `json:"sourceClass"`
	ReusedFromLotID string `json:"reusedFromLotId"`
}

type auctionCollected struct {
	AuctionID     string `json:"auctionId"`
	URL           string `json:"url"`
	Title         string `json:"title"`
	LotCount      int    `json:"lotCount"`
	AffiliateID   string `json:"affiliateId"`
	AffiliateName string `json:"affiliateName"`
	City          string `json:"city"`
	EndsAt        string `json:"endsAt"`
}

type scanCompleted struct {
	AuctionID string `json:"auctionId"`
}

type bidsRefreshed struct {
	AuctionID string `json:"auctionId"`
	At        string `json:"at"`
	Lots      int    `json:"lots"`
	Source    string `json:"source"`
}

type sitesDiscovered struct {
	AuctionCount int `json:"auctionCount"`
}

type sweepCompleted struct {
	Auctions int `json:"auctions"`
	Lots     int `json:"lots"`
}

type sweepThrottled struct {
	Until string `json:"until"`
}

type matchCompleted struct {
	Findings   int `json:"findings"`
	Watchlists int `json:"watchlists"`
	Failed     int `json:"failed"`
}

type expiredCleaned struct {
	Auctions int `json:"auctions"`
	Lots     int `json:"lots"`
	Sites    int `json:"sites"`
	Hidden   int `json:"hidden"`
}

type searchCompleted struct {
	Query string `json:"query"`
	Scope string `json:"scope"`
	Hits  int    `json:"hits"`
	Error string `json:"error,omitempty"`
}

type intentCompleted struct {
	Query   string `json:"query"`
	Hits    int    `json:"hits"`
	Scanned int    `json:"scanned"`
	Skipped int    `json:"skipped"`
	Error   string `json:"error,omitempty"`
}

type watchCompleted struct {
	WatchlistID string `json:"watchlistId"`
	Name        string `json:"name"`
	Findings    int    `json:"findings"`
}

type findingCreated struct {
	WatchlistID string `json:"watchlistId"`
	Count       int    `json:"count"`
}

type findingDecided struct {
	FindingID string `json:"findingId"`
	LotID     string `json:"lotId"`
	State     string `json:"state"`
}

// alerted is the one alert shape. Two situations raise it — a favorited lot
// closing soon, and a watchlist finding — so each fills the fields it has and
// leaves the rest empty, rather than publishing a second shape under the same
// type.
type alerted struct {
	Title       string `json:"title"`
	Body        string `json:"body"`
	WatchlistID string `json:"watchlistId,omitempty"`
	Count       int    `json:"count,omitempty"`
	LotID       string `json:"lotId,omitempty"`
	EndsAt      string `json:"endsAt,omitempty"`
}

func publishedEvents() []host.EventSpec {
	return []host.EventSpec{
		event("alert", "A new watchlist finding, or a saved lot entering one configured closing window. Matched by the default plugin-alert rule.",
			alerted{}, map[string]string{
				"title":       "Short headline.",
				"body":        "What happened, ready to send.",
				"watchlistId": "Watchlist that produced findings, when this is a finding alert.",
				"count":       "How many new findings, when this is a finding alert.",
				"lotId":       "The lot, when this is an ending-soon alert.",
				"endsAt":      "When that lot closes.",
			}),
		event("finding.created", "A watchlist produced new lots that still need a decision.",
			findingCreated{}, map[string]string{
				"watchlistId": "The watchlist that matched.",
				"count":       "How many new findings this run.",
			}),
		event("finding.decided", "Someone accepted or rejected a finding.",
			findingDecided{}, map[string]string{
				"findingId": "The finding that was decided.",
				"lotId":     "The lot on that finding.",
				"state":     "accepted or rejected.",
			}),
		event("watch.completed", "One watchlist finished matching against stored lots.",
			watchCompleted{}, map[string]string{
				"watchlistId": "The watchlist that ran.",
				"name":        "Display name of the watchlist.",
				"findings":    "New findings this run.",
			}),
		event("sweep.completed", "A scheduled sweep finished collecting auctions.",
			sweepCompleted{}, map[string]string{
				"auctions": "Auctions collected.",
				"lots":     "Lots stored.",
			}),
		event("sweep.throttled", "BidRL refused requests; the sweep is latched until a later tick.",
			sweepThrottled{}, map[string]string{
				"until": "RFC3339Nano time after which the next sweep may try again.",
			}),
		event("match.completed", "Every enabled watchlist finished a match pass.",
			matchCompleted{}, map[string]string{
				"findings":   "New findings across watchlists.",
				"watchlists": "How many watchlists ran.",
				"failed":     "How many watchlists errored.",
			}),
		event("auction.collected", "An auction the sweep had not stored before was collected: its lots are now stored.",
			auctionCollected{}, map[string]string{
				"auctionId":     "The auction.",
				"url":           "Canonical auction URL.",
				"title":         "Auction title.",
				"lotCount":      "Lots stored.",
				"affiliateId":   "BidRL location the auction belongs to.",
				"affiliateName": "Display name of that location.",
				"city":          "City of that location.",
				"endsAt":        "When the auction closes.",
			}),
		event("scan.completed", "Vision, enrichment, and pricing for one auction finished.",
			scanCompleted{}, map[string]string{
				"auctionId": "The auction that was scanned.",
			}),
		event("bids.refreshed", "Live catalog bids were written onto stored lots.",
			bidsRefreshed{}, map[string]string{
				"auctionId": "The auction whose bids were refreshed.",
				"at":        "RFC3339Nano time of the refresh.",
				"lots":      "Lots whose bid changed.",
				"source":    "Where the bids came from.",
			}),
		event("lot.enriched", "Photographs for one lot were stored.",
			lotEnriched{}, map[string]string{
				"lotId":     "The lot.",
				"auctionId": "The auction it belongs to.",
			}),
		event("lot.analyzed", "Vision identified what a lot's photographs show.",
			lotAnalyzed{}, map[string]string{
				"lotId":          "The lot.",
				"auctionId":      "The auction it belongs to.",
				"title":          "Auction title as listed.",
				"identification": "What the model said the photographs show.",
				"basis":          "How confident the identification is.",
				"bucket":         "priced, needs_price, or rejected.",
				"category":       "Closed category the model assigned.",
			}),
		event("lot.priced", "A cited marketplace price was written for a lot.",
			lotPriced{}, pricedPurposes()),
		event("deal_found", "The cited price is enough of a discount relative to the current bid to be interesting.",
			lotPriced{}, pricedPurposes()),
		event("search.completed", "A title search against stored lots finished.",
			searchCompleted{}, map[string]string{
				"query": "What was searched.",
				"scope": "prefer, only, or all.",
				"hits":  "Lots returned.",
				"error": "Why the search failed, on the failure event.",
			}),
		event("intent.completed", "An intent search ranked stored lots by meaning.",
			intentCompleted{}, map[string]string{
				"query":   "The stated intent.",
				"hits":    "Lots kept.",
				"scanned": "Lots considered.",
				"skipped": "Lots excluded by free rules.",
				"error":   "Why the search failed, on the failure event.",
			}),
		event("sites.discovered", "Open SITES auctions were listed from BidRL's locations menu.",
			sitesDiscovered{}, map[string]string{
				"auctionCount": "Auctions found.",
			}),
		event("expired.cleaned", "Closed auctions and their lots were removed.",
			expiredCleaned{}, map[string]string{
				"auctions": "Auctions removed.",
				"lots":     "Lots removed.",
				"sites":    "SITES rows removed.",
				"hidden":   "Ended auctions hidden because something saved still points at them.",
			}),
	}
}

func pricedPurposes() map[string]string {
	return map[string]string{
		"lotId":           "The lot.",
		"auctionId":       "The auction it belongs to.",
		"title":           "Auction title as listed.",
		"priceCents":      "Cited price in cents.",
		"bidCents":        "Current bid in cents.",
		"kind":            "How the price was obtained.",
		"sourceUrl":       "Page the price was cited from.",
		"sourceClass":     "What kind of site that was.",
		"reusedFromLotId": "Earlier lot the price was copied from, if any.",
	}
}

// event pairs a payload struct with the purposes of its fields. The struct is
// the source of names and types, so the two cannot disagree.
func event(typ, purpose string, payload any, purposes map[string]string) host.EventSpec {
	return host.EventSpec{Type: typ, Purpose: purpose, Fields: host.Fields(payload, purposes)}
}
