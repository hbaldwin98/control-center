package bidrl

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hostai "github.com/hbaldwin98/control-center/host/ai"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
)

const (
	embedBatch     = 32
	minIntentScore = 0.30

	// A lot near one of the expanded product types counts, but never quite as
	// much as a lot near the words the user actually typed.
	intentRelatedWeight = 0.88
	// Weight of the keyword overlap folded on top of the vector score, so an
	// exact title or model hit outranks a merely thematic neighbour.
	intentLexicalWeight = 0.35
	// Vector scores only mean anything relative to each other, so the tail is cut
	// at a fraction of the best match rather than at a fixed cosine.
	intentRelativeFloor = 0.72
	// Descriptions are mostly boilerplate; past this the title stops carrying the
	// vector.
	maxDocDescription = 320
)

// lotDocument is what gets embedded for a lot. Title and identification come
// first and the free text is clipped: a long boilerplate description otherwise
// dominates the vector and buries what the lot actually is.
func lotDocument(c intentCard) string {
	parts := []string{c.Title, c.Identification, c.Model, c.Category, c.Terms,
		clipWords(c.Notes, maxDocDescription), clipWords(c.Description, maxDocDescription)}
	var b strings.Builder
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(p)
	}
	return b.String()
}

func clipWords(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	s = s[:max]
	if i := strings.LastIndexAny(s, " \n\t"); i > max/2 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func textHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func encodeVector(v []float64) []byte {
	buf := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(float32(x)))
	}
	return buf
}

func decodeVector(b []byte) []float64 {
	if len(b) < 4 || len(b)%4 != 0 {
		return nil
	}
	out := make([]float64, len(b)/4)
	for i := range out {
		out[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:])))
	}
	return out
}

func normalizeVector(v []float64) []float64 {
	var sum float64
	for _, x := range v {
		sum += x * x
	}
	if sum == 0 || math.IsNaN(sum) || math.IsInf(sum, 0) {
		return nil
	}
	inv := 1 / math.Sqrt(sum)
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}

func cosine(a, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var sum float64
	for i := range a {
		sum += a[i] * b[i]
	}
	if math.IsNaN(sum) || math.IsInf(sum, 0) {
		return 0
	}
	return sum
}

type storedEmbedding struct {
	hash   string
	dims   int
	vector []float64
}

func keepLotEmbedding(lotExists bool, endsAt string, now time.Time) bool {
	return lotExists && !hasEnded(endsAt, now)
}

