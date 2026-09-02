package bidrl_test

import (
	"encoding/json"
	"hash/fnv"
	"math"
	"strings"

	hostai "github.com/hbaldwin98/control-center/host/ai"
	"github.com/hbaldwin98/control-center/host/hosttest"
	hostsearch "github.com/hbaldwin98/control-center/host/search"
)

// This file is bidrl's stand-in for the models it asks the host to call.
//
// The replies have to depend on the request: the plugin sends a different schema for
// each task and reads the answer back through a typed struct, so a fixed string proves
// nothing. Every schema and prompt shape it keys on is defined in this package, which
// is why the knowledge belongs here. It used to live in the core AI fake, where core
// had to know what "price_cents", "basis", "item_words", and "matches" meant to a
// plugin it is not supposed to know anything about.
//
// These answers are plausible, not real. They exist so the pipeline around the model
// -- prompt assembly, parsing, storage, scoring -- is exercised end to end.

// installModels programs every route the plugin declares.
func installModels(ai *hosttest.AIFake) {
	// One function per route, dispatching on the schema, because a route can carry
	// more than one task.
	ai.AnswerChat("cheap-vision", answerBySchema)
	ai.AnswerChat("cheap-chat", answerBySchema)
	ai.AnswerChat("grounded-price", answerBySchema)
	ai.AnswerEmbed("intent-match", answerEmbed)
}

// installSearch answers the web lookups the pricing path makes. The plugin builds these
// queries from what it identified in a lot, so they cannot be programmed by exact
// string; the shape of the results is what its price parser reads.
func installSearch(s *hosttest.SearchFake) {
	s.Answer(func(req hostsearch.Request) ([]hostsearch.Hit, error) {
		q := strings.ToLower(req.Query)
		switch {
		case strings.Contains(q, "keurig"), strings.Contains(q, "k-supreme"), strings.Contains(q, "k supreme"):
			return []hostsearch.Hit{{
				URL:     "https://www.ebay.com/itm/k-supreme-plus",
				Title:   "Keurig K-Supreme Plus sold listing",
				Snippet: "Sold listing for K-Supreme Plus at $129 used.",
			}}, nil
		case strings.Contains(q, "dewalt"), strings.Contains(q, "dcd791"):
			return []hostsearch.Hit{{
				URL:     "https://www.ebay.com/itm/dewalt-dcd791",
				Title:   "DeWalt DCD791 used drill",
				Snippet: "Asking $89 for a used DeWalt DCD791 20V drill.",
			}}, nil
		}
		// Anything else finds a page with no price on it, which is the case the plugin
		// has to survive without inventing a valuation.
		return []hostsearch.Hit{{
			URL:     "https://example-market.test/search",
			Title:   "Market listings",
			Snippet: "No exact model match in fixtures.",
		}}, nil
	})
}

// answerBySchema picks a synthesizer by what the request asked for. The ordering
// matches the plugin's own: the most specific schema wins.
func answerBySchema(req hostai.ChatRequest) (*hostai.ChatResponse, error) {
	prompt := promptText(req)
	schema := string(req.Schema)
	switch {
	case strings.Contains(schema, "price_cents"):
		// The pricing prompt lists the search results it will accept, and the plugin
		// rejects an answer that cites anything else. Echoing back the first result's
		// URL is what a well-behaved model would do.
		return jsonReply(fakePrice(prompt))
	case req.Grounding != nil:
		return answerGroundedPrice(req)
	case strings.Contains(schema, "basis"):
		return jsonReply(fakeAnalysis(prompt))
	case strings.Contains(schema, "item_words"):
		return jsonReply(fakeIntentExpand(prompt))
	case strings.Contains(schema, `"matches"`):
		return jsonReply(fakeIntentMatches(prompt))
	case schema != "":
		return jsonReply(map[string]any{"ok": true})
	}
	return &hostai.ChatResponse{
		Text:   "echo: " + strings.TrimSpace(prompt),
		Usage:  hostai.Usage{InputTokens: 1, OutputTokens: 1, Attempts: 1},
		Finish: "stop",
	}, nil
}

