package bidrl

import (
	"encoding/json"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"
)

const itemDataPath = "/api/ItemData"

type itemRecord struct {
	ItemID         string
	AuctionID      string
	URL            string
	Title          string
	LotCode        string
	Description    string
	AuctionTitle   string
	BidCents       *int64
	MinBidCents    *int64
	IncrementCents *int64
	BidCount       int
	HighBidder     string
	EndsAt         string
	BiddingExt     bool
	ReserveMet     bool
	Images         []string
}

func parseItemData(auctionID, itemID string, body []byte) (itemRecord, bool) {
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil || len(root) == 0 {
		return itemRecord{}, false
	}
	item := asObject(root["item"])
	if len(item) == 0 {
		item = root
	}
	auction := asObject(root["auction"])
	if len(auction) == 0 {
		auction = asObject(item["auction"])
	}

	rec := itemRecord{
		ItemID:       firstString(item, "id", "item_id"),
		AuctionID:    firstString(item, "auction_id", "auctionId"),
		URL:          firstString(item, "item_url", "url"),
		Title:        collapseText(html.UnescapeString(firstString(item, "title", "item_title"))),
		LotCode:      firstString(item, "lot_number", "lotNumber", "lot"),
		Description:  collapseText(html.UnescapeString(firstString(item, "description", "item_description", "desc"))),
		AuctionTitle: collapseText(html.UnescapeString(firstString(auction, "title", "auction_title"))),
		HighBidder:   firstString(item, "highbidder_username", "high_bidder_username", "high_bidder"),
		BidCount:     firstInt(item, "bid_count", "bids", "num_bids"),
		BiddingExt:   firstBool(item, "bidding_extended", "extended"),
		ReserveMet:   firstBool(item, "reserve_met", "reserveMet"),
		Images:       itemDataImages(item),
	}
	if rec.AuctionTitle == "" {
		rec.AuctionTitle = collapseText(html.UnescapeString(firstString(item, "auction_title")))
	}
	if rec.ItemID == "" {
		rec.ItemID = itemID
	}
	if rec.AuctionID == "" {
		rec.AuctionID = firstString(auction, "id", "auction_id")
	}
	if rec.AuctionID == "" {
		rec.AuctionID = auctionID
	}
	rec.Title = truncateRunes(rec.Title, 200)
	rec.AuctionTitle = truncateRunes(rec.AuctionTitle, 200)
	rec.Description = truncateRunes(rec.Description, 4000)
	rec.LotCode = strings.TrimSpace(rec.LotCode)
	rec.HighBidder = truncateRunes(strings.TrimSpace(rec.HighBidder), 80)
	if rec.URL == "" && rec.AuctionID != "" && rec.ItemID != "" {
		rec.URL = fmt.Sprintf("https://www.bidrl.com/auction/%s/item/%s/", rec.AuctionID, rec.ItemID)
	}
	if u, err := parseHTTPS(rec.URL); err == nil && isAllowedHost(u.Hostname()) {
		rec.URL = u.String()
	} else {
		rec.URL = ""
	}
	rec.BidCents = firstMoney(item, "current_bid", "currentBid", "bid")
	rec.MinBidCents = firstMoney(item, "minimum_bid", "min_bid", "next_bid")
	rec.IncrementCents = firstMoney(item, "current_increment", "bid_increment", "increment")
	offset := itemTimeOffset(item, auction)
	rec.EndsAt = parseEndTime(firstRaw(item, "end_time", "ends_at", "endTime", "closes"), offset)
	if rec.EndsAt == "" {
		rec.EndsAt = parseEndTime(firstRaw(auction, "end_time", "last_item_closes", "ends"), offset)
	}
	if rec.Title == "" && len(rec.Images) == 0 && rec.EndsAt == "" && rec.BidCents == nil {
		return itemRecord{}, false
	}
	if rec.Title == "" {
		rec.Title = "Lot " + rec.ItemID
	}
	return rec, true
}

func itemDataImages(item map[string]json.RawMessage) []string {
	for _, key := range []string{"images", "photos", "pictures", "item_images"} {
		if urls := imagesFromJSON(item[key]); len(urls) > 0 {
			return urls
		}
	}
	if one := firstString(item, "image_url", "image", "photo"); one != "" {
		if u, err := parseHTTPS(one); err == nil && isAllowedHost(u.Hostname()) {
			return []string{u.String()}
		}
	}
	return nil
}

