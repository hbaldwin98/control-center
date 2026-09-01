package bidrl

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hostai "github.com/hbaldwin98/control-center/host/ai"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
)

var priceSchema = json.RawMessage(`{
	"type":"object",
	"additionalProperties":false,
	"required":["price_cents","currency","condition","kind","model_or_code"],
	"properties":{
		"price_cents":{"type":"integer"},
		"currency":{"type":"string"},
		"condition":{"type":"string"},
		"kind":{"type":"string","enum":["asking","sold"]},
		"model_or_code":{"type":"string"}
	}
}`)

type priceResult struct {
	PriceCents  int64  `json:"price_cents"`
	Currency    string `json:"currency"`
	Condition   string `json:"condition"`
	Kind        string `json:"kind"`
	ModelOrCode string `json:"model_or_code"`
}

func (p *Plugin) priceEligible(jc hostjobs.Context, h host.Host, auctionID string) error {
	rows, err := h.Store().Query(jc, `SELECT l.id, l.auction_id, l.title, l.current_bid_cents, a.basis, a.model_or_sku, a.identification
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
		Basis, Model, Ident string
	}
	var lots []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.ID, &r.AuctionID, &r.Title, &r.BidCents, &r.Basis, &r.Model, &r.Ident); err != nil {
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
		if err := p.priceLot(jc, h, lot.lotRow, lot.Basis, lot.Model, lot.Ident); err != nil {
			_ = jc.Logf("price %s: %v", lot.ID, err)
			if stopCollect(err) {
				return err
			}
		}
	}
	return nil
}

func (p *Plugin) priceLot(jc hostjobs.Context, h host.Host, lot lotRow, basis, model, ident string) error {
	if basis != "exact_text" && basis != "barcode" {
		return nil
	}
	if strings.TrimSpace(model) == "" {
		_ = jc.Logf("lot %s has basis %s but no model/sku; leaving unpriced", lot.ID, basis)
		return nil
	}
	prompt := fmt.Sprintf(`Find a current market price for this exact item. Cite a source that names the model and a price.
Identification: %s
Model: %s
Title: %s
Condition hint: used auction lot`, ident, model, lot.Title)
	resp, err := h.AI().Chat(jc, hostai.ChatRequest{
		Model:    "grounded-price",
		Schema:   priceSchema,
		Messages: []hostai.Message{{Role: hostai.RoleUser, Text: prompt}},
		Grounding: &hostai.GroundingOptions{
			MaxQueries:     4,
			Freshness:      30 * 24 * time.Hour,
			AllowedDomains: nil,
		},
	})
	if err != nil {
		return err
	}
	ev, ok := evidenceFrom(resp, model, h.Clock().Now().UTC())
	if !ok {
		_ = jc.Logf("lot %s: grounded response lacked matching cited evidence; leaving unpriced", lot.ID)
		return nil
	}
	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	return h.Store().Tx(jc, func(tx hoststorage.Tx) error {
		if _, err := tx.Exec(jc, `UPDATE bidrl_lots SET bucket = 'priced' WHERE id = ?`, lot.ID); err != nil {
			return err
		}
		res, err := tx.Exec(jc, `INSERT INTO bidrl_valuations(lot_id, price_cents, currency, condition, kind, model_or_code, cited_text, source_url, source_title, source_published_at, retrieved_at, created_at, event_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
			lot.ID, ev.PriceCents, ev.Currency, ev.Condition, ev.Kind, ev.ModelOrCode,
			ev.CitedText, ev.SourceURL, ev.SourceTitle, ev.PublishedAt, ev.RetrievedAt, now)
		if err != nil {
			return err
		}
		payload := map[string]any{
			"lotId": lot.ID, "auctionId": lot.AuctionID, "title": lot.Title,
			"priceCents": ev.PriceCents, "bidCents": lot.BidCents, "kind": ev.Kind,
			"sourceUrl": ev.SourceURL,
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

func evidenceFrom(resp *hostai.ChatResponse, model string, now time.Time) (evidence, bool) {
	if resp == nil || len(resp.Sources) == 0 || len(resp.Citations) == 0 {
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
	cite := resp.Citations[0]
	if cite.Source < 0 || cite.Source >= len(resp.Sources) {
		return evidence{}, false
	}
	src := resp.Sources[cite.Source]
	if src.URL == "" {
		return evidence{}, false
	}
	quoted := resp.Text
	if cite.Start >= 0 && cite.End <= len(resp.Text) && cite.End > cite.Start {
		quoted = resp.Text[cite.Start:cite.End]
	}
	if !containsPrice(quoted) && !containsPrice(resp.Text) {
		return evidence{}, false
	}
	pub := ""
	if src.PublishedAt != nil {
		pub = src.PublishedAt.UTC().Format(time.RFC3339Nano)
	}
	return evidence{
		PriceCents: parsed.PriceCents, Currency: "USD", Condition: parsed.Condition,
		Kind: parsed.Kind, ModelOrCode: code, CitedText: quoted,
		SourceURL: src.URL, SourceTitle: src.Title, PublishedAt: pub,
		RetrievedAt: now.Format(time.RFC3339Nano),
	}, true
}

func modelMatch(hay, model string) bool {
	h := strings.ToLower(hay)
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return false
	}
	return strings.Contains(h, m)
}

func containsPrice(s string) bool {
	return dollar.FindString(s) != "" || strings.Contains(s, "price_cents")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
