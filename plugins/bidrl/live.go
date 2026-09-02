package bidrl

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hostbrowser "github.com/hbaldwin98/control-center/host/browser"
	hostpush "github.com/hbaldwin98/control-center/host/push"
)

// Live bids are a push topic per lot, and no transport code at all.
//
// The host owns the connection a screen opens, its framing, its buffer, and its
// teardown; this file owns only the two things that are actually BidRL's business:
// what a topic means, and what a message contains. A topic is one lot, named
// "lot:<id>". The host tells us when the first viewer starts watching one and when the
// last stops, and between those two calls we keep that lot's channel joined on BidRL's
// feed.
//
// The refcount is the point. Ten people looking at the same lot is one upstream
// channel, not ten, and a lot nobody has on screen costs nothing. That is the part a
// per-request stream could never do, because each request would have had to open its
// own socket and could not see the others.
//
// Bids are written to bidrl_lots first and published second. The database stays the
// one source of a price; the message only lets a screen fold in a change it can
// already see coming rather than refetching a list to learn one number.
const (
	// maxLiveLots keeps the rebuilt handshake inside the host's per-socket bound.
	maxLiveLots = 500
	// liveSettle coalesces demand changes. Opening a list view joins a page of lots
	// one Join at a time; without a settling delay that would redial the feed once
	// per row instead of once per screen.
	liveSettle = 250 * time.Millisecond
	// topicPrefix namespaces lot topics. The host never parses a topic name; this is
	// purely so a later kind of topic cannot collide with a lot id.
	topicPrefix = "lot:"
)

// liveFeed keeps BidRL's websocket joined to exactly the lots someone is watching.
//
// The host's subscription is deliberately read-only: the handshake is declared at
// connect time and nothing may be written afterwards. So a change in the watched set
// is a redial with a new handshake, not a frame on the open socket. That costs one TLS
// handshake per settled batch, which is cheap precisely because the hub already
// collapsed every viewer of a lot into one Join.
type liveFeed struct {
	p *Plugin

	mu    sync.Mutex
	lots  map[string]string // lot id -> auction id
	timer *time.Timer
	gen   int
	stop  context.CancelFunc
}

// Join is called by the host when a lot gains its first viewer.
func (f *liveFeed) Join(ctx context.Context, topic string) error {
	id, ok := lotFromTopic(topic)
	if !ok {
		return nil
	}
	h, live := f.p.host()
	if !live {
		return nil
	}
	// Resolving against our own rows is what makes a topic name safe: an id a client
	// invented never becomes a channel, and never becomes an UPDATE. A closed lot
	// resolves to nothing, so it joins nothing and costs nothing.
	auctionID, found, err := f.p.liveLot(ctx, h, id)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}

	f.mu.Lock()
	if f.lots == nil {
		f.lots = map[string]string{}
	}
	if len(f.lots) >= maxLiveLots {
		f.mu.Unlock()
		return nil
	}
	f.lots[id] = auctionID
	f.mu.Unlock()

	f.schedule()
	return nil
}

// Leave is called by the host when a lot loses its last viewer.
func (f *liveFeed) Leave(topic string) {
	id, ok := lotFromTopic(topic)
	if !ok {
		return
	}
	f.mu.Lock()
	if _, watched := f.lots[id]; !watched {
		f.mu.Unlock()
		return
	}
	delete(f.lots, id)
	f.mu.Unlock()

	f.schedule()
}

// schedule debounces a rebuild. Neither Join nor Leave may block: they run on the
// connection's critical path, so the socket work happens on the timer instead.
func (f *liveFeed) schedule() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.timer != nil {
		f.timer.Reset(liveSettle)
		return
	}
	f.timer = time.AfterFunc(liveSettle, f.rebuild)
}

