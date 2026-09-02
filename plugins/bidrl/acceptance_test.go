package bidrl_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	hostjobs "github.com/hbaldwin98/control-center/host/jobs"

	"github.com/hbaldwin98/control-center/host/hosttest"
	"github.com/hbaldwin98/control-center/plugins/bidrl"
)

// TestCollectsAnalyzesAndPrices walks the whole pipeline: add an auction, collect its
// catalog, analyze the lots, price them, and rank them against a stated intent.
//
// The fake site is the plugin's own FakeSite, served for the real bidrl.com hosts, so
// the URLs under test are the production ones. The models are answered by this
// package's own fake (modelfake_test.go); the assertions below depend on what those
// answers say, which is why they live beside the plugin rather than in the host.
func TestCollectsAnalyzesAndPrices(t *testing.T) {
	ctx := context.Background()

	h := hosttest.New(t, bidrl.New())
	site := bidrl.FakeSite()
	// The catalog refresh is a direct allowlisted request and the lot pages go through
	// the engine; both reach the same site.
	h.Browser.Handle("www.bidrl.com", site)
	h.Browser.Handle("bidrl.com", site)
	installModels(h.AI)
	installSearch(h.Search)
	h.Run(ctx)

	serve := func(method, path string, body []byte) *httptest.ResponseRecorder {
		t.Helper()
		if method == http.MethodGet {
			return h.GET(path)
		}
		req := httptest.NewRequest(method, path, strings.NewReader(string(body)))
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return h.Do(req)
	}

	// waitJob runs the job the request enqueued, then anything that job enqueued in
	// turn. The real queue runs both without being asked; here the test decides when,
	// which is what makes the run deterministic.
	waitJob := func(id int64) {
		t.Helper()
		if err := h.RunJob(ctx, id); err != nil {
			t.Fatalf("job %d failed: %v", id, err)
		}
		if err := h.Drain(ctx); err != nil {
			t.Fatalf("follow-on job failed: %v", err)
		}
	}

	body, _ := json.Marshal(map[string]string{"url": "https://www.bidrl.com/auction/42/bidgallery"})
	rec := serve(http.MethodPost, "/auctions", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("add auction: %d %s", rec.Code, rec.Body.Bytes())
	}
	var posted struct {
		JobID     int64  `json:"jobId"`
		AuctionID string `json:"auctionId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &posted); err != nil || posted.JobID == 0 || posted.AuctionID != "42" {
		t.Fatalf("add body %s", rec.Body.Bytes())
	}
	waitJob(posted.JobID)

	// Opening a lot re-reads that one lot, which is the cheap half of the same trade the
	// auction view makes: one small snapshot for one lot, rather than the whole catalog.
	// The snapshot carries the username outright, so it names the winner where the
	// catalog can only compare ids.
	rec = serve(http.MethodGet, "/lots/1001", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("lot on open: %d %s", rec.Code, rec.Body.Bytes())
	}
	var openedLot struct {
		CurrentBidCents int64  `json:"currentBidCents"`
		BidCount        int    `json:"bidCount"`
		HighBidder      string `json:"highBidder"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &openedLot); err != nil {
		t.Fatal(err)
	}
	if openedLot.CurrentBidCents != 1700 || openedLot.BidCount != 6 {
		t.Fatalf("lot 1001 was not refreshed on open: %+v", openedLot)
	}
	if openedLot.HighBidder != "sniper7" {
		t.Fatalf("lot snapshot did not name the high bidder: %q", openedLot.HighBidder)
	}

	rec = serve(http.MethodGet, "/auctions/42", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get auction: %d %s", rec.Code, rec.Body.Bytes())
	}
	var auction struct {
		Auction struct {
			Title    string `json:"title"`
			LotCount int    `json:"lotCount"`
		} `json:"auction"`
		Lots []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"lots"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &auction); err != nil {
		t.Fatal(err)
	}
	if auction.Auction.Title != "Test Warehouse Auction" || auction.Auction.LotCount != 3 || len(auction.Lots) != 3 {
		t.Fatalf("auction %+v", auction)
	}

	// The lot screen pages through an auction from this, so it has to list every lot in
	// the same order the auction screen does, and carry none of the heavy columns.
	rec = serve(http.MethodGet, "/auctions/42/index", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("auction index: %d %s", rec.Code, rec.Body.Bytes())
	}
	var index struct {
		Title string `json:"title"`
		Lots  []struct {
			ID      string `json:"id"`
			LotCode string `json:"lotCode"`
			Title   string `json:"title"`
		} `json:"lots"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &index); err != nil {
		t.Fatal(err)
	}
	if index.Title != "Test Warehouse Auction" || len(index.Lots) != len(auction.Lots) {
		t.Fatalf("auction index %+v", index)
	}
	for i, entry := range index.Lots {
		if entry.ID != auction.Lots[i].ID {
			t.Fatalf("auction index order: %+v vs %+v", index.Lots, auction.Lots)
		}
	}
	if rec = serve(http.MethodGet, "/auctions/nope/index", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing auction index: %d", rec.Code)
	}

	rec = serve(http.MethodPost, "/auctions/42/scan", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("scan: %d %s", rec.Code, rec.Body.Bytes())
	}
	var scan struct {
		JobID int64 `json:"jobId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &scan); err != nil {
		t.Fatal(err)
	}
	waitJob(scan.JobID)

	// The front page counts in SQL and returns two short lists, so it never ships the lot
	// table to the browser just to count it.
	rec = serve(http.MethodGet, "/overview", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("overview: %d %s", rec.Code, rec.Body.Bytes())
	}
	var overview struct {
		Stats struct {
			Auctions  int `json:"auctions"`
			Lots      int `json:"lots"`
			Scanned   int `json:"scanned"`
			Unscanned int `json:"unscanned"`
			Priced    int `json:"priced"`
		} `json:"stats"`
		Deals []struct {
			ID        string   `json:"id"`
			DealScore *float64 `json:"dealScore"`
		} `json:"deals"`
		Closing []struct {
			ID string `json:"id"`
		} `json:"closing"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &overview); err != nil {
		t.Fatal(err)
	}
	if overview.Stats.Auctions != 1 || overview.Stats.Lots != 3 {
		t.Fatalf("overview stats %+v", overview.Stats)
	}
	if overview.Stats.Scanned+overview.Stats.Unscanned != overview.Stats.Lots {
		t.Fatalf("overview scanned split %+v", overview.Stats)
	}
	if overview.Stats.Priced != 1 || len(overview.Deals) != 1 || overview.Deals[0].ID != "1001" {
		t.Fatalf("overview deals %+v %+v", overview.Stats, overview.Deals)
	}
	if overview.Deals[0].DealScore == nil {
		t.Fatal("overview deal carries no gap")
	}

	rec = serve(http.MethodGet, "/feed?filter=deals", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("feed: %d %s", rec.Code, rec.Body.Bytes())
	}
	var feed struct {
		Lots []struct {
			ID         string   `json:"id"`
			Bucket     string   `json:"bucket"`
			Basis      string   `json:"basis"`
			PriceCents *int64   `json:"priceCents"`
			DealScore  *float64 `json:"dealScore"`
			SourceURL  string   `json:"sourceUrl"`
		} `json:"lots"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &feed); err != nil {
		t.Fatal(err)
	}
	if len(feed.Lots) != 1 || feed.Lots[0].ID != "1001" || feed.Lots[0].Bucket != "priced" {
		t.Fatalf("deals %+v", feed.Lots)
	}
	if feed.Lots[0].PriceCents == nil || *feed.Lots[0].PriceCents != 12900 || !strings.Contains(feed.Lots[0].SourceURL, "ebay.com") {
		t.Fatalf("priced lot %+v", feed.Lots[0])
	}

	rec = serve(http.MethodGet, "/feed?filter=worth_opening", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("worth opening: %d %s", rec.Code, rec.Body.Bytes())
	}
	var worth struct {
		Lots []struct {
			ID     string `json:"id"`
			Bucket string `json:"bucket"`
			Basis  string `json:"basis"`
		} `json:"lots"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &worth); err != nil {
		t.Fatal(err)
	}
	if len(worth.Lots) != 1 || worth.Lots[0].ID != "1003" || worth.Lots[0].Basis != "distinctive_visual_match" {
		t.Fatalf("worth opening %+v", worth.Lots)
	}

	rec = serve(http.MethodGet, "/feed?filter=all", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("all: %d %s", rec.Code, rec.Body.Bytes())
	}
	var all struct {
		Lots []struct {
			ID     string `json:"id"`
			Bucket string `json:"bucket"`
		} `json:"lots"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &all); err != nil {
		t.Fatal(err)
	}
	if len(all.Lots) != 3 {
		t.Fatalf("all lots %+v", all.Lots)
	}

	rec = serve(http.MethodGet, "/lots?bucket=priced", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("lots priced: %d %s", rec.Code, rec.Body.Bytes())
	}
	var priced struct {
		Lots []struct {
			ID string `json:"id"`
		} `json:"lots"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &priced); err != nil {
		t.Fatal(err)
	}
	if len(priced.Lots) != 1 || priced.Lots[0].ID != "1001" {
		t.Fatalf("priced lots %+v", priced.Lots)
	}

	rec = serve(http.MethodGet, "/lots?q=keurig", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("lots q: %d %s", rec.Code, rec.Body.Bytes())
	}
	var found struct {
		Lots []struct {
			ID string `json:"id"`
		} `json:"lots"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &found); err != nil {
		t.Fatal(err)
	}
	if len(found.Lots) != 1 || found.Lots[0].ID != "1001" {
		t.Fatalf("keurig lots %+v", found.Lots)
	}

	rec = serve(http.MethodGet, "/lots?category=appliances", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("lots category: %d %s", rec.Code, rec.Body.Bytes())
	}
	var appliances struct {
		Lots []struct {
			ID string `json:"id"`
		} `json:"lots"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &appliances); err != nil {
		t.Fatal(err)
	}
	if len(appliances.Lots) != 1 || appliances.Lots[0].ID != "1001" {
		t.Fatalf("appliance lots %+v", appliances.Lots)
	}

	rec = serve(http.MethodGet, "/feed?filter=all&q=keurig", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("feed q: %d %s", rec.Code, rec.Body.Bytes())
	}
	var feedQ struct {
		Lots []struct {
			ID string `json:"id"`
		} `json:"lots"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &feedQ); err != nil {
		t.Fatal(err)
	}
	if len(feedQ.Lots) != 1 || feedQ.Lots[0].ID != "1001" {
		t.Fatalf("feed keurig %+v", feedQ.Lots)
	}

	searchBody, _ := json.Marshal(map[string]string{"query": "keurig", "scope": "prefer"})
	rec = serve(http.MethodPost, "/search", searchBody)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("search: %d %s", rec.Code, rec.Body.Bytes())
	}
	var searched struct {
		JobID    int64  `json:"jobId"`
		SearchID string `json:"searchId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &searched); err != nil || searched.JobID == 0 {
		t.Fatalf("search body %s", rec.Body.Bytes())
	}
	waitJob(searched.JobID)

	rec = serve(http.MethodGet, "/search", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get search: %d %s", rec.Code, rec.Body.Bytes())
	}
	var results struct {
		Hits []struct {
			LotID     string `json:"lotId"`
			AuctionID string `json:"auctionId"`
			Preferred bool   `json:"preferred"`
			Collected bool   `json:"collected"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &results); err != nil {
		t.Fatal(err)
	}
	if len(results.Hits) < 2 {
		t.Fatalf("keurig hits %+v", results.Hits)
	}
	if results.Hits[0].LotID != "1001" || !results.Hits[0].Preferred || !results.Hits[0].Collected {
		t.Fatalf("SITES keurig should rank first: %+v", results.Hits)
	}
	var sawOther bool
	for _, h := range results.Hits {
		if h.LotID == "9001" {
			sawOther = true
			if h.Preferred {
				t.Fatalf("non-SITES lot marked preferred: %+v", h)
			}
		}
	}
	if !sawOther {
		t.Fatalf("expected the non-SITES keurig in prefer-scope hits: %+v", results.Hits)
	}

	coffeeBody, _ := json.Marshal(map[string]string{"query": "help me make coffee in the morning"})
	rec = serve(http.MethodPost, "/intent", coffeeBody)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("intent coffee: %d %s", rec.Code, rec.Body.Bytes())
	}
	var intentPosted struct {
		JobID int64 `json:"jobId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &intentPosted); err != nil || intentPosted.JobID == 0 {
		t.Fatalf("intent body %s", rec.Body.Bytes())
	}
	waitJob(intentPosted.JobID)
	rec = serve(http.MethodGet, "/intent", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get intent: %d %s", rec.Code, rec.Body.Bytes())
	}
	var intentPage struct {
		Search struct {
			Query  string `json:"query"`
			Status string `json:"status"`
		} `json:"search"`
		Lots []struct {
			ID          string  `json:"id"`
			MatchReason string  `json:"matchReason"`
			MatchScore  float64 `json:"matchScore"`
		} `json:"lots"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &intentPage); err != nil {
		t.Fatal(err)
	}
	if intentPage.Search.Status != "ready" || len(intentPage.Lots) != 1 || intentPage.Lots[0].ID != "1001" || intentPage.Lots[0].MatchReason == "" {
		t.Fatalf("coffee intent %+v", intentPage)
	}

	sitBody, _ := json.Marshal(map[string]string{"query": "a comfortable place to sit at a desk"})
	rec = serve(http.MethodPost, "/intent", sitBody)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("intent sit: %d %s", rec.Code, rec.Body.Bytes())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &intentPosted); err != nil || intentPosted.JobID == 0 {
		t.Fatalf("sit intent body %s", rec.Body.Bytes())
	}
	waitJob(intentPosted.JobID)
	rec = serve(http.MethodGet, "/intent", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get sit intent: %d %s", rec.Code, rec.Body.Bytes())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &intentPage); err != nil {
		t.Fatal(err)
	}
	if intentPage.Search.Status != "ready" || len(intentPage.Lots) < 1 {
		t.Fatalf("desk seating intent %+v", intentPage)
	}
	for _, lot := range intentPage.Lots {
		if lot.ID == "1001" {
			t.Fatalf("coffee maker should not match seating: %+v", intentPage.Lots)
		}
	}

	// The plugin reads its own clock, so fixture timestamps have to come from the same
	// one. Wall-clock values would sit in the plugin's future and never look expired.
	now := h.Clock.Now().UTC().Format(time.RFC3339Nano)
	if _, err := h.DB().Exec(`INSERT INTO bidrl_lots(id, auction_id, url, lot_code, title, bucket, created_at, description)
		VALUES ('1004', '42', 'https://www.bidrl.com/auction/42/item/tent-1004', 'T1004', '4-person camping tent', 'pending', ?, 'Rainfly, stakes, and a stuff sack')`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := h.DB().Exec(`INSERT INTO bidrl_lots(id, auction_id, url, lot_code, title, bucket, created_at, description)
		VALUES ('1005', '42', 'https://www.bidrl.com/auction/42/item/headlamp-1005', 'T1005', 'Black Diamond LED headlamp', 'pending', ?, '210 lumens, elastic strap')`, now); err != nil {
		t.Fatal(err)
	}

	campBody, _ := json.Marshal(map[string]string{"query": "things that would help me camp"})
	rec = serve(http.MethodPost, "/intent", campBody)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("intent camp: %d %s", rec.Code, rec.Body.Bytes())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &intentPosted); err != nil || intentPosted.JobID == 0 {
		t.Fatalf("camp intent body %s", rec.Body.Bytes())
	}
	waitJob(intentPosted.JobID)
	rec = serve(http.MethodGet, "/intent", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get camp intent: %d %s", rec.Code, rec.Body.Bytes())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &intentPage); err != nil {
		t.Fatal(err)
	}
	if intentPage.Search.Query != "things that would help me camp" || intentPage.Search.Status != "ready" {
		t.Fatalf("camping search %+v", intentPage)
	}
	sawTent, sawLamp := false, false
	for _, lot := range intentPage.Lots {
		if lot.ID == "1001" {
			t.Fatalf("coffee maker should not match camping: %+v", intentPage.Lots)
		}
		if lot.ID == "1004" {
			sawTent = true
		}
		if lot.ID == "1005" {
			sawLamp = true
		}
	}
	if !sawTent || !sawLamp {
		t.Fatalf("camping should match the tent and a headlamp with no camp in the title: %+v", intentPage)
	}

	rec = serve(http.MethodGet, "/sites/auctions", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("sites: %d %s", rec.Code, rec.Body.Bytes())
	}
	var sites struct {
		Auctions []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"auctions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sites); err != nil {
		t.Fatal(err)
	}
	if len(sites.Auctions) != 1 || sites.Auctions[0].ID != "42" {
		t.Fatalf("SITES auctions %+v", sites.Auctions)
	}

	// The live feed is a plugin route, not a job: it answers on the request itself and
	// enqueues nothing. Asking for lots this install does not hold opens the stream and
	// says so, rather than joining an upstream socket for ids a client made up.
	// Opening the auction re-reads the catalog first, so the page shows the current bid
	// without anyone pressing refresh. The fake's catalog is one increment above what
	// collection stored, which is what makes the difference observable.
	rec = serve(http.MethodGet, "/auctions/42", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("auction on open: %d %s", rec.Code, rec.Body.Bytes())
	}
	var opened struct {
		Lots []struct {
			ID              string `json:"id"`
			CurrentBidCents int64  `json:"currentBidCents"`
			BidCount        int    `json:"bidCount"`
			HighBidder      string `json:"highBidder"`
		} `json:"lots"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &opened); err != nil {
		t.Fatal(err)
	}
	if len(opened.Lots) == 0 {
		t.Fatal("auction view served no lots")
	}
	for _, l := range opened.Lots {
		if l.ID != "1001" {
			continue
		}
		if l.CurrentBidCents != 1800 || l.BidCount != 7 {
			t.Fatalf("lot 1001 was not refreshed on open: %+v", l)
		}
		// The catalog names the winner by id only, and it is a different id than the one
		// the lot snapshot just stored, so that username must be gone rather than left
		// describing someone who was outbid.
		if l.HighBidder != "" {
			t.Fatalf("stale high bidder survived a catalog refresh: %q", l.HighBidder)
		}
	}
	// The other side of the same rule: lot 1002's bidder id has not changed since
	// collection recorded it, so the catalog -- which never sends usernames -- keeps
	// the name rather than blanking a bidder who is still winning.
	for _, l := range opened.Lots {
		if l.ID != "1002" {
			continue
		}
		if l.CurrentBidCents != 900 || l.BidCount != 3 {
			t.Fatalf("lot 1002 was not refreshed on open: %+v", l)
		}
		if l.HighBidder != "bidder2" {
			t.Fatalf("high bidder lost while the bidder id was unchanged: %q", l.HighBidder)
		}
	}

	// The live stream must not schedule work; it reads what is already there.
	jobsBefore, err := h.Host().Jobs().List(ctx, hostjobs.Filter{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	rec = serve(http.MethodGet, "/live?lots=no-such-lot", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("live stream: %d %s", rec.Code, rec.Body.Bytes())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("live content type = %q", ct)
	}
	if body := rec.Body.String(); !strings.Contains(body, "event: idle") {
		t.Fatalf("live stream body = %q", body)
	}
	rec = serve(http.MethodGet, "/live", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("live with no lots: %d %s", rec.Code, rec.Body.Bytes())
	}
	jobsAfter, err := h.Host().Jobs().List(ctx, hostjobs.Filter{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobsAfter) != len(jobsBefore) {
		t.Fatalf("live enqueued work: %d jobs before, %d after", len(jobsBefore), len(jobsAfter))
	}

	rec = serve(http.MethodGet, "/auctions", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list auctions: %d %s", rec.Code, rec.Body.Bytes())
	}
	var listed struct {
		Auctions []struct {
			ID            string `json:"id"`
			AffiliateName string `json:"affiliateName"`
			City          string `json:"city"`
		} `json:"auctions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Auctions) != 1 || listed.Auctions[0].ID != "42" || listed.Auctions[0].AffiliateName != "Turlock" {
		t.Fatalf("collected auctions %+v", listed.Auctions)
	}

	past := h.Clock.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := h.DB().Exec(`UPDATE bidrl_lots SET ends_at = ? WHERE id = '1002'`, past); err != nil {
		t.Fatal(err)
	}
	rec = serve(http.MethodPost, "/cleanup", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("cleanup: %d %s", rec.Code, rec.Body.Bytes())
	}
	var cleaned struct {
		Auctions int `json:"auctions"`
		Lots     int `json:"lots"`
		Sites    int `json:"sites"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cleaned); err != nil {
		t.Fatal(err)
	}
	if cleaned.Auctions != 0 || cleaned.Lots != 1 {
		t.Fatalf("cleanup %+v", cleaned)
	}
	rec = serve(http.MethodGet, "/auctions/42", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("auction after lot cleanup: %d %s", rec.Code, rec.Body.Bytes())
	}
	var remaining struct {
		Auction struct {
			LotCount int `json:"lotCount"`
		} `json:"auction"`
		Lots []struct {
			ID string `json:"id"`
		} `json:"lots"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &remaining); err != nil {
		t.Fatal(err)
	}
	if remaining.Auction.LotCount != 4 || len(remaining.Lots) != 4 {
		t.Fatalf("after lot cleanup %+v", remaining)
	}
	sawTent = false
	sawLamp = false
	for _, lot := range remaining.Lots {
		if lot.ID == "1002" {
			t.Fatal("ended lot still present")
		}
		if lot.ID == "1004" {
			sawTent = true
		}
		if lot.ID == "1005" {
			sawLamp = true
		}
	}
	if !sawTent || !sawLamp {
		t.Fatalf("unscanned tent or headlamp missing after cleanup %+v", remaining)
	}

	if _, err := h.DB().Exec(`UPDATE bidrl_affiliate_auctions SET ends_at = ? WHERE id = '42'`, past); err != nil {
		t.Fatal(err)
	}
	rec = serve(http.MethodPost, "/cleanup", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("sites cleanup: %d %s", rec.Code, rec.Body.Bytes())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cleaned); err != nil {
		t.Fatal(err)
	}
	if cleaned.Sites != 1 {
		t.Fatalf("sites cleanup %+v", cleaned)
	}
	rec = serve(http.MethodGet, "/sites/auctions", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("sites after cleanup: %d %s", rec.Code, rec.Body.Bytes())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sites); err != nil {
		t.Fatal(err)
	}
	if len(sites.Auctions) != 0 {
		t.Fatalf("ended SITES listing still present: %+v", sites.Auctions)
	}

	rec = serve(http.MethodDelete, "/auctions/42", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.Bytes())
	}
	rec = serve(http.MethodGet, "/auctions/42", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("deleted auction still present: %d", rec.Code)
	}
}
