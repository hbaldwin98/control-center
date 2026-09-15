package bidrl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hostai "github.com/hbaldwin98/control-center/host/ai"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
)

const (
	maxIntentHits      = 80
	maxIntentQuery     = 400
	keepIntentSearches = 10
	maxIntentProbes    = 12
	intentMaxTokens    = 256
	maxIntentWords     = 24
	intentExpandModel  = "intent-expand"
	intentEmbedModel   = "intent-match"
)

var intentExpandSchema = json.RawMessage(`{
	"type":"object",
	"additionalProperties":false,
	"required":["item_words"],
	"properties":{
		"item_words":{
			"type":"array",
			"items":{"type":"string"},
			"description":"Related product types that would appear in an auction title for this intent, not only synonyms of the typed words"
		}
	}
}`)

type intentArgs struct {
	SearchID string `json:"searchId"`
	Query    string `json:"query"`
}

type intentBody struct {
	Query string `json:"query"`
}

type intentCard struct {
	ID             string
	Title          string
	Description    string
	Identification string
	Model          string
	Category       string
	Terms          string
	Notes          string
}

type intentMatch struct {
	ID     string  `json:"id"`
	Score  float64 `json:"score"`
	Reason string  `json:"reason"`
}

func (p *Plugin) handlePostIntent(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	var body intentBody
	if !decodeJSON(w, r, &body) {
		return
	}
	q := strings.Join(strings.Fields(body.Query), " ")
	if q == "" || len(q) > maxIntentQuery {
		writeErr(w, http.StatusBadRequest, "bad_request", "query must be 1–400 characters")
		return
	}
	id := newSearchID(h.Clock().Now())
	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	if err := p.markIntent(r.Context(), h, id, q, "queued", 0, 0, 0, "", now); err != nil {
		writeHostErr(w, err)
		return
	}
	jobID, err := h.Jobs().Enqueue(r.Context(), "intent", intentArgs{SearchID: id, Query: q},
		hostjobs.WithIdempotencyKey("intent-"+id))
	if err != nil {
		writeHostErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"jobId": jobID, "searchId": id})
}

func (p *Plugin) handleGetIntent(w http.ResponseWriter, r *http.Request) {
	h, ok := p.host()
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "plugin_disabled", "plugin disabled")
		return
	}
	var id, query, status, lastErr, created string
	var scanned, skipped, hits int
	err := h.Store().QueryRow(r.Context(), `SELECT id, query, status, scanned, skipped, hit_count, last_error, created_at
		FROM bidrl_intent_searches ORDER BY created_at DESC, id DESC LIMIT 1`).
		Scan(&id, &query, &status, &scanned, &skipped, &hits, &lastErr, &created)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"search": nil, "lots": []lotView{}, "latestEventId": latestEventID(r.Context(), h),
		})
		return
	}
	rows, err := h.Store().Query(r.Context(), `SELECT lot_id, score, reason FROM bidrl_intent_hits WHERE search_id = ? ORDER BY ordinal`, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	defer rows.Close()
	type hit struct {
		LotID  string
		Score  float64
		Reason string
	}
	var ordered []hit
	for rows.Next() {
		var htv hit
		if err := rows.Scan(&htv.LotID, &htv.Score, &htv.Reason); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		ordered = append(ordered, htv)
	}
	if err := rows.Err(); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	lotIDs := make([]string, 0, len(ordered))
	for _, htv := range ordered {
		lotIDs = append(lotIDs, htv.LotID)
	}
	lotWhere, lotArgs := lotIDWhere(lotIDs)
	all, err := p.queryLots(h, r, lotWhere, lotArgs...)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	byID := make(map[string]lotView, len(all))
	for _, lot := range all {
		byID[lot.ID] = lot
	}
	out := []lotView{}
	for _, htv := range ordered {
		lot, ok := byID[htv.LotID]
		if !ok {
			continue
		}
		score := htv.Score
		lot.MatchScore = &score
		lot.MatchReason = htv.Reason
		out = append(out, lot)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"search": map[string]any{
			"id": id, "query": query, "status": status,
			"scanned": scanned, "skipped": skipped, "hitCount": hits,
			"lastError": lastErr, "createdAt": created,
		},
		"lots":          out,
		"latestEventId": latestEventID(r.Context(), h),
	})
}