// answerGroundedPrice returns a priced answer with the citation and source a grounded
// route is expected to attach, so the plugin's evidence handling is exercised.
func answerGroundedPrice(req hostai.ChatRequest) (*hostai.ChatResponse, error) {
	prompt := promptText(req)
	model := fieldAfter(prompt, "Model:")
	if model == "" {
		model = fieldAfter(prompt, "model_or_sku")
	}
	if model == "" {
		model = "K-Supreme Plus"
	}
	text := "Sold listing for " + model + " at $129 used."
	parsed, err := json.Marshal(map[string]any{
		"price_cents": 12900, "currency": "USD", "condition": "used",
		"kind": "sold", "model_or_code": model,
	})
	if err != nil {
		return nil, err
	}
	return &hostai.ChatResponse{
		Text:      text,
		Parsed:    parsed,
		Citations: []hostai.Citation{{Start: 0, End: len(text), Source: 0}},
		Sources: []hostai.Source{{
			URL:   "https://example-market.test/" + slug(model),
			Title: model + " sold listing",
		}},
		Usage:  hostai.Usage{InputTokens: 10, OutputTokens: 10, Attempts: 1},
		Finish: "stop",
	}, nil
}

func fakeAnalysis(prompt string) map[string]any {
	title := fieldAfter(prompt, "Title:")
	out := map[string]any{
		"identification": title, "basis": "category_only", "model_or_sku": "",
		"category": "other", "search_terms": []string{},
		"title_agreement": 0.9, "notes": "fake analysis",
	}
	lower := strings.ToLower(title)
	switch {
	case strings.Contains(lower, "keurig") || strings.Contains(strings.ToLower(prompt), "k-supreme"):
		// A legible model number is the strongest identification the plugin can get.
		out["identification"] = "Keurig K-Supreme Plus"
		out["basis"] = "exact_text"
		out["model_or_sku"] = "K-Supreme Plus"
		out["category"] = "appliances"
		out["search_terms"] = []string{"keurig", "coffee maker"}
		out["title_agreement"] = 0.85
		out["notes"] = "Model number is legible on the machine."
	case strings.Contains(lower, "aeron"):
		// Recognisable product, no model plate: a weaker basis, and the title and the
		// photos disagree.
		out["identification"] = "Herman Miller Aeron-like mesh chair"
		out["basis"] = "distinctive_visual_match"
		out["category"] = "furniture"
		out["search_terms"] = []string{"herman miller", "aeron"}
		out["title_agreement"] = 0.2
		out["notes"] = "Looks like an Aeron; no model plate readable."
	case strings.Contains(lower, "chair"):
		out["category"] = "furniture"
	}
	return out
}

func fakePrice(prompt string) map[string]any {
	model := fieldAfter(prompt, "Model:")
	if model == "" {
		model = "K-Supreme Plus"
	}
	srcURL := fieldAfter(prompt, "URL:")
	if !strings.HasPrefix(srcURL, "https://") {
		srcURL = "https://example-market.test/" + slug(model)
	}
	kind, cents := "sold", int64(12900)
	if strings.Contains(strings.ToLower(prompt), "asking") {
		kind, cents = "asking", 8900
	}
	return map[string]any{
		"price_cents": cents, "currency": "USD", "condition": "used",
		"kind": kind, "model_or_code": model, "source_url": srcURL,
	}
}

// intentGroups are the word clusters the fake treats as one meaning. A real model
// would infer these; the fake hard-codes enough of them to drive the intent pipeline.
var intentGroups = [][]string{
	{"coffee", "brew", "keurig", "espresso", "cafe"},
	{"sit", "seat", "seating", "chair", "desk", "office", "aeron"},
	{"camp", "camping", "tent", "stove", "lantern", "cooler", "backpack", "shelter",
		"headlamp", "flashlight", "canopy", "sleeping"},
}

func fakeIntentExpand(prompt string) map[string]any {
	intent := strings.ToLower(fieldAfter(prompt, "Intent:"))
	words := []string{}
	for _, g := range intentGroups {
		if groupHits(intent, g) {
			words = append(words, g...)
		}
	}
	return map[string]any{"item_words": words}
}

func fakeIntentMatches(prompt string) map[string]any {
	intent := strings.ToLower(fieldAfter(prompt, "Intent:"))
	lotsPart := prompt
	if i := strings.Index(prompt, "Lots:"); i >= 0 {
		lotsPart = prompt[i+len("Lots:"):]
	}
	var active [][]string
	for _, g := range intentGroups {
		if groupHits(intent, g) {
			active = append(active, g)
		}
	}
	matches := []map[string]any{}
	for line := range strings.SplitSeq(lotsPart, "\n") {
		id, hay, ok := parseIntentLot(strings.TrimSpace(line))
		if !ok {
			continue
		}
		for _, g := range active {
			if groupHits(hay, g) {
				matches = append(matches, map[string]any{
					"id": id, "score": 0.86, "reason": "serves the stated intent",
				})
				break
			}
		}
	}
	return map[string]any{"matches": matches}
}

