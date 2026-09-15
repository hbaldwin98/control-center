package bidrl_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/hbaldwin98/control-center/host/hosttest"
	"github.com/hbaldwin98/control-center/plugins/bidrl"
)

func TestListLotsPaginatesResults(t *testing.T) {
	ctx := context.Background()
	h := hosttest.New(t, bidrl.New())
	h.Run(ctx)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := h.DB().Exec(`INSERT INTO bidrl_auctions
		(id, url, title, host, lot_count, status, created_at)
		VALUES (?, ?, ?, ?, ?, 'ready', ?)`,
		"auction-page", "https://www.bidrl.com/auction/page", "Paged auction", "www.bidrl.com", 120, now); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 120; i++ {
		id := fmt.Sprintf("lot-%03d", i)
		if _, err := h.DB().Exec(`INSERT INTO bidrl_lots
			(id, auction_id, url, title, bucket, created_at, bids_refreshed_at)
			VALUES (?, ?, ?, ?, 'priced', ?, ?)`,
			id, "auction-page", "https://www.bidrl.com/auction/page/item/"+id, "Paged lot "+id, now, now); err != nil {
			t.Fatal(err)
		}
	}

	rec := h.GET("/lots?page=3&perPage=25")
	if rec.Code != http.StatusOK {
		t.Fatalf("list lots: %d %s", rec.Code, rec.Body.Bytes())
	}
	var page struct {
		Lots []struct {
			ID string `json:"id"`
		} `json:"lots"`
		Page       int  `json:"page"`
		PerPage    int  `json:"perPage"`
		Total      int  `json:"total"`
		TotalPages int  `json:"totalPages"`
		HasNext    bool `json:"hasNext"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Page != 3 || page.PerPage != 25 || page.Total != 120 || page.TotalPages != 5 || !page.HasNext {
		t.Fatalf("page metadata = %+v", page)
	}
	if len(page.Lots) != 25 || page.Lots[0].ID != "lot-051" || page.Lots[24].ID != "lot-075" {
		t.Fatalf("page rows = %d, first/last = %#v / %#v", len(page.Lots), page.Lots[0], page.Lots[len(page.Lots)-1])
	}

	for i := 1; i <= 120; i++ {
		if _, err := h.DB().Exec(`INSERT INTO bidrl_favorites(lot_id, note, created_at)
			VALUES (?, '', ?)`, fmt.Sprintf("lot-%03d", i), now); err != nil {
			t.Fatal(err)
		}
	}
	rec = h.GET("/favorites?page=2&perPage=25")
	if rec.Code != http.StatusOK {
		t.Fatalf("list favorites: %d %s", rec.Code, rec.Body.Bytes())
	}
	var favorites struct {
		Lots []struct {
			ID string `json:"id"`
		} `json:"lots"`
		Page       int  `json:"page"`
		Total      int  `json:"total"`
		TotalPages int  `json:"totalPages"`
		HasNext    bool `json:"hasNext"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &favorites); err != nil {
		t.Fatal(err)
	}
	if favorites.Page != 2 || favorites.Total != 120 || favorites.TotalPages != 5 || !favorites.HasNext {
		t.Fatalf("favorite page metadata = %+v", favorites)
	}
	if len(favorites.Lots) != 25 || favorites.Lots[0].ID != "lot-026" {
		t.Fatalf("favorite page rows = %d, first = %#v", len(favorites.Lots), favorites.Lots[0])
	}
}
