package bidrl

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hostai "github.com/hbaldwin98/control-center/host/ai"
	hostsearch "github.com/hbaldwin98/control-center/host/search"
	_ "modernc.org/sqlite"
)

func TestParseAuctionURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		raw, id string
		ok      bool
	}{
		{raw: "https://www.bidrl.com/auction/187375/bidgallery", id: "187375", ok: true},
		{raw: "https://www.bidrl.com/auction/high-end-auction-107453/bidgallery/", id: "107453", ok: true},
		{raw: "https://www.bidrl.com/print_catalog/index/auction/177807", id: "177807", ok: true},
		{raw: "http://www.bidrl.com/auction/1"},
		{raw: "https://evil.test/auction/1"},
		{raw: "https://www.bidrl.com:8443/auction/1"},
		{raw: "https://user@www.bidrl.com/auction/1"},
		{raw: "https://127.0.0.1/auction/1"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			t.Parallel()
			_, _, id, err := parseAuctionURL(tt.raw)
			if tt.ok && err != nil {
				t.Fatalf("parseAuctionURL() error = %v", err)
			}
			if !tt.ok && err == nil {
				t.Fatal("parseAuctionURL() unexpectedly accepted url")
			}
			if id != tt.id {
				t.Fatalf("id = %q, want %q", id, tt.id)
			}
		})
	}
}

func TestParseAuctionHTMLFindsLots(t *testing.T) {
	t.Parallel()
	raw := `<!doctype html><html><body>
		<h1>Test Warehouse Auction</h1>
		<article class="lot"><a href="/auction/42/item/keurig-k-supreme-plus-1001">Keurig K-Supreme Plus</a><span>$15.00</span></article>
		<article class="lot"><a href="/auction/42/item/office-mesh-chair-1002">Office mesh task chair</a><span>$8.00</span></article>
		<article class="lot"><a href="/auction/42/item/aeron-style-chair-1003">Aeron-style mesh chair</a><span>$40.00</span></article>
	</body></html>`
	got := parseAuctionHTML("https://www.bidrl.com/auction/42/bidgallery", raw)
	if got.Title != "Test Warehouse Auction" {
		t.Fatalf("title = %q", got.Title)
	}
	if len(got.Lots) != 3 {
		t.Fatalf("lots = %d, want 3: %+v", len(got.Lots), got.Lots)
	}
	if got.Lots[0].URL != "https://www.bidrl.com/auction/42/item/keurig-k-supreme-plus-1001" {
		t.Fatalf("first url = %q", got.Lots[0].URL)
	}
	if lotIDFromPath(got.Lots[0].URL) != "1001" {
		t.Fatalf("lot id = %q", lotIDFromPath(got.Lots[0].URL))
	}
}

func TestParseLotHTMLImagesAndBid(t *testing.T) {
	t.Parallel()
	raw := `<html><h1>Keurig K-Supreme Plus</h1>
		<p>Current bid $15.00</p>
		<img src="https://www.bidrl.com/img/1.png">
		<img src="https://d3ugkdpeq35ojy.cloudfront.net/photos/2.jpg">
		<img src="https://evil.test/x.png">
	`
	got := parseLotHTML("https://www.bidrl.com/auction/42/item/keurig-1001", raw)
	if got.BidCents == nil || *got.BidCents != 1500 {
		t.Fatalf("bid = %v", got.BidCents)
	}
	if len(got.Images) != 2 {
		t.Fatalf("images = %#v", got.Images)
	}
}

func TestDealAndMislabelScores(t *testing.T) {
	t.Parallel()
	bid := int64(1500)
	d, ok := dealScore(&bid, 12900)
	if !ok || d < 0.8 || d > 0.9 {
		t.Fatalf("dealScore = %v, %t", d, ok)
	}
	if mislabelScore(0.2) != 0.8 {
		t.Fatalf("mislabelScore = %v", mislabelScore(0.2))
	}
}

func TestBucketForBasis(t *testing.T) {
	t.Parallel()
	if bucketFor("exact_text") != "research" {
		t.Fatal(bucketFor("exact_text"))
	}
	if bucketFor("distinctive_visual_match") != "worth_opening" {
		t.Fatal(bucketFor("distinctive_visual_match"))
	}
	if bucketFor("category_only") != "discarded" {
		t.Fatal(bucketFor("category_only"))
	}
}

func TestDecodeAnalysisUsesStructuredJSON(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"identification":"Ninja CREAMi","basis":"exact_text","model_or_sku":"NC301","title_agreement":0.9,"notes":"model plate"}`)
	got, ok := decodeAnalysis(&hostai.ChatResponse{Parsed: raw}, "wrong title")
	if !ok || got.Basis != "exact_text" || got.ModelOrSKU != "NC301" || got.Identification != "Ninja CREAMi" {
		t.Fatalf("ok=%t %+v", ok, got)
	}
}

func TestDecodeAnalysisExtractsJSONFromProse(t *testing.T) {
	t.Parallel()
	got, ok := decodeAnalysis(&hostai.ChatResponse{Text: "Sure.\n```json\n{\"identification\":\"INSE S9X\",\"basis\":\"exact_text\",\"model_or_sku\":\"S9X\",\"title_agreement\":0.8,\"notes\":\"label\"}\n```\n"}, "Cordless Vacuum")
	if !ok || got.Basis != "exact_text" || got.ModelOrSKU != "S9X" {
		t.Fatalf("ok=%t %+v", ok, got)
	}
	got, ok = decodeAnalysis(&hostai.ChatResponse{Text: `Here it is: {"identification":"Ninja CREAMi","basis":"exact_text","model_or_sku":"NC301","title_agreement":0.9,"notes":"plate"}`}, "Ice Cream Maker")
	if !ok || got.Basis != "exact_text" || got.Identification != "Ninja CREAMi" {
		t.Fatalf("embedded object: ok=%t %+v", ok, got)
	}
}

func TestDecodeAnalysisLeavesDefaultWhenTheModelWroteProse(t *testing.T) {
	t.Parallel()
	got, ok := decodeAnalysis(&hostai.ChatResponse{Text: "This looks like a cordless vacuum, probably an INSE."}, "Cordless Vacuum")
	if ok || got.Basis != "category_only" || got.Identification != "Cordless Vacuum" || got.Notes != "" {
		t.Fatalf("prose must not look like a successful identification: ok=%t %+v", ok, got)
	}
}

func TestPluginContract(t *testing.T) {
	t.Parallel()
	p := New()
	m := p.Manifest()
	if m.ID != pluginID {
		t.Fatalf("manifest id = %q, want %q", m.ID, pluginID)
	}
	jobs := p.Jobs()
	if len(jobs) != 11 {
		t.Fatalf("jobs = %d", len(jobs))
	}
	scheduled := map[string]bool{}
	for _, j := range jobs {
		if j.Concurrency != 1 || j.Timeout != jobTO {
			t.Fatalf("job %s = %#v", j.Name, j)
		}
		if j.Schedule == "" {
			continue
		}
		// A scheduled job needs a zone, and only these two may have a schedule at
		// all: everything else stays something a person started.
		if j.TimeZone == "" || len(strings.Fields(j.Schedule)) != 5 {
			t.Fatalf("scheduled job %s = %#v", j.Name, j)
		}
		scheduled[j.Name] = true
	}
	if len(scheduled) != 2 || !scheduled["sweep"] || !scheduled["match"] {
		t.Fatalf("scheduled = %#v", scheduled)
	}
	if !m.Automated {
		t.Fatalf("a plugin with cron must declare Automated")
	}
	if len(p.Routes()) != 34 {
		t.Fatalf("routes = %d", len(p.Routes()))
	}
	if len(p.Subscriptions()) != 1 || p.Subscriptions()[0].Durable == nil {
		t.Fatalf("subscriptions = %#v", p.Subscriptions())
	}
	mig := &captureMigrator{}
	if err := p.Migrate(mig); err != nil {
		t.Fatal(err)
	}
	// Each migration is named by what it must contain, so a merge that drops or
	// reorders one fails here rather than at the next install.
	wantMigrations := []string{
		"bidrl_lots", "bidrl_affiliate_auctions", "itemdata_at", "reused_from_lot_id",
		"bidrl_intent_searches", "bidrl_lot_embeddings", "affiliate_id",
		"bidrl_favorites", "bidrl_watchlists", "strftime", "bidrl_automation",
		"high_bidder_id",
	}
	if len(mig.migrations) != len(wantMigrations) {
		t.Fatalf("migrations = %d, want %d", len(mig.migrations), len(wantMigrations))
	}
	for i, want := range wantMigrations {
		if !strings.Contains(mig.migrations[i].Up, want) {
			t.Fatalf("migration %d (%q) does not mention %q", i+1, mig.migrations[i].Name, want)
		}
	}
	var defaults map[string]any
	if err := json.Unmarshal(m.Config.Defaults, &defaults); err != nil {
		t.Fatal(err)
	}
	if len(m.Models) != 5 || m.Models[4].Name != "watch-judge" || m.Models[2].Name != "intent-expand" || m.Models[2].Capabilities[0] != "chat" || m.Models[3].Name != "intent-match" || m.Models[3].Capabilities[0] != "embed" {
		t.Fatalf("models = %#v", m.Models)
	}
}