func (p *Plugin) intentJob(jc hostjobs.Context) error {
	h, ok := p.host()
	if !ok {
		return fmt.Errorf("bidrl: host is not initialized")
	}
	var args intentArgs
	if err := jc.Args(&args); err != nil {
		return err
	}
	q := strings.Join(strings.Fields(args.Query), " ")
	if q == "" {
		return hostjobs.Permanent(fmt.Errorf("bidrl: intent needs a query"))
	}
	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	if err := p.markIntent(jc, h, args.SearchID, q, "running", 0, 0, 0, "", now); err != nil {
		return err
	}
	fail := func(err error) error {
		return p.failIntent(jc, h, args.SearchID, q, now, err)
	}
	if err := p.pruneLotEmbeddings(jc, h); err != nil {
		return fail(err)
	}

	cards, listed, err := p.loadIntentCards(jc, h)
	if err != nil {
		return fail(err)
	}
	_ = jc.Logf("intent %q: %d lots (%d title-only)", q, len(cards), listed)
	if err := jc.Progress(0.1, "expanding intent"); err != nil {
		return err
	}

	if len(cards) == 0 {
		if err := p.storeIntentHits(jc, h, args.SearchID, q, 0, 0, nil); err != nil {
			return fail(err)
		}
		if err := h.Events().Publish(jc, "intent.completed", args.SearchID, intentCompleted{Query: q}); err != nil {
			return err
		}
		return jc.Progress(1, "no lots")
	}

	words, err := p.expandIntent(jc, h, q)
	if err != nil {
		_ = jc.Logf("intent expand: %v; using the typed words", err)
		words = intentWords(q, nil)
	}
	_ = jc.Logf("intent %q → %s", q, strings.Join(words, ", "))
	if err := jc.Progress(0.35, "embedding lots"); err != nil {
		return err
	}

	matches, err := p.matchIntentSemantic(jc, h, q, words, cards)
	if err != nil {
		_ = jc.Logf("intent embed: %v; matching expanded words against titles", err)
		matches = matchIntentLexical(words, cards)
	}
	if err := jc.Progress(0.8, "ranking"); err != nil {
		return err
	}

	kept := keepIntentMatches(matches, cards)
	if err := p.storeIntentHits(jc, h, args.SearchID, q, len(cards), listed, kept); err != nil {
		return fail(err)
	}
	if err := h.Events().Publish(jc, "intent.completed", args.SearchID, intentCompleted{
		Query: q, Hits: len(kept), Scanned: len(cards), Skipped: listed,
	}); err != nil {
		return err
	}
	return jc.Progress(1, fmt.Sprintf("%d matches", len(kept)))
}

func (p *Plugin) loadIntentCards(ctx context.Context, h host.Host) ([]intentCard, int, error) {
	return p.loadCards(ctx, h, "1=1")
}

