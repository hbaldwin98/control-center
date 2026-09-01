package pluginhost

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

	br, err := browser.New(bus, pol, browser.Options{
		Engine: browser.NewFake(map[string]http.Handler{
			"www.bidrl.com": bidrl.FakeSite(),
			"bidrl.com":     bidrl.FakeSite(),
		}),
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

	rec = serve(http.MethodDelete, "/api/plugins/bidrl/auctions/42", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.Bytes())
	}
	rec = serve(http.MethodGet, "/api/plugins/bidrl/auctions/42", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("deleted auction still present: %d", rec.Code)
	}
}
