package bidrl

import (
	"encoding/json"
	"html"
	"net/url"
	"strconv"
	"strings"
)

// The bid gallery is an AngularJS app: the served HTML carries no lot links, and the
// lots arrive as a JSON POST to /api/getitems that the browser session captures. Every
// field the collector needs — canonical lot URL, title, lot number, bid, and full-size
// images — is on that response, so a captured page costs no per-lot navigation.

const getItemsPath = "/api/getitems"

type itemsResponse struct {
	Total   json.Number `json:"total"`
	Page    json.Number `json:"page"`
	PerPage json.Number `json:"perpage"`
	Items   []apiItem   `json:"items"`
}

type apiItem struct {
	ID         string     `json:"id"`
	AuctionID  string     `json:"auction_id"`
	LotNumber  string     `json:"lot_number"`
	Title      string     `json:"title"`
	ItemURL    string     `json:"item_url"`
	CurrentBid string     `json:"current_bid"`
	Auction    string     `json:"auction_title"`
	Images     []apiImage `json:"images"`
}

type apiImage struct {
	ImageURL string `json:"image_url"`
	ThumbURL string `json:"thumb_url"`
}

// parseItemsJSON reads one /api/getitems body. It returns ok only for a response that
// is actually an item page, so unrelated captured JSON is ignored. The auction title
// comes from the items too: the gallery's own <title> is set by the app after load and
// is not in the served markup.
func parseItemsJSON(body []byte) (lots []parsedLot, title string, page, total int, ok bool) {
	var resp itemsResponse
	if err := json.Unmarshal(body, &resp); err != nil || len(resp.Items) == 0 {
		return nil, "", 0, 0, false
	}
	for _, it := range resp.Items {
		lot, ok := it.toParsedLot()
		if !ok {
			continue
		}
		if title == "" {
			title = truncateRunes(collapseText(html.UnescapeString(it.Auction)), 200)
		}
		lots = append(lots, lot)
	}
	if len(lots) == 0 {
		return nil, "", 0, 0, false
	}
	return lots, title, atoiNumber(resp.Page), atoiNumber(resp.Total), true
}

func (it apiItem) toParsedLot() (parsedLot, bool) {
	u, err := parseHTTPS(it.ItemURL)
	if err != nil || !isAllowedHost(u.Hostname()) || lotIDFromPath(u.Path) == "" {
		return parsedLot{}, false
	}
	lot := parsedLot{
		URL:          u.String(),
		ItemID:       strings.TrimSpace(it.ID),
		Title:        truncateRunes(collapseText(html.UnescapeString(it.Title)), 200),
		LotCode:      strings.TrimSpace(it.LotNumber),
		AuctionID:    strings.TrimSpace(it.AuctionID),
		AuctionTitle: truncateRunes(collapseText(html.UnescapeString(it.Auction)), 200),
		BidCents:     dollarsToCents(it.CurrentBid),
		Images:       itemImages(it.Images),
	}
	if lot.ItemID == "" {
		lot.ItemID = lotIDFromPath(u.Path)
	}
	if lot.AuctionID == "" {
		lot.AuctionID = auctionIDFromPath(u.Path)
	}
	if lot.LotCode == "" {
		lot.LotCode = lotCode.FindString(u.Path)
	}
	return lot, true
}

// itemImages keeps the full-size URL and drops the thumbnail: the scanner reads
// photographs, and a 320px thumb is not evidence.
func itemImages(in []apiImage) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, img := range in {
		raw := strings.TrimSpace(img.ImageURL)
		if raw == "" {
			raw = strings.TrimSpace(img.ThumbURL)
		}
		u, err := parseHTTPS(raw)
		if err != nil || !isAllowedHost(u.Hostname()) {
			continue
		}
		key := u.String()
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
		if len(out) >= maxImagesPerLot {
			break
		}
	}
	return out
}

func dollarsToCents(s string) *int64 {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "$"))
	if s == "" {
		return nil
	}
	cents, ok := parseDollars(s)
	if !ok {
		return nil
	}
	return &cents
}

func atoiNumber(n json.Number) int {
	v, err := strconv.Atoi(strings.TrimSpace(n.String()))
	if err != nil {
		return 0
	}
	return v
}

func isGetItemsURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return strings.HasPrefix(u.Path, getItemsPath)
}
