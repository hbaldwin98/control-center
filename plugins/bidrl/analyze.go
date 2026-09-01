package bidrl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hostai "github.com/hbaldwin98/control-center/host/ai"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
)

var analysisSchema = json.RawMessage(`{
	"type":"object",
	"additionalProperties":false,
	"required":["identification","basis","model_or_sku","category","search_terms","title_agreement","notes"],
	"properties":{
		"identification":{"type":"string","description":"What the photographs actually show"},
		"basis":{"type":"string","enum":["exact_text","barcode","distinctive_visual_match","product_family","category_only"],"description":"exact_text: a model or SKU is legible in a photo. barcode: a barcode, UPC, or SKU. distinctive_visual_match: a named product with no readable model. product_family: a brand or line without a specific model. category_only: a generic item with no brand or model."},
		"model_or_sku":{"type":"string","description":"The readable model, barcode, or empty"},
		"category":{"type":"string","enum":["tools","furniture","electronics","appliances","outdoor","automotive","sporting","household","collectibles","other"],"description":"What kind of thing the photographs show, independent of identification quality"},
		"search_terms":{"type":"array","items":{"type":"string"},"description":"Brand, model nicknames, and other words someone would type to find this lot"},
		"title_agreement":{"type":"number","description":"0-1 how well the listing title matches the photographs"},
		"notes":{"type":"string","description":"Short evidence taken from the photographs"}
	}
}`)

type analysisResult struct {
	Identification string   `json:"identification"`
	Basis          string   `json:"basis"`
	ModelOrSKU     string   `json:"model_or_sku"`
	Category       string   `json:"category"`
	SearchTerms    []string `json:"search_terms"`
	TitleAgreement float64  `json:"title_agreement"`
	Notes          string   `json:"notes"`
}

func (p *Plugin) analyzeUnseen(jc hostjobs.Context, h host.Host, auctionID string) error {
	lots, err := p.lotsNeedingAnalysis(jc, h, `l.auction_id = ?`, auctionID)
	if err != nil {
		return err
	}
	return p.analyzeLots(jc, h, lots)
}

// analyzeLots runs the vision pass over an explicit set of lots. A scan hands it a
// whole auction; a watchlist run hands it only the few lots that survived its funnel,
// which is the difference between paying for a warehouse and paying for what you asked
// for.
func (p *Plugin) analyzeLots(jc hostjobs.Context, h host.Host, lots []lotRow) error {
	if len(lots) == 0 {
		_ = jc.Logf("no lots left to analyze")
		return nil
	}
	_ = jc.Logf("analyzing %d lots (%d at a time)", len(lots), scanAIParallel)

	sem := make(chan struct{}, scanAIParallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	done := 0
	for _, lot := range lots {
		if err := jc.Err(); err != nil {
			return err
		}
		mu.Lock()
		stop := first
		mu.Unlock()
		if stop != nil {
			break
		}
		lot := lot
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := p.analyzeLot(jc, h, lot); err != nil {
				mu.Lock()
				if first == nil {
					first = err
				}
				mu.Unlock()
				return
			}
			mu.Lock()
			done++
			n := done
			mu.Unlock()
			_ = jc.Progress(0.2+0.5*float64(n)/float64(len(lots)), lot.Title)
		}()
	}
	wg.Wait()
	return first
}

type lotRow struct {
	ID        string
	AuctionID string
	URL       string
	Title     string
	LotCode   string
	BidCents  *int64
}

func (p *Plugin) lotsNeedingAnalysis(jc hostjobs.Context, h host.Host, where string, args ...any) ([]lotRow, error) {
	rows, err := h.Store().Query(jc, `SELECT l.id, l.auction_id, l.url, l.title, l.lot_code, l.current_bid_cents
		FROM bidrl_lots l
		WHERE (`+where+`) AND NOT EXISTS (SELECT 1 FROM bidrl_analyses a WHERE a.lot_id = l.id)
		ORDER BY l.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []lotRow
	for rows.Next() {
		var lot lotRow
		if err := rows.Scan(&lot.ID, &lot.AuctionID, &lot.URL, &lot.Title, &lot.LotCode, &lot.BidCents); err != nil {
			return nil, err
		}
		out = append(out, lot)
	}
	return out, rows.Err()
}

func (p *Plugin) analyzeLot(jc hostjobs.Context, h host.Host, lot lotRow) error {
	if err := jc.Err(); err != nil {
		return err
	}
	images, err := p.loadLotImages(jc, h, lot.ID)
	if err != nil {
		return err
	}
	prompt := fmt.Sprintf(`Identify this auction lot from the photographs, not the title.

Reply with JSON:
- identification: what the photos show
- basis: exact_text (model/SKU legible in a photo), barcode, distinctive_visual_match (named product, no readable model), product_family (brand or line only), category_only (generic item)
- model_or_sku: the readable model or barcode, or empty
- category: tools, furniture, electronics, appliances, outdoor, automotive, sporting, household, collectibles, or other
- search_terms: brand names, model nicknames, and other words someone would type to find this
- title_agreement: 0-1 how well the title matches the photos
- notes: short evidence from the photos

Title: %s
Lot code: %s
URL: %s
Photos: %d`, lot.Title, lot.LotCode, lot.URL, len(images))
	resp, err := h.AI().Chat(jc, hostai.ChatRequest{
		Model:  "cheap-vision",
		Schema: analysisSchema,
		Messages: []hostai.Message{{
			Role:   hostai.RoleUser,
			Text:   prompt,
			Images: images,
		}},
	})
	if err != nil {
		return err
	}
	parsed, ok := decodeAnalysis(resp, lot.Title)
	if !ok && resp != nil {
		_ = jc.Logf("lot %s: cheap-vision returned no JSON (%d chars); defaulting to category_only", lot.ID, len(resp.Text))
	}
	parsed.Basis = normalizeBasis(parsed.Basis)
	parsed.Category = normalizeCategory(parsed.Category)
	parsed.SearchTerms = cleanSearchTerms(parsed.SearchTerms)
	if parsed.TitleAgreement < 0 {
		parsed.TitleAgreement = 0
	}
	if parsed.TitleAgreement > 1 {
		parsed.TitleAgreement = 1
	}
	bucket := bucketFor(parsed.Basis)
	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	terms, _ := json.Marshal(parsed.SearchTerms)
	return h.Store().Tx(jc, func(tx hoststorage.Tx) error {
		if _, err := tx.Exec(jc, `UPDATE bidrl_lots SET bucket = ?, category = ? WHERE id = ?`, bucket, parsed.Category, lot.ID); err != nil {
			return err
		}
		if err := h.Events().PublishTx(jc, tx, "lot.analyzed", lot.ID, map[string]any{
			"lotId": lot.ID, "auctionId": lot.AuctionID, "title": lot.Title,
			"identification": parsed.Identification, "basis": parsed.Basis, "bucket": bucket, "category": parsed.Category,
		}); err != nil {
			return err
		}
		_, err := tx.Exec(jc, `INSERT INTO bidrl_analyses(lot_id, identification, basis, model_or_sku, title_agreement, notes, input_tokens, output_tokens, cost_micro_usd, created_at, event_id, category, search_terms)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
			lot.ID, parsed.Identification, parsed.Basis, parsed.ModelOrSKU, parsed.TitleAgreement, parsed.Notes,
			resp.Usage.InputTokens, resp.Usage.OutputTokens, int64(resp.Usage.CostMicroUSD), now, parsed.Category, string(terms))
		return err
	})
}