func imagesFromJSON(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var objs []apiImage
	if json.Unmarshal(raw, &objs) == nil && len(objs) > 0 {
		return itemImages(objs)
	}
	var strs []string
	if json.Unmarshal(raw, &strs) == nil {
		var wrapped []apiImage
		for _, s := range strs {
			wrapped = append(wrapped, apiImage{ImageURL: s})
		}
		return itemImages(wrapped)
	}
	var one apiImage
	if json.Unmarshal(raw, &one) == nil {
		return itemImages([]apiImage{one})
	}
	return nil
}

func asObject(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

func firstString(m map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		if s := jsonString(m[k]); s != "" {
			return s
		}
	}
	return ""
}

func firstRaw(m map[string]json.RawMessage, keys ...string) json.RawMessage {
	for _, k := range keys {
		if len(m[k]) > 0 && string(m[k]) != "null" {
			return m[k]
		}
	}
	return nil
}

func firstInt(m map[string]json.RawMessage, keys ...string) int {
	for _, k := range keys {
		if n, ok := jsonInt(m[k]); ok {
			return n
		}
	}
	return 0
}

func firstBool(m map[string]json.RawMessage, keys ...string) bool {
	for _, k := range keys {
		if jsonBool(m[k]) {
			return true
		}
	}
	return false
}

func firstMoney(m map[string]json.RawMessage, keys ...string) *int64 {
	for _, k := range keys {
		s := jsonString(m[k])
		if s == "" {
			continue
		}
		if c := dollarsToCents(s); c != nil {
			return c
		}
	}
	return nil
}

func jsonString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return strings.TrimSpace(n.String())
	}
	return strings.TrimSpace(string(raw))
}

func jsonInt(raw json.RawMessage) (int, bool) {
	s := jsonString(raw)
	if s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		f, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil {
			return 0, false
		}
		return int(f), true
	}
	return n, true
}

func jsonBool(raw json.RawMessage) bool {
	s := strings.ToLower(jsonString(raw))
	switch s {
	case "1", "true", "yes", "y":
		return true
	}
	var b bool
	return json.Unmarshal(raw, &b) == nil && b
}

// bidrlUnixOffset is BidRL's time_offset when a payload omits it (pusher
// snapshots). Their countdown is end_time - (now + time_offset); live ItemData
// reports -7200, and applying it matches end_time_display in Pacific time.
const bidrlUnixOffset = -7200

func itemTimeOffset(maps ...map[string]json.RawMessage) int {
	for _, m := range maps {
		if n, ok := jsonInt(m["time_offset"]); ok {
			return n
		}
	}
	return bidrlUnixOffset
}

func parseEndTime(raw json.RawMessage, offsetSec int) string {
	return parseEndTimeString(jsonString(raw), offsetSec)
}

func parseEndTimeString(s string, offsetSec int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 1_000_000_000 {
		if n > 1_000_000_000_000 {
			n = n / 1000
		}
		return time.Unix(n-int64(offsetSec), 0).UTC().Format(time.RFC3339)
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	// BidRL landing pages use PHP gmdate plus date('O'): a UTC wall clock with a
	// colon-less offset such as +0300. Honoring that offset shifts the instant
	// by hours. RFC3339 (colon offset or Z) was already tried above.
	if t, ok := parseUTCWallWithNumericOffset(s); ok {
		return t.UTC().Format(time.RFC3339)
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return ""
}

func parseUTCWallWithNumericOffset(s string) (time.Time, bool) {
	if len(s) < 24 {
		return time.Time{}, false
	}
	sep := s[10]
	if sep != 'T' && sep != ' ' {
		return time.Time{}, false
	}
	base, extra := s[:19], s[19:]
	if len(extra) < 5 || (extra[0] != '+' && extra[0] != '-') {
		return time.Time{}, false
	}
	for i := 1; i <= 4; i++ {
		if extra[i] < '0' || extra[i] > '9' {
			return time.Time{}, false
		}
	}
	if len(extra) > 5 && extra[5] == ':' {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("2006-01-02T15:04:05", base[:10]+"T"+base[11:], time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func itemDataURL() string {
	return "https://www.bidrl.com" + itemDataPath
}

func pusherURL(auctionID, itemID string) string {
	return "https://www.bidrl.com/aucbeat/pusher/" + auctionID + "-" + itemID + ".json"
}
