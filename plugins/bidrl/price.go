package bidrl

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hostai "github.com/hbaldwin98/control-center/host/ai"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
	hostsearch "github.com/hbaldwin98/control-center/host/search"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
)

var priceSchema = json.RawMessage(`{
	"type":"object",
	"additionalProperties":false,
	"required":["price_cents","currency","condition","kind","model_or_code","source_url"],
	"properties":{
		"price_cents":{"type":"integer"},
		"currency":{"type":"string"},
		"condition":{"type":"string"},
		"kind":{"type":"string","enum":["asking","sold"]},
		"model_or_code":{"type":"string"},
		"source_url":{"type":"string"}
	}
}`)

type priceResult struct {
	PriceCents  int64  `json:"price_cents"`
	Currency    string `json:"currency"`
	Condition   string `json:"condition"`
	Kind        string `json:"kind"`
	ModelOrCode string `json:"model_or_code"`
	SourceURL   string `json:"source_url"`
}

func (p *Plugin) priceEligible(jc hostjobs.Context, h host.Host, auctionID string) error {
	rows, err := h.Store().Query(jc, `SELECT l.id, l.auction_id, l.title, l.current_bid_cents, IFNULL(l.description,''), a.basis, a.model_or_sku, a.identification, IFNULL(a.notes,'')
		FROM bidrl_lots l
		JOIN bidrl_analyses a ON a.id = (SELECT MAX(id) FROM bidrl_analyses WHERE lot_id = l.id)
		WHERE l.auction_id = ? AND a.basis IN ('exact_text','barcode')
		  AND NOT EXISTS (SELECT 1 FROM bidrl_valuations v WHERE v.lot_id = l.id)`, auctionID)
	if err != nil {
		return err
	}
	defer rows.Close()
	type row struct {
		lotRow
		Desc, Basis, Model, Ident, Notes string
	}
	var lots []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.ID, &r.AuctionID, &r.Title, &r.BidCents, &r.Desc, &r.Basis, &r.Model, &r.Ident, &r.Notes); err != nil {
			return err
		}
		lots = append(lots, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_ = jc.Logf("pricing %d lots with cited model or barcode", len(lots))
	for i, lot := range lots {
		if err := jc.Err(); err != nil {
			return err
		}
		if err := jc.Progress(0.75+0.2*float64(i)/float64(max(1, len(lots))), lot.Title); err != nil {
			return err
		}
		if err := p.priceLot(jc, h, lot.lotRow, lot.Basis, lot.Model, lot.Ident, lot.Notes, lot.Desc, false); err != nil {
			_ = jc.Logf("price %s: %v", lot.ID, err)
			if stopCollect(err) {
				return err
			}
		}
	}
	return nil
}

func (p *Plugin) priceLot(jc hostjobs.Context, h host.Host, lot lotRow, basis, model, ident, notes, description string, force bool) error {
	if basis != "exact_text" && basis != "barcode" {
		return nil
	}
	if strings.TrimSpace(model) == "" {
		_ = jc.Logf("lot %s has basis %s but no model/sku; leaving unpriced", lot.ID, basis)
		return nil
	}
	signals := []string{lot.Title, ident, notes, description}
	if !force {
		if prior, from, ok := p.findReusableComp(jc, h, lot.ID, model, signals); ok {
			_ = jc.Logf("lot %s: reused comparable from lot %s (%s)", lot.ID, from, model)
			return p.storeValuation(jc, h, lot, prior, from)
		}
	}
	hits, tier, err := lookupComparables(jc, h.Search().Query, model)
	if err != nil {
		return err
	}
	if len(hits) == 0 {
		_ = jc.Logf("lot %s: no search hit named the model and a dollar amount; leaving unpriced", lot.ID)
		return nil
	}
	_ = jc.Logf("lot %s: pricing from %s (%d hits)", lot.ID, tier.label, len(hits))
	var listed strings.Builder
	for i, hit := range hits {
		fmt.Fprintf(&listed, "[%d] %s\nURL: %s\n%s\n\n", i+1, hit.Title, hit.URL, hit.Snippet)
	}
	prompt := fmt.Sprintf(`Pick a comparable price for this used auction lot from ONLY the %s results below.
Prefer [1] if it names this model and a dollar amount; only move down the list if it does not.
Set source_url to that result's URL exactly. Set price_cents to a dollar amount already written in that result's title or snippet. Do not invent, average, or convert retail MSRP into a street price.
Identification: %s
Model: %s
Title: %s
Condition hint: used auction lot
Source class: %s

Search results:
%s`, tier.label, ident, model, lot.Title, tier.class, listed.String())
	resp, err := h.AI().Chat(jc, hostai.ChatRequest{
		Model:    "grounded-price",
		Schema:   priceSchema,
		Messages: []hostai.Message{{Role: hostai.RoleUser, Text: prompt}},
	})
	if err != nil {
		return err
	}
	ev, ok := evidenceFrom(resp, model, h.Clock().Now().UTC(), hits)
	if !ok {
		_ = jc.Logf("lot %s: response did not cite a matching search hit; leaving unpriced", lot.ID)
		return nil
	}
	return p.storeValuation(jc, h, lot, ev, "")
}