// answerEmbed produces a deterministic vector per input. Nearby text gets nearby
// vectors because the tokens overlap, which is all the plugin's ranking needs.
func answerEmbed(req hostai.EmbedRequest) (*hostai.EmbedResponse, error) {
	vectors := make([][]float64, 0, len(req.Inputs))
	for _, in := range req.Inputs {
		vectors = append(vectors, embedVector(in))
	}
	return &hostai.EmbedResponse{
		Vectors: vectors,
		Usage:   hostai.Usage{InputTokens: int64(len(req.Inputs)), Attempts: 1},
	}, nil
}

const (
	embedDims    = 48
	conceptCount = 8
)

// concepts map tokens onto a few shared axes, so a search for "coffee" still retrieves
// a Keurig the way a real embedding model would. A hashed tail keeps unrelated text
// apart. The axes are weighted well above the tail because the plugin ranks on cosine
// similarity, and a shared concept has to outweigh incidental word overlap.
var concepts = map[string]int{
	"coffee": 0, "keurig": 0, "espresso": 0, "brew": 0, "kcup": 0, "k-cup": 0,
	"k-supreme": 0, "ksupreme": 0,
	"camp": 1, "camping": 1, "tent": 1, "stove": 1, "cooler": 1, "lantern": 1,
	"sleeping": 1, "backpack": 1, "hike": 1, "hiking": 1, "outdoor": 1,
	"headlamp": 1, "flashlight": 1, "canopy": 1, "popup": 1, "shelter": 1,
	"chair": 2, "chairs": 2, "seating": 2, "seat": 2, "sit": 2, "desk": 2, "office": 2,
	"mesh": 2, "herman": 2, "miller": 2, "aeron": 2, "stool": 2,
}

func embedVector(text string) []float64 {
	v := make([]float64, embedDims)
	for _, tok := range embedTokens(text) {
		if dim, ok := concepts[tok]; ok {
			v[dim] += 4
		}
		v[conceptCount+int(tokenHash(tok)%uint32(embedDims-conceptCount))]++
	}
	var sum float64
	for _, x := range v {
		sum += x * x
	}
	if sum == 0 {
		v[0] = 1
		return v
	}
	inv := 1 / math.Sqrt(sum)
	for i := range v {
		v[i] *= inv
	}
	return v
}

// embedTokens splits on anything that is not a letter, digit, or hyphen, and also emits
// the parts of a hyphenated token so "k-supreme" matches "supreme".
func embedTokens(s string) []string {
	s = strings.ToLower(s)
	var out []string
	var b strings.Builder
	flush := func() {
		t := b.String()
		b.Reset()
		if t == "" {
			return
		}
		out = append(out, t)
		if strings.Contains(t, "-") {
			for _, part := range strings.Split(t, "-") {
				if part != "" {
					out = append(out, part)
				}
			}
		}
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return out
}

func tokenHash(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

func groupHits(s string, group []string) bool {
	for _, w := range group {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// parseIntentLot reads one line of the lot list the plugin builds into its prompt.
func parseIntentLot(line string) (id, hay string, ok bool) {
	parts := strings.Split(line, " | ")
	if len(parts) < 3 {
		return "", "", false
	}
	id = strings.TrimSpace(parts[0])
	if id == "" || strings.ContainsAny(id, " \t") {
		return "", "", false
	}
	rest := parts[1:]
	if parts[1] == "photos" || parts[1] == "listing" {
		rest = parts[2:]
	}
	return id, strings.ToLower(strings.Join(rest, " ")), true
}

// fieldAfter reads the value the plugin wrote after a "Label:" line in its prompt.
func fieldAfter(s, label string) string {
	i := strings.Index(s, label)
	if i < 0 {
		return strings.TrimSpace(s)
	}
	rest := s[i+len(label):]
	if j := strings.IndexAny(rest, "\n\r"); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}

func promptText(req hostai.ChatRequest) string {
	var b strings.Builder
	for _, m := range req.Messages {
		b.WriteString(m.Text)
		b.WriteString("\n")
	}
	return b.String()
}

func slug(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), " ", "-")
}

func jsonReply(v map[string]any) (*hostai.ChatResponse, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return &hostai.ChatResponse{
		Text:   string(raw),
		Parsed: raw,
		Usage:  hostai.Usage{InputTokens: 10, OutputTokens: int64(len(raw)) / 4, Attempts: 1},
		Finish: "stop",
	}, nil
}
