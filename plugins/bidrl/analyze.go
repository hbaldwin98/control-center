package bidrl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
	"required":["identification","basis","model_or_sku","title_agreement","notes"],
	"properties":{
		"identification":{"type":"string"},
		"basis":{"type":"string","enum":["exact_text","barcode","distinctive_visual_match","product_family","category_only"]},
		"model_or_sku":{"type":"string"},
		"title_agreement":{"type":"number"},
		"notes":{"type":"string"}
	}
}`)

type analysisResult struct {
	Identification string  `json:"identification"`
	Basis          string  `json:"basis"`
	ModelOrSKU     string  `json:"model_or_sku"`
	TitleAgreement float64 `json:"title_agreement"`
	Notes          string  `json:"notes"`
}

func (p *Plugin) analyzeUnseen(jc hostjobs.Context, h host.Host, auctionID string) error {
	lots, err := p.lotsNeedingAnalysis(jc, h, auctionID)
	if err != nil {
		return err
	}
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

func (p *Plugin) lotsNeedingAnalysis(jc hostjobs.Context, h host.Host, auctionID string) ([]lotRow, error) {
	rows, err := h.Store().Query(jc, `SELECT l.id, l.auction_id, l.url, l.title, l.lot_code, l.current_bid_cents
		FROM bidrl_lots l
		WHERE l.auction_id = ? AND NOT EXISTS (SELECT 1 FROM bidrl_analyses a WHERE a.lot_id = l.id)
		ORDER BY l.id`, auctionID)
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
	prompt := fmt.Sprintf(`Identify this auction lot from the photographs, not the title. Report why you identified it.
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
	parsed := analysisResult{Identification: lot.Title, Basis: "category_only", TitleAgreement: 0}
	if len(resp.Parsed) > 0 {
		_ = json.Unmarshal(resp.Parsed, &parsed)
	} else if trim := bytes.TrimSpace([]byte(resp.Text)); json.Valid(trim) {
		_ = json.Unmarshal(trim, &parsed)
	}
	parsed.Basis = normalizeBasis(parsed.Basis)
	if parsed.TitleAgreement < 0 {
		parsed.TitleAgreement = 0
	}
	if parsed.TitleAgreement > 1 {
		parsed.TitleAgreement = 1
	}
	bucket := bucketFor(parsed.Basis)
	now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
	return h.Store().Tx(jc, func(tx hoststorage.Tx) error {
		if _, err := tx.Exec(jc, `UPDATE bidrl_lots SET bucket = ? WHERE id = ?`, bucket, lot.ID); err != nil {
			return err
		}
		if err := h.Events().PublishTx(jc, tx, "lot.analyzed", lot.ID, map[string]any{
			"lotId": lot.ID, "auctionId": lot.AuctionID, "title": lot.Title,
			"identification": parsed.Identification, "basis": parsed.Basis, "bucket": bucket,
		}); err != nil {
			return err
		}
		_, err := tx.Exec(jc, `INSERT INTO bidrl_analyses(lot_id, identification, basis, model_or_sku, title_agreement, notes, input_tokens, output_tokens, cost_micro_usd, created_at, event_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
			lot.ID, parsed.Identification, parsed.Basis, parsed.ModelOrSKU, parsed.TitleAgreement, parsed.Notes,
			resp.Usage.InputTokens, resp.Usage.OutputTokens, int64(resp.Usage.CostMicroUSD), now)
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

func normalizeBasis(s string) string {
	switch s {
	case "exact_text", "barcode", "distinctive_visual_match", "product_family", "category_only":
		return s
	default:
		return "category_only"
	}
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
