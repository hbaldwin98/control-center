package bidrl

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hostai "github.com/hbaldwin98/control-center/host/ai"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
)

const (
	watchJudgeModel = "watch-judge"

	// maxJudged bounds one run's spend. The embedding stage is already ranked, so
	// the tail past this is what the funnel liked least.
	maxJudged = 40
	// maxWatchlists keeps the Findings screen and the eventual cron tick bounded.
	maxWatchlists = 20
	// Expansion is cached because a nightly run must not pay intent-expand every
	// night to be told "tent, lantern, cooler" again.
	expansionMaxAge  = 30 * 24 * time.Hour
	judgeMaxTokens   = 200
	maxWatchName     = 80
	defaultMinScore  = 0.55
	maxFindingReason = 300
)

var watchJudgeSchema = json.RawMessage(`{
	"type":"object",
	"additionalProperties":false,
	"required":["relevant","reason"],
	"properties":{
		"relevant":{"type":"boolean","description":"True only if this lot is the kind of thing the person described"},
		"reason":{"type":"string","description":"One short sentence saying why, naming what in the listing decided it"}
	}
}`)

type watchlist struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Query        string   `json:"query"`
	Enabled      bool     `json:"enabled"`
	AffiliateIDs []string `json:"affiliateIds"`
	Categories   []string `json:"categories"`
	MaxBidCents  *int64   `json:"maxBidCents"`
	MinScore     float64  `json:"minScore"`
	Status       string   `json:"status"`
	LastError    string   `json:"lastError"`
	LastRunAt    string   `json:"lastRunAt"`
	CreatedAt    string   `json:"createdAt"`
	NewFindings  int      `json:"newFindings"`
}

type watchlistBody struct {
	Name         string   `json:"name"`
	Query        string   `json:"query"`
	Enabled      *bool    `json:"enabled"`
	AffiliateIDs []string `json:"affiliateIds"`
	Categories   []string `json:"categories"`
	MaxBidCents  *int64   `json:"maxBidCents"`
	MinScore     *float64 `json:"minScore"`
}

type watchArgs struct {
	WatchlistID string `json:"watchlistId"`
}

// ---------------------------------------------------------------- the funnel

// watchJob runs one watchlist's funnel: free SQL rules, then the embedding rank
// that Ask already uses, then one cheap chat call per survivor, and only then the
// vision and pricing calls. Each stage is more expensive than the last, so the
// cost of a run tracks what you asked for rather than how much BidRL listed.
func (p *Plugin) watchJob(jc hostjobs.Context) error {
	h, ok := p.host()
	if !ok {
		return fmt.Errorf("bidrl: host is not initialized")
	}
	var args watchArgs
	if err := jc.Args(&args); err != nil {
		return err
	}
	w, err := p.loadWatchlist(jc, h, args.WatchlistID)
	if err != nil {
		return hostjobs.Permanent(err)
	}
	_, err = p.runWatchlist(jc, h, w)
	return err
}

