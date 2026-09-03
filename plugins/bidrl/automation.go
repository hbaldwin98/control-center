package bidrl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hostbrowser "github.com/hbaldwin98/control-center/host/browser"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
)

const (
	// The two ticks are deliberately offset. sweep touches BidRL and must stop when
	// BidRL says so; match never touches it at all and should still run when sweep
	// was throttled.
	sweepSchedule = "0 */6 * * *"
	matchSchedule = "30 */6 * * *"
	warnSchedule  = "*/15 * * * *"
	warnTimeout   = time.Minute
	cronTimeZone  = "UTC"

	// How long a throttle latch holds. A cron that quietly retries into a ban is
	// worse than one that stops and says so, so this is deliberately long enough
	// that a person notices and clears it.
	throttleHold = 24 * time.Hour

	defaultMaxAuctionsPerSweep = 5
	defaultMaxNewLotsPerSweep  = 400
)

// automationConfig is the operator's switch. Everything here defaults to off or to a
// small number: turning the plugin on must never start traffic on its own.
type automationConfig struct {
	Enabled             bool     `json:"enabled"`
	AffiliateIDs        []string `json:"affiliateIds"`
	MaxAuctionsPerSweep int      `json:"maxAuctionsPerSweep"`
	MaxNewLotsPerSweep  int      `json:"maxNewLotsPerSweep"`
}

func (c automationConfig) normalized() automationConfig {
	c.AffiliateIDs = cleanAffiliateIDs(c.AffiliateIDs)
	if c.MaxAuctionsPerSweep <= 0 {
		c.MaxAuctionsPerSweep = defaultMaxAuctionsPerSweep
	}
	if c.MaxNewLotsPerSweep <= 0 {
		c.MaxNewLotsPerSweep = defaultMaxNewLotsPerSweep
	}
	return c
}

type automationState struct {
	Enabled   bool `json:"enabled"`
	Locations int  `json:"locations"`
	// AffiliateIDs is what Locations counts, so the screen can name the places
	// rather than say "2 locations" and leave the operator to guess which.
	AffiliateIDs        []string `json:"affiliateIds"`
	MaxAuctionsPerSweep int      `json:"maxAuctionsPerSweep"`
	MaxNewLotsPerSweep  int      `json:"maxNewLotsPerSweep"`
	SweepSchedule       string   `json:"sweepSchedule"`
	MatchSchedule       string   `json:"matchSchedule"`
	TimeZone            string   `json:"timeZone"`
	LastSweepAt         string   `json:"lastSweepAt"`
	LastSweepNote       string   `json:"lastSweepNote"`
	LastMatchAt         string   `json:"lastMatchAt"`
	LastMatchNote       string   `json:"lastMatchNote"`
	ThrottledUntil      string   `json:"throttledUntil"`
	Throttled           bool     `json:"throttled"`
	NewFindings         int      `json:"newFindings"`
	// What the next two ticks have to work with: auctions the sweep would collect
	// and watchlists the match would run. Both are zero for reasons worth showing.
	QueuedAuctions    int `json:"queuedAuctions"`
	Watchlists        int `json:"watchlists"`
	EnabledWatchlists int `json:"enabledWatchlists"`
}

// ------------------------------------------------------------------- the sweep

// sweepJob is the only scheduled work that touches BidRL. Every guard it checks is a
// reason it might do nothing at all, and doing nothing is the common case:
//
//   - automation off, which is the default;
//   - no locations chosen, because an unscoped sweep is a crawl;
//   - a throttle latch set by an earlier tick, which no tick clears on its own.
//
// The pacing is not relaxed for being scheduled: one request in flight, 400ms apart,
// and three consecutive 429/403 responses stop the job. What changes is that a
// scheduled stop also latches, so the next tick does not walk back into it.
func (p *Plugin) sweepJob(jc hostjobs.Context) error {
	h, ok := p.host()
	if !ok {
		return fmt.Errorf("bidrl: host is not initialized")
	}
	cfg := p.cfg().Automation
	if !cfg.Enabled {
		return p.idleSweep(jc, h, "automation is off")
	}
	if len(cfg.AffiliateIDs) == 0 {
		return p.idleSweep(jc, h, "no locations chosen, so there is nothing to sweep")
	}
	state, err := p.loadAutomationState(jc, h)
	if err != nil {
		return err
	}
	if throttleHeld(state.ThrottledUntil, h.Clock().Now()) {
		// Return before opening a browser session at all: the point of the latch is
		// that the next tick does not touch BidRL, not that it touches it politely.
		return p.idleSweep(jc, h, "throttled by BidRL; resume it from the Overview when you are ready")
	}

	sess, err := h.Browser().Open(jc, hostbrowser.OpenOptions{AllowedHosts: allowedHosts})
	if err != nil {
		return err
	}
	defer sess.Close(jc)
	page, err := sess.NewPage(jc)
	if err != nil {
		return err
	}
	defer page.Close(jc)

	_ = jc.Progress(0.05, "listing auctions at your locations")
	if _, err := p.discoverSitesFor(jc, h, page, cfg.AffiliateIDs); err != nil {
		return p.sweepFailed(jc, h, err)
	}

	targets, err := p.auctionsToSweep(jc, h, cfg)
	if err != nil {
		return p.sweepFailed(jc, h, err)
	}
	if len(targets) == 0 {
		return p.finishSweep(jc, h, 0, 0, "nothing new at your locations")
	}

	lots, done := 0, 0
	for i, target := range targets {
		if err := jc.Err(); err != nil {
			return err
		}
		if lots >= cfg.MaxNewLotsPerSweep {
			_ = jc.Logf("stopping at %d lots this tick; the rest waits for the next one", lots)
			break
		}
		_ = jc.Progress(0.1+0.85*float64(i)/float64(len(targets)), target.Title)
		n, err := p.collectAuction(jc, h, target.URL)
		if err != nil {
			if errors.Is(err, errThrottled) {
				return p.sweepFailed(jc, h, err)
			}
			// One bad auction is not a reason to abandon the tick.
			_ = jc.Logf("auction %s: %v", target.ID, err)
			continue
		}
		lots += n
		done++
	}
	return p.finishSweep(jc, h, done, lots, "")
}

