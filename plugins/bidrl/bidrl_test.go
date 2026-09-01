package bidrl

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hbaldwin98/control-center/host"
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

func TestPluginContract(t *testing.T) {
	t.Parallel()
	p := New()
	m := p.Manifest()
	if m.ID != pluginID || m.Automated {
		t.Fatalf("manifest = %#v, want non-automated %q", m, pluginID)
	}
	jobs := p.Jobs()
	if len(jobs) != 4 {
		t.Fatalf("jobs = %d", len(jobs))
	}
	for _, j := range jobs {
		if j.Schedule != "" || j.Concurrency != 1 || j.Timeout != jobTO {
			t.Fatalf("job %s = %#v", j.Name, j)
		}
	}
	if len(p.Routes()) != 9 {
		t.Fatalf("routes = %d", len(p.Routes()))
	}
	if len(p.Subscriptions()) != 1 || p.Subscriptions()[0].Durable == nil {
		t.Fatalf("subscriptions = %#v", p.Subscriptions())
	}
	mig := &captureMigrator{}
	if err := p.Migrate(mig); err != nil {
		t.Fatal(err)
	}
	if len(mig.migrations) != 1 || !strings.Contains(mig.migrations[0].Up, "bidrl_lots") {
		t.Fatalf("migrations = %#v", mig.migrations)
	}
	var defaults map[string]any
	if err := json.Unmarshal(m.Config.Defaults, &defaults); err != nil {
		t.Fatal(err)
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