type captureMigrator struct{ migrations []host.Migration }

func (m *captureMigrator) Apply(migrations []host.Migration) error {
	m.migrations = migrations
	return nil
}

// itemsFixture is the shape of a real /api/getitems response, trimmed to the fields
// the collector reads.
const itemsFixture = `{
  "total": "196", "page": "2", "perpage": "100",
  "items": [
    {
      "id": "25807462", "auction_id": "191464", "lot_number": "TKD10000",
      "auction_title": "Oversize General Auction - Turlock",
      "title": "Discharge Hose Pump, 3&#34; (Retail $259) ",
      "item_url": "https://www.bidrl.com/auction/191464/item/discharge-hose-pump-25807462/",
      "current_bid": "46.25",
      "images": [
        {"thumb_url": "https://d3ugkdpeq35ojy.cloudfront.net/auctionimages/191464/a_t.jpg",
         "image_url": "https://d3ugkdpeq35ojy.cloudfront.net/auctionimages/191464/a.jpg"},
        {"thumb_url": "https://d3ugkdpeq35ojy.cloudfront.net/auctionimages/191464/b_t.jpg",
         "image_url": "https://d3ugkdpeq35ojy.cloudfront.net/auctionimages/191464/b.jpg"}
      ]
    },
    {
      "id": "1", "lot_number": "TKD10001", "title": "Offsite lot",
      "item_url": "https://evil.test/auction/191464/item/nope-1/",
      "current_bid": "1.00", "images": []
    }
  ]
}`

func TestParseItemsJSON(t *testing.T) {
	t.Parallel()
	lots, title, page, total, ok := parseItemsJSON([]byte(itemsFixture))
	if !ok {
		t.Fatal("parseItemsJSON() rejected a well-formed feed")
	}
	if title != "Oversize General Auction - Turlock" {
		t.Fatalf("title = %q", title)
	}
	if page != 2 || total != 196 {
		t.Fatalf("page/total = %d/%d, want 2/196", page, total)
	}
	if len(lots) != 1 {
		t.Fatalf("got %d lots, want 1 (the off-allowlist lot must be dropped)", len(lots))
	}
	got := lots[0]
	if got.URL != "https://www.bidrl.com/auction/191464/item/discharge-hose-pump-25807462/" {
		t.Fatalf("URL = %q", got.URL)
	}
	if got.Title != `Discharge Hose Pump, 3" (Retail $259)` {
		t.Fatalf("Title = %q", got.Title)
	}
	if got.LotCode != "TKD10000" {
		t.Fatalf("LotCode = %q", got.LotCode)
	}
	if got.BidCents == nil || *got.BidCents != 4625 {
		t.Fatalf("BidCents = %v, want 4625", got.BidCents)
	}
	if got.AuctionID != "191464" {
		t.Fatalf("AuctionID = %q", got.AuctionID)
	}
	want := []string{
		"https://d3ugkdpeq35ojy.cloudfront.net/auctionimages/191464/a.jpg",
		"https://d3ugkdpeq35ojy.cloudfront.net/auctionimages/191464/b.jpg",
	}
	if len(got.Images) != len(want) {
		t.Fatalf("Images = %v, want the full-size urls %v", got.Images, want)
	}
	for i, w := range want {
		if got.Images[i] != w {
			t.Fatalf("Images[%d] = %q, want %q", i, got.Images[i], w)
		}
	}
}

