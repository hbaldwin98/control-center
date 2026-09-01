package bidrl

import (
	"encoding/json"
	"io"
	"net/http"
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