type sweepTarget struct {
	ID    string
	URL   string
	Title string
}

// auctionsToSweep picks what to collect: open auctions at the chosen locations that
// are not already stored, soonest to close first, because a warehouse closing tonight
// is the one worth knowing about.
func (p *Plugin) auctionsToSweep(ctx context.Context, h host.Host, cfg automationConfig) ([]sweepTarget, error) {
	where := inClause("s.affiliate_id", len(cfg.AffiliateIDs))
	args := anyStrings(cfg.AffiliateIDs)
	rows, err := h.Store().Query(ctx, `SELECT s.id, s.url, s.title FROM bidrl_affiliate_auctions s
		WHERE `+where+`
		  AND NOT EXISTS (SELECT 1 FROM bidrl_auctions a WHERE a.id = s.id AND a.status = 'ready')
		ORDER BY CASE WHEN s.ends_at = '' THEN 1 ELSE 0 END, s.ends_at, s.id
		LIMIT ?`, append(args, cfg.MaxAuctionsPerSweep)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sweepTarget
	for rows.Next() {
		var t sweepTarget
		if err := rows.Scan(&t.ID, &t.URL, &t.Title); err != nil {
			return nil, err
		}
		if t.URL == "" {
			t.URL = auctionURL(t.ID)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (p *Plugin) idleSweep(jc hostjobs.Context, h host.Host, why string) error {
	_ = jc.Logf("sweep did nothing: %s", why)
	if err := p.recordAutomation(jc, h, "sweep", why); err != nil {
		return err
	}
	return jc.Progress(1, why)
}

func (p *Plugin) finishSweep(jc hostjobs.Context, h host.Host, auctions, lots int, note string) error {
	if note == "" {
		note = fmt.Sprintf("collected %d auctions, %d lots", auctions, lots)
	}
	if err := p.recordAutomation(jc, h, "sweep", note); err != nil {
		return err
	}
	if err := h.Events().Publish(jc, "sweep.completed", "sweep", sweepCompleted{Auctions: auctions, Lots: lots}); err != nil {
		return err
	}
	return jc.Progress(1, note)
}

// sweepFailed latches when BidRL is the one refusing. Any other failure is just a
// failed job: it will be retried, and retrying it is safe.
func (p *Plugin) sweepFailed(jc hostjobs.Context, h host.Host, err error) error {
	if !errors.Is(err, errThrottled) {
		_ = p.recordAutomation(jc, h, "sweep", "failed: "+err.Error())
		return err
	}
	until := h.Clock().Now().UTC().Add(throttleHold).Format(time.RFC3339Nano)
	if setErr := p.setThrottle(jc, h, until); setErr != nil {
		return setErr
	}
	_ = p.recordAutomation(jc, h, "sweep", "stopped: BidRL is refusing requests")
	_ = h.Events().Publish(jc, "sweep.throttled", "sweep", sweepThrottled{Until: until})
	_ = jc.Logf("latched until %s; no tick will touch BidRL until you resume it", until)
	return err
}

// ------------------------------------------------------------------- the match

// matchJob runs every enabled watchlist. It never touches BidRL, so it runs even
// while a sweep is latched — there is usually a backlog of collected lots no
// watchlist has looked at yet.
func (p *Plugin) matchJob(jc hostjobs.Context) error {
	h, ok := p.host()
	if !ok {
		return fmt.Errorf("bidrl: host is not initialized")
	}
	if !p.cfg().Automation.Enabled {
		_ = jc.Logf("match did nothing: automation is off")
		if err := p.recordAutomation(jc, h, "match", "automation is off"); err != nil {
			return err
		}
		return jc.Progress(1, "automation is off")
	}
	all, err := p.listWatchlists(jc, h)
	if err != nil {
		return err
	}
	var enabled []watchlist
	for _, w := range all {
		if w.Enabled {
			enabled = append(enabled, w)
		}
	}
	if len(enabled) == 0 {
		if err := p.recordAutomation(jc, h, "match", "no enabled watchlists"); err != nil {
			return err
		}
		return jc.Progress(1, "no enabled watchlists")
	}

	created, failed := 0, 0
	for i, w := range enabled {
		if err := jc.Err(); err != nil {
			return err
		}
		_ = jc.Progress(float64(i)/float64(len(enabled)), w.Name)
		n, err := p.runWatchlist(jc, h, w)
		if err != nil {
			// One watchlist failing — a budget refusal, a model not assigned — must
			// not stop the others. runWatchlist already recorded why on that row.
			_ = jc.Logf("watchlist %q: %v", w.Name, err)
			failed++
			continue
		}
		created += n
	}
	note := fmt.Sprintf("%d findings from %d watchlists", created, len(enabled))
	if failed > 0 {
		note += fmt.Sprintf(", %d failed", failed)
	}
	if err := p.recordAutomation(jc, h, "match", note); err != nil {
		return err
	}
	if err := h.Events().Publish(jc, "match.completed", "match", matchCompleted{Findings: created, Watchlists: len(enabled), Failed: failed}); err != nil {
		return err
	}
	return jc.Progress(1, note)
}

// -------------------------------------------------------------------- storage

func throttleHeld(until string, now time.Time) bool {
	if until == "" {
		return false
	}
	t, ok := parseEndsAt(until)
	return ok && t.After(now)
}

func (p *Plugin) loadAutomationState(ctx context.Context, h host.Host) (automationState, error) {
	var st automationState
	err := h.Store().QueryRow(ctx, `SELECT last_sweep_at, last_sweep_note, last_match_at, last_match_note, throttled_until
		FROM bidrl_automation WHERE id = 1`).
		Scan(&st.LastSweepAt, &st.LastSweepNote, &st.LastMatchAt, &st.LastMatchNote, &st.ThrottledUntil)
	if err != nil {
		return automationState{}, nil
	}
	return st, nil
}

func (p *Plugin) recordAutomation(ctx context.Context, h host.Host, tick, note string) error {
	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	column := "last_match_at"
	noteColumn := "last_match_note"
	if tick == "sweep" {
		column, noteColumn = "last_sweep_at", "last_sweep_note"
	}
	_, err := h.Store().Exec(ctx, `UPDATE bidrl_automation SET `+column+` = ?, `+noteColumn+` = ? WHERE id = 1`, now, note)
	return err
}

func (p *Plugin) setThrottle(ctx context.Context, h host.Host, until string) error {
	_, err := h.Store().Exec(ctx, `UPDATE bidrl_automation SET throttled_until = ? WHERE id = 1`, until)
	return err
}

// ----------------------------------------------------------------------- HTTP

func (p *Plugin) handleGetAutomation(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	st, err := p.loadAutomationState(r.Context(), h)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	cfg := p.cfg().Automation
	st.Enabled = cfg.Enabled
	st.Locations = len(cfg.AffiliateIDs)
	st.AffiliateIDs = cfg.AffiliateIDs
	if st.AffiliateIDs == nil {
		st.AffiliateIDs = []string{}
	}
	st.MaxAuctionsPerSweep = cfg.MaxAuctionsPerSweep
	st.MaxNewLotsPerSweep = cfg.MaxNewLotsPerSweep
	st.SweepSchedule = sweepSchedule
	st.MatchSchedule = matchSchedule
	st.TimeZone = cronTimeZone
	st.Throttled = throttleHeld(st.ThrottledUntil, h.Clock().Now())
	_ = h.Store().QueryRow(r.Context(), `SELECT COUNT(*) FROM bidrl_findings WHERE state = 'new'`).Scan(&st.NewFindings)
	if len(cfg.AffiliateIDs) > 0 {
		// The same query the tick itself runs, so the screen promises exactly what
		// the next sweep would do rather than an approximation of it.
		if targets, err := p.auctionsToSweep(r.Context(), h, cfg); err == nil {
			st.QueuedAuctions = len(targets)
		}
	}
	if lists, err := p.listWatchlists(r.Context(), h); err == nil {
		st.Watchlists = len(lists)
		for _, w := range lists {
			if w.Enabled {
				st.EnabledWatchlists++
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"automation": st, "latestEventId": latestEventID(r.Context(), h),
	})
}

// handleResumeAutomation clears the latch. Only a person does this: no tick clears
// its own stop, because the whole value of the latch is that it holds until someone
// has looked.
func (p *Plugin) handleResumeAutomation(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	if err := p.setThrottle(r.Context(), h, ""); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"throttled": false})
}