func TestParseItemsJSONRejectsOtherResponses(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{"items":[]}`, `{"total":"3"}`, `[1,2,3]`, `not json`, ``} {
		if _, _, _, _, ok := parseItemsJSON([]byte(body)); ok {
			t.Fatalf("parseItemsJSON(%q) accepted a non-item response", body)
		}
	}
}

func TestGalleryPageURL(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"https://www.bidrl.com/auction/oversize-191464/bidgallery/",
			"https://www.bidrl.com/auction/oversize-191464/bidgallery/perpage_100/page_2/"},
		{"https://www.bidrl.com/auction/191464",
			"https://www.bidrl.com/auction/191464/bidgallery/perpage_100/page_2/"},
	}
	for _, tt := range tests {
		if got := galleryPageURL(tt.in, 2, 100); got != tt.want {
			t.Fatalf("galleryPageURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestIsGetItemsURL(t *testing.T) {
	t.Parallel()
	// The prefix match is deliberate: the liveview gallery posts to /api/getitems/liveview.
	for _, raw := range []string{
		"https://www.bidrl.com/api/getitems",
		"https://www.bidrl.com/api/getitems/liveview",
	} {
		if !isGetItemsURL(raw) {
			t.Fatalf("isGetItemsURL(%q) = false, want true", raw)
		}
	}
	for _, raw := range []string{
		"https://www.bidrl.com/api/auctions",
		"https://www.bidrl.com/api/auctionDetail/191464",
	} {
		if isGetItemsURL(raw) {
			t.Fatalf("isGetItemsURL(%q) = true, want false", raw)
		}
	}
}

func TestExpandQuery(t *testing.T) {
	t.Parallel()
	got := expandQuery("Keurig K-Supreme Plus")
	if len(got) == 0 || got[0] != "Keurig K-Supreme Plus" {
		t.Fatalf("expandQuery = %#v", got)
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "K-Supreme") {
		t.Fatalf("expected a model-token variant in %#v", got)
	}
	if len(expandQuery("office chair")) != 1 {
		t.Fatalf("generic two-word query should stay one variant, got %#v", expandQuery("office chair"))
	}
	if expandQuery("   ") != nil {
		t.Fatal("empty query")
	}
}

func TestMatchScorePrefersIdentification(t *testing.T) {
	t.Parallel()
	titleOnly, _ := matchScore("herman miller aeron", "Office mesh task chair", "", "", "", "")
	photos, reason := matchScore("herman miller aeron", "Office mesh task chair", "Herman Miller Aeron", "", "", "")
	if photos <= titleOnly {
		t.Fatalf("identification should outrank a mismatched title: title=%v photos=%v %s", titleOnly, photos, reason)
	}
	if !strings.Contains(reason, "photos") {
		t.Fatalf("reason = %q", reason)
	}
	model, mReason := matchScore("K-Supreme", "Keurig K-Supreme Plus Coffee Maker", "", "K-Supreme Plus", "", "")
	if model < 3 {
		t.Fatalf("model score = %v (%s)", model, mReason)
	}
}

func TestAffiliateIDFromSlug(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"turlock-19":                 "19",
		"now-bidding-sacramento-72":  "72",
		"galt-the-deal-finder-app-7": "7",
		"19":                         "19",
		"https://evil":               "",
	}
	for in, want := range tests {
		if got := affiliateIDFromSlug(in); got != want {
			t.Fatalf("affiliateIDFromSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseHomeAffiliates(t *testing.T) {
	t.Parallel()
	raw := `[{"company_name":"Turlock","landing_page_slug":"turlock-19","do_not_display_tab":"0"},
		{"company_name":"Hidden","landing_page_slug":"hidden-1","do_not_display_tab":"1"},
		{"company_name":"Nope","landing_page_slug":"https://evil.test","do_not_display_tab":"0"}]`
	got := parseHomeAffiliates([]byte(raw))
	if len(got) != 1 || got[0].Slug != "turlock-19" || got[0].Name != "Turlock" {
		t.Fatalf("%#v", got)
	}
}

func TestParseLandingPage(t *testing.T) {
	t.Parallel()
	raw := `{
		"result":"success",
		"affiliate":{"affiliate_id":"19","aff_company_name":"Turlock","aff_city":"Turlock","landing_page_slug":"turlock-19"},
		"auctions":{
			"3":{"id":"42","title":"Test Warehouse Auction","auction_id_slug":"test-warehouse-42","item_count":"3","city":"Turlock","auction_group_type":"1","ends":"2026-09-01T19:00:00Z"},
			"4":{"id":"99","title":"Info only","auction_group_type":"8","item_count":"0"}
		}
	}`
	got, ok := parseLandingPage("turlock-19", []byte(raw))
	if !ok {
		t.Fatal("parseLandingPage rejected a well-formed page")
	}
	if got.Affiliate.Name != "Turlock" {
		t.Fatalf("affiliate = %#v", got.Affiliate)
	}
	if len(got.Auctions) != 1 || got.Auctions[0].ID != "42" {
		t.Fatalf("auctions = %#v", got.Auctions)
	}
	if got.Auctions[0].URL != "https://www.bidrl.com/auction/test-warehouse-42/bidgallery" {
		t.Fatalf("url = %q", got.Auctions[0].URL)
	}
}

func TestAllitemsURL(t *testing.T) {
	t.Parallel()
	got := allitemsURL("Keurig K-Supreme", 1, 100)
	if got != "https://www.bidrl.com/allitems/keyword_Keurig%20K-Supreme/perpage_100/page_1/" {
		t.Fatalf("allitemsURL = %q", got)
	}
}

func TestMergeHitsRanksSITESFirst(t *testing.T) {
	t.Parallel()
	preferred := map[string]landingAuction{"42": {ID: "42", Affiliate: "19", Name: "Turlock", Title: "Warehouse"}}
	live := []parsedLot{
		{URL: "https://www.bidrl.com/auction/99/item/keurig-mini-9001", Title: "Keurig Mini", AuctionID: "99"},
		{URL: "https://www.bidrl.com/auction/42/item/keurig-k-supreme-plus-1001", Title: "Keurig K-Supreme Plus", AuctionID: "42"},
	}
	hits := mergeHits("keurig", pluginConfig{SearchScope: scopePrefer}, nil, live, preferred)
	if len(hits) != 2 {
		t.Fatalf("hits = %#v", hits)
	}
	if !hits[0].Preferred || hits[0].AuctionID != "42" {
		t.Fatalf("SITES lot should rank first: %#v", hits)
	}
	only := mergeHits("keurig", pluginConfig{SearchScope: scopeOnly}, nil, live, preferred)
	if len(only) != 1 || only[0].AuctionID != "42" {
		t.Fatalf("only scope = %#v", only)
	}
}

func TestParseItemData(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
		"item":{
			"id":"25808125","auction_id":"191465","title":"DeWalt 20V Drill",
			"lot_number":"TKD1","description":"<p>A drill</p>","current_bid":"12.50",
			"minimum_bid":"13.00","highbidder_username":"goldwing44","bid_count":"9",
			"end_time":"1788396360","time_offset":-7200,"current_increment":"0.25","reserve_met":false,
			"item_url":"https://www.bidrl.com/auction/191465/item/dewalt-25808125/",
			"images":[{"image_url":"https://d3ugkdpeq35ojy.cloudfront.net/photos/a.jpg"}]
		},
		"auction":{"id":"191465","title":"Turlock Warehouse"}
	}`)
	got, ok := parseItemData("191465", "25808125", raw)
	if !ok {
		t.Fatal("parseItemData rejected a fat payload")
	}
	if got.Title != "DeWalt 20V Drill" || got.AuctionTitle != "Turlock Warehouse" {
		t.Fatalf("titles %+v", got)
	}
	if got.BidCents == nil || *got.BidCents != 1250 {
		t.Fatalf("bid %v", got.BidCents)
	}
	if got.HighBidder != "goldwing44" || got.BidCount != 9 {
		t.Fatalf("bidder %+v", got)
	}
	if got.EndsAt == "" || !strings.HasPrefix(got.EndsAt, "2026-") {
		t.Fatalf("endsAt %q", got.EndsAt)
	}
	if len(got.Images) != 1 || !strings.Contains(got.Images[0], "cloudfront") {
		t.Fatalf("images %v", got.Images)
	}
	if got.Description != "A drill" {
		t.Fatalf("description %q", got.Description)
	}
}

func TestParseEndTimeMatchesBidRLDisplay(t *testing.T) {
	t.Parallel()
	// Live Turlock ItemData: unix 1788306048, time_offset -7200, end_time_display
	// "Tue, Sep 1, 2026 at 06:40:48 pm PT". Treating the unix value as UTC is two
	// hours early (4:40 PM PDT). BidRL remaining time is end_time - (now + offset).
	raw := []byte(`{
		"id":"25814632","auction_id":"191466","title":"Curtain Track",
		"end_time":"1788306048","time_offset":-7200,"current_bid":"4.26",
		"timezone":"America/Los_Angeles",
		"end_time_display":"Tue, Sep 1, 2026 at 06:40:48 pm  PT"
	}`)
	got, ok := parseItemData("191466", "25814632", raw)
	if !ok {
		t.Fatal("parseItemData rejected a live-shaped payload")
	}
	if got.EndsAt != "2026-09-02T01:40:48Z" {
		t.Fatalf("endsAt %q, want the instant BidRL displays as 6:40:48 PM PT", got.EndsAt)
	}

	// Pusher snapshots omit time_offset; they use the same unix timebase.
	snap, ok := parsePusher([]byte(`{"item":{"end_time":"1788306048","current_bid":4.26,"bid_count":5}}`))
	if !ok {
		t.Fatal("parsePusher rejected a live snapshot")
	}
	if snap.EndsAt != "2026-09-02T01:40:48Z" {
		t.Fatalf("pusher endsAt %q, want the same instant as ItemData", snap.EndsAt)
	}
}

func TestParseEndTimeLandingISOIsUTCWallClock(t *testing.T) {
	t.Parallel()
	// BidRL landing pages stamp a PHP server offset (+0300) onto a UTC wall clock.
	// Honoring that offset is three hours early vs the Google-calendar close time.
	raw := `{
		"result":"success",
		"affiliate":{"affiliate_id":"19","aff_company_name":"Turlock","aff_city":"Turlock","landing_page_slug":"turlock-19"},
		"auctions":{
			"3":{"id":"191466","title":"General Merchandise","auction_id_slug":"191466","item_count":"208","city":"Turlock","auction_group_type":"1","ends":"2026-09-02T01:00:00+0300","last_item_closes":"2026-09-02T03:04:12+0300"}
		}
	}`
	got, ok := parseLandingPage("turlock-19", []byte(raw))
	if !ok || len(got.Auctions) != 1 {
		t.Fatalf("parseLandingPage = %#v ok=%v", got, ok)
	}
	if got.Auctions[0].EndsAt != "2026-09-02T03:04:12Z" {
		t.Fatalf("last_item_closes stored as %q, want UTC wall clock 2026-09-02T03:04:12Z", got.Auctions[0].EndsAt)
	}
}

func TestRewriteStoredEndsAtSQL(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", filepath.ToSlash(filepath.Join(t.TempDir(), "ends.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, stmt := range []string{
		`CREATE TABLE bidrl_lots (id TEXT PRIMARY KEY, ends_at TEXT NOT NULL)`,
		`CREATE TABLE bidrl_auctions (id TEXT PRIMARY KEY, ends_at TEXT NOT NULL)`,
		`CREATE TABLE bidrl_affiliate_auctions (id TEXT PRIMARY KEY, ends_at TEXT NOT NULL)`,
		`INSERT INTO bidrl_lots(id, ends_at) VALUES
			('unix', '2026-09-01T23:40:48Z'),
			('empty', ''),
			('landing', '2026-09-02T03:04:12+0300')`,
		`INSERT INTO bidrl_auctions(id, ends_at) VALUES ('unix', '2026-09-01T22:00:00Z')`,
		`INSERT INTO bidrl_affiliate_auctions(id, ends_at) VALUES
			('offset', '2026-09-02T03:04:12+0300'),
			('z', '2026-09-01T19:00:00Z')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(rewriteStoredEndsAtSQL); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"lots/unix":     "2026-09-02T01:40:48Z",
		"lots/empty":    "",
		"lots/landing":  "2026-09-02T03:04:12Z",
		"auctions/unix": "2026-09-02T00:00:00Z",
		"sites/offset":  "2026-09-02T03:04:12Z",
		"sites/z":       "2026-09-01T19:00:00Z",
	}
	got := map[string]string{}
	scan := func(q, prefix string) {
		t.Helper()
		rows, err := db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id, ends string
			if err := rows.Scan(&id, &ends); err != nil {
				t.Fatal(err)
			}
			got[prefix+"/"+id] = ends
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
	scan(`SELECT id, ends_at FROM bidrl_lots`, "lots")
	scan(`SELECT id, ends_at FROM bidrl_auctions`, "auctions")
	scan(`SELECT id, ends_at FROM bidrl_affiliate_auctions`, "sites")
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
}

func TestParsePusher(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"item":{"bid_count":139,"current_bid":80.93,"minimum_bid":81.18,"high_bidder":"11320","highbidder_username":"goldwing44","bidding_extended":false,"end_time":"1788396360","current_increment":0.25,"reserve_met":false}}`)
	got, ok := parsePusher(raw)
	if !ok {
		t.Fatal("parsePusher rejected live snapshot")
	}
	if got.BidCents == nil || *got.BidCents != 8093 {
		t.Fatalf("bid %v", got.BidCents)
	}
	if got.HighBidder != "goldwing44" || got.BidCount != 139 {
		t.Fatalf("%+v", got)
	}
	if got.EndsAt == "" {
		t.Fatal("missing end time")
	}
}