func (p *Plugin) findReusableComp(jc hostjobs.Context, h host.Host, lotID, model string, lotSignals []string) (evidence, string, bool) {
	rows, err := h.Store().Query(jc, `SELECT v.lot_id, v.price_cents, v.currency, v.condition, v.kind, v.model_or_code,
		v.cited_text, v.source_url, v.source_title, v.source_published_at, v.retrieved_at, IFNULL(v.reused_from_lot_id,''),
		IFNULL(a.notes,''), IFNULL(a.identification,''), IFNULL(l.title,''), IFNULL(l.description,'')
		FROM bidrl_valuations v
		JOIN bidrl_lots l ON l.id = v.lot_id
		LEFT JOIN bidrl_analyses a ON a.id = (SELECT MAX(id) FROM bidrl_analyses WHERE lot_id = v.lot_id)
		WHERE v.lot_id != ? AND LOWER(TRIM(v.model_or_code)) = LOWER(TRIM(?))
		ORDER BY v.id DESC LIMIT 20`, lotID, model)
	if err != nil {
		_ = jc.Logf("lot %s: comparable reuse lookup: %v", lotID, err)
		return evidence{}, "", false
	}
	defer rows.Close()
	now := h.Clock().Now().UTC()
	for rows.Next() {
		var ev evidence
		var from, reusedFrom, notes, ident, title, desc string
		if err := rows.Scan(&from, &ev.PriceCents, &ev.Currency, &ev.Condition, &ev.Kind, &ev.ModelOrCode,
			&ev.CitedText, &ev.SourceURL, &ev.SourceTitle, &ev.PublishedAt, &ev.RetrievedAt, &reusedFrom,
			&notes, &ident, &title, &desc); err != nil {
			return evidence{}, "", false
		}
		retrieved := parseRetrieved(ev.RetrievedAt)
		if !shouldReuseComp(model, ev.ModelOrCode, retrieved, now, []string{title, ident, notes, desc}, lotSignals) {
			continue
		}
		return ev, reuseOrigin(from, reusedFrom), true
	}
	if err := rows.Err(); err != nil {
		return evidence{}, "", false
	}
	return evidence{}, "", false
}

func (p *Plugin) storeValuation(jc hostjobs.Context, h host.Host, lot lotRow, ev evidence, reusedFrom string) error {
	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	return h.Store().Tx(jc, func(tx hoststorage.Tx) error {
		if _, err := tx.Exec(jc, `UPDATE bidrl_lots SET bucket = 'priced' WHERE id = ?`, lot.ID); err != nil {
			return err
		}
		res, err := tx.Exec(jc, `INSERT INTO bidrl_valuations(lot_id, price_cents, currency, condition, kind, model_or_code, cited_text, source_url, source_title, source_published_at, retrieved_at, created_at, event_id, reused_from_lot_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)`,
			lot.ID, ev.PriceCents, ev.Currency, ev.Condition, ev.Kind, ev.ModelOrCode,
			ev.CitedText, ev.SourceURL, ev.SourceTitle, ev.PublishedAt, ev.RetrievedAt, now, reusedFrom)
		if err != nil {
			return err
		}
		payload := map[string]any{
			"lotId": lot.ID, "auctionId": lot.AuctionID, "title": lot.Title,
			"priceCents": ev.PriceCents, "bidCents": lot.BidCents, "kind": ev.Kind,
			"sourceUrl": ev.SourceURL, "sourceClass": classifySource(ev.SourceURL).Class,
			"reusedFromLotId": reusedFrom,
		}
		if err := h.Events().PublishTx(jc, tx, "lot.priced", lot.ID, payload); err != nil {
			return err
		}
		if deal, ok := dealScore(lot.BidCents, ev.PriceCents); ok && deal >= 0.4 {
			if err := h.Events().PublishTx(jc, tx, "deal_found", lot.ID, payload); err != nil {
				return err
			}
		}
		_, _ = res.LastInsertId()
		return nil
	})
}