func (p *Plugin) pruneLotEmbeddings(ctx context.Context, h host.Host) error {
	now := h.Clock().Now()
	rows, err := h.Store().Query(ctx, `SELECT e.lot_id, IFNULL(l.id,''), IFNULL(l.ends_at,'')
		FROM bidrl_lot_embeddings e
		LEFT JOIN bidrl_lots l ON l.id = e.lot_id`)
	if err != nil {
		return err
	}
	var drop []string
	for rows.Next() {
		var embedID, lotID, endsAt string
		if err := rows.Scan(&embedID, &lotID, &endsAt); err != nil {
			_ = rows.Close()
			return err
		}
		if !keepLotEmbedding(lotID != "", endsAt, now) {
			drop = append(drop, embedID)
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range drop {
		if _, err := h.Store().Exec(ctx, `DELETE FROM bidrl_lot_embeddings WHERE lot_id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

func (p *Plugin) loadLotEmbeddings(ctx context.Context, h host.Host) (map[string]storedEmbedding, error) {
	rows, err := h.Store().Query(ctx, `SELECT lot_id, text_hash, dims, vector FROM bidrl_lot_embeddings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]storedEmbedding{}
	for rows.Next() {
		var id, hash string
		var dims int
		var blob []byte
		if err := rows.Scan(&id, &hash, &dims, &blob); err != nil {
			return nil, err
		}
		vec := decodeVector(blob)
		if len(vec) != dims {
			continue
		}
		out[id] = storedEmbedding{hash: hash, dims: dims, vector: vec}
	}
	return out, rows.Err()
}

func (p *Plugin) clearLotEmbeddings(ctx context.Context, h host.Host) error {
	_, err := h.Store().Exec(ctx, `DELETE FROM bidrl_lot_embeddings`)
	return err
}

func (p *Plugin) upsertLotEmbeddings(ctx context.Context, h host.Host, cards []intentCard, vecs [][]float64, now string) error {
	if len(cards) != len(vecs) {
		return fmt.Errorf("bidrl: embedding count mismatch")
	}
	return h.Store().Tx(ctx, func(tx hoststorage.Tx) error {
		for i, c := range cards {
			n := normalizeVector(vecs[i])
			if n == nil {
				continue
			}
			if _, err := tx.Exec(ctx, `INSERT INTO bidrl_lot_embeddings(lot_id, text_hash, dims, vector, updated_at)
				VALUES (?, ?, ?, ?, ?)
				ON CONFLICT(lot_id) DO UPDATE SET text_hash = excluded.text_hash, dims = excluded.dims,
					vector = excluded.vector, updated_at = excluded.updated_at`,
				c.ID, textHash(lotDocument(c)), len(n), encodeVector(n), now); err != nil {
				return err
			}
		}
		return nil
	})
}

func (p *Plugin) embedTexts(ctx context.Context, h host.Host, inputs []string) ([][]float64, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	var out [][]float64
	for i := 0; i < len(inputs); i += embedBatch {
		end := i + embedBatch
		if end > len(inputs) {
			end = len(inputs)
		}
		resp, err := h.AI().Embed(ctx, hostai.EmbedRequest{Model: intentEmbedModel, Inputs: inputs[i:end]})
		if err != nil {
			return nil, err
		}
		if resp == nil || len(resp.Vectors) != end-i {
			return nil, fmt.Errorf("bidrl: embedding count mismatch")
		}
		out = append(out, resp.Vectors...)
	}
	return out, nil
}

// matchIntentSemantic ranks lots against the typed query and each expanded
// product type separately, then folds in the keyword overlap and keeps only
// the lots that stay close to the best one.
func (p *Plugin) matchIntentSemantic(jc hostjobs.Context, h host.Host, query string, words []string, cards []intentCard) ([]intentMatch, error) {
	probes := intentProbes(query, words)
	if len(probes) == 0 {
		return nil, fmt.Errorf("bidrl: intent has nothing to embed")
	}
	pvecs, err := p.embedTexts(jc, h, probes)
	if err != nil {
		return nil, err
	}
	type probe struct {
		text   string
		vector []float64
		weight float64
	}
	var qs []probe
	for i, v := range pvecs {
		n := normalizeVector(v)
		if n == nil {
			continue
		}
		weight := intentRelatedWeight
		if i == 0 {
			weight = 1
		}
		qs = append(qs, probe{text: probes[i], vector: n, weight: weight})
	}
	if len(qs) == 0 {
		return nil, fmt.Errorf("bidrl: empty query embedding")
	}
	dims := len(qs[0].vector)

	stored, err := p.loadLotEmbeddings(jc, h)
	if err != nil {
		return nil, err
	}
	for _, e := range stored {
		if e.dims != dims {
			if err := p.clearLotEmbeddings(jc, h); err != nil {
				return nil, err
			}
			stored = map[string]storedEmbedding{}
			break
		}
	}

	var stale []intentCard
	var staleIdx []int
	docs := make([]string, len(cards))
	for i, c := range cards {
		docs[i] = lotDocument(c)
		hash := textHash(docs[i])
		if e, ok := stored[c.ID]; ok && e.hash == hash && len(e.vector) == dims {
			continue
		}
		stale = append(stale, c)
		staleIdx = append(staleIdx, i)
	}
	if len(stale) > 0 {
		_ = jc.Logf("embedding %d lots", len(stale))
		staleDocs := make([]string, len(stale))
		for i, idx := range staleIdx {
			staleDocs[i] = docs[idx]
		}
		vecs, err := p.embedTexts(jc, h, staleDocs)
		if err != nil {
			return nil, err
		}
		now := h.Clock().Now().UTC().Format(time.RFC3339Nano)
		if err := p.upsertLotEmbeddings(jc, h, stale, vecs, now); err != nil {
			return nil, err
		}
		for i, c := range stale {
			n := normalizeVector(vecs[i])
			if n == nil {
				continue
			}
			stored[c.ID] = storedEmbedding{hash: textHash(docs[staleIdx[i]]), dims: len(n), vector: n}
		}
	}

	var matches []intentMatch
	var top float64
	for _, c := range cards {
		e, ok := stored[c.ID]
		if !ok || len(e.vector) != dims {
			continue
		}
		best, bestProbe := 0.0, ""
		for _, q := range qs {
			score := q.weight * cosine(q.vector, e.vector)
			if score > best {
				best, bestProbe = score, q.text
			}
		}
		if best < minIntentScore {
			continue
		}
		score := best + intentLexicalWeight*lexicalAgreement(words, c)
		if score > top {
			top = score
		}
		matches = append(matches, intentMatch{ID: c.ID, Score: score, Reason: intentReason(bestProbe, words, c)})
	}
	return trimIntentTail(matches, top), nil
}

// lexicalAgreement is the keyword overlap from the lexical pass squeezed into
// 0–1, so a lot whose title actually says the word beats a lot that only sits
// nearby in vector space.
func lexicalAgreement(words []string, c intentCard) float64 {
	score, _ := scoreIntentCard(words, c)
	if score <= 0 {
		return 0
	}
	if score >= lexicalFullMatch {
		return 1
	}
	return score / lexicalFullMatch
}

const lexicalFullMatch = 6

// trimIntentTail drops everything far behind the best match. Without it a
// fixed cosine cut lets most of the catalog through in an order the user reads
// as random.
func trimIntentTail(matches []intentMatch, top float64) []intentMatch {
	if len(matches) == 0 {
		return nil
	}
	floor := top * intentRelativeFloor
	out := matches[:0]
	for _, m := range matches {
		if m.Score < floor {
			continue
		}
		out = append(out, m)
	}
	return out
}

// intentReason names the words that connect the lot to the probe it matched.
func intentReason(query string, words []string, c intentCard) string {
	var qtoks []string
	qtoks = append(qtoks, words...)
	qtoks = append(qtoks, contentTokens(query)...)
	hay := strings.ToLower(strings.Join([]string{c.Title, c.Identification, c.Model, c.Category, c.Terms}, " "))
	var hit []string
	seen := map[string]struct{}{}
	for _, t := range qtoks {
		tl := strings.ToLower(t)
		if _, stop := intentStop[tl]; stop {
			continue
		}
		if _, ok := seen[tl]; ok {
			continue
		}
		if strings.Contains(hay, tl) {
			seen[tl] = struct{}{}
			hit = append(hit, t)
		}
	}
	if len(hit) > 0 {
		return strings.Join(hit, ", ")
	}
	if c.Identification != "" {
		return "similar to " + c.Identification
	}
	if c.Title != "" {
		return "similar to " + c.Title
	}
	return "similar meaning"
}
