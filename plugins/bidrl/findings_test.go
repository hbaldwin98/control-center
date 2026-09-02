package bidrl_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hbaldwin98/control-center/host/hosttest"
)

// ------------------------------------------------------------------ enrichment

// enrichJob re-reads one lot from BidRL's own item endpoint, which carries fields the
// catalog HTML does not: the lot code, the bid count, the high bidder, and the photos.
func TestEnrichJobUpdatesTheLotFromItemData(t *testing.T) {
	ctx := context.Background()
	h := newAutomationHarness(t, ctx, autoConfig{Enabled: true, AffiliateIDs: []string{"19"}})

	if err := h.RunJobNow(ctx, "sweep", nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if err := h.Drain(ctx); err != nil {
		t.Fatalf("follow-on job: %v", err)
	}
	// Blank the columns enrichment is responsible for, so the assertion cannot pass on
	// what the catalog collection already wrote.
	if _, err := h.DB().Exec(`UPDATE bidrl_lots SET lot_code = '', bid_count = 0, high_bidder = '', itemdata_at = '' WHERE id = '1001'`); err != nil {
		t.Fatal(err)
	}

	if err := h.RunJobNow(ctx, "enrich", map[string]string{"lotId": "1001"}); err != nil {
		t.Fatalf("enrich: %v", err)
	}

	var lotCode, highBidder, at string
	var bidCount int
	if err := h.DB().QueryRow(`SELECT lot_code, bid_count, high_bidder, itemdata_at FROM bidrl_lots WHERE id = '1001'`).
		Scan(&lotCode, &bidCount, &highBidder, &at); err != nil {
		t.Fatal(err)
	}
	if bidCount == 0 || highBidder == "" || at == "" {
		t.Fatalf("enrich did not write the item fields: code=%q bids=%d bidder=%q at=%q", lotCode, bidCount, highBidder, at)
	}
	if !publishedEvent(h, "lot.enriched") {
		t.Fatalf("enrich did not publish lot.enriched, events %v", eventTypes(h))
	}
}

// A lot the plugin has never stored is not a retryable failure: there is nothing a
// later attempt could do differently.
func TestEnrichJobRejectsAnUnknownLot(t *testing.T) {
	ctx := context.Background()
	h := newAutomationHarness(t, ctx, autoConfig{Enabled: true, AffiliateIDs: []string{"19"}})

	err := h.RunJobNow(ctx, "enrich", map[string]string{"lotId": "does-not-exist"})
	if err == nil {
		t.Fatal("expected enrich to fail on an unknown lot")
	}
	if !strings.Contains(err.Error(), "unknown lot") {
		t.Fatalf("err = %v", err)
	}
}

// --------------------------------------------------------------------- findings

type findingsView struct {
	Findings []struct {
		ID          string  `json:"id"`
		WatchlistID string  `json:"watchlistId"`
		Watchlist   string  `json:"watchlist"`
		Score       float64 `json:"score"`
		State       string  `json:"state"`
		Lot         struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"lot"`
	} `json:"findings"`
}

// findingsHarness collects the fake auction and runs one watchlist over it, which is
// the only way findings come to exist.
func findingsHarness(t *testing.T, ctx context.Context) (*hosttest.Harness, string) {
	t.Helper()
	h := newAutomationHarness(t, ctx, autoConfig{Enabled: true, AffiliateIDs: []string{"19"}})
	if err := h.RunJobNow(ctx, "sweep", nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if err := h.Drain(ctx); err != nil {
		t.Fatalf("follow-on job: %v", err)
	}
	id := createWatchlist(t, h, map[string]any{"name": "Coffee", "query": "keurig coffee maker"})
	if err := h.RunJobNow(ctx, "match", nil); err != nil {
		t.Fatalf("match: %v", err)
	}
	if err := h.Drain(ctx); err != nil {
		t.Fatalf("follow-on job: %v", err)
	}
	return h, id
}

func TestListFindingsDefaultsToNewAndJoinsTheLot(t *testing.T) {
	ctx := context.Background()
	h, wlID := findingsHarness(t, ctx)

	var got findingsView
	h.DecodeJSON(h.GET("/findings"), http.StatusOK, &got)
	if len(got.Findings) == 0 {
		t.Fatal("the match tick produced no findings to list")
	}
	for _, f := range got.Findings {
		if f.State != "new" {
			t.Fatalf("default listing returned state %q", f.State)
		}
		// A finding without its lot is unreadable on the screen, so the join is part
		// of the contract rather than a convenience.
		if f.Lot.ID == "" || f.Lot.Title == "" {
			t.Fatalf("finding %s carries no lot: %+v", f.ID, f)
		}
		if f.WatchlistID != wlID || f.Watchlist != "Coffee" {
			t.Fatalf("finding is not attributed to its watchlist: %+v", f)
		}
	}
}

func TestListFindingsFiltersByStateAndWatchlist(t *testing.T) {
	ctx := context.Background()
	h, wlID := findingsHarness(t, ctx)

	var all findingsView
	h.DecodeJSON(h.GET("/findings"), http.StatusOK, &all)
	first := all.Findings[0].ID

	if rec := h.POST("/findings/"+first+"/accept", nil); rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.Bytes())
	}

	var accepted findingsView
	h.DecodeJSON(h.GET("/findings?state=accepted"), http.StatusOK, &accepted)
	if len(accepted.Findings) != 1 || accepted.Findings[0].ID != first {
		t.Fatalf("accepted listing = %+v", accepted.Findings)
	}

	var remaining findingsView
	h.DecodeJSON(h.GET("/findings?state=new"), http.StatusOK, &remaining)
	for _, f := range remaining.Findings {
		if f.ID == first {
			t.Fatal("an accepted finding is still listed as new")
		}
	}

	var everything findingsView
	h.DecodeJSON(h.GET("/findings?state=all"), http.StatusOK, &everything)
	if len(everything.Findings) != len(all.Findings) {
		t.Fatalf("state=all listed %d, want %d", len(everything.Findings), len(all.Findings))
	}

	var scoped findingsView
	h.DecodeJSON(h.GET("/findings?state=all&watchlist="+wlID), http.StatusOK, &scoped)
	if len(scoped.Findings) == 0 {
		t.Fatal("filtering by the owning watchlist returned nothing")
	}
	var other findingsView
	h.DecodeJSON(h.GET("/findings?state=all&watchlist=wl-nope"), http.StatusOK, &other)
	if len(other.Findings) != 0 {
		t.Fatalf("filtering by an unrelated watchlist returned %d", len(other.Findings))
	}
}

func TestListFindingsRejectsAnUnknownState(t *testing.T) {
	ctx := context.Background()
	h := newAutomationHarness(t, ctx, autoConfig{Enabled: true, AffiliateIDs: []string{"19"}})
	if rec := h.GET("/findings?state=maybe"); rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400: %s", rec.Code, rec.Body.Bytes())
	}
}

// ------------------------------------------------------------------ watchlists

// PATCH is a partial update: an omitted field keeps what the row already had, and a
// present one replaces it.
func TestUpdateWatchlistPatchesOnlyWhatWasSent(t *testing.T) {
	ctx := context.Background()
	h := newAutomationHarness(t, ctx, autoConfig{Enabled: true, AffiliateIDs: []string{"19"}})
	id := createWatchlist(t, h, map[string]any{
		"name": "Coffee", "query": "keurig coffee maker",
		"affiliateIds": []string{"19"}, "categories": []string{"appliances"},
	})

	type wl struct {
		Name         string   `json:"name"`
		Query        string   `json:"query"`
		Enabled      bool     `json:"enabled"`
		AffiliateIDs []string `json:"affiliateIds"`
		Categories   []string `json:"categories"`
		MaxBidCents  *int64   `json:"maxBidCents"`
		MinScore     *float64 `json:"minScore"`
	}

	// Name only: everything else survives.
	var got wl
	h.DecodeJSON(h.Do(patch(t, "/watchlists/"+id, map[string]any{"name": "Espresso"})), http.StatusOK, &got)
	if got.Name != "Espresso" || got.Query != "keurig coffee maker" {
		t.Fatalf("name-only patch changed too much: %+v", got)
	}
	if len(got.AffiliateIDs) != 1 || len(got.Categories) != 1 || !got.Enabled {
		t.Fatalf("name-only patch dropped list fields: %+v", got)
	}

	// The remaining fields, each of which has its own keep-or-replace branch.
	h.DecodeJSON(h.Do(patch(t, "/watchlists/"+id, map[string]any{
		"query": "  breville   espresso  ", "enabled": false,
		"affiliateIds": []string{"20", "21"}, "categories": []string{},
		"maxBidCents": 5000, "minScore": 0.8,
	})), http.StatusOK, &got)
	if got.Query != "breville espresso" {
		t.Fatalf("query was not collapsed: %q", got.Query)
	}
	if got.Enabled {
		t.Fatal("enabled=false was not applied")
	}
	if len(got.AffiliateIDs) != 2 || len(got.Categories) != 0 {
		t.Fatalf("list fields not replaced: %+v", got)
	}
	if got.MaxBidCents == nil || *got.MaxBidCents != 5000 {
		t.Fatalf("maxBidCents = %v", got.MaxBidCents)
	}
	if got.MinScore == nil || *got.MinScore != 0.8 {
		t.Fatalf("minScore = %v", got.MinScore)
	}

	// A blank name or an all-whitespace query is not a request to erase them.
	h.DecodeJSON(h.Do(patch(t, "/watchlists/"+id, map[string]any{"name": "   ", "query": "  "})), http.StatusOK, &got)
	if got.Name != "Espresso" || got.Query != "breville espresso" {
		t.Fatalf("blank patch erased fields: %+v", got)
	}
}

func TestUpdateWatchlistRejectsBadRequests(t *testing.T) {
	ctx := context.Background()
	h := newAutomationHarness(t, ctx, autoConfig{Enabled: true, AffiliateIDs: []string{"19"}})
	id := createWatchlist(t, h, map[string]any{"name": "Coffee", "query": "keurig"})

	if rec := h.Do(patch(t, "/watchlists/wl-nope", map[string]any{"name": "x"})); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: got %d, want 404", rec.Code)
	}

	req := httptest.NewRequest(http.MethodPatch, "/watchlists/"+id, strings.NewReader("{not json"))
	req.Header.Set("Content-Type", "application/json")
	if rec := h.Do(req); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: got %d, want 400", rec.Code)
	}

	long := strings.Repeat("a ", 300)
	if rec := h.Do(patch(t, "/watchlists/"+id, map[string]any{"query": long})); rec.Code != http.StatusBadRequest {
		t.Fatalf("overlong query: got %d, want 400", rec.Code)
	}
}
