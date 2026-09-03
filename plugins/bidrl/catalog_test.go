package bidrl

import (
	"testing"

	"github.com/hbaldwin98/control-center/host/hosttest"
)

// TestCatalogMatchesThePayloads is the guard against drift: every declared path
// has to resolve against a real payload, and every key a payload carries has to
// be declared. Each sample is fully populated, optional fields included, because
// the catalog promises those paths exist.
func TestCatalogMatchesThePayloads(t *testing.T) {
	t.Parallel()
	bid := int64(1234)
	priced := lotPriced{
		LotID: "lot-1", AuctionID: "auction-1", Title: "A lot", PriceCents: 9900, BidCents: &bid,
		Kind: "sold", SourceURL: "https://example.test/item", SourceClass: "marketplace",
		ReusedFromLotID: "lot-0",
	}
	hosttest.CheckEventCatalog(t, New().Manifest(), map[string]any{
		"alert": alerted{
			Title: "BIDRL finding", Body: "1 new finding", WatchlistID: "w-1", Count: 1,
			LotID: "lot-1", EndsAt: "2026-09-03T00:00:00Z",
		},
		"finding.created":   findingCreated{WatchlistID: "w-1", Count: 2},
		"finding.decided":   findingDecided{FindingID: "f-1", LotID: "lot-1", State: "accepted"},
		"watch.completed":   watchCompleted{WatchlistID: "w-1", Name: "Tools", Findings: 2},
		"sweep.completed":   sweepCompleted{Auctions: 3, Lots: 40},
		"sweep.throttled":   sweepThrottled{Until: "2026-09-02T06:00:00Z"},
		"match.completed":   matchCompleted{Findings: 2, Watchlists: 3, Failed: 1},
		"auction.collected": auctionCollected{AuctionID: "auction-1", URL: "https://example.test/a", Title: "A", LotCount: 12, AffiliateID: "aff-1", AffiliateName: "Turlock", City: "Turlock", EndsAt: "2026-09-03T00:00:00Z"},
		"scan.completed":    scanCompleted{AuctionID: "auction-1"},
		"bids.refreshed":    bidsRefreshed{AuctionID: "auction-1", At: "2026-09-02T00:00:00Z", Lots: 5, Source: "catalog"},
		"lot.enriched":      lotEnriched{LotID: "lot-1", AuctionID: "auction-1"},
		"lot.analyzed":      lotAnalyzed{LotID: "lot-1", AuctionID: "auction-1", Title: "A lot", Identification: "drill", Basis: "photo", Bucket: "priced", Category: "tools"},
		"lot.priced":        priced,
		"deal_found":        priced,
		"search.completed":  searchCompleted{Query: "drill", Scope: "prefer", Hits: 4, Error: "boom"},
		"intent.completed":  intentCompleted{Query: "drill", Hits: 4, Scanned: 40, Skipped: 8, Error: "boom"},
		"sites.discovered":  sitesDiscovered{AuctionCount: 7},
		"expired.cleaned":   expiredCleaned{Auctions: 1, Lots: 20, Sites: 2},
	})
}