// rebuild tears the feed down and reopens it against the current watch set.
func (f *liveFeed) rebuild() {
	h, live := f.p.host()
	if !live {
		f.close()
		return
	}

	f.mu.Lock()
	f.gen++
	gen := f.gen
	watched := make(map[string]string, len(f.lots))
	for id, auctionID := range f.lots {
		watched[id] = auctionID
	}
	prev := f.stop
	f.stop = nil
	f.mu.Unlock()

	// The old socket goes first, so the site never sees two connections joined to the
	// same channels from one install.
	if prev != nil {
		prev()
	}
	if len(watched) == 0 {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	f.mu.Lock()
	if f.gen != gen {
		// Another rebuild overtook this one while the old socket was closing.
		f.mu.Unlock()
		cancel()
		return
	}
	f.stop = cancel
	f.mu.Unlock()

	go f.run(ctx, h, gen, watched)
}

// close ends any open feed and forgets the watch set.
func (f *liveFeed) close() {
	f.mu.Lock()
	f.gen++
	stop := f.stop
	f.stop = nil
	f.lots = nil
	if f.timer != nil {
		f.timer.Stop()
		f.timer = nil
	}
	f.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// run holds one generation of the feed: a session, a subscription, and the pump that
// turns frames into rows and messages. It returns when the generation is superseded,
// the plugin is disabled, or the site drops the socket.
func (f *liveFeed) run(ctx context.Context, h host.Host, gen int, watched map[string]string) {
	sess, err := h.Browser().Open(ctx, hostbrowser.OpenOptions{AllowedHosts: feedHosts()})
	if err != nil {
		h.Log().Warn("bidrl: live feed session failed", "err", err)
		f.unavailable(ctx, h, watched)
		return
	}
	defer func() { _ = sess.Close(context.Background()) }()

	sub, err := sess.Subscribe(ctx, feedURL(), hostbrowser.SubscribeOptions{
		Handshake: subscribeFrames(watched),
		KeepAlive: []hostbrowser.KeepAliveRule{{
			Event: "pusher:ping",
			Reply: json.RawMessage(`{"event":"pusher:pong","data":{}}`),
		}},
	})
	if err != nil {
		h.Log().Warn("bidrl: live feed subscribe failed", "err", err)
		f.unavailable(ctx, h, watched)
		return
	}
	defer func() { _ = sub.Close(context.Background()) }()

	h.Log().Info("bidrl: live feed joined", "lots", len(watched), "generation", gen)

	for {
		select {
		case <-ctx.Done():
			return

		case frame, open := <-sub.Frames():
			if !open {
				// The site ended it. A rebuild will reopen on the next demand change;
				// until then the periodic catalog refresh is what keeps prices honest.
				h.Log().Info("bidrl: live feed ended", "generation", gen, "reason", sub.Err())
				return
			}
			ev, ok := parsePusherFrame(frame.Data)
			if !ok {
				continue
			}
			auctionID, ours := watched[ev.ItemID]
			if !ours {
				// The feed only sends channels we joined, so this is a protocol
				// surprise rather than a lot: never write on it.
				continue
			}
			if err := f.apply(ctx, h, ev.ItemID, auctionID, ev); err != nil {
				h.Log().Warn("bidrl: live bid write failed", "lot", ev.ItemID, "err", err)
				return
			}
		}
	}
}

// apply writes one broadcast bid onto its lot and publishes what changed. A soft close
// pushes the auction's own end time out, the way the catalog refresh does.
func (f *liveFeed) apply(ctx context.Context, h host.Host, lotID, auctionID string, ev pusherEvent) error {
	ext, reserve := 0, 0
	if ev.Snap.BiddingExt {
		ext = 1
	}
	if ev.Snap.ReserveMet {
		reserve = 1
	}
	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	res, err := h.Store().Exec(ctx, `UPDATE bidrl_lots SET current_bid_cents = ?, min_bid_cents = ?,
		bid_increment_cents = ?, bid_count = ?, high_bidder = ?, high_bidder_id = ?,
		ends_at = COALESCE(NULLIF(?, ''), ends_at), bidding_extended = ?, reserve_met = ?,
		bids_refreshed_at = ? WHERE id = ?`,
		ev.Snap.BidCents, ev.Snap.MinBidCents, ev.Snap.IncrementCents, ev.Snap.BidCount,
		ev.Snap.HighBidder, ev.Snap.HighBidderID, ev.Snap.EndsAt, ext, reserve, now, lotID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	if ev.Snap.EndsAt != "" && auctionID != "" {
		if _, err := h.Store().Exec(ctx, `UPDATE bidrl_auctions SET ends_at = ?
			WHERE id = ? AND (ends_at IS NULL OR ends_at < ?)`, ev.Snap.EndsAt, auctionID, ev.Snap.EndsAt); err != nil {
			return err
		}
	}
	return h.Push().Publish(ctx, lotTopic(lotID), bidMessage(lotID, auctionID, ev))
}

// bidMessage is what a screen receives: the values that changed, so it can fold one
// price into the row it already has instead of refetching a list to learn it.
func bidMessage(lotID, auctionID string, ev pusherEvent) map[string]any {
	msg := map[string]any{
		"lotId":           lotID,
		"auctionId":       auctionID,
		"bidCount":        ev.Snap.BidCount,
		"endsAt":          ev.Snap.EndsAt,
		"biddingExtended": ev.Snap.BiddingExt,
		"reserveMet":      ev.Snap.ReserveMet,
		"highBidder":      ev.Snap.HighBidder,
	}
	// Money is nullable, and a null is not the same as zero on a screen.
	if ev.Snap.BidCents != nil {
		msg["currentBidCents"] = *ev.Snap.BidCents
	}
	if ev.Snap.MinBidCents != nil {
		msg["minBidCents"] = *ev.Snap.MinBidCents
	}
	if ev.Snap.IncrementCents != nil {
		msg["bidIncrementCents"] = *ev.Snap.IncrementCents
	}
	return msg
}

// unavailable tells the watchers of every lot in a failed generation that the feed did
// not come up, so a screen can say so instead of showing a live badge over stale data.
func (f *liveFeed) unavailable(ctx context.Context, h host.Host, watched map[string]string) {
	for id := range watched {
		_ = h.Push().Publish(ctx, lotTopic(id), map[string]any{"lotId": id, "live": false})
	}
}

// liveLot resolves one lot id to its auction, and reports whether it is still open.
// A closed lot is not an error and not a channel: nothing more will ever happen to it.
func (p *Plugin) liveLot(ctx context.Context, h host.Host, id string) (string, bool, error) {
	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	rows, err := h.Store().Query(ctx, `SELECT auction_id FROM bidrl_lots
		WHERE id = ? AND (ends_at IS NULL OR ends_at = '' OR ends_at > ?)`, id, now)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", false, rows.Err()
	}
	var auctionID string
	if err := rows.Scan(&auctionID); err != nil {
		return "", false, err
	}
	return auctionID, true, rows.Err()
}

// subscribeFrames is one pusher:subscribe per watched lot, in a stable order so an
// unchanged watch set produces an identical handshake.
func subscribeFrames(watched map[string]string) []json.RawMessage {
	ids := make([]string, 0, len(watched))
	for id := range watched {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	frames := make([]json.RawMessage, 0, len(ids))
	for _, id := range ids {
		frame, err := json.Marshal(map[string]any{
			"event": "pusher:subscribe",
			"data":  map[string]any{"channel": itemChannel(id)},
		})
		if err != nil {
			continue
		}
		frames = append(frames, frame)
	}
	return frames
}

func lotTopic(id string) string { return topicPrefix + id }

// lotFromTopic reads a lot id back out of a topic name, rejecting anything else so a
// topic this plugin does not define can never reach the feed.
func lotFromTopic(topic string) (string, bool) {
	id, ok := strings.CutPrefix(topic, topicPrefix)
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

// liveFeed is the plugin's reaction to demand, registered once in Init.
var _ hostpush.Watcher = (*liveFeed)(nil)
