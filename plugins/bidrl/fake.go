package bidrl

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const fakePNG = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\nIDATx\x9cc\x00\x01\x00\x00\x05\x00\x01\r\n-\xb4\x00\x00\x00\x00IEND\xaeB`\x82"

// FakeSite is the in-process BIDRL the fake browser engine serves. Real pages
// need Playwright; this fixture is what tests and `browser.engine: fake` see.
func FakeSite() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auction/42/bidgallery", warehouseGallery)
	mux.HandleFunc("GET /auction/42/bidgallery/", warehouseGallery)
	mux.HandleFunc("GET /auction/42/item/keurig-k-supreme-plus-1001", lotPage("Keurig K-Supreme Plus Coffee Maker", "$15.00"))
	mux.HandleFunc("GET /auction/42/item/office-mesh-chair-1002", lotPage("Office mesh task chair", "$8.00"))
	mux.HandleFunc("GET /auction/42/item/aeron-style-chair-1003", lotPage("Aeron-style mesh chair", "$40.00"))
	mux.HandleFunc("GET /auction/99/item/keurig-mini-9001", lotPage("Keurig Mini", "$12.00"))
	mux.HandleFunc("GET /img/1.png", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = io.WriteString(w, fakePNG)
	})
	mux.HandleFunc("GET /api/affiliatesforhomepage", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]string{{
			"fullname": "Turlock BidRL", "company_name": "Turlock",
			"landing_page_slug": "turlock-19", "do_not_display_tab": "0",
		}})
	})
	mux.HandleFunc("/api/landingPage/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		slug := strings.TrimPrefix(r.URL.Path, "/api/landingPage/")
		if sanitizeAffiliateSlug(slug) != "turlock-19" {
			_, _ = io.WriteString(w, `{"result":"success","auctions":[],"affiliate":{},"total":0}`)
			return
		}
		_, _ = io.WriteString(w, `{
			"result":"success",
			"affiliate":{"affiliate_id":"19","aff_company_name":"Turlock","aff_city":"Turlock","landing_page_slug":"turlock-19"},
			"auctions":{"3":{"id":"42","title":"Test Warehouse Auction","auction_id_slug":"42","item_count":"3","city":"Turlock","auction_group_type":"1"}}
		}`)
	})
	mux.HandleFunc("/allitems/", fakeAllitems)
	mux.HandleFunc("POST /api/ItemData", fakeItemData)
	mux.HandleFunc("/aucbeat/pusher/", fakePusher)
	return mux
}

func warehouseGallery(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, `<!doctype html><html><body>
		<h1>Test Warehouse Auction</h1>
		<article class="lot"><a href="/auction/42/item/keurig-k-supreme-plus-1001">Keurig K-Supreme Plus Coffee Maker</a><span>$15.00</span></article>
		<article class="lot"><a href="/auction/42/item/office-mesh-chair-1002">Office mesh task chair</a><span>$8.00</span></article>
		<article class="lot"><a href="/auction/42/item/aeron-style-chair-1003">Aeron-style mesh chair</a><span>$40.00</span></article>
	</body></html>`)
}

func lotPage(title, bid string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<html><h1>`+title+`</h1><p>Current bid `+bid+`</p><img src="https://www.bidrl.com/img/1.png"></html>`)
	}
}

func fakeItemData(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "form", http.StatusBadRequest)
		return
	}
	itemID := r.Form.Get("item_id")
	auctionID := r.Form.Get("auction_id")
	lot, ok := fakeLots[itemID]
	if !ok {
		http.Error(w, "missing", http.StatusNotFound)
		return
	}
	if auctionID != "" && lot.auctionID != auctionID {
		http.Error(w, "auction", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, lot.itemData)
}