func TestMatchScoreUsesCategoryAndAliases(t *testing.T) {
	t.Parallel()
	_, reason := matchScore("aeron", "Office mesh", "", "", "furniture", "herman miller aeron")
	if !strings.Contains(reason, "alias") && !strings.Contains(reason, "model") {
		t.Fatalf("reason = %q", reason)
	}
	score, catReason := matchScore("furniture", "Random lot", "", "", "furniture", "")
	if score <= 0 || !strings.Contains(catReason, "category") {
		t.Fatalf("category match %v %q", score, catReason)
	}
}

func TestEndingSoon(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if endingSoon("", now) {
		t.Fatal("empty ends_at is not ending soon")
	}
	if endingSoon(now.Add(-time.Minute).Format(time.RFC3339Nano), now) {
		t.Fatal("already ended is not ending soon")
	}
	if !endingSoon(now.Add(2*time.Hour).Format(time.RFC3339Nano), now) {
		t.Fatal("two hours out should be ending soon")
	}
	if endingSoon(now.Add(48*time.Hour).Format(time.RFC3339Nano), now) {
		t.Fatal("two days out is not ending soon")
	}
}

func TestAuctionEnded(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour).Format(time.RFC3339Nano)
	future := now.Add(time.Hour).Format(time.RFC3339Nano)
	if auctionEnded("", nil, now) {
		t.Fatal("unknown auction with no lots is not ended")
	}
	if auctionEnded("", []string{"", ""}, now) {
		t.Fatal("lots with no end time are not ended")
	}
	if !auctionEnded(past, []string{future}, now) {
		t.Fatal("auction close in the past ends the auction")
	}
	if !auctionEnded("", []string{past, past}, now) {
		t.Fatal("every dated lot closed should end the auction")
	}
	if auctionEnded("", []string{past, future}, now) {
		t.Fatal("a live lot keeps the auction")
	}
}

func TestEvidenceFromRequiresASearchHit(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	hit := hostsearch.Hit{
		URL: "https://example-market.test/k-supreme-plus", Title: "Keurig K-Supreme Plus",
		Snippet: "Sold listing for K-Supreme Plus at $129 used.",
	}
	parsed, _ := json.Marshal(map[string]any{
		"price_cents": 12900, "currency": "USD", "condition": "used", "kind": "sold",
		"model_or_code": "K-Supreme Plus", "source_url": hit.URL,
	})
	resp := &hostai.ChatResponse{Text: hit.Snippet, Parsed: parsed}
	ev, ok := evidenceFrom(resp, "K-Supreme Plus", now, []hostsearch.Hit{hit})
	if !ok || ev.SourceURL != hit.URL || ev.PriceCents != 12900 {
		t.Fatalf("evidence %+v ok=%v", ev, ok)
	}
	if ev.CitedText != hit.Snippet {
		t.Fatalf("cited %q", ev.CitedText)
	}
	if _, ok := evidenceFrom(resp, "K-Supreme Plus", now, nil); ok {
		t.Fatal("no hits must leave the lot unpriced")
	}
	invented, _ := json.Marshal(map[string]any{
		"price_cents": 19900, "currency": "USD", "condition": "used", "kind": "sold",
		"model_or_code": "K-Supreme Plus", "source_url": hit.URL,
	})
	if _, ok := evidenceFrom(&hostai.ChatResponse{Text: "Sold at $199", Parsed: invented}, "K-Supreme Plus", now, []hostsearch.Hit{hit}); ok {
		t.Fatal("price_cents must appear in the search hit, not only in model prose")
	}
}

func TestClassifySourcePrefersEbay(t *testing.T) {
	t.Parallel()
	if got := classifySource("https://www.ebay.com/itm/123"); got.Class != "ebay" || got.Label != "eBay" {
		t.Fatalf("ebay %+v", got)
	}
	if got := classifySource("https://sfbay.craigslist.org/zip/d/drill"); got.Class != "marketplace" || got.Label != "Craigslist" {
		t.Fatalf("craigslist %+v", got)
	}
	if got := classifySource("https://www.amazon.com/dp/B00"); got.Class != "retail" || got.Label != "Amazon" {
		t.Fatalf("amazon %+v", got)
	}
}

func TestLookupComparablesPrefersRetailOverMarketplace(t *testing.T) {
	t.Parallel()
	q := func(_ context.Context, req hostsearch.Request) ([]hostsearch.Hit, error) {
		return []hostsearch.Hit{
			{URL: "https://www.ebay.com/itm/x", Title: "K-Supreme Plus", Snippet: "K-Supreme Plus listing with no dollar amount."},
			{URL: "https://www.mercari.com/k-supreme-plus", Title: "Keurig K-Supreme Plus", Snippet: "K-Supreme Plus for $40 used."},
			{URL: "https://www.amazon.com/dp/k-supreme-plus", Title: "Keurig K-Supreme Plus", Snippet: "K-Supreme Plus for $89."},
		}, nil
	}
	hits, tier, err := lookupComparables(context.Background(), q, "K-Supreme Plus")
	if err != nil || tier.class != "retail" || len(hits) != 1 || !strings.Contains(hits[0].URL, "amazon.com") {
		t.Fatalf("tier=%s hits=%+v err=%v", tier.class, hits, err)
	}
}

func TestLookupComparablesFallsThroughWhenEbayHasNoPrice(t *testing.T) {
	t.Parallel()
	q := func(_ context.Context, req hostsearch.Request) ([]hostsearch.Hit, error) {
		return []hostsearch.Hit{{
			URL: "https://www.mercari.com/k-supreme-plus", Title: "Keurig K-Supreme Plus",
			Snippet: "K-Supreme Plus for $40 used.",
		}}, nil
	}
	hits, tier, err := lookupComparables(context.Background(), q, "K-Supreme Plus")
	if err != nil {
		t.Fatal(err)
	}
	if tier.class != "marketplace" || len(hits) != 1 || hits[0].URL != "https://www.mercari.com/k-supreme-plus" {
		t.Fatalf("tier=%s hits=%+v", tier.class, hits)
	}
}

func TestLookupComparablesStopsAtEbay(t *testing.T) {
	t.Parallel()
	calls := 0
	q := func(_ context.Context, req hostsearch.Request) ([]hostsearch.Hit, error) {
		calls++
		return []hostsearch.Hit{
			{URL: "https://www.amazon.com/dp/k", Title: "Keurig K-Supreme Plus", Snippet: "K-Supreme Plus for $199."},
			{URL: "https://www.ebay.com/itm/k-supreme-plus", Title: "Keurig K-Supreme Plus", Snippet: "Sold listing for K-Supreme Plus at $129 used."},
		}, nil
	}
	hits, tier, err := lookupComparables(context.Background(), q, "K-Supreme Plus")
	if err != nil || tier.class != "ebay" || len(hits) != 1 || calls != 1 {
		t.Fatalf("tier=%s hits=%d calls=%d err=%v", tier.class, len(hits), calls, err)
	}
}

