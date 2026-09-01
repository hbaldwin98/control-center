package bidrl

import (
	"encoding/json"
	"strings"
)

type pusherSnapshot struct {
	BidCents       *int64
	MinBidCents    *int64
	IncrementCents *int64
	BidCount       int
	HighBidder     string
	EndsAt         string
	BiddingExt     bool
	ReserveMet     bool
}

func parsePusher(body []byte) (pusherSnapshot, bool) {
	var wrap struct {
		Item json.RawMessage `json:"item"`
	}
	if json.Unmarshal(body, &wrap) != nil || len(wrap.Item) == 0 {
		return pusherSnapshot{}, false
	}
	item := asObject(wrap.Item)
	if len(item) == 0 {
		return pusherSnapshot{}, false
	}
	snap := pusherSnapshot{
		BidCents:       firstMoney(item, "current_bid", "currentBid"),
		MinBidCents:    firstMoney(item, "minimum_bid", "min_bid"),
		IncrementCents: firstMoney(item, "current_increment", "bid_increment"),
		BidCount:       firstInt(item, "bid_count", "bids"),
		HighBidder:     truncateRunes(strings.TrimSpace(firstString(item, "highbidder_username", "high_bidder_username", "high_bidder")), 80),
		EndsAt:         parseEndTime(firstRaw(item, "end_time", "ends_at")),
		BiddingExt:     firstBool(item, "bidding_extended"),
		ReserveMet:     firstBool(item, "reserve_met"),
	}
	if snap.BidCents == nil && snap.EndsAt == "" && snap.HighBidder == "" && snap.BidCount == 0 {
		return pusherSnapshot{}, false
	}
	return snap, true
}