func fakePusher(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/aucbeat/pusher/"), ".json")
	parts := strings.Split(name, "-")
	if len(parts) < 2 {
		http.Error(w, "missing", http.StatusNotFound)
		return
	}
	itemID := parts[len(parts)-1]
	lot, ok := fakeLots[itemID]
	if !ok {
		http.Error(w, "missing", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, lot.pusher)
}

type fakeLot struct {
	auctionID string
	itemData  string
	pusher    string
}

func fakeItemJSON(id, auctionID, title, lotCode, bid, min, endUnix string, bids int, bidder string) string {
	return `{
		"item":{
			"id":"` + id + `","auction_id":"` + auctionID + `","title":"` + title + `","lot_number":"` + lotCode + `",
			"description":"<p>` + title + `</p>","current_bid":"` + bid + `","minimum_bid":"` + min + `",
			"highbidder_username":"` + bidder + `","bid_count":"` + strconv.Itoa(bids) + `","end_time":"` + endUnix + `",
			"time_offset":-7200,"current_increment":"1.00","reserve_met":false,"bidding_extended":false,
			"item_url":"https://www.bidrl.com/auction/` + auctionID + `/item/` + id + `/",
			"images":[{"image_url":"https://www.bidrl.com/img/1.png","thumb_url":"https://www.bidrl.com/img/1.png"}]
		},
		"auction":{"id":"` + auctionID + `","title":"Test Warehouse Auction"}
	}`
}

func fakePusherJSON(bid, min, endUnix string, bids int, bidder string) string {
	return `{"item":{"bid_count":` + strconv.Itoa(bids) + `,"current_bid":` + bid + `,"minimum_bid":` + min + `,
		"high_bidder":"1","highbidder_username":"` + bidder + `","bidding_extended":false,"end_time":"` + endUnix + `",
		"current_increment":1,"reserve_met":false}}`
}

var fakeLots = map[string]fakeLot{
	"1001": {auctionID: "42", itemData: fakeItemJSON("1001", "42", "Keurig K-Supreme Plus Coffee Maker", "K1001", "15.00", "16.00", "1788396360", 4, "goldwing44"), pusher: fakePusherJSON("15.00", "16.00", "1788396360", 4, "goldwing44")},
	"1002": {auctionID: "42", itemData: fakeItemJSON("1002", "42", "Office mesh task chair", "C1002", "8.00", "9.00", "1788396360", 2, "bidder2"), pusher: fakePusherJSON("8.00", "9.00", "1788396360", 2, "bidder2")},
	"1003": {auctionID: "42", itemData: fakeItemJSON("1003", "42", "Aeron-style mesh chair", "C1003", "40.00", "41.00", "1788396360", 6, "bidder3"), pusher: fakePusherJSON("40.00", "41.00", "1788396360", 6, "bidder3")},
	"9001": {auctionID: "99", itemData: fakeItemJSON("9001", "99", "Keurig Mini", "K9001", "12.00", "13.00", "1788396360", 1, "bidder9"), pusher: fakePusherJSON("12.00", "13.00", "1788396360", 1, "bidder9")},
}

func fakeAllitems(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	kw := allitemsKeyword(r.URL.Path)
	type row struct{ href, title, bid string }
	lots := []row{
		{"/auction/42/item/keurig-k-supreme-plus-1001", "Keurig K-Supreme Plus Coffee Maker", "$15.00"},
		{"/auction/42/item/office-mesh-chair-1002", "Office mesh task chair", "$8.00"},
		{"/auction/42/item/aeron-style-chair-1003", "Aeron-style mesh chair", "$40.00"},
		{"/auction/99/item/keurig-mini-9001", "Keurig Mini", "$12.00"},
	}
	var b strings.Builder
	b.WriteString(`<!doctype html><html><body><h1>Currently Open Items</h1>`)
	for _, lot := range lots {
		if kw != "" && !strings.Contains(strings.ToLower(lot.title), strings.ToLower(kw)) {
			continue
		}
		b.WriteString(`<article class="lot"><a href="` + lot.href + `">` + lot.title + `</a><span>` + lot.bid + `</span></article>`)
	}
	b.WriteString(`</body></html>`)
	_, _ = io.WriteString(w, b.String())
}

func allitemsKeyword(path string) string {
	const prefix = "keyword_"
	for _, p := range strings.Split(path, "/") {
		if strings.HasPrefix(p, prefix) {
			k := strings.TrimPrefix(p, prefix)
			k = strings.ReplaceAll(k, "%20", " ")
			k = strings.ReplaceAll(k, "+", " ")
			return k
		}
	}
	return ""
}