// loadCards reads the text a lot is matched on. The predicate is how a watchlist
// applies its free rules — locations, categories, a price ceiling — before anything
// costs a call.
func (p *Plugin) loadCards(ctx context.Context, h host.Host, where string, args ...any) ([]intentCard, int, error) {
	now := h.Clock().Now()
	rows, err := h.Store().Query(ctx, `SELECT l.id, l.title, IFNULL(l.description,''), IFNULL(a.identification,''),
		IFNULL(a.model_or_sku,''), IFNULL(a.category,''), IFNULL(a.search_terms,''), IFNULL(a.notes,''), IFNULL(l.ends_at,'')
		FROM bidrl_lots l
		LEFT JOIN bidrl_auctions au ON au.id = l.auction_id
		LEFT JOIN bidrl_analyses a ON a.id = (SELECT MAX(id) FROM bidrl_analyses WHERE lot_id = l.id)
		WHERE l.bucket != 'rejected' AND (`+where+`)
		ORDER BY l.id`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []intentCard
	listed := 0
	for rows.Next() {
		var c intentCard
		var termsRaw, endsAt string
		if err := rows.Scan(&c.ID, &c.Title, &c.Description, &c.Identification, &c.Model, &c.Category, &termsRaw, &c.Notes, &endsAt); err != nil {
			return nil, 0, err
		}
		c.Terms = joinSearchTerms(termsRaw)
		c.Identification = strings.TrimSpace(c.Identification)
		c.Title = strings.TrimSpace(c.Title)
		c.Description = strings.TrimSpace(c.Description)
		c.Notes = strings.TrimSpace(c.Notes)
		if c.Identification == "" && c.Title == "" && c.Description == "" {
			continue
		}
		if hasEnded(endsAt, now) {
			continue
		}
		if c.Identification == "" {
			listed++
		}
		out = append(out, c)
	}
	return out, listed, rows.Err()
}

func joinSearchTerms(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return ""
	}
	var terms []string
	if json.Unmarshal([]byte(raw), &terms) == nil {
		return strings.Join(cleanSearchTerms(terms), ", ")
	}
	return strings.Join(strings.Fields(strings.Trim(raw, "[]\"")), " ")
}

func (p *Plugin) expandIntent(jc hostjobs.Context, h host.Host, query string) ([]string, error) {
	prompt := `Turn this intent into short product types that would appear in an auction title. Include typical related gear, not only synonyms of the words the user typed. It does not need to be exhaustive or perfect.

Intent: ` + query
	resp, err := h.AI().Chat(jc, hostai.ChatRequest{
		Model:     intentExpandModel,
		Schema:    intentExpandSchema,
		MaxTokens: intentMaxTokens,
		Messages:  []hostai.Message{{Role: hostai.RoleUser, Text: prompt}},
	})
	if err != nil {
		return nil, err
	}
	return intentWords(query, decodeIntentWords(resp)), nil
}