func (p *Plugin) loadLotImages(jc hostjobs.Context, h host.Host, lotID string) ([]hostai.Image, error) {
	rows, err := h.Store().Query(jc, `SELECT blob_key, mime FROM bidrl_images WHERE lot_id = ? ORDER BY ordinal`, lotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys [][2]string
	for rows.Next() {
		var key, mime string
		if err := rows.Scan(&key, &mime); err != nil {
			return nil, err
		}
		keys = append(keys, [2]string{key, mime})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var images []hostai.Image
	for _, km := range keys {
		rc, _, err := h.Blobs().Get(jc, km[0])
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(rc, maxImageBytes+1))
		_ = rc.Close()
		if err != nil {
			return nil, err
		}
		images = append(images, hostai.Image{Blob: body, MIME: km[1], Resolution: hostai.ResolutionMedium})
	}
	return images, nil
}

func decodeAnalysis(resp *hostai.ChatResponse, fallbackTitle string) (analysisResult, bool) {
	parsed := analysisResult{Identification: fallbackTitle, Basis: "category_only", TitleAgreement: 0}
	if resp == nil {
		return parsed, false
	}
	raw := resp.Parsed
	if len(raw) == 0 || !json.Valid(raw) {
		raw = extractJSONObject(resp.Text)
	}
	if len(raw) == 0 {
		return parsed, false
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return analysisResult{Identification: fallbackTitle, Basis: "category_only", TitleAgreement: 0}, false
	}
	return parsed, true
}

// extractJSONObject returns the first JSON object in s, including one wrapped in a
// markdown fence. Prose with no object yields nil, which leaves analysis at its
// category_only default.
func extractJSONObject(s string) json.RawMessage {
	trim := bytes.TrimSpace([]byte(s))
	if obj := jsonObject(trim); obj != nil {
		return obj
	}
	open := bytes.Index(trim, []byte("```"))
	if open < 0 {
		return jsonObject(firstObject(trim))
	}
	rest := trim[open+3:]
	if i := bytes.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[i+1:]
	}
	if close := bytes.Index(rest, []byte("```")); close >= 0 {
		rest = rest[:close]
	}
	if obj := jsonObject(bytes.TrimSpace(rest)); obj != nil {
		return obj
	}
	return jsonObject(firstObject(trim))
}

func jsonObject(b []byte) json.RawMessage {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || b[0] != '{' || !json.Valid(b) {
		return nil
	}
	return json.RawMessage(b)
}

func firstObject(b []byte) []byte {
	start := bytes.IndexByte(b, '{')
	if start < 0 {
		return nil
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(b); i++ {
		c := b[i]
		if inStr {
			if esc {
				esc = false
				continue
			}
			switch c {
			case '\\':
				esc = true
			case '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return b[start : i+1]
			}
		}
	}
	return nil
}

func normalizeBasis(s string) string {
	switch s {
	case "exact_text", "barcode", "distinctive_visual_match", "product_family", "category_only":
		return s
	default:
		return "category_only"
	}
}

func normalizeCategory(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "tools", "furniture", "electronics", "appliances", "outdoor", "automotive", "sporting", "household", "collectibles":
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return "other"
	}
}

func cleanSearchTerms(in []string) []string {
	out := uniqueFold(in, 12)
	if out == nil {
		return []string{}
	}
	return out
}

func bucketFor(basis string) string {
	switch basis {
	case "exact_text", "barcode":
		return "research"
	case "distinctive_visual_match":
		return "worth_opening"
	case "product_family":
		return "worth_opening"
	default:
		return "discarded"
	}
}