type evidence struct {
	PriceCents  int64
	Currency    string
	Condition   string
	Kind        string
	ModelOrCode string
	CitedText   string
	SourceURL   string
	SourceTitle string
	PublishedAt string
	RetrievedAt string
}

func evidenceFrom(resp *hostai.ChatResponse, model string, now time.Time, hits []hostsearch.Hit) (evidence, bool) {
	if resp == nil {
		return evidence{}, false
	}
	var parsed priceResult
	if len(resp.Parsed) > 0 {
		_ = json.Unmarshal(resp.Parsed, &parsed)
	}
	if parsed.PriceCents <= 0 || !strings.EqualFold(parsed.Currency, "USD") {
		return evidence{}, false
	}
	if parsed.Kind != "asking" && parsed.Kind != "sold" {
		return evidence{}, false
	}
	if parsed.Condition == "" {
		parsed.Condition = "unknown"
	}
	code := parsed.ModelOrCode
	if code == "" {
		code = model
	}
	if !modelMatch(code, model) && !modelMatch(resp.Text, model) {
		return evidence{}, false
	}
	hit, ok := matchHit(parsed.SourceURL, hits)
	if !ok {
		hit, ok = matchHitFromCitations(resp, hits)
	}
	if !ok {
		return evidence{}, false
	}
	if !hitStatesCents(hit, parsed.PriceCents) {
		return evidence{}, false
	}
	if !modelMatch(hit.Title, model) && !modelMatch(hit.Snippet, model) {
		return evidence{}, false
	}
	return evidence{
		PriceCents: parsed.PriceCents, Currency: "USD", Condition: parsed.Condition,
		Kind: parsed.Kind, ModelOrCode: code, CitedText: quoteHit(hit, parsed.PriceCents),
		SourceURL: hit.URL, SourceTitle: hit.Title, RetrievedAt: now.Format(time.RFC3339Nano),
	}, true
}

type searchQuery func(ctx context.Context, req hostsearch.Request) ([]hostsearch.Hit, error)

func lookupComparables(ctx context.Context, query searchQuery, model string) ([]hostsearch.Hit, priceTier, error) {
	model = strings.TrimSpace(model)
	if model == "" || query == nil {
		return nil, priceTier{}, nil
	}
	var last error
	for _, q := range []string{model + " used sold price", model + " price"} {
		hits, err := query(ctx, hostsearch.Request{Query: q, MaxResults: 8})
		if err != nil {
			last = err
			continue
		}
		ranked := rankUsableHits(hits, model)
		if len(ranked) == 0 {
			continue
		}
		return ranked, tierFromClass(classifySource(ranked[0].URL).Class), nil
	}
	return nil, priceTier{}, last
}

func matchHit(sourceURL string, hits []hostsearch.Hit) (hostsearch.Hit, bool) {
	want := strings.TrimRight(strings.TrimSpace(sourceURL), "/")
	if want == "" {
		return hostsearch.Hit{}, false
	}
	for _, h := range hits {
		if strings.EqualFold(strings.TrimRight(h.URL, "/"), want) {
			return h, true
		}
	}
	return hostsearch.Hit{}, false
}

func matchHitFromCitations(resp *hostai.ChatResponse, hits []hostsearch.Hit) (hostsearch.Hit, bool) {
	if resp == nil {
		return hostsearch.Hit{}, false
	}
	for _, src := range resp.Sources {
		if hit, ok := matchHit(src.URL, hits); ok {
			return hit, true
		}
	}
	return hostsearch.Hit{}, false
}

func modelMatch(hay, model string) bool {
	h := strings.ToLower(hay)
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return false
	}
	return strings.Contains(h, m)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