func decodeIntentWords(resp *hostai.ChatResponse) []string {
	if resp == nil {
		return nil
	}
	raw := resp.Parsed
	if len(raw) == 0 || !json.Valid(raw) {
		raw = extractJSONObject(resp.Text)
	}
	if len(raw) == 0 {
		return nil
	}
	var parsed struct {
		ItemWords []string `json:"item_words"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	return parsed.ItemWords
}

// intentProbes are the texts embedded for one search. The typed query is
// always first and stands alone: gluing the expansion words onto it averages
// every related product into one vague vector that sits about the same
// distance from the whole catalog, which is what made results look arbitrary.
// Each related word is its own probe instead, and a lot only has to be near
// one of them.
func intentProbes(query string, words []string) []string {
	q := strings.Join(strings.Fields(query), " ")
	out := []string{}
	if q != "" {
		out = append(out, q)
	}
	for _, w := range words {
		w = strings.Join(strings.Fields(w), " ")
		if w == "" || strings.EqualFold(w, q) {
			continue
		}
		out = append(out, w)
	}
	return uniqueFold(out, maxIntentProbes)
}

func matchIntentLexical(words []string, cards []intentCard) []intentMatch {
	var matches []intentMatch
	for _, c := range cards {
		score, reason := scoreIntentCard(words, c)
		if score <= 0 {
			continue
		}
		matches = append(matches, intentMatch{ID: c.ID, Score: score, Reason: reason})
	}
	return matches
}

func intentWords(query string, extra []string) []string {
	var in []string
	in = append(in, extra...)
	in = append(in, contentTokens(query)...)
	var out []string
	for _, w := range in {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		if _, stop := queryStop[strings.ToLower(w)]; stop {
			continue
		}
		if _, stop := intentStop[strings.ToLower(w)]; stop {
			continue
		}
		if len(w) < 3 {
			continue
		}
		out = append(out, w)
	}
	return uniqueFold(out, maxIntentWords)
}

var intentStop = map[string]struct{}{
	"help": {}, "helps": {}, "want": {}, "wants": {}, "thing": {}, "things": {},
	"that": {}, "this": {}, "would": {}, "could": {}, "make": {}, "looking": {},
	"look": {}, "something": {}, "stuff": {}, "need": {}, "needs": {}, "please": {},
	"find": {}, "get": {}, "useful": {}, "use": {}, "using": {}, "really": {},
	"just": {}, "like": {}, "also": {}, "some": {}, "any": {},
}

func scoreIntentCard(words []string, c intentCard) (float64, string) {
	q := strings.Join(words, " ")
	if strings.TrimSpace(q) == "" {
		return 0, ""
	}
	extra := strings.TrimSpace(c.Description + " " + c.Terms + " " + c.Notes)
	score, reason := matchScore(q, c.Title, c.Identification, c.Model, c.Category, extra)
	if score <= 0 {
		return 0, ""
	}
	if reason == "" {
		reason = "title"
	}
	return score, reason
}

func keepIntentMatches(matches []intentMatch, cards []intentCard) []intentMatch {
	known := make(map[string]struct{}, len(cards))
	for _, c := range cards {
		known[c.ID] = struct{}{}
	}
	best := map[string]intentMatch{}
	for _, m := range matches {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		if _, ok := known[id]; !ok {
			continue
		}
		score := m.Score
		if score < 0 {
			score = 0
		}
		if score <= 0 {
			continue
		}
		reason := strings.Join(strings.Fields(m.Reason), " ")
		if len(reason) > 160 {
			reason = reason[:160]
		}
		prev, ok := best[id]
		if ok && prev.Score >= score {
			continue
		}
		best[id] = intentMatch{ID: id, Score: score, Reason: reason}
	}
	out := make([]intentMatch, 0, len(best))
	for _, m := range best {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > maxIntentHits {
		out = out[:maxIntentHits]
	}
	return out
}

func (p *Plugin) failIntent(ctx context.Context, h host.Host, id, query, now string, err error) error {
	_ = p.markIntent(ctx, h, id, query, "failed", 0, 0, 0, err.Error(), now)
	_ = h.Events().Publish(ctx, "intent.completed", id, intentCompleted{Query: query, Error: err.Error()})
	return err
}

func (p *Plugin) markIntent(ctx context.Context, h host.Host, id, query, status string, scanned, skipped, hits int, lastErr, now string) error {
	_, err := h.Store().Exec(ctx, `INSERT INTO bidrl_intent_searches(id, query, status, scanned, skipped, hit_count, last_error, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET query = excluded.query, status = excluded.status,
			scanned = excluded.scanned, skipped = excluded.skipped, hit_count = excluded.hit_count, last_error = excluded.last_error`,
		id, query, status, scanned, skipped, hits, lastErr, now)
	return err
}

func (p *Plugin) storeIntentHits(ctx context.Context, h host.Host, id, query string, scanned, skipped int, hits []intentMatch) error {
	err := h.Store().Tx(ctx, func(tx hoststorage.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE bidrl_intent_searches SET status = 'ready', scanned = ?, skipped = ?, hit_count = ?, last_error = '' WHERE id = ?`,
			scanned, skipped, len(hits), id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM bidrl_intent_hits WHERE search_id = ?`, id); err != nil {
			return err
		}
		for i, hit := range hits {
			if _, err := tx.Exec(ctx, `INSERT INTO bidrl_intent_hits(search_id, ordinal, lot_id, score, reason) VALUES (?, ?, ?, ?, ?)`,
				id, i+1, hit.ID, hit.Score, hit.Reason); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return p.pruneIntentSearches(ctx, h)
}

func (p *Plugin) pruneIntentSearches(ctx context.Context, h host.Host) error {
	rows, err := h.Store().Query(ctx, `SELECT id FROM bidrl_intent_searches ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(ids) <= keepIntentSearches {
		return nil
	}
	for _, id := range ids[keepIntentSearches:] {
		if _, err := h.Store().Exec(ctx, `DELETE FROM bidrl_intent_hits WHERE search_id = ?`, id); err != nil {
			return err
		}
		if _, err := h.Store().Exec(ctx, `DELETE FROM bidrl_intent_searches WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}
