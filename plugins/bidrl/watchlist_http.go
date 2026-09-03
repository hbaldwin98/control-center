package bidrl

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
)

// Watchlists over HTTP, and the cleaning every write goes through first.

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
