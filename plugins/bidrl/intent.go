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
	intentBatchSize    = 40
	minIntentScore     = 0.6
	maxIntentHits      = 80
	maxIntentQuery     = 400
	keepIntentSearches = 10
	intentMaxTokens    = 1024
	intentModel        = "intent-match"
)

var intentSchema = json.RawMessage(`{
	"type":"object",
	"additionalProperties":false,
	"required":["matches"],
	"properties":{
		"matches":{
			"type":"array",
			"items":{
				"type":"object",
				"additionalProperties":false,
				"required":["id","score","reason"],
				"properties":{
					"id":{"type":"string","description":"The lot id from the list"},
					"score":{"type":"number","description":"0-1 how well this lot serves the intent"},
					"reason":{"type":"string","description":"One short clause: why this lot helps"}
				}
			}
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

type intentResult struct {
	Matches []intentMatch `json:"matches"`
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
	all, err := p.queryLots(h, r, "1=1")
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

	cards, listed, err := p.loadIntentCards(jc, h)
	if err != nil {
		return fail(err)
	}
	_ = jc.Logf("intent %q: %d lots (%d from listing text)", q, len(cards), listed)
	if err := jc.Progress(0.05, fmt.Sprintf("reading %d lots", len(cards))); err != nil {
		return err
	}

	var matches []intentMatch
	if len(cards) == 0 {
		if err := p.storeIntentHits(jc, h, args.SearchID, q, 0, 0, nil); err != nil {
			return fail(err)
		}
		if err := h.Events().Publish(jc, "intent.completed", args.SearchID, map[string]any{
			"query": q, "hits": 0, "scanned": 0, "skipped": 0,
		}); err != nil {
			return err
		}
		return jc.Progress(1, "no lots")
	}

	batches := (len(cards) + intentBatchSize - 1) / intentBatchSize
	for i := 0; i < len(cards); i += intentBatchSize {
		if err := jc.Err(); err != nil {
			return fail(err)
		}
		end := i + intentBatchSize
		if end > len(cards) {
			end = len(cards)
		}
		batch := cards[i:end]
		n := i/intentBatchSize + 1
		_ = jc.Progress(0.05+0.85*float64(i)/float64(len(cards)), fmt.Sprintf("batch %d of %d", n, batches))
		got, err := p.judgeIntentBatch(jc, h, q, batch)
		if err != nil {
			return fail(err)
		}
		matches = append(matches, got...)
	}

	kept := keepIntentMatches(matches, cards)
	if err := p.storeIntentHits(jc, h, args.SearchID, q, len(cards), listed, kept); err != nil {
		return fail(err)
	}
	if err := h.Events().Publish(jc, "intent.completed", args.SearchID, map[string]any{
		"query": q, "hits": len(kept), "scanned": len(cards), "skipped": listed,
	}); err != nil {
		return err
	}
	return jc.Progress(1, fmt.Sprintf("%d matches", len(kept)))
}

func (p *Plugin) loadIntentCards(ctx context.Context, h host.Host) ([]intentCard, int, error) {
	rows, err := h.Store().Query(ctx, `SELECT l.id, l.title, IFNULL(l.description,''), IFNULL(a.identification,''),
		IFNULL(a.model_or_sku,''), IFNULL(a.category,''), IFNULL(a.search_terms,''), IFNULL(a.notes,'')
		FROM bidrl_lots l
		LEFT JOIN bidrl_analyses a ON a.id = (SELECT MAX(id) FROM bidrl_analyses WHERE lot_id = l.id)
		WHERE l.bucket != 'rejected'
		ORDER BY l.id`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []intentCard
	listed := 0
	for rows.Next() {
		var c intentCard
		var termsRaw string
		if err := rows.Scan(&c.ID, &c.Title, &c.Description, &c.Identification, &c.Model, &c.Category, &termsRaw, &c.Notes); err != nil {
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

func (p *Plugin) judgeIntentBatch(jc hostjobs.Context, h host.Host, query string, batch []intentCard) ([]intentMatch, error) {
	var b strings.Builder
	b.WriteString(`Match collected auction lots to the person's intent.

A lot marked photos was identified from photographs — judge from that identification. BidRL titles often lie; do not use the listing title against a photos row.
A lot marked listing has not been scanned. Judge it from the title and description.
Include a lot only if it would actually help with the intent. A propane stove helps camping; a television does not.
Do not match on coincidental words. Empty matches is correct when nothing serves the intent.

Intent: `)
	b.WriteString(query)
	b.WriteString("\n\nLots:\n")
	for _, c := range batch {
		b.WriteString(formatIntentCard(c))
		b.WriteByte('\n')
	}
	b.WriteString("\nReply with JSON matches: id, score (0-1), reason (one short clause). Omit lots below 0.6.")

	resp, err := h.AI().Chat(jc, hostai.ChatRequest{
		Model:     intentModel,
		Schema:    intentSchema,
		MaxTokens: intentMaxTokens,
		Messages: []hostai.Message{{
			Role: hostai.RoleUser,
			Text: b.String(),
		}},
	})
	if err != nil {
		return nil, err
	}
	return decodeIntentMatches(resp), nil
}

func formatIntentCard(c intentCard) string {
	clip := func(s string, n int) string {
		s = strings.Join(strings.Fields(s), " ")
		if len(s) <= n {
			return s
		}
		return s[:n]
	}
	if c.Identification != "" {
		return c.ID + " | photos | " + c.Category + " | " + clip(c.Identification, 200) + " | " +
			clip(c.Model, 80) + " | " + clip(c.Terms, 160) + " | " + clip(c.Notes, 200)
	}
	return c.ID + " | listing | " + clip(c.Title, 200) + " | " + clip(c.Description, 280)
}

func decodeIntentMatches(resp *hostai.ChatResponse) []intentMatch {
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
	var parsed intentResult
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	return parsed.Matches
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
		if score > 1 {
			score = 1
		}
		if score < minIntentScore {
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
	_ = h.Events().Publish(ctx, "intent.completed", id, map[string]any{
		"query": query, "hits": 0, "error": err.Error(),
	})
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