func TestShouldReuseCompSameTypicalModel(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	ok := shouldReuseComp("K-Supreme Plus", "k-supreme plus", now.Add(-2*time.Hour), now,
		[]string{"Keurig K-Supreme Plus"}, []string{"Keurig K-Supreme Plus Coffee Maker"})
	if !ok {
		t.Fatal("typical identical lots should share a comparable")
	}
}

func TestShouldNotReuseImpairedOrStale(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if shouldReuseComp("K-Supreme Plus", "K-Supreme Plus", now.Add(-2*time.Hour), now,
		[]string{"working"}, []string{"Keurig for parts, not working"}) {
		t.Fatal("impaired lot must not reuse a working-unit comparable")
	}
	if shouldReuseComp("K-Supreme Plus", "K-Supreme Plus", now.Add(-8*24*time.Hour), now,
		[]string{"Keurig"}, []string{"Keurig"}) {
		t.Fatal("stale comparable must be looked up again")
	}
	if shouldReuseComp("K-Supreme Plus", "DCD791", now, now, []string{"a"}, []string{"a"}) {
		t.Fatal("different models must not share a comparable")
	}
}

func TestConditionClassAndReuseOrigin(t *testing.T) {
	t.Parallel()
	if conditionClass("nice used keurig") != "typical" {
		t.Fatal("typical")
	}
	if conditionClass("sold for parts only") != "impaired" {
		t.Fatal("impaired")
	}
	if reuseOrigin("1001", "") != "1001" || reuseOrigin("1002", "1001") != "1001" {
		t.Fatal("origin")
	}
}

func TestLookupComparablesRetriesAfterEngineError(t *testing.T) {
	t.Parallel()
	calls := 0
	q := func(_ context.Context, _ hostsearch.Request) ([]hostsearch.Hit, error) {
		calls++
		if calls == 1 {
			return nil, hostsearch.ErrUnavailable
		}
		return []hostsearch.Hit{{
			URL: "https://www.walmart.com/ip/k", Title: "Keurig K-Supreme Plus",
			Snippet: "K-Supreme Plus $79",
		}}, nil
	}
	hits, tier, err := lookupComparables(context.Background(), q, "K-Supreme Plus")
	if err != nil || tier.class != "retail" || len(hits) != 1 || calls != 2 {
		t.Fatalf("tier=%s hits=%+v calls=%d err=%v", tier.class, hits, calls, err)
	}
}

