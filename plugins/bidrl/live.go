package bidrl

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hostbrowser "github.com/hbaldwin98/control-center/host/browser"
)

// Live bids are a plugin API route, not a job.
//
// Joining a websocket for as long as someone is looking at a page is request-shaped
// work: it begins when a view opens, ends when the view closes, and leaves nothing
// behind that anyone would want to resume. A job is the wrong shape for it -- it would
// outlive the viewer, need a window and a renewal to bound that, and write a durable
// row per renewal for work that is neither durable nor resumable. Jobs stay for what is
// genuinely background: collection, scanning, pricing, the sweep.
//
// So the view opens an EventSource on this route. The handler holds the BidRL feed for
// exactly the lifetime of that request: navigate away and the request ends, the
// context cancels, and the socket closes with it. The bids themselves are not sent
// down this stream -- they are written to bidrl_lots and announced as ordinary
// bids.refreshed events, which is what already revalidates every screen. This stream
// carries only liveness, so there is one path for data and no second way for a screen
// to disagree with the database.
const (
	// maxLiveLots keeps one subscription inside the host's per-socket handshake bound.
	maxLiveLots = 500
	// liveHeartbeat keeps the SSE connection warm and lets the client notice a dead one.
	liveHeartbeat = 20 * time.Second
)

// liveLot is one watched lot and the auction whose end time its soft close extends.
type liveLot struct {
	ID        string
	AuctionID string
}

// liveSession is one browser session shared by every viewer. Sessions are capped per
// plugin, so a tab that opened its own would starve collection of the one it needs;
// refcounting keeps the feed to a single session that closes when the last viewer
// leaves.
type liveSession struct {
	mu   sync.Mutex
	sess hostbrowser.Session
	refs int
}

// acquire returns the shared session, opening it for the first viewer.
func (l *liveSession) acquire(r *http.Request, h host.Host) (hostbrowser.Session, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sess != nil {
		l.refs++
		return l.sess, nil
	}
	// The session must outlive this request: later viewers share it. The host closes
	// it on plugin disable regardless.
	sess, err := h.Browser().Open(r.Context(), hostbrowser.OpenOptions{AllowedHosts: feedHosts()})
	if err != nil {
		return nil, err
	}
	l.sess = sess
	l.refs = 1
	return sess, nil
}

func (l *liveSession) release(r *http.Request) {
	l.mu.Lock()
	sess := l.sess
	l.refs--
	last := l.refs <= 0
	if last {
		l.sess = nil
		l.refs = 0
	}
	l.mu.Unlock()
	if last && sess != nil {
		_ = sess.Close(r.Context())
	}
}

// handleLive streams liveness for the lots a view is showing, and applies the bids the
// site broadcasts for them while the stream is open.
func (p *Plugin) handleLive(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}
	ids := dedupeIDs(strings.Split(r.URL.Query().Get("lots"), ","))
	if len(ids) == 0 {
		writeErr(w, http.StatusBadRequest, "bad_request", "lots must name at least one lot")
		return
	}
	// Resolving against our own rows is what makes the id list safe: an id a client
	// invented never becomes a channel, and never becomes an UPDATE.
	lots, err := p.liveLots(r, h, ids)
	if err != nil {
		writeHostErr(w, err)
		return
	}

	head := w.Header()
	head.Set("Content-Type", "text/event-stream; charset=utf-8")
	head.Set("Cache-Control", "no-cache, no-transform")
	head.Set("Connection", "keep-alive")
	// Defeat proxy buffering, which would otherwise hold events until a buffer fills.
	head.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	send := func(event string, payload any) bool {
		body, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if len(lots) == 0 {
		send("idle", map[string]any{"watching": 0})
		return
	}

	sub, release, err := p.joinFeed(r, h, lots)
	if err != nil {
		send("unavailable", map[string]any{"reason": liveReason(err)})
		return
	}
	defer release()

	if !send("watching", map[string]any{"watching": len(lots)}) {
		return
	}
	p.streamLive(r, h, lots, sub, send)
}

// joinFeed subscribes to one channel per lot on the shared session. The host caps
// subscriptions per session, so a viewer beyond that cap is told the feed is busy
// rather than being queued behind one.
func (p *Plugin) joinFeed(r *http.Request, h host.Host, lots map[string]liveLot) (hostbrowser.Subscription, func(), error) {
	sess, err := p.live.acquire(r, h)
	if err != nil {
		return nil, nil, err
	}
	sub, err := sess.Subscribe(r.Context(), feedURL(), hostbrowser.SubscribeOptions{
		Handshake: subscribeFrames(lots),
		KeepAlive: []hostbrowser.KeepAliveRule{{
			Event: "pusher:ping",
			Reply: json.RawMessage(`{"event":"pusher:pong","data":{}}`),
		}},
	})
	if err != nil {
		p.live.release(r)
		return nil, nil, err
	}
	return sub, func() {
		_ = sub.Close(r.Context())
		p.live.release(r)
	}, nil
}