// runWatchlist is one watchlist's funnel, and returns how many findings it created.
// The watch job runs one on demand; the match tick runs every enabled one.
func (p *Plugin) runWatchlist(jc hostjobs.Context, h host.Host, w watchlist) (int, error) {
	if err := p.markWatchlist(jc, h, w.ID, "running", ""); err != nil {
		return 0, err
	}
	fail := func(err error) (int, error) {
		_ = p.markWatchlist(jc, h, w.ID, "failed", err.Error())
		return 0, err
	}

	if err := p.pruneLotEmbeddings(jc, h); err != nil {
		return fail(err)
	}
	where, wargs := watchRules(w)
	cards, _, err := p.loadCards(jc, h, where, wargs...)
	if err != nil {
		return fail(err)
	}
	_ = jc.Logf("watchlist %q: %d lots pass its rules", w.Name, len(cards))
	if len(cards) == 0 {
		return p.finishWatch(jc, h, w, 0)
	}
	if err := jc.Progress(0.15, "ranking against the catalog"); err != nil {
		return 0, err
	}

	words, err := p.watchWords(jc, h, w)
	if err != nil {
		_ = jc.Logf("watchlist expand: %v; using the typed words", err)
		words = intentWords(w.Query, nil)
	}
	matches, err := p.matchIntentSemantic(jc, h, w.Query, words, cards)
	if err != nil {
		_ = jc.Logf("watchlist embed: %v; matching expanded words against titles", err)
		matches = matchIntentLexical(words, cards)
	}
	ranked := keepWatchMatches(matches, cards, w.MinScore)
	_ = jc.Logf("%d lots survived the ranking", len(ranked))
	if len(ranked) == 0 {
		return p.finishWatch(jc, h, w, 0)
	}
	if err := jc.Progress(0.45, "judging candidates"); err != nil {
		return 0, err
	}

	byID := make(map[string]intentCard, len(cards))
	for _, c := range cards {
		byID[c.ID] = c
	}
	kept := make([]intentMatch, 0, len(ranked))
	for _, m := range ranked {
		if err := jc.Err(); err != nil {
			return 0, err
		}
		card, ok := byID[m.ID]
		if !ok {
			continue
		}
		relevant, reason, err := p.judgeLot(jc, h, w, card)
		if err != nil {
			// A judge that cannot answer must not silently drop the candidate: the
			// ranking already liked it, so keep it and say the judge was skipped.
			_ = jc.Logf("judge %s: %v", card.ID, err)
			kept = append(kept, intentMatch{ID: m.ID, Score: m.Score, Reason: m.Reason})
			continue
		}
		if !relevant {
			continue
		}
		kept = append(kept, intentMatch{ID: m.ID, Score: m.Score, Reason: reason})
	}
	_ = jc.Logf("%d judged relevant", len(kept))
	if len(kept) == 0 {
		return p.finishWatch(jc, h, w, 0)
	}
	if err := jc.Progress(0.65, "reading the photographs"); err != nil {
		return 0, err
	}

	ids := make([]string, 0, len(kept))
	for _, m := range kept {
		ids = append(ids, m.ID)
	}
	lots, err := p.lotsNeedingAnalysis(jc, h, inClause("l.id", len(ids)), anyStrings(ids)...)
	if err != nil {
		return fail(err)
	}
	if err := p.analyzeLots(jc, h, lots); err != nil {
		return fail(err)
	}
	if err := jc.Progress(0.85, "pricing what named a model"); err != nil {
		return 0, err
	}
	if err := p.priceEligibleWhere(jc, h, inClause("l.id", len(ids)), anyStrings(ids)...); err != nil {
		return fail(err)
	}

	created, err := p.storeFindings(jc, h, w, kept)
	if err != nil {
		return fail(err)
	}
	return p.finishWatch(jc, h, w, created)
}

func (p *Plugin) finishWatch(jc hostjobs.Context, h host.Host, w watchlist, created int) (int, error) {
	if err := p.markWatchlist(jc, h, w.ID, "ready", ""); err != nil {
		return 0, err
	}
	if err := h.Events().Publish(jc, "watch.completed", w.ID, map[string]any{
		"watchlistId": w.ID, "name": w.Name, "findings": created,
	}); err != nil {
		return 0, err
	}
	return created, jc.Progress(1, fmt.Sprintf("%d new findings", created))
}

// watchRules is the free stage: everything that can be decided in SQL before a
// single call is made. Lots already decided for this watchlist are excluded here
// too, so a rejection costs nothing to honour on every later run.
func watchRules(w watchlist) (string, []any) {
	where := `NOT EXISTS (SELECT 1 FROM bidrl_findings f WHERE f.watchlist_id = ? AND f.lot_id = l.id)`
	args := []any{w.ID}
	if len(w.AffiliateIDs) > 0 {
		where += ` AND ` + inClause("au.affiliate_id", len(w.AffiliateIDs))
		args = append(args, anyStrings(w.AffiliateIDs)...)
	}
	if len(w.Categories) > 0 {
		where += ` AND ` + inClause(`IFNULL(NULLIF(l.category, ''), IFNULL(a.category, ''))`, len(w.Categories))
		args = append(args, anyStrings(w.Categories)...)
	}
	if w.MaxBidCents != nil {
		where += ` AND (l.current_bid_cents IS NULL OR l.current_bid_cents <= ?)`
		args = append(args, *w.MaxBidCents)
	}
	return where, args
}

func inClause(column string, n int) string {
	return column + " IN (" + strings.TrimSuffix(strings.Repeat("?,", n), ",") + ")"
}

