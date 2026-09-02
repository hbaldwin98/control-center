package pluginhost

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/ai"
	"github.com/hbaldwin98/control-center/internal/core/browser"
	"github.com/hbaldwin98/control-center/internal/core/credentials"
	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/jobs"
	"github.com/hbaldwin98/control-center/internal/core/policy"
	"github.com/hbaldwin98/control-center/internal/core/search"
	"github.com/hbaldwin98/control-center/internal/core/storage"
	"github.com/hbaldwin98/control-center/plugins/bidrl"
)

func TestBidrlCollectsAnalyzesAndPrices(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := storage.Open(ctx, storage.Options{Path: filepath.Join(dir, "cc.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	blobs, err := storage.NewBlobStore(store, storage.BlobOptions{
		Dir: filepath.Join(dir, "blobs"), MaxObjectBytes: 1 << 20, MaxScopeBytes: 10 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.New(store, store, events.Options{PollInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	bus.Start(ctx)
	t.Cleanup(bus.Stop)

	pol, err := policy.New(store, store, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	q, err := jobs.New(store, store, bus, pol, jobs.Options{
		PollInterval: 20 * time.Millisecond, LeaseTTL: time.Second, Heartbeat: 40 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	q.Start(ctx)
	t.Cleanup(q.Stop)

	key := make([]byte, 32)
	_, _ = rand.Read(key)
	creds, err := credentials.New(store, store, bus, credentials.Options{Keys: map[int][]byte{1: key}, Active: 1})
	if err != nil {
		t.Fatal(err)
	}
	actx := credentials.WithActor(ctx, "admin")
	if _, err := creds.CreateAPIKey(actx, credentials.APIKeyInput{
		ID: "fake-key", Provider: "fake", Secret: credentials.SecretInput{Value: "test-token"},
	}); err != nil {
		t.Fatal(err)
	}

	models := filepath.Join(dir, "models.yaml")
	if err := os.WriteFile(models, []byte(`
routes:
  cheap-vision:
    capabilities: [chat, vision]
    maxInputTokens: 1024
    maxOutputTokens: 256
    attempts:
      - provider: fake
        model: echo
        credential: fake-key
        inputMicroUSDPerMillion: 1000000
        outputMicroUSDPerMillion: 2000000
  grounded-price:
    capabilities: [chat]
    maxInputTokens: 512
    maxOutputTokens: 256
    attempts:
      - provider: fake
        model: echo
        credential: fake-key
        inputMicroUSDPerMillion: 1000000
        outputMicroUSDPerMillion: 2000000
  intent-expand:
    capabilities: [chat]
    maxInputTokens: 512
    maxOutputTokens: 256
    attempts:
      - provider: fake
        model: echo
        credential: fake-key
        inputMicroUSDPerMillion: 1000000
        outputMicroUSDPerMillion: 2000000
  intent-match:
    capabilities: [embed]
    maxInputTokens: 8192
    maxOutputTokens: 1
    attempts:
      - provider: fake
        model: echo
        credential: fake-key
        inputMicroUSDPerMillion: 1000000
        outputMicroUSDPerMillion: 0
`), 0o600); err != nil {
		t.Fatal(err)
	}
	seed, err := ai.LoadSeed(models)
	if err != nil {
		t.Fatal(err)
	}
	aisvc, err := ai.New(store, store, bus, pol, creds, ai.Options{Seed: seed, Refs: creds})
	if err != nil {
		t.Fatal(err)
	}

	// The catalog refresh is a direct allowlisted request, which does not go through
	// the engine, so the fake site has to be reachable both ways: as pages the engine
	// serves, and as an origin the direct client reaches.
	site := httptest.NewServer(bidrl.FakeSite())
	t.Cleanup(site.Close)
	siteURL, err := url.Parse(site.URL)
	if err != nil {
		t.Fatal(err)
	}
	siteClient := site.Client()
	siteTransport := siteClient.Transport
	siteClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = siteURL.Scheme
		clone.URL.Host = siteURL.Host
		return siteTransport.RoundTrip(clone)
	})

	br, err := browser.New(bus, pol, browser.Options{
		Engine: browser.NewFake(map[string]http.Handler{
			"www.bidrl.com": bidrl.FakeSite(),
			"bidrl.com":     bidrl.FakeSite(),
		}),
		Client: siteClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(br.Close)

	searchsvc := search.New(pol, search.Options{Engine: search.Fake{}})

	reg, err := New(ctx, store, Options{
		DB: store, Blobs: blobs, Events: bus, Policy: pol, Jobs: q, AI: aisvc, Browser: br,
		Search: searchsvc, Creds: creds, Refs: creds, ShutdownTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterAll(bidrl.New()); err != nil {
		t.Fatal(err)
	}
	if err := reg.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Stop(context.Background()) })

	if err := pol.SetBudget(ctx, "bidrl", policy.Budget{Daily: 50_000_000, OnExceed: policy.ExceedReject}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Enable(ctx, "bidrl", "test", "acceptance"); err != nil {
		t.Fatal(err)
	}

	serve := func(method, path string, body []byte) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		reg.ServeHTTP(rec, req)
		return rec
	}
	waitJob := func(id int64) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		var last *jobs.Job
		for time.Now().Before(deadline) {
			j, err := q.Get(ctx, id)
			if err == nil {
				last = j
				if j.State == jobs.StateSucceeded {
					return
				}
				if j.State == jobs.StateFailed || j.State == jobs.StateDead {
					t.Fatalf("job %d %s: %s", id, j.State, j.LastError)
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		if last == nil {
			t.Fatalf("job %d never appeared", id)
		}
		t.Fatalf("job %d state %s: %s", id, last.State, last.LastError)
	}

	body, _ := json.Marshal(map[string]string{"url": "https://www.bidrl.com/auction/42/bidgallery"})
	rec := serve(http.MethodPost, "/api/plugins/bidrl/auctions", body)
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
	rec = serve(http.MethodGet, "/api/plugins/bidrl/lots/1001", nil)
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

	rec = serve(http.MethodGet, "/api/plugins/bidrl/auctions/42", nil)
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
	rec = serve(http.MethodGet, "/api/plugins/bidrl/auctions/42/index", nil)
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
	if rec = serve(http.MethodGet, "/api/plugins/bidrl/auctions/nope/index", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing auction index: %d", rec.Code)
	}

	rec = serve(http.MethodPost, "/api/plugins/bidrl/auctions/42/scan", nil)
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
	rec = serve(http.MethodGet, "/api/plugins/bidrl/overview", nil)
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

	rec = serve(http.MethodGet, "/api/plugins/bidrl/feed?filter=deals", nil)
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

	rec = serve(http.MethodGet, "/api/plugins/bidrl/feed?filter=worth_opening", nil)
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

	rec = serve(http.MethodGet, "/api/plugins/bidrl/feed?filter=all", nil)
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

	rec = serve(http.MethodGet, "/api/plugins/bidrl/lots?bucket=priced", nil)
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

	rec = serve(http.MethodGet, "/api/plugins/bidrl/lots?q=keurig", nil)
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

	rec = serve(http.MethodGet, "/api/plugins/bidrl/lots?category=appliances", nil)
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

	rec = serve(http.MethodGet, "/api/plugins/bidrl/feed?filter=all&q=keurig", nil)
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
	rec = serve(http.MethodPost, "/api/plugins/bidrl/search", searchBody)
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

	rec = serve(http.MethodGet, "/api/plugins/bidrl/search", nil)
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
	rec = serve(http.MethodPost, "/api/plugins/bidrl/intent", coffeeBody)
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
	rec = serve(http.MethodGet, "/api/plugins/bidrl/intent", nil)
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
	rec = serve(http.MethodPost, "/api/plugins/bidrl/intent", sitBody)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("intent sit: %d %s", rec.Code, rec.Body.Bytes())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &intentPosted); err != nil || intentPosted.JobID == 0 {
		t.Fatalf("sit intent body %s", rec.Body.Bytes())
	}
	waitJob(intentPosted.JobID)
	rec = serve(http.MethodGet, "/api/plugins/bidrl/intent", nil)
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

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.Exec(ctx, `INSERT INTO bidrl_lots(id, auction_id, url, lot_code, title, bucket, created_at, description)
		VALUES ('1004', '42', 'https://www.bidrl.com/auction/42/item/tent-1004', 'T1004', '4-person camping tent', 'pending', ?, 'Rainfly, stakes, and a stuff sack')`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Exec(ctx, `INSERT INTO bidrl_lots(id, auction_id, url, lot_code, title, bucket, created_at, description)
		VALUES ('1005', '42', 'https://www.bidrl.com/auction/42/item/headlamp-1005', 'T1005', 'Black Diamond LED headlamp', 'pending', ?, '210 lumens, elastic strap')`, now); err != nil {
		t.Fatal(err)
	}

	campBody, _ := json.Marshal(map[string]string{"query": "things that would help me camp"})
	rec = serve(http.MethodPost, "/api/plugins/bidrl/intent", campBody)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("intent camp: %d %s", rec.Code, rec.Body.Bytes())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &intentPosted); err != nil || intentPosted.JobID == 0 {
		t.Fatalf("camp intent body %s", rec.Body.Bytes())
	}
	waitJob(intentPosted.JobID)
	rec = serve(http.MethodGet, "/api/plugins/bidrl/intent", nil)
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

	rec = serve(http.MethodGet, "/api/plugins/bidrl/sites/auctions", nil)
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
	rec = serve(http.MethodGet, "/api/plugins/bidrl/auctions/42", nil)
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

	jobsBefore, err := q.List(ctx, jobs.Filter{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	rec = serve(http.MethodGet, "/api/plugins/bidrl/live?lots=no-such-lot", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("live stream: %d %s", rec.Code, rec.Body.Bytes())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("live content type = %q", ct)
	}
	if body := rec.Body.String(); !strings.Contains(body, "event: idle") {
		t.Fatalf("live stream body = %q", body)
	}
	rec = serve(http.MethodGet, "/api/plugins/bidrl/live", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("live with no lots: %d %s", rec.Code, rec.Body.Bytes())
	}
	jobsAfter, err := q.List(ctx, jobs.Filter{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobsAfter) != len(jobsBefore) {
		t.Fatalf("live enqueued work: %d jobs before, %d after", len(jobsBefore), len(jobsAfter))
	}

	rec = serve(http.MethodGet, "/api/plugins/bidrl/auctions", nil)
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

	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := store.Exec(ctx, `UPDATE bidrl_lots SET ends_at = ? WHERE id = '1002'`, past); err != nil {
		t.Fatal(err)
	}
	rec = serve(http.MethodPost, "/api/plugins/bidrl/cleanup", nil)
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
	rec = serve(http.MethodGet, "/api/plugins/bidrl/auctions/42", nil)
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

	if _, err := store.Exec(ctx, `UPDATE bidrl_affiliate_auctions SET ends_at = ? WHERE id = '42'`, past); err != nil {
		t.Fatal(err)
	}
	rec = serve(http.MethodPost, "/api/plugins/bidrl/cleanup", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("sites cleanup: %d %s", rec.Code, rec.Body.Bytes())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cleaned); err != nil {
		t.Fatal(err)
	}
	if cleaned.Sites != 1 {
		t.Fatalf("sites cleanup %+v", cleaned)
	}
	rec = serve(http.MethodGet, "/api/plugins/bidrl/sites/auctions", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("sites after cleanup: %d %s", rec.Code, rec.Body.Bytes())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sites); err != nil {
		t.Fatal(err)
	}
	if len(sites.Auctions) != 0 {
		t.Fatalf("ended SITES listing still present: %+v", sites.Auctions)
	}

	rec = serve(http.MethodDelete, "/api/plugins/bidrl/auctions/42", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.Bytes())
	}
	rec = serve(http.MethodGet, "/api/plugins/bidrl/auctions/42", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("deleted auction still present: %d", rec.Code)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