// streamLive applies frames until the viewer leaves or the feed ends.
func (p *Plugin) streamLive(r *http.Request, h host.Host, lots map[string]liveLot, sub hostbrowser.Subscription, send func(string, any) bool) {
	ctx := r.Context()
	beat := time.NewTicker(liveHeartbeat)
	defer beat.Stop()

	applied := 0
	for {
		select {
		case <-ctx.Done():
			// The viewer navigated away, or the plugin was disabled. Everything seen is
			// already written and announced, so there is nothing to flush.
			return

		case <-beat.C:
			if !send("heartbeat", map[string]any{"applied": applied}) {
				return
			}

		case frame, open := <-sub.Frames():
			if !open {
				send("closed", map[string]any{"reason": liveReason(sub.Err())})
				return
			}
			ev, ok := parsePusherFrame(frame.Data)
			if !ok {
				continue
			}
			lot, watched := lots[ev.ItemID]
			if !watched {
				// The feed only sends channels we joined, so this is a protocol
				// surprise rather than a lot: never write on it.
				continue
			}
			n, err := p.applyLiveBid(r, h, lot, ev)
			if err != nil {
				send("closed", map[string]any{"reason": "write failed"})
				return
			}
			if n == 0 {
				continue
			}
			applied++
			if err := p.announceBid(r, h, lot, ev); err != nil {
				send("closed", map[string]any{"reason": "publish failed"})
				return
			}
		}
	}
}

// liveLots resolves the request to lots this install actually holds, open ones first
// and soonest-closing before the rest, so a view past maxLiveLots watches what can
// still move.
func (p *Plugin) liveLots(r *http.Request, h host.Host, ids []string) (map[string]liveLot, error) {
	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	args := make([]any, 0, len(ids)+2)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, now, maxLiveLots)
	rows, err := h.Store().Query(r.Context(), `SELECT id, auction_id FROM bidrl_lots
		WHERE id IN (`+placeholders(len(ids))+`)
		AND (ends_at IS NULL OR ends_at = '' OR ends_at > ?)
		ORDER BY CASE WHEN ends_at IS NULL OR ends_at = '' THEN 1 ELSE 0 END, ends_at, id
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]liveLot{}
	for rows.Next() {
		var lot liveLot
		if err := rows.Scan(&lot.ID, &lot.AuctionID); err != nil {
			return nil, err
		}
		out[lot.ID] = lot
	}
	return out, rows.Err()
}

// dedupeIDs keeps the caller's order, drops blanks and repeats, and stops at the cap.
func dedupeIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
		if len(out) >= maxLiveLots {
			break
		}
	}
	return out
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func subscribeFrames(lots map[string]liveLot) []json.RawMessage {
	ids := make([]string, 0, len(lots))
	for id := range lots {
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

// applyLiveBid writes one broadcast bid onto its lot, and lets a soft close push the
// auction's own end time out the way the catalog refresh does.
func (p *Plugin) applyLiveBid(r *http.Request, h host.Host, lot liveLot, ev pusherEvent) (int64, error) {
	ext, reserve := 0, 0
	if ev.Snap.BiddingExt {
		ext = 1
	}
	if ev.Snap.ReserveMet {
		reserve = 1
	}
	ctx := r.Context()
	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	res, err := h.Store().Exec(ctx, `UPDATE bidrl_lots SET current_bid_cents = ?, min_bid_cents = ?,
		bid_increment_cents = ?, bid_count = ?, high_bidder = ?, high_bidder_id = ?,
		ends_at = COALESCE(NULLIF(?, ''), ends_at), bidding_extended = ?, reserve_met = ?,
		bids_refreshed_at = ? WHERE id = ?`,
		ev.Snap.BidCents, ev.Snap.MinBidCents, ev.Snap.IncrementCents, ev.Snap.BidCount,
		ev.Snap.HighBidder, ev.Snap.HighBidderID, ev.Snap.EndsAt, ext, reserve, now, lot.ID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n > 0 && ev.Snap.EndsAt != "" && lot.AuctionID != "" {
		if _, err := h.Store().Exec(ctx, `UPDATE bidrl_auctions SET ends_at = ?
			WHERE id = ? AND (ends_at IS NULL OR ends_at < ?)`, ev.Snap.EndsAt, lot.AuctionID, ev.Snap.EndsAt); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// announceBid publishes one bid as the values it changed, so a screen can fold it into
// the row it already has instead of re-fetching a whole list to learn one price. The
// catalog refresh still publishes bids.refreshed, because "many lots moved" is an
// invalidation and there is nothing useful to fold.
func (p *Plugin) announceBid(r *http.Request, h host.Host, lot liveLot, ev pusherEvent) error {
	payload := map[string]any{
		"lotId":           lot.ID,
		"auctionId":       lot.AuctionID,
		"bidCount":        ev.Snap.BidCount,
		"endsAt":          ev.Snap.EndsAt,
		"biddingExtended": ev.Snap.BiddingExt,
		"reserveMet":      ev.Snap.ReserveMet,
		"highBidder":      ev.Snap.HighBidder,
	}
	// Money is nullable, and a null is not the same as zero on a screen.
	if ev.Snap.BidCents != nil {
		payload["currentBidCents"] = *ev.Snap.BidCents
	}
	if ev.Snap.MinBidCents != nil {
		payload["minBidCents"] = *ev.Snap.MinBidCents
	}
	if ev.Snap.IncrementCents != nil {
		payload["bidIncrementCents"] = *ev.Snap.IncrementCents
	}
	return h.Events().Publish(r.Context(), "bid.observed", lot.ID, payload)
}

// liveReason is the short, non-leaking cause a viewer is shown.
func liveReason(err error) string {
	switch {
	case err == nil:
		return "ended"
	case errors.Is(err, hostbrowser.ErrLimit):
		return "busy"
	case errors.Is(err, hostbrowser.ErrDenied):
		return "denied"
	default:
		return "unavailable"
	}
}