func TestDecodeIntentWordsUsesStructuredJSON(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"item_words":["tent","headlamp","lantern","canopy"]}`)
	got := decodeIntentWords(&hostai.ChatResponse{Parsed: raw})
	if len(got) != 4 || got[1] != "headlamp" {
		t.Fatalf("%+v", got)
	}
}

func TestIntentProbesKeepTheQuerySeparate(t *testing.T) {
	t.Parallel()
	probes := intentProbes("camping", []string{"tent", "headlamp", "camping", "lantern"})
	if probes[0] != "camping" {
		t.Fatalf("typed query must lead: %q", probes)
	}
	if len(probes) != 4 {
		t.Fatalf("one probe per related word, no duplicate of the query: %q", probes)
	}
	for _, p := range probes[1:] {
		if strings.Contains(p, "camping") {
			t.Fatalf("related probes must not be glued to the query: %q", p)
		}
	}
}

func TestIntentProbesAreCapped(t *testing.T) {
	t.Parallel()
	var words []string
	for i := 0; i < 40; i++ {
		words = append(words, fmt.Sprintf("word%d", i))
	}
	if got := len(intentProbes("camping", words)); got != maxIntentProbes {
		t.Fatalf("probes = %d, want %d", got, maxIntentProbes)
	}
}

func TestLexicalAgreementRewardsTitleHits(t *testing.T) {
	t.Parallel()
	words := []string{"tent", "camping"}
	hit := lexicalAgreement(words, intentCard{ID: "1", Title: "4-person camping tent"})
	miss := lexicalAgreement(words, intentCard{ID: "2", Title: "Keurig K-Supreme Plus"})
	if hit <= miss || miss != 0 {
		t.Fatalf("hit=%v miss=%v", hit, miss)
	}
	if hit > 1 {
		t.Fatalf("agreement must stay in 0-1: %v", hit)
	}
}

func TestTrimIntentTailDropsTheLongTail(t *testing.T) {
	t.Parallel()
	in := []intentMatch{
		{ID: "1", Score: 0.90},
		{ID: "2", Score: 0.70},
		{ID: "3", Score: 0.40},
		{ID: "4", Score: 0.32},
	}
	got := trimIntentTail(in, 0.90)
	if len(got) != 2 || got[0].ID != "1" || got[1].ID != "2" {
		t.Fatalf("%+v", got)
	}
	if trimIntentTail(nil, 0) != nil {
		t.Fatal("no matches means no matches")
	}
}

func TestLotDocumentClipsBoilerplateDescription(t *testing.T) {
	t.Parallel()
	c := intentCard{ID: "1", Title: "camping tent", Description: strings.Repeat("boilerplate ", 200)}
	doc := lotDocument(c)
	if !strings.HasPrefix(doc, "camping tent") {
		t.Fatalf("title must lead the document: %q", doc[:40])
	}
	if len(doc) > 2*maxDocDescription {
		t.Fatalf("description was not clipped: %d chars", len(doc))
	}
}

func TestIntentReasonNamesTheBridge(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, query, probe, want string
		card                     intentCard
	}{
		{
			name:  "typed words in the title",
			query: "camping tent", probe: "camping tent",
			card: intentCard{Title: "4-person camping tent"},
			want: `"camping", "tent" in the title`,
		},
		{
			name:  "a related type says why it surfaced",
			query: "camping", probe: "lantern",
			card: intentCard{Title: "Coleman LED Lantern"},
			want: `"lantern" in the title, related to camping`,
		},
		{
			name:  "the photos carried the match",
			query: "herman miller", probe: "herman miller",
			card: intentCard{Title: "office mesh chair", Identification: "Herman Miller Aeron"},
			want: "photos show Herman Miller Aeron",
		},
		{
			name:  "a stored alias carried the match",
			query: "aeron", probe: "aeron",
			card: intentCard{Title: "office chair", Terms: "aeron, task chair"},
			want: `listed as "aeron"`,
		},
		{
			name:  "no shared words at all",
			query: "camping", probe: "sleeping bag",
			card: intentCard{Title: "Ozark Trail bedroll"},
			want: "reads like sleeping bag, related to camping",
		},
		{
			name:  "typed query with nothing to quote",
			query: "camping", probe: "camping",
			card: intentCard{Title: "Ozark Trail bedroll"},
			want: "reads like camping",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := intentReason(tc.query, tc.probe, tc.card); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIntentReasonPrefersTheTitleOverThePhotos(t *testing.T) {
	t.Parallel()
	got := intentReason("camping", "tent", intentCard{
		Title: "camping tent", Identification: "Coleman Sundome tent", Category: "tent",
	})
	if got != `"tent" in the title, related to camping` {
		t.Fatalf("%q", got)
	}
}

func TestKeepIntentMatchesDropsUnknownAndEmpty(t *testing.T) {
	t.Parallel()
	cards := []intentCard{{ID: "1001"}, {ID: "1002"}}
	got := keepIntentMatches([]intentMatch{
		{ID: "1001", Score: 0.9, Reason: "camp cooking"},
		{ID: "1001", Score: 0.7, Reason: "weaker duplicate"},
		{ID: "1002", Score: 0, Reason: "no match"},
		{ID: "9999", Score: 0.99, Reason: "unknown lot"},
		{ID: "1002", Score: 1.4, Reason: "  extra   spaces  "},
	}, cards)
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	if got[0].ID != "1002" || got[0].Score != 1.4 || got[0].Reason != "extra spaces" {
		t.Fatalf("kept high score %+v", got[0])
	}
	if got[1].ID != "1001" || got[1].Reason != "camp cooking" {
		t.Fatalf("kept %+v", got[1])
	}
}

func TestScoreIntentCardTitleIsEnough(t *testing.T) {
	t.Parallel()
	words := []string{"tent", "camping", "stove"}
	score, reason := scoreIntentCard(words, intentCard{ID: "1004", Title: "4-person camping tent"})
	if score <= 0 || reason == "" {
		t.Fatalf("title should match: score=%v reason=%q", score, reason)
	}
	score, _ = scoreIntentCard(words, intentCard{ID: "1001", Title: "Keurig K-Supreme Plus", Identification: "Keurig K-Supreme Plus"})
	if score != 0 {
		t.Fatalf("coffee maker should not match camping words: %v", score)
	}
	score, _ = scoreIntentCard([]string{"camping"}, intentCard{
		ID: "9", Title: "camping gear lot", Identification: "LED television",
	})
	if score <= 0 {
		t.Fatalf("a camping title is enough even when photos say otherwise: %v", score)
	}
}

func TestLotDocumentHashChangesWhenIdentificationArrives(t *testing.T) {
	t.Parallel()
	title := intentCard{ID: "1", Title: "office mesh"}
	scanned := intentCard{ID: "1", Title: "office mesh", Identification: "Herman Miller Aeron"}
	if lotDocument(title) == lotDocument(scanned) {
		t.Fatal("scan should change the embedded document")
	}
	if textHash(lotDocument(title)) == textHash(lotDocument(scanned)) {
		t.Fatal("hash should change after a scan")
	}
}

func TestEncodeVectorRoundTrip(t *testing.T) {
	t.Parallel()
	in := normalizeVector([]float64{3, 4, 0})
	got := decodeVector(encodeVector(in))
	if len(got) != 3 {
		t.Fatalf("%v", got)
	}
	if cosine(in, got) < 0.999 {
		t.Fatalf("round trip %v vs %v", in, got)
	}
	if cosine(in, normalizeVector([]float64{0, 1, 0})) > 0.9 {
		t.Fatal("orthogonal-ish vectors should not look identical")
	}
}

func TestKeepLotEmbeddingDropsEndedAndOrphans(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if keepLotEmbedding(false, now.Add(time.Hour).Format(time.RFC3339Nano), now) {
		t.Fatal("deleted lots must not keep a vector")
	}
	if keepLotEmbedding(true, now.Add(-time.Minute).Format(time.RFC3339Nano), now) {
		t.Fatal("ended lots must not keep a vector")
	}
	if !keepLotEmbedding(true, "", now) {
		t.Fatal("lots with no end time stay searchable")
	}
	if !keepLotEmbedding(true, now.Add(time.Hour).Format(time.RFC3339Nano), now) {
		t.Fatal("open lots keep a vector")
	}
}

func TestFeedWhere(t *testing.T) {
	// The catalog and the feed share these, so a preset naming a column the catalog does
	// not join would break one screen and not the other.
	for _, name := range []string{"", "all", "deals", "worth_opening", "model", "mislabeled", "scanned"} {
		where, ok := feedWhere(name)
		if !ok {
			t.Fatalf("feedWhere(%q) rejected a known preset", name)
		}
		if strings.TrimSpace(where) == "" {
			t.Fatalf("feedWhere(%q) returned an empty predicate", name)
		}
	}
	if _, ok := feedWhere("nonsense"); ok {
		t.Fatal("feedWhere() accepted an unknown preset")
	}
	if where, _ := feedWhere("all"); where != "1=1" {
		t.Fatalf(`feedWhere("all") = %q, want every lot`, where)
	}
}

func TestAffiliateClauseAcceptsSeveralLocations(t *testing.T) {
	// Filtering to a handful of locations is the point: "what is within a drive"
	// is never one location and never all of them.
	clause, args := affiliateClause(url.Values{"affiliate": {"19,7", "turlock-19", "", "3"}})
	if clause != " AND au.affiliate_id IN (?,?,?)" {
		t.Fatalf("clause = %q", clause)
	}
	if len(args) != 3 || args[0] != "19" || args[1] != "7" || args[2] != "3" {
		t.Fatalf("args = %#v", args)
	}
	// No parameter means every location, so links written before the filter
	// existed keep working.
	if clause, args := affiliateClause(url.Values{}); clause != "" || args != nil {
		t.Fatalf("empty = %q %#v", clause, args)
	}
	if clause, _ := affiliateClause(url.Values{"affiliate": {"", ","}}); clause != "" {
		t.Fatalf("junk = %q", clause)
	}
}

func TestCleanupKeepsWhatYouSaved(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	past := "2026-08-30T00:00:00Z"
	future := "2026-09-05T00:00:00Z"

	// An ended auction with nothing saved goes whole.
	plan := cleanupPlan(cleanupAuction{ID: "1", EndsAt: past, Lots: []cleanupLot{
		{ID: "a", EndsAt: past},
		{ID: "b", EndsAt: past},
	}}, now)
	if !plan.DropAuction || plan.Kept != 0 {
		t.Fatalf("ended auction = %#v", plan)
	}

	// One saved lot keeps the auction as a shell, so the lot keeps its photos and
	// comparable, but the auction's other ended lots still go.
	plan = cleanupPlan(cleanupAuction{ID: "2", EndsAt: past, Lots: []cleanupLot{
		{ID: "a", EndsAt: past, Favorite: true},
		{ID: "b", EndsAt: past},
	}}, now)
	if plan.DropAuction || plan.Kept != 1 || len(plan.DropLots) != 1 || plan.DropLots[0] != "b" {
		t.Fatalf("saved auction = %#v", plan)
	}

	// A saved ended lot in a still-open auction is kept too.
	plan = cleanupPlan(cleanupAuction{ID: "3", EndsAt: future, Lots: []cleanupLot{
		{ID: "a", EndsAt: past, Favorite: true},
		{ID: "b", EndsAt: past},
		{ID: "c", EndsAt: future},
	}}, now)
	if plan.DropAuction || plan.Kept != 1 || len(plan.DropLots) != 1 || plan.DropLots[0] != "b" {
		t.Fatalf("open auction = %#v", plan)
	}
}

func TestWatchRulesNarrowsBeforeAnythingCosts(t *testing.T) {
	// Everything decidable in SQL happens before a call is made, and a lot already
	// decided for this watchlist never reaches the funnel again — that is what makes
	// a rejection free to honour on every later run.
	where, args := watchRules(watchlist{ID: "wl-1"})
	if !strings.Contains(where, "bidrl_findings") || len(args) != 1 || args[0] != "wl-1" {
		t.Fatalf("bare = %q %#v", where, args)
	}

	max := int64(8000)
	where, args = watchRules(watchlist{
		ID:           "wl-2",
		AffiliateIDs: []string{"19", "7"},
		Categories:   []string{"outdoor"},
		MaxBidCents:  &max,
	})
	for _, want := range []string{"au.affiliate_id IN (?,?)", "IN (?)", "current_bid_cents IS NULL OR l.current_bid_cents <= ?"} {
		if !strings.Contains(where, want) {
			t.Fatalf("where %q missing %q", where, want)
		}
	}
	if len(args) != 5 || args[1] != "19" || args[2] != "7" || args[3] != "outdoor" || args[4] != max {
		t.Fatalf("args = %#v", args)
	}
}

func TestKeepWatchMatchesAppliesTheFloorAndTheCap(t *testing.T) {
	cards := []intentCard{}
	matches := []intentMatch{}
	for i := 0; i < maxJudged+10; i++ {
		id := fmt.Sprintf("lot-%03d", i)
		cards = append(cards, intentCard{ID: id, Title: id})
		matches = append(matches, intentMatch{ID: id, Score: 2 - float64(i)*0.01})
	}
	// Everything clears the floor here, so the cap is what bounds the spend.
	kept := keepWatchMatches(matches, cards, 0.55)
	if len(kept) != maxJudged {
		t.Fatalf("kept %d, want the judge cap %d", len(kept), maxJudged)
	}

	// With a floor biting first, only what clears it reaches the judge.
	few := []intentCard{{ID: "a", Title: "a"}, {ID: "b", Title: "b"}, {ID: "c", Title: "c"}}
	kept = keepWatchMatches([]intentMatch{
		{ID: "a", Score: 1.4}, {ID: "b", Score: 0.6}, {ID: "c", Score: 0.2},
	}, few, 0.55)
	if len(kept) != 2 || kept[0].ID != "a" || kept[1].ID != "b" {
		t.Fatalf("floor = %#v", kept)
	}
	// A floor above every score keeps nothing, so no judge call is made at all.
	if got := keepWatchMatches(matches, cards, 5); len(got) != 0 {
		t.Fatalf("high floor kept %d", len(got))
	}
}

func TestCleanupKeepsAnUndecidedFinding(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	past := "2026-08-30T00:00:00Z"

	// A finding you have not looked at yet pins its lot, so the queue still holds
	// what the watchlist found while you were asleep.
	plan := cleanupPlan(cleanupAuction{ID: "1", EndsAt: past, Lots: []cleanupLot{
		{ID: "a", EndsAt: past, Pending: true},
		{ID: "b", EndsAt: past},
	}}, now)
	if plan.DropAuction || plan.Kept != 1 || len(plan.DropLots) != 1 || plan.DropLots[0] != "b" {
		t.Fatalf("pending = %#v", plan)
	}

	// Once decided, it is no longer pinned and the auction goes whole.
	plan = cleanupPlan(cleanupAuction{ID: "2", EndsAt: past, Lots: []cleanupLot{
		{ID: "a", EndsAt: past},
	}}, now)
	if !plan.DropAuction {
		t.Fatalf("decided = %#v", plan)
	}
}

func TestThrottleLatchHoldsUntilSomeoneClearsIt(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if throttleHeld("", now) {
		t.Fatal("an unset latch must not hold")
	}
	if !throttleHeld(now.Add(time.Hour).Format(time.RFC3339Nano), now) {
		t.Fatal("a latch in the future must hold")
	}
	if throttleHeld(now.Add(-time.Minute).Format(time.RFC3339Nano), now) {
		t.Fatal("a latch in the past must not hold")
	}
	// Unparseable is treated as not held rather than held forever: a corrupt
	// timestamp must not silently disable automation with no way back.
	if throttleHeld("not a time", now) {
		t.Fatal("an unreadable latch must not hold")
	}
}

func TestPacerLatchIsRecognisable(t *testing.T) {
	// The sweep has to tell "BidRL is refusing" apart from any other failure, because
	// only the first one latches. A string match would rot; the sentinel will not.
	p := newPacer(time.Nanosecond)
	p.backoff = time.Nanosecond
	ctx := context.Background()
	var err error
	for i := 0; i < rateLimitMax; i++ {
		err = p.observe(ctx, 429)
	}
	if !errors.Is(err, errThrottled) {
		t.Fatalf("after %d refusals err = %v, want errThrottled", rateLimitMax, err)
	}
	// A success in between resets the run, so an occasional 403 never latches.
	p2 := newPacer(time.Nanosecond)
	p2.backoff = time.Nanosecond
	_ = p2.observe(ctx, 403)
	_ = p2.observe(ctx, 200)
	_ = p2.observe(ctx, 403)
	if err := p2.observe(ctx, 200); err != nil {
		t.Fatalf("interrupted run err = %v", err)
	}
}

func TestAutomationConfigDefaultsToDoingNothing(t *testing.T) {
	// The default must be inert: turning the plugin on cannot start traffic.
	var zero automationConfig
	got := zero.normalized()
	if got.Enabled {
		t.Fatal("automation must default to off")
	}
	if len(got.AffiliateIDs) != 0 {
		t.Fatalf("locations = %#v, want none", got.AffiliateIDs)
	}
	if got.MaxAuctionsPerSweep != defaultMaxAuctionsPerSweep || got.MaxNewLotsPerSweep != defaultMaxNewLotsPerSweep {
		t.Fatalf("caps = %#v", got)
	}

	// Junk locations are dropped rather than widening the sweep.
	got = automationConfig{AffiliateIDs: []string{"turlock-19", "", "19", "nope"}}.normalized()
	if len(got.AffiliateIDs) != 1 || got.AffiliateIDs[0] != "19" {
		t.Fatalf("locations = %#v", got.AffiliateIDs)
	}
}

func TestMigrationVersionsAreUniqueAndIncreasing(t *testing.T) {
	// Two branches both adding "the next migration" is how a duplicate version gets
	// in, and the host rejects that at startup rather than at review. Catching it
	// here means a merge that collides fails a test instead of an install.
	mig := &captureMigrator{}
	if err := New().Migrate(mig); err != nil {
		t.Fatal(err)
	}
	seen := map[int]string{}
	prev := 0
	for _, m := range mig.migrations {
		if other, dup := seen[m.Version]; dup {
			t.Fatalf("version %d is claimed by both %q and %q", m.Version, other, m.Name)
		}
		if m.Version <= prev {
			t.Fatalf("version %d (%q) does not increase past %d", m.Version, m.Name, prev)
		}
		seen[m.Version] = m.Name
		prev = m.Version
	}
}

func TestParsePusherFrameReadsBroadcastBid(t *testing.T) {
	frame := []byte(`{"event":"bid","channel":"www.bidrl.com-item-1001","data":"{\"item\":{\"id\":1001,\"current_bid\":\"17.00\",\"minimum_bid\":\"18.00\",\"bid_count\":5,\"highbidder_username\":\"goldwing44\",\"end_time\":\"1788396360\",\"bidding_extended\":true}}"}`)
	ev, ok := parsePusherFrame(frame)
	if !ok {
		t.Fatal("frame not parsed")
	}
	if ev.ItemID != "1001" {
		t.Errorf("ItemID = %q, want 1001", ev.ItemID)
	}
	if ev.Snap.BidCents == nil || *ev.Snap.BidCents != 1700 {
		t.Errorf("BidCents = %v, want 1700", ev.Snap.BidCents)
	}
	if ev.Snap.BidCount != 5 || ev.Snap.HighBidder != "goldwing44" {
		t.Errorf("snapshot = %+v", ev.Snap)
	}
	if !ev.Snap.BiddingExt {
		t.Error("bidding_extended lost")
	}
	if ev.Snap.EndsAt == "" {
		t.Error("EndsAt lost")
	}
}

func TestParsePusherFrameAcceptsInlineObjectAndChannelID(t *testing.T) {
	frame := []byte(`{"event":"bid","channel":"www.bidrl.com-item-1002","data":{"item":{"current_bid":"9.00","bid_count":3}}}`)
	ev, ok := parsePusherFrame(frame)
	if !ok {
		t.Fatal("inline object payload not parsed")
	}
	if ev.ItemID != "1002" {
		t.Errorf("ItemID = %q, want 1002 from the channel name", ev.ItemID)
	}
}

func TestParsePusherFrameIgnoresProtocolChatter(t *testing.T) {
	for _, frame := range []string{
		`{"event":"pusher:connection_established","data":"{\"socket_id\":\"1.2\"}"}`,
		`{"event":"pusher_internal:subscription_succeeded","channel":"www.bidrl.com-item-1001","data":"{}"}`,
		`{"event":"pusher:ping","data":"{}"}`,
		`{"event":"minimum_bid","channel":"www.bidrl.com-item-1001","data":"{\"minimum_bid\":\"3.00\"}"}`,
		`{"event":"bid","channel":"www.bidrl.com-item-1001","data":"{\"item\":{}}"}`,
		`not json`,
	} {
		if ev, ok := parsePusherFrame([]byte(frame)); ok {
			t.Errorf("parsePusherFrame(%s) = %+v, want ignored", frame, ev)
		}
	}
}

func TestSubscribeFramesJoinOneChannelPerLot(t *testing.T) {
	frames := subscribeFrames(map[string]string{"1001": "42", "1002": "99"})
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2", len(frames))
	}
	if !strings.Contains(string(frames[0]), `"www.bidrl.com-item-1001"`) {
		t.Errorf("frame = %s", frames[0])
	}
	if !strings.Contains(string(frames[0]), `"pusher:subscribe"`) {
		t.Errorf("frame = %s", frames[0])
	}
}

func TestDedupeIDsKeepsOrderAndCaps(t *testing.T) {
	got := dedupeIDs([]string{" 1001 ", "1002", "1001", "", "  "})
	if len(got) != 2 || got[0] != "1001" || got[1] != "1002" {
		t.Fatalf("dedupeIDs = %q", got)
	}
	many := make([]string, maxLiveLots+50)
	for i := range many {
		many[i] = fmt.Sprint(i)
	}
	if n := len(dedupeIDs(many)); n != maxLiveLots {
		t.Errorf("cap = %d, want %d", n, maxLiveLots)
	}
}

func TestParsePusherFrameReadsCapturedProductionFrames(t *testing.T) {
	frames := []struct {
		raw    string
		lot    string
		cents  int64
		bidder string
		bids   int
	}{
		{`{"event":"bid","data":"{\"item\":{\"current_bid\":\"25.00\",\"bid_count\":36,\"minimum_bid\":25.25,\"high_bidder\":\"54991\",\"highbidder_username\":\"Melface92\",\"bidding_extended\":false,\"end_time\":\"1788312870\",\"current_increment\":\"0.25\",\"reserve_met\":false}}","channel":"www.bidrl.com-item-25814891"}`,
			"25814891", 2500, "Melface92", 36},
		{`{"event":"bid","data":"{\"item\":{\"current_bid\":\"90.53\",\"high_bidder\":\"174351\",\"buyer_number\":\"\",\"bid_count\":344,\"minimum_bid\":90.78,\"highbidder_username\":\"Drakeley\",\"bidding_extended\":false,\"end_time\":\"1788312420\",\"current_increment\":\"0.25\",\"reserve_met\":false}}","channel":"www.bidrl.com-item-25814553"}`,
			"25814553", 9053, "Drakeley", 344},
		{`{"event":"bid","data":"{\"item\":{\"bid_count\":32,\"current_bid\":28.75,\"minimum_bid\":29,\"high_bidder\":\"59396\",\"highbidder_username\":\"Rober\",\"bidding_extended\":false,\"end_time\":\"1788312555\",\"current_increment\":\"0.25\",\"reserve_met\":false}}","channel":"www.bidrl.com-item-25814904"}`,
			"25814904", 2875, "Rober", 32},
	}
	for _, want := range frames {
		ev, ok := parsePusherFrame([]byte(want.raw))
		if !ok {
			t.Fatalf("lot %s: frame not parsed", want.lot)
		}
		if ev.ItemID != want.lot {
			t.Errorf("ItemID = %q, want %q from the channel name", ev.ItemID, want.lot)
		}
		if ev.Snap.BidCents == nil || *ev.Snap.BidCents != want.cents {
			t.Errorf("lot %s: BidCents = %v, want %d", want.lot, ev.Snap.BidCents, want.cents)
		}
		if ev.Snap.HighBidder != want.bidder || ev.Snap.BidCount != want.bids {
			t.Errorf("lot %s: snapshot = %+v", want.lot, ev.Snap)
		}
		if ev.Snap.EndsAt == "" {
			t.Errorf("lot %s: EndsAt lost", want.lot)
		}
	}
}

// The field shapes here are what production actually sends: ids and counts as strings,
// current_bid as a string on one lot and a number on the next, the high bidder as a
// numeric id with no username anywhere, and a per-item time_offset.
func TestParseCatalogBidsReadsEveryLotInOneResponse(t *testing.T) {
	body := []byte(`{"total":"3","page":"1","perpage":"500","total_pages":"1","items":[
		{"id":"25811337","auction_id":"191449","current_bid":"1.00","minimum_bid":"1.25","bid_count":"1",
		 "high_bidder":"54991","end_time":"1788390000","time_offset":-7200,"current_increment":"0.25",
		 "bidding_extended":"0","reserve_met":false},
		{"id":"25814904","auction_id":"191449","current_bid":28.75,"minimum_bid":29,"bid_count":32,
		 "high_bidder":"59396","end_time":"1788312555","time_offset":-7200,"current_increment":"0.25",
		 "bidding_extended":"1","reserve_met":true},
		{"auction_id":"191449","current_bid":"5.00"}
	]}`)
	bids, ok := parseCatalogBids(body)
	if !ok {
		t.Fatal("catalog not parsed")
	}
	// The third item has no id, so there is nothing to write it onto.
	if len(bids.Snapshots) != 2 {
		t.Fatalf("snapshots = %d, want 2", len(bids.Snapshots))
	}
	if bids.Total != 3 || bids.Pages != 1 {
		t.Errorf("total = %d, pages = %d", bids.Total, bids.Pages)
	}

	first := bids.Snapshots["25811337"]
	if first.BidCents == nil || *first.BidCents != 100 || first.BidCount != 1 {
		t.Errorf("first = %+v", first)
	}
	if first.HighBidderID != "54991" {
		t.Errorf("HighBidderID = %q", first.HighBidderID)
	}
	// The catalog never names the bidder, which is what makes the id load-bearing.
	if first.HighBidder != "" {
		t.Errorf("HighBidder = %q, want empty", first.HighBidder)
	}
	if first.EndsAt == "" {
		t.Error("EndsAt lost")
	}

	second := bids.Snapshots["25814904"]
	if second.BidCents == nil || *second.BidCents != 2875 {
		t.Errorf("numeric current_bid = %v", second.BidCents)
	}
	if second.MinBidCents == nil || *second.MinBidCents != 2900 {
		t.Errorf("numeric minimum_bid = %v", second.MinBidCents)
	}
	if !second.BiddingExt || !second.ReserveMet {
		t.Errorf("flags = %+v", second)
	}
}

func TestParseCatalogBidsRejectsUnrelatedJSON(t *testing.T) {
	for _, body := range []string{`{"items":[]}`, `{"total":"3"}`, `not json`, `{"items":[{"auction_id":"1"}]}`} {
		if bids, ok := parseCatalogBids([]byte(body)); ok {
			t.Errorf("parseCatalogBids(%s) = %+v, want rejected", body, bids)
		}
	}
}

// The catalog and the feed carry the same fields under the same names, so both write a
// lot the same way. This pins that: one payload through each parser must agree.
func TestCatalogAndFeedAgreeOnTheSameLot(t *testing.T) {
	catalogBody := []byte(`{"total":"1","total_pages":"1","items":[{"id":"1001","current_bid":"25.00",
		"minimum_bid":"25.25","bid_count":"36","high_bidder":"54991","end_time":"1788312870","time_offset":-7200}]}`)
	feedFrame := []byte(`{"event":"bid","channel":"www.bidrl.com-item-1001","data":"{\"item\":{\"current_bid\":\"25.00\",` +
		`\"minimum_bid\":25.25,\"bid_count\":36,\"high_bidder\":\"54991\",\"highbidder_username\":\"Melface92\",` +
		`\"end_time\":\"1788312870\",\"time_offset\":-7200}}"}`)

	cat, ok := parseCatalogBids(catalogBody)
	if !ok {
		t.Fatal("catalog not parsed")
	}
	ev, ok := parsePusherFrame(feedFrame)
	if !ok {
		t.Fatal("frame not parsed")
	}
	got := cat.Snapshots["1001"]
	if *got.BidCents != *ev.Snap.BidCents || got.BidCount != ev.Snap.BidCount || got.EndsAt != ev.Snap.EndsAt {
		t.Fatalf("catalog %+v disagrees with feed %+v", got.pusherSnapshot, ev.Snap)
	}
	if got.HighBidderID != ev.Snap.HighBidderID {
		t.Errorf("bidder id: catalog %q, feed %q", got.HighBidderID, ev.Snap.HighBidderID)
	}
	// Only the feed can name the bidder.
	if got.HighBidder != "" || ev.Snap.HighBidder != "Melface92" {
		t.Errorf("names: catalog %q, feed %q", got.HighBidder, ev.Snap.HighBidder)
	}
}

