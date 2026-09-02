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
	// HighBidderID is BidRL's numeric bidder id. The catalog endpoint knows only this,
	// so it is what says whether a stored username still belongs to whoever is winning.
	HighBidderID string
	EndsAt       string
	BiddingExt   bool
	ReserveMet   bool
}

// pusherEvent is one frame off the realtime feed. Pusher wraps every message in the
// same envelope, and delivers the payload as a JSON string rather than an object.
type pusherEvent struct {
	Event   string
	Channel string
	ItemID  string
	Snap    pusherSnapshot
}

// parsePusherFrame reads one feed frame. It reports false for the protocol chatter --
// connection_established, subscription_succeeded, ping -- and for any event that is
// not a bid, so the caller only sees frames that move a lot's price.
func parsePusherFrame(frame []byte) (pusherEvent, bool) {
	var envelope struct {
		Event   string          `json:"event"`
		Channel string          `json:"channel"`
		Data    json.RawMessage `json:"data"`
	}
	if json.Unmarshal(frame, &envelope) != nil {
		return pusherEvent{}, false
	}
	if envelope.Event != "bid" || len(envelope.Data) == 0 {
		return pusherEvent{}, false
	}
	payload := envelope.Data
	// Pusher double-encodes: data is a JSON string holding the real object.
	if len(payload) > 0 && payload[0] == '"' {
		var inner string
		if json.Unmarshal(payload, &inner) != nil {
			return pusherEvent{}, false
		}
		payload = json.RawMessage(inner)
	}
	snap, ok := parsePusher(payload)
	if !ok {
		return pusherEvent{}, false
	}
	ev := pusherEvent{Event: envelope.Event, Channel: envelope.Channel, Snap: snap}
	ev.ItemID = itemIDOf(payload)
	if ev.ItemID == "" {
		ev.ItemID = itemIDFromChannel(envelope.Channel)
	}
	if ev.ItemID == "" {
		return pusherEvent{}, false
	}
	return ev, true
}

func itemIDOf(payload []byte) string {
	var wrap struct {
		Item json.RawMessage `json:"item"`
	}
	if json.Unmarshal(payload, &wrap) != nil || len(wrap.Item) == 0 {
		return ""
	}
	item := asObject(wrap.Item)
	if len(item) == 0 {
		return ""
	}
	return strings.TrimSpace(firstString(item, "id", "item_id"))
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
		HighBidder:     truncateRunes(strings.TrimSpace(firstString(item, "highbidder_username", "high_bidder_username")), 80),
		HighBidderID:   truncateRunes(strings.TrimSpace(firstString(item, "high_bidder")), 80),
		EndsAt:         parseEndTime(firstRaw(item, "end_time", "ends_at"), itemTimeOffset(item)),
		BiddingExt:     firstBool(item, "bidding_extended"),
		ReserveMet:     firstBool(item, "reserve_met"),
	}
	if snap.BidCents == nil && snap.EndsAt == "" && snap.HighBidder == "" && snap.BidCount == 0 {
		return pusherSnapshot{}, false
	}
	return snap, true
}