func anyStrings(in []string) []any {
	out := make([]any, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}

// keepWatchMatches applies the watchlist's own floor to the ranked list and caps
// what reaches the judge, which is the stage that costs a call each.
func keepWatchMatches(matches []intentMatch, cards []intentCard, minScore float64) []intentMatch {
	ranked := keepIntentMatches(matches, cards)
	out := ranked[:0]
	for _, m := range ranked {
		if m.Score < minScore {
			continue
		}
		out = append(out, m)
	}
	if len(out) > maxJudged {
		out = out[:maxJudged]
	}
	return out
}

// watchWords returns the watchlist's expansion, calling intent-expand only when the
// cache is missing or stale.
func (p *Plugin) watchWords(jc hostjobs.Context, h host.Host, w watchlist) ([]string, error) {
	var raw, at string
	if err := h.Store().QueryRow(jc, `SELECT expansion, expanded_at FROM bidrl_watchlists WHERE id = ?`, w.ID).
		Scan(&raw, &at); err == nil {
		var cached []string
		if json.Unmarshal([]byte(raw), &cached) == nil && len(cached) > 0 && !expansionStale(at, h.Clock().Now()) {
			return intentWords(w.Query, cached), nil
		}
	}
	words, err := p.expandIntent(jc, h, w.Query)
	if err != nil {
		return nil, err
	}
	blob, mErr := json.Marshal(words)
	if mErr == nil {
		now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
		_, _ = h.Store().Exec(jc, `UPDATE bidrl_watchlists SET expansion = ?, expanded_at = ? WHERE id = ?`,
			string(blob), now, w.ID)
	}
	return words, nil
}

func expansionStale(at string, now time.Time) bool {
	if at == "" {
		return true
	}
	t, ok := parseEndsAt(at)
	if !ok {
		return true
	}
	return now.Sub(t) > expansionMaxAge
}

// judgeLot asks a cheap chat model whether one lot is the kind of thing the
// watchlist described. It is a filter, not a scorer, and it sees text only: asking
// a model to rank things it cannot see produces confident noise, which is the
// failure this plugin already refuses for pricing.
func (p *Plugin) judgeLot(jc hostjobs.Context, h host.Host, w watchlist, c intentCard) (bool, string, error) {
	var b strings.Builder
	b.WriteString("Someone is watching auction listings for: ")
	b.WriteString(w.Query)
	b.WriteString("\n\nIs this listing that kind of thing? Judge only from the text below; you cannot see the photographs. Say no when the listing is something else that merely shares a word.\n\nTitle: ")
	b.WriteString(c.Title)
	if c.Identification != "" {
		b.WriteString("\nIdentified from photos as: " + c.Identification)
	}
	if c.Model != "" {
		b.WriteString("\nModel: " + c.Model)
	}
	if c.Category != "" {
		b.WriteString("\nCategory: " + c.Category)
	}
	if c.Description != "" {
		b.WriteString("\nDescription: " + clipWords(c.Description, 60))
	}
	resp, err := h.AI().Chat(jc, hostai.ChatRequest{
		Model:     watchJudgeModel,
		Schema:    watchJudgeSchema,
		MaxTokens: judgeMaxTokens,
		Messages:  []hostai.Message{{Role: hostai.RoleUser, Text: b.String()}},
	})
	if err != nil {
		return false, "", err
	}
	raw := resp.Parsed
	if len(raw) == 0 || !json.Valid(raw) {
		raw = extractJSONObject(resp.Text)
	}
	var parsed struct {
		Relevant bool   `json:"relevant"`
		Reason   string `json:"reason"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &parsed) != nil {
		return false, "", fmt.Errorf("bidrl: judge returned no usable answer")
	}
	reason := strings.Join(strings.Fields(parsed.Reason), " ")
	if len(reason) > maxFindingReason {
		reason = reason[:maxFindingReason]
	}
	return parsed.Relevant, reason, nil
}

func (p *Plugin) storeFindings(ctx context.Context, h host.Host, w watchlist, kept []intentMatch) (int, error) {
	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	created := 0
	err := h.Store().Tx(ctx, func(tx hoststorage.Tx) error {
		for _, m := range kept {
			// The unique key is what makes the queue usable: a lot decided once
			// never comes back to it, however many times a later run re-matches.
			res, err := tx.Exec(ctx, `INSERT INTO bidrl_findings(id, watchlist_id, lot_id, score, reason, state, created_at)
				VALUES (?, ?, ?, ?, ?, 'new', ?)
				ON CONFLICT(watchlist_id, lot_id) DO NOTHING`,
				w.ID+"-"+m.ID, w.ID, m.ID, m.Score, m.Reason, now)
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err == nil && n > 0 {
				created++
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if created > 0 {
		_ = h.Events().Publish(ctx, "finding.created", w.ID, map[string]any{
			"watchlistId": w.ID, "count": created,
		})
	}
	return created, nil
}

// ------------------------------------------------------------------- storage

func (p *Plugin) loadWatchlist(ctx context.Context, h host.Host, id string) (watchlist, error) {
	var w watchlist
	var enabled int
	var affRaw, catRaw string
	err := h.Store().QueryRow(ctx, `SELECT id, name, query, enabled, affiliate_ids, categories, max_bid_cents,
		min_score, status, last_error, last_run_at, created_at
		FROM bidrl_watchlists WHERE id = ?`, id).
		Scan(&w.ID, &w.Name, &w.Query, &enabled, &affRaw, &catRaw, &w.MaxBidCents,
			&w.MinScore, &w.Status, &w.LastError, &w.LastRunAt, &w.CreatedAt)
	if err != nil {
		return w, fmt.Errorf("bidrl: no such watchlist %q", id)
	}
	w.Enabled = enabled != 0
	w.AffiliateIDs = decodeStringList(affRaw)
	w.Categories = decodeStringList(catRaw)
	return w, nil
}

func decodeStringList(raw string) []string {
	out := []string{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	var parsed []string
	if json.Unmarshal([]byte(raw), &parsed) != nil {
		return out
	}
	for _, s := range parsed {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (p *Plugin) markWatchlist(ctx context.Context, h host.Host, id, status, lastErr string) error {
	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	_, err := h.Store().Exec(ctx, `UPDATE bidrl_watchlists SET status = ?, last_error = ?,
		last_run_at = CASE WHEN ? IN ('ready','failed') THEN ? ELSE last_run_at END WHERE id = ?`,
		status, lastErr, status, now, id)
	return err
}

// --------------------------------------------------------------------- HTTP

func (p *Plugin) handleListWatchlists(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	out, err := p.listWatchlists(r.Context(), h)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"watchlists": out, "latestEventId": latestEventID(r.Context(), h)})
}

func (p *Plugin) listWatchlists(ctx context.Context, h host.Host) ([]watchlist, error) {
	rows, err := h.Store().Query(ctx, `SELECT w.id, w.name, w.query, w.enabled, w.affiliate_ids, w.categories,
		w.max_bid_cents, w.min_score, w.status, w.last_error, w.last_run_at, w.created_at,
		(SELECT COUNT(*) FROM bidrl_findings f WHERE f.watchlist_id = w.id AND f.state = 'new')
		FROM bidrl_watchlists w ORDER BY w.created_at DESC, w.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []watchlist{}
	for rows.Next() {
		var w watchlist
		var enabled int
		var affRaw, catRaw string
		if err := rows.Scan(&w.ID, &w.Name, &w.Query, &enabled, &affRaw, &catRaw, &w.MaxBidCents,
			&w.MinScore, &w.Status, &w.LastError, &w.LastRunAt, &w.CreatedAt, &w.NewFindings); err != nil {
			return nil, err
		}
		w.Enabled = enabled != 0
		w.AffiliateIDs = decodeStringList(affRaw)
		w.Categories = decodeStringList(catRaw)
		out = append(out, w)
	}
	return out, rows.Err()
}

func (p *Plugin) handleCreateWatchlist(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	var body watchlistBody
	if !decodeJSON(w, r, &body) {
		return
	}
	query := strings.Join(strings.Fields(body.Query), " ")
	if query == "" || len(query) > maxIntentQuery {
		writeErr(w, http.StatusBadRequest, "bad_request", "query must be 1–400 characters")
		return
	}
	var count int
	_ = h.Store().QueryRow(r.Context(), `SELECT COUNT(*) FROM bidrl_watchlists`).Scan(&count)
	if count >= maxWatchlists {
		writeErr(w, http.StatusConflict, "too_many", fmt.Sprintf("at most %d watchlists", maxWatchlists))
		return
	}
	name := truncateRunes(collapseText(body.Name), maxWatchName)
	if name == "" {
		name = truncateRunes(query, maxWatchName)
	}
	// A millisecond stamp alone is not unique: two saves in the same millisecond — a
	// double-click, a script — would collide on the primary key and read as an
	// internal error. The suffix costs nothing and makes the id the row's own.
	id := "wl-" + newSearchID(h.Clock().Now()) + "-" + randomSuffix()
	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	if _, err := h.Store().Exec(r.Context(), `INSERT INTO bidrl_watchlists(id, name, query, enabled,
		affiliate_ids, categories, max_bid_cents, min_score, created_at)
		VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?)`,
		id, name, query, encodeStringList(cleanAffiliateIDs(body.AffiliateIDs)),
		encodeStringList(cleanCategories(body.Categories)), body.MaxBidCents,
		cleanMinScore(body.MinScore), now); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	wl, err := p.loadWatchlist(r.Context(), h, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, wl)
}

// randomSuffix is four hex characters of entropy, enough to separate two watchlists
// saved in the same millisecond. It is an identifier, not a secret.
func randomSuffix() string {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0000"
	}
	return hex.EncodeToString(b[:])
}

func (p *Plugin) handleUpdateWatchlist(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	id := r.PathValue("id")
	current, err := p.loadWatchlist(r.Context(), h, id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "watchlist not found")
		return
	}
	var body watchlistBody
	if !decodeJSON(w, r, &body) {
		return
	}
	name := current.Name
	if strings.TrimSpace(body.Name) != "" {
		name = truncateRunes(collapseText(body.Name), maxWatchName)
	}
	query := current.Query
	if q := strings.Join(strings.Fields(body.Query), " "); q != "" {
		if len(q) > maxIntentQuery {
			writeErr(w, http.StatusBadRequest, "bad_request", "query must be 1–400 characters")
			return
		}
		query = q
	}
	enabled := current.Enabled
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	affiliates := current.AffiliateIDs
	if body.AffiliateIDs != nil {
		affiliates = cleanAffiliateIDs(body.AffiliateIDs)
	}
	categories := current.Categories
	if body.Categories != nil {
		categories = cleanCategories(body.Categories)
	}
	maxBid := current.MaxBidCents
	if body.MaxBidCents != nil {
		maxBid = body.MaxBidCents
	}
	minScore := current.MinScore
	if body.MinScore != nil {
		minScore = cleanMinScore(body.MinScore)
	}
	// A changed query invalidates the cached expansion: the words were derived
	// from the old one, and reusing them would answer a question nobody asked.
	expansionReset := ""
	if query != current.Query {
		expansionReset = `, expansion = '[]', expanded_at = ''`
	}
	if _, err := h.Store().Exec(r.Context(), `UPDATE bidrl_watchlists SET name = ?, query = ?, enabled = ?,
		affiliate_ids = ?, categories = ?, max_bid_cents = ?, min_score = ?`+expansionReset+` WHERE id = ?`,
		name, query, enabledInt, encodeStringList(affiliates), encodeStringList(categories),
		maxBid, minScore, id); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	updated, err := p.loadWatchlist(r.Context(), h, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (p *Plugin) handleDeleteWatchlist(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	id := r.PathValue("id")
	err := h.Store().Tx(r.Context(), func(tx hoststorage.Tx) error {
		if _, err := tx.Exec(r.Context(), `DELETE FROM bidrl_findings WHERE watchlist_id = ?`, id); err != nil {
			return err
		}
		_, err := tx.Exec(r.Context(), `DELETE FROM bidrl_watchlists WHERE id = ?`, id)
		return err
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

func (p *Plugin) handleRunWatchlist(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	id := r.PathValue("id")
	if _, err := p.loadWatchlist(r.Context(), h, id); err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "watchlist not found")
		return
	}
	if err := p.markWatchlist(r.Context(), h, id, "queued", ""); err != nil {
		writeHostErr(w, err)
		return
	}
	jobID, err := h.Jobs().Enqueue(r.Context(), "watch", watchArgs{WatchlistID: id},
		hostjobs.WithIdempotencyKey("watch-"+id+"-"+newSearchID(h.Clock().Now())))
	if err != nil {
		writeHostErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int64{"jobId": jobID})
}

// ------------------------------------------------------------------ findings

type findingView struct {
	ID          string  `json:"id"`
	WatchlistID string  `json:"watchlistId"`
	Watchlist   string  `json:"watchlist"`
	Score       float64 `json:"score"`
	Reason      string  `json:"reason"`
	State       string  `json:"state"`
	CreatedAt   string  `json:"createdAt"`
	Lot         lotView `json:"lot"`
}

func (p *Plugin) handleListFindings(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	state := r.URL.Query().Get("state")
	if state == "" {
		state = "new"
	}
	switch state {
	case "new", "accepted", "rejected", "all":
	default:
		writeErr(w, http.StatusBadRequest, "bad_request", "unknown state")
		return
	}
	where := `1=1`
	var args []any
	if state != "all" {
		where += ` AND f.state = ?`
		args = append(args, state)
	}
	if wl := strings.TrimSpace(r.URL.Query().Get("watchlist")); wl != "" {
		where += ` AND f.watchlist_id = ?`
		args = append(args, wl)
	}
	rows, err := h.Store().Query(r.Context(), `SELECT f.id, f.watchlist_id, IFNULL(w.name,''), f.lot_id,
		f.score, f.reason, f.state, f.created_at
		FROM bidrl_findings f
		LEFT JOIN bidrl_watchlists w ON w.id = f.watchlist_id
		WHERE `+where+`
		ORDER BY f.created_at DESC, f.score DESC, f.id`, args...)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	defer rows.Close()
	type row struct {
		findingView
		LotID string
	}
	var listed []row
	for rows.Next() {
		var rv row
		if err := rows.Scan(&rv.ID, &rv.WatchlistID, &rv.Watchlist, &rv.LotID,
			&rv.Score, &rv.Reason, &rv.State, &rv.CreatedAt); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		listed = append(listed, rv)
	}
	if err := rows.Err(); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	lots, err := p.queryLots(h, r, "1=1")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	byID := make(map[string]lotView, len(lots))
	for _, lot := range lots {
		byID[lot.ID] = lot
	}
	out := []findingView{}
	for _, rv := range listed {
		lot, ok := byID[rv.LotID]
		if !ok {
			continue
		}
		view := rv.findingView
		view.Lot = lot
		out = append(out, view)
	}
	watchlists, err := p.listWatchlists(r.Context(), h)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"findings": out, "watchlists": watchlists, "state": state,
		"latestEventId": latestEventID(r.Context(), h),
	})
}

func (p *Plugin) handleAcceptFinding(w http.ResponseWriter, r *http.Request) {
	p.decideFinding(w, r, "accepted")
}

func (p *Plugin) handleRejectFinding(w http.ResponseWriter, r *http.Request) {
	p.decideFinding(w, r, "rejected")
}

// decideFinding records the verdict. Accepting also saves the lot: accepting
// something and then hunting for it in another tab is the obvious wrong flow.
func (p *Plugin) decideFinding(w http.ResponseWriter, r *http.Request, state string) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	id := r.PathValue("id")
	var lotID string
	if err := h.Store().QueryRow(r.Context(), `SELECT lot_id FROM bidrl_findings WHERE id = ?`, id).Scan(&lotID); err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "finding not found")
		return
	}
	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	err := h.Store().Tx(r.Context(), func(tx hoststorage.Tx) error {
		if _, err := tx.Exec(r.Context(), `UPDATE bidrl_findings SET state = ?, decided_at = ? WHERE id = ?`,
			state, now, id); err != nil {
			return err
		}
		if state != "accepted" {
			return nil
		}
		_, err := tx.Exec(r.Context(), `INSERT INTO bidrl_favorites(lot_id, note, created_at)
			VALUES (?, '', ?) ON CONFLICT(lot_id) DO NOTHING`, lotID, now)
		return err
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	_ = h.Events().Publish(r.Context(), "finding.decided", id, map[string]any{
		"findingId": id, "lotId": lotID, "state": state,
	})
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "state": state, "lotId": lotID})
}