func TestLotTopicRoundTripsAndRejectsForeignNames(t *testing.T) {
	if got := lotTopic("1001"); got != "lot:1001" {
		t.Fatalf("lotTopic = %q", got)
	}
	if id, ok := lotFromTopic("lot:1001"); !ok || id != "1001" {
		t.Fatalf("lotFromTopic = %q, %v", id, ok)
	}
	// A topic this plugin does not define must never reach the feed, whatever the
	// host is willing to carry as a name.
	for _, topic := range []string{"", "lot:", "auction:42", "1001", "LOT:1001"} {
		if id, ok := lotFromTopic(topic); ok {
			t.Errorf("lotFromTopic(%q) = %q, want rejected", topic, id)
		}
	}
}

func TestBidMessageCarriesOnlyTheValuesThatMoved(t *testing.T) {
	cents := int64(1800)
	ev := pusherEvent{ItemID: "1001", Snap: pusherSnapshot{
		BidCents: &cents, BidCount: 7, HighBidder: "sniper7", EndsAt: "2026-01-01T00:00:00Z",
	}}
	msg := bidMessage("1001", "42", ev)
	if msg["lotId"] != "1001" || msg["auctionId"] != "42" || msg["currentBidCents"] != cents {
		t.Fatalf("message = %+v", msg)
	}
	// Money is nullable, and a null is not the same as zero on a screen: an absent
	// minimum must stay absent rather than arrive as 0.
	if _, present := msg["minBidCents"]; present {
		t.Fatalf("absent minimum was published as a value: %+v", msg)
	}
}