// ------------------------------------------------------------------ cleaning

func cleanAffiliateIDs(in []string) []string {
	out := []string{}
	seen := map[string]struct{}{}
	for _, raw := range in {
		id := affiliateIDFromSlug(raw)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// lotCategories is the vision pass's closed category list, repeated here so a
// watchlist cannot be narrowed to a category no analysis will ever produce.
var lotCategories = []string{
	"tools", "furniture", "electronics", "appliances", "outdoor",
	"automotive", "sporting", "household", "collectibles", "other",
}

func cleanCategories(in []string) []string {
	allowed := map[string]struct{}{}
	for _, c := range lotCategories {
		allowed[c] = struct{}{}
	}
	out := []string{}
	seen := map[string]struct{}{}
	for _, raw := range in {
		c := strings.ToLower(strings.TrimSpace(raw))
		if _, ok := allowed[c]; !ok {
			continue
		}
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out
}

func cleanMinScore(in *float64) float64 {
	if in == nil {
		return defaultMinScore
	}
	v := *in
	if v < 0 {
		return 0
	}
	if v > 5 {
		return 5
	}
	return v
}

func encodeStringList(in []string) string {
	if in == nil {
		in = []string{}
	}
	blob, err := json.Marshal(in)
	if err != nil {
		return "[]"
	}
	return string(blob)
}
